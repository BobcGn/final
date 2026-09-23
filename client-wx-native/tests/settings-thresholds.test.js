const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')

test('settings preserves the server humidity limit when saving two visible sliders', async () => {
  let page
  let submitted
  const device = {
    getThresholds: async () => ({
      temperatureHighC: 35,
      humidityHighRh: 72,
      gasHighPpm: 80,
      desiredVersion: 3,
      confirmedVersion: 3,
      confirmationState: 'confirmed',
      updatedAt: '2026-09-23T05:00:00Z',
    }),
    putThresholds: async (_deviceId, payload) => {
      submitted = payload
      return { desiredVersion: 4 }
    },
    getCommandStatus: async () => ({
      requestId: 'test-req',
      state: 'applied',
    }),
  }
  const monitoring = {
    loadSettings: async () => {
      const t = await device.getThresholds()
      return {
        temperatureHighC: t.temperatureHighC,
        humidityHighRh: t.humidityHighRh,
        gasHighPpm: t.gasHighPpm,
        desiredVersion: t.desiredVersion,
        confirmedVersion: t.confirmedVersion,
        confirmationState: t.confirmationState,
        confirmationText: '设备已确认',
        confirmationTone: 'mint',
      }
    },
    updateThresholds: async (_deviceId, payload) => {
      submitted = payload
      return { requestId: 'test-req', stateText: '等待设备确认', tone: 'warning' }
    },
    awaitCommandOutcome: async () => ({
      requestId: 'test-req',
      stateText: '设备已确认',
      tone: 'mint',
      confirmed: true,
    }),
  }
  const dependencies = {
    '../../services/monitoring.js': monitoring,
    '../../services/device.js': device,
    '../../utils/helpers.js': { formatRfc3339: (value) => value },
  }
  const source = fs.readFileSync(path.join(__dirname, '../pages/settings/settings.js'), 'utf8')
  vm.runInNewContext(source, {
    require: (id) => dependencies[id],
    Page: (definition) => { page = definition },
    wx: { showToast: () => {}, showLoading: () => {}, hideLoading: () => {} },
    setTimeout: () => {},
  })
  page.data = { ...page.data }
  page.setData = (update) => Object.assign(page.data, update)

  await page.fetch()
  page.onTempChange({ detail: { value: 36 } })
  page.onGasChange({ detail: { value: 65 } })
  await page.onSave()

  assert.deepEqual({ ...submitted }, {
    temperatureHighC: 36,
    humidityHighRh: 72,
    gasHighPpm: 65,
  })
})
