package tools

import (
	"time"

	"github.com/psyb0t/ctxerrors"
)

const (
	defaultMaxListEntries        = 1000
	defaultMaxListDepth          = 16
	defaultMaxSearchMatches      = 200
	defaultMaxSearchFileBytes    = 2 * 1024 * 1024
	defaultMaxReadBytes          = 256 * 1024
	defaultMaxReadLines          = 2000
	defaultMaxWriteBytes         = 4 * 1024 * 1024
	defaultMaxEdits              = 64
	defaultMaxDiffBytes          = 64 * 1024
	defaultMaxPatchBytes         = 1024 * 1024
	defaultMaxPatchFiles         = 64
	defaultMaxPatchHunks         = 256
	defaultMaxPatchChangedBytes  = 4 * 1024 * 1024
	defaultMaxRemoveEntries      = 20000
	defaultMaxCommandOutputBytes = 64 * 1024
	defaultCommandTimeout        = 2 * time.Minute
	defaultMaxCommandTimeout     = 15 * time.Minute

	defaultMaxJobOutputLines     = 2000
	defaultMaxReadJobOutputLines = 500
	defaultWaitJobTimeout        = 30 * time.Second
	defaultMaxWaitJobTimeout     = 10 * time.Minute
	defaultJobStopGracePeriod    = 10 * time.Second
	defaultMaxListJobs           = 500
)

// Limits bounds every host tool result. Zero fields take the default.
type Limits struct {
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
	MaxCommandTimeout     time.Duration

	// MaxJobOutputLines bounds how many lines a job's stdout/stderr ring
	// buffer retains. MaxCommandOutputBytes bounds the same buffer's total
	// bytes; whichever bound is hit first evicts the oldest lines.
	MaxJobOutputLines int
	// MaxReadJobOutputLines bounds one read_job_output/wait_job window,
	// independent of how much a job's ring buffer retains overall.
	MaxReadJobOutputLines int
	// WaitJobTimeout and MaxWaitJobTimeout bound wait_job the same way
	// CommandTimeout and MaxCommandTimeout bound run_command's wait.
	WaitJobTimeout    time.Duration
	MaxWaitJobTimeout time.Duration
	// JobStopGracePeriod bounds how long signal_job's stop and the registry's
	// Shutdown wait after SIGTERM before escalating to SIGKILL.
	JobStopGracePeriod time.Duration
	// MaxListJobs bounds how many jobs list_jobs returns in one call.
	MaxListJobs int
}

// DefaultLimits returns the bounded defaults every deployment starts from.
func DefaultLimits() Limits {
	return Limits{
		MaxListEntries:        defaultMaxListEntries,
		MaxListDepth:          defaultMaxListDepth,
		MaxSearchMatches:      defaultMaxSearchMatches,
		MaxSearchFileBytes:    defaultMaxSearchFileBytes,
		MaxReadBytes:          defaultMaxReadBytes,
		MaxReadLines:          defaultMaxReadLines,
		MaxWriteBytes:         defaultMaxWriteBytes,
		MaxEdits:              defaultMaxEdits,
		MaxDiffBytes:          defaultMaxDiffBytes,
		MaxPatchBytes:         defaultMaxPatchBytes,
		MaxPatchFiles:         defaultMaxPatchFiles,
		MaxPatchHunks:         defaultMaxPatchHunks,
		MaxPatchChangedBytes:  defaultMaxPatchChangedBytes,
		MaxRemoveEntries:      defaultMaxRemoveEntries,
		MaxCommandOutputBytes: defaultMaxCommandOutputBytes,
		CommandTimeout:        defaultCommandTimeout,
		MaxCommandTimeout:     defaultMaxCommandTimeout,
		MaxJobOutputLines:     defaultMaxJobOutputLines,
		MaxReadJobOutputLines: defaultMaxReadJobOutputLines,
		WaitJobTimeout:        defaultWaitJobTimeout,
		MaxWaitJobTimeout:     defaultMaxWaitJobTimeout,
		JobStopGracePeriod:    defaultJobStopGracePeriod,
		MaxListJobs:           defaultMaxListJobs,
	}
}

func (l Limits) withDefaults() Limits {
	defaults := DefaultLimits()

	l = l.withFileToolDefaults(defaults)
	l = l.withPatchToolDefaults(defaults)
	l = l.withJobToolDefaults(defaults)

	return l
}

