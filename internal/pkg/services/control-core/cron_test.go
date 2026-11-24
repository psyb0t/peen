package controlcore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlCoreCronOffIgnoresMalformedConfiguration(t *testing.T) {
	fixture := newCoreFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.config.ConfigDirectory, ".agents", "cron.yaml"), "jobs: [")
	runCoreService(t, fixture)
	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)
	jobs, err := core.Cron.ListCronJobs(t.Context())
	require.NoError(t, err)
	assert.Empty(t, jobs.Items)
}

func TestControlCoreCronLoadsBeforePublishingAndRefusesInvalidJobs(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{name: "outside policy", field: "workspace", value: "/"},
		{name: "unknown space", field: "agentSpace", value: "missing"},
		{name: "telegram disabled", field: "telegramChatId", value: "123"},
		{name: "unknown model", field: "model", value: "missing/model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCoreFixtureWith(t, func(c *peenconfig.Config) { c.Cron = true })
			data := fmt.Sprintf("jobs:\n  - name: report\n    schedule: '@daily'\n    workspace: %q\n    message: Review\n", fixture.config.WorkingDirectory)
			if tc.field == "workspace" {
				data = fmt.Sprintf("jobs:\n  - name: report\n    schedule: '@daily'\n    workspace: %q\n    message: Review\n", tc.value)
			} else {
				data += fmt.Sprintf("    %s: %s\n", tc.field, tc.value)
			}
			writeFixtureFile(t, filepath.Join(fixture.config.ConfigDirectory, ".agents", "cron.yaml"), data)
			ctx, cancel := context.WithTimeout(t.Context(), coreTestStartupTimeout)
			defer cancel()
			err := fixture.service.Run(ctx)
			require.Error(t, err)
			assert.ErrorContains(t, err, "report")
			await, cancelAwait := context.WithTimeout(t.Context(), coreTestClearedTimeout)
			defer cancelAwait()
			_, err = fixture.handoff.Await(await)
			require.Error(t, err)
		})
	}
}
