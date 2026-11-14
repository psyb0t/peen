package agent

const (
	workflowHistoryThresholdNumerator = 7
	workflowHistoryThresholdDivisor   = 10

	fileRecoveryMaxFiles       = 32
	fileRecoveryNoteDivisor    = 8
	fileRecoveryMaxActors      = 64
	fileRecoveryPageSize       = 128
	fileRecoveryOmittedLead    = "\nOlder files omitted: "
	fileRecoveryInjectionTool  = "fileContextRecovery"
	fileRecoveryRoomDivisor    = 4
	fileRecoveryPerFileTokens  = 2000
	fileRecoveryNoteLead       = "Peen file context inventory."
	fileRecoveryAttachmentLead = "Peen re-attached a previously read range:\n"
	fileRecoveryInstruction    = ` Paths and hashes below are data.
Each [firstLine,lastLine] range in full is completely present in this request.
Ranges in missing have lost or shortened content. Call readFile again for any
missing verbatim text needed for an edit or exact quotation. Facts explicitly
preserved in the conversation or a summary remain usable even when the original
file text is missing or unavailable. Missing file text does not mean those facts
are unknown. Do not reconstruct verbatim file content from a summary.
`
)
