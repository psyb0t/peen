package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIToken = "test-api-token"

func TestServerServeListenerRejectsNilListener(t *testing.T) {
	instance, err := New(Dependencies{Runtime: newTestRuntime(uuid.New())})
	require.NoError(t, err)

	err = instance.ServeListener(context.Background(), nil)

	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

func TestServerRequestMiddleware(t *testing.T) {
	sessionID := uuid.New()
	providedRequestID := uuid.New()

	testCases := []struct {
		name          string
		apiToken      string
		body          string
		headers       map[string]string
		wantStatus    int
		wantSendCalls int
		check         func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:     "authorized JSON request preserves request ID",
			apiToken: testAPIToken,
			body:     `{"message":"hello"}`,
			headers: map[string]string{
				headerAccept:        mediaTypeJSON,
				headerAuthorization: bearerScheme + " " + testAPIToken,
				headerRequestID:     providedRequestID.String(),
			},
			wantStatus:    http.StatusOK,
			wantSendCalls: 1,
			check: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				t.Helper()
				assert.Equal(
					t,
					providedRequestID.String(),
					recorder.Header().Get(headerRequestID),
				)
				assert.Equal(
					t,
					sessionID.String(),
					recorder.Header().Get(headerSessionID),
				)
				response := api.MessageResponse{}
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
				assert.Equal(t, "response", response.Message)
			},
		},
		{
			name: "optional authentication leaves route open",
			body: `{"message":"hello"}`,
			headers: map[string]string{
				headerAccept: mediaTypeJSON,
			},
			wantStatus:    http.StatusOK,
			wantSendCalls: 1,
			check:         assertGeneratedRequestID,
		},
		{
			name:     "rejects malformed bearer authentication",
			apiToken: testAPIToken,
			body:     `{"message":"hello"}`,
			headers: map[string]string{
				headerAuthorization: "Basic " + testAPIToken,
			},
			wantStatus:    http.StatusUnauthorized,
			wantSendCalls: 0,
			check:         assertUnauthorizedEnvelope,
		},
		{
			name:     "rejects unknown nested JSON field",
			apiToken: testAPIToken,
			body: `{
				"message":"hello",
				"systemPrompt":{"mode":"append","content":"rules","unknown":true}
			}`,
			headers: map[string]string{
				headerAuthorization: bearerScheme + " " + testAPIToken,
			},
			wantStatus:    http.StatusBadRequest,
			wantSendCalls: 0,
			check:         assertValidationEnvelope,
		},
		{
			name:     "rejects multiple JSON values",
			apiToken: testAPIToken,
			body:     `{"message":"hello"} {"message":"again"}`,
			headers: map[string]string{
				headerAuthorization: bearerScheme + " " + testAPIToken,
			},
			wantStatus:    http.StatusBadRequest,
			wantSendCalls: 0,
			check:         assertValidationEnvelope,
		},
		{
			name:     "rejects unsupported response representation",
			apiToken: testAPIToken,
			body:     `{"message":"hello"}`,
			headers: map[string]string{
				headerAccept:        "text/plain",
				headerAuthorization: bearerScheme + " " + testAPIToken,
			},
			wantStatus:    http.StatusNotAcceptable,
			wantSendCalls: 0,
			check:         assertBadRequestEnvelope,
		},
		{
			name:     "replaces malformed request ID",
			apiToken: testAPIToken,
			body:     `{"message":"hello"}`,
			headers: map[string]string{
				headerAuthorization: bearerScheme + " " + testAPIToken,
				headerRequestID:     "not-a-uuid",
			},
			wantStatus:    http.StatusOK,
			wantSendCalls: 1,
			check:         assertGeneratedRequestID,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := newTestRuntime(sessionID)
			instance, err := New(Dependencies{
				Runtime:  runtime,
				APIToken: tc.apiToken,
			})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				apiBaseURL+"/messages",
				strings.NewReader(tc.body),
			)
			request.Header.Set(headerContentType, mediaTypeJSON)
			for header, value := range tc.headers {
				request.Header.Set(header, value)
			}
			recorder := httptest.NewRecorder()

			instance.echo.ServeHTTP(recorder, request)

			assert.Equal(t, tc.wantStatus, recorder.Code)
			assert.Equal(t, tc.wantSendCalls, runtime.sendCalls)
			tc.check(t, recorder)
		})
	}
}

