package agent

const (
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
missing content you need. Never rely on memory or a summary for exact file text.
`
)
