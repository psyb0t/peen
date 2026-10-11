package control_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSandboxSpace  = "docker:sandbox"
	testRiskySpace    = "docker:ops"
	testUndefinedName = "docker:never-defined"
	testSandboxImage  = "psyb0t/peen@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
)

// reconfigureFixture is a registry over a real store with three allowed agent
// spaces: control, a sealed Docker space, and a Docker space with risky grants.
//
// Workers is left nil on purpose. This file asserts the durable decision the
// control surface records; the worker replacement that follows a decision is
// asserted against a real socket and a real generation in the supervisor
// package.
//
// These tests do not call t.Parallel. db.Open installs the generated
// repositories as a package default, which is process-global state, so two
// fixtures opening a database at once race.
type reconfigureFixture struct {
	registry  *control.Registry
	store     *session.Store
	sessionID uuid.UUID
}

func newReconfigureFixture(t *testing.T) reconfigureFixture {
	t.Helper()

	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	root := canonicalTempDir(t)

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	agentSpaces, err := worker.NewAgentSpaceSet(
		[]worker.AgentSpace{
			{
				Name:     worker.AgentSpaceControl,
				Kind:     worker.KindControl,
				Revision: 1,
			},
			{
				Name:     testSandboxSpace,
				Kind:     worker.KindDocker,
				Revision: 2,
				Image:    testSandboxImage,
				Docker:   worker.DockerAccessNone,
				Network:  worker.NetworkModeBridge,
			},
			{
				Name:     testRiskySpace,
				Kind:     worker.KindDocker,
				Revision: 3,
				Image:    testSandboxImage,
				Docker:   worker.DockerAccessFull,
				Network:  worker.NetworkModeHost,
				Sudo:     true,
			},
		},
		worker.AgentSpaceControl,
	)
	require.NoError(t, err)

	registry, err := control.NewRegistry(control.RegistryOptions{
		Store:        store,
		Policy:       policy,
		AgentSpaces:  agentSpaces,
		RootAgent:    testRootAgent,
		DefaultModel: testDefaultModel,
	})
	require.NoError(t, err)

	opened, err := registry.Open(t.Context(), root, worker.AgentSpaceControl)
	require.NoError(t, err)

	return reconfigureFixture{
		registry:  registry,
		store:     store,
		sessionID: opened.Session.ID,
	}
}

// The list a client reads carries every agent space with its grants, and the
// warnings name each risky grant.
func TestRegistryAgentSpacesReportsTheGrants(t *testing.T) {
	fixture := newReconfigureFixture(t)

	list := fixture.registry.AgentSpaces()

	assert.Equal(t, worker.AgentSpaceControl, list.Default)
	require.Len(t, list.Items, 3)

	byName := map[string]api.AgentSpace{}
	for _, item := range list.Items {
		byName[item.Name] = item
	}

	controlSpace := byName[worker.AgentSpaceControl]
	assert.Equal(t, api.AgentSpaceKindControl, controlSpace.Kind)
	assert.Nil(t, controlSpace.Docker)
	assert.Nil(t, controlSpace.Network)
	assert.Nil(t, controlSpace.Sudo)
	assert.Nil(t, controlSpace.Profile)
	assert.False(t, controlSpace.HostRootEquivalent)
	assert.Empty(t, controlSpace.CapabilityWarnings)

	sealed := byName[testSandboxSpace]
	assert.Equal(t, api.AgentSpaceKindDocker, sealed.Kind)
	require.NotNil(t, sealed.Profile)
	assert.Equal(t, "sandbox", *sealed.Profile)
	require.NotNil(t, sealed.Docker)
	assert.Equal(t, api.AgentSpaceDockerNone, *sealed.Docker)
	require.NotNil(t, sealed.Network)
	assert.Equal(t, api.AgentSpaceNetworkBridge, *sealed.Network)
	require.NotNil(t, sealed.Sudo)
	assert.False(t, *sealed.Sudo)
	assert.False(t, sealed.HostRootEquivalent)
	assert.Empty(t, sealed.CapabilityWarnings)

	risky := byName[testRiskySpace]
	require.NotNil(t, risky.Docker)
	assert.Equal(t, api.AgentSpaceDockerFull, *risky.Docker)
	require.NotNil(t, risky.Network)
	assert.Equal(t, api.AgentSpaceNetworkHost, *risky.Network)
	require.NotNil(t, risky.Sudo)
	assert.True(t, *risky.Sudo)
	assert.True(t, risky.HostRootEquivalent)
	assert.Len(t, risky.CapabilityWarnings, 3)
}

