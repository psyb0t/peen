package cronconfig

const (
	cronConfigFileName      = "cron.yaml"
	maximumConfigBytes      = 1 << 20
	maximumJobs             = 256
	maximumYAMLNodes        = 10000
	maximumYAMLAliases      = 64
	maximumYAMLDepth        = 64
	maximumJobNameBytes     = 64
	maximumScheduleBytes    = 256
	maximumTimeZoneBytes    = 128
	maximumWorkspaceBytes   = 4096
	maximumAgentSpaceBytes  = 128
	maximumModelIDBytes     = 256
	maximumEffortBytes      = 16
	maximumTimeoutBytes     = 64
	defaultMaximumMessage   = 262144
	defaultMaximumSchema    = 65536
	defaultTimeZone         = "UTC"
	defaultSessionMode      = SessionContinue
	yamlMappingPairWidth    = 2
	cronIntervalFieldCount  = 2
	yamlStringTag           = "!!str"
	jobFieldName            = "name"
	jobFieldSchedule        = "schedule"
	jobFieldMessage         = "message"
	jobFieldReasoningEffort = "reasoningEffort"
	jobFieldTimeZone        = "timeZone"
	jobFieldModel           = "model"
	jobFieldTimeout         = "timeout"
	jobFieldWorkspace       = "workspace"
	jobFieldAgentSpace      = "agentSpace"
	jobFieldSession         = "session"
	jobFieldOutputSchema    = "outputSchema"
	jobFieldTelegramChatID  = "telegramChatId"
	cronJobsField           = "jobs"
)

const (
	// SessionContinue keeps an existing session when a job runs.
	SessionContinue = "continue"
	// SessionNew creates a new session when a job runs.
	SessionNew = "new"
)
