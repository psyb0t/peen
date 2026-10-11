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

// seedAuditLogDays creates an empty audit file for each of the given days
// before today, so a test can see which ones the handler prunes.
func seedAuditLogDays(t *testing.T, directory string, daysAgo ...int) {
	t.Helper()

	require.NoError(t, os.MkdirAll(directory, directoryMode))

	for _, count := range daysAgo {
		day := utcDay(time.Now().UTC().AddDate(0, 0, -count))
		path := filepath.Join(directory, day.Format(dailyFilenameLayout))
		require.NoError(t, os.WriteFile(path, nil, fileMode))
	}
}

func auditLogFileNames(t *testing.T, directory string) []string {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

func auditLogDayName(daysAgo int) string {
	day := utcDay(time.Now().UTC().AddDate(0, 0, -daysAgo))

	return day.Format(dailyFilenameLayout)
}

// configureAuditLog runs Configure with the given env and restores the
// process logger afterwards. It must not run in parallel: it sets env vars and
// stacks a sink on the default logger.
func configureAuditLog(t *testing.T, env map[string]string) error {
	t.Helper()

	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	t.Setenv("PEEN_LOG_RETENTION", "")
	require.NoError(t, os.Unsetenv("PEEN_LOG_RETENTION"))

	for key, value := range env {
		t.Setenv(key, value)
	}

	return Configure()
}

func TestConfigureDefaultsToFourteenDaysOfRetention(t *testing.T) {
	stateDirectory := t.TempDir()
	directory := filepath.Join(stateDirectory, defaultDirectory)
	seedAuditLogDays(t, directory, 13, 14)

	err := configureAuditLog(t, map[string]string{
		"PEEN_STATE_DIR": stateDirectory,
	})
	require.NoError(t, err)

	names := auditLogFileNames(t, directory)
	assert.Contains(t, names, auditLogDayName(13))
	assert.NotContains(t, names, auditLogDayName(14))
	assert.Contains(t, names, auditLogDayName(0))
}

func TestNewDailyHandlerTreatsZeroRetentionAsTheDefault(t *testing.T) {
	directory := t.TempDir()
	seedAuditLogDays(t, directory, 13, 14)

	handler, err := NewDailyHandler(Config{Directory: directory, Retention: 0})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handler.Close()) })

	names := auditLogFileNames(t, directory)
	assert.Contains(t, names, auditLogDayName(13))
	assert.NotContains(t, names, auditLogDayName(14))
}

func TestConfigureReadsRetentionAsAGoDuration(t *testing.T) {
	directory := t.TempDir()
	seedAuditLogDays(t, directory, 1, 2, 3)

	err := configureAuditLog(t, map[string]string{
		"PEEN_LOG_DIRECTORY": directory,
		"PEEN_LOG_RETENTION": "48h",
	})
	require.NoError(t, err)

	assert.ElementsMatch(
		t,
		[]string{auditLogDayName(0), auditLogDayName(1)},
		auditLogFileNames(t, directory),
	)
}

func TestConfigureRejectsInvalidRetentionValues(t *testing.T) {
	testCases := []struct {
		name  string
		value string
	}{
		{name: "not a duration", value: "two weeks"},
		{name: "a bare number", value: "14"},
		{name: "not a whole number of days", value: "36h"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()

			err := configureAuditLog(t, map[string]string{
				"PEEN_LOG_DIRECTORY": directory,
				"PEEN_LOG_RETENTION": tc.value,
			})

			require.Error(t, err)
			assert.Empty(t, auditLogFileNames(t, directory))
		})
	}
}

func TestConfigureNamesTheRetentionRuleForAPartialDay(t *testing.T) {
	err := configureAuditLog(t, map[string]string{
		"PEEN_LOG_DIRECTORY": t.TempDir(),
		"PEEN_LOG_RETENTION": "36h",
	})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Contains(t, err.Error(), "a multiple of 24h")
}
