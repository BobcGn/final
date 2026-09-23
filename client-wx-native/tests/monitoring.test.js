const test = require('node:test')
const assert = require('node:assert/strict')
const monitoringService = require('../services/monitoring.js')
const deviceService = require('../services/device.js')
const { ApiError } = require('../services/request.js')
const { formatRfc3339, genIdempotencyKey } = require('../utils/helpers.js')
const presentation = require('../utils/presentation.js')

test('loadDashboard returns hasData=false when latest telemetry is 404', async () => {
  const origGetStatus = deviceService.getStatus
  const origGetLatest = deviceService.getLatestTelemetry

  deviceService.getStatus = async () => ({
    deviceId: 'MCU001',
    connectivity: 'online',
    alarmState: 'normal',
    localAlarm: false,
    lastSeenAt: '2026-09-23T12:00:00Z',
  })

  deviceService.getLatestTelemetry = async () => {
    throw new ApiError('not_found', 'No telemetry yet', 404)
  }

  try {
    const dash = await monitoringService.loadDashboard('MCU001')
    assert.equal(dash.hasData, false)
    assert.equal(dash.online, true)
    assert.equal(dash.temperatureText, '--')
    assert.equal(dash.humidityText, '--')
    assert.equal(dash.gasText, '--')
    assert.equal(dash.buzzerText, '待机')
  } finally {
    deviceService.getStatus = origGetStatus
    deviceService.getLatestTelemetry = origGetLatest
  }
})

test('loadDashboard throws when getStatus fails with 500', async () => {
  const origGetStatus = deviceService.getStatus
  deviceService.getStatus = async () => {
    throw new ApiError('internal_error', 'Database down', 500)
  }

  try {
    await assert.rejects(
      async () => {
        await monitoringService.loadDashboard('MCU001')
      },
      (err) => err.statusCode === 500
    )
  } finally {
    deviceService.getStatus = origGetStatus
  }
})

test('loadTrends queries with order=desc and reverses to ascending', async () => {
  const origGetHistory = deviceService.getTelemetryHistory
  let queryCaptured = null

  deviceService.getTelemetryHistory = async (_devId, query) => {
    queryCaptured = query
    return {
      items: [
        { receivedAt: '2026-09-23T14:10:00Z', temperatureC: 30, sequence: 3 }, // newest
        { receivedAt: '2026-09-23T14:05:00Z', temperatureC: 25, sequence: 2 },
        { receivedAt: '2026-09-23T14:00:00Z', temperatureC: 20, sequence: 1 }, // oldest
      ],
    }
  }

  try {
    const trends = await monitoringService.loadTrends('MCU001', 'LAST_HOUR', 50)
    assert.equal(queryCaptured.order, 'desc')
    assert.equal(queryCaptured.limit, 50)
    assert.ok(queryCaptured.from < queryCaptured.to)

    // 反转后第一项为 oldest，最后一项为 newest
    assert.equal(trends.series[0].receivedAt, '2026-09-23T14:00:00Z')
    assert.equal(trends.series[2].receivedAt, '2026-09-23T14:10:00Z')
    assert.equal(trends.curveAxisStart, '14:00:00')
    assert.equal(trends.curveAxisEnd, '14:10:00')
  } finally {
    deviceService.getTelemetryHistory = origGetHistory
  }
})

test('loadTrendSamples returns both trendsView and rawPoints', async () => {
  const origGetHistory = deviceService.getTelemetryHistory
  deviceService.getTelemetryHistory = async () => ({
    items: [
      { receivedAt: '2026-09-23T14:10:00Z', temperatureC: 30 },
      { receivedAt: '2026-09-23T14:00:00Z', temperatureC: 20 },
    ],
  })

  try {
    const result = await monitoringService.loadTrendSamples('LAST_HOUR')
    assert.ok(result.trendsView)
    assert.equal(result.rawPoints.length, 2)
    assert.equal(result.rawPoints[0].temperatureC, 20)
    assert.equal(result.rawPoints[1].temperatureC, 30)
  } finally {
    deviceService.getTelemetryHistory = origGetHistory
  }
})

test('loadAlerts, loadSettings, loadCommandStatus forward to deviceService and presentation', async () => {
  const origGetAlerts = deviceService.getAlerts
  const origGetThresholds = deviceService.getThresholds
  const origGetCmd = deviceService.getCommandStatus

  deviceService.getAlerts = async () => ({
    items: [
      {
        id: 'ev-1',
        state: 'fire_warning',
        startedAt: '2026-09-23T10:00:00Z',
        evidence: { gasAdcRise: 200, gasAdcRiseThreshold: 150, temperatureRateCPerMinute: 3.5, sampleCount: 5 },
      },
    ],
  })

  deviceService.getThresholds = async () => ({
    temperatureHighC: 30,
    humidityHighRh: 60,
    gasHighPpm: 80,
    desiredVersion: 1,
    confirmedVersion: 1,
    confirmationState: 'confirmed',
  })

  deviceService.getCommandStatus = async () => ({
    requestId: 'cmd-1',
    state: 'applied',
    confirmedVersion: 2,
  })

  try {
    const alerts = await monitoringService.loadAlerts('MCU001', 10, 'fire_warning')
    assert.equal(alerts.visibleCount, 1)
    assert.equal(alerts.items[0].gasAdcRiseText, '200')

    const settings = await monitoringService.loadSettings('MCU001')
    assert.equal(settings.confirmed, true)
    assert.equal(settings.confirmationText, '设备已确认')

    const cmd = await monitoringService.loadCommandStatus('MCU001', 'cmd-1')
    assert.equal(cmd.confirmed, true)
    assert.equal(cmd.stateText, '设备已确认')
  } finally {
    deviceService.getAlerts = origGetAlerts
    deviceService.getThresholds = origGetThresholds
    deviceService.getCommandStatus = origGetCmd
  }
})

