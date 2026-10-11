package worker_test

import (
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProfile       = "ops"
	testImage         = "psyb0t/peen:test"
	testMountSource   = "/srv/data"
	testRelativeMount = "relative/data"
)

func dockerSpace(profile string) worker.AgentSpace {
	return worker.AgentSpace{
		Name:    worker.DockerSpaceName(profile),
		Kind:    worker.KindDocker,
		Docker:  worker.DockerAccessNone,
		Network: worker.NetworkModeBridge,
	}
}

func TestDockerSpaceName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "docker:ops", worker.DockerSpaceName(testProfile))
	assert.Equal(t, testProfile, dockerSpace(testProfile).DockerProfile())
	assert.Equal(t, "default", worker.DockerSpaceName("default")[len("docker:"):])
}

func TestAgentSpaceValidateDockerGrants(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		mutate  func(*worker.AgentSpace)
		wantErr error
	}{
		{name: "sealed defaults", mutate: func(*worker.AgentSpace) {}},
		{
			name: "docker full",
			mutate: func(s *worker.AgentSpace) {
				s.Docker = worker.DockerAccessFull
			},
		},
		{
			name: "docker readonly is not available yet",
			mutate: func(s *worker.AgentSpace) {
				s.Docker = worker.DockerAccessReadonly
			},
			wantErr: worker.ErrDockerReadonlyUnavailable,
		},
		{
			name: "docker unknown value",
			mutate: func(s *worker.AgentSpace) {
				s.Docker = "rw"
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "docker empty value",
			mutate: func(s *worker.AgentSpace) {
				s.Docker = ""
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "network none",
			mutate: func(s *worker.AgentSpace) {
				s.Network = worker.NetworkModeNone
			},
		},
		{
			name: "network host",
			mutate: func(s *worker.AgentSpace) {
				s.Network = worker.NetworkModeHost
			},
		},
		{
			name: "network unknown value",
			mutate: func(s *worker.AgentSpace) {
				s.Network = "overlay"
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "network empty value",
			mutate: func(s *worker.AgentSpace) {
				s.Network = ""
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:   "sudo",
			mutate: func(s *worker.AgentSpace) { s.Sudo = true },
		},
		{
			name: "relative mount source",
			mutate: func(s *worker.AgentSpace) {
				s.Mounts = []worker.Mount{{Source: testRelativeMount}}
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "relative mount target",
			mutate: func(s *worker.AgentSpace) {
				s.Mounts = []worker.Mount{{
					Source: testMountSource,
					Target: testRelativeMount,
				}}
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "negative revision",
			mutate: func(s *worker.AgentSpace) {
				s.Revision = -1
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "name without the docker prefix",
			mutate: func(s *worker.AgentSpace) {
				s.Name = testProfile
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "profile name with a path separator",
			mutate: func(s *worker.AgentSpace) {
				s.Name = worker.DockerSpaceName("a/b")
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "empty profile name",
			mutate: func(s *worker.AgentSpace) {
				s.Name = worker.DockerSpaceName("")
			},
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			space := dockerSpace(testProfile)
			tc.mutate(&space)

			err := space.Validate()
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestAgentSpaceValidateControl(t *testing.T) {
	t.Parallel()

	control := worker.AgentSpace{
		Name: worker.AgentSpaceControl,
		Kind: worker.KindControl,
	}
	require.NoError(t, control.Validate())

	testCases := []struct {
		name   string
		mutate func(*worker.AgentSpace)
	}{
		{name: "docker grant", mutate: func(s *worker.AgentSpace) {
			s.Docker = worker.DockerAccessFull
		}},
		{name: "network grant", mutate: func(s *worker.AgentSpace) {
			s.Network = worker.NetworkModeHost
		}},
		{name: "sudo grant", mutate: func(s *worker.AgentSpace) {
			s.Sudo = true
		}},
		{name: "image", mutate: func(s *worker.AgentSpace) {
			s.Image = testImage
		}},
		{name: "mounts", mutate: func(s *worker.AgentSpace) {
			s.Mounts = []worker.Mount{{Source: testMountSource}}
		}},
		{name: "another name", mutate: func(s *worker.AgentSpace) {
			s.Name = "host"
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			space := control
			tc.mutate(&space)

			require.ErrorIs(t, space.Validate(), commerr.ErrValidationFailed)
		})
	}
}

func TestAgentSpaceCapabilityWarnings(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		mutate       func(*worker.AgentSpace)
		wantWarnings int
		wantHostRoot bool
	}{
		{
			name:   "sealed defaults warn about nothing",
			mutate: func(*worker.AgentSpace) {},
		},
		{
			name: "network none warns about nothing",
			mutate: func(s *worker.AgentSpace) {
				s.Network = worker.NetworkModeNone
			},
		},
		{
			name: "docker full is host root",
			mutate: func(s *worker.AgentSpace) {
				s.Docker = worker.DockerAccessFull
			},
			wantWarnings: 1,
			wantHostRoot: true,
		},
		{
			name: "network host",
			mutate: func(s *worker.AgentSpace) {
				s.Network = worker.NetworkModeHost
			},
			wantWarnings: 1,
		},
		{
			name:         "sudo",
			mutate:       func(s *worker.AgentSpace) { s.Sudo = true },
			wantWarnings: 1,
		},
		{
			name: "every risky grant is listed",
			mutate: func(s *worker.AgentSpace) {
				s.Docker = worker.DockerAccessFull
				s.Network = worker.NetworkModeHost
				s.Sudo = true
			},
			wantWarnings: 3,
			wantHostRoot: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			space := dockerSpace(testProfile)
			tc.mutate(&space)

			assert.Len(t, space.CapabilityWarnings(), tc.wantWarnings)
			assert.Equal(t, tc.wantHostRoot, space.HostRootEquivalent())
		})
	}

	control := worker.AgentSpace{
		Name: worker.AgentSpaceControl,
		Kind: worker.KindControl,
	}
	assert.Empty(t, control.CapabilityWarnings())
	assert.False(t, control.HostRootEquivalent())
}

func TestAgentSpaceSetSelect(t *testing.T) {
	t.Parallel()

	spaces := []worker.AgentSpace{
		{Name: worker.AgentSpaceControl, Kind: worker.KindControl},
		dockerSpace("default"),
		dockerSpace(testProfile),
	}

	set, err := worker.NewAgentSpaceSet(spaces, worker.DockerSpaceName("default"))
	require.NoError(t, err)

	assert.Equal(t, "docker:default", set.Default())
	assert.Equal(
		t,
		[]string{"control", "docker:default", "docker:ops"},
		set.Names(),
	)

	selected, err := set.Select("")
	require.NoError(t, err)
	assert.Equal(t, "docker:default", selected.Name)

	selected, err = set.Select("docker:ops")
	require.NoError(t, err)
	assert.Equal(t, worker.KindDocker, selected.Kind)

	_, err = set.Select("docker:missing")
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	require.ErrorIs(t, err, worker.ErrAgentSpaceNotAllowed)
}

func TestNewAgentSpaceSetRefusals(t *testing.T) {
	t.Parallel()

	control := worker.AgentSpace{
		Name: worker.AgentSpaceControl,
		Kind: worker.KindControl,
	}

	testCases := []struct {
		name         string
		spaces       []worker.AgentSpace
		defaultSpace string
	}{
		{name: "no spaces", defaultSpace: "control"},
		{
			name:   "no default",
			spaces: []worker.AgentSpace{control},
		},
		{
			name:         "default not defined",
			spaces:       []worker.AgentSpace{control},
			defaultSpace: "docker:default",
		},
		{
			name:         "duplicate space",
			spaces:       []worker.AgentSpace{control, control},
			defaultSpace: "control",
		},
		{
			name: "invalid space",
			spaces: []worker.AgentSpace{{
				Name: "docker:ops",
				Kind: worker.KindDocker,
			}},
			defaultSpace: "docker:ops",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := worker.NewAgentSpaceSet(tc.spaces, tc.defaultSpace)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestAgentSpaceResolvedMountsFillTargets(t *testing.T) {
	t.Parallel()

	space := dockerSpace(testProfile)
	space.Mounts = []worker.Mount{
		{Source: testMountSource},
		{Source: "/srv/other", Target: "/mnt/other", ReadOnly: true},
	}

	assert.Equal(t, []worker.Mount{
		{Source: testMountSource, Target: testMountSource},
		{Source: "/srv/other", Target: "/mnt/other", ReadOnly: true},
	}, space.Resolved())
}

func TestAgentSpaceRequiresDockerAuthorityOnlyForDocker(t *testing.T) {
	t.Parallel()

	assert.True(t, dockerSpace(testProfile).RequiresDockerAuthority())
	assert.False(t, worker.AgentSpace{
		Name: worker.AgentSpaceControl,
		Kind: worker.KindControl,
	}.RequiresDockerAuthority())
}
