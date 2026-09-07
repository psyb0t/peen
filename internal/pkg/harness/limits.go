package harness

const (
	defaultMaxFiles             = 256
	defaultMaxInstructions      = 64
	defaultMaxSkills            = 64
	defaultMaxAgents            = 64
	defaultMaxEventHandlers     = 64
	defaultMaxHooks             = 256
	defaultMaxDirectoryEntries  = 1024
	defaultMaxFileBytes         = 128 * 1024
	defaultMaxTotalContextBytes = 1024 * 1024
	maximumBoundedReadBytes     = int64(1<<63 - 2)
)

// Limits bounds filesystem-derived harness context. Zero fields use defaults.
type Limits struct {
	MaxFiles             int
	MaxInstructions      int
	MaxSkills            int
	MaxAgents            int
	MaxEventHandlers     int
	MaxHooks             int
	MaxDirectoryEntries  int
	MaxFileBytes         int64
	MaxTotalContextBytes int64
}

// DefaultLimits returns the bounds a zero Limits resolves to. Wiring code
// needs them to keep a separate bound consistent with the file bound rather
// than restating the number and letting the two drift.
func DefaultLimits() Limits {
	return defaultLimits()
}

func defaultLimits() Limits {
	return Limits{
		MaxFiles:             defaultMaxFiles,
		MaxInstructions:      defaultMaxInstructions,
		MaxSkills:            defaultMaxSkills,
		MaxAgents:            defaultMaxAgents,
		MaxEventHandlers:     defaultMaxEventHandlers,
		MaxHooks:             defaultMaxHooks,
		MaxDirectoryEntries:  defaultMaxDirectoryEntries,
		MaxFileBytes:         defaultMaxFileBytes,
		MaxTotalContextBytes: defaultMaxTotalContextBytes,
	}
}

func (l Limits) withDefaults() Limits {
	defaults := defaultLimits()

	if l.MaxFiles == 0 {
		l.MaxFiles = defaults.MaxFiles
	}

	if l.MaxInstructions == 0 {
		l.MaxInstructions = defaults.MaxInstructions
	}

	if l.MaxSkills == 0 {
		l.MaxSkills = defaults.MaxSkills
	}

	if l.MaxAgents == 0 {
		l.MaxAgents = defaults.MaxAgents
	}

	if l.MaxEventHandlers == 0 {
		l.MaxEventHandlers = defaults.MaxEventHandlers
	}

	if l.MaxHooks == 0 {
		l.MaxHooks = defaults.MaxHooks
	}

	if l.MaxDirectoryEntries == 0 {
		l.MaxDirectoryEntries = defaults.MaxDirectoryEntries
	}

	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = defaults.MaxFileBytes
	}

	if l.MaxTotalContextBytes == 0 {
		l.MaxTotalContextBytes = defaults.MaxTotalContextBytes
	}

	return l
}

func (l Limits) validate() error {
	if l.hasInvalidCounts() {
		return ErrInvalidOptions
	}

	if l.MaxFileBytes < 1 || l.MaxFileBytes > maximumBoundedReadBytes {
		return ErrInvalidOptions
	}

	if l.MaxTotalContextBytes < 1 || l.MaxTotalContextBytes < l.MaxFileBytes {
		return ErrInvalidOptions
	}

	return nil
}

func (l Limits) hasInvalidCounts() bool {
	return l.MaxFiles < 1 || l.MaxInstructions < 1 ||
		l.MaxSkills < 1 || l.MaxAgents < 1 ||
		l.MaxEventHandlers < 1 || l.MaxHooks < 1 ||
		l.MaxDirectoryEntries < 1
}
