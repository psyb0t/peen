//go:build installer

package installer_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	systemAgentSetting     = "PEEN_SYSTEM_AGENT"
	systemAgentPrompt      = "report the deployment"
	systemAgentName        = "core:system"
	systemAgentSpace       = "control"
	systemSessionAPIPath   = "/api/v1/sessions/system"
	systemAgentOffErrorMsg = "the system agent is off"
)

// The installed command has to start a managed controller, and that controller
// can reach the Docker daemon the suite runs on, so this is the deployment where
// the system agent is host root: every other session stays refused from
// control, and the system session runs there anyway.
func TestSystemCommandThroughTheInstalledCommand(t *testing.T) {
	for _, control := range []string{hostSpace, dockerSpace} {
		t.Run(control, func(t *testing.T) {
			instance := newInstall(t, installOptions{})
			instance.mustInstall(t, control, dockerSpace)
			instance.configure(t)

			off := instance.peenIn(t, instance.project, "system", systemAgentPrompt)
			assert.NotZero(t, off.code, off.output())
			assert.Contains(t, off.output(), systemAgentOffErrorMsg)

			instance.mustPeen(t, "config", "set", systemAgentSetting, "true")
			instance.mustPeen(t, "restart")

			turn := instance.peenIn(
				t,
				instance.project,
				"system",
				"--output",
				"text",
				systemAgentPrompt,
			)
			require.Zero(t, turn.code, turn.output())
			assert.Equal(t, providerAnswer+"\n", turn.stdout)

			status, content := apiCall(
				t,
				instance,
				http.MethodPost,
				systemSessionAPIPath,
				nil,
			)
			require.Equal(t, http.StatusOK, status, string(content))

			opened := struct {
				Session struct {
					Agent      string `json:"agent"`
					AgentSpace string `json:"agentSpace"`
				} `json:"session"`
			}{}
			require.NoError(t, json.Unmarshal(content, &opened))
			assert.Equal(t, systemAgentName, opened.Session.Agent)
			assert.Equal(t, systemAgentSpace, opened.Session.AgentSpace)
		})
	}
}
