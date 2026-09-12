package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/commander"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

const (
	shellPath = "/bin/sh"
	shellFlag = "-c"

	// unknownExitCode reports a job whose numeric exit status this package
	// cannot determine: still running, terminated by a signal, or failed to
	// launch.
	unknownExitCode = -1

	// jobStreamChannelBuffer sizes the channels this package hands to
	// commander's Stream. commander drops a line to a subscriber that blocks
	// for more than 100ms, so a small buffer absorbs bursts without this
	// package ever needing to apply backpressure to the process.
	jobStreamChannelBuffer = 64

	// jobEventSource identifies this package as the origin of job.* events.
	jobEventSource = "tools.job"

	// exitCodeMarker is the fixed prefix commander v0.5.8 folds a failed
	// process's numeric exit code into, inside the message of the error its
	// Process.Wait returns. See exitCodeFromWaitError.
	exitCodeMarker = "(exit "

	jobOperationRunCommand = "run_command"
	jobOutputStdout        = "stdout"
	jobOutputStderr        = "stderr"
)

// EventPublisher publishes job completion notices without coupling jobs to a
// concrete bus. A nil publisher means "do not publish".
type EventPublisher = events.Publisher

// JobOutputRecord is one captured line before it reaches an in-memory buffer.
type JobOutputRecord struct {
	Stream    JobStream
	Content   string
	CreatedAt time.Time
}

// JobObserver persists process lifecycle data before this registry exposes it
// to live readers. A nil observer leaves the registry usable for standalone
// callers that do not configure durable storage.
type JobObserver interface {
	JobStarted(context.Context, JobSnapshot) error
	JobOutput(context.Context, JobSnapshot, JobOutputRecord) error
	JobSignal(context.Context, JobSnapshot, JobSignal) error
	JobFinished(context.Context, JobSnapshot) error
}

// jobEventData is the structured payload carried by every job.* notice.
type jobEventData struct {
	JobID      uuid.UUID `json:"jobId"`
	ExitCode   int       `json:"exitCode"`
	DurationMs int64     `json:"durationMs"`
}

// Job is one supervised process: what started it, its host process, and its
// captured output. The identity fields are set once at creation and never
// change; state, EndedAt, and ExitCode are read and written through mu so a
// concurrent reader never observes a torn update.
type Job struct {
	ID         uuid.UUID
	PID        int
	SessionID  uuid.UUID
	TurnID     uuid.UUID
	ToolCallID string
	Purpose    string
	Command    string
	Directory  string
	StartedAt  time.Time

	stdout *jobBuffer
	stderr *jobBuffer

	process    commander.Process
	done       chan struct{}
	streamDone sync.WaitGroup
	outputMu   sync.Mutex

	mu             sync.RWMutex
	state          JobState
	endedAt        time.Time
	exitCode       int
	failureDetail  string
	persistenceErr error
}

// JobSnapshot is one point-in-time, race-free view of a job.
type JobSnapshot struct {
	ID                  uuid.UUID
	PID                 int
	SessionID           uuid.UUID
	TurnID              uuid.UUID
	ToolCallID          string
	Purpose             string
	Command             string
	Directory           string
	State               JobState
	StartedAt           time.Time
	EndedAt             time.Time
	ExitCode            int
	FailureDetail       string
	StdoutBufferedLines int
	StdoutDroppedLines  int
	StderrBufferedLines int
	StderrDroppedLines  int
}

// Snapshot copies every field a caller outside this package may read,
// so list_jobs, read_job_output, wait_job, and signal_job never touch a
// Job's internal locks directly.
func (j *Job) Snapshot() JobSnapshot {
	j.mu.RLock()
	state, endedAt, exitCode := j.state, j.endedAt, j.exitCode
	failureDetail := j.failureDetail
	j.mu.RUnlock()

	stdoutLines, stdoutDropped := j.stdout.Stats()
	stderrLines, stderrDropped := j.stderr.Stats()

	return JobSnapshot{
		ID:                  j.ID,
		PID:                 j.PID,
		SessionID:           j.SessionID,
		TurnID:              j.TurnID,
		ToolCallID:          j.ToolCallID,
		Purpose:             j.Purpose,
		Command:             j.Command,
		Directory:           j.Directory,
		State:               state,
		StartedAt:           j.StartedAt,
		EndedAt:             endedAt,
		ExitCode:            exitCode,
		FailureDetail:       failureDetail,
		StdoutBufferedLines: stdoutLines,
		StdoutDroppedLines:  stdoutDropped,
		StderrBufferedLines: stderrLines,
		StderrDroppedLines:  stderrDropped,
	}
}

