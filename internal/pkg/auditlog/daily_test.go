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

const auditLogTestRetentionDays = 14

func TestAuditConfigDefaultsToLocalLogDirectoryWithoutConfigDirectory(t *testing.T) {
	config := (Config{}).withDefaults()

	assert.Equal(t, defaultDirectory, config.Directory)
}

func TestNewDailyHandlerDefaultsUnderConfigDirectory(t *testing.T) {
	configDirectory := filepath.Join(t.TempDir(), "config")
	handler, err := NewDailyHandler(Config{ConfigDirectory: configDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handler.Close()) })

	entries, err := os.ReadDir(filepath.Join(configDirectory, defaultDirectory))
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
		Directory:     directory,
		RetentionDays: auditLogTestRetentionDays,
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

func TestNewDailyHandlerRejectsNegativeRetention(t *testing.T) {
	_, err := NewDailyHandler(Config{Directory: t.TempDir(), RetentionDays: -1})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
