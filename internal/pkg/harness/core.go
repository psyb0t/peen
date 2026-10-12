package harness

import (
	"errors"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

const (
	// CorePrefix addresses a core skill or agent, the ones embedded in the
	// binary. A custom skill or agent name never contains ":", so the prefix
	// cannot collide with a name from a layer.
	CorePrefix = "core:"

	// DisableAllCore selects every core skill or agent in a disable list.
	DisableAllCore = "all"

	coreSkillKind = "skill"
	coreAgentKind = "agent"

	disabledCoreAgentsSetting = "PEEN_DISABLED_CORE_AGENTS"
	validNamesSeparator       = ", "
)

// ResolverOption adjusts how a Resolver builds its snapshots.
type ResolverOption func(*resolverSettings)

type resolverSettings struct {
	memoryLayers   []workspacefs.Reader
	filesystem     workspacefs.Reader
	disabledSkills []string
	disabledAgents []string
}

// WithMemoryLayers adds immutable layers after disk and workspace discovery.
func WithMemoryLayers(layers []workspacefs.Reader) ResolverOption {
	return func(settings *resolverSettings) {
		settings.memoryLayers = slices.Clone(layers)
	}
}

// WithWorkspaceFilesystem confines workspace discovery to a virtual root.
// The trusted configuration directory is still read from the host.
func WithWorkspaceFilesystem(
	filesystem workspacefs.Reader,
	virtual bool,
) ResolverOption {
	return func(settings *resolverSettings) {
		if virtual {
			settings.filesystem = filesystem
		}
	}
}

// WithDisabledCoreSkills removes the named core skills, given without the
// core prefix, from every snapshot. NewResolver rejects a name no core skill
// has.
func WithDisabledCoreSkills(names []string) ResolverOption {
	return func(settings *resolverSettings) {
		settings.disabledSkills = slices.Clone(names)
	}
}

// WithDisabledCoreAgents removes the named core agents, given without the
// core prefix, from every snapshot. NewResolver rejects a name no core agent
// has.
func WithDisabledCoreAgents(names []string) ResolverOption {
	return func(settings *resolverSettings) {
		settings.disabledAgents = slices.Clone(names)
	}
}

// CoreSkillNames lists the core skills, without the core prefix, in sorted
// order.
func CoreSkillNames() []string {
	assets := embeddedSkillAssets()
	names := make([]string, 0, len(assets))

	for _, asset := range assets {
		names = append(names, asset.name)
	}

	slices.Sort(names)

	return names
}

// CoreAgentNames lists the core agents, without the core prefix, in sorted
// order.
func CoreAgentNames() []string {
	assets := embeddedAgentAssets()
	names := make([]string, 0, len(assets))

	for _, asset := range assets {
		names = append(names, asset.name)
	}

	slices.Sort(names)

	return names
}

// ParseDisabledCoreSkills reads a PEEN_DISABLED_CORE_SKILLS value: core skill
// names without the core prefix, separated by commas, or "all". Whitespace
// around an entry is ignored, as are empty entries. The result is sorted and
// free of duplicates.
func ParseDisabledCoreSkills(raw string) ([]string, error) {
	return parseDisabledCore(raw, coreSkillKind, CoreSkillNames())
}

// ParseDisabledCoreAgents reads a PEEN_DISABLED_CORE_AGENTS value with the
// same rules as ParseDisabledCoreSkills.
func ParseDisabledCoreAgents(raw string) ([]string, error) {
	return parseDisabledCore(raw, coreAgentKind, CoreAgentNames())
}

func parseDisabledCore(
	raw string,
	kind string,
	valid []string,
) ([]string, error) {
	selected := make([]string, 0)
	all := false

	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		if entry == DisableAllCore {
			all = true

			continue
		}

		if err := requireCoreName(kind, entry, valid); err != nil {
			return nil, err
		}

		selected = append(selected, entry)
	}

	if all {
		return slices.Clone(valid), nil
	}

	slices.Sort(selected)

	return slices.Compact(selected), nil
}

func requireCoreName(kind string, name string, valid []string) error {
	if slices.Contains(valid, name) {
		return nil
	}

	return ctxerrors.Wrapf(
		ErrUnknownCoreName,
		"core %s %q does not exist, valid names are %s and %s",
		kind,
		name,
		strings.Join(valid, validNamesSeparator),
		DisableAllCore,
	)
}

func validateDisabledCore(settings resolverSettings) error {
	for _, name := range settings.disabledSkills {
		if err := requireCoreName(
			coreSkillKind,
			name,
			CoreSkillNames(),
		); err != nil {
			return err
		}
	}

	for _, name := range settings.disabledAgents {
		if err := requireCoreName(
			coreAgentKind,
			name,
			CoreAgentNames(),
		); err != nil {
			return err
		}
	}

	return nil
}

// ValidateCoreAgentReference checks the part of an agent reference that needs
// no filesystem: a core: reference must name an existing core agent that is
// not disabled. Any other reference passes, because a plain name may be a
// custom agent only a layer knows about.
func ValidateCoreAgentReference(name string, disabled []string) error {
	bare, isCore := strings.CutPrefix(name, CorePrefix)
	if !isCore {
		return nil
	}

	valid := CoreAgentNames()
	if !slices.Contains(valid, bare) {
		return ctxerrors.Wrapf(
			ErrAgentNotFound,
			"core agent %q does not exist, core agents are %s",
			name,
			strings.Join(prefixedNames(valid), validNamesSeparator),
		)
	}

	if slices.Contains(disabled, bare) {
		return ctxerrors.Wrapf(
			ErrAgentNotFound,
			"core agent %q is disabled by %s",
			name,
			disabledCoreAgentsSetting,
		)
	}

	return nil
}

// CheckRootAgent verifies at startup the root agent failures that no
// workspace can fix: a core: name that is unknown or disabled. Any plain name
// passes, because a workspace may define it. It resolves per turn and fails
// then if no layer has it.
func (r Resolver) CheckRootAgent(name string) error {
	return ValidateCoreAgentReference(name, r.disabledAgents)
}

// lookupAddresses lists the addresses a name may resolve to, best first. A
// plain name prefers the custom entry and falls back to the core one. A name
// with the core prefix names only the core entry.
func lookupAddresses(name string) []string {
	if strings.HasPrefix(name, CorePrefix) {
		return []string{name}
	}

	return []string{name, CorePrefix + name}
}

func isCoreName(name string) bool {
	return strings.HasPrefix(name, CorePrefix)
}

func prefixedNames(names []string) []string {
	prefixed := make([]string, 0, len(names))
	for _, name := range names {
		prefixed = append(prefixed, CorePrefix+name)
	}

	return prefixed
}

func joinOptionsError(err error) error {
	return errors.Join(ErrInvalidOptions, err)
}