// Done reports the channel this package closes exactly once, the moment the
// job leaves JobStateRunning.
// ReadStdout returns a bounded window of the job's stdout from a 1-based
// cursor, the cursor to resume from, and how many lines were dropped before
// the window. It exists so a reader outside a turn, such as the HTTP job
// endpoints, can follow a job without constructing a JobExecutor.
func (j *Job) ReadStdout(cursor, maxLines int) ([]string, int, int) {
	return j.stdout.Read(cursor, maxLines)
}

// ReadStderr is ReadStdout for the job's stderr.
func (j *Job) ReadStderr(cursor, maxLines int) ([]string, int, int) {
	return j.stderr.Read(cursor, maxLines)
}

func (j *Job) Done() <-chan struct{} {
	return j.done
}

// requestStop sends SIGTERM to the process group and escalates to SIGKILL
// after grace via commander's Stop. The job's own monitor goroutine is what
// finalizes state and publishes the completion event once the process
// actually exits, whichever caller's Stop/Kill call commander's internal
// once-guard happens to run, so a non-nil result here is only logged, not
// acted on further. The stop itself deliberately runs on a detached
// context: it must complete on its own schedule, never truncated by the
// calling turn's context ending, exactly like Shutdown's own use of a
// fresh context below; ctx is used only to log the outcome with the
// caller's scope attached.
func (j *Job) requestStop(ctx context.Context, grace time.Duration) {
	stopCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	//nolint:contextcheck // Deliberately detached from ctx; see the comment
	// above requestStop.
	if err := j.process.Stop(stopCtx); err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"job stop request finished",
			"job_id", j.ID,
			"err", err,
		)
	}
}

// requestKill sends SIGKILL to the process group immediately via commander's
// Kill. See requestStop for why the result is only logged and ctx is used
// solely for that logging.
func (j *Job) requestKill(ctx context.Context) {
	//nolint:contextcheck // Deliberately detached from ctx; see the comment
	// above requestStop.
	if err := j.process.Kill(context.Background()); err != nil {
		ctxscope.GetLogger(ctx).Debug(
			"job kill request finished",
			"job_id", j.ID,
			"err", err,
		)
	}
}

// JobRegistry owns every process job for one session, deliberately
// outliving any single turn: a job started in one turn stays visible,
// readable, and killable from a later one.
type JobRegistry struct {
	sessionID uuid.UUID
	publisher EventPublisher
	limits    Limits
	cmdr      commander.Commander
	metrics   *metrics.Metrics

	observerMu sync.RWMutex
	observer   JobObserver

	mu   sync.RWMutex
	jobs map[uuid.UUID]*Job
}

// SetObserver configures lifecycle persistence for jobs this registry starts.
// Call it during session setup, before the registry becomes visible to tools.
func (r *JobRegistry) SetObserver(observer JobObserver) {
	r.observerMu.Lock()
	defer r.observerMu.Unlock()

	r.observer = observer
}

// NewJobRegistry builds a session-scoped job registry. publisher may be nil,
// meaning completion events are not published.
func NewJobRegistry(
	sessionID uuid.UUID,
	publisher EventPublisher,
	limits Limits,
	collectors ...*metrics.Metrics,
) (*JobRegistry, error) {
	if sessionID == uuid.Nil {
		return nil, ctxerrors.Wrap(
			ErrInvalidOptions,
			"job registry session is required",
		)
	}

	resolved := limits.withDefaults()
	if err := resolved.validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate job registry limits")
	}

	var collector *metrics.Metrics
	if len(collectors) > 0 {
		collector = collectors[0]
	}

	return &JobRegistry{
		sessionID: sessionID,
		publisher: publisher,
		limits:    resolved,
		cmdr:      commander.New(),
		metrics:   collector,
		jobs:      map[uuid.UUID]*Job{},
	}, nil
}

// StartJobInput starts one supervised process.
type StartJobInput struct {
	Command    string
	Directory  string
	Env        []string
	Purpose    string
	TurnID     uuid.UUID
	ToolCallID string
}

