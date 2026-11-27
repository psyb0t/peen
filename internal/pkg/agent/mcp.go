package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/mcptools"
)

const (
	mcpImageNotShownFormat  = "[image %d of %d not shown: %s]"
	mcpNoteSeparator        = "\n"
	mcpReasonNoImageInput   = "the model does not take image input"
	mcpReasonTooManyImages  = "it is over PEEN_MAX_IMAGES_PER_MESSAGE"
	mcpReasonImageTooLarge  = "it is larger than PEEN_MAX_IMAGE_BYTES"
	mcpReasonImageWrongType = "it is not PNG, JPEG, GIF, or WebP data"

	// mcpDescriptionMaxBytes bounds an MCP tool's description. A server
	// writes its own descriptions, and every one rides on every request.
	mcpDescriptionMaxBytes = 2048
	//nolint:lll // One model-facing note.
	mcpDescriptionCutNote = "\n<note kind=\"descriptionCut\">The server's description was longer than 2048 bytes; this is its start.</note>"

	// mcpElicitationNote follows the result of a call during which an MCP
	// server asked the user a question, because the user's reply joins the
	// conversation like any message sent while a turn runs.
	//nolint:lll // One model-facing note.
	mcpElicitationNote = "<note kind=\"mcpElicitation\">While this call ran, an MCP server asked the user a question. The user's reply also reaches you as a user message after this result; it answers the server, not you.</note>"
)

// capMCPDescription cuts a description longer than mcpDescriptionMaxBytes at
// a character boundary and says it was cut.
func capMCPDescription(description string) string {
	if len(description) <= mcpDescriptionMaxBytes {
		return description
	}

	return workflowUTF8Prefix(description, mcpDescriptionMaxBytes) +
		mcpDescriptionCutNote
}

// mcpStatusPayload is the mcp.status event: every configured MCP server and
// what happened when the worker started it.
type mcpStatusPayload struct {
	Servers []mcptools.Status `json:"servers"`
}

// mcpTools are the tools of every connected MCP server, wrapped so their
// images reach the model the way readFile's do, followed by the resource
// and prompt tools when a server offers either.
func (r *Runtime) mcpTools(onPostRun elelem.MessageInjector) []elelem.Tool {
	if !r.mcp.Configured() {
		return nil
	}

	available := r.mcp.Tools()
	registered := make([]elelem.Tool, 0, len(available))

	for _, tool := range available {
		tool.Description = capMCPDescription(tool.Description)
		tool.Handler = r.mcpToolHandler(tool.Name, tool.Handler)
		tool.PostRunMessageInjector = onPostRun
		registered = append(registered, tool)
	}

	registered = append(registered, r.mcpResourceTools(onPostRun)...)

	return append(registered, r.mcpPromptTools(onPostRun)...)
}

// emitMCPStatus records the session's MCP servers at the start of every
// turn, so a client can see which servers run and why one failed. A turn
// with no MCP configuration records an empty list, so the latest report
// never describes servers that are no longer configured. The servers were
// refreshed before the turn's system prompt was built.
func (p *preparedTurn) emitMCPStatus(ctx context.Context, r *Runtime) error {
	servers := []mcptools.Status{}
	if r.mcp.Configured() {
		servers = r.mcp.Status(ctx)
	}

	if err := p.turn.emit(ctx, EventTypeMCPStatus, mcpStatusPayload{
		Servers: servers,
	}); err != nil {
		return ctxerrors.Wrap(err, "emit MCP status")
	}

	return nil
}

// mcpToolHandler runs one MCP tool. A server failure is a tool error the
// model can react to. Only cancellation ends the turn. A returned image
// travels the way a readFile image does, so it is stored and replayed with
// the transcript.
func (r *Runtime) mcpToolHandler(
	name string,
	call elelem.ToolHandler,
) elelem.ToolHandler {
	return func(
		ctx context.Context,
		input elelem.ToolInput,
	) (elelem.ToolResult, error) {
		ctx = contextWithParentToolCallID(ctx, input.CallID)
		asked := r.mcpElicitations.Load()

		result, err := call(ctx, input)
		if err != nil {
			if isTurnEndingError(err) {
				return elelem.ToolResult{}, ctxerrors.Wrap(err, "run MCP tool")
			}

			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		return r.mcpShowResult(ctx, name, result, asked)
	}
}

// mcpShowResult describes the images the model cannot see, attaches the ones
// it can, and notes a question the server asked the user while the call ran.
// asked is the elicitation count read before the call.
func (r *Runtime) mcpShowResult(
	ctx context.Context,
	name string,
	result elelem.ToolResult,
	asked int64,
) (elelem.ToolResult, error) {
	shown, notes := r.mcpResultImages(ctx, name, result.Images)
	result.Images = nil
	// Resource links ride in elelem's metadata. The text already names each
	// link, and a tool result's metadata carries Peen's own keys only.
	result.Metadata = nil

	if r.mcpElicitations.Load() != asked {
		notes = append(notes, mcpElicitationNote)
	}

	if len(notes) > 0 {
		result.Content = strings.Join(
			append([]string{result.Content}, notes...), mcpNoteSeparator,
		)
	}

	if len(shown) == 0 || result.IsError {
		return result, nil
	}

	return withToolImages(result, shown)
}

// mcpResultImages picks the images a result shows the model, at most
// PEEN_MAX_IMAGES_PER_MESSAGE, and says for every other image why it is not
// shown. Nothing is dropped silently.
func (r *Runtime) mcpResultImages(
	ctx context.Context,
	tool string,
	images []elelem.ImageSource,
) ([]toolImage, []string) {
	notes := make([]string, 0)
	shown := make([]toolImage, 0, len(images))

	for index, image := range images {
		reason := ""

		switch {
		case !imageInputAllowed(ctx):
			reason = mcpReasonNoImageInput
		case len(image.Data) > r.maxImageBytes:
			reason = mcpReasonImageTooLarge
		case !isImageOf(image.MediaType, image.Data):
			reason = mcpReasonImageWrongType
		case len(shown) >= r.maxImagesPerMessage:
			reason = mcpReasonTooManyImages
		}

		if reason != "" {
			notes = append(notes, fmt.Sprintf(
				mcpImageNotShownFormat, index+1, len(images), reason,
			))

			continue
		}

		shown = append(shown, toolImage{
			Tool: tool, MediaType: image.MediaType, Data: image.Data,
		})
	}

	return shown, notes
}
