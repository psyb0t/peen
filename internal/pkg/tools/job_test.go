package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	jobTestPurpose        = "test job"
	jobTestGrace          = 300 * time.Millisecond
	jobTestEventuallyWait = 5 * time.Second
	jobTestEventuallyTick = 5 * time.Millisecond
)

// stubPublisher records every notice it is asked to publish, so tests can
// assert on completion events without a real event bus.
type stubPublisher struct {
	mu      sync.Mutex
	notices []events.Notice
}

func (p *stubPublisher) PublishContext(
	_ context.Context,
	notice events.Notice,
) (events.Notice, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.notices = append(p.notices, notice)

	return notice, nil
}

func (p *stubPublisher) all() []events.Notice {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]events.Notice(nil), p.notices...)
}

func newTestRegistry(t *testing.T, publisher EventPublisher) *JobRegistry {
	t.Helper()

	registry, err := NewJobRegistry(uuid.New(), publisher, Limits{
		JobStopGracePeriod: jobTestGrace,
	})
	require.NoError(t, err)

	return registry
}

func TestNewJobRegistry_RequiresSessionID(t *testing.T) {
	t.Parallel()

	_, err := NewJobRegistry(uuid.Nil, nil, Limits{})

	require.ErrorIs(t, err, ErrInvalidOptions)
}

func TestNewJobExecutor_RequiresExecutorAndRegistry(t *testing.T) {
	t.Parallel()

	executor := newTestExecutor(t)
	registry := newTestRegistry(t, nil)

	_, err := NewJobExecutor(nil, registry, uuid.New())
	require.ErrorIs(t, err, ErrInvalidOptions)

	_, err = NewJobExecutor(executor, nil, uuid.New())
	require.ErrorIs(t, err, ErrInvalidOptions)
}

func TestJobRegistry_GetUnknownJob(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	_, ok := registry.Get(uuid.New())
	assert.False(t, ok)
}

func TestJobRegistry_GetFromAnotherSessionNotFound(t *testing.T) {
	t.Parallel()

	registryA := newTestRegistry(t, nil)
	registryB := newTestRegistry(t, nil)

	job, err := registryA.Start(context.Background(), StartJobInput{
		Command:   "true",
		Directory: t.TempDir(),
		Purpose:   jobTestPurpose,
	})
	require.NoError(t, err)

	_, ok := registryB.Get(job.ID)
	assert.False(t, ok, "a job from another session's registry must not be found")
}

func TestJobRegistry_SignalUnknownJobIdempotent(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	_, ok := registry.Signal(context.Background(), uuid.New(), JobSignalStop)
	assert.False(t, ok)
}

func TestJobRegistry_SignalExitedJobIdempotent(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	job, err := registry.Start(context.Background(), StartJobInput{
		Command:   "true",
		Directory: t.TempDir(),
		Purpose:   jobTestPurpose,
	})
	require.NoError(t, err)

	select {
	case <-job.Done():
	case <-time.After(jobTestEventuallyWait):
		t.Fatal("job never exited")
	}

	snapshot, ok := registry.Signal(context.Background(), job.ID, JobSignalStop)
	require.True(t, ok)
	assert.Equal(t, JobStateExited, snapshot.State)
}

func TestJobRegistry_SignalAlreadySignalledJobIdempotent(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	job, err := registry.Start(context.Background(), StartJobInput{
		Command:   "sleep 30",
		Directory: t.TempDir(),
		Purpose:   jobTestPurpose,
	})
	require.NoError(t, err)

	_, ok := registry.Signal(context.Background(), job.ID, JobSignalKill)
	require.True(t, ok)

	select {
	case <-job.Done():
	case <-time.After(jobTestEventuallyWait):
		t.Fatal("job never finalized after kill")
	}

	assert.Equal(t, JobStateSignalled, job.Snapshot().State)

	snapshot, ok := registry.Signal(context.Background(), job.ID, JobSignalStop)
	require.True(t, ok)
	assert.Equal(t, JobStateSignalled, snapshot.State)
}

func TestJobRegistry_PublishesJobExited(t *testing.T) {
	t.Parallel()

	publisher := &stubPublisher{}
	registry := newTestRegistry(t, publisher)

	job, err := registry.Start(context.Background(), StartJobInput{
		Command:   fmt.Sprintf("exit %d", commandExitCodeNonZero),
		Directory: t.TempDir(),
		Purpose:   jobTestPurpose,
	})
	require.NoError(t, err)

	select {
	case <-job.Done():
	case <-time.After(jobTestEventuallyWait):
		t.Fatal("job never exited")
	}

	notices := publisher.all()
	require.Len(t, notices, 1)

	notice := notices[0]
	assert.Equal(t, events.TypeJobExited, notice.Type)
	assert.NotEmpty(t, notice.Summary)
	assert.Contains(t, notice.Summary, jobTestPurpose)

	var data jobEventData
	require.NoError(t, json.Unmarshal(notice.Data, &data))
	assert.Equal(t, job.ID, data.JobID)
	assert.Equal(t, commandExitCodeNonZero, data.ExitCode)
	assert.GreaterOrEqual(t, data.DurationMs, int64(0))
}

