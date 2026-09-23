/**
 * 契约冻结的阈值范围限制常量与本地校验。
 * 事实源：docs/api/openapi.yaml 与 client-kmp ThresholdLimits。
 */

const ThresholdLimits = {
  TEMPERATURE_MIN_C: 0.0,
  TEMPERATURE_MAX_C: 80.0,
  HUMIDITY_MIN_RH: 0.0,
  HUMIDITY_MAX_RH: 100.0,
  GAS_MIN_PPM: 1.0,
  GAS_MAX_PPM: 999.0,
}

/**
 * 校验即将下发的阈值对象。
 * 文案与 client-kmp 保持严格一致。
 * @param {Object} update { temperatureHighC, humidityHighRh, gasHighPpm }
 * @throws {Error} 校验失败抛出带提示文案的 Error
 */
function validateThresholds(update) {
  if (!update) throw new Error('阈值参数不能为空')
  const { temperatureHighC, humidityHighRh, gasHighPpm } = update

  if (
    temperatureHighC == null ||
    !Number.isFinite(Number(temperatureHighC)) ||
    Number(temperatureHighC) < ThresholdLimits.TEMPERATURE_MIN_C ||
    Number(temperatureHighC) > ThresholdLimits.TEMPERATURE_MAX_C
  ) {
    throw new Error('温度阈值需在 0-80 °C')
  }

  if (
    humidityHighRh == null ||
    !Number.isFinite(Number(humidityHighRh)) ||
    Number(humidityHighRh) < ThresholdLimits.HUMIDITY_MIN_RH ||
    Number(humidityHighRh) > ThresholdLimits.HUMIDITY_MAX_RH
  ) {
    throw new Error('湿度阈值需在 0-100 %RH')
  }

  if (
    gasHighPpm == null ||
    !Number.isFinite(Number(gasHighPpm)) ||
    Number(gasHighPpm) < ThresholdLimits.GAS_MIN_PPM ||
    Number(gasHighPpm) > ThresholdLimits.GAS_MAX_PPM
  ) {
    throw new Error('气体阈值需在 1-999 ppm')
  }
}

module.exports = {
  ThresholdLimits,
  validateThresholds,
}