//nolint:cyclop // One zero check per bound reads better than indirection.
func (l Limits) withFileToolDefaults(defaults Limits) Limits {
	if l.MaxListEntries == 0 {
		l.MaxListEntries = defaults.MaxListEntries
	}

	if l.MaxListDepth == 0 {
		l.MaxListDepth = defaults.MaxListDepth
	}

	if l.MaxSearchMatches == 0 {
		l.MaxSearchMatches = defaults.MaxSearchMatches
	}

	if l.MaxSearchFileBytes == 0 {
		l.MaxSearchFileBytes = defaults.MaxSearchFileBytes
	}

	if l.MaxReadBytes == 0 {
		l.MaxReadBytes = defaults.MaxReadBytes
	}

	if l.MaxReadLines == 0 {
		l.MaxReadLines = defaults.MaxReadLines
	}

	if l.MaxWriteBytes == 0 {
		l.MaxWriteBytes = defaults.MaxWriteBytes
	}

	if l.MaxEdits == 0 {
		l.MaxEdits = defaults.MaxEdits
	}

	if l.MaxDiffBytes == 0 {
		l.MaxDiffBytes = defaults.MaxDiffBytes
	}

	if l.MaxRemoveEntries == 0 {
		l.MaxRemoveEntries = defaults.MaxRemoveEntries
	}

	if l.MaxCommandOutputBytes == 0 {
		l.MaxCommandOutputBytes = defaults.MaxCommandOutputBytes
	}

	if l.CommandTimeout == 0 {
		l.CommandTimeout = defaults.CommandTimeout
	}

	if l.MaxCommandTimeout == 0 {
		l.MaxCommandTimeout = defaults.MaxCommandTimeout
	}

	return l
}

func (l Limits) withPatchToolDefaults(defaults Limits) Limits {
	if l.MaxPatchBytes == 0 {
		l.MaxPatchBytes = defaults.MaxPatchBytes
	}

	if l.MaxPatchFiles == 0 {
		l.MaxPatchFiles = defaults.MaxPatchFiles
	}

	if l.MaxPatchHunks == 0 {
		l.MaxPatchHunks = defaults.MaxPatchHunks
	}

	if l.MaxPatchChangedBytes == 0 {
		l.MaxPatchChangedBytes = defaults.MaxPatchChangedBytes
	}

	return l
}

func (l Limits) withJobToolDefaults(defaults Limits) Limits {
	if l.MaxJobOutputLines == 0 {
		l.MaxJobOutputLines = defaults.MaxJobOutputLines
	}

	if l.MaxReadJobOutputLines == 0 {
		l.MaxReadJobOutputLines = defaults.MaxReadJobOutputLines
	}

	if l.WaitJobTimeout == 0 {
		l.WaitJobTimeout = defaults.WaitJobTimeout
	}

	if l.MaxWaitJobTimeout == 0 {
		l.MaxWaitJobTimeout = defaults.MaxWaitJobTimeout
	}

	if l.JobStopGracePeriod == 0 {
		l.JobStopGracePeriod = defaults.JobStopGracePeriod
	}

	if l.MaxListJobs == 0 {
		l.MaxListJobs = defaults.MaxListJobs
	}

	return l
}

func (l Limits) validate() error {
	positives := []int{
		l.MaxListEntries,
		l.MaxListDepth,
		l.MaxSearchMatches,
		l.MaxReadBytes,
		l.MaxReadLines,
		l.MaxWriteBytes,
		l.MaxEdits,
		l.MaxDiffBytes,
		l.MaxPatchBytes,
		l.MaxPatchFiles,
		l.MaxPatchHunks,
		l.MaxPatchChangedBytes,
		l.MaxRemoveEntries,
		l.MaxCommandOutputBytes,
		l.MaxJobOutputLines,
		l.MaxReadJobOutputLines,
		l.MaxListJobs,
	}

	for _, value := range positives {
		if value <= 0 {
			return ctxerrors.Wrap(ErrInvalidLimits, "bound must be positive")
		}
	}

	if l.MaxSearchFileBytes <= 0 {
		return ctxerrors.Wrap(ErrInvalidLimits, "search file bound")
	}

	if l.CommandTimeout <= 0 || l.MaxCommandTimeout <= 0 {
		return ctxerrors.Wrap(ErrInvalidLimits, "command timeout bound")
	}

	if l.CommandTimeout > l.MaxCommandTimeout {
		return ctxerrors.Wrap(ErrInvalidLimits, "default exceeds maximum")
	}

	return l.validateJobLimits()
}

func (l Limits) validateJobLimits() error {
	if l.WaitJobTimeout <= 0 || l.MaxWaitJobTimeout <= 0 {
		return ctxerrors.Wrap(ErrInvalidLimits, "wait job timeout bound")
	}

	if l.WaitJobTimeout > l.MaxWaitJobTimeout {
		return ctxerrors.Wrap(
			ErrInvalidLimits,
			"wait job default exceeds maximum",
		)
	}

	if l.JobStopGracePeriod <= 0 {
		return ctxerrors.Wrap(ErrInvalidLimits, "job stop grace period bound")
	}

	return nil
}
