package oneshot

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/agent"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStructuredRendererKeepsOnlyValidatedOutputOnStdout(t *testing.T) {
	t.Parallel()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	output := &structuredRenderer{
		stdout:  stdout,
		details: newRenderer(OutputFormatText, stderr, stderr),
	}
	require.NoError(t, output.render(controlclient.TurnFrame{
		Type: frameTypeContentBlockDelta,
		Data: json.RawMessage(`{"delta":{"type":"text_delta","text":"draft"}}`),
	}))
	require.NoError(t, output.render(controlclient.TurnFrame{
		Type: agent.EventTypeTurnCompleted,
		Data: json.RawMessage(`{"model":"example/model","text":"draft","output":{"answer":7}}`),
	}))
	require.NoError(t, output.finish())
	assert.Equal(t, "{\"answer\":7}\n", stdout.String())
	assert.Equal(t, "draft\n", stderr.String())
}

func TestStructuredRendererReportsExhaustedAttempts(t *testing.T) {
	t.Parallel()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	output := &structuredRenderer{
		stdout:  stdout,
		details: newRenderer(OutputFormatText, stderr, stderr),
	}
	require.NoError(t, output.render(controlclient.TurnFrame{
		Type: agent.EventTypeTurnCompleted,
		Data: json.RawMessage(`{"model":"example/model","text":"draft","outputError":"schema mismatch"}`),
	}))
	require.NoError(t, output.finish())
	assert.Empty(t, stdout.String())
	assert.Equal(t, "output failed: schema mismatch\n", stderr.String())
}
