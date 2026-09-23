/**
 * 历史遥测折线图几何计算与 Canvas 绘制工具。
 *
 * 核心设计规则：
 * 1. 独立 Y 轴缩放：温度 (°C, #FB7185)、湿度 (%RH, #7DD3FC)、气体 (ppm, #3FE8C3)
 *    各自独立计算极值并保留 10% 上下内边距（padding）。
 * 2. 气体缺失分段：当 gasPpm 为 null 或 undefined 时打断连续线段，禁止将缺失值转为 0；
 *    有效测量值 0.0 正常保留并绘制。
 * 3. 时间比例 X 轴（all-or-nothing）：仅当「样本数 > 1」且「每个时间戳都有效」
 *    且「首尾跨度为正」时按时间比例分布；单样本、任一时间戳异常或全部时间戳相同
 *    时，整条序列统一降级为按索引均匀分布。绝不混用两种映射。
 * 4. 纯逻辑与绘图解耦：buildChartGeometry 为无 DOM/Canvas 依赖的纯函数，便于自动化测试。
 * 5. 竞态门闩：createRenderGate 提供可注入的版本校验，供页面在异步 Canvas 回调里
 *    判断「本次绘制是否仍是最新意图」。
 */

const { formatTime } = require('./helpers.js')

const COLORS = {
  temp: '#FB7185',
  hum: '#7DD3FC',
  gas: '#3FE8C3',
}

/**
 * 计算单一指标的显示数值范围（包含 10% padding）。
 * @param {number[]} values 有效数值列表
 * @param {number} fallbackMin 缺省最小值
 * @param {number} fallbackMax 缺省最大值
 */
function computeRange(values, fallbackMin, fallbackMax) {
  const valid = values.filter((v) => v != null && !isNaN(v) && isFinite(v))
  if (!valid.length) {
    return {
      min: fallbackMin,
      max: fallbackMax,
      rawMin: null,
      rawMax: null,
    }
  }

  let rawMin = valid[0]
  let rawMax = valid[0]
  for (let i = 1; i < valid.length; i++) {
    if (valid[i] < rawMin) rawMin = valid[i]
    if (valid[i] > rawMax) rawMax = valid[i]
  }

  if (rawMin === rawMax) {
    return {
      min: rawMin - 1.0,
      max: rawMax + 1.0,
      rawMin,
      rawMax,
    }
  }

  const span = rawMax - rawMin
  const pad = span * 0.1
  return {
    min: rawMin - pad,
    max: rawMax + pad,
    rawMin,
    rawMax,
  }
}

/**
 * 解析时间戳为毫秒数。
 * 无法解析时返回 null，绝不伪造 epoch 0。
 */
function parseTimestamp(ts) {
  if (ts == null) return null
  if (typeof ts === 'number') return isFinite(ts) ? ts : null
  const ms = Date.parse(ts)
  return isNaN(ms) ? null : ms
}

/**
 * 根据原始遥测样本构建折线图几何结构。
 * @param {Array} rawPoints 遥测数据列表（包含 receivedAt/timestamp, temperatureC, humidityRh, gasPpm）
 * @param {Object} options 图表尺寸与内边距配置
 */
