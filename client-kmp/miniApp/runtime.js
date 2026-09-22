/**
 * Boundary between the WeChat host UI and the KMP shared runtime.
 *
 * `./kotlin` is generated output: run `./gradlew prepareMiniAppHost` in
 * `client-kmp` before opening this project in WeChat DevTools. That task copies
 * the CommonJS distribution published by the `io.github.bobcgn.miniapp` plugin,
 * whose entry file name comes from the Gradle project path and is therefore
 * `client-kmp-shared-miniapp.js`. The directory is git-ignored on purpose.
 *
 * Only this module knows the generated layout. Page code imports `runtime.js`
 * and never reaches into `./kotlin` itself.
 */

const ENTRY_FILE = 'client-kmp-shared-miniapp.js'

let bundle
try {
  bundle = require('./kotlin/' + ENTRY_FILE)
} catch (cause) {
  throw new Error(
    'KMP MiniApp bundle is missing or unreadable. Run ' +
      '`./gradlew --no-configuration-cache prepareMiniAppHost` in client-kmp and reopen the project. (' +
      ENTRY_FILE + ': ' + (cause && cause.message) + ')'
  )
}

/**
 * `@JsExport` objects are published as members of their Kotlin package, so the
 * export is reached through `org.example.client_kmp.monitoring`, not from the
 * bundle root. Verified against the generated bundle, not assumed.
 */
const monitor =
  bundle.org &&
  bundle.org.example &&
  bundle.org.example.client_kmp &&
  bundle.org.example.client_kmp.monitoring &&
  bundle.org.example.client_kmp.monitoring.LabMonitorExports

if (!monitor) {
  throw new Error(
    'LabMonitorExports was not found in the generated bundle. Rebuild with ' +
      '`./gradlew --no-configuration-cache prepareMiniAppHost`.'
  )
}

/**
 * Normalises a rejected Kotlin throwable into a plain `Error`.
 *
 * A `MonitoringException` crosses the JS boundary as an object carrying `code`
 * and `statusCode`; both are copied onto the Error so page code can branch on
 * `err.code` without depending on the Kotlin class shape.
 *
 * @param {*} cause value delivered by the rejected Promise.
 * @returns {Error} an Error whose `message` is always displayable, optionally
 *   carrying `code` and `statusCode`.
 */
function toError(cause) {
  if (cause instanceof Error) {
    if (cause.code === undefined && cause.name && cause.name !== 'Error') {
      // Kotlin throwables arrive with a Kotlin class name; keep it for logs but
      // prefer the message for the user.
      cause.message = cause.message || cause.name
    }
    return cause
  }
  const error = new Error((cause && cause.message) || String(cause) || '请求失败')
  if (cause && cause.code !== undefined) error.code = cause.code
  if (cause && cause.statusCode !== undefined) error.statusCode = cause.statusCode
  return error
}

/**
 * Wraps one exported accessor so every failure reaches the page as a normalised
 * Error instead of a raw Kotlin throwable.
 *
 * @param {string} name exported function name.
 * @returns {Function} the accessor returning a Promise.
 */
function call(name) {
  return function () {
    const args = Array.prototype.slice.call(arguments)
    return Promise.resolve()
      .then(function () {
        return monitor[name].apply(monitor, args)
      })
      .catch(function (cause) {
        throw toError(cause)
      })
  }
}

module.exports = {
  /** Applies the backend address; must run before any data call. */
  configure: function (baseUrl, deviceId) {
    monitor.configure(baseUrl, deviceId)
  },
  dashboard: call('dashboard'),
  trends: call('trends'),
  alerts: call('alerts'),
  settings: call('settings'),
  commandStatus: call('commandStatus'),
  awaitCommandOutcome: call('awaitCommandOutcome'),
  updateThresholds: call('updateThresholds'),
  /**
   * The selector option lists, as a JSON string.
   *
   * Synchronous because it performs no I/O: the page draws the trends window
   * selector before it has fetched any history, and WXML cannot enumerate a
   * Kotlin enum, so the labels have to come from the shared layer.
   */
  selectors: function () {
    try {
      return monitor.selectors()
    } catch (cause) {
      throw toError(cause)
    }
  },
}
