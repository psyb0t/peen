package run

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSocketPath      = "/run/peen/workers/session.sock"
	testWorkspacePath   = "/srv/work/project"
	testConfigDirectory = "/data/peen"
	testProfileName     = "native"

	// testReadyGracePeriod is how long a waiting command is observed before
	// the test concludes it really is waiting.
	testReadyGracePeriod = 50 * time.Millisecond
	testWaitTimeout      = 5 * time.Second
)

func TestReadLaunchDocumentAcceptsAControllerIssuedDocument(t *testing.T) {
	t.Parallel()

	want := validLaunchDocument(t)

	encoded, err := want.Encode()
	require.NoError(t, err)

	got, err := readLaunchDocument(bytes.NewReader(encoded))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// `peen worker` is not a general user command. Without a complete
// controller-issued launch it has no session, no socket, and nothing to do, so
// every incomplete input is refused before the process reaches a controller.
func TestReadLaunchDocumentRefusesAnythingButACompleteLaunch(t *testing.T) {
	t.Parallel()

	oversized, err := json.Marshal(map[string]string{
		"workspace": strings.Repeat("x", maxLaunchDocumentBytes),
	})
	require.NoError(t, err)

	incomplete := validLaunchDocument(t)
	incomplete.Credential = ""

	incompleteJSON, err := incomplete.Encode()
	require.NoError(t, err)

	testCases := []struct {
		name    string
		input   []byte
		wantErr error
	}{
		{
			name:    "no document at all",
			input:   nil,
			wantErr: commerr.ErrRequiredFieldNotSet,
		},
		{
			name:    "larger than the bound",
			input:   oversized,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:  "not JSON",
			input: []byte("not-a-launch-document"),
		},
		{
			name:    "missing the credential",
			input:   incompleteJSON,
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := readLaunchDocument(bytes.NewReader(tc.input))
			require.Error(t, err)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

// Run refuses before it dials anything, so a worker started without a launch
// never reaches a controller socket.
func TestRunRefusesWithoutALaunchDocument(t *testing.T) {
	t.Parallel()

	err := Run(t.Context(), Options{
		LaunchInput: bytes.NewReader(nil),
		ParseConfig: func() (peenconfig.Config, error) {
			t.Fatal("configuration was loaded for a worker with no launch")

			return peenconfig.Config{}, nil
		},
	})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

// Durable traffic goes worker to controller, never the other way. A controller
// that tried to call a worker would be asking the worker to own state it does
// not have.
func TestHandleCallIsRefused(t *testing.T) {
	t.Parallel()

	handler := newCommandHandler()

	result, err := handler.HandleCall(
		t.Context(),
		"list_messages",
		json.RawMessage(`{}`),
	)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Nil(t, result)
}

// A worker registers before it can build its runtime, because building it
// reads durable state back over the same connection. A command that lands in
// that gap waits for the runtime instead of failing the client's first
// message.
func TestHandleCommandWaitsForTheRuntime(t *testing.T) {
	t.Parallel()

	handler := newCommandHandler()
	started := make(chan struct{})
	finished := make(chan error, 1)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		close(started)

		_, err := handler.HandleCommand(
			ctx,
			protocol.CommandRunTurn,
			json.RawMessage(`{"message":"hello"}`),
		)
		finished <- err
	}()

	<-started

	select {
	case <-finished:
		t.Fatal("the command was answered before the runtime existed")
	case <-time.After(testReadyGracePeriod):
	}

	cancel()

	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(testWaitTimeout):
		t.Fatal("the waiting command did not end with its context")
	}
}

// Shutdown is the one command a worker answers before it has a runtime: a
// controller that gave up on a launch still needs the process to end.
func TestShutdownIsAnsweredWithoutARuntime(t *testing.T) {
	t.Parallel()

	handler := newCommandHandler()

	result, err := handler.HandleCommand(
		t.Context(),
		protocol.CommandShutdown,
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, protocol.Ack{}, result)

	select {
	case <-handler.stopped:
	case <-time.After(testWaitTimeout):
		t.Fatal("the worker did not stop")
	}

	// Stopping twice is what a controller that retries a shutdown does.
	handler.stop()
}

func validLaunchDocument(t *testing.T) worker.LaunchDocument {
	t.Helper()

	credential, err := worker.NewCredential()
	require.NoError(t, err)

	return worker.LaunchDocument{
		SessionID:       uuid.New(),
		GenerationID:    uuid.New(),
		Credential:      credential,
		SocketPath:      testSocketPath,
		Workspace:       testWorkspacePath,
		ConfigDirectory: testConfigDirectory,
		Profile:         testProfileName,
	}
}
