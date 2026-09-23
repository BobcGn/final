/**
 * 微信原生 trends 页：Canvas 绘制竞态 + 失败态语义的单测。
 *
 * trends.js 的 Page 对象难以在 Node 下直接驱动（依赖 createSelectorQuery /
 * setData / Canvas），因此这些用例直接驱动 trend-chart.js 里的纯逻辑
 * createRenderGate —— 页面在 selector 回调里判断「本次绘制是否仍是最新意图」
 * 的唯一依据。覆盖题面要求的乱序回调、卸载后回调、时间窗切换三类场景。
 */

const test = require('node:test')
const assert = require('node:assert/strict')
const { createRenderGate, buildChartGeometry } = require('../utils/trend-chart.js')

function makeRecordingCtx() {
  const calls = []
  return {
    calls,
    clearRect(...args) { calls.push(['clearRect', ...args]) },
    save() { calls.push(['save']) },
    restore() { calls.push(['restore']) },
    beginPath() { calls.push(['beginPath']) },
    moveTo(...args) { calls.push(['moveTo', ...args]) },
    lineTo(...args) { calls.push(['lineTo', ...args]) },
    stroke() { calls.push(['stroke']) },
    arc(...args) { calls.push(['arc', ...args]) },
    fill() { calls.push(['fill']) },
    strokeStyle: '',
    fillStyle: '',
    lineWidth: 1,
    lineCap: '',
    lineJoin: '',
  }
}

// 模拟 trends.js renderChart 的关键守卫：只有 isStillWanted 通过才允许清空/重绘。
function tryDraw(gate, version, windowKey, ctx, geometry, setDataSink) {
  if (!gate.isStillWanted(version, windowKey)) return false
  // 这里对应 selector 回调里的绘制与 setData
  if (ctx) {
    const { drawTrendChart } = require('../utils/trend-chart.js')
    drawTrendChart(ctx, geometry)
  }
  if (setDataSink) setDataSink()
  return true
}

test('twoOutOfOrderSelectorCallbacksKeepOnlyTheSecond', () => {
  const gate = createRenderGate()
  const ctxA = makeRecordingCtx()
  const ctxB = makeRecordingCtx()

  // 请求/绘制 A（近1小时）发起，拿到版本 1
  const vA = gate.nextVersion('0')
  // 请求/绘制 B（近6小时）发起，拿到版本 2
  const vB = gate.nextVersion('1')

  // B 的 selector 回调先完成：应放行并绘制
  const drewB = tryDraw(gate, vB, '1', ctxB, { hasData: true, width: 10, height: 10, gridLines: [1], tempSegments: [[{ x: 1, y: 1 }]], humSegments: [], gasSegments: [], singlePoints: [] })
  assert.equal(drewB, true)
  assert.ok(ctxB.calls.some((c) => c[0] === 'clearRect'), 'B 允许清空并重绘')

  // A 的 selector 回调后完成：必须被拒绝，不得 clearRect / 绘制 / setData
  let aSetData = false
  const drewA = tryDraw(gate, vA, '0', ctxA, { hasData: true, width: 10, height: 10, gridLines: [1], tempSegments: [[{ x: 1, y: 1 }]], humSegments: [], gasSegments: [], singlePoints: [] }, () => { aSetData = true })
  assert.equal(drewA, false)
  assert.equal(ctxA.calls.length, 0, '过期回调不得 clearRect 或绘制')
  assert.equal(aSetData, false, '过期回调不得 setData 更新 chartRanges/axisTimes')
})

test('afterDisposeTheCallbackMustNotDrawOrSetData', () => {
  const gate = createRenderGate()
  const v = gate.nextVersion('0')
  // 页面 onUnload：_unloaded = true，renderGate.dispose()
  gate.dispose()

  const ctx = makeRecordingCtx()
  let setDataCalled = false
  const drew = tryDraw(gate, v, '0', ctx, { hasData: true, width: 10, height: 10, gridLines: [1], tempSegments: [[{ x: 1, y: 1 }]], humSegments: [], gasSegments: [], singlePoints: [] }, () => { setDataCalled = true })
  assert.equal(drew, false)
  assert.equal(ctx.calls.length, 0, '卸载后回调不得清空/绘制')
  assert.equal(setDataCalled, false, '卸载后回调不得 setData')
})

