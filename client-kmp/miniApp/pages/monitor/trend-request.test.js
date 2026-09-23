/**
 * 趋势请求竞态门闩的单测。
 *
 * 这些用例直接驱动 createRequestGate —— monitor.js 里「响应是否仍是最新意图」
 * 的唯一判定逻辑 —— 以覆盖真实会话中的乱序/卸载/切页场景，而不需要伪造
 * 不可执行的 Page 对象假实现。
 */

const test = require('node:test')
const assert = require('node:assert/strict')
const { createRequestGate } = require('./trend-request.js')

test('aThenBWithBFinishingFirstKeepsOnlyB', () => {
  const gate = createRequestGate()
  // 用户点了「近1小时」，请求 A 发出
  const a = gate.beginTrend('LAST_HOUR')
  // 用户立刻又点「近6小时」，请求 B 发出
  const b = gate.beginTrend('LAST_SIX_HOURS')

  // B 先返回：此刻仍是最新意图
  assert.equal(gate.isTrendCurrent(b, 'LAST_SIX_HOURS'), true)
  // 页面此时应只写入 B 的数据

  // A 后返回：已过期，必须被丢弃，不得覆盖 B
  assert.equal(gate.isTrendCurrent(a, 'LAST_HOUR'), false)
  assert.equal(gate.isTrendCurrent(a, 'LAST_SIX_HOURS'), false)
})

test('aStaleRequestCannotOverwriteANewerSuccess', () => {
  const gate = createRequestGate()
  const a = gate.beginTrend('LAST_HOUR')
  const b = gate.beginTrend('LAST_DAY')

  // B 成功落库后，A 的迟到失败/成功都不得改写页面
  assert.equal(gate.isTrendCurrent(a, 'LAST_HOUR'), false)
  assert.equal(gate.isTrendCurrent(a), false)
  // B 自己仍有效
  assert.equal(gate.isTrendCurrent(b, 'LAST_DAY'), true)
})

test('switchingTabBeforeCompletionRejectsTheTrendResponse', () => {
  const gate = createRequestGate()
  const trendVersion = gate.beginTrend('LAST_HOUR')

  // 趋势请求还在路上，用户切到了告警页（tab 2）
  gate.beginLoad(2)

  // 趋势响应不得再改写 trends / loading / error / legend / 轴标签 / Canvas
  assert.equal(gate.isTrendCurrent(trendVersion, 'LAST_HOUR'), false)
  // 当前活跃的是告警页的加载版本，而非趋势版本
  assert.equal(gate.isLoadCurrent(trendVersion, 2), false)
})

test('switchingTabThenBackStillRejectsTheStaleTrendResponse', () => {
  const gate = createRequestGate()
  const v1 = gate.beginTrend('LAST_HOUR')
  // 切到告警
  gate.beginLoad(2)
  // 再切回趋势，发起新请求
  const v2 = gate.beginTrend('LAST_SIX_HOURS')
  // 第一次趋势响应此刻才回来 —— 不得覆盖 v2
  assert.equal(gate.isTrendCurrent(v1, 'LAST_HOUR'), false)
  assert.equal(gate.isTrendCurrent(v2, 'LAST_SIX_HOURS'), true)
})

test('afterUnloadEveryInFlightRequestIsInvalid', () => {
  const gate = createRequestGate()
  const trendVersion = gate.beginTrend('LAST_HOUR')
  const loadVersion = gate.beginLoad(0)

  gate.invalidateAll()

  // 卸载后任何响应都不得 setData 或绘制
  assert.equal(gate.isTrendCurrent(trendVersion, 'LAST_HOUR'), false)
  assert.equal(gate.isTrendCurrent(trendVersion), false)
  assert.equal(gate.isLoadCurrent(loadVersion, 0), false)
  assert.equal(gate.isLoadCurrent(loadVersion), false)
  assert.equal(gate.isDisposed(), true)

  // 卸载后即使「再开始一次」也不该复活：invalidateAll 是终态
  assert.equal(gate.isTrendCurrent(gate.beginTrend('LAST_DAY'), 'LAST_DAY'), false)
  assert.equal(gate.isLoadCurrent(gate.beginLoad(1), 1), false)
})

test('aTrendResponseMustNotPolluteANonTrendTab', () => {
  const gate = createRequestGate()
  // 趋势请求在途
  const trendVersion = gate.beginTrend('LAST_HOUR')
  // 用户切到设置页
  const settingsVersion = gate.beginLoad(3)

  // 非趋势页当前生效的是 settingsVersion；过期的 trendVersion 不得应用
  assert.equal(gate.isTrendCurrent(trendVersion, 'LAST_HOUR'), false)
  assert.equal(gate.isLoadCurrent(settingsVersion, 3), true)
  // 而且 trendVersion 也绝不能冒充设置页的加载版本
  assert.equal(gate.isLoadCurrent(trendVersion, 3), false)
})

test('windowKeyMismatchIsRejectedEvenWhenVersionMatches', () => {
  const gate = createRequestGate()
  const v = gate.beginTrend('LAST_HOUR')
  // 用户在响应落地前又点了同一「版本号」不会发生 —— 但若窗口已变（例如
  // 页面内部再同步），windowKey 不匹配仍必须拒绝，避免把旧窗口数据画进新窗口。
  assert.equal(gate.isTrendCurrent(v, 'LAST_SIX_HOURS'), false)
  assert.equal(gate.isTrendCurrent(v, 'LAST_HOUR'), true)
})

test('rapidRepeatedSwitchesOnlyTheLastOneWins', () => {
  const gate = createRequestGate()
  const versions = []
  for (const key of ['LAST_HOUR', 'LAST_SIX_HOURS', 'LAST_DAY', 'LAST_HOUR']) {
    versions.push(gate.beginTrend(key))
  }
  // 只有最后一次（LAST_HOUR）是当前意图
  const last = versions[versions.length - 1]
  versions.slice(0, -1).forEach((v) => {
    assert.equal(gate.isTrendCurrent(v), false)
  })
  assert.equal(gate.isTrendCurrent(last, 'LAST_HOUR'), true)
})
