package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunningCompactionReadsEveryPageAndIgnoresEndedCalls(t *testing.T) {
	t.Parallel()

	sessionID, runID, callID := uuid.New(), uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request,
	) {
		assert.Equal(t, sessionID.String(), r.Header.Get(headerSessionID))
		switch r.URL.Path {
		case "/api/v1/session/model-runs":
			assert.Equal(t, "compaction", r.URL.Query().Get("stage"))
			assert.Equal(t, "running", r.URL.Query().Get("state"))
			if r.URL.Query().Get("offset") == "0" {
				_, err := fmt.Fprintf(w,
					`{"modelRuns":[{"id":%q,"agentRunId":%q}],"hasMore":true}`,
					uuid.New(), uuid.New())
				assert.NoError(t, err)

				return
			}

			assert.Equal(t, "1", r.URL.Query().Get("offset"))
			_, err := fmt.Fprintf(w, `{"modelRuns":[{"id":%q}]}`, runID)
			assert.NoError(t, err)
		default:
			assert.Equal(t, fmt.Sprintf("/api/v1/session/model-runs/%s/calls",
				runID), r.URL.Path)
			if r.URL.Query().Get("offset") == "0" {
				_, err := fmt.Fprintf(w,
					`{"calls":[{"id":%q,"state":"completed"}],"hasMore":true}`,
					uuid.New())
				assert.NoError(t, err)

				return
			}

			assert.Equal(t, "1", r.URL.Query().Get("offset"))
			_, err := fmt.Fprintf(w, `{"calls":[{"id":%q,"state":"running"}]}`,
				callID)
			assert.NoError(t, err)
		}
	}))
	t.Cleanup(server.Close)
	r := &runner{baseURL: server.URL, httpClient: server.Client()}

	actual, err := r.runningCompaction(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, callID, actual)
}

func TestRunningCompactionRefusesObserverFailures(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusUnauthorized, http.StatusInternalServerError,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
				_ *http.Request,
			) {
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)
			r := &runner{baseURL: server.URL, httpClient: server.Client()}

			id, err := r.runningCompaction(t.Context(), uuid.New())
			require.ErrorIs(t, err, commerr.ErrFetchFailed)
			assert.Equal(t, uuid.Nil, id)
		})
	}
}

func TestRunningCompactionWithoutAModelRequestDoesNotTrigger(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		_ *http.Request,
	) {
		_, err := w.Write([]byte(`{"modelRuns":[]}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	r := &runner{baseURL: server.URL, httpClient: server.Client()}

	id, err := r.runningCompaction(t.Context(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, id)
}

func TestWatchCompactionStopsWithTheTurn(t *testing.T) {
	t.Parallel()

	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		_ *http.Request,
	) {
		_, err := w.Write([]byte(`{"modelRuns":[]}`))
		assert.NoError(t, err)
		select {
		case requested <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(server.Close)
	state := &sendState{
		runner: &runner{
			baseURL: server.URL, httpClient: server.Client(),
			sessions: map[string]uuid.UUID{defaultSessionName: uuid.New()},
			record:   &recorder{encoder: json.NewEncoder(io.Discard)},
		},
		current: step{Session: defaultSessionName, Kill: &kill{OnCompaction: true}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	state.killDone.Add(1)
	go state.watchCompaction(t.Context(), ctx)

	require.Eventually(t, func() bool {
		return len(requested) > 0
	}, readyTimeout, readyPollInterval)
	cancel()
	state.killDone.Wait()
	assert.False(t, state.isKilled)
}

func TestValidateCompactionKill(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		kill kill
	}{
		{name: "controller", kill: kill{Target: killTargetController}},
		{name: "stop", kill: kill{Target: killTargetStop}},
		{name: "file", kill: kill{Target: killTargetWorker, OnFile: "/tmp/x"}},
		{name: "tool count", kill: kill{Target: killTargetWorker, AfterToolUses: 1}},
		{name: "tool", kill: kill{Target: killTargetWorker, OnTool: "readFile"}},
		{name: "negative delay", kill: kill{Target: killTargetWorker, DelayMilliseconds: -1}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spec := tc.kill
			spec.OnCompaction = true
			require.ErrorIs(t, validateSend(&step{Message: "hello", Kill: &spec}),
				commerr.ErrValidationFailed)
		})
	}

	require.NoError(t, validateSend(&step{Message: "hello", Kill: &kill{
		Target: killTargetWorker, OnCompaction: true,
	}}))
}
