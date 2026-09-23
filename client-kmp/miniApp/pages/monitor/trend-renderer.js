/**
 * 趋势图 Canvas 2D 绘制协调器与高性能几何渲染引擎。
 *
 * 核心设计：
 * 1. 布局时机感知：容器尺寸未稳定（width <= 0 或未脱离缺省回退尺寸）时绝不执行脏绘制；
 *    尺寸稳定后方才执行首次绘制，无需依赖页面滚动刷新。
 * 2. 绘制性能优化：对齐基线设计，连续折线段以单一 Path 描边，仅对孤立单点（singlePoints）
 *    绘制圆形标记点，彻底消除对 600 个点逐个调用 ctx.arc / ctx.fill 引起的主线程卡顿。
 * 3. DPR 与矩阵安全：通过 ctx.setTransform(dpr, 0, 0, dpr, 0, 0) 设置画布缩放，
 *    避免重绘时矩阵累乘放大失真。
 * 4. 竞态门闩贯穿：测量和绘制回调作为异步边界，均在 gate.isTrendCurrent 校验通过后
 *    才允许写入画布，快速切窗或页面卸载/隐藏后所有过期回调一律废弃。
 */

const COLORS = {
  temp: '#FB7185',
  hum: '#7DD3FC',
  gas: '#3FE8C3',
}

const DEFAULT_PADDING = {
  left: 12,
  right: 12,
  top: 16,
  bottom: 16,
}

/**
 * 判断当前获取到的容器与画布尺寸是否已经就绪。
 * 当尺寸不存在、宽高 <= 0 时视为未就绪。
 */
function isLayoutReady(size) {
  if (!size) return false
  const width = Number(size.width)
  const height = Number(size.height)
  if (isNaN(width) || isNaN(height)) return false
  return width > 0 && height > 0
}

/**
 * 从原始点列构建折线图几何数据（纯函数，无 DOM 依赖）。
 */
function computeChartGeometry(rawPoints, width, height, padding = DEFAULT_PADDING) {
  const padLeft = padding.left || DEFAULT_PADDING.left
  const padRight = padding.right || DEFAULT_PADDING.right
  const padTop = padding.top || DEFAULT_PADDING.top
  const padBottom = padding.bottom || DEFAULT_PADDING.bottom

  const plotWidth = Math.max(width - padLeft - padRight, 1)
  const plotHeight = Math.max(height - padTop - padBottom, 1)

  // 4 条水平参考线
  const gridLines = [0, 1, 2, 3].map((i) => padTop + (plotHeight / 3) * i)

  if (!rawPoints || !rawPoints.length) {
    return {
      hasData: false,
      width,
      height,
      gridLines,
      tempSegments: [],
      humSegments: [],
      gasSegments: [],
      singlePoints: [],
    }
  }

  // 1. X 坐标映射（all-or-nothing：全部时间戳有效且跨度为正时按时间比例，否则降级为均匀分布）
  let xCoords = []
  if (rawPoints.length === 1) {
    xCoords = [padLeft + plotWidth / 2]
  } else {
    const timestamps = rawPoints.map((p) => (p.timestampEpochMs == null ? null : p.timestampEpochMs))
    const allValid = timestamps.every((t) => t != null)
    const positiveSpan = allValid && timestamps[timestamps.length - 1] - timestamps[0] > 0
    if (allValid && positiveSpan) {
      const tMin = timestamps[0]
      const tMax = timestamps[timestamps.length - 1]
      const span = tMax - tMin
      xCoords = timestamps.map((t) => {
        const fraction = Math.max(0, Math.min(1, (t - tMin) / span))
        return padLeft + fraction * plotWidth
      })
    } else {
      const step = plotWidth / (rawPoints.length - 1)
      xCoords = rawPoints.map((_, idx) => padLeft + idx * step)
    }
  }

  // 2. Y 坐标映射与分段提取
  function buildMetricSegments(values, color) {
    const valid = values.filter((v) => v !== null && v !== undefined && !isNaN(v) && isFinite(v))
    if (!valid.length) return { segments: [], singles: [] }

    const minVal = Math.min(...valid)
    const maxVal = Math.max(...valid)

    const computeY = (v) => {
      if (maxVal <= minVal) return padTop + plotHeight / 2
      const span = maxVal - minVal
      const pMin = minVal - span * 0.1
      const pMax = maxVal + span * 0.1
      const frac = Math.max(0, Math.min(1, (v - pMin) / (pMax - pMin)))
      return height - padBottom - frac * plotHeight
    }

    const segments = []
    let curSeg = []
    const singles = []

    for (let i = 0; i < values.length; i++) {
      const v = values[i]
      if (v === null || v === undefined || isNaN(v) || !isFinite(v)) {
        if (curSeg.length === 1) {
          singles.push({ x: curSeg[0].x, y: curSeg[0].y, color })
        } else if (curSeg.length > 1) {
          segments.push(curSeg)
        }
        curSeg = []
      } else {
        curSeg.push({ x: xCoords[i], y: computeY(v) })
      }
    }
    if (curSeg.length === 1) {
      singles.push({ x: curSeg[0].x, y: curSeg[0].y, color })
    } else if (curSeg.length > 1) {
      segments.push(curSeg)
    }

    return { segments, singles }
  }

  const tempResult = buildMetricSegments(rawPoints.map((p) => p.temperatureC), COLORS.temp)
  const humResult = buildMetricSegments(rawPoints.map((p) => p.humidityRh), COLORS.hum)
  const gasResult = buildMetricSegments(rawPoints.map((p) => p.gasPpm), COLORS.gas)

  const singlePoints = [...tempResult.singles, ...humResult.singles, ...gasResult.singles]

  return {
    hasData: true,
    width,
    height,
    gridLines,
    tempSegments: tempResult.segments,
    humSegments: humResult.segments,
    gasSegments: gasResult.segments,
    singlePoints,
  }
}

