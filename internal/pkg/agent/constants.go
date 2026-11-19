package agent

const (
	providerRetryDelayDivisor = 2

	workflowHistoryThresholdNumerator = 7
	workflowHistoryThresholdDivisor   = 10

	fileRecoveryMaxFiles        = 32
	fileRecoveryNoteDivisor     = 8
	fileRecoveryMaxActors       = 64
	fileRecoveryPageSize        = 128
	fileRecoveryInjectionTool   = "fileContextRecovery"
	fileRecoveryRoomDivisor     = 4
	fileRecoveryPerFileTokens   = 2000
	fileRecoveryNoteLead        = `<note kind="fileContext">`
	fileRecoveryAttachmentLead  = "<note kind=\"fileReattached\">\n"
	fileRecoveryAttachmentClose = "\n</note>"

	// fileRecoveryNoteTemplateText lists, one file per line, which ranges
	// read earlier are still complete in the request and which were lost.
	// Content hashes are left out: the model cannot use them, and a summary
	// copying them spends room on nothing.
	//
	//nolint:lll // Model-facing lines stay whole.
	fileRecoveryNoteTemplateText = `<note kind="fileContext">
Files read earlier in this conversation. Present ranges are complete in this request. Read missing ranges again with readFile before you quote or edit their exact text. Facts the conversation or a summary kept stay usable when the file text is gone, but do not rebuild exact file text from a summary.
{{range .Files}}- {{printf "%q" .Path}}:{{if .Full}} present lines {{lineRanges .Full}};{{end}}{{if .Missing}} missing lines {{lineRanges .Missing}};{{end}}{{if .Changed}} changed since it was read;{{end}}{{if .Unavailable}} no longer readable;{{end}}
{{end}}{{if .Omitted}}{{.Omitted}} older files are left out. Read their exact text again before you use it.
{{end}}</note>`
	fileRecoveryNoteTemplateName = "fileContext"
	lineRangesFunctionName       = "lineRanges"
	lineRangeFormat              = "%d-%d"
	lineRangeSeparator           = ", "
)
