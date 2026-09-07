package tools

import (
	"time"

	"github.com/google/uuid"
)

// EntryType names what a listed path is on the host filesystem. It is an
// alias because it carries no behavior and crosses the JSON tool boundary as
// a plain string.
type EntryType = string

const (
	// EntryTypeFile marks a regular file.
	EntryTypeFile EntryType = "file"
	// EntryTypeDirectory marks a directory.
	EntryTypeDirectory EntryType = "directory"
	// EntryTypeSymlink marks a symbolic link, reported without following it.
	EntryTypeSymlink EntryType = "symlink"
	// EntryTypeOther marks sockets, devices, and named pipes.
	EntryTypeOther EntryType = "other"
)

// Options configures one per-turn executor.
type Options struct {
	// Workspace is the already canonicalized default directory for the turn.
	Workspace string
	Limits    Limits
}

// ListFilesInput selects one directory listing.
type ListFilesInput struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	MaxDepth  int    `json:"maxDepth"`
}

// FileEntry describes one listed path relative to the listed directory.
type FileEntry struct {
	Path string    `json:"path"`
	Type EntryType `json:"type"`
	Size int64     `json:"size"`
	Mode string    `json:"mode"`
}

// ListFilesOutput carries a bounded directory listing in stable path order.
type ListFilesOutput struct {
	Directory string      `json:"directory"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// SearchTextInput selects a bounded literal or regular-expression search.
type SearchTextInput struct {
	Path       string   `json:"path"`
	Pattern    string   `json:"pattern"`
	Regex      bool     `json:"regex"`
	Include    []string `json:"include"`
	MaxMatches int      `json:"maxMatches"`
}

// SearchMatch is one matching line.
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// SearchTextOutput carries bounded matches in stable path and line order.
type SearchTextOutput struct {
	Matches   []SearchMatch `json:"matches"`
	Truncated bool          `json:"truncated"`
}

// ReadFileInput selects one line window. Offset is the 1-based first line.
type ReadFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// ReadFileOutput carries the window plus continuation and identity metadata.
// NextOffset is zero when the window reached the end of the file.
type ReadFileOutput struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	FirstLine  int    `json:"firstLine"`
	LastLine   int    `json:"lastLine"`
	TotalLines int    `json:"totalLines"`
	NextOffset int    `json:"nextOffset"`
	Truncated  bool   `json:"truncated"`
	SHA256     string `json:"sha256"`
}

// WriteFileInput creates or replaces one text file. ExpectedSHA256 is required
// when the path already exists and rejected when it does not.
type WriteFileInput struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expectedSha256"`
}

// WriteFileOutput reports the durable result of one write.
type WriteFileOutput struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}

// TextEdit is one exact replacement. Old must occur exactly once.
type TextEdit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// EditFileInput applies non-overlapping exact replacements to one file.
type EditFileInput struct {
	Path  string     `json:"path"`
	Edits []TextEdit `json:"edits"`
}

// EditFileOutput carries a bounded unified diff and the new content hash.
type EditFileOutput struct {
	Path          string `json:"path"`
	Diff          string `json:"diff"`
	DiffTruncated bool   `json:"diffTruncated"`
	Applied       int    `json:"applied"`
	SHA256        string `json:"sha256"`
}

// PatchOperation identifies one file-level apply_patch action.
type PatchOperation string

// PatchOutcome identifies whether one reported operation fully completed.
type PatchOutcome string

const (
	PatchOperationAdd    PatchOperation = "add"
	PatchOperationUpdate PatchOperation = "update"
	PatchOperationDelete PatchOperation = "delete"
	PatchOperationMove   PatchOperation = "move"

	PatchOutcomeApplied PatchOutcome = "applied"
	PatchOutcomePartial PatchOutcome = "partial"
)

// ApplyPatchInput carries one complete Codex-compatible patch document.
type ApplyPatchInput struct {
	Patch string `json:"patch"`
}

// ApplyPatchFileResult reports one file operation that reached the filesystem.
type ApplyPatchFileResult struct {
	Operation   PatchOperation `json:"operation"`
	Outcome     PatchOutcome   `json:"outcome"`
	Path        string         `json:"path"`
	Destination string         `json:"destination,omitempty"`
	Bytes       int            `json:"bytes"`
	SHA256      string         `json:"sha256,omitempty"`
}

