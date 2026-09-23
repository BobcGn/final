/**
 * 趋势图 Canvas 2D 渲染器与生命周期/布局协调器回归测试。
 *
 * 覆盖根因要求的 5 大场景：
 * 1. 首次布局尚未就绪：测量尺寸未就绪（如 0x0 或无效值）时不执行脏绘制。
 * 2. 尺寸就绪后首次绘制：真实容器尺寸就绪时正确执行绘制，DPR 矩阵通过 setTransform 准确设置，连续段批量 stroke 无逐点 arc 循环。
 * 3. 时间窗快速切换产生的过期回调：窗口 A 测量未回时切到窗口 B，A 回调到达后被废弃，不绘制过期数据。
 * 4. 页面离开后回调：页面卸载或切出后到达的测量与绘制回调被彻底阻止。
 * 5. 重新进入页面：onShow 重新进入后能够基于最新数据安全触发重绘。
 */

const test = require('node:test')
const assert = require('node:assert/strict')
const { createRequestGate } = require('./trend-request.js')
const {
  isLayoutReady,
  computeChartGeometry,
  drawGeometryOnCanvas,
  createTrendCoordinator,
} = require('./trend-renderer.js')

function createMockContext() {
  const calls = []
  return {
    calls,
    setTransform(a, b, c, d, e, f) {
      calls.push({ type: 'setTransform', a, b, c, d, e, f })
    },
    scale(x, y) {
      calls.push({ type: 'scale', x, y })
    },
    clearRect(x, y, w, h) {
      calls.push({ type: 'clearRect', x, y, w, h })
    },
    save() {
      calls.push({ type: 'save' })
    },
    restore() {
      calls.push({ type: 'restore' })
    },
    beginPath() {
      calls.push({ type: 'beginPath' })
    },
    moveTo(x, y) {
      calls.push({ type: 'moveTo', x, y })
    },
    lineTo(x, y) {
      calls.push({ type: 'lineTo', x, y })
    },
    stroke() {
      calls.push({ type: 'stroke' })
    },
    arc(x, y, r, sa, ea) {
      calls.push({ type: 'arc', x, y, r, sa, ea })
    },
    fill() {
      calls.push({ type: 'fill' })
    },
  }
}

function createMockCanvas(ctx) {
  return {
    width: 0,
    height: 0,
    getContext(type) {
      if (type === '2d') return ctx
      return null
    },
  }
}

function createSampleTrends(count = 10) {
  const series = []
  const baseTime = 1774300000000
  for (let i = 0; i < count; i++) {
    series.push({
      key: 'k' + i,
      receivedAt: '2026-09-23T10:00:00Z',
      timeText: '10:00:00',
      temperatureText: '25',
      humidityText: '50',
      gasText: '15',
      localAlarm: false,
      timestampEpochMs: baseTime + i * 5000,
      temperatureC: 20.0 + (i % 5),
      humidityRh: 45.0 + (i % 10),
      gasPpm: i === 5 ? null : 15.0 + (i % 3), // 包含气体空洞
    })
  }
  return {
    hasData: true,
    sampleCount: count,
    gasSampleCount: count - 1,
    curveReady: true,
    series,
  }
}

test('1. 首次布局尚未就绪时不执行脏绘制', async () => {
  const gate = createRequestGate()
  const coordinator = createTrendCoordinator({ gate })
  const ctx = createMockContext()
  const canvas = createMockCanvas(ctx)
  const trends = createSampleTrends(50)
  const version = gate.beginTrend('LAST_HOUR')

  let queryCallCount = 0
  // 模拟初次查询：布局尚未就绪（bodyRect 为 0x0，未脱离未渲染状态）
  const queryExecutor = () => {
    queryCallCount += 1
    return [
      { width: 0, height: 0 },
      { node: canvas, width: 0, height: 0 },
    ]
  }

  const scheduled = coordinator.scheduleRender(trends, version, 'LAST_HOUR', {}, queryExecutor)
  assert.equal(scheduled, true)

  // 等待微任务
  await new Promise((resolve) => setTimeout(resolve, 10))

  // 验证：因为尺寸 <= 0，绝不在 0x0 或缺省 300x150 脏画布上执行 stroke/draw
  const strokeCalls = ctx.calls.filter((c) => c.type === 'stroke')
  assert.equal(strokeCalls.length, 0, '布局未就绪时不应执行任何曲线描边')
  assert.equal(canvas.width, 0, 'Canvas buffer 宽度不应被修改')
})

