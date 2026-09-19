package config

import (
	"encoding/json"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

// ExecutionProfile is one operator-defined profile as it appears in
// PEEN_EXECUTION_PROFILES.
//
// This is the only place a Docker capability can be turned on. A client names a
// profile; it never sends an image, a mount, a network setting, or a socket
// request.
type ExecutionProfile struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Revision int64  `json:"revision"`
	Image    string `json:"image"`

	Mounts []ExecutionMount `json:"mounts"`

	AllowDockerSocket        bool `json:"allowDockerSocket"`
	AllowNetwork             bool `json:"allowNetwork"`
	AllowPrivilegeEscalation bool `json:"allowPrivilegeEscalation"`
}

// ExecutionMount is one extra path a profile exposes. An empty target means
// the worker sees the source at its own absolute path.
type ExecutionMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

// ExecutionProfiles returns the deployment's allowed execution profiles.
//
// An unset PEEN_EXECUTION_PROFILES yields the single native profile, so a
// deployment that configures nothing keeps running on the host.
func (c Config) ExecutionProfiles() (*worker.ProfileSet, error) {
	configured, err := c.configuredExecutionProfiles()
	if err != nil {
		return nil, err
	}

	profiles := make([]worker.Profile, 0, len(configured))

	for index, profile := range configured {
		converted, err := profile.toWorkerProfile()
		if err != nil {
			return nil, ctxerrors.Wrapf(
				err,
				"PEEN_EXECUTION_PROFILES entry %d",
				index,
			)
		}

		profiles = append(profiles, converted)
	}

	defaultProfile := c.DefaultExecutionProfile
	if defaultProfile == "" {
		defaultProfile = worker.ProfileNative
	}

	set, err := worker.NewProfileSet(profiles, defaultProfile)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate execution profiles")
	}

	return set, nil
}

func (c Config) configuredExecutionProfiles() ([]ExecutionProfile, error) {
	if strings.TrimSpace(c.ExecutionProfilesJSON) == "" {
		return []ExecutionProfile{{
			Name: worker.ProfileNative,
			Kind: string(worker.KindNative),
		}}, nil
	}

	var profiles []ExecutionProfile
	if err := json.Unmarshal(
		[]byte(c.ExecutionProfilesJSON),
		&profiles,
	); err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"parse PEEN_EXECUTION_PROFILES JSON",
		)
	}

	return profiles, nil
}

func (p ExecutionProfile) toWorkerProfile() (worker.Profile, error) {
	mounts := make([]worker.Mount, 0, len(p.Mounts))
	for _, mount := range p.Mounts {
		mounts = append(mounts, worker.Mount{
			Source:   mount.Source,
			Target:   mount.Target,
			ReadOnly: mount.ReadOnly,
		})
	}

	converted := worker.Profile{
		Name:                     p.Name,
		Kind:                     worker.Kind(p.Kind),
		Revision:                 p.Revision,
		Image:                    p.Image,
		Mounts:                   mounts,
		AllowDockerSocket:        p.AllowDockerSocket,
		AllowNetwork:             p.AllowNetwork,
		AllowPrivilegeEscalation: p.AllowPrivilegeEscalation,
	}

	if err := converted.Validate(); err != nil {
		return worker.Profile{}, ctxerrors.Wrap(
			err,
			"validate execution profile",
		)
	}

	return converted, nil
}
