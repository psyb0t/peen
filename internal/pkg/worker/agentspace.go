package worker

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	// AgentSpaceControl is the built-in agent space whose worker runs where
	// the controller runs. It has no profile and no grants.
	AgentSpaceControl = "control"

	// DockerSpacePrefix starts the name of every Docker agent space. The rest
	// of the name is the Docker profile.
	DockerSpacePrefix = "docker:"

	// DockerProfileDefault is the built-in Docker profile: no Docker access,
	// bridge network, no sudo.
	DockerProfileDefault = "default"

	// maxProfileNameLength bounds a Docker profile name.
	maxProfileNameLength = 64

	warningDockerFull = "docker: full mounts the host Docker socket into the " +
		"worker, which is host root"
	warningNetworkHost = "network: host puts the worker on the host network, " +
		"so it can reach every service the host can"
	warningSudo = "sudo: true lets the agent run commands as root inside the " +
		"worker container"
)

// Mount is one path a Docker worker sees.
//
// Source and Target are literal host paths. Peen does not translate paths: a
// workspace at /srv/work/project is /srv/work/project inside the worker too, so
// paths in prompts, hooks, logs, and tool calls stay truthful. A relocated
// target is a deliberate operator field and is recorded with both paths.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// AgentSpace is one place a session's agent can run.
//
// The control agent space runs the agent where the controller runs. A Docker
// agent space runs it in its own container, shaped by a named Docker profile.
// Every field here comes from deployment configuration. None of it is ever
// taken from model output or a client request: a client names an agent space,
// and the operator decides what that name means.
type AgentSpace struct {
	// Name is `control` or `docker:<profile>`.
	Name string
	Kind Kind

	// Revision increments when an operator changes this Docker profile. Every
	// generation stores the revision it started under, so a durable record
	// says which definition ran.
	Revision int64

	// Image is the container image of a Docker profile. Any reference the
	// Docker daemon can resolve is valid. An image that needs host-account
	// setup must provide Peen's worker entrypoint.
	Image string

	// Mounts are the extra paths beyond the workspace and the global Peen
	// configuration, which the launcher always adds.
	Mounts []Mount

	// Docker is how much of the host Docker daemon the worker reaches.
	Docker DockerAccess

	// Network is the network the worker gets.
	Network NetworkMode

	// Sudo permits passwordless sudo inside the worker.
	Sudo bool
}

// DockerSpaceName is the agent space name of a Docker profile.
func DockerSpaceName(profile string) string {
	return DockerSpacePrefix + profile
}

// DockerProfile is the Docker profile behind a Docker agent space. It is empty
// for any other agent space.
func (s AgentSpace) DockerProfile() string {
	profile, found := strings.CutPrefix(s.Name, DockerSpacePrefix)
	if !found {
		return ""
	}

	return profile
}

// HostRootEquivalent reports whether this agent space gives the worker access
// that is effectively host root. A Docker socket is the clearest case: any
// process that can reach it can start a privileged container that mounts the
// host.
func (s AgentSpace) HostRootEquivalent() bool {
	return s.Docker == DockerAccessFull
}

// CapabilityWarnings lists each risky grant this agent space makes, one
// sentence per grant. It is empty for a space that grants nothing risky.
func (s AgentSpace) CapabilityWarnings() []string {
	warnings := []string{}

	if s.Docker == DockerAccessFull {
		warnings = append(warnings, warningDockerFull)
	}

	if s.Network == NetworkModeHost {
		warnings = append(warnings, warningNetworkHost)
	}

	if s.Sudo {
		warnings = append(warnings, warningSudo)
	}

	return warnings
}

// RequiresDockerAuthority reports whether launching this agent space needs the
// controller's own Docker socket. A deployment that cannot reach one refuses
// the space rather than running the work somewhere else.
func (s AgentSpace) RequiresDockerAuthority() bool {
	return s.Kind == KindDocker
}

// Validate rejects an agent space a deployment cannot run.
func (s AgentSpace) Validate() error {
	if s.Name == "" {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agent space requires a name",
		)
	}

	if !s.Kind.Valid() {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q has an unknown kind",
			s.Name,
		)
	}

	if s.Revision < 0 {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q has a negative revision",
			s.Name,
		)
	}

	switch s.Kind {
	case KindControl:
		return s.validateControl()
	case KindDocker:
		return s.validateDocker()
	}

	return nil
}

// validateControl keeps the control agent space exactly the built-in one.
// It runs where the controller runs, so an image, a mount, or a grant on it
// would promise isolation it cannot give.
func (s AgentSpace) validateControl() error {
	if s.Name != AgentSpaceControl {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q is a control space and must be named %q",
			s.Name,
			AgentSpaceControl,
		)
	}

	if s.Image != "" || len(s.Mounts) != 0 || s.Docker != "" ||
		s.Network != "" || s.Sudo {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"the control agent space takes no image, mounts, or grants",
		)
	}

	return nil
}