test('updateThresholds rejects invalid values before calling deviceService', async () => {
  let called = false
  const origPut = deviceService.putThresholds
  deviceService.putThresholds = async () => {
    called = true
  }

  try {
    await assert.rejects(
      async () => {
        await monitoringService.updateThresholds('MCU001', {
          temperatureHighC: 99, // 越界
          humidityHighRh: 50,
          gasHighPpm: 50,
        })
      },
      /温度阈值需在 0-80 °C/
    )
    assert.equal(called, false)
  } finally {
    deviceService.putThresholds = origPut
  }
})

test('awaitCommandOutcome polls until applied terminal outcome', async () => {
  const origGetCommand = deviceService.getCommandStatus
  let calls = 0

  deviceService.getCommandStatus = async () => {
    calls++
    if (calls === 1) {
      return { requestId: 'req-1', state: 'published' }
    }
    return { requestId: 'req-1', state: 'applied', confirmedVersion: 5 }
  }

  try {
    const outcome = await monitoringService.awaitCommandOutcome('MCU001', 'req-1', 5, 10)
    assert.equal(calls, 2)
    assert.equal(outcome.confirmed, true)
    assert.equal(outcome.stateText, '设备已确认')
  } finally {
    deviceService.getCommandStatus = origGetCommand
  }
})

test('awaitCommandOutcome retries on 429 or 500 errors and throws on 404', async () => {
  const origGetCommand = deviceService.getCommandStatus
  let calls = 0

  // 1. 重试场景
  deviceService.getCommandStatus = async () => {
    calls++
    if (calls === 1) throw new ApiError('rate_limited', 'Too many requests', 429)
    if (calls === 2) throw new ApiError('server_error', 'Server error', 503)
    return { requestId: 'req-2', state: 'applied' }
  }

  try {
    const outcome = await monitoringService.awaitCommandOutcome('MCU001', 'req-2', 5, 10)
    assert.equal(calls, 3)
    assert.equal(outcome.confirmed, true)
  } finally {
    deviceService.getCommandStatus = origGetCommand
  }

  // 2. 404 立即中断
  deviceService.getCommandStatus = async () => {
    throw new ApiError('not_found', 'Command missing', 404)
  }

  try {
    await assert.rejects(
      async () => {
        await monitoringService.awaitCommandOutcome('MCU001', 'req-missing', 5, 10)
      },
      (err) => err.statusCode === 404
    )
  } finally {
    deviceService.getCommandStatus = origGetCommand
  }
})

test('helpers formatRfc3339 and genIdempotencyKey', () => {
  assert.equal(formatRfc3339(null), '--')
  assert.equal(formatRfc3339('invalid-date'), '--')
  const formatted = formatRfc3339('2026-09-23T14:30:00Z')
  assert.ok(formatted.includes(':'))

  const key1 = genIdempotencyKey()
  const key2 = genIdempotencyKey()
  assert.notEqual(key1, key2)
  assert.equal(key1.length, 36)
})

test('presentation selectors returns window and filter lists', () => {
  const sel = presentation.selectors()
  assert.equal(sel.windows.length, 3)
  assert.equal(sel.filters.length, 4)
})

test('mock service handlers handle status, latest, history, alerts, thresholds and commands', async () => {
  const status = await deviceService.getStatus('MCU001')
  assert.ok(status.deviceId)

  const latest = await deviceService.getLatestTelemetry('MCU001')
  assert.ok(latest.temperatureC)

  const history = await deviceService.getTelemetryHistory('MCU001', { limit: 10, order: 'desc' })
  assert.ok(history.items.length <= 10)

  const alerts = await deviceService.getAlerts('MCU001')
  assert.ok(alerts.items.length > 0)

  const thresholds = await deviceService.getThresholds('MCU001')
  assert.ok(thresholds.desiredVersion)

  const putRes = await deviceService.putThresholds('MCU001', {
    temperatureHighC: 32,
    humidityHighRh: 65,
    gasHighPpm: 90,
  })
  assert.ok(putRes.requestId)

  const cmdStatus = await deviceService.getCommandStatus('MCU001', putRes.requestId)
  assert.ok(cmdStatus.state)
})
