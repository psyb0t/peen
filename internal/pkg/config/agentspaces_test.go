package config

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSocketName  = "docker.sock"
	testOpsProfile  = "ops"
	testOpsSpace    = "docker:ops"
	testDefaultName = "docker:default"
)

// reachableSocket starts a Unix socket that answers the Docker API ping the way
// a mounted Docker socket does.
func reachableSocket(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), testSocketName)

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(context.Background(), "unix", path)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: time.Second,
	}

	go func() { _ = server.Serve(listener) }()

	t.Cleanup(func() { _ = server.Close() })

	return path
}

func absentSocket(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), testSocketName)
}

func withSocket(config Config, path string) Config {
	config.DockerSocket = path

	return config
}

func TestAgentSpacesWithoutASocketDefaultToControl(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), absentSocket(t))

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)

	assert.Equal(t, worker.AgentSpaceControl, spaces.Default())
	assert.Equal(t, []string{"control", testDefaultName}, spaces.Names())

	control, err := spaces.Select("")
	require.NoError(t, err)
	assert.Equal(t, worker.KindControl, control.Kind)
}

func TestAgentSpacesWithASocketAndNothingSetDefaultToDockerDefault(
	t *testing.T,
) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)

	assert.Equal(t, testDefaultName, spaces.Default())
	assert.Equal(t, []string{testDefaultName}, spaces.Names())

	selected, err := spaces.Select("")
	require.NoError(t, err)
	assert.Equal(t, worker.KindDocker, selected.Kind)
	assert.Equal(t, worker.DockerAccessNone, selected.Docker)
	assert.Equal(t, worker.NetworkModeBridge, selected.Network)
	assert.False(t, selected.Sudo)
}

func TestAgentSpacesRefuseControlWhenTheDockerSocketIsReachable(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))
	config.DefaultAgentSpace = worker.AgentSpaceControl

	_, err := config.AgentSpaces()
	require.ErrorIs(t, err, ErrControlSpaceRefused)
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "host root")
	assert.Contains(t, err.Error(), "Docker profiles")
}

func TestValidateRefusesControlWhenTheDockerSocketIsReachable(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))
	config.DefaultAgentSpace = worker.AgentSpaceControl

	require.ErrorIs(t, config.Validate(), ErrControlSpaceRefused)
}

func TestValidateAcceptsTheSocketDefaultWithNothingSet(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))

	require.NoError(t, config.Validate())
}

func TestWorkerValidationIgnoresAgentSpaces(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))
	config.DefaultAgentSpace = worker.AgentSpaceControl

	require.NoError(t, config.ValidateWorker())
}

func TestAgentSpacesWithoutASocketKeepControlWhenAskedFor(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), absentSocket(t))
	config.DefaultAgentSpace = worker.AgentSpaceControl

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)
	assert.Equal(t, worker.AgentSpaceControl, spaces.Default())
}

func TestAgentSpacesWithoutASocketAllowADockerDefault(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), absentSocket(t))
	config.DefaultAgentSpace = testDefaultName

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)
	assert.Equal(t, testDefaultName, spaces.Default())
}

func TestAgentSpacesAcceptAConfiguredDefaultProfile(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))
	config.DockerProfilesJSON = `[{"name":"ops","docker":"full"}]`
	config.DefaultAgentSpace = testOpsSpace

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)
	assert.Equal(t, testOpsSpace, spaces.Default())
	assert.Equal(t, []string{testDefaultName, testOpsSpace}, spaces.Names())
}