/**
 * 在 Canvas 2D 上执行高效绘制。
 * 连续折线段以单一路径批量描边，只有孤立单点才绘制小圆点。
 */
function drawGeometryOnCanvas(ctx, geometry) {
  if (!ctx || !geometry) return
  const { width, height, gridLines, tempSegments, humSegments, gasSegments, singlePoints, hasData } = geometry

  ctx.clearRect(0, 0, width, height)

  // 1. 绘制水平参考线
  ctx.save()
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.06)'
  ctx.lineWidth = 1
  gridLines.forEach((y) => {
    ctx.beginPath()
    ctx.moveTo(DEFAULT_PADDING.left, y)
    ctx.lineTo(width - DEFAULT_PADDING.right, y)
    ctx.stroke()
  })
  ctx.restore()

  if (!hasData) return

  // 辅助函数：以单一路径连续绘制折线段
  function strokeSegments(segments, color) {
    if (!segments || !segments.length) return
    ctx.save()
    ctx.strokeStyle = color
    ctx.lineWidth = 2
    ctx.lineCap = 'round'
    ctx.lineJoin = 'round'

    segments.forEach((seg) => {
      if (seg.length >= 2) {
        ctx.beginPath()
        ctx.moveTo(seg[0].x, seg[0].y)
        for (let i = 1; i < seg.length; i++) {
          ctx.lineTo(seg[i].x, seg[i].y)
        }
        ctx.stroke()
      }
    })
    ctx.restore()
  }

  // 2. 依次绘制三项指标曲线
  strokeSegments(humSegments, COLORS.hum)
  strokeSegments(gasSegments, COLORS.gas)
  strokeSegments(tempSegments, COLORS.temp)

  // 3. 仅对孤立单点绘制圆点
  if (singlePoints && singlePoints.length) {
    ctx.save()
    singlePoints.forEach((pt) => {
      ctx.beginPath()
      ctx.fillStyle = pt.color
      ctx.arc(pt.x, pt.y, 3.5, 0, Math.PI * 2)
      ctx.fill()
    })
    ctx.restore()
  }
}

/**
 * 创建趋势图绘制协调器。
 *
 * @param {Object} options
 * @param {Object} options.gate 趋势请求竞态门闩（createRequestGate 实例）
 * @param {Function} [options.queryExecutor] 节点查询函数，缺省使用 wx.createSelectorQuery
 */