func (s AgentSpace) validateDocker() error {
	if !validProfileName(s.DockerProfile()) {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"docker agent space %q must be named %s<profile>, where the "+
				"profile is letters, digits, dots, underscores, and hyphens "+
				"and starts with a letter or digit",
			s.Name,
			DockerSpacePrefix,
		)
	}

	if err := s.validateGrants(); err != nil {
		return err
	}

	return s.validateMounts()
}

func (s AgentSpace) validateGrants() error {
	if !s.Docker.Valid() {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q has docker %q, want none or full",
			s.Name,
			s.Docker,
		)
	}

	if s.Docker == DockerAccessReadonly {
		return ctxerrors.Wrapf(
			errors.Join(
				commerr.ErrValidationFailed,
				ErrDockerReadonlyUnavailable,
			),
			"agent space %q",
			s.Name,
		)
	}

	if !s.Network.Valid() {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"agent space %q has network %q, want none, bridge, or host",
			s.Name,
			s.Network,
		)
	}

	return nil
}

func (s AgentSpace) validateMounts() error {
	for index, mount := range s.Mounts {
		if !filepath.IsAbs(mount.Source) {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"agent space %q mount %d source is not absolute",
				s.Name,
				index,
			)
		}

		if mount.Target != "" && !filepath.IsAbs(mount.Target) {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"agent space %q mount %d target is not absolute",
				s.Name,
				index,
			)
		}
	}

	return nil
}

// validProfileName reports whether a Docker profile name is usable inside a
// Docker agent space name.
func validProfileName(name string) bool {
	if name == "" || len(name) > maxProfileNameLength {
		return false
	}

	for index, char := range name {
		if isASCIIAlphanumeric(char) {
			continue
		}

		if index > 0 && isProfileNameSeparator(char) {
			continue
		}

		return false
	}

	return true
}

func isASCIIAlphanumeric(char rune) bool {
	return (char >= 'a' && char <= 'z') ||
		(char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9')
}

func isProfileNameSeparator(char rune) bool {
	return char == '.' || char == '_' || char == '-'
}

// Resolved returns the mounts with every empty target filled in from its
// source. Source equals target is the default, so a profile only writes a
// target when it deliberately relocates a path.
func (s AgentSpace) Resolved() []Mount {
	resolved := make([]Mount, 0, len(s.Mounts))

	for _, mount := range s.Mounts {
		if mount.Target == "" {
			mount.Target = mount.Source
		}

		resolved = append(resolved, mount)
	}

	return resolved
}

// AgentSpaceSet is the deployment's allowed agent spaces.
type AgentSpaceSet struct {
	spaces   map[string]AgentSpace
	fallback string
}

// NewAgentSpaceSet validates the operator's agent spaces and the default one.
func NewAgentSpaceSet(
	spaces []AgentSpace,
	defaultSpace string,
) (*AgentSpaceSet, error) {
	if len(spaces) == 0 {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"at least one agent space is required",
		)
	}

	owned := make(map[string]AgentSpace, len(spaces))

	for _, space := range spaces {
		if err := space.Validate(); err != nil {
			return nil, err
		}

		if _, exists := owned[space.Name]; exists {
			return nil, ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"duplicate agent space %q",
				space.Name,
			)
		}

		owned[space.Name] = space
	}

	if defaultSpace == "" {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"a default agent space is required",
		)
	}

	if _, exists := owned[defaultSpace]; !exists {
		return nil, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"default agent space %q is not defined",
			defaultSpace,
		)
	}

	return &AgentSpaceSet{spaces: owned, fallback: defaultSpace}, nil
}

// Select resolves a requested agent space name.
//
// An empty name takes the deployment's default. A name the operator did not
// define is refused with commerr.ErrPermissionDenied, because naming an agent
// space is the only worker choice a client gets: it never supplies an image,
// mount, socket, network setting, capability, or credential.
func (s *AgentSpaceSet) Select(name string) (AgentSpace, error) {
	if name == "" {
		name = s.fallback
	}

	space, found := s.spaces[name]
	if !found {
		return AgentSpace{}, ctxerrors.Wrapf(
			errors.Join(commerr.ErrPermissionDenied, ErrAgentSpaceNotAllowed),
			"agent space %q is not allowed by this deployment",
			name,
		)
	}

	return space, nil
}

// Default returns the deployment's default agent space name.
func (s *AgentSpaceSet) Default() string {
	return s.fallback
}

// Names lists every allowed agent space name in a stable order.
func (s *AgentSpaceSet) Names() []string {
	names := make([]string, 0, len(s.spaces))
	for name := range s.spaces {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// All returns every allowed agent space in a stable order.
func (s *AgentSpaceSet) All() []AgentSpace {
	spaces := make([]AgentSpace, 0, len(s.spaces))
	for _, name := range s.Names() {
		spaces = append(spaces, s.spaces[name])
	}

	return spaces
}