// An agent space change moves the session and records the caller's reason.
func TestRegistryReconfigureSessionRecordsTheDecision(t *testing.T) {
	fixture := newReconfigureFixture(t)

	decision, err := fixture.registry.ReconfigureSession(
		t.Context(),
		fixture.sessionID,
		api.ReconfigureSessionRequest{
			AgentSpace: testSandboxSpace,
			Reason:     "isolating the workspace",
		},
	)
	require.NoError(t, err)

	assert.Equal(t, testSandboxSpace, decision.ToAgentSpace)
	require.NotNil(t, decision.FromAgentSpace)
	assert.Equal(t, worker.AgentSpaceControl, *decision.FromAgentSpace)
	assert.Equal(t, "isolating the workspace", decision.Reason)

	stored, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, testSandboxSpace, stored.AgentSpace)
}

// An agent space the deployment never offered is refused, and nothing changes.
func TestRegistryReconfigureSessionRefusesAnUndefinedAgentSpace(t *testing.T) {
	fixture := newReconfigureFixture(t)

	_, err := fixture.registry.ReconfigureSession(
		t.Context(),
		fixture.sessionID,
		api.ReconfigureSessionRequest{
			AgentSpace: testUndefinedName,
			Reason:     "wants the host",
		},
	)

	require.ErrorIs(t, err, commerr.ErrPermissionDenied)

	stored, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, worker.AgentSpaceControl, stored.AgentSpace)

	page, err := fixture.registry.ListAgentSpaceDecisions(
		t.Context(),
		fixture.sessionID,
		api.ListSessionAgentSpaceDecisionsParams{},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

// The history reports every change newest first, with the reason given.
func TestRegistryListAgentSpaceDecisionsReportsTheHistory(t *testing.T) {
	fixture := newReconfigureFixture(t)

	changes := []struct {
		agentSpace string
		reason     string
	}{
		{agentSpace: testSandboxSpace, reason: "isolating the workspace"},
		{agentSpace: worker.AgentSpaceControl, reason: "back to control"},
	}

	for _, change := range changes {
		_, err := fixture.registry.ReconfigureSession(
			t.Context(),
			fixture.sessionID,
			api.ReconfigureSessionRequest{
				AgentSpace: change.agentSpace,
				Reason:     change.reason,
			},
		)
		require.NoError(t, err)
	}

	page, err := fixture.registry.ListAgentSpaceDecisions(
		t.Context(),
		fixture.sessionID,
		api.ListSessionAgentSpaceDecisionsParams{},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	assert.Equal(t, worker.AgentSpaceControl, page.Items[0].ToAgentSpace)
	assert.Equal(t, "back to control", page.Items[0].Reason)
	require.NotNil(t, page.Items[0].FromAgentSpace)
	assert.Equal(t, testSandboxSpace, *page.Items[0].FromAgentSpace)

	assert.Equal(t, testSandboxSpace, page.Items[1].ToAgentSpace)
	assert.Equal(t, "isolating the workspace", page.Items[1].Reason)
}

// A session with a turn in flight is refused until that turn ends, because
// changing the agent space under running work would attribute its tool calls
// to an agent space that did not run them.
func TestRegistryReconfigureSessionRefusesABusySession(t *testing.T) {
	fixture := newReconfigureFixture(t)

	stored, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)

	lease, err := fixture.store.AcquireTurn(
		t.Context(),
		fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: stored.Workspace,
		},
	)
	require.NoError(t, err)

	_, err = fixture.registry.ReconfigureSession(
		t.Context(),
		fixture.sessionID,
		api.ReconfigureSessionRequest{
			AgentSpace: testSandboxSpace,
			Reason:     "isolating the workspace",
		},
	)
	require.ErrorIs(t, err, session.ErrSessionBusy)

	fixture.store.ReleaseTurn(lease)

	_, err = fixture.registry.ReconfigureSession(
		t.Context(),
		fixture.sessionID,
		api.ReconfigureSessionRequest{
			AgentSpace: testSandboxSpace,
			Reason:     "isolating the workspace",
		},
	)
	require.NoError(t, err)
}