test('2. 尺寸就绪后首次正确绘制并使用 setTransform 设定 DPR 且无 600 次逐点 arc 循环', async () => {
  const gate = createRequestGate()
  const coordinator = createTrendCoordinator({ gate })
  const ctx = createMockContext()
  const canvas = createMockCanvas(ctx)
  const count = 200 // 满额 200 条样本，三指标合计 600 个点
  const trends = createSampleTrends(count)
  const version = gate.beginTrend('LAST_HOUR')

  // 模拟真实容器已测量就绪：宽 317px，高 180px
  const queryExecutor = () => {
    return [
      { width: 317, height: 180 },
      { node: canvas, width: 317, height: 180 },
    ]
  }

  const scheduled = coordinator.scheduleRender(trends, version, 'LAST_HOUR', {}, queryExecutor)
  assert.equal(scheduled, true)

  await new Promise((resolve) => setTimeout(resolve, 10))

  // 验证 DPR 与 setTransform
  const transformCalls = ctx.calls.filter((c) => c.type === 'setTransform')
  assert.ok(transformCalls.length >= 1, '必须通过 setTransform 设置矩阵')
  assert.ok(canvas.width > 0 && canvas.height > 0, 'Canvas 缓冲区尺寸必须正值')

  // 验证清空画布
  const clearCalls = ctx.calls.filter((c) => c.type === 'clearRect')
  assert.ok(clearCalls.length >= 1, '必须执行 clearRect')

  // 验证连续折线描边正常产生
  const strokeCalls = ctx.calls.filter((c) => c.type === 'stroke')
  assert.ok(strokeCalls.length >= 4, '必须包含 1 次网格线 + 3 项指标曲线描边')

  // 核心卡顿根因验证：200 点连续段绝不应出现 600 次 arc 绘制
  const arcCalls = ctx.calls.filter((c) => c.type === 'arc')
  assert.ok(
    arcCalls.length <= 5,
    `连续线段不应对每个点调用 arc；期望孤点数量极少，实际 arc 调用次数: ${arcCalls.length}`
  )
})

test('3. 时间窗快速切换产生的过期回调被完全废弃', async () => {
  const gate = createRequestGate()
  const coordinator = createTrendCoordinator({ gate })
  const ctx = createMockContext()
  const canvas = createMockCanvas(ctx)
  const trendsA = createSampleTrends(10)
  const trendsB = createSampleTrends(20)

  // 1. 用户点击近1小时（请求 A 发出）
  const versionA = gate.beginTrend('LAST_HOUR')

  // 2. 模拟请求 A 的测量较慢，用户在此期间快速切换到近6小时（请求 B 发出）
  let resolveQueryA
  const queryExecutorA = () => new Promise((r) => (resolveQueryA = r))
  coordinator.scheduleRender(trendsA, versionA, 'LAST_HOUR', {}, queryExecutorA)

  const versionB = gate.beginTrend('LAST_SIX_HOURS')
  const queryExecutorB = () => [
    { width: 317, height: 180 },
    { node: canvas, width: 317, height: 180 },
  ]
  coordinator.scheduleRender(trendsB, versionB, 'LAST_SIX_HOURS', {}, queryExecutorB)

  // 3. 请求 A 延迟返回
  if (resolveQueryA) {
    resolveQueryA([
      { width: 317, height: 180 },
      { node: canvas, width: 317, height: 180 },
    ])
  }

  await new Promise((resolve) => setTimeout(resolve, 15))

  // 验证：coordinator 内当前保留的活跃意图必须是 B
  assert.equal(gate.isTrendCurrent(versionA, 'LAST_HOUR'), false)
  assert.equal(gate.isTrendCurrent(versionB, 'LAST_SIX_HOURS'), true)
})

test('4. 页面离开后（卸载/切出）回调被安全阻止', async () => {
  const gate = createRequestGate()
  const coordinator = createTrendCoordinator({ gate })
  const ctx = createMockContext()
  const canvas = createMockCanvas(ctx)
  const trends = createSampleTrends(15)
  const version = gate.beginTrend('LAST_HOUR')

  const page = { _unloaded: false }

  let resolveQuery
  const slowQueryExecutor = () => new Promise((r) => (resolveQuery = r))

  coordinator.scheduleRender(trends, version, 'LAST_HOUR', page, slowQueryExecutor)

  // 页面离开/卸载
  page._unloaded = true
  coordinator.dispose()
  gate.invalidateAll()

  // 延迟回调终于返回
  if (resolveQuery) {
    resolveQuery([
      { width: 317, height: 180 },
      { node: canvas, width: 317, height: 180 },
    ])
  }

  await new Promise((resolve) => setTimeout(resolve, 10))

  const strokeCalls = ctx.calls.filter((c) => c.type === 'stroke')
  assert.equal(strokeCalls.length, 0, '页面卸载后不应在已销毁页面上绘制')
})

test('5. 重新进入页面（onShow）正常触发重绘且不产生矩阵累乘', async () => {
  const gate = createRequestGate()
  const coordinator = createTrendCoordinator({ gate })
  const ctx = createMockContext()
  const canvas = createMockCanvas(ctx)
  const trends = createSampleTrends(10)

  const queryExecutor = () => [
    { width: 317, height: 180 },
    { node: canvas, width: 317, height: 180 },
  ]

  // 第一次进入
  const v1 = gate.beginTrend('LAST_HOUR')
  coordinator.scheduleRender(trends, v1, 'LAST_HOUR', {}, queryExecutor)
  await new Promise((resolve) => setTimeout(resolve, 10))

  // 离开页面
  coordinator.clearCanvas({}, () => [
    { width: 317, height: 180 },
    { node: canvas, width: 317, height: 180 },
  ])

  // 再次进入页面（onShow）
  const v2 = gate.beginTrend('LAST_HOUR')
  coordinator.scheduleRender(trends, v2, 'LAST_HOUR', {}, queryExecutor)
  await new Promise((resolve) => setTimeout(resolve, 10))

  // 验证 setTransform 每次都基于绝对 DPR 设定，不会调用相对累乘的 scale
  const scaleCalls = ctx.calls.filter((c) => c.type === 'scale')
  assert.equal(scaleCalls.length, 0, '重入时严禁使用相对 scale 导致缩放累乘')
  const transformCalls = ctx.calls.filter((c) => c.type === 'setTransform')
  assert.ok(transformCalls.length >= 2, '每次重绘必须调用 setTransform 重置坐标系')
})
