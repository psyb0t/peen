package peen_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeTelemetryStaysLocalAndUnregisters(t *testing.T) {
	t.Parallel()
	var records bytes.Buffer
	options := validOptions(t)
	options.Workspace = t.TempDir()
	options.Logger = slog.New(slog.NewJSONHandler(&records, nil))
	registry := prometheus.NewRegistry()
	options.MetricsRegisterer = registry
	options.AuditLog.Enabled = true
	options.StateDirectory = t.TempDir()
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver(elelemtest.Text("reply"))),
		Model:  elelem.Model{ID: testModelID},
	}
	previousLogger := slog.Default()
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	assert.Same(t, previousLogger, slog.Default())
	_, err = runtime.Message(context.Background(), peen.MessageRequest{Message: "secret input"})
	require.NoError(t, err)
	families, err := registry.Gather()
	require.NoError(t, err)
	assert.NotEmpty(t, families)
	require.NoError(t, runtime.Close(context.Background()))
	families, err = registry.Gather()
	require.NoError(t, err)
	assert.Empty(t, families)
	assert.Contains(t, records.String(), "session_id")
	assert.NotContains(t, records.String(), "secret input")
	files, err := filepath.Glob(filepath.Join(options.StateDirectory, "logs", "*.log"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Contains(t, string(data), "session_id")
	assert.NotContains(t, string(data), "secret input")
}

func TestIndependentMemoryRuntimesDoNotShareSessions(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	options.InMemoryStore = true
	first, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close(context.Background())) })
	second, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close(context.Background())) })
	firstDetails, err := first.Details(context.Background())
	require.NoError(t, err)
	secondDetails, err := second.Details(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, firstDetails.ID, secondDetails.ID)
	_, err = second.ResumeSession(context.Background(), firstDetails.ID)
	require.Error(t, err)
	require.NoError(t, first.Close(context.Background()))
	_, err = second.Details(context.Background())
	require.NoError(t, err)
}
