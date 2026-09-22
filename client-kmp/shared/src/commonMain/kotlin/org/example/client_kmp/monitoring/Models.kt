package org.example.client_kmp.monitoring

import kotlinx.serialization.Serializable

/**
 * Connectivity values defined by `docs/api/openapi.yaml`.
 *
 * Decoding is strict: an unexpected value is a contract break and must surface
 * as a parse failure rather than be silently coerced.
 */
@Serializable
enum class Connectivity { online, offline, unknown }

/**
 * Composite warning lifecycle defined by the backend contract.
 *
 * Phase 1 has no explicit acknowledgement endpoint, so `acknowledged` is
 * deliberately absent and must not be invented by a client.
 */
@Serializable
enum class AlertState { normal, suspect, fire_warning, recovered }

/** Per-device reporting link state carried by a telemetry sample. */
@Serializable
enum class NetworkState { online, reconnecting }

/**
 * Current device state.
 *
 * `lastSeenAt` is required by the contract but nullable, so an absent value is
 * indistinguishable from "never seen" and is never defaulted to a timestamp.
 * Times are RFC 3339 strings owned by the backend; clients only display them.
 */
@Serializable
data class DeviceStatus(
    val deviceId: String,
    val connectivity: Connectivity,
    val alarmState: AlertState,
    val lastSeenAt: String? = null,
    val localAlarm: Boolean = false,
    val offlineAfterSeconds: Int? = null,
)

/**
 * One accepted telemetry point.
 *
 * `gasPpm` is nullable because the MQ135 estimate is uncalibrated; a null value
 * means "not measured" and must never be rendered as `0`.
 * `bootId`/`sequence` are the device ordering key and stay null until the
 * firmware reports them.
 *
 * `alarmCauses` is kept as raw strings rather than an enum: no UI branches on it,
 * so an added cause must pass through instead of failing the whole page. The
 * frozen vocabulary is in `docs/api/openapi.yaml` (`AlarmCause`).
 */
@Serializable
data class TelemetryPoint(
    val deviceId: String,
    val receivedAt: String,
    val temperatureC: Double,
    val humidityRh: Double,
    val gasAdcRaw: Int,
    val gasAdcFiltered: Int,
    val localAlarm: Boolean,
    val gasPpm: Double? = null,
    val gasCalibrated: Boolean = false,
    val alarmCauses: List<String> = emptyList(),
    val network: NetworkState = NetworkState.online,
    val sensorFault: Boolean = false,
    val bootId: String? = null,
    val sequence: Long? = null,
    val timestamp: String? = null,
)

/** Cursor page returned by the telemetry history endpoint. */
@Serializable
data class TelemetryPage(val items: List<TelemetryPoint> = emptyList(), val nextCursor: String? = null)

/**
 * Evidence persisted at alert trigger time.
 *
 * Clients must not reconstruct the reason for a historical alert from the
 * latest live values, so this is read from the event and never recomputed.
 * The gas term is an ADC-code rise, which stays meaningful while the ppm
 * estimate is uncalibrated.
 */
@Serializable
data class AlertEvidence(
    val gasAdcRise: Int,
    val temperatureRateCPerMinute: Double,
    val sampleCount: Int,
    val gasAdcRiseThreshold: Int? = null,
    val temperatureRateThresholdCPerMinute: Double? = null,
    val windowSeconds: Int? = null,
)

/** One persisted composite-alert event and its trigger-time evidence. */
@Serializable
data class AlertEvent(
    val id: String,
    val deviceId: String,
    val state: AlertState,
    val startedAt: String,
    val evidence: AlertEvidence,
    val endedAt: String? = null,
)

/** Cursor page returned by the alert history endpoint. */
@Serializable
data class AlertPage(val items: List<AlertEvent> = emptyList(), val nextCursor: String? = null)

/** Device-confirmation lifecycle of a threshold version. */
@Serializable
enum class ConfirmationState { confirmed, pending, rejected, timed_out }

/**
 * Desired and device-confirmed thresholds.
 *
 * A `confirmedVersion` below `desiredVersion` means the command is still
 * pending, failed, or the device is offline; the desired value is never
 * presented as if the device had confirmed it.
 */
@Serializable
data class Thresholds(
    val desiredVersion: Int,
    val temperatureHighC: Double,
    val humidityHighRh: Double,
    val gasHighPpm: Double,
    val confirmedVersion: Int? = null,
    val updatedAt: String? = null,
    val confirmationState: ConfirmationState = ConfirmationState.pending,
)

/** Threshold payload accepted by `PUT /thresholds`; ranges mirror the contract. */
@Serializable
data class ThresholdUpdate(
    val temperatureHighC: Double,
    val humidityHighRh: Double,
    val gasHighPpm: Double,
)

/** Backend acknowledgement that a command was enqueued; never a device confirmation. */
@Serializable
data class CommandAccepted(
    val requestId: String,
    val status: String,
    val desiredVersion: Int? = null,
    val expiresAt: String? = null,
)

/** Lifecycle states of a control command as recorded by the backend. */
@Serializable
enum class CommandState {
    accepted,
    published,
    applied,
    rejected,
    expired,
    duplicate,
    failed,
    timed_out,
    publish_failed,
}

/** Recorded lifecycle of a control command, fetched by `requestId`. */
@Serializable
data class CommandStatus(
    val requestId: String,
    val deviceId: String,
    val type: String,
    val state: CommandState,
    val acceptedAt: String,
    val completedAt: String? = null,
    val expiresAt: String? = null,
    val desiredVersion: Int? = null,
    val confirmedVersion: Int? = null,
    val errorCode: String? = null,
)

@Serializable
internal data class ApiErrorEnvelope(val error: ApiErrorBody? = null)

@Serializable
internal data class ApiErrorBody(
    val code: String = "unknown",
    val message: String = "请求失败",
    val requestId: String? = null,
)
