//go:build integration

package api_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/stretchr/testify/assert"
)

func TestAPIReturnsDocumentedErrorEnvelopes(t *testing.T) {
	unknownSessionID := uuid.New()
	testCases := []struct {
		name       string
		method     string
		path       string
		headers    map[string]string
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "missing bearer token",
			method:     http.MethodGet,
			path:       messagesPath,
			headers:    map[string]string{headerSessionID: unknownSessionID.String()},
			wantStatus: http.StatusUnauthorized,
			wantCode:   aichteeteapee.ErrorCodeUnauthorized,
		},
		{
			name:   "wrong bearer token",
			method: http.MethodGet,
			path:   messagesPath,
			headers: withHeader(
				withHeader(authenticatedHeaders(), headerAuthorization, bearerPrefix+"wrong"),
				headerSessionID,
				unknownSessionID.String(),
			),
			wantStatus: http.StatusUnauthorized,
			wantCode:   aichteeteapee.ErrorCodeUnauthorized,
		},
		{
			name:       "unknown session",
			method:     http.MethodGet,
			path:       sessionPath,
			headers:    withHeader(authenticatedHeaders(), headerSessionID, unknownSessionID.String()),
			wantStatus: http.StatusNotFound,
			wantCode:   server.ErrorCodeSessionNotFound,
		},
		{
			name:       "invalid page limit",
			method:     http.MethodGet,
			path:       messagesPath + "?limit=201",
			headers:    withHeader(authenticatedHeaders(), headerSessionID, unknownSessionID.String()),
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "malformed session id",
			method:     http.MethodGet,
			path:       sessionPath,
			headers:    withHeader(authenticatedHeaders(), headerSessionID, "not-a-uuid"),
			wantStatus: http.StatusBadRequest,
			wantCode:   server.ErrorCodeInvalidSessionID,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			response := apiRequest(t, tc.method, tc.path, nil, tc.headers)

			assert.Equal(t, tc.wantStatus, response.StatusCode)
			assertErrorCode(t, response, tc.wantCode)
		})
	}
}