function createTrendCoordinator(options = {}) {
  const gate = options.gate
  let activeVersion = 0
  let activeWindowKey = null
  let pendingRender = null
  let retryCount = 0
  const MAX_RETRIES = 5

  return {
    /**
     * 调度一次趋势绘制。
     *
     * @param {Object} trends 共享运行时输出的 TrendsView
     * @param {number} version 本次绘制意图的版本号
     * @param {string} windowKey 本次绘制对应的时间窗
     * @param {Object} pageContext 页面实例（Page 对象，包含 this）
     * @param {Function} [customQuery] 可选注入的查询器，用于单测
     */
    scheduleRender(trends, version, windowKey, pageContext, customQuery) {
      if (!gate || gate.isDisposed()) return false
      if (!gate.isTrendCurrent(version, windowKey)) return false
      if (pageContext && pageContext._unloaded) return false
      if (!trends || !trends.hasData || !trends.series || !trends.series.length) {
        return false
      }

      activeVersion = version
      activeWindowKey = windowKey
      pendingRender = { trends, version, windowKey }
      retryCount = 0

      const runMeasureAndDraw = () => {
        // 异步重入校验
        if (!gate.isTrendCurrent(version, windowKey)) return
        if (pageContext && pageContext._unloaded) return
        if (activeVersion !== version || activeWindowKey !== windowKey) return

        const doQuery = customQuery || (typeof wx !== 'undefined' && wx.createSelectorQuery ? () => {
          return new Promise((resolve) => {
            const query = wx.createSelectorQuery().in(pageContext)
            query.select('.chart-body').boundingClientRect()
            query.select('#trendCanvas').fields({ node: true, size: true })
            query.exec((res) => resolve(res))
          })
        } : null)

        if (!doQuery) return

        Promise.resolve(doQuery()).then((res) => {
          if (!gate.isTrendCurrent(version, windowKey)) return
          if (pageContext && pageContext._unloaded) return
          if (activeVersion !== version || activeWindowKey !== windowKey) return
          if (!res || !Array.isArray(res)) return

          const bodyRect = res[0]
          const canvasField = res[1]

          // 优先从已完成布局的 .chart-body 容器读取尺寸；回退从 canvasField 读取
          const width = (bodyRect && bodyRect.width) || (canvasField && canvasField.width) || 0
          const height = (bodyRect && bodyRect.height) || (canvasField && canvasField.height) || 0
          const canvasNode = canvasField && canvasField.node

          if (!isLayoutReady({ width, height }) || !canvasNode) {
            // 首次布局尚未就绪：重试机制（借助 wx.nextTick，绝不使用固定 setTimeout）
            if (retryCount < MAX_RETRIES) {
              retryCount += 1
              if (typeof wx !== 'undefined' && wx.nextTick) {
                wx.nextTick(() => runMeasureAndDraw())
              } else if (typeof setImmediate !== 'undefined') {
                setImmediate(() => runMeasureAndDraw())
              } else {
                Promise.resolve().then(() => runMeasureAndDraw())
              }
            }
            return
          }

          // 尺寸就绪：设置 DPR 并完成绘制
          const dpr = (typeof wx !== 'undefined' && wx.getWindowInfo && wx.getWindowInfo().pixelRatio) ||
                      (typeof wx !== 'undefined' && wx.getSystemInfoSync && wx.getSystemInfoSync().pixelRatio) || 1

          canvasNode.width = Math.round(width * dpr)
          canvasNode.height = Math.round(height * dpr)
          const ctx = canvasNode.getContext('2d')
          if (!ctx) return

          // 关键：绝对矩阵缩放，防止重复 scale 累乘
          if (typeof ctx.setTransform === 'function') {
            ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
          } else {
            ctx.scale(dpr, dpr)
          }

          const geometry = computeChartGeometry(trends.series, width, height, DEFAULT_PADDING)
          drawGeometryOnCanvas(ctx, geometry)
          pendingRender = null
        })
      }

      // 初次触发安排在 nextTick 之后，确保逻辑层数据已同步到视图层
      if (typeof wx !== 'undefined' && wx.nextTick) {
        wx.nextTick(() => runMeasureAndDraw())
      } else {
        Promise.resolve().then(() => runMeasureAndDraw())
      }
      return true
    },

    /** 清空画布。切窗或请求失败时立即调用。 */
    clearCanvas(pageContext, customQuery) {
      pendingRender = null
      activeVersion += 1
      activeWindowKey = null

      const doQuery = customQuery || (typeof wx !== 'undefined' && wx.createSelectorQuery ? () => {
        return new Promise((resolve) => {
          const query = wx.createSelectorQuery().in(pageContext)
          query.select('#trendCanvas').fields({ node: true, size: true }).exec((res) => resolve(res))
        })
      } : null)

      if (!doQuery) return

      Promise.resolve(doQuery()).then((res) => {
        if (!res || !res[0] || !res[0].node) return
        const canvasNode = res[0].node
        const ctx = canvasNode.getContext('2d')
        if (!ctx) return
        const width = res[0].width || (canvasNode.width || 300)
        const height = res[0].height || (canvasNode.height || 180)
        ctx.clearRect(0, 0, width, height)
      })
    },

    getPendingRender() {
      return pendingRender
    },

    dispose() {
      pendingRender = null
      activeVersion += 1
      activeWindowKey = null
    },
  }
}

module.exports = {
  COLORS,
  DEFAULT_PADDING,
  isLayoutReady,
  computeChartGeometry,
  drawGeometryOnCanvas,
  createTrendCoordinator,
}
