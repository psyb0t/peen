package agent

// WorkspaceToolNames lists built-ins that an embedding caller may disable.
// Messaging remains outside this list and is always available.
func WorkspaceToolNames() []string {
	return []string{
		toolNameListFiles, toolNameSearchText, toolNameReadFile,
		toolNameWriteFile, toolNameEditFile, toolNameApplyPatch,
		toolNameMovePath, toolNameMakeDirectory, toolNameRemovePath,
		toolNameChangeDirectory,
		toolNameRunCommand, toolNameListJobs, toolNameReadJobOutput,
		toolNameWaitJob, toolNameSignalJob, toolNameUseSkill,
		toolNameLaunchAgent,
	}
}
