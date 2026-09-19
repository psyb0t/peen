package docker_test

import (
	"os"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurrentHostIdentityWithOverrideUsesTheProcessIDs(t *testing.T) {
	identity, err := docker.CurrentHostIdentityWithOverride(
		"developer",
		"/home/developer",
	)
	require.NoError(t, err)

	assert.Equal(t, "developer", identity.Username)
	assert.Equal(t, "/home/developer", identity.Home)
	assert.Equal(t, os.Geteuid(), identity.UID)
	assert.Equal(t, os.Getegid(), identity.GID)
}

func TestCurrentHostIdentityWithOverrideRequiresTheCompletePair(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		username string
		home     string
	}{
		{name: "missing username", home: "/home/developer"},
		{name: "missing home", username: "developer"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := docker.CurrentHostIdentityWithOverride(
				testCase.username,
				testCase.home,
			)

			require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
		})
	}
}