function buildChartGeometry(rawPoints, options = {}) {
  const width = Number(options.width) || 300
  const height = Number(options.height) || 180
  const padding = Object.assign(
    { left: 16, right: 16, top: 16, bottom: 16 },
    options.padding || {}
  )

  const plotWidth = Math.max(width - padding.left - padding.right, 1)
  const plotHeight = Math.max(height - padding.top - padding.bottom, 1)

  // 4 条水平网格参考线
  const gridLines = [0, 1, 2, 3].map((i) => {
    return padding.top + (plotHeight / 3) * i
  })

  if (!rawPoints || !rawPoints.length) {
    return {
      hasData: false,
      usesTimeScale: false,
      width,
      height,
      padding,
      gridLines,
      tempSegments: [],
      humSegments: [],
      gasSegments: [],
      singlePoints: [],
      xStartText: '--',
      xEndText: '--',
      tempRangeText: '--',
      humRangeText: '--',
      gasRangeText: '--',
    }
  }

  // 解析每个样本的时间戳；保留 iso 字段供轴标签使用。
  const points = rawPoints.map((p) => {
    const iso = p.receivedAt || p.timestamp
    return {
      raw: p,
      iso,
      timestampMs: parseTimestamp(iso),
      temp: p.temperatureC != null && !isNaN(p.temperatureC) && isFinite(p.temperatureC) ? Number(p.temperatureC) : null,
      hum: p.humidityRh != null && !isNaN(p.humidityRh) && isFinite(p.humidityRh) ? Number(p.humidityRh) : null,
      gas: p.gasPpm != null && !isNaN(p.gasPpm) && isFinite(p.gasPpm) ? Number(p.gasPpm) : null,
    }
  })

  // 排序规则：仅当每个时间戳都有效时才按事件时间升序（稳定排序），
  // 否则保留调用方给定的呈现顺序 —— 混杂无效时间戳时没有可靠排序键。
  const allValidTs = points.every((p) => p.timestampMs != null)
  if (points.length > 1 && allValidTs) {
    points.sort((a, b) => a.timestampMs - b.timestampMs)
  }

  // X 映射（all-or-nothing）：全部有效且跨度为正 → 时间比例；否则整条序列均匀分布。
  const n = points.length
  let usesTimeScale = false
  let xs
  if (n === 1) {
    xs = [padding.left + plotWidth / 2]
  } else if (allValidTs) {
    const tMin = points[0].timestampMs
    const tMax = points[n - 1].timestampMs
    const span = tMax - tMin
    if (span > 0) {
      usesTimeScale = true
      xs = points.map((p) => {
        const ratio = Math.max(0, Math.min(1, (p.timestampMs - tMin) / span))
        return padding.left + ratio * plotWidth
      })
    } else {
      // 全部时间戳相同：跨度非正，整条序列降级为均匀分布。
      xs = points.map((_, idx) => padding.left + (idx / (n - 1)) * plotWidth)
    }
  } else {
    // 任一时间戳异常：整条序列降级为均匀分布，禁止混用时间比例。
    xs = points.map((_, idx) => padding.left + (idx / (n - 1)) * plotWidth)
  }

  // 计算各指标数值范围
  const tempRange = computeRange(points.map((p) => p.temp), 15, 35)
  const humRange = computeRange(points.map((p) => p.hum), 30, 80)
  const gasRange = computeRange(points.map((p) => p.gas), 0, 100)

  // 映射 Y 坐标函数
  function mapY(val, range) {
    if (val == null) return null
    const clamped = Math.max(range.min, Math.min(range.max, val))
    const ratio = (clamped - range.min) / (range.max - range.min)
    return padding.top + (1.0 - ratio) * plotHeight
  }

  // 构建连续线段
  const tempSegments = []
  let currentTempSeg = []
  for (let i = 0; i < n; i++) {
    const val = points[i].temp
    if (val != null) {
      currentTempSeg.push({ x: xs[i], y: mapY(val, tempRange), value: val })
    } else if (currentTempSeg.length) {
      tempSegments.push(currentTempSeg)
      currentTempSeg = []
    }
  }
  if (currentTempSeg.length) tempSegments.push(currentTempSeg)

  const humSegments = []
  let currentHumSeg = []
  for (let i = 0; i < n; i++) {
    const val = points[i].hum
    if (val != null) {
      currentHumSeg.push({ x: xs[i], y: mapY(val, humRange), value: val })
    } else if (currentHumSeg.length) {
      humSegments.push(currentHumSeg)
      currentHumSeg = []
    }
  }
  if (currentHumSeg.length) humSegments.push(currentHumSeg)

  // 气体段：gasPpm 为 null 时分段
  const gasSegments = []
  let currentGasSeg = []
  for (let i = 0; i < n; i++) {
    const val = points[i].gas
    if (val != null) {
      currentGasSeg.push({ x: xs[i], y: mapY(val, gasRange), value: val })
    } else if (currentGasSeg.length) {
      gasSegments.push(currentGasSeg)
      currentGasSeg = []
    }
  }
  if (currentGasSeg.length) gasSegments.push(currentGasSeg)

  // 提取孤立单点（需要绘制圆点以便清晰辨识）
  const singlePoints = []
  tempSegments.forEach((seg) => {
    if (seg.length === 1) singlePoints.push({ x: seg[0].x, y: seg[0].y, color: COLORS.temp })
  })
  humSegments.forEach((seg) => {
    if (seg.length === 1) singlePoints.push({ x: seg[0].x, y: seg[0].y, color: COLORS.hum })
  })
  gasSegments.forEach((seg) => {
    if (seg.length === 1) singlePoints.push({ x: seg[0].x, y: seg[0].y, color: COLORS.gas })
  })

  // 格式化图例文本
  const tempRangeText = tempRange.rawMin != null ? `${tempRange.rawMin.toFixed(1)}~${tempRange.rawMax.toFixed(1)}°C` : '--'
  const humRangeText = humRange.rawMin != null ? `${humRange.rawMin.toFixed(1)}~${humRange.rawMax.toFixed(1)}%` : '--'
  const gasRangeText = gasRange.rawMin != null ? `${gasRange.rawMin.toFixed(1)}~${gasRange.rawMax.toFixed(1)}ppm` : '--'

  // 轴标签只在该端点时间戳可靠时显示时钟；不可靠端点显示 `--`，绝不编造时间。
  const firstTsValid = points[0].timestampMs != null
  const lastTsValid = points[n - 1].timestampMs != null
  const xStartText = firstTsValid ? formatTime(points[0].iso) : '--'
  const xEndText = lastTsValid ? formatTime(points[n - 1].iso) : '--'

  return {
    hasData: true,
    usesTimeScale,
    width,
    height,
    padding,
    gridLines,
    tempSegments,
    humSegments,
    gasSegments,
    singlePoints,
    xStartText,
    xEndText,
    tempRangeText,
    humRangeText,
    gasRangeText,
  }
}

