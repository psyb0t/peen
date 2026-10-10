package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	auditLogTestRetentionDays = 14
	auditLogTestRetention     = auditLogTestRetentionDays * 24 * time.Hour
)

func TestNewDailyHandlerRequiresDirectoryOrStateDirectory(t *testing.T) {
	_, err := NewDailyHandler(Config{})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestNewDailyHandlerDefaultsUnderStateDirectory(t *testing.T) {
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handler, err := NewDailyHandler(Config{StateDirectory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handler.Close()) })

	entries, err := os.ReadDir(filepath.Join(stateDirectory, defaultDirectory))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestDailyHandlerDoesNotLogOnSuccessfulWrite(t *testing.T) {
	directory := t.TempDir()
	handler, err := NewDailyHandler(Config{Directory: directory})
	require.NoError(t, err)

	previousLogger := slog.Default()
	processLogs := bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewJSONHandler(&processLogs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
		require.NoError(t, handler.Close())
	})

	record := slog.NewRecord(
		time.Now().UTC(),
		slog.LevelInfo,
		"audit event",
		0,
	)
	require.NoError(t, handler.Handle(context.Background(), record))
	assert.Empty(t, processLogs.String())
}

func TestDailyHandlerRotatesAndRetainsConfiguredDays(t *testing.T) {
	directory := t.TempDir()
	handler, err := NewDailyHandler(Config{
		Directory: directory,
		Retention: auditLogTestRetention,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handler.Close()) })

	firstDay := time.Now().UTC()
	for dayOffset := 0; dayOffset <= auditLogTestRetentionDays; dayOffset++ {
		when := firstDay.AddDate(0, 0, dayOffset)
		record := slog.NewRecord(when, slog.LevelInfo, "audit event", 0)
		record.AddAttrs(slog.String("audit_event", "turn.started"))
		require.NoError(t, handler.Handle(context.Background(), record))
	}

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	assert.Len(t, entries, auditLogTestRetentionDays)

	newestPath := filepath.Join(
		directory,
		utcDay(firstDay.AddDate(0, 0, auditLogTestRetentionDays)).Format(
			dailyFilenameLayout,
		),
	)
	content, err := os.ReadFile(newestPath)
	require.NoError(t, err)

	line := map[string]any{}
	require.NoError(t, json.Unmarshal(content, &line))
	assert.Equal(t, "audit event", line["msg"])
	assert.Equal(t, "turn.started", line["audit_event"])
}

func TestNewDailyHandlerRejectsInvalidRetention(t *testing.T) {
	testCases := []struct {
		name      string
		retention time.Duration
	}{
		{name: "negative", retention: -time.Hour},
		{name: "not a whole number of days", retention: 36 * time.Hour},
		{name: "shorter than a day", retention: time.Hour},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDailyHandler(Config{
				Directory: t.TempDir(),
				Retention: tc.retention,
			})

			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestDailyHandlerRetentionKeepsRetentionDividedByDayFiles(t *testing.T) {
	directory := t.TempDir()
	handler, err := NewDailyHandler(Config{
		Directory: directory,
		Retention: 48 * time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handler.Close()) })

	firstDay := time.Now().UTC()
	for dayOffset := range 4 {
		when := firstDay.AddDate(0, 0, dayOffset)
		record := slog.NewRecord(when, slog.LevelInfo, "audit event", 0)
		require.NoError(t, handler.Handle(context.Background(), record))
	}

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestNewDailyHandlerRejectsRemovedRetentionDaysVariable(t *testing.T) {
	_, err := NewDailyHandler(Config{
		Directory:            t.TempDir(),
		RemovedRetentionDays: "14",
	})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Contains(t, err.Error(), "PEEN_LOG_RETENTION")
	assert.Contains(t, err.Error(), "336h")
}