// Start launches one process through commander and registers it. The
// process's own execution context is detached from ctx on purpose:
// cancelling ctx (a turn ending or its own bound expiring) must never kill
// a job, only an explicit signal, Shutdown, or the process exiting does.
// ctx is only used for a pre-flight cancellation check and to log from the
// goroutines this call spawns.
func (r *JobRegistry) Start(
	ctx context.Context,
	input StartJobInput,
) (*Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "start job context")
	}

	execCtx := context.WithoutCancel(ctx)

	process, err := r.cmdr.Start(
		execCtx,
		shellPath,
		[]string{shellFlag, input.Command},
		commander.WithDir(input.Directory),
		commander.WithEnv(input.Env),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(ErrCommandStartFailed, "launch command")
	}

	job := r.newJob(process, input)
	persistCtx := context.WithoutCancel(ctx)
	if err := r.observeStarted(persistCtx, job.Snapshot()); err != nil {
		job.requestKill(persistCtx)
		if waitErr := job.process.Wait(); waitErr != nil {
			ctxscope.GetLogger(persistCtx).Debug(
				"wait for unpersisted job after kill",
				"err", waitErr,
				"job_id", job.ID,
			)
		}

		return nil, ctxerrors.Wrap(err, "persist started job")
	}
	startJobStreams(persistCtx, r, job)

	r.mu.Lock()
	r.jobs[job.ID] = job
	r.mu.Unlock()
	r.metrics.JobStarted()

	go r.monitor(persistCtx, job)

	return job, nil
}

// newJob builds the registry entry for a process commander has already
// started, with the ring buffers sized from this registry's own limits.
func (r *JobRegistry) newJob(
	process commander.Process,
	input StartJobInput,
) *Job {
	maxLines := r.limits.MaxJobOutputLines
	maxBytes := r.limits.MaxCommandOutputBytes

	return &Job{
		ID:         uuid.New(),
		PID:        process.PID(),
		SessionID:  r.sessionID,
		TurnID:     input.TurnID,
		ToolCallID: input.ToolCallID,
		Purpose:    input.Purpose,
		Command:    input.Command,
		Directory:  input.Directory,
		StartedAt:  time.Now().UTC(),
		stdout:     newJobBuffer(maxLines, maxBytes),
		stderr:     newJobBuffer(maxLines, maxBytes),
		process:    process,
		done:       make(chan struct{}),
		state:      JobStateRunning,
		exitCode:   unknownExitCode,
	}
}

// startJobStreams registers commander's Stream synchronously, in the
// caller's own goroutine, before anything else touches the process:
// commander starts streaming from the moment Stream is called, not from
// process start, so a process that exits fast (echo, printf, true) can
// finish and have its output discarded before a subscriber deferred to
// another goroutine ever registers. This mirrors commander's own
// documented usage. The two reader goroutines it spawns drain into job's
// ring buffers and are tracked by job.streamDone.
func startJobStreams(ctx context.Context, registry *JobRegistry, job *Job) {
	stdoutCh := make(chan string, jobStreamChannelBuffer)
	stderrCh := make(chan string, jobStreamChannelBuffer)
	job.process.Stream(stdoutCh, stderrCh)

	job.streamDone.Go(func() {
		defer recoverAndLog(ctx, "job stdout reader")

		for line := range stdoutCh {
			registry.recordOutput(ctx, job, JobOutputRecord{
				Stream:    JobStreamStdout,
				Content:   line,
				CreatedAt: time.Now().UTC(),
			})
		}
	})

	job.streamDone.Go(func() {
		defer recoverAndLog(ctx, "job stderr reader")

		for line := range stderrCh {
			registry.recordOutput(ctx, job, JobOutputRecord{
				Stream:    JobStreamStderr,
				Content:   line,
				CreatedAt: time.Now().UTC(),
			})
		}
	})
}

func (r *JobRegistry) recordOutput(
	ctx context.Context,
	job *Job,
	record JobOutputRecord,
) {
	job.outputMu.Lock()
	defer job.outputMu.Unlock()

	if job.persistenceFailure() != nil {
		return
	}
	if err := r.observeOutput(ctx, job.Snapshot(), record); err != nil {
		job.setPersistenceFailure(err)
		ctxscope.GetLogger(ctx).Warn(
			"persist job output",
			"err", err,
			"job_id", job.ID,
		)
		job.requestKill(ctx)

		return
	}

	if record.Stream == JobStreamStdout {
		job.stdout.Append(record.Content)

		return
	}

	job.stderr.Append(record.Content)
}

