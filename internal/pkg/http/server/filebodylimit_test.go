package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOnlyFileWritesReceiveTheLargerBodyLimit(t *testing.T) {
	t.Parallel()
	instance := &Server{deps: Dependencies{MaxFileAPIBytes: 2 << 20}}
	for _, test := range []struct {
		method string
		path   string
		ok     bool
	}{
		{http.MethodPut, workspaceFileContentPath, true},
		{http.MethodPost, apiBaseURL + "/session/notices", false},
		{http.MethodPut, apiBaseURL + "/session", false},
		{http.MethodPost, workspaceFileContentPath, false},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(t.Context(),
				test.method, test.path,
				bytes.NewReader(make([]byte, maximumRequestBodyBytes+1)))
			instance.limitBody(http.HandlerFunc(func(
				_ http.ResponseWriter, request *http.Request,
			) {
				_, err := io.Copy(io.Discard, request.Body)
				assert.Equal(t, test.ok, err == nil)
			})).ServeHTTP(httptest.NewRecorder(), request)
		})
	}
}
