package docker_test

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
)

const (
	testWorkspace  = "/srv/work/project"
	testConfigDir  = "/etc/peen"
	testSocketRoot = "/run/peen/workers"
	testImage      = "psyb0t/peen@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
	// testCredential is fixed test data, never a real secret. These tests
	// assert it is absent from everything the daemon receives.
	testCredential   = "the-one-time-credential" //nolint:gosec // Test fixture.
	testUID          = 1000
	testGID          = 1000
	testDockerGID    = 999
	testContainerID  = "container-abc"
	dockerSocketPath = "/var/run/docker.sock"
	testProfileName  = "sandbox"

	// testLocalImageID is the shape a container inspect reports. It is a local
	// content ID, not a repository digest, and the launcher must never record
	// it as one.
	testLocalImageID = "sha256:" +
		"2222222222222222222222222222222222222222222222222222222222222222"
)

func testIdentity() docker.HostIdentity {
	return docker.HostIdentity{
		Username:  "peen",
		GroupName: "peen",
		UID:       testUID,
		GID:       testGID,
		Home:      "/home/peen",
	}
}

func testDocument(sessionID uuid.UUID) worker.LaunchDocument {
	return worker.LaunchDocument{
		SessionID:       sessionID,
		GenerationID:    uuid.New(),
		Credential:      worker.Credential(testCredential),
		SocketPath:      worker.SocketPathFor(testSocketRoot, sessionID),
		Workspace:       testWorkspace,
		ConfigDirectory: testConfigDir,
		AgentSpace:      worker.DockerSpaceName(testProfileName),
	}
}

// sandboxSpace is a Docker agent space with every grant at its safe value.
func sandboxSpace() worker.AgentSpace {
	return worker.AgentSpace{
		Name:     worker.DockerSpaceName(testProfileName),
		Kind:     worker.KindDocker,
		Revision: 1,
		Image:    testImage,
		Docker:   worker.DockerAccessNone,
		Network:  worker.NetworkModeBridge,
	}
}

// grantedSpace is the sandbox with the given grants changed.
func grantedSpace(change func(*worker.AgentSpace)) worker.AgentSpace {
	space := sandboxSpace()
	change(&space)

	return space
}