// Get looks up one job by ID within this session's registry.
func (r *JobRegistry) Get(id uuid.UUID) (*Job, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	job, ok := r.jobs[id]

	return job, ok
}

// List returns every job this registry has ever started, in no particular
// order.
func (r *JobRegistry) List() []*Job {
	r.mu.RLock()
	defer r.mu.RUnlock()

	jobs := make([]*Job, 0, len(r.jobs))
	for _, job := range r.jobs {
		jobs = append(jobs, job)
	}

	return jobs
}

// Signal asks one job to stop or die. Found is false when id names no job
// in this session. Signalling a job that is not running is a no-op: it
// reports the job's current (terminal) state rather than failing, and
// signalling a running job dispatches the request asynchronously and
// reports JobStateRunning, since the process has not necessarily exited by
// the time this call returns.
func (r *JobRegistry) Signal(
	ctx context.Context,
	id uuid.UUID,
	signal JobSignal,
) (JobSnapshot, bool) {
	snapshot, found, _ := r.SignalContext(ctx, id, signal)

	return snapshot, found
}

// SignalContext persists the request before it dispatches a process signal.
func (r *JobRegistry) SignalContext(
	ctx context.Context,
	id uuid.UUID,
	signal JobSignal,
) (JobSnapshot, bool, error) {
	job, ok := r.Get(id)
	if !ok {
		return JobSnapshot{}, false, nil
	}

	snapshot := job.Snapshot()
	persistCtx := context.WithoutCancel(ctx)
	if err := r.observeSignal(persistCtx, snapshot, signal); err != nil {
		return snapshot, true, ctxerrors.Wrap(err, "persist job signal")
	}
	if snapshot.State != JobStateRunning {
		return snapshot, true, nil
	}

	grace := r.limits.JobStopGracePeriod

	go func() {
		defer recoverAndLog(persistCtx, "job signal dispatch")

		if signal == JobSignalKill {
			job.requestKill(persistCtx)

			return
		}

		job.requestStop(persistCtx, grace)
	}()

	return snapshot, true, nil
}

// Shutdown stops every running job through commander's graceful stop, waits
// the configured grace period, then lets commander's own escalation kill
// the group. It returns only once every job that was running has fully
// finalized state, so no job silently outlives the service.
func (r *JobRegistry) Shutdown(ctx context.Context) error {
	running := make([]*Job, 0)

	for _, job := range r.List() {
		if job.Snapshot().State == JobStateRunning {
			running = append(running, job)
		}
	}

	var wg sync.WaitGroup

	for _, job := range running {
		wg.Go(func() {
			defer recoverAndLog(ctx, "job shutdown stop")

			job.requestStop(ctx, r.limits.JobStopGracePeriod)
		})
	}

	wg.Wait()

	for _, job := range running {
		<-job.Done()
	}

	return nil
}

// monitor blocks for the process to exit and finalizes state once it does.
// Stream registration and the ring-buffer reader goroutines already started
// synchronously in Start; this goroutine only waits for both to be done.
func (r *JobRegistry) monitor(ctx context.Context, job *Job) {
	defer recoverAndLog(ctx, "job monitor")

	waitErr := job.process.Wait()
	job.streamDone.Wait()

	r.finalize(ctx, job, waitErr)
}

// finalize records the job's terminal state and publishes its completion
// event exactly once.
func (r *JobRegistry) finalize(ctx context.Context, job *Job, waitErr error) {
	state, exitCode := classifyJobResult(waitErr)
	endedAt := time.Now().UTC()
	failureDetail := ""
	if persistenceErr := job.persistenceFailure(); persistenceErr != nil {
		state = JobStateFailed
		exitCode = unknownExitCode
		failureDetail = persistenceErr.Error()
	}

	job.mu.Lock()
	job.state = state
	job.endedAt = endedAt
	job.exitCode = exitCode
	job.failureDetail = failureDetail
	job.mu.Unlock()
	snapshot := job.Snapshot()
	if err := r.observeFinished(ctx, snapshot); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"persist finished job",
			"err", err,
			"job_id", job.ID,
		)
		close(job.done)

		return
	}

	// Publish before closing done: a caller unblocked by Done must always
	// find the completion event already published, never racing it.
	r.publish(ctx, job, state, exitCode, endedAt)
	r.recordJobMetrics(job, state, exitCode, endedAt)

	close(job.done)
}

