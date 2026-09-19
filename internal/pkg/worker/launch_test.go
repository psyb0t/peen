package worker_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSocketRoot = "/data/peen/workers"

	// testDeepRootSegment builds a root past the socket limit without
	// depending on any real directory.
	testDeepRootSegment = "verylongdirectorysegment"
	testDeepRootDepth   = 6
)

// A socket root is checked before any session exists, because every session
// socket is that root plus a fixed-length name.
func TestValidateSocketRoot(t *testing.T) {
	t.Parallel()

	deep := "/" + strings.TrimSuffix(
		strings.Repeat(testDeepRootSegment+"/", testDeepRootDepth),
		"/",
	)

	testCases := []struct {
		name    string
		root    string
		wantErr error
	}{
		{
			name: "a deployment root fits",
			root: testSocketRoot,
		},
		{
			name:    "a deep root is refused",
			root:    deep,
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := worker.ValidateSocketRoot(tc.root)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)

			// The refusal has to name the setting an operator changes,
			// otherwise it only reports that something is too long.
			assert.Contains(t, err.Error(), "PEEN_WORKER_SOCKET_DIR")
		})
	}
}

// The refusal is exact rather than approximate: a root one byte inside the
// limit is accepted and one byte over is not.
func TestValidateSocketRootIsExactAtTheLimit(t *testing.T) {
	t.Parallel()

	fitting := socketRootOfPathLength(t, 107)
	require.NoError(t, worker.ValidateSocketRoot(fitting))

	require.ErrorIs(
		t,
		worker.ValidateSocketRoot(fitting+"x"),
		commerr.ErrValidationFailed,
	)
}

// Each session's socket lives in its own directory beneath the root. A worker
// receives that directory, so the directory boundary is what stops one worker
// from seeing where another session's controller surface lives.
func TestSocketPathForIsSessionScoped(t *testing.T) {
	t.Parallel()

	first := uuid.New()
	second := uuid.New()

	firstPath := worker.SocketPathFor(testSocketRoot, first)
	secondPath := worker.SocketPathFor(testSocketRoot, second)

	assert.NotEqual(t, firstPath, secondPath)
	assert.Contains(t, firstPath, first.String())
	assert.NotContains(t, firstPath, second.String())

	firstDirectory := worker.SessionSocketDirectory(testSocketRoot, first)
	assert.Equal(t, firstDirectory, filepath.Dir(firstPath))

	// The session directory is one level below the root, and it contains this
	// session's socket and nothing that names another session.
	assert.Equal(t, testSocketRoot, filepath.Dir(firstDirectory))
	assert.NotEqual(t, testSocketRoot, firstDirectory)
	assert.False(
		t,
		strings.HasPrefix(secondPath, firstDirectory+string(filepath.Separator)),
		"one session's directory must not contain another session's socket",
	)
}

// A credential is compared in constant time and stored only as a digest, so a
// controller reading its own database back cannot recover one.
func TestCredentialMatching(t *testing.T) {
	t.Parallel()

	credential, err := worker.NewCredential()
	require.NoError(t, err)

	other, err := worker.NewCredential()
	require.NoError(t, err)

	hash := credential.Hash()

	assert.NotEqual(t, string(credential), hash)
	assert.True(t, credential.Matches(hash))
	assert.False(t, other.Matches(hash))
	assert.False(t, worker.Credential("").Matches(hash))
}

// socketRootOfPathLength builds a root whose longest session socket path is
// exactly want bytes.
func socketRootOfPathLength(t *testing.T, want int) string {
	t.Helper()

	root := "/"
	for len(worker.SocketPathFor(root, uuid.Nil)) < want {
		root += "x"
	}

	require.Len(t, worker.SocketPathFor(root, uuid.Nil), want)

	return root
}
