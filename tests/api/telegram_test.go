//go:build integration

package api_test

import (
	"net/http"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
)

func TestAPITelegramOffIgnoresUnusedBotConfiguration(t *testing.T) {
	variant := startAPIVariant(t, testinfra.VariantOptions{
		Environment: map[string]string{
			"PEEN_TELEGRAM":           "false",
			"PEEN_TELEGRAM_TOKEN":     "EXAMPLE-DO-NOT-USE",
			"PEEN_TELEGRAM_WORKSPACE": "relative",
		},
	})
	response := variantRequest(t, variant, http.MethodGet,
		"/api/v1/cron/jobs", nil)
	requireAPIStatus(t, response, http.StatusOK)
}
