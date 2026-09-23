const test = require('node:test')
const assert = require('node:assert/strict')
const {
  COLORS,
  computeRange,
  parseTimestamp,
  buildChartGeometry,
  drawTrendChart,
} = require('../utils/trend-chart.js')

test('computeRange with empty array returns fallback range', () => {
  const range = computeRange([], 10, 50)
  assert.equal(range.min, 10)
  assert.equal(range.max, 50)
  assert.equal(range.rawMin, null)
  assert.equal(range.rawMax, null)
})

test('computeRange with single value expands by +/- 1.0', () => {
  const range = computeRange([25.0], 10, 50)
  assert.equal(range.rawMin, 25.0)
  assert.equal(range.rawMax, 25.0)
  assert.equal(range.min, 24.0)
  assert.equal(range.max, 26.0)
})

test('computeRange calculates 10% padding correctly', () => {
  // span = 30 - 20 = 10, pad = 1
  const range = computeRange([20.0, 25.0, 30.0], 0, 100)
  assert.equal(range.rawMin, 20.0)
  assert.equal(range.rawMax, 30.0)
  assert.equal(range.min, 19.0)
  assert.equal(range.max, 31.0)
})

test('computeRange ignores null, undefined and NaN without converting to zero', () => {
  const range = computeRange([null, undefined, NaN, 50.0, 70.0], 0, 100)
  assert.equal(range.rawMin, 50.0)
  assert.equal(range.rawMax, 70.0)
  // span = 20, pad = 2
  assert.equal(range.min, 48.0)
  assert.equal(range.max, 72.0)
})

test('parseTimestamp parses RFC 3339 strings and numeric values', () => {
  assert.equal(parseTimestamp('1970-01-01T00:00:00Z'), 0)
  assert.equal(parseTimestamp('2026-09-22T08:00:00Z'), 1790064000000)
  assert.equal(parseTimestamp(123456789), 123456789)
  assert.equal(parseTimestamp('not-a-timestamp'), null)
  assert.equal(parseTimestamp(null), null)
})

test('buildChartGeometry on empty input returns hasData false', () => {
  const geo = buildChartGeometry([], { width: 300, height: 180 })
  assert.equal(geo.hasData, false)
  assert.equal(geo.tempSegments.length, 0)
  assert.equal(geo.humSegments.length, 0)
  assert.equal(geo.gasSegments.length, 0)
  assert.equal(geo.gridLines.length, 4)
  assert.equal(geo.xStartText, '--')
  assert.equal(geo.xEndText, '--')
})

test('buildChartGeometry on single point centers X and generates singlePoints', () => {
  const points = [
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 25.0, humidityRh: 60.0, gasPpm: 15.0 },
  ]
  const geo = buildChartGeometry(points, {
    width: 200,
    height: 100,
    padding: { left: 10, right: 10, top: 10, bottom: 10 },
  })

  assert.equal(geo.hasData, true)
  assert.equal(geo.tempSegments.length, 1)
  assert.equal(geo.tempSegments[0].length, 1)
  // X should be center: left 10 + (200 - 20) / 2 = 100
  assert.equal(geo.tempSegments[0][0].x, 100)
  assert.equal(geo.singlePoints.length, 3) // temp, hum, gas all single points
  assert.equal(geo.tempRangeText, '25.0~25.0°C')
  assert.equal(geo.humRangeText, '60.0~60.0%')
  assert.equal(geo.gasRangeText, '15.0~15.0ppm')
})

test('buildChartGeometry breaks gas segment on null and preserves 0.0', () => {
  const points = [
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
    { receivedAt: '2026-09-22T10:01:00Z', temperatureC: 21.0, humidityRh: 51.0, gasPpm: null },
    { receivedAt: '2026-09-22T10:02:00Z', temperatureC: 22.0, humidityRh: 52.0, gasPpm: 0.0 },
    { receivedAt: '2026-09-22T10:03:00Z', temperatureC: 23.0, humidityRh: 53.0, gasPpm: 5.0 },
  ]
  const geo = buildChartGeometry(points, { width: 400, height: 200 })

  // Temperature and Humidity have 1 continuous segment
  assert.equal(geo.tempSegments.length, 1)
  assert.equal(geo.tempSegments[0].length, 4)
  assert.equal(geo.humSegments.length, 1)
  assert.equal(geo.humSegments[0].length, 4)

  // Gas should break into 2 segments: [10.0] and [0.0, 5.0]
  assert.equal(geo.gasSegments.length, 2)
  assert.equal(geo.gasSegments[0].length, 1)
  assert.equal(geo.gasSegments[0][0].value, 10.0)
  assert.equal(geo.gasSegments[1].length, 2)
  assert.equal(geo.gasSegments[1][0].value, 0.0)
  assert.equal(geo.gasSegments[1][1].value, 5.0)

  // 0.0 must not be excluded or converted to null
  assert.equal(geo.gasRangeText, '0.0~10.0ppm')
})