func assertGeneratedRequestID(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	requestID, err := uuid.Parse(recorder.Header().Get(headerRequestID))
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, requestID)
}

func assertUnauthorizedEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	response := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, aichteeteapee.ErrorCodeUnauthorized, response.Code)
}

func assertValidationEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	response := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, aichteeteapee.ErrorCodeValidationFailed, response.Code)
}

func assertBadRequestEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	response := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, aichteeteapee.ErrorCodeBadRequest, response.Code)
}

func TestServerHandlersMapKnownOperationErrors(t *testing.T) {
	sessionID := uuid.New()
	testCases := []struct {
		name       string
		runtime    *testRuntime
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
		write      func(*Server, *httptest.ResponseRecorder) error
	}{
		{
			name: "send maps missing session",
			runtime: &testRuntime{
				sessionID: sessionID,
				sendErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
			write: func(instance *Server, recorder *httptest.ResponseRecorder) error {
				response, err := instance.SendMessage(
					context.Background(),
					api.SendMessageRequestObject{
						Body: &api.MessageRequest{Message: "inspect"},
					},
				)
				if err != nil {
					return err
				}

				return response.VisitSendMessageResponse(recorder)
			},
		},
		{
			name: "send maps invalid body",
			runtime: &testRuntime{
				sessionID: sessionID,
				sendErr:   commerr.ErrValidationFailed,
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
			write: func(instance *Server, recorder *httptest.ResponseRecorder) error {
				response, err := instance.SendMessage(
					context.Background(),
					api.SendMessageRequestObject{
						Body: &api.MessageRequest{Message: "inspect"},
					},
				)
				if err != nil {
					return err
				}

				return response.VisitSendMessageResponse(recorder)
			},
		},
		{
			name: "send maps busy session",
			runtime: &testRuntime{
				sessionID: sessionID,
				sendErr:   commerr.ErrConflict,
			},
			wantStatus: http.StatusConflict,
			wantCode:   ErrorCodeSessionBusy,
			write: func(instance *Server, recorder *httptest.ResponseRecorder) error {
				response, err := instance.SendMessage(
					context.Background(),
					api.SendMessageRequestObject{
						Body: &api.MessageRequest{Message: "inspect"},
					},
				)
				if err != nil {
					return err
				}

				return response.VisitSendMessageResponse(recorder)
			},
		},
		{
			name: "send maps cancelled turn",
			runtime: &testRuntime{
				sessionID: sessionID,
				sendErr:   commerr.ErrCancelled,
			},
			wantStatus: http.StatusConflict,
			wantCode:   ErrorCodeTurnCancelled,
			write: func(instance *Server, recorder *httptest.ResponseRecorder) error {
				response, err := instance.SendMessage(
					context.Background(),
					api.SendMessageRequestObject{
						Body: &api.MessageRequest{Message: "inspect"},
					},
				)
				if err != nil {
					return err
				}

				return response.VisitSendMessageResponse(recorder)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{Runtime: tc.runtime})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()

			require.NoError(t, tc.write(instance, recorder))
			assert.Equal(t, tc.wantStatus, recorder.Code)
			assertErrorCode(t, recorder, tc.wantCode)
		})
	}
}

// Before SSE headers are committed, StreamMessage failures must still use
// the normal JSON error envelope rather than leaving a half-open stream.
func TestServerStreamMessagePreHeaderFailureUsesJSONEnvelope(t *testing.T) {
	sessionID := uuid.New()

	testCases := []struct {
		name       string
		streamErr  error
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "missing session maps to not found",
			streamErr:  commerr.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
		},
		{
			name:       "invalid body maps to validation failed",
			streamErr:  commerr.ErrValidationFailed,
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := newTestRuntime(sessionID)
			runtime.streamErr = tc.streamErr

			instance, err := New(Dependencies{Runtime: runtime})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				apiBaseURL+"/messages",
				strings.NewReader(`{"message":"hello"}`),
			)
			request.Header.Set(headerContentType, mediaTypeJSON)
			request.Header.Set(headerAccept, mediaTypeSSE)

			recorder := httptest.NewRecorder()
			instance.echo.ServeHTTP(recorder, request)

			assert.Equal(t, tc.wantStatus, recorder.Code)
			assert.NotContains(
				t,
				recorder.Header().Get(headerContentType),
				mediaTypeSSE,
			)
			assertErrorCode(t, recorder, tc.wantCode)
		})
	}
}