func (j *Job) persistenceFailure() error {
	j.mu.RLock()
	defer j.mu.RUnlock()

	return j.persistenceErr
}

func (j *Job) setPersistenceFailure(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.persistenceErr == nil {
		j.persistenceErr = err
	}
}

func (r *JobRegistry) observerValue() JobObserver {
	r.observerMu.RLock()
	defer r.observerMu.RUnlock()

	return r.observer
}

func (r *JobRegistry) observeStarted(
	ctx context.Context,
	snapshot JobSnapshot,
) error {
	observer := r.observerValue()
	if observer == nil {
		return nil
	}

	return observer.JobStarted(ctx, snapshot)
}

func (r *JobRegistry) observeOutput(
	ctx context.Context,
	snapshot JobSnapshot,
	record JobOutputRecord,
) error {
	observer := r.observerValue()
	if observer == nil {
		return nil
	}

	return observer.JobOutput(ctx, snapshot, record)
}

func (r *JobRegistry) observeSignal(
	ctx context.Context,
	snapshot JobSnapshot,
	signal JobSignal,
) error {
	observer := r.observerValue()
	if observer == nil {
		return nil
	}

	return observer.JobSignal(ctx, snapshot, signal)
}

func (r *JobRegistry) observeFinished(
	ctx context.Context,
	snapshot JobSnapshot,
) error {
	observer := r.observerValue()
	if observer == nil {
		return nil
	}

	return observer.JobFinished(ctx, snapshot)
}

func (r *JobRegistry) recordJobMetrics(
	job *Job,
	state JobState,
	exitCode int,
	endedAt time.Time,
) {
	snapshot := job.Snapshot()

	outcome := metrics.OutcomeSuccess
	if state == JobStateSignalled {
		outcome = metrics.OutcomeCancelled
	}

	if state == JobStateFailed || exitCode != 0 {
		outcome = metrics.OutcomeError
	}

	r.metrics.JobCompleted(
		jobOperationRunCommand,
		outcome,
		endedAt.Sub(job.StartedAt),
	)
	r.metrics.JobOutputDropped(jobOutputStdout, snapshot.StdoutDroppedLines)
	r.metrics.JobOutputDropped(jobOutputStderr, snapshot.StderrDroppedLines)
}

// publish announces one job's completion on the event bus. Summary carries
// the job's purpose rather than its command text or output, matching the
// same never-log-command-content discipline this package applies to its own
// logging.
func (r *JobRegistry) publish(
	ctx context.Context,
	job *Job,
	state JobState,
	exitCode int,
	endedAt time.Time,
) {
	if r.publisher == nil {
		return
	}

	eventType, summary := jobCompletionEvent(job, state, exitCode)

	data, err := json.Marshal(jobEventData{
		JobID:      job.ID,
		ExitCode:   exitCode,
		DurationMs: endedAt.Sub(job.StartedAt).Milliseconds(),
	})
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"encode job completion event data",
			"err", err,
			"job_id", job.ID,
		)

		data = nil
	}

	notice := events.Notice{
		SessionID: r.sessionID,
		Type:      eventType,
		Source:    jobEventSource,
		Summary:   summary,
		Data:      data,
	}

	if _, err := r.publisher.PublishContext(ctx, notice); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"publish job completion event",
			"err", err,
			"job_id", job.ID,
		)
	}
}

// jobCompletionEvent maps a terminal job state onto its event type and a
// one-line, model-readable summary. state is always JobStateExited,
// JobStateSignalled, or JobStateFailed here: finalize only calls this after
// classifyJobResult, which never returns JobStateRunning.
func jobCompletionEvent(
	job *Job,
	state JobState,
	exitCode int,
) (events.Type, string) {
	switch state {
	case JobStateExited:
		return events.TypeJobExited, fmt.Sprintf(
			"job %s (%s) exited with code %d",
			job.ID, job.Purpose, exitCode,
		)
	case JobStateSignalled:
		return events.TypeJobSignalled, fmt.Sprintf(
			"job %s (%s) was terminated by a signal",
			job.ID, job.Purpose,
		)
	case JobStateRunning, JobStateFailed:
		fallthrough
	default:
		return events.TypeJobFailed, fmt.Sprintf(
			"job %s (%s) failed to run",
			job.ID, job.Purpose,
		)
	}
}