test('buildChartGeometry maps X proportionally by elapsed time', () => {
  const points = [
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
    { receivedAt: '2026-09-22T10:10:00Z', temperatureC: 22.0, humidityRh: 52.0, gasPpm: 12.0 }, // +10 min (10%)
    { receivedAt: '2026-09-22T11:40:00Z', temperatureC: 25.0, humidityRh: 55.0, gasPpm: 15.0 }, // +100 min total
  ]
  const geo = buildChartGeometry(points, {
    width: 1000,
    height: 500,
    padding: { left: 0, right: 0, top: 0, bottom: 0 },
  })

  // t0 = 0 -> x = 0
  // t1 = 10% -> x = 100
  // t2 = 100% -> x = 1000
  assert.equal(geo.tempSegments[0][0].x, 0)
  assert.equal(Math.round(geo.tempSegments[0][1].x), 100)
  assert.equal(geo.tempSegments[0][2].x, 1000)
})

test('buildChartGeometry falls back to uniform spacing on identical timestamps', () => {
  const points = [
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 22.0, humidityRh: 52.0, gasPpm: 12.0 },
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 25.0, humidityRh: 55.0, gasPpm: 15.0 },
  ]
  const geo = buildChartGeometry(points, {
    width: 200,
    height: 100,
    padding: { left: 0, right: 0, top: 0, bottom: 0 },
  })

  assert.equal(geo.tempSegments[0][0].x, 0)
  assert.equal(geo.tempSegments[0][1].x, 100)
  assert.equal(geo.tempSegments[0][2].x, 200)
})

test('buildChartGeometry sorts descending input into ascending order', () => {
  const points = [
    { receivedAt: '2026-09-22T12:00:00Z', temperatureC: 30.0, humidityRh: 40.0, gasPpm: 20.0 },
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 20.0, humidityRh: 60.0, gasPpm: 10.0 },
  ]
  const geo = buildChartGeometry(points, { width: 200, height: 100, padding: { left: 0, right: 0, top: 0, bottom: 0 } })

  assert.equal(geo.tempSegments[0][0].value, 20.0)
  assert.equal(geo.tempSegments[0][1].value, 30.0)
})

test('drawTrendChart executes drawing commands without error', () => {
  const calls = []
  const mockCtx = {
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

  const points = [
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 25.0, humidityRh: 60.0, gasPpm: 15.0 },
  ]
  const geo = buildChartGeometry(points, { width: 300, height: 180 })
  drawTrendChart(mockCtx, geo)

  assert.ok(calls.some((c) => c[0] === 'clearRect'))
  assert.ok(calls.some((c) => c[0] === 'stroke'))
  assert.ok(calls.some((c) => c[0] === 'fill')) // single dot fill
})

// --- timestamp degradation: all-or-nothing uniform fallback ---------------------

function assertUniformByIndex(segment, width, padding) {
  const n = segment.length
  const plotWidth = width - padding.left - padding.right
  const step = plotWidth / (n - 1)
  segment.forEach((p, i) => {
    assert.equal(
      Math.round(p.x * 100) / 100,
      Math.round((padding.left + i * step) * 100) / 100,
      `degraded X must be uniform by index at ${i}`,
    )
  })
}

function assertMonotonicFiniteX(segment) {
  segment.forEach((p, i) => {
    assert.ok(Number.isFinite(p.x), `x[${i}] must be finite`)
  })
  for (let i = 1; i < segment.length; i++) {
    assert.ok(segment[i].x >= segment[i - 1].x, `x must be monotonically non-decreasing at ${i}`)
  }
}

test('aSingleInvalidTimestampInTheMiddleDegradesTheWholeSeriesToUniform', () => {
  const points = [
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
    { receivedAt: 'not-a-timestamp', temperatureC: 22.0, humidityRh: 52.0, gasPpm: 12.0 },
    { receivedAt: '2026-09-22T11:40:00Z', temperatureC: 25.0, humidityRh: 55.0, gasPpm: 15.0 },
  ]
  const padding = { left: 0, right: 0, top: 0, bottom: 0 }
  const geo = buildChartGeometry(points, { width: 300, height: 100, padding })

  assert.equal(geo.usesTimeScale, false, 'one invalid timestamp degrades the whole series')
  assertUniformByIndex(geo.tempSegments[0], 300, padding)
  assertMonotonicFiniteX(geo.tempSegments[0])
})