func TestJobRegistry_PublishesJobSignalled(t *testing.T) {
	t.Parallel()

	publisher := &stubPublisher{}
	registry := newTestRegistry(t, publisher)

	job, err := registry.Start(context.Background(), StartJobInput{
		Command:   "sleep 30",
		Directory: t.TempDir(),
		Purpose:   jobTestPurpose,
	})
	require.NoError(t, err)

	_, ok := registry.Signal(context.Background(), job.ID, JobSignalKill)
	require.True(t, ok)

	select {
	case <-job.Done():
	case <-time.After(jobTestEventuallyWait):
		t.Fatal("job never finalized after kill")
	}

	notices := publisher.all()
	require.Len(t, notices, 1)
	assert.Equal(t, events.TypeJobSignalled, notices[0].Type)
	assert.NotEmpty(t, notices[0].Summary)
}

func TestJobRegistry_NilPublisherDoesNotPublish(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	job, err := registry.Start(context.Background(), StartJobInput{
		Command:   "true",
		Directory: t.TempDir(),
		Purpose:   jobTestPurpose,
	})
	require.NoError(t, err)

	select {
	case <-job.Done():
	case <-time.After(jobTestEventuallyWait):
		t.Fatal("job never exited")
	}

	assert.Equal(t, JobStateExited, job.Snapshot().State)
}

func TestJobRegistry_Shutdown_StopsEveryRunningJob(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	const jobCount = 3

	jobs := make([]*Job, 0, jobCount)

	for range jobCount {
		job, err := registry.Start(context.Background(), StartJobInput{
			Command:   "sleep 30",
			Directory: t.TempDir(),
			Purpose:   jobTestPurpose,
		})
		require.NoError(t, err)

		jobs = append(jobs, job)
	}

	require.NoError(t, registry.Shutdown(context.Background()))

	for _, job := range jobs {
		snapshot := job.Snapshot()
		assert.NotEqual(t, JobStateRunning, snapshot.State,
			"no job may still be running once Shutdown returns")
		assert.False(t, processAlive(t, snapshot.PID))
	}
}

func TestJobRegistry_Shutdown_NoRunningJobsIsNoop(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	assert.NoError(t, registry.Shutdown(context.Background()))
}

func TestJobRegistry_StartRejectsCancelledContext(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := registry.Start(ctx, StartJobInput{
		Command:   "true",
		Directory: t.TempDir(),
		Purpose:   jobTestPurpose,
	})

	require.ErrorIs(t, err, context.Canceled)
}

func TestJobRegistry_StartRejectsUnlaunchableCommand(t *testing.T) {
	t.Parallel()

	registry := newTestRegistry(t, nil)

	_, err := registry.Start(context.Background(), StartJobInput{
		Command:   "true",
		Directory: filepath.Join(t.TempDir(), "does-not-exist"),
		Purpose:   jobTestPurpose,
	})

	require.ErrorIs(t, err, ErrCommandStartFailed)
}

func TestExitCodeFromWaitError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		err    error
		want   int
		wantOK bool
	}{
		{
			name:   "nil error",
			err:    nil,
			want:   0,
			wantOK: false,
		},
		{
			name:   "no marker",
			err:    commerr.ErrFailed,
			want:   0,
			wantOK: false,
		},
		{
			name:   "well-formed marker",
			err:    errors.New("(exit 7): boom"), //nolint:err113 // test fixture
			want:   7,
			wantOK: true,
		},
		{
			name:   "unterminated marker",
			err:    errors.New("(exit 7"), //nolint:err113 // test fixture
			want:   0,
			wantOK: false,
		},
		{
			name:   "non-numeric marker",
			err:    errors.New("(exit x)"), //nolint:err113 // test fixture
			want:   0,
			wantOK: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			code, ok := exitCodeFromWaitError(tc.err)

			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, code)
		})
	}
}

func TestContextWithToolCallID_RoundTrips(t *testing.T) {
	t.Parallel()

	ctx := ContextWithToolCallID(context.Background(), "call-1")

	assert.Equal(t, "call-1", toolCallIDFromContext(ctx))
	assert.Empty(t, toolCallIDFromContext(context.Background()))
}