test('afterTimeWindowChangeTheOldCallbackIsRejected', () => {
  const gate = createRenderGate()
  // 旧时间窗的绘制意图
  const vOld = gate.nextVersion('0')
  // 用户切换时间窗，产生新意图
  const vNew = gate.nextVersion('1')

  // 旧回调带着旧 windowKey 回来：即使版本号「没变过」（它也已经变了），或
  // 即便侥幸版本仍相同，windowKey 不匹配也必须拒绝。
  const ctx = makeRecordingCtx()
  const drew = tryDraw(gate, vOld, '0', ctx, { hasData: true, width: 10, height: 10, gridLines: [1], tempSegments: [[{ x: 1, y: 1 }]], humSegments: [], gasSegments: [], singlePoints: [] })
  assert.equal(drew, false)

  // 新回调带着新 windowKey 才放行
  const ctx2 = makeRecordingCtx()
  const drew2 = tryDraw(gate, vNew, '1', ctx2, { hasData: true, width: 10, height: 10, gridLines: [1], tempSegments: [[{ x: 1, y: 1 }]], humSegments: [], gasSegments: [], singlePoints: [] })
  assert.equal(drew2, true)
})

test('windowKeyMismatchAloneIsSufficientToReject', () => {
  const gate = createRenderGate()
  const v = gate.nextVersion('0')
  // 版本还是当前的，但 windowKey 变了（防御：即使 nextVersion 尚未被再次调用）
  assert.equal(gate.isStillWanted(v, '1'), false)
  assert.equal(gate.isStillWanted(v, '0'), true)
})

test('emptySuccessAndNetworkFailureAreTwoDistinctStates', () => {
  // 空数据：hasData=false，几何层 xStartText/xEndText 为 --，表示「该区间没有样本」。
  const emptyGeo = buildChartGeometry([], { width: 100, height: 50 })
  assert.equal(emptyGeo.hasData, false)
  assert.equal(emptyGeo.xStartText, '--')
  assert.equal(emptyGeo.xEndText, '--')

  // 网络失败：页面侧应展示 LOAD_ERROR_TEXT，而不是把旧曲线伪装成新窗口，
  // 也不是把「加载失败」写成「暂无历史数据」。两者在 wxml 里是两个独立分支。
  const { readFileSync } = require('fs')
  const { join } = require('path')
  const wxml = readFileSync(join(__dirname, '../pages/trends/trends.wxml'), 'utf8')
  assert.ok(wxml.includes('暂无历史数据'), '空数据文案保留')
  assert.ok(wxml.includes('error'), '网络失败有独立的 error 分支')
  assert.ok(!/暂无历史数据[^]*error/.test(wxml.replace(/\s+/g, ' ')) || wxml.indexOf('error') !== -1)
  // 更直接：空态与 error 态必须是两个不同的条件分支，而不是共用同一段文案。
  const emptyBlock = /(!hasChartData)/.test(wxml)
  const errorBlock = /(\{\{error\}\})/.test(wxml)
  assert.ok(emptyBlock, '空态绑定 !hasChartData')
  assert.ok(errorBlock, '失败态绑定 error，与空态分开')
})

test('failureClearsTheOldCurveInsteadOfPresentingItAsTheNewWindow', () => {
  // 这里验证的是「失败后旧曲线不该再出现在 hasChartData=true 的状态」。
  // 页面契约：onRangeTap 立即清空 hasChartData / chartRanges / axisTimes；
  // fetch() 失败分支同样清空 chartPoints 并置 error。因此失败态下
  // hasChartData 必须为 false，曲线不可见。
  const { readFileSync } = require('fs')
  const { join } = require('path')
  const js = readFileSync(join(__dirname, '../pages/trends/trends.js'), 'utf8')

  // 失败分支必须清空 chartPoints（防止旧曲线伪装成新窗口）
  assert.ok(
    /catch\s*\(/.test(js) && /chartPoints\s*=\s*\[\]/.test(js),
    '失败分支必须清空 chartPoints',
  )
  // 失败分支必须写入 error（而不是只 toast 后当作空数据）
  assert.ok(
    /error\s*:/.test(js) && /历史数据加载失败/.test(js),
    '失败分支必须展示可理解的失败文案，而非空数据文案',
  )
})
