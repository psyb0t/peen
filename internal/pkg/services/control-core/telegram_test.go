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

func TestTelegramPreparedBeforeCorePublication(t *testing.T) {
	fixture := newCoreFixtureWith(t, func(c *peenconfig.Config) {
		c.Telegram = true
		c.TelegramToken = "123:EXAMPLE-DO-NOT-USE"
		c.TelegramAllowedChats = "-123"
		c.TelegramWorkspace = c.WorkingDirectory
	})
	runCoreService(t, fixture)
	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, core.Telegram)
}

func TestTelegramOffDoesNotConstructAClient(t *testing.T) {
	fixture := newCoreFixtureWith(t, func(c *peenconfig.Config) {
		c.TelegramToken = "invalid"
		c.TelegramWorkspace = "relative"
	})
	runCoreService(t, fixture)
	core, err := fixture.handoff.Await(t.Context())
	require.NoError(t, err)
	assert.Nil(t, core.Telegram)
}

func TestTelegramRefusesUnauthorizedCronRecipientBeforePublication(t *testing.T) {
	fixture := newCoreFixtureWith(t, func(c *peenconfig.Config) {
		c.Telegram = true
		c.Cron = true
		c.TelegramToken = "123:EXAMPLE-DO-NOT-USE"
		c.TelegramAllowedChats = "-123"
		c.TelegramWorkspace = c.WorkingDirectory
	})
	yaml := fmt.Sprintf("jobs:\n  - name: report\n    schedule: '@daily'\n    workspace: %q\n    message: Review\n    telegramChatId: 999\n",
		fixture.config.WorkingDirectory)
	writeFixtureFile(t, filepath.Join(fixture.config.ConfigDirectory,
		".agents", "cron.yaml"), yaml)
	ctx, cancel := context.WithTimeout(t.Context(), coreTestStartupTimeout)
	defer cancel()
	err := fixture.service.Run(ctx)
	require.ErrorContains(t, err, "telegramChatId is not allowed")
	await, stop := context.WithTimeout(t.Context(), coreTestClearedTimeout)
	defer stop()
	_, err = fixture.handoff.Await(await)
	require.Error(t, err)
}
