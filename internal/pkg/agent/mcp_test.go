package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mcpTestToolName = "mcp__fixture__shot"

// An MCP description over the budget is cut at a character boundary and says
// so; one at the budget or under reaches the model unchanged.
func TestMCPDescriptionsStayWithinTheirBudget(t *testing.T) {
	t.Parallel()

	atLimit := strings.Repeat("a", mcpDescriptionMaxBytes)
	// A three-byte character straddles the budget, so a byte cut would
	// split it.
	straddling := strings.Repeat("a", mcpDescriptionMaxBytes-1) + "€tail"

	testCases := []struct {
		name        string
		description string
		want        string
	}{
		{
			name: "short", description: "Takes a screenshot.",
			want: "Takes a screenshot.",
		},
		{name: "at the budget", description: atLimit, want: atLimit},
		{
			name:        "over the budget",
			description: straddling,
			want: strings.Repeat("a", mcpDescriptionMaxBytes-1) +
				mcpDescriptionCutNote,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := capMCPDescription(tc.description)
			assert.Equal(t, tc.want, got)
			assert.True(t, utf8.ValidString(got))
		})
	}
}

func mcpTestHandler(
	result elelem.ToolResult,
	err error,
) elelem.ToolHandler {
	return func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
		return result, err
	}
}

func TestMCPToolHandlerShowsImagesUpToTheLimitAndNotesTheRest(t *testing.T) {
	t.Parallel()

	png := []byte(testPNGBytes)
	large := []byte(testPNGBytes + " larger than the limit")
	runtime := &Runtime{maxImageBytes: len(png), maxImagesPerMessage: 2}

	images := []elelem.ImageSource{
		{Data: png, MediaType: elelem.MediaTypePNG},
		{Data: png, MediaType: elelem.MediaTypePNG},
		{Data: png, MediaType: elelem.MediaTypePNG},
		{Data: large, MediaType: elelem.MediaTypePNG},
		{Data: []byte("not an image"), MediaType: elelem.MediaTypePNG},
	}

	handler := runtime.mcpToolHandler(mcpTestToolName, mcpTestHandler(
		elelem.ToolResult{Content: "the screen", Images: images}, nil,
	))

	result, err := handler(contextWithImageInput(t.Context(), true),
		elelem.ToolInput{Name: mcpTestToolName})
	require.NoError(t, err)

	assert.Nil(t, result.Images, "images travel as Peen's image metadata")
	assert.Equal(t, "the screen\n"+
		"[image 3 of 5 not shown: it is over PEEN_MAX_IMAGES_PER_MESSAGE]\n"+
		"[image 4 of 5 not shown: it is larger than PEEN_MAX_IMAGE_BYTES]\n"+
		"[image 5 of 5 not shown: it is not PNG, JPEG, GIF, or WebP data]",
		result.Content)

	shown, err := resultImages(result)
	require.NoError(t, err)
	require.Len(t, shown, 2)

	for _, image := range shown {
		assert.Equal(t, png, image.Data)
		assert.Equal(t, mcpTestToolName, image.Tool)
	}

	assert.Contains(t, toolImageText(shown),
		"The 2 images below were returned by the tool "+mcpTestToolName)
	assert.Contains(t, toolImageText(shown[:1]),
		"The image below was returned by the tool "+mcpTestToolName)

	message := toolImageMessage(shown)
	assert.Len(t, message.Content, 1+len(shown),
		"one text part, then one part per image")
}

func TestMCPToolHandlerWithoutImageInputNotesEveryImage(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{maxImageBytes: len(testPNGBytes)}
	handler := runtime.mcpToolHandler(mcpTestToolName, mcpTestHandler(
		elelem.ToolResult{Content: "the screen", Images: []elelem.ImageSource{
			{Data: []byte(testPNGBytes), MediaType: elelem.MediaTypePNG},
		}}, nil,
	))

	result, err := handler(contextWithImageInput(t.Context(), false),
		elelem.ToolInput{Name: mcpTestToolName})
	require.NoError(t, err)

	assert.Equal(t, "the screen\n"+
		"[image 1 of 1 not shown: the model does not take image input]",
		result.Content)

	images, err := resultImages(result)
	require.NoError(t, err)
	assert.Empty(t, images)
}

func TestMCPToolHandlerErrors(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{}

	t.Run("a server failure is a tool error", func(t *testing.T) {
		t.Parallel()

		handler := runtime.mcpToolHandler(mcpTestToolName, mcpTestHandler(
			elelem.ToolResult{},
			errors.New("MCP server stopped"), //nolint:err113 // Test cause.
		))

		result, err := handler(t.Context(), elelem.ToolInput{})
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Equal(t, "MCP server stopped", result.Content)
	})

	t.Run("cancellation ends the turn", func(t *testing.T) {
		t.Parallel()

		handler := runtime.mcpToolHandler(mcpTestToolName, mcpTestHandler(
			elelem.ToolResult{}, context.Canceled,
		))

		_, err := handler(t.Context(), elelem.ToolInput{})
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("an error result keeps no image", func(t *testing.T) {
		t.Parallel()

		png := []byte(testPNGBytes)
		handler := (&Runtime{maxImageBytes: len(png)}).mcpToolHandler(
			mcpTestToolName, mcpTestHandler(elelem.ToolResult{
				Content: "failed", IsError: true,
				Images: []elelem.ImageSource{
					{Data: png, MediaType: elelem.MediaTypePNG},
				},
			}, nil),
		)

		result, err := handler(contextWithImageInput(t.Context(), true),
			elelem.ToolInput{})
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Nil(t, result.Metadata)
	})
}

func TestMCPToolsWithoutAManager(t *testing.T) {
	t.Parallel()

	assert.Nil(t, (&Runtime{}).mcpTools(nil))
}

// A turn with no MCP configuration still records an mcp.status event with an
// empty server list, so the latest report never names servers that are no
// longer configured.
func TestTurnWithoutMCPRecordsAnEmptyStatus(t *testing.T) {
	ctx := context.Background()
	fixture := newRuntimeFixture(t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")))

	result, err := fixture.runtime.Run(ctx, TurnRequest{
		Message:   "no MCP here",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	page, err := fixture.store.ListEvents(ctx, result.SessionID,
		session.ListEventsOptions{
			Limit:      session.MaximumPageLimit,
			EventTypes: []string{EventTypeMCPStatus},
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.JSONEq(t, `{"servers":[]}`, page.Items[0].PayloadJSON)
}