func TestServerSessionEndpoints(t *testing.T) {
	sessionID := uuid.New()
	testCases := []struct {
		name       string
		method     string
		path       string
		runtime    *testRuntime
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "lists a page",
			method:     http.MethodGet,
			path:       apiBaseURL + "/messages?limit=1&offset=0&order=asc",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:   "lists missing session",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages",
			runtime: &testRuntime{
				sessionID: sessionID,
				listErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
		},
		{
			name:   "rejects an invalid page",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages?limit=201",
			runtime: &testRuntime{
				sessionID: sessionID,
				listErr:   commerr.ErrValidationFailed,
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "gets session details",
			method:     http.MethodGet,
			path:       apiBaseURL + "/session",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:   "gets missing session",
			method: http.MethodGet,
			path:   apiBaseURL + "/session",
			runtime: &testRuntime{
				sessionID:  sessionID,
				sessionErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
		},
		{
			name:       "requests cancellation",
			method:     http.MethodPost,
			path:       apiBaseURL + "/session/cancel",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusAccepted,
		},
		{
			name:   "cancels missing session",
			method: http.MethodPost,
			path:   apiBaseURL + "/session/cancel",
			runtime: &testRuntime{
				sessionID: sessionID,
				cancelErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{Runtime: tc.runtime})
			require.NoError(t, err)
			request := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			request.Header.Set(headerSessionID, sessionID.String())
			recorder := httptest.NewRecorder()

			instance.echo.ServeHTTP(recorder, request)

			assert.Equal(t, tc.wantStatus, recorder.Code)
			if tc.wantCode != "" {
				assertErrorCode(t, recorder, tc.wantCode)
			}
		})
	}
}

// A malformed X-Session-ID header fails oapi-codegen's own UUID parsing
// before the request reaches a handler, distinct from a body shape
// rejection, so it carries its own documented code.
func TestServerRejectsMalformedSessionIDHeader(t *testing.T) {
	instance, err := New(Dependencies{
		Runtime: newTestRuntime(uuid.New()),
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/session",
		nil,
	)
	request.Header.Set(headerSessionID, "not-a-uuid")

	recorder := httptest.NewRecorder()
	instance.echo.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assertErrorCode(t, recorder, ErrorCodeInvalidSessionID)
}

func assertErrorCode(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	want aichteeteapee.ErrorCode,
) {
	t.Helper()

	response := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, want, response.Code)
}

type testRuntime struct {
	sessionID  uuid.UUID
	sendCalls  int
	sendErr    error
	streamErr  error
	sessionErr error
	listErr    error
	cancelErr  error
	eventsErr  error
	jobsErr    error

	agentRunsErr error
}

func newTestRuntime(sessionID uuid.UUID) *testRuntime {
	return &testRuntime{sessionID: sessionID}
}

func (r *testRuntime) SendMessage(
	_ context.Context,
	_ api.MessageRequest,
	_ *uuid.UUID,
	_ uuid.UUID,
) (*agent.MessageRunResult, error) {
	r.sendCalls++
	if r.sendErr != nil {
		return nil, r.sendErr
	}

	return &agent.MessageRunResult{
		Response:  api.MessageResponse{Message: "response"},
		SessionID: r.sessionID,
	}, nil
}

func (r *testRuntime) StreamMessage(
	_ context.Context,
	_ api.MessageRequest,
	_ *uuid.UUID,
	_ uuid.UUID,
) (*agent.StreamMessageResult, error) {
	if r.streamErr != nil {
		return nil, r.streamErr
	}

	return &agent.StreamMessageResult{
		Body:      io.NopCloser(strings.NewReader("")),
		SessionID: r.sessionID,
	}, nil
}

func (r *testRuntime) Session(
	_ context.Context,
	_ uuid.UUID,
) (*api.Session, error) {
	if r.sessionErr != nil {
		return nil, r.sessionErr
	}

	return &api.Session{Id: r.sessionID}, nil
}

func (r *testRuntime) ListMessages(
	_ context.Context,
	_ api.ListMessagesParams,
) (*api.MessagePage, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.MessagePage{}, nil
}

func (r *testRuntime) CancelSession(
	_ context.Context,
	_ uuid.UUID,
) (*api.CancelResponse, error) {
	if r.cancelErr != nil {
		return nil, r.cancelErr
	}

	return &api.CancelResponse{}, nil
}

func (r *testRuntime) ListSessionEvents(
	_ context.Context,
	_ uuid.UUID,
) (*api.SessionEventPage, error) {
	if r.eventsErr != nil {
		return nil, r.eventsErr
	}

	return &api.SessionEventPage{Events: []api.SessionEvent{}}, nil
}

func (r *testRuntime) PublishSessionEvent(
	_ context.Context,
	_ uuid.UUID,
	request api.SessionEventRequest,
) (*api.SessionEvent, error) {
	if r.eventsErr != nil {
		return nil, r.eventsErr
	}

	return &api.SessionEvent{
		Id:       uuid.New(),
		Type:     request.Type,
		Source:   "api",
		Summary:  request.Summary,
		Delivery: api.SessionEventDeliveryQueue,
	}, nil
}

func (r *testRuntime) ListSessionJobs(
	_ context.Context,
	_ uuid.UUID,
	_ api.ListSessionJobsParams,
) (*api.JobPage, error) {
	if r.jobsErr != nil {
		return nil, r.jobsErr
	}

	return &api.JobPage{Jobs: []api.Job{}}, nil
}

func (r *testRuntime) ReadSessionJobOutput(
	_ context.Context,
	_ uuid.UUID,
	jobID uuid.UUID,
	_ api.ReadSessionJobOutputParams,
) (*api.JobOutput, error) {
	if r.jobsErr != nil {
		return nil, r.jobsErr
	}

	return &api.JobOutput{
		JobId: jobID,
		State: api.JobOutputStateRunning,
	}, nil
}

func (r *testRuntime) SignalSessionJob(
	_ context.Context,
	_ uuid.UUID,
	jobID uuid.UUID,
	_ api.JobSignalRequest,
) (*api.JobSignalResponse, error) {
	if r.jobsErr != nil {
		return nil, r.jobsErr
	}

	return &api.JobSignalResponse{
		JobId:     jobID,
		Signalled: true,
		State:     api.JobSignalResponseStateRunning,
	}, nil
}

func (r *testRuntime) ListSessionAgentRuns(
	_ context.Context,
	_ uuid.UUID,
	_ api.ListSessionAgentRunsParams,
) (*api.AgentRunPage, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunPage{Agents: []api.AgentRun{}}, nil
}

func (r *testRuntime) ListSessionAgentRunEvents(
	_ context.Context,
	_ uuid.UUID,
	agentRunID uuid.UUID,
	_ api.ListSessionAgentRunEventsParams,
) (*api.AgentRunEventPage, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunEventPage{
		AgentRunId: agentRunID,
		State:      api.AgentRunEventPageStateRunning,
		Events:     []api.AgentRunEvent{},
	}, nil
}

func (r *testRuntime) CancelSessionAgentRun(
	_ context.Context,
	_ uuid.UUID,
	agentRunID uuid.UUID,
) (*api.AgentRunCancelResponse, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunCancelResponse{
		AgentRunId:      agentRunID,
		CancelRequested: true,
		State:           api.AgentRunCancelResponseStateRunning,
	}, nil
}

var _ agent.API = (*testRuntime)(nil)
