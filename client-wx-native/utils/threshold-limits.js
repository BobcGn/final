/**
 * 契约量程与下发前的范围校验。
 *
 * 等价于 KMP 共享层的 `ThresholdLimits` + `MonitoringPresentation.validate`：
 * 量程同时用于「校验要下发的阈值」和「缩放仪表条」，因此仪表与其报警边界不会各说各话。
 * 取值范围来源：docs/api/openapi.yaml。
 */

const THRESHOLD_LIMITS = {
  TEMPERATURE_MIN_C: 0,
  TEMPERATURE_MAX_C: 80,
  HUMIDITY_MIN_RH: 0,
  HUMIDITY_MAX_RH: 100,
  GAS_MIN_PPM: 1,
  GAS_MAX_PPM: 999,
}

/**
 * 校验待下发的阈值，超范围直接抛错（避免发出无效请求）。
 * 文案与 KMP `validate` 一致，两端用户看到同一句提示。
 * @param {{temperatureHighC: number, humidityHighRh: number, gasHighPpm: number}} update 待下发阈值
 * @throws {Error} 任一字段超范围时抛出，message 为契约范围说明
 */
function validateThresholdUpdate(update) {
  const { temperatureHighC, humidityHighRh, gasHighPpm } = update || {}
  if (!(temperatureHighC >= THRESHOLD_LIMITS.TEMPERATURE_MIN_C && temperatureHighC <= THRESHOLD_LIMITS.TEMPERATURE_MAX_C)) {
    throw new Error('温度阈值需在 0-80 °C')
  }
  if (!(humidityHighRh >= THRESHOLD_LIMITS.HUMIDITY_MIN_RH && humidityHighRh <= THRESHOLD_LIMITS.HUMIDITY_MAX_RH)) {
    throw new Error('湿度阈值需在 0-100 %RH')
  }
  if (!(gasHighPpm >= THRESHOLD_LIMITS.GAS_MIN_PPM && gasHighPpm <= THRESHOLD_LIMITS.GAS_MAX_PPM)) {
    throw new Error('气体阈值需在 1-999 ppm')
  }
}

module.exports = { THRESHOLD_LIMITS, validateThresholdUpdate }