// ApplyPatchOutput reports applied files and one bounded combined diff. Error
// is populated by the agent adapter when a later filesystem operation fails.
type ApplyPatchOutput struct {
	Files         []ApplyPatchFileResult `json:"files"`
	Diff          string                 `json:"diff"`
	DiffTruncated bool                   `json:"diffTruncated"`
	Error         string                 `json:"error,omitempty"`
}

// MovePathInput renames one file or directory. Destination must not exist.
type MovePathInput struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// MovePathOutput reports the resolved endpoints of one rename.
type MovePathOutput struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// MakeDirectoryInput creates a bounded directory tree.
type MakeDirectoryInput struct {
	Path string `json:"path"`
}

// MakeDirectoryOutput lists the directories this call actually created.
type MakeDirectoryOutput struct {
	Path    string   `json:"path"`
	Created []string `json:"created"`
}

// RemovePathInput removes one file or directory. ExpectedSHA256 is required
// for a regular file. Recursive is required for a non-empty directory.
type RemovePathInput struct {
	Path           string `json:"path"`
	Recursive      bool   `json:"recursive"`
	ExpectedSHA256 string `json:"expectedSha256"`
}

// RemovePathOutput reports how many entries the removal deleted.
type RemovePathOutput struct {
	Path    string `json:"path"`
	Removed int    `json:"removed"`
}

// RunCommandInput executes one shell command immediately, no approval step.
// Purpose is required: one short line saying why the agent is running this,
// stored with the resulting job. TimeoutSeconds bounds how long this call
// WAITS for the command, never how long the command may run; a command still
// running when the bound elapses is left running and reported as a job
// handle. Background skips the wait entirely and returns the handle at once.
type RunCommandInput struct {
	Command        string            `json:"command"`
	Directory      string            `json:"directory"`
	Environment    map[string]string `json:"environment"`
	Purpose        string            `json:"purpose"`
	TimeoutSeconds int               `json:"timeoutSeconds"`
	Background     bool              `json:"background"`
}

// RunCommandOutput reports the job this call started and whatever its wait
// bound observed. Running is true when the process was still alive when this
// call returned, whether because Background skipped the wait or because the
// wait bound elapsed first; a running job is never killed by returning it,
// only recorded. Stdout and Stderr carry every line captured so far. ExitCode
// is unknownExitCode until State leaves JobStateRunning with a normal exit.
type RunCommandOutput struct {
	JobID              uuid.UUID `json:"jobId"`
	PID                int       `json:"pid"`
	Directory          string    `json:"directory"`
	Purpose            string    `json:"purpose"`
	Background         bool      `json:"background"`
	Running            bool      `json:"running"`
	State              JobState  `json:"state"`
	ExitCode           int       `json:"exitCode"`
	Stdout             string    `json:"stdout"`
	Stderr             string    `json:"stderr"`
	StdoutDroppedLines int       `json:"stdoutDroppedLines"`
	StderrDroppedLines int       `json:"stderrDroppedLines"`
}

// JobState names the lifecycle stage of one supervised process job. It is an
// alias because it carries no behavior and crosses the JSON tool boundary as
// a plain string.
type JobState = string

const (
	// JobStateRunning marks a job whose process has not yet exited.
	JobStateRunning JobState = "running"
	// JobStateExited marks a job whose process exited on its own, whatever
	// its exit code.
	JobStateExited JobState = "exited"
	// JobStateSignalled marks a job terminated by a signal, whether sent
	// through signal_job, shutdown, or an outside actor.
	JobStateSignalled JobState = "signalled"
	// JobStateFailed marks a job that could not be launched or whose wait
	// failed in a way that is neither a normal exit nor a signal.
	JobStateFailed JobState = "failed"
)

// JobStream selects which output stream(s) read_job_output returns.
type JobStream = string

const (
	// JobStreamStdout selects only standard output.
	JobStreamStdout JobStream = "stdout"
	// JobStreamStderr selects only standard error.
	JobStreamStderr JobStream = "stderr"
	// JobStreamBoth selects both streams. The default when Stream is empty.
	JobStreamBoth JobStream = "both"
)

// JobSignal selects how signal_job asks a job's process group to stop.
type JobSignal = string

const (
	// JobSignalStop requests a graceful SIGTERM, escalating to SIGKILL if
	// the process group does not exit within the configured grace period.
	JobSignalStop JobSignal = "stop"
	// JobSignalKill requests an immediate SIGKILL with no grace period.
	JobSignalKill JobSignal = "kill"
)

