package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testWorkspaceFileService struct {
	err   error
	entry control.WorkspaceFileEntry
}

func (s testWorkspaceFileService) ListWorkspaceFiles(
	context.Context,
	uuid.UUID,
	string,
) (control.WorkspaceFileListing, error) {
	return control.WorkspaceFileListing{}, s.err
}

func (s testWorkspaceFileService) ReadWorkspaceFile(
	context.Context,
	uuid.UUID,
	string,
) (control.WorkspaceFileContent, error) {
	return control.WorkspaceFileContent{}, s.err
}

func (s testWorkspaceFileService) WriteWorkspaceFile(
	context.Context,
	uuid.UUID,
	string,
	string,
	bool,
) (control.WorkspaceFileEntry, error) {
	return s.entry, s.err
}

func (s testWorkspaceFileService) DeleteWorkspaceFile(
	context.Context,
	uuid.UUID,
	string,
) error {
	return s.err
}

func TestServerWorkspaceFileWriteUsesSafeValidationMessage(t *testing.T) {
	service := testWorkspaceFileService{
		err: ctxerrors.Wrap(commerr.ErrValidationFailed,
			"host path /private/worker/workspace leaked by backend"),
	}
	instance, err := newTestServer(Dependencies{
		Runtime:        newTestRuntime(uuid.New()),
		WorkspaceFiles: service,
	})
	require.NoError(t, err)

	response, err := instance.WriteWorkspaceFile(t.Context(),
		api.WriteWorkspaceFileRequestObject{
			Params: api.WriteWorkspaceFileParams{XSessionID: uuid.New()},
			Body: &api.WorkspaceFileWrite{
				Path:          "file.txt",
				ContentBase64: "eA==",
			},
		})
	require.NoError(t, err)
	badRequest, ok := response.(api.WriteWorkspaceFile400JSONResponse)
	require.True(t, ok)
	recorder := httptest.NewRecorder()
	require.NoError(t, badRequest.VisitWriteWorkspaceFileResponse(recorder))

	var body api.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, "workspace file request is invalid", body.Message)
	assert.NotContains(t, recorder.Body.String(), "/private/worker/workspace")
}

func TestServerWorkspaceFileWriteReturnsGeneratedMetadata(t *testing.T) {
	sessionID := uuid.New()
	modifiedAt := time.Now().UTC()
	service := testWorkspaceFileService{
		entry: api.WorkspaceFileEntry{
			Name:       "file.txt",
			Type:       api.WorkspaceFileTypeFile,
			Size:       1,
			ModifiedAt: modifiedAt,
		},
	}
	instance, err := newTestServer(Dependencies{
		Runtime:        newTestRuntime(sessionID),
		WorkspaceFiles: service,
	})
	require.NoError(t, err)

	response, err := instance.WriteWorkspaceFile(t.Context(),
		api.WriteWorkspaceFileRequestObject{
			Params: api.WriteWorkspaceFileParams{XSessionID: sessionID},
			Body: &api.WorkspaceFileWrite{
				Path:          "file.txt",
				ContentBase64: "eA==",
			},
		})
	require.NoError(t, err)
	created, ok := response.(api.WriteWorkspaceFile200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, "file.txt", created.Body.Name)
	assert.Equal(t, api.WorkspaceFileTypeFile, created.Body.Type)
	assert.Equal(t, sessionID, created.Headers.XSessionID)
}

func TestServerWorkspaceFileDeleteMapsWorkspaceRefusal(t *testing.T) {
	service := testWorkspaceFileService{
		err: ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"path resolves outside the workspace"),
	}
	instance, err := newTestServer(Dependencies{
		Runtime:        newTestRuntime(uuid.New()),
		WorkspaceFiles: service,
	})
	require.NoError(t, err)

	response, err := instance.DeleteWorkspaceFile(t.Context(),
		api.DeleteWorkspaceFileRequestObject{
			Params: api.DeleteWorkspaceFileParams{
				XSessionID: uuid.New(),
				Path:       "link",
			},
		})
	require.NoError(t, err)
	forbidden, ok := response.(api.DeleteWorkspaceFile403JSONResponse)
	require.True(t, ok)
	recorder := httptest.NewRecorder()
	require.NoError(t, forbidden.VisitDeleteWorkspaceFileResponse(recorder))

	var body api.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, ErrorCodeWorkspaceNotAllowed, body.Code)
	assert.NotContains(t, recorder.Body.String(), "outside the workspace")
}

func TestWorkspaceFileRoutesMapErrorsWithoutInternalDetails(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"forbidden", commerr.ErrPermissionDenied, http.StatusForbidden},
		{"missing", commerr.ErrNotFound, http.StatusNotFound},
		{"invalid", commerr.ErrValidationFailed, http.StatusBadRequest},
		{"conflict", commerr.ErrInvalidState, http.StatusConflict},
		{"unexpected", ctxerrors.New("unexpected failure"), http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sessionID := uuid.New()
			instance, err := newTestServer(Dependencies{
				Runtime: newTestRuntime(sessionID),
				WorkspaceFiles: testWorkspaceFileService{
					err: ctxerrors.Wrap(test.err, "internal path /private/worker"),
				},
			})
			require.NoError(t, err)
			for _, operation := range []struct {
				method string
				path   string
				body   string
			}{
				{http.MethodGet, "/session/files", ""},
				{http.MethodGet, "/session/files/content?path=file", ""},
				{http.MethodPut, "/session/files/content", `{"path":"file","contentBase64":"eA=="}`},
				{http.MethodDelete, "/session/files?path=file", ""},
			} {
				request := httptest.NewRequestWithContext(t.Context(),
					operation.method, apiBaseURL+operation.path,
					strings.NewReader(operation.body))
				request.Header.Set(headerSessionID, sessionID.String())
				request.Header.Set(headerContentType, mediaTypeJSON)
				recorder := httptest.NewRecorder()
				instance.inProcessHandler.ServeHTTP(recorder, request)
				assert.Equal(t, test.status, recorder.Code, operation.method)
				assert.NotContains(t, recorder.Body.String(), "/private/worker")
			}
		})
	}
}
