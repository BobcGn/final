/**
 * 共享展示层测试（utils/presentation.js）。
 *
 * 逐项验证与 KMP `MonitoringPresentation` 一致的派生规则：
 * 整数示数、空态、报警文案、告警证据口径、阈值确认语义与命令终态语义。
 */
const test = require('node:test')
const assert = require('node:assert')
const p = require('../utils/presentation.js')

const status = (over) =>
  Object.assign(
    {
      deviceId: 'MCU001',
      connectivity: 'online',
      alarmState: 'normal',
      localAlarm: false,
      buzzerMuted: false,
      lastSeenAt: '2026-09-23T07:00:00Z',
      thresholdVersion: { desired: 4, confirmed: 4 },
    },
    over
  )

const telemetry = (over) =>
  Object.assign(
    {
      deviceId: 'MCU001',
      receivedAt: '2026-09-23T07:11:41Z',
      temperatureC: 26.1,
      humidityRh: 61.4,
      gasAdcRaw: 1350,
      gasAdcFiltered: 1328,
      gasPpm: 25.6,
      gasCalibrated: true,
      localAlarm: false,
      alarmCauses: [],
      buzzerMuted: false,
      network: 'online',
      sensorFault: false,
      sequence: 42,
      timestamp: '2026-09-23T07:11:41Z',
    },
    over
  )

/* ---------------- dashboard ---------------- */

test('dashboard：三个示数都是整数文本', () => {
  const v = p.dashboard(status(), telemetry())
  assert.strictEqual(v.temperatureText, '26')
  assert.strictEqual(v.humidityText, '61')
  assert.strictEqual(v.gasText, '26')
  assert.ok(!/\./.test(v.temperatureText + v.humidityText + v.gasText), '不得出现小数点')
})

test('dashboard：百分比按契约量程（80 °C / 100 %RH / 999 ppm）', () => {
  const v = p.dashboard(status(), telemetry({ temperatureC: 40, humidityRh: 61, gasPpm: 500 }))
  assert.strictEqual(v.temperaturePercent, 50)
  assert.strictEqual(v.humidityPercent, 61)
  assert.strictEqual(v.gasPercent, 50)
})

test('dashboard：无有效遥测时是空态，指标显示 -- 而状态仍真实', () => {
  const v = p.dashboard(status({ alarmState: 'suspect', connectivity: 'offline' }), null)
  assert.strictEqual(v.hasData, false)
  assert.strictEqual(v.temperatureText, '--')
  assert.strictEqual(v.gasText, '--')
  assert.strictEqual(v.gasAvailable, false)
  assert.strictEqual(v.riskText, '疑似异常')
  assert.strictEqual(v.connectivityText, '离线')
  assert.strictEqual(v.online, false)
})

test('dashboard：气体未标定（gasPpm 为 null）显示 -- 而不是 0', () => {
  const v = p.dashboard(status(), telemetry({ gasPpm: null, gasCalibrated: false }))
  assert.strictEqual(v.gasText, '--')
  assert.strictEqual(v.gasAvailable, false)
  assert.strictEqual(v.gasPercent, 0)
})

test('dashboard：四种风险状态的文案与 tone', () => {
  assert.deepStrictEqual(
    [p.dashboard(status({ alarmState: 'normal' }), telemetry()).riskText,
     p.dashboard(status({ alarmState: 'suspect' }), telemetry()).riskText,
     p.dashboard(status({ alarmState: 'fire_warning' }), telemetry()).riskText,
     p.dashboard(status({ alarmState: 'recovered' }), telemetry()).riskText],
    ['环境正常', '疑似异常', '火情预警', '指标已恢复']
  )
  assert.strictEqual(p.dashboard(status({ alarmState: 'fire_warning' }), telemetry()).riskTone, 'danger')
  assert.strictEqual(p.dashboard(status({ alarmState: 'suspect' }), telemetry()).riskTone, 'warning')
  assert.strictEqual(p.dashboard(status({ alarmState: 'recovered' }), telemetry()).riskTone, 'info')
})

test('dashboard：未知告警状态回退到正常，不崩', () => {
  const v = p.dashboard(status({ alarmState: 'something_new' }), telemetry())
  assert.strictEqual(v.riskLevel, 'normal')
  assert.strictEqual(v.riskText, '环境正常')
})

test('dashboard：蜂鸣器三种文案（已静音 / 报警策略生效 / 待机）', () => {
  assert.strictEqual(p.dashboard(status(), telemetry({ buzzerMuted: true })).buzzerText, '已静音')
  assert.strictEqual(p.dashboard(status({ localAlarm: true }), telemetry({ localAlarm: true })).buzzerText, '报警策略生效')
  assert.strictEqual(p.dashboard(status(), telemetry()).buzzerText, '待机')
})

test('dashboard：本地报警文案与回退（遥测缺失时用 status 的值）', () => {
  assert.strictEqual(p.dashboard(status({ localAlarm: true }), null).localAlarmText, '报警中')
  assert.strictEqual(p.dashboard(status({ localAlarm: true }), null).localAlarm, true)
})