// JobSummary describes one job for list_jobs: identity, attribution,
// timing, and how much of its output is currently buffered or already
// dropped.
type JobSummary struct {
	JobID      uuid.UUID `json:"jobId"`
	PID        int       `json:"pid"`
	TurnID     uuid.UUID `json:"turnId"`
	ToolCallID string    `json:"toolCallId"`
	Purpose    string    `json:"purpose"`
	Command    string    `json:"command"`
	Directory  string    `json:"directory"`
	State      JobState  `json:"state"`
	StartedAt  time.Time `json:"startedAt"`
	// omitzero, not omitempty: omitempty does nothing on a struct, so a
	// running job would serialize a zero timestamp instead of no timestamp.
	EndedAt             time.Time `json:"endedAt,omitzero"`
	ExitCode            int       `json:"exitCode"`
	StdoutBufferedLines int       `json:"stdoutBufferedLines"`
	StdoutDroppedLines  int       `json:"stdoutDroppedLines"`
	StderrBufferedLines int       `json:"stderrBufferedLines"`
	StderrDroppedLines  int       `json:"stderrDroppedLines"`
}

// ListJobsInput selects this session's jobs. An empty State lists every job.
type ListJobsInput struct {
	State JobState `json:"state"`
}

// ListJobsOutput carries a bounded, oldest-first list of this session's jobs.
type ListJobsOutput struct {
	Jobs      []JobSummary `json:"jobs"`
	Truncated bool         `json:"truncated"`
}

// ReadJobOutputInput selects a bounded incremental window of one job's
// output. StdoutCursor and StderrCursor are the cursors returned by a
// previous call, or zero to read from the oldest line still buffered.
type ReadJobOutputInput struct {
	JobID        uuid.UUID `json:"jobId"`
	Stream       JobStream `json:"stream"`
	StdoutCursor int       `json:"stdoutCursor"`
	StderrCursor int       `json:"stderrCursor"`
	MaxLines     int       `json:"maxLines"`
}

// ReadJobOutputOutput carries the requested window plus the cursor to
// resume from and how many lines were dropped before the window began.
// Found is false when JobID is not a job in this session.
type ReadJobOutputOutput struct {
	JobID              uuid.UUID `json:"jobId"`
	Found              bool      `json:"found"`
	State              JobState  `json:"state"`
	Stdout             string    `json:"stdout"`
	Stderr             string    `json:"stderr"`
	NextStdoutCursor   int       `json:"nextStdoutCursor"`
	NextStderrCursor   int       `json:"nextStderrCursor"`
	StdoutDroppedLines int       `json:"stdoutDroppedLines"`
	StderrDroppedLines int       `json:"stderrDroppedLines"`
}

// WaitJobInput blocks for one job to leave the running state, up to a bound.
// StdoutCursor and StderrCursor windowed exactly as in ReadJobOutputInput.
type WaitJobInput struct {
	JobID          uuid.UUID `json:"jobId"`
	TimeoutSeconds int       `json:"timeoutSeconds"`
	StdoutCursor   int       `json:"stdoutCursor"`
	StderrCursor   int       `json:"stderrCursor"`
}

// WaitJobOutput reports the job's status once wait_job returns, plus
// whatever output arrived during the wait. Found is false when JobID is not
// a job in this session; Running is true when the bound elapsed first.
type WaitJobOutput struct {
	JobID              uuid.UUID `json:"jobId"`
	Found              bool      `json:"found"`
	Running            bool      `json:"running"`
	State              JobState  `json:"state"`
	ExitCode           int       `json:"exitCode"`
	DurationMs         int64     `json:"durationMs"`
	Stdout             string    `json:"stdout"`
	Stderr             string    `json:"stderr"`
	NextStdoutCursor   int       `json:"nextStdoutCursor"`
	NextStderrCursor   int       `json:"nextStderrCursor"`
	StdoutDroppedLines int       `json:"stdoutDroppedLines"`
	StderrDroppedLines int       `json:"stderrDroppedLines"`
}

// SignalJobInput asks one job's process group to stop or die. Signalling an
// unknown, exited, or already signalled job is idempotent: see
// SignalJobOutput.Found and SignalJobOutput.State.
type SignalJobInput struct {
	JobID  uuid.UUID `json:"jobId"`
	Signal JobSignal `json:"signal"`
}

// SignalJobOutput reports the job's state at the moment signal_job was
// called, not necessarily its state once the signal takes effect. Found is
// false when JobID is not a job in this session; that is reported, not
// failed.
type SignalJobOutput struct {
	JobID  uuid.UUID `json:"jobId"`
	Found  bool      `json:"found"`
	Signal JobSignal `json:"signal"`
	State  JobState  `json:"state"`
}
