package config

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

const (
	envDockerProfiles    = "PEEN_DOCKER_PROFILES"
	envDefaultAgentSpace = "PEEN_DEFAULT_AGENT_SPACE"
)

// DockerProfile is one operator-defined Docker profile as it appears in
// PEEN_DOCKER_PROFILES. A Docker profile shapes the Docker agent space named
// `docker:<name>`.
//
// This is the only place a Docker capability can be turned on. A client names
// an agent space; it never sends an image, a mount, a network setting, or a
// socket request. An omitted `docker` means none, and an omitted `network`
// means bridge.
type DockerProfile struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Image    string `json:"image"`

	Mounts []DockerMount `json:"mounts"`

	Docker  worker.DockerAccess `json:"docker"`
	Network worker.NetworkMode  `json:"network"`
	Sudo    bool                `json:"sudo"`
}

// DockerMount is one extra path a Docker profile exposes. An empty target
// means the worker sees the source at its own absolute path.
type DockerMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

// AgentSpaces returns the deployment's allowed agent spaces.
//
// Every Docker profile is a `docker:<name>` agent space, and the built-in
// `default` profile (no Docker access, bridge network, no sudo) exists unless
// PEEN_DOCKER_PROFILES defines a profile of the same name.
//
// The control agent space is offered only when this controller cannot reach a
// Docker socket. With a reachable socket a control worker, which has the
// controller's own user and groups, could use that socket to start a container
// that mounts the host, so control is refused as the default and never offered.
// With a socket and no PEEN_DEFAULT_AGENT_SPACE the default is
// `docker:default`. Without a socket the default is `control`, and Docker
// agent spaces stay listed but refuse to launch.
func (c Config) AgentSpaces() (*worker.AgentSpaceSet, error) {
	profiles, err := c.dockerProfiles()
	if err != nil {
		return nil, err
	}

	authority := c.DockerAuthority()

	defaultSpace, err := c.defaultAgentSpace(authority)
	if err != nil {
		return nil, err
	}

	spaces := make([]worker.AgentSpace, 0, len(profiles)+1)

	if !authority.Present() {
		spaces = append(spaces, worker.AgentSpace{
			Name: worker.AgentSpaceControl,
			Kind: worker.KindControl,
		})
	}

	for _, profile := range profiles {
		space := profile.toAgentSpace()

		if err := space.Validate(); err != nil {
			return nil, ctxerrors.Wrapf(
				errors.Join(ErrInvalidConfig, err),
				"%s profile %q",
				envDockerProfiles,
				profile.Name,
			)
		}

		spaces = append(spaces, space)
	}

	set, err := worker.NewAgentSpaceSet(spaces, defaultSpace)
	if err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(ErrInvalidConfig, err),
			"validate agent spaces",
		)
	}

	return set, nil
}

// validateAgentSpaces fails startup on a bad Docker profile, a bad default
// agent space, or the control agent space beside a reachable Docker socket.
func (c Config) validateAgentSpaces() error {
	if _, err := c.AgentSpaces(); err != nil {
		return err
	}

	return nil
}

// defaultAgentSpace resolves PEEN_DEFAULT_AGENT_SPACE, refusing `control`
// beside a reachable Docker daemon.
func (c Config) defaultAgentSpace(authority DockerAuthority) (string, error) {
	configured := strings.TrimSpace(c.DefaultAgentSpace)

	if configured == "" {
		if authority.Present() {
			return worker.DockerSpaceName(worker.DockerProfileDefault), nil
		}

		return worker.AgentSpaceControl, nil
	}

	isDocker := strings.HasPrefix(configured, worker.DockerSpacePrefix)
	if configured != worker.AgentSpaceControl && !isDocker {
		return "", ctxerrors.Wrapf(
			ErrInvalidConfig,
			"%s must be %s or %s<profile>, got %q",
			envDefaultAgentSpace,
			worker.AgentSpaceControl,
			worker.DockerSpacePrefix,
			configured,
		)
	}

	if configured == worker.AgentSpaceControl && authority.Present() {
		return "", ctxerrors.Wrapf(
			errors.Join(ErrInvalidConfig, ErrControlSpaceRefused),
			"%s=%s: this controller can reach the Docker daemon at %s, "+
				"and a reachable daemon makes control sessions host root, "+
				"so use Docker profiles instead, for example "+
				"%s=%s, or run the controller without the socket",
			envDefaultAgentSpace,
			worker.AgentSpaceControl,
			authority,
			envDefaultAgentSpace,
			worker.DockerSpaceName(worker.DockerProfileDefault),
		)
	}

	return configured, nil
}

// dockerProfiles returns the built-in default profile and the operator's
// profiles. An operator profile named `default` replaces the built-in one.
func (c Config) dockerProfiles() ([]DockerProfile, error) {
	configured, err := c.configuredDockerProfiles()
	if err != nil {
		return nil, err
	}

	profiles := make([]DockerProfile, 0, len(configured)+1)

	overridesDefault := false

	for _, profile := range configured {
		if profile.Name == worker.DockerProfileDefault {
			overridesDefault = true
		}

		profiles = append(profiles, profile)
	}

	if !overridesDefault {
		profiles = append([]DockerProfile{{
			Name: worker.DockerProfileDefault,
		}}, profiles...)
	}

	return profiles, nil
}

func (c Config) configuredDockerProfiles() ([]DockerProfile, error) {
	if strings.TrimSpace(c.DockerProfilesJSON) == "" {
		return nil, nil
	}

	decoder := json.NewDecoder(strings.NewReader(c.DockerProfilesJSON))
	decoder.DisallowUnknownFields()

	var profiles []DockerProfile
	if err := decoder.Decode(&profiles); err != nil {
		return nil, ctxerrors.Wrapf(
			errors.Join(ErrInvalidConfig, err),
			"parse %s JSON",
			envDockerProfiles,
		)
	}

	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ctxerrors.Wrapf(
			ErrInvalidConfig,
			"%s holds data after the JSON array",
			envDockerProfiles,
		)
	}

	return profiles, nil
}

// toAgentSpace converts a profile, filling the safe defaults for a grant the
// operator left out.
func (p DockerProfile) toAgentSpace() worker.AgentSpace {
	mounts := make([]worker.Mount, 0, len(p.Mounts))
	for _, mount := range p.Mounts {
		mounts = append(mounts, worker.Mount{
			Source:   mount.Source,
			Target:   mount.Target,
			ReadOnly: mount.ReadOnly,
		})
	}

	docker := p.Docker
	if docker == "" {
		docker = worker.DockerAccessNone
	}

	network := p.Network
	if network == "" {
		network = worker.NetworkModeBridge
	}

	return worker.AgentSpace{
		Name:     worker.DockerSpaceName(p.Name),
		Kind:     worker.KindDocker,
		Revision: p.Revision,
		Image:    p.Image,
		Mounts:   mounts,
		Docker:   docker,
		Network:  network,
		Sudo:     p.Sudo,
	}
}