test('dashboard：更新时间优先用 receivedAt，其次 lastSeenAt', () => {
  assert.strictEqual(p.dashboard(status(), telemetry()).updatedAt, '2026-09-23T07:11:41Z')
  assert.strictEqual(p.dashboard(status({ lastSeenAt: null }), null).updatedAt, '--')
})

/* ---------------- trends ---------------- */

function sample(i, over) {
  return Object.assign(
    {
      deviceId: 'MCU001',
      receivedAt: new Date(Date.UTC(2026, 8, 23, 6, i, 0)).toISOString(),
      temperatureC: 24 + i,
      humidityRh: 50 + i,
      gasPpm: 20 + i,
      localAlarm: false,
      sequence: i,
      bootId: 'boot-a1',
    },
    over
  )
}

test('trends：统计值为整数且 peakAt 取最大值样本时间', () => {
  const v = p.trends([sample(0), sample(1), sample(2)], 'LAST_HOUR')
  assert.strictEqual(v.temperature.minimum, '24')
  assert.strictEqual(v.temperature.maximum, '26')
  assert.strictEqual(v.temperature.average, '25')
  assert.strictEqual(v.temperature.peakAt, '06:02:00')
  assert.strictEqual(v.sampleCount, 3)
  assert.strictEqual(v.hasData, true)
})

test('trends：气体缺读数的样本被跳过，样本数单独暴露', () => {
  const v = p.trends([sample(0), sample(1, { gasPpm: null }), sample(2)], 'LAST_HOUR')
  assert.strictEqual(v.sampleCount, 3)
  assert.strictEqual(v.gasSampleCount, 2)
  assert.strictEqual(v.gas.minimum, '20')
  assert.strictEqual(v.gas.maximum, '22')
})

test('trends：空样本时统计为 -- 且 hasData 为 false', () => {
  const v = p.trends([], 'LAST_HOUR')
  assert.strictEqual(v.hasData, false)
  assert.strictEqual(v.temperature.minimum, '--')
  assert.strictEqual(v.gasSampleCount, 0)
})

test('trends：窗口只决定高亮与标签，不二次过滤样本', () => {
  const points = [sample(0), sample(1)]
  const v = p.trends(points, 'LAST_DAY')
  assert.strictEqual(v.sampleCount, 2)
  assert.strictEqual(v.windowKey, 'LAST_DAY')
  assert.strictEqual(v.windowLabel, '近24小时')
})

test('trends：曲线占位文案与图例 tone 来自共享层', () => {
  const v = p.trends([sample(0)], 'LAST_HOUR')
  assert.strictEqual(v.curveReady, false)
  assert.strictEqual(v.curveMaskTitle, '趋势曲线即将上线')
  assert.strictEqual(v.footerHint, '统计基于所选区间内的真实历史样本计算')
  assert.deepStrictEqual(
    v.curveLegend.map((l) => l.tone),
    ['danger', 'info', 'mint']
  )
})

/* ---------------- alerts ---------------- */

const event = (over) =>
  Object.assign(
    {
      id: 'e1',
      deviceId: 'MCU001',
      state: 'fire_warning',
      startedAt: '2026-09-23T06:00:00Z',
      endedAt: null,
      evidence: {
        gasAdcRise: 1870,
        gasAdcRiseThreshold: 1500,
        temperatureRateCPerMinute: 4.2,
        temperatureRateThresholdCPerMinute: 3.0,
        sampleCount: 8,
        windowSeconds: 60,
      },
    },
    over
  )

test('alerts：按四种状态筛选，标签文案与 KMP 一致', () => {
  const list = [
    event(),
    event({ id: 'e2', state: 'suspect' }),
    event({ id: 'e3', state: 'recovered', endedAt: '2026-09-23T06:30:00Z' }),
  ]
  assert.strictEqual(p.alerts(list, 'all').visibleCount, 3)
  assert.strictEqual(p.alerts(list, 'fire_warning').visibleCount, 1)
  assert.strictEqual(p.alerts(list, 'suspect').items[0].stateText, '疑似异常')
  assert.deepStrictEqual(
    p.alerts(list, 'all').filters.map((f) => f.label),
    ['全部', '火情', '疑似', '已恢复']
  )
})

test('alerts：证据用 ADC 码口径，速率保留小数并带单位', () => {
  const item = p.alerts([event()], 'all').items[0]
  assert.strictEqual(item.gasAdcRiseText, '1870')
  assert.strictEqual(item.gasAdcRiseThresholdText, '1500')
  assert.strictEqual(item.temperatureRateText, '4.2 °C/min')
  assert.strictEqual(item.temperatureRateThresholdText, '3 °C/min')
  assert.strictEqual(item.sampleCountText, '8')
  assert.strictEqual(item.windowSecondsText, '60')
})

