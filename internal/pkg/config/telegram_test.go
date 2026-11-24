package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramPolicyRequiresExplicitGrants(t *testing.T) {
	t.Parallel()
	base := Config{
		Telegram: true, TelegramToken: "123:EXAMPLE-DO-NOT-USE",
		TelegramWorkspace:    "/workspaces/telegram",
		TelegramAllowedChats: "-123,456", TelegramAllowedUsers: "7,8",
	}
	policy, err := base.TelegramPolicy()
	require.NoError(t, err)
	assert.Len(t, policy.Chats, 2)
	assert.Len(t, policy.Users, 2)
	signed := base
	signed.TelegramAllowedChats = "123,-123"
	policy, err = signed.TelegramPolicy()
	require.NoError(t, err)
	assert.Len(t, policy.Chats, 2)
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"missing token", func(c *Config) { c.TelegramToken = "" }},
		{"URL token", func(c *Config) { c.TelegramToken = "123:/token" }},
		{"relative workspace", func(c *Config) { c.TelegramWorkspace = "relative" }},
		{"missing chats", func(c *Config) { c.TelegramAllowedChats = "" }},
		{"wildcard", func(c *Config) { c.TelegramAllowedChats = "*" }},
		{"zero chat", func(c *Config) { c.TelegramAllowedChats = "0" }},
		{"negative user", func(c *Config) { c.TelegramAllowedUsers = "-7" }},
		{"zero user", func(c *Config) { c.TelegramAllowedUsers = "0" }},
		{"overflow", func(c *Config) { c.TelegramAllowedChats = "9223372036854775808" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := base
			tc.change(&config)
			_, err := config.TelegramPolicy()
			require.Error(t, err)
			if config.TelegramToken != "" {
				assert.NotContains(t, err.Error(), config.TelegramToken)
			}
		})
	}
	disabled := Config{}
	_, err = disabled.TelegramPolicy()
	require.NoError(t, err)
}

func TestTelegramSettingsStayOutOfWorkersAndCommands(t *testing.T) {
	t.Parallel()
	environment := []string{
		"PEEN_TELEGRAM=true", "PEEN_TELEGRAM_TOKEN=EXAMPLE-DO-NOT-USE",
		"PEEN_TELEGRAM_ALLOWED_CHATS=123", "PEEN_TELEGRAM_ALLOWED_USERS=7",
		"PEEN_TELEGRAM_WORKSPACE=/workspaces/telegram",
		"PEEN_TELEGRAM_AGENT_SPACE=control", "PATH=/bin",
	}
	keys := []string{"PEEN_TELEGRAM_TOKEN"}
	assert.Equal(t, []string{"PATH=/bin", environmentLogFormat + "=" + workerLogFormat},
		ControlWorkerEnvironment(environment, keys))
	command, err := (Config{UpstreamsJSON: "[]"}).CommandEnvironment(environment)
	require.NoError(t, err)
	for _, entry := range command {
		assert.NotContains(t, entry, "PEEN_TELEGRAM_TOKEN=")
	}
}