/**
 * 在 Canvas 2D 上绘制折线图。
 * @param {CanvasRenderingContext2D} ctx
 * @param {Object} geometry 由 buildChartGeometry 返回的几何数据
 */
function drawTrendChart(ctx, geometry) {
  if (!ctx || !geometry) return

  const { width, height, gridLines, tempSegments, humSegments, gasSegments, singlePoints, hasData } = geometry
  ctx.clearRect(0, 0, width, height)

  // 1. 绘制水平参考线
  ctx.save()
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.06)'
  ctx.lineWidth = 1
  gridLines.forEach((y) => {
    ctx.beginPath()
    ctx.moveTo(0, y)
    ctx.lineTo(width, y)
    ctx.stroke()
  })
  ctx.restore()

  if (!hasData) return

  // 辅助函数：绘制曲线段
  function drawSegments(segments, color, lineWidth = 2) {
    ctx.save()
    ctx.strokeStyle = color
    ctx.lineWidth = lineWidth
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

  // 2. 依次绘制湿度、气体、温度曲线
  drawSegments(humSegments, COLORS.hum, 2)
  drawSegments(gasSegments, COLORS.gas, 2)
  drawSegments(tempSegments, COLORS.temp, 2)

  // 3. 绘制孤立单点
  if (singlePoints && singlePoints.length) {
    ctx.save()
    singlePoints.forEach((pt) => {
      ctx.beginPath()
      ctx.fillStyle = pt.color
      ctx.arc(pt.x, pt.y, 3, 0, Math.PI * 2)
      ctx.fill()
    })
    ctx.restore()
  }
}

/**
 * 创建绘制竞态门闩（纯逻辑，可注入、可单测）。
 *
 * 网络请求和 `createSelectorQuery().exec()` 是两个异步边界：请求 A 的 selector
 * 回调可能在请求 B 之后完成。页面在发起绘制前递增版本号，并在 selector 回调里
 * 调用 `isStillWanted` 重新校验：页面已卸载、版本已过期、或时间窗已切换时，
 * 本次回调必须放弃绘制与 setData。
 *
 * @returns {{nextVersion: () => number, isStillWanted: (version: number, windowKey: string) => boolean, dispose: () => void}}
 */
function createRenderGate() {
  let version = 0
  let windowKey = null
  let disposed = false

  return {
    /** 开始一次新的绘制意图；返回本次的版本号。 */
    nextVersion(nextWindowKey) {
      version += 1
      windowKey = nextWindowKey == null ? null : String(nextWindowKey)
      return version
    },
    /**
     * 判断 version 是否仍是最新且页面仍存活、时间窗未变。
     * @param {number} candidateVersion 发起绘制时拿到的版本号
     * @param {string} candidateWindowKey 发起绘制时的时间窗
     */
    isStillWanted(candidateVersion, candidateWindowKey) {
      if (disposed) return false
      if (candidateVersion !== version) return false
      const nextKey = candidateWindowKey == null ? null : String(candidateWindowKey)
      return nextKey === windowKey
    },
    /** 页面卸载时调用；使所有在途回调失效。 */
    dispose() {
      disposed = true
      version += 1
      windowKey = null
    },
  }
}

module.exports = {
  COLORS,
  computeRange,
  parseTimestamp,
  buildChartGeometry,
  drawTrendChart,
  createRenderGate,
}
