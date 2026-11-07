//go:build installer

package installer_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOneShotWithoutManagedController(t *testing.T) {
	for _, control := range []string{hostSpace, dockerSpace} {
		t.Run(control, func(t *testing.T) {
			instance := newInstall(t, installOptions{})
			instance.mustInstall(t, control, dockerSpace)
			instance.configure(t)

			outcome := instance.peenIn(t, instance.project, "--prompt", "say something")
			assertOneShotJSONL(t, instance, outcome)
			assertStopped(t, instance, control)
			unknown := instance.peenIn(t, instance.project, "say something")
			assert.Equal(t, 1, unknown.code)
			assert.Contains(t, unknown.stderr, "unknown command: say something")
			assert.Empty(t, unknown.stdout, "a bare word must not run a turn")
			for _, flags := range [][]string{
				{"--control-space="}, {"--control-space", ""},
			} {
				flags = append(flags, "--prompt", "say something")
				refused := instance.peenIn(t, instance.project, flags...)
				assert.Equal(t, 1, refused.code)
				assert.Contains(t, refused.stderr,
					"--control-space must be host or docker")
			}

			instance.mustPeen(t, "config", "set", "PEEN_DEFAULT_MODEL", "missing/model")
			instance.extraEnv = append(instance.extraEnv,
				"PEEN_DEFAULT_MODEL="+providerName+"/"+providerModel,
				"LOG_LEVEL=warn",
				"LOG_FORMAT=json", "LOG_ADD_SOURCE=false",
			)
			text := instance.peenIn(t, instance.outside,
				"--model="+providerName+"/"+providerModel,
				"--control-space", control, "--workspace", instance.project,
				"--output", "text", "--prompt", "say something")
			require.Zero(t, text.code, text.output())
			assert.Equal(t, providerAnswer+"\n", text.stdout)
			assertStopped(t, instance, control)
			instance.extraEnv = append(instance.extraEnv,
				"PEEN_WORKSPACE_ROOT="+instance.project,
			)

			outside := instance.peenIn(t, instance.outside, "--prompt", "say something")
			assert.Equal(t, 1, outside.code)
			assert.Contains(t, outside.stderr,
				"outside the workspace root "+canonical(t, instance.project))
			assert.Contains(t, outside.stderr, outsideRootHint+
				canonical(t, instance.outside)+" && peen restart")

			explicit := instance.peenIn(t, instance.project, "exec",
				"--workspace", instance.outside, "say something")
			assert.Equal(t, 1, explicit.code)
			assert.Contains(t, explicit.stderr, outsideRootHint+
				canonical(t, instance.outside)+" && peen restart")
		})
	}
}

func assertOneShotJSONL(t *testing.T, instance *install, outcome result) {
	t.Helper()

	require.Zero(t, outcome.code, outcome.output())
	var types []string
	sessions := map[string]bool{}
	// JSONL holds one frame per line, so an empty line is a defect.
	for line := range strings.SplitSeq(strings.TrimSuffix(outcome.stdout, "\n"), "\n") {
		require.NotEmpty(t, strings.TrimSpace(line), "JSONL output holds an empty line")

		var frame struct {
			Type     string `json:"type"`
			Metadata struct {
				SessionID string `json:"sessionId"`
			} `json:"metadata"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &frame), line)
		types = append(types, frame.Type)
		if frame.Metadata.SessionID != "" {
			sessions[frame.Metadata.SessionID] = true
		}
	}
	assert.Contains(t, types, "turn.completed")
	assert.Contains(t, types, webSocketCompleted)
	for session := range sessions {
		instance.recordSession(session)
	}
}
