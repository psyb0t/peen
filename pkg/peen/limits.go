package peen

import "time"

// ToolLimits bounds file, patch, command and job operations. Zero fields use
// the same defaults as the controller. CommandTimeout and WaitJobTimeout are
// the waits runCommand and waitJob use when a call passes no timeout; a call
// may ask for any wait.
type ToolLimits struct {
	MaxListEntries        int
	MaxListDepth          int
	MaxSearchMatches      int
	MaxSearchFileBytes    int64
	MaxReadBytes          int
	MaxReadLines          int
	MaxWriteBytes         int
	MaxEdits              int
	MaxDiffBytes          int
	MaxPatchBytes         int
	MaxPatchFiles         int
	MaxPatchHunks         int
	MaxPatchChangedBytes  int
	MaxRemoveEntries      int
	MaxCommandOutputBytes int
	CommandTimeout        time.Duration
	MaxJobOutputLines     int
	MaxReadJobOutputLines int
	WaitJobTimeout        time.Duration
	JobStopGracePeriod    time.Duration
	MaxListJobs           int
}

// AgentLimits bounds concurrent children, nesting and live event buffers.
type AgentLimits struct {
	MaxDepth                 int
	MaxChildTurns            int
	MaxConcurrentRuns        int
	MaxEventCount            int
	MaxEventBytes            int
	MaxAdHocInstructionBytes int
}

// HarnessLimits bounds discovery and context assembled from harness files.
type HarnessLimits struct {
	MaxFiles             int
	MaxInstructions      int
	MaxSkills            int
	MaxAgents            int
	MaxEventHandlers     int
	MaxHooks             int
	MaxDirectoryEntries  int
	MaxFileBytes         int64
	MaxTotalContextBytes int64
}

// EventLimits bounds notice data and live subscription buffers.
type EventLimits struct {
	MaxPendingPerSession int
	MaxSummaryBytes      int
	MaxDataBytes         int
	SubscriberBuffer     int
}
