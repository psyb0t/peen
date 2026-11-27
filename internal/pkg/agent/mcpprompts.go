package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/mcp"
)

const (
	toolNameListMCPPrompts = "listMcpPrompts"
	toolNameGetMCPPrompt   = "getMcpPrompt"

	mcpPromptTemplateName = "mcpPrompt"
	//nolint:lll // One model-facing block.
	mcpPromptTemplateText = `<mcpPrompt server="{{attr .Server}}" name="{{attr .Name}}">{{range .Messages}}
<promptMessage role="{{attr .Role}}">
{{.Text}}
</promptMessage>{{end}}
</mcpPrompt>`

	// mcpPromptImageFormat describes an image a prompt message carries where
	// it cannot be shown.
	mcpPromptImageFormat = "[image (%s, %d bytes) from the prompt is not shown]"
	mcpPromptPartFormat  = "[%s content from the prompt is not shown]"

	//nolint:lll // One model-facing description.
	listMCPPromptsDescription = "Lists the prompts the connected MCP servers offer: reusable instructions a server fills in from named arguments. Pass a prompt's name and arguments to getMcpPrompt. A server with nextCursor has more prompts: call again with that server and cursor."
	//nolint:lll // One model-facing description.
	listMCPPromptsServerDescription = "The MCP server to list, as named in mcp__<server>__ tool names. Omit it to list every server that offers prompts. Required with cursor."
	//nolint:lll // One model-facing description.
	listMCPPromptsCursorDescription = "The nextCursor from an earlier listMcpPrompts result for the same server. Omit it for the first page."

	//nolint:lll // One model-facing description.
	getMCPPromptDescription       = "Fills in one prompt from an MCP server and returns its messages, each with the role the server gave it. Images reach you as images when the model takes them. Fails when the server is not running, offers no prompts, has no prompt by that name, or refuses the arguments."
	getMCPPromptServerDescription = "The MCP server that offers the prompt."
	//nolint:lll // One model-facing description.
	getMCPPromptNameDescription = "The prompt name exactly as listMcpPrompts gave it."
	//nolint:lll // One model-facing description.
	getMCPPromptArgumentsDescription = "The prompt's arguments as names mapped to string values, from the arguments listMcpPrompts gave. Omit it for a prompt that takes none."
)

type listMCPPromptsInput struct {
	Server string `json:"server"`
	Cursor string `json:"cursor"`
}

type listMCPPromptsOutput struct {
	Servers []mcpPromptListing `json:"servers"`
}

type mcpPromptListing struct {
	Server     string           `json:"server"`
	Prompts    []mcpPromptEntry `json:"prompts,omitempty"`
	NextCursor string           `json:"nextCursor,omitempty"`
	Error      string           `json:"error,omitempty"`
}

type mcpPromptEntry struct {
	Name        string                   `json:"name"`
	Title       string                   `json:"title,omitempty"`
	Description string                   `json:"description,omitempty"`
	Arguments   []mcpPromptArgumentEntry `json:"arguments,omitempty"`
}

type mcpPromptArgumentEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type getMCPPromptInput struct {
	Server    string            `json:"server"`
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

// mcpPromptData is what mcpPromptTemplateText renders.
type mcpPromptData struct {
	Server   string
	Name     string
	Messages []mcpPromptMessageData
}

type mcpPromptMessageData struct {
	Role string
	Text string
}

// renderMCPPrompt renders a filled-in prompt as an <mcpPrompt> block, with
// the server's text escaped. keepImages returns the images for the caller
// to show; otherwise each is described in its message's text.
func renderMCPPrompt(
	server, name string,
	result mcp.PromptResult,
	keepImages bool,
) (string, []elelem.ImageSource, error) {
	data := mcpPromptData{
		Server: server, Name: name,
		Messages: make([]mcpPromptMessageData, 0, len(result.Messages)),
	}
	images := make([]elelem.ImageSource, 0)

	for _, message := range result.Messages {
		texts := make([]string, 0, len(message.Content))

		for _, part := range message.Content {
			if keepImages && part.Type == elelem.PartTypeImage &&
				part.Image != nil {
				images = append(images, *part.Image)

				continue
			}

			texts = append(texts, mcpPromptPartText(part))
		}

		data.Messages = append(data.Messages, mcpPromptMessageData{
			Role: message.Role, Text: strings.Join(texts, mcpNoteSeparator),
		})
	}

	rendered, err := renderTemplate(mcpPromptTemplateName,
		mcpPromptTemplateText, data)
	if err != nil {
		return "", nil, err
	}

	return rendered, images, nil
}

// mcpPromptPartText is a prompt part as escaped text, or a note for a part
// the message cannot show.
func mcpPromptPartText(part elelem.Part) string {
	switch {
	case part.Type == elelem.PartTypeText:
		return mcpTextEscaper.Replace(part.Text)
	case part.Type == elelem.PartTypeImage && part.Image != nil:
		return fmt.Sprintf(mcpPromptImageFormat, part.Image.MediaType,
			len(part.Image.Data))
	default:
		return fmt.Sprintf(mcpPromptPartFormat, part.Type)
	}
}

// mcpPromptTools are listMcpPrompts and getMcpPrompt, offered only while a
// running server offers prompts.
func (r *Runtime) mcpPromptTools(
	onPostRun elelem.MessageInjector,
) []elelem.Tool {
	servers := r.mcp.PromptServers()
	if len(servers) == 0 {
		return nil
	}

	listSchema, err := objectSchema(jsonSchemaObject{
		mcpArgumentServer: mcpServerProperty(
			listMCPPromptsServerDescription, servers),
		mcpArgumentCursor: stringProperty(listMCPPromptsCursorDescription),
	})
	if err != nil {
		return nil
	}

	getSchema, err := objectSchema(jsonSchemaObject{
		mcpArgumentServer: mcpServerProperty(
			getMCPPromptServerDescription, servers),
		mcpArgumentName: stringProperty(getMCPPromptNameDescription),
		mcpArgumentArguments: jsonSchemaObject{
			schemaKeyType:        schemaTypeObject,
			schemaKeyDescription: getMCPPromptArgumentsDescription,
			schemaKeyAdditionalProperties: jsonSchemaObject{
				schemaKeyType: mcpSchemaTypeString,
			},
		},
	}, mcpArgumentServer, mcpArgumentName)
	if err != nil {
		return nil
	}

	list := hostTool(toolNameListMCPPrompts, listMCPPromptsDescription,
		string(listSchema), r.listMCPPrompts, onPostRun)

	return []elelem.Tool{list, {
		Name:                   toolNameGetMCPPrompt,
		Description:            getMCPPromptDescription,
		ArgumentsSchema:        getSchema,
		Handler:                r.getMCPPromptHandler(),
		PostRunMessageInjector: onPostRun,
	}}
}

func (r *Runtime) listMCPPrompts(
	ctx context.Context,
	input listMCPPromptsInput,
) (listMCPPromptsOutput, error) {
	servers, err := mcpListServers(r.mcp.PromptServers(), input.Server,
		input.Cursor != "")
	if err != nil {
		return listMCPPromptsOutput{}, err
	}

	output := listMCPPromptsOutput{
		Servers: make([]mcpPromptListing, 0, len(servers)),
	}

	for _, server := range servers {
		listing := mcpPromptListing{Server: server}

		page, err := r.mcp.ListPrompts(ctx, server, input.Cursor)
		if err != nil {
			if isTurnEndingError(err) || input.Server != "" {
				return listMCPPromptsOutput{}, ctxerrors.Wrap(err,
					"list MCP prompts")
			}

			listing.Error = toolErrorMessage(err)
			output.Servers = append(output.Servers, listing)

			continue
		}

		listing.Prompts = mcpPromptEntries(page.Prompts)
		listing.NextCursor = page.NextCursor
		output.Servers = append(output.Servers, listing)
	}

	return output, nil
}

func mcpPromptEntries(prompts []mcp.Prompt) []mcpPromptEntry {
	entries := make([]mcpPromptEntry, 0, len(prompts))

	for _, prompt := range prompts {
		arguments := make([]mcpPromptArgumentEntry, 0, len(prompt.Arguments))
		for _, argument := range prompt.Arguments {
			arguments = append(arguments, mcpPromptArgumentEntry{
				Name: argument.Name,
				Description: workflowUTF8Prefix(argument.Description,
					mcpListTextMaxBytes),
				Required: argument.Required,
			})
		}

		entries = append(entries, mcpPromptEntry{
			Name:  prompt.Name,
			Title: workflowUTF8Prefix(prompt.Title, mcpListTextMaxBytes),
			Description: workflowUTF8Prefix(prompt.Description,
				mcpListTextMaxBytes),
			Arguments: arguments,
		})
	}

	return entries
}

// getMCPPromptHandler fills in a prompt and returns it as an <mcpPrompt>
// block, with its images routed the way an MCP tool's images are.
func (r *Runtime) getMCPPromptHandler() elelem.ToolHandler {
	return func(
		ctx context.Context,
		call elelem.ToolInput,
	) (elelem.ToolResult, error) {
		ctx = contextWithParentToolCallID(ctx, call.CallID)

		var input getMCPPromptInput
		if err := decodeToolArguments(call.Arguments, &input); err != nil {
			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		asked := r.mcpElicitations.Load()

		result, err := r.mcp.GetPrompt(ctx, input.Server, input.Name,
			input.Arguments)
		if err != nil {
			if isTurnEndingError(err) {
				return elelem.ToolResult{}, ctxerrors.Wrap(err,
					"get MCP prompt")
			}

			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		text, images, err := renderMCPPrompt(input.Server, input.Name,
			result, true)
		if err != nil {
			return elelem.ToolResult{}, err
		}

		return r.mcpShowResult(ctx, toolNameGetMCPPrompt,
			elelem.ToolResult{Content: text, Images: images}, asked)
	}
}
