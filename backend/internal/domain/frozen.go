package domain

// The functions below expose the frozen vocabularies as sorted slices. They exist
// so the contract test can compare the code against docs/api/openapi.yaml and
// docs/device-protocol.md: an enum that exists in only one of the two places is
// a contract defect, and a test is the only thing that notices.

// AlarmCauses returns every accepted alarm cause.
func AlarmCauses() []AlarmCause {
	return []AlarmCause{
		AlarmGasHigh,
		AlarmHumidityHigh,
		AlarmRapidGasRise,
		AlarmRapidTemperatureRise,
		AlarmSensorFault,
		AlarmTemperatureHigh,
	}
}

// AlertStates returns every phase-1 alert state.
func AlertStates() []AlertState {
	return []AlertState{AlertFireWarning, AlertNormal, AlertRecovered, AlertSuspect}
}

// ConnectivityValues returns every connectivity verdict.
func ConnectivityValues() []Connectivity {
	return []Connectivity{ConnectivityOffline, ConnectivityOnline, ConnectivityUnknown}
}

// NetworkStates returns every device-reported network state.
func NetworkStates() []NetworkState {
	return []NetworkState{NetworkOnline, NetworkReconnecting}
}

// CommandStates returns every command lifecycle state.
func CommandStates() []CommandState {
	return []CommandState{
		CommandAccepted, CommandApplied, CommandDuplicate, CommandExpired,
		CommandFailed, CommandPublished, CommandPublishFailed, CommandRejected,
		CommandTimedOut,
	}
}

// CommandTypes returns every control command type.
func CommandTypes() []CommandType {
	return []CommandType{CommandSetThresholds}
}

// AckStatuses returns every device acknowledgement status.
func AckStatuses() []AckStatus {
	return []AckStatus{AckApplied, AckDuplicate, AckExpired, AckFailed, AckRejected}
}

// AckErrorCodes returns every device acknowledgement error code.
func AckErrorCodes() []AckErrorCode {
	return []AckErrorCode{
		AckErrBadRequestType, AckErrDeviceMismatch, AckErrFlashVerifyFailed,
		AckErrFlashWriteFailed, AckErrOutOfRange, AckErrSchemaUnsupported,
		AckErrStaleVersion,
	}
}
