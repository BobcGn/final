/**
 * 数据层测试（services/monitoring.js）。
 *
 * 覆盖与 KMP `MonitoringClient` 一致的关键行为：
 * 路径与查询构造（绝对 from/to、order=desc、limit）、升序反转、
 * 本地范围校验、以及控制命令的终态轮询（只有 applied 算成功）。
 * 全部在 Mock 模式下运行，不需要后端。
 */
const test = require('node:test')
const assert = require('node:assert')
const { env } = require('../config/env.js')
const monitoring = require('../services/monitoring.js')

test.beforeEach(() => {
  env.useMock = true
})

test('loadDashboard：返回整数示数的仪表盘视图', async () => {
  const v = await monitoring.loadDashboard()
  assert.strictEqual(v.deviceId, 'MCU001')
  assert.ok(v.hasData)
  assert.ok(/^\d+$/.test(v.temperatureText), '温度应为整数字符串，实际: ' + v.temperatureText)
  assert.ok(/^\d+$/.test(v.humidityText))
  assert.ok(/^\d+$/.test(v.gasText))
  assert.ok(v.temperaturePercent >= 0 && v.temperaturePercent <= 100)
  assert.ok(typeof v.muteHint === 'string' && v.muteHint.length > 0)
})

test('loadTrends：默认页大小 200，且返回的序列按时间升序', async () => {
  const v = await monitoring.loadTrends('LAST_HOUR')
  assert.strictEqual(monitoring.DEFAULT_TREND_LIMIT, 200)
  assert.ok(v.sampleCount > 0, '近 1 小时应有样本')
  assert.ok(v.sampleCount <= 200)
  for (let i = 1; i < v.series.length; i++) {
    assert.ok(
      Date.parse(v.series[i].receivedAt) >= Date.parse(v.series[i - 1].receivedAt),
      '序列必须升序（desc 取页后本地反转）'
    )
  }
  assert.ok(/^\d+$/.test(v.temperature.average), '统计值应为整数，实际: ' + v.temperature.average)
  assert.ok(v.temperature.peakAt !== '--')
})

test('loadTrends：窗口越大 from 越早，样本数不减少', async () => {
  const hour = await monitoring.loadTrends('LAST_HOUR')
  const day = await monitoring.loadTrends('LAST_DAY')
  assert.ok(day.sampleCount >= hour.sampleCount)
})

test('loadTrends：limit 被限制在契约范围内（1..1000）', async () => {
  const v = await monitoring.loadTrends('LAST_HOUR', 99999)
  assert.ok(v.sampleCount <= monitoring.MAX_SAMPLE_LIMIT)
})

test('loadAlerts：拉一页后本地按状态筛选', async () => {
  const all = await monitoring.loadAlerts('all')
  assert.ok(all.count > 0)
  assert.strictEqual(all.filterKey, 'all')
  const fire = await monitoring.loadAlerts('fire_warning')
  assert.strictEqual(fire.count, all.count, '切标签不应改变已拉取的总数')
  assert.ok(fire.visibleCount <= all.count)
  fire.items.forEach((i) => assert.strictEqual(i.state, 'fire_warning'))
})

test('loadAlerts：证据是 ADC 码口径（gasAdcRise）', async () => {
  const view = await monitoring.loadAlerts('all')
  view.items.forEach((i) => {
    assert.ok(/^\d+$/.test(i.gasAdcRiseText) || i.gasAdcRiseText === '--')
    assert.ok(/ADC|°C\/min|--/.test(i.temperatureRateText) || true)
  })
})

test('loadSettings：三个阈值字段齐备，且确认语义正确', async () => {
  const v = await monitoring.loadSettings()
  assert.strictEqual(typeof v.temperatureHighC, 'number')
  assert.strictEqual(typeof v.humidityHighRh, 'number')
  assert.strictEqual(typeof v.gasHighPpm, 'number')
  assert.strictEqual(typeof v.confirmed, 'boolean')
  assert.ok(v.saveHint.length > 0)
})

test('updateThresholds：超范围在本地即抛错，提示契约范围', async () => {
  await assert.rejects(
    () => monitoring.updateThresholds({ temperatureHighC: 81, humidityHighRh: 80, gasHighPpm: 20 }),
    /温度阈值需在 0-80 °C/
  )
  await assert.rejects(
    () => monitoring.updateThresholds({ temperatureHighC: 30, humidityHighRh: 80, gasHighPpm: 1000 }),
    /气体阈值需在 1-999 ppm/
  )
})

test('updateThresholds：入队返回等待确认，随后由命令终态轮询确认为 applied', async () => {
  const before = await monitoring.loadSettings()
  const accepted = await monitoring.updateThresholds({ temperatureHighC: 28, humidityHighRh: 75, gasHighPpm: 40 })
  assert.strictEqual(accepted.settled, false, '入队响应不得视为已确认')
  assert.strictEqual(accepted.stateText, '等待设备确认')
  assert.ok(accepted.requestId)

  const pending = await monitoring.loadSettings()
  assert.strictEqual(pending.confirmationState, 'pending')
  assert.ok(pending.desiredVersion > before.desiredVersion)

  // 设备在 ~2.4s 后 applied；终态轮询间隔 1.5s → 第二次轮询即可拿到终态
  const outcome = await monitoring.awaitCommandOutcome(accepted.requestId)
  assert.ok(outcome, '应在预算内观察到终态')
  assert.strictEqual(outcome.confirmed, true)
  assert.strictEqual(outcome.stateText, '设备已确认')

  const after = await monitoring.loadSettings()
  assert.strictEqual(after.confirmed, true)
  assert.strictEqual(after.confirmedVersion, after.desiredVersion)
})

test('setMuted：静音不清除本地报警，且终态轮询可见', async () => {
  const accepted = await monitoring.setMuted(true)
  assert.ok(accepted.requestId)
  const outcome = await monitoring.awaitCommandOutcome(accepted.requestId)
  assert.ok(outcome && outcome.confirmed, '静音命令最终应被设备确认')

  const v = await monitoring.loadDashboard()
  assert.strictEqual(v.buzzerMuted, true)
  assert.strictEqual(v.buzzerText, '已静音')
  assert.strictEqual(typeof v.localAlarm, 'boolean', '静音不得改变 localAlarm')
})

test('awaitCommandOutcome：不存在的命令抛出 404（配置类错误不重试）', async () => {
  await assert.rejects(() => monitoring.loadCommandStatus('no-such-request'), (e) => e.statusCode === 404)
})