test('anInvalidFirstOrLastTimestampShowsDashInsteadOfInventingATime', () => {
  const padding = { left: 0, right: 0, top: 0, bottom: 0 }
  const firstInvalid = buildChartGeometry(
    [
      { receivedAt: 'not-a-timestamp', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
      { receivedAt: '2026-09-22T11:00:00Z', temperatureC: 25.0, humidityRh: 55.0, gasPpm: 15.0 },
    ],
    { width: 200, height: 100, padding },
  )
  assert.equal(firstInvalid.usesTimeScale, false)
  assert.equal(firstInvalid.xStartText, '--', 'unreliable start endpoint must read --')
  assert.notEqual(firstInvalid.xEndText, '--', 'reliable end endpoint keeps its clock')

  const lastInvalid = buildChartGeometry(
    [
      { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
      { receivedAt: 'not-a-timestamp', temperatureC: 25.0, humidityRh: 55.0, gasPpm: 15.0 },
    ],
    { width: 200, height: 100, padding },
  )
  assert.equal(lastInvalid.usesTimeScale, false)
  assert.notEqual(lastInvalid.xStartText, '--')
  assert.equal(lastInvalid.xEndText, '--', 'unreliable end endpoint must read --')
})

test('allInvalidTimestampsDegradeToUniformAndShowDashOnBothEnds', () => {
  const points = [
    { receivedAt: 'not-a-timestamp', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
    { receivedAt: 'also-not-a-time', temperatureC: 22.0, humidityRh: 52.0, gasPpm: 12.0 },
    { receivedAt: 'still-not-a-time', temperatureC: 25.0, humidityRh: 55.0, gasPpm: 15.0 },
  ]
  const padding = { left: 0, right: 0, top: 0, bottom: 0 }
  const geo = buildChartGeometry(points, { width: 300, height: 100, padding })

  assert.equal(geo.usesTimeScale, false)
  assert.equal(geo.xStartText, '--')
  assert.equal(geo.xEndText, '--')
  assertUniformByIndex(geo.tempSegments[0], 300, padding)
  assertMonotonicFiniteX(geo.tempSegments[0])
})

test('duplicateTimestampsWithPositiveSpanKeepTimeScaleAndMonotonicX', () => {
  const points = [
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 22.0, humidityRh: 52.0, gasPpm: 12.0 },
    { receivedAt: '2026-09-22T11:00:00Z', temperatureC: 25.0, humidityRh: 55.0, gasPpm: 15.0 },
  ]
  const padding = { left: 0, right: 0, top: 0, bottom: 0 }
  const geo = buildChartGeometry(points, { width: 300, height: 100, padding })

  assert.equal(geo.usesTimeScale, true, 'positive span with duplicates still qualifies for time scale')
  assert.equal(geo.tempSegments[0][0].x, 0, 'duplicate timestamp shares the same X')
  assert.equal(geo.tempSegments[0][1].x, 0)
  assert.equal(geo.tempSegments[0][2].x, 300)
  assertMonotonicFiniteX(geo.tempSegments[0])
})

test('singleSampleIsUniformNotTimeScale', () => {
  const geo = buildChartGeometry(
    [{ receivedAt: '2026-09-22T10:00:00Z', temperatureC: 25.0, humidityRh: 60.0, gasPpm: 15.0 }],
    { width: 200, height: 100 },
  )
  assert.equal(geo.usesTimeScale, false, 'a single sample cannot form a time span')
})

test('allEqualTimestampsDegradeToUniformSpacing', () => {
  const points = [
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 20.0, humidityRh: 50.0, gasPpm: 10.0 },
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 22.0, humidityRh: 52.0, gasPpm: 12.0 },
    { receivedAt: '2026-09-22T10:00:00Z', temperatureC: 25.0, humidityRh: 55.0, gasPpm: 15.0 },
  ]
  const padding = { left: 0, right: 0, top: 0, bottom: 0 }
  const geo = buildChartGeometry(points, { width: 200, height: 100, padding })
  assert.equal(geo.usesTimeScale, false, 'zero time span must degrade to uniform spacing')
  assert.equal(geo.tempSegments[0][0].x, 0)
  assert.equal(geo.tempSegments[0][1].x, 100)
  assert.equal(geo.tempSegments[0][2].x, 200)
})
