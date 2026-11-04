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
)

// mcpStatusPayload is the mcp.status event: every configured MCP server and
// what happened when the worker started it.
type mcpStatusPayload struct {
	Servers []mcptools.Status `json:"servers"`
}

// mcpTools are the tools of every connected MCP server, wrapped so their
// images reach the model the way readFile's do.
func (r *Runtime) mcpTools(onPostRun elelem.MessageInjector) []elelem.Tool {
	if !r.mcp.Configured() {
		return nil
	}

	available := r.mcp.Tools()
	registered := make([]elelem.Tool, 0, len(available))

	for _, tool := range available {
		tool.Handler = r.mcpToolHandler(tool.Name, tool.Handler)
		tool.PostRunMessageInjector = onPostRun
		registered = append(registered, tool)
	}

	return registered
}

// emitMCPStatus records the session's MCP servers at the start of a turn,
// so a client can see which servers run and why one failed.
func (p *preparedTurn) emitMCPStatus(ctx context.Context, r *Runtime) error {
	// Refresh first: a session that had no mcp.json when its worker started
	// may have one now.
	r.mcp.Refresh(ctx)

	if !r.mcp.Configured() {
		return nil
	}

	if err := p.turn.emit(ctx, EventTypeMCPStatus, mcpStatusPayload{
		Servers: r.mcp.Status(ctx),
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
		result, err := call(ctx, input)
		if err != nil {
			if isTurnEndingError(err) {
				return elelem.ToolResult{}, ctxerrors.Wrap(err, "run MCP tool")
			}

			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		shown, notes := r.mcpResultImages(ctx, name, result.Images)
		result.Images = nil

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
