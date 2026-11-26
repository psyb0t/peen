//go:build integration

package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPICronRunsThroughARegularWorkerAndListsHistory(t *testing.T) {
	disabled := apiRequest(t, http.MethodGet, "/api/v1/cron/jobs", nil, authenticatedHeaders())
	requireAPIStatus(t, disabled, http.StatusOK)
	assert.Empty(t, decodeResponse[api.CronJobList](t, disabled).Items)
	fixture := `jobs:
  - name: recurringReport
    schedule: "* * * * * *"
    timeZone: UTC
    workspace: /workspace
    message: "Explain briefly"
    timeout: 15s
`
	variant := startAPIVariant(t, testinfra.VariantOptions{
		Environment: map[string]string{"PEEN_CRON": "true"},
		CronConfiguration: strings.ReplaceAll(fixture, "/workspace",
			testinfra.ContainerWorkingDirectory),
	})
	jobsResponse := variantRequest(t, variant, http.MethodGet, "/api/v1/cron/jobs", nil)
	requireAPIStatus(t, jobsResponse, http.StatusOK)
	jobs := decodeResponse[api.CronJobList](t, jobsResponse)
	require.Len(t, jobs.Items, 1)
	assert.Equal(t, "recurringReport", jobs.Items[0].Name)
	var completed api.CronRun
	require.Eventually(t, func() bool {
		response := variantRequest(t, variant, http.MethodGet,
			"/api/v1/cron/runs?job=recurringReport&limit=2", nil)
		requireAPIStatus(t, response, http.StatusOK)
		page := decodeResponse[api.CronRunPage](t, response)
		for _, run := range page.Items {
			if run.State == api.CronRunStateCompleted {
				completed = run

				return true
			}
		}

		return false
	}, 30*time.Second, 100*time.Millisecond)
	require.NotNil(t, completed.SessionId)
	assert.NotEqual(t, uuid.Nil, *completed.SessionId)
	assert.Equal(t, openVariantSession(t, variant), *completed.SessionId)
	assert.NotNil(t, completed.StartedAt)
	assert.NotNil(t, completed.FinishedAt)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		variant.APIURL("/api/v1/session"), nil)
	require.NoError(t, err)
	for name, value := range authenticatedHeaders() {
		request.Header.Set(name, value)
	}
	request.Header.Set("X-Session-ID", completed.SessionId.String())
	response, err := variant.HTTPClient().Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	requireAPIStatus(t, readAPIResponse(t, response), http.StatusOK)
	invalid := variantRequest(t, variant, http.MethodGet, "/api/v1/cron/runs?job=../escape", nil)
	requireAPIStatus(t, invalid, http.StatusBadRequest)
}
