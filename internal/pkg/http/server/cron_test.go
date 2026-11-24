package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cronReaderFixture struct {
	err   error
	calls int
}

func (f *cronReaderFixture) ListCronJobs(context.Context) (api.CronJobList, error) {
	f.calls++

	return api.CronJobList{Items: []api.CronJob{}}, f.err
}

func (f *cronReaderFixture) ListCronRuns(context.Context, api.ListCronRunsParams) (api.CronRunPage, error) {
	f.calls++

	return api.CronRunPage{Items: []api.CronRun{}, Limit: 50}, f.err
}

func TestCronRoutesAuthenticateAndValidateBeforeReads(t *testing.T) {
	t.Parallel()
	fake := &cronReaderFixture{}
	s, err := newTestServer(Dependencies{Runtime: newTestRuntime(uuid.New()), Cron: fake, APIToken: testAPIToken})
	require.NoError(t, err)
	for _, tc := range []struct {
		path   string
		auth   bool
		status int
	}{
		{path: "/api/v1/cron/jobs", status: http.StatusUnauthorized},
		{path: "/api/v1/cron/runs?limit=201", auth: true, status: http.StatusBadRequest},
		{path: "/api/v1/cron/jobs", auth: true, status: http.StatusOK},
		{path: "/api/v1/cron/runs", auth: true, status: http.StatusOK},
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
		r.Header.Set(headerAccept, "application/json")
		if tc.auth {
			r.Header.Set(headerAuthorization, bearerScheme+" "+testAPIToken)
		}
		w := httptest.NewRecorder()
		s.inProcessHandler.ServeHTTP(w, r)
		assert.Equal(t, tc.status, w.Code, w.Body.String())
	}
	assert.Equal(t, 2, fake.calls)
	fake.err = commerr.ErrValidationFailed
	r, err := s.ListCronRuns(t.Context(), api.ListCronRunsRequestObject{})
	require.NoError(t, err)
	_, ok := r.(api.ListCronRuns400JSONResponse)
	assert.True(t, ok)
	fake.err = commerr.ErrExecFailed
	_, err = s.ListCronJobs(t.Context(), api.ListCronJobsRequestObject{})
	require.ErrorIs(t, err, commerr.ErrExecFailed)
}
