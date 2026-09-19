package docker_test

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	daemonTestContainerID       = "worker-container"
	daemonTestReadHeaderTimeout = time.Second
)

//nolint:tagliatelle // The Docker Engine API defines these names.
type daemonCreatePayload struct {
	HostConfig struct {
		SecurityOpt []string `json:"SecurityOpt"`
	} `json:"HostConfig"`
}

type daemonCreateRecord struct {
	payload daemonCreatePayload
	method  string
	err     error
}

// The local Unix listener is a Docker Engine protocol boundary, not a Docker
// daemon. It proves the public client writes the security option the request
// builder promised without a Docker socket or container.
func TestDaemonClientWritesNoNewPrivileges(t *testing.T) {
	t.Parallel()

	listenerConfig := net.ListenConfig{}
	listener, err := listenerConfig.Listen(
		t.Context(),
		"unix",
		filepath.Join(t.TempDir(), "daemon.sock"),
	)
	require.NoError(t, err)

	records := make(chan daemonCreateRecord, 1)
	server := &http.Server{
		ReadHeaderTimeout: daemonTestReadHeaderTimeout,
		Handler: http.HandlerFunc(func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			if request.Method != http.MethodPost {
				records <- daemonCreateRecord{method: request.Method}
				http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

				return
			}

			payload := daemonCreatePayload{}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				records <- daemonCreateRecord{err: err}
				http.Error(response, "bad request", http.StatusBadRequest)

				return
			}

			response.Header().Set("Content-Type", "application/json")
			if _, err := response.Write(
				[]byte(`{"Id":"` + daemonTestContainerID + `"}`),
			); err != nil {
				records <- daemonCreateRecord{err: err}

				return
			}

			records <- daemonCreateRecord{
				method:  request.Method,
				payload: payload,
			}
		}),
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(listener)
	}()

	t.Cleanup(func() {
		closeErr := server.Close()
		require.True(
			t,
			closeErr == nil || errors.Is(closeErr, http.ErrServerClosed),
		)
		require.ErrorIs(t, <-serverDone, http.ErrServerClosed)
	})

	client, err := docker.NewDaemonClient(listener.Addr().String())
	require.NoError(t, err)

	containerID, err := client.CreateContainer(t.Context(), docker.CreateRequest{
		Name:            "peen-worker-test",
		Image:           testImage,
		Command:         []string{"worker"},
		User:            "0:0",
		NoNewPrivileges: true,
	})
	require.NoError(t, err)
	assert.Equal(t, daemonTestContainerID, containerID)

	record := <-records
	require.NoError(t, record.err)
	assert.Equal(t, http.MethodPost, record.method)
	assert.Equal(
		t,
		[]string{"no-new-privileges:true"},
		record.payload.HostConfig.SecurityOpt,
	)
}