test('alerts：未结束的事件是 active，结束时间缺失显示 --', () => {
  const active = p.alerts([event()], 'all').items[0]
  assert.strictEqual(active.active, true)
  assert.strictEqual(active.endedAt, '--')
  const closed = p.alerts([event({ endedAt: '2026-09-23T06:30:00Z' })], 'all').items[0]
  assert.strictEqual(closed.active, false)
})

test('alerts：证据缺失的字段显示 -- 而不是 0', () => {
  const item = p.alerts([event({ evidence: { gasAdcRise: 5, sampleCount: 1 } })], 'all').items[0]
  assert.strictEqual(item.gasAdcRiseThresholdText, '--')
  assert.strictEqual(item.temperatureRateText, '--')
  assert.strictEqual(item.temperatureRateThresholdText, '--')
  assert.strictEqual(item.windowSecondsText, '--')
})

/* ---------------- settings / commands ---------------- */

test('settings：confirmed 要求确认版本不低于期望版本', () => {
  const awaiting = p.settings({
    desiredVersion: 5,
    confirmedVersion: 4,
    temperatureHighC: 30,
    humidityHighRh: 80,
    gasHighPpm: 20,
    updatedAt: '2026-09-23T07:00:00Z',
    confirmationState: 'confirmed',
  })
  assert.strictEqual(awaiting.confirmed, false)
  assert.strictEqual(awaiting.awaitingDevice, true)
  assert.strictEqual(awaiting.confirmationText, '设备已确认')

  const ok = p.settings({
    desiredVersion: 5,
    confirmedVersion: 5,
    temperatureHighC: 30,
    humidityHighRh: 80,
    gasHighPpm: 20,
    updatedAt: null,
    confirmationState: 'confirmed',
  })
  assert.strictEqual(ok.confirmed, true)
  assert.strictEqual(ok.updatedAt, '--')
})

test('settings：四种确认状态的文案与 tone', () => {
  const base = { desiredVersion: 1, confirmedVersion: 1, temperatureHighC: 30, humidityHighRh: 80, gasHighPpm: 20 }
  assert.strictEqual(p.settings(Object.assign({ confirmationState: 'pending' }, base)).confirmationTone, 'warning')
  assert.strictEqual(p.settings(Object.assign({ confirmationState: 'rejected' }, base)).confirmationTone, 'danger')
  assert.strictEqual(
    p.settings(Object.assign({ confirmationState: 'timed_out' }, base)).confirmationText,
    '确认超时，请重试'
  )
})

test('commandAccepted：入队响应永远是等待确认，不算成功', () => {
  const v = p.commandAccepted({ requestId: 'r1', status: 'pending', desiredVersion: 5, expiresAt: 'x' })
  assert.strictEqual(v.stateText, '等待设备确认')
  assert.strictEqual(v.settled, false)
  assert.strictEqual(v.confirmed, false)
  assert.strictEqual(v.versionText, '5')
})

test('commandStatus：只有 applied 算成功，超时/拒绝/重复各有文案', () => {
  const applied = p.commandStatus({ requestId: 'r1', state: 'applied', confirmedVersion: 5 })
  assert.strictEqual(applied.confirmed, true)
  assert.strictEqual(applied.settled, true)
  assert.strictEqual(applied.tone, 'mint')

  const timeout = p.commandStatus({ requestId: 'r1', state: 'timed_out' })
  assert.strictEqual(timeout.confirmed, false)
  assert.strictEqual(timeout.failed, true)
  assert.strictEqual(timeout.stateText, '设备确认超时')

  const dup = p.commandStatus({ requestId: 'r1', state: 'duplicate' })
  assert.strictEqual(dup.failed, false)
  assert.strictEqual(dup.tone, 'info')

  const published = p.commandStatus({ requestId: 'r1', state: 'published' })
  assert.strictEqual(published.settled, false)
  assert.strictEqual(published.tone, 'warning')
})

test('selectors：两份选项来自共享层且顺序固定', () => {
  const s = p.selectors()
  assert.deepStrictEqual(
    s.windows.map((w) => w.key),
    ['LAST_HOUR', 'LAST_SIX_HOURS', 'LAST_DAY']
  )
  assert.strictEqual(s.filters[0].key, 'all')
  assert.strictEqual(p.windowHours('LAST_DAY'), 24)
})

test('validate：三个字段的范围校验与提示文案', () => {
  const ok = { temperatureHighC: 30, humidityHighRh: 80, gasHighPpm: 20 }
  assert.doesNotThrow(() => p.validate(ok))
  assert.throws(() => p.validate(Object.assign({}, ok, { temperatureHighC: 81 })), /温度阈值需在 0-80 °C/)
  assert.throws(() => p.validate(Object.assign({}, ok, { humidityHighRh: 101 })), /湿度阈值需在 0-100 %RH/)
  assert.throws(() => p.validate(Object.assign({}, ok, { gasHighPpm: 0 })), /气体阈值需在 1-999 ppm/)
})