// classifyJobResult maps commander's Process.Wait error onto this package's
// state and exit-code vocabulary.
func classifyJobResult(err error) (JobState, int) {
	if err == nil {
		return JobStateExited, 0
	}

	signalled := errors.Is(err, commerr.ErrTerminated) ||
		errors.Is(err, commerr.ErrKilled)
	if signalled {
		return JobStateSignalled, unknownExitCode
	}

	if errors.Is(err, commerr.ErrFailed) {
		code, ok := exitCodeFromWaitError(err)
		if !ok {
			code = unknownExitCode
		}

		return JobStateExited, code
	}

	return JobStateFailed, unknownExitCode
}

// exitCodeFromWaitError recovers the numeric exit code commander v0.5.8
// folds into its wrapped error text. Process.Wait's handleWaitError (see
// .research_files/commander/process_core.go) extracts
// *exec.ExitError.ExitCode() and immediately discards the typed value into
// a "(exit %d): <stderr>" message wrapped around commerr.ErrFailed, so the
// error chain never carries the original *exec.ExitError; errors.As cannot
// recover it. Parsing this fixed, commander-tested prefix is the only way
// this dependency version reports which code a normally-exited-but-failed
// process used.
func exitCodeFromWaitError(err error) (int, bool) {
	if err == nil {
		return 0, false
	}

	message := err.Error()

	start := strings.Index(message, exitCodeMarker)
	if start < 0 {
		return 0, false
	}

	start += len(exitCodeMarker)

	rest := message[start:]

	before, _, ok := strings.Cut(rest, ")")
	if !ok {
		return 0, false
	}

	code, convErr := strconv.Atoi(before)
	if convErr != nil {
		return 0, false
	}

	return code, true
}

// recoverAndLog recovers a panic in the calling goroutine and logs it,
// per this project's rule that every spawned goroutine must never crash
// the process on a panic. Call it first in a deferred statement.
func recoverAndLog(ctx context.Context, label string) {
	r := recover()
	if r == nil {
		return
	}

	ctxscope.GetLogger(ctx).Error(
		label+" panicked",
		"panic", r,
		"stack", string(debug.Stack()),
	)
}

// toolCallIDKey is the unexported context key run_command's caller uses to
// attach the tool-call ID Elelem assigned to this invocation.
type toolCallIDKey struct{}

// ContextWithToolCallID attaches the tool-call ID that started this
// invocation of run_command, so the resulting job records which specific
// call started it. The tools package cannot learn this ID from the model's
// JSON arguments; the caller must set it on ctx before invoking RunCommand.
func ContextWithToolCallID(
	ctx context.Context,
	toolCallID string,
) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, toolCallID)
}

// toolCallIDFromContext reads back the ID ContextWithToolCallID attached,
// or "" when none was set.
func toolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)

	return id
}

// JobExecutor extends Executor with the session-scoped job registry that
// process-oriented tools need: run_command, list_jobs, read_job_output,
// wait_job, and signal_job. It embeds Executor so every other host tool
// method stays available unchanged on the same value; a caller can use a
// JobExecutor everywhere it previously used a plain Executor.
type JobExecutor struct {
	*Executor

	jobs   *JobRegistry
	turnID uuid.UUID
}

// NewJobExecutor attaches a session-scoped job registry and the current
// turn's ID to an already constructed Executor. jobs must be shared across
// every turn in one session, never rebuilt per turn, so a job started in one
// turn stays visible in a later one.
func NewJobExecutor(
	executor *Executor,
	jobs *JobRegistry,
	turnID uuid.UUID,
) (*JobExecutor, error) {
	if executor == nil || jobs == nil {
		return nil, ctxerrors.Wrap(
			ErrInvalidOptions,
			"job executor requires an executor and a job registry",
		)
	}

	return &JobExecutor{Executor: executor, jobs: jobs, turnID: turnID}, nil
}
