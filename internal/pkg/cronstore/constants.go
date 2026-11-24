package cronstore

const (
	maxJobNameBytes     = 64
	definitionHashBytes = 64
	maxErrorBytes       = 256
)

// Safe error reasons are the only failure text cron history may persist.
const (
	ErrorRunFailed            = "scheduled run failed"
	ErrorTurnFailed           = "scheduled turn failed"
	ErrorTurnCancelled        = "scheduled turn cancelled"
	ErrorTurnTimeout          = "scheduled turn timed out"
	ErrorPreviousRunning      = "previous run still running"
	ErrorControllerOffline    = "controller was offline"
	ErrorInterrupted          = "scheduled turn interrupted"
	ErrorWorkspaceUnavailable = "workspace unavailable"
	ErrorSessionUnavailable   = "session unavailable"
	ErrorOutputInvalid        = "scheduled output invalid"
	ErrorAgentSpaceConflict   = "workspace agent space conflict"
)
