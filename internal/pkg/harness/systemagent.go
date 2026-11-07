package harness

import (
	_ "embed" // The system agent's document ships in the binary.
	"strings"
	"text/template"

	"github.com/psyb0t/ctxerrors"
)

const (
	// SystemAgentName is the root agent of the system session. Only the
	// controller writes this value on a session, and it is never in a normal
	// snapshot, so the model cannot reach it.
	SystemAgentName = CorePrefix + systemAgentBareName

	systemAgentBareName = "system"
	systemAgentSource   = embeddedAgentsSourcePrefix + systemAgentBareName +
		agentsFileExtension
)

// embeddedAgentAssets leaves this document out, so no normal snapshot holds
// the system agent.
//
//go:embed builtin/agents/system.md
var systemAgentDocument string

// SystemAgentPaths are the directories the system agent's instructions name.
type SystemAgentPaths struct {
	ConfigDirectory string
	StateDirectory  string
}

// WithSystemAgent makes every snapshot hold the system agent and nothing else:
// no embedded instructions, skills, or agents, and no file from any layer. The
// system agent has more authority than any session, so nothing a session could
// have written may reach its prompt.
func WithSystemAgent(paths SystemAgentPaths) ResolverOption {
	return func(settings *resolverSettings) {
		settings.systemAgent = &paths
	}
}

// IsReservedAgentName reports a name no agent file or setting may take.
func IsReservedAgentName(name string) bool {
	return name == systemAgentBareName || name == SystemAgentName
}

func (r Resolver) resolveSystemAgent(workspace string) (Snapshot, error) {
	resolvedWorkspace, err := canonicalDirectory(workspace)
	if err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "canonicalize harness workspace")
	}

	state := newResolutionState(r.limits)
	if err := state.discoverSystemAgent(*r.systemAgent); err != nil {
		return Snapshot{}, err
	}

	snapshot, err := state.snapshot(r.configRoot, resolvedWorkspace)
	if err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "build system agent snapshot")
	}

	return snapshot, nil
}

func (s *resolutionState) discoverSystemAgent(paths SystemAgentPaths) error {
	content, err := renderSystemAgent(paths)
	if err != nil {
		return err
	}

	metadata, instructions, err := parseAgentDocument(content)
	if err != nil {
		return ctxerrors.Wrap(err, "validate the system agent")
	}

	allowedTools, err := parseAgentAllowedTools(metadata.AllowedTools)
	if err != nil {
		return ctxerrors.Wrap(err, "parse the system agent's allowed tools")
	}

	s.agents[SystemAgentName] = Agent{
		Name:         SystemAgentName,
		Description:  metadata.Description,
		Source:       systemAgentSource,
		Instructions: instructions,
		AllowedTools: allowedTools,
		Hash:         hashString(content),
	}

	return nil
}

func renderSystemAgent(paths SystemAgentPaths) (string, error) {
	parsed, err := template.New(systemAgentBareName).Parse(systemAgentDocument)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse the system agent template")
	}

	var rendered strings.Builder
	if err := parsed.Execute(&rendered, paths); err != nil {
		return "", ctxerrors.Wrap(err, "render the system agent template")
	}

	return rendered.String(), nil
}
