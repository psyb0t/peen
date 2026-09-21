package workerlog

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRelayCarriesWorkerRecordsIntoTheControllerLogger is the regression guard
// for the audit trail.
//
// A worker writing straight to its own output skipped every sink the
// controller configured, so the durable audit file held controller records and
// nothing the agent loop did. The relay has to reproduce the worker's level and
// message, because the audit trail is searched by those.
func TestRelayCarriesWorkerRecordsIntoTheControllerLogger(t *testing.T) {
	sessionID := uuid.New()
	generationID := uuid.New()

	captured := &bytes.Buffer{}

	restore := captureDefaultLogger(t, captured, slog.LevelDebug)
	defer restore()

	stream := strings.NewReader(strings.Join([]string{
		`{"time":"2026-09-19T05:00:00Z","level":"DEBUG","msg":"hook lifecycle event started","hook":"config-remember-read"}`,
		`{"time":"2026-09-19T05:00:01Z","level":"INFO","msg":"skill activated","skill":"fixture-service"}`,
		`{"time":"2026-09-19T05:00:02Z","level":"ERROR","msg":"child agent completed","agent":"fixture-reviewer"}`,
	}, "\n"))

	Relay(context.Background(), stream, sessionID, generationID)

	output := captured.String()
	for _, want := range []string{
		"hook lifecycle event started",
		"config-remember-read",
		"skill activated",
		"fixture-service",
		"child agent completed",
		"fixture-reviewer",
		sessionID.String(),
		generationID.String(),
	} {
		assert.Contains(t, output, want)
	}
}

// TestRelayPreservesWorkerLevels keeps a debug record from being promoted, so a
// sink that filters by level sees what the worker actually emitted.
func TestRelayPreservesWorkerLevels(t *testing.T) {
	captured := &bytes.Buffer{}

	restore := captureDefaultLogger(t, captured, slog.LevelWarn)
	defer restore()

	stream := strings.NewReader(strings.Join([]string{
		`{"level":"DEBUG","msg":"quiet worker detail"}`,
		`{"level":"ERROR","msg":"loud worker failure"}`,
	}, "\n"))

	Relay(context.Background(), stream, uuid.New(), uuid.New())

	output := captured.String()
	assert.NotContains(t, output, "quiet worker detail")
	assert.Contains(t, output, "loud worker failure")
}

// TestRelayKeepsNonRecordOutput covers a worker subprocess that prints plain
// text. Dropping it would lose the output of a command the agent ran.
func TestRelayKeepsNonRecordOutput(t *testing.T) {
	captured := &bytes.Buffer{}

	restore := captureDefaultLogger(t, captured, slog.LevelDebug)
	defer restore()

	stream := strings.NewReader(strings.Join([]string{
		"PASS",
		"ok  \tfixture.local/service\t0.01s",
		`{"level":"INFO","msg":"structured still works"}`,
		`{"not":"a log record"}`,
	}, "\n"))

	Relay(context.Background(), stream, uuid.New(), uuid.New())

	output := captured.String()
	assert.Contains(t, output, "PASS")
	assert.Contains(t, output, "fixture.local/service")
	assert.Contains(t, output, "structured still works")
	assert.Contains(t, output, "a log record")
}

// TestRelayHandlesAnEmptyStream keeps a worker that printed nothing from
// producing a spurious record.
func TestRelayHandlesAnEmptyStream(t *testing.T) {
	captured := &bytes.Buffer{}

	restore := captureDefaultLogger(t, captured, slog.LevelDebug)
	defer restore()

	Relay(context.Background(), strings.NewReader(""), uuid.New(), uuid.New())

	assert.Empty(t, captured.String())
}

// captureDefaultLogger points slog's default at a buffer for one test. These
// tests mutate process-global logging state, so they never run in parallel.
func captureDefaultLogger(
	t *testing.T,
	into *bytes.Buffer,
	level slog.Level,
) func() {
	t.Helper()

	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewJSONHandler(
		into,
		&slog.HandlerOptions{Level: level},
	)))

	restored := false

	restore := func() {
		if restored {
			return
		}

		restored = true

		slog.SetDefault(previous)
	}

	t.Cleanup(restore)

	require.NotNil(t, slog.Default())

	return restore
}