func TestAgentSpacesRefuseABadDefault(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		defaultSpace string
	}{
		{name: "an undefined profile", defaultSpace: "docker:missing"},
		{name: "the removed native name", defaultSpace: "native"},
		{name: "an empty profile", defaultSpace: "docker:"},
		{name: "a bad profile name", defaultSpace: "docker:a/b"},
		{name: "another prefix", defaultSpace: "host:default"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := withSocket(testConfig(t), absentSocket(t))
			config.DefaultAgentSpace = tc.defaultSpace

			_, err := config.AgentSpaces()
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestDockerProfileGrantValues(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		profileJSON string
		wantDocker  worker.DockerAccess
		wantNetwork worker.NetworkMode
		wantSudo    bool
		wantErr     error
	}{
		{
			name:        "omitted grants take the safe defaults",
			profileJSON: `{"name":"ops"}`,
			wantDocker:  worker.DockerAccessNone,
			wantNetwork: worker.NetworkModeBridge,
		},
		{
			name:        "docker none",
			profileJSON: `{"name":"ops","docker":"none"}`,
			wantDocker:  worker.DockerAccessNone,
			wantNetwork: worker.NetworkModeBridge,
		},
		{
			name:        "docker full",
			profileJSON: `{"name":"ops","docker":"full"}`,
			wantDocker:  worker.DockerAccessFull,
			wantNetwork: worker.NetworkModeBridge,
		},
		{
			name:        "docker readonly is not available yet",
			profileJSON: `{"name":"ops","docker":"readonly"}`,
			wantErr:     worker.ErrDockerReadonlyUnavailable,
		},
		{
			name:        "docker unknown value",
			profileJSON: `{"name":"ops","docker":"rw"}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "network none",
			profileJSON: `{"name":"ops","network":"none"}`,
			wantDocker:  worker.DockerAccessNone,
			wantNetwork: worker.NetworkModeNone,
		},
		{
			name:        "network bridge",
			profileJSON: `{"name":"ops","network":"bridge"}`,
			wantDocker:  worker.DockerAccessNone,
			wantNetwork: worker.NetworkModeBridge,
		},
		{
			name:        "network host",
			profileJSON: `{"name":"ops","network":"host"}`,
			wantDocker:  worker.DockerAccessNone,
			wantNetwork: worker.NetworkModeHost,
		},
		{
			name:        "network unknown value",
			profileJSON: `{"name":"ops","network":"overlay"}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "sudo true",
			profileJSON: `{"name":"ops","sudo":true}`,
			wantDocker:  worker.DockerAccessNone,
			wantNetwork: worker.NetworkModeBridge,
			wantSudo:    true,
		},
		{
			name:        "sudo false",
			profileJSON: `{"name":"ops","sudo":false}`,
			wantDocker:  worker.DockerAccessNone,
			wantNetwork: worker.NetworkModeBridge,
		},
		{
			name:        "sudo is a boolean",
			profileJSON: `{"name":"ops","sudo":"yes"}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "the removed allowNetwork key",
			profileJSON: `{"name":"ops","allowNetwork":true}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "the removed allowDockerSocket key",
			profileJSON: `{"name":"ops","allowDockerSocket":true}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "the removed allowPrivilegeEscalation key",
			profileJSON: `{"name":"ops","allowPrivilegeEscalation":true}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "the removed kind key",
			profileJSON: `{"name":"ops","kind":"docker"}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "relative mount source",
			profileJSON: `{"name":"ops","mounts":[{"source":"data"}]}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "a bad profile name",
			profileJSON: `{"name":"a b"}`,
			wantErr:     ErrInvalidConfig,
		},
		{
			name:        "no profile name",
			profileJSON: `{"image":"psyb0t/peen:test"}`,
			wantErr:     ErrInvalidConfig,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := withSocket(testConfig(t), reachableSocket(t))
			config.DockerProfilesJSON = "[" + tc.profileJSON + "]"

			spaces, err := config.AgentSpaces()
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)

			space, err := spaces.Select(testOpsSpace)
			require.NoError(t, err)
			assert.Equal(t, tc.wantDocker, space.Docker)
			assert.Equal(t, tc.wantNetwork, space.Network)
			assert.Equal(t, tc.wantSudo, space.Sudo)
		})
	}
}

func TestDockerProfilesRefuseMalformedJSON(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		profilesRaw string
	}{
		{name: "not JSON", profilesRaw: "ops"},
		{name: "an object instead of an array", profilesRaw: `{"name":"ops"}`},
		{name: "trailing data", profilesRaw: `[{"name":"ops"}] x`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := withSocket(testConfig(t), reachableSocket(t))
			config.DockerProfilesJSON = tc.profilesRaw

			_, err := config.AgentSpaces()
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestDockerProfilesRefuseADuplicateName(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))
	config.DockerProfilesJSON = `[{"name":"ops"},{"name":"ops"}]`

	_, err := config.AgentSpaces()
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestAConfiguredDefaultProfileReplacesTheBuiltInOne(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))
	config.DockerProfilesJSON = `[{"name":"default","network":"none","revision":3}]`

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)

	space, err := spaces.Select(testDefaultName)
	require.NoError(t, err)
	assert.Equal(t, worker.NetworkModeNone, space.Network)
	assert.Equal(t, int64(3), space.Revision)
}

func TestDockerProfileCarriesImageAndMounts(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))
	config.DockerProfilesJSON = `[{
		"name":"ops",
		"image":"psyb0t/peen:test",
		"mounts":[{"source":"/srv/data","readOnly":true},
			{"source":"/srv/other","target":"/mnt/other"}]
	}]`

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)

	space, err := spaces.Select(testOpsSpace)
	require.NoError(t, err)
	assert.Equal(t, "psyb0t/peen:test", space.Image)
	assert.Equal(t, []worker.Mount{
		{Source: "/srv/data", Target: "/srv/data", ReadOnly: true},
		{Source: "/srv/other", Target: "/mnt/other"},
	}, space.Resolved())
}

func TestDockerProfileCapabilityWarningsNameEachRiskyGrant(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), reachableSocket(t))
	config.DockerProfilesJSON = `[{"name":"ops","docker":"full",` +
		`"network":"host","sudo":true}]`

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)

	space, err := spaces.Select(testOpsSpace)
	require.NoError(t, err)

	warnings := space.CapabilityWarnings()
	require.Len(t, warnings, 3)
	assert.Contains(t, warnings[0], "docker: full")
	assert.Contains(t, warnings[0], "host root")
	assert.Contains(t, warnings[1], "network: host")
	assert.Contains(t, warnings[2], "sudo")
	assert.True(t, space.HostRootEquivalent())

	builtIn, err := spaces.Select(testDefaultName)
	require.NoError(t, err)
	assert.Empty(t, builtIn.CapabilityWarnings())
}

func TestDockerProfilesAreNotPassedToWorkers(t *testing.T) {
	t.Parallel()

	assert.True(t, workerEnvironmentReserved("PEEN_DOCKER_PROFILES"))
	assert.True(t, workerEnvironmentReserved("PEEN_DEFAULT_AGENT_SPACE"))
	assert.False(t, workerRuntimeEnvironmentKey("PEEN_DOCKER_PROFILES"))
	assert.False(t, workerRuntimeEnvironmentKey("PEEN_DEFAULT_AGENT_SPACE"))
}
