// Package auditlog adds a durable JSON sink beside the process logger.
package auditlog

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/gonfiguration"
	"github.com/psyb0t/slogging/slogconf"
)

const (
	defaultDirectory     = "logs"
	defaultRetentionDays = 14
	dailyFilenameLayout  = "20060102-150405.log"
	directoryMode        = 0o700
	fileMode             = 0o600
)

// Config controls the private rolling audit-log sink.
type Config struct {
	ConfigDirectory string `env:"PEEN_CONFIG_DIR"`
	Directory       string `env:"PEEN_LOG_DIRECTORY"`
	RetentionDays   int    `default:"14"             env:"PEEN_LOG_RETENTION_DAYS"` //nolint:lll // Immutable env tag.
}

// Configure reads audit-log settings and adds the durable sink beside stdout.
func Configure() error {
	config := Config{}
	if err := gonfiguration.Parse(&config); err != nil {
		return ctxerrors.Wrap(err, "parse audit log configuration")
	}

	return ConfigureWith(config)
}

// ConfigureWith validates config and adds the daily sink to slog's fan-out.
func ConfigureWith(config Config) error {
	handler, err := NewDailyHandler(config)
	if err != nil {
		return err
	}

	slogconf.AddSink(handler)

	return nil
}

// DailyHandler writes JSON records to one UTC-day file and retains recent days.
type DailyHandler struct {
	state  *dailyState
	attrs  []slog.Attr
	groups []string
}

type dailyState struct {
	mu            sync.Mutex
	directory     string
	retentionDays int
	file          *os.File
	day           time.Time
}

// NewDailyHandler creates today's file immediately so startup failures are
// detected before the application accepts work.
func NewDailyHandler(config Config) (*DailyHandler, error) {
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return nil, err
	}

	handler := &DailyHandler{state: &dailyState{
		directory:     filepath.Clean(config.Directory),
		retentionDays: config.RetentionDays,
	}}
	if err := handler.state.rotate(time.Now().UTC()); err != nil {
		return nil, err
	}

	return handler, nil
}

// Enabled keeps the file trace at debug even when stdout is less verbose.
func (h *DailyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug
}

// Handle writes a structured record to the file for the record's UTC day.
func (h *DailyHandler) Handle(ctx context.Context, record slog.Record) error {
	when := record.Time.UTC()
	if when.IsZero() {
		when = time.Now().UTC()
	}

	h.state.mu.Lock()
	defer h.state.mu.Unlock()

	if err := h.state.rotate(when); err != nil {
		return err
	}

	var writer slog.Handler = slog.NewJSONHandler(
		h.state.file,
		&slog.HandlerOptions{
			AddSource: true,
			Level:     slog.LevelDebug,
		},
	)
	for _, group := range h.groups {
		writer = writer.WithGroup(group)
	}

	if err := writer.WithAttrs(h.attrs).Handle(ctx, record); err != nil {
		return ctxerrors.Wrap(err, "write audit log record")
	}

	return nil
}

// WithAttrs returns a derived handler with the attributes on each record.
func (h *DailyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	derived := *h
	derived.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)

	return &derived
}

// WithGroup returns a derived handler with the group on each record.
func (h *DailyHandler) WithGroup(name string) slog.Handler {
	derived := *h
	derived.groups = append(append([]string(nil), h.groups...), name)

	return &derived
}

// Close releases the active file. It is intended for tests and controlled exit.
func (h *DailyHandler) Close() error {
	h.state.mu.Lock()
	defer h.state.mu.Unlock()

	if h.state.file == nil {
		return nil
	}

	err := h.state.file.Close()
	h.state.file = nil

	if err != nil {
		return ctxerrors.Wrap(err, "close audit log file")
	}

	return nil
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.Directory) == "" {
		c.Directory = filepath.Join(c.ConfigDirectory, defaultDirectory)
	}

	if c.RetentionDays == 0 {
		c.RetentionDays = defaultRetentionDays
	}

	return c
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Directory) == "" {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"audit log directory is required",
		)
	}

	if c.RetentionDays <= 0 {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"audit log retention days must be positive, got %d",
			c.RetentionDays,
		)
	}

	return nil
}

func (s *dailyState) rotate(when time.Time) error {
	day := utcDay(when)
	if s.file != nil && s.day.Equal(day) {
		return nil
	}

	if err := os.MkdirAll(s.directory, directoryMode); err != nil {
		return ctxerrors.Wrap(err, "create audit log directory")
	}

	path := filepath.Join(s.directory, day.Format(dailyFilenameLayout))
	if err := validateLogPath(path); err != nil {
		return err
	}

	file, err := os.OpenFile(
		path,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		fileMode,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "open audit log file")
	}

	previous := s.file
	s.file = file
	s.day = day

	if previous != nil {
		if closeErr := previous.Close(); closeErr != nil {
			return ctxerrors.Wrap(closeErr, "close rotated audit log file")
		}
	}

	if err := s.prune(day); err != nil {
		return err
	}

	return nil
}

func validateLogPath(path string) error {
	info, err := os.Lstat(path)

	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return ctxerrors.Wrap(err, "inspect audit log path")
	}

	if !info.Mode().IsRegular() {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"audit log path is not a regular file",
		)
	}

	return nil
}

func (s *dailyState) prune(day time.Time) error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return ctxerrors.Wrap(err, "read audit log directory")
	}

	firstRetained := day.AddDate(0, 0, -(s.retentionDays - 1))

	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			continue
		}

		fileDay, parseErr := time.Parse(dailyFilenameLayout, entry.Name())
		if parseErr != nil || !fileDay.Before(firstRetained) {
			continue
		}

		path := filepath.Join(s.directory, entry.Name())
		if removeErr := os.Remove(path); removeErr != nil {
			return ctxerrors.Wrap(removeErr, "remove expired audit log")
		}
	}

	return nil
}

func utcDay(value time.Time) time.Time {
	utc := value.UTC()

	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}
