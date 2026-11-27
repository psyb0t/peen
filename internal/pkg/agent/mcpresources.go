package agent

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/mcp"
)

const (
	toolNameListMCPResources = "listMcpResources"
	toolNameReadMCPResource  = "readMcpResource"

	// mcpListTextMaxBytes bounds each server-written description or title a
	// list tool returns.
	mcpListTextMaxBytes = 256

	//nolint:lll // One model-facing description.
	listMCPResourcesDescription = "Lists the resources and resource templates the connected MCP servers offer. Each resource has a uri you can pass to readMcpResource. A template has a uriTemplate; fill its {placeholders} to build a uri. A server with nextCursor or nextTemplatesCursor has more entries: call again with that server and the cursor to read the next page of that list."
	//nolint:lll // One model-facing description.
	listMCPResourcesServerDescription = "The MCP server to list, as named in mcp__<server>__ tool names. Omit it to list every server that offers resources. Required with cursor or templatesCursor."
	//nolint:lll // One model-facing description.
	listMCPResourcesCursorDescription = "The nextCursor from an earlier listMcpResources result for the same server, to read its next page of resources. Omit it for the first page."
	//nolint:lll // One model-facing description.
	listMCPResourcesTemplatesCursorDescription = "The nextTemplatesCursor from an earlier listMcpResources result for the same server, to read its next page of templates. Omit it for the first page."

	//nolint:lll // One model-facing description.
	readMCPResourceDescription       = "Reads one resource from an MCP server. Text comes back as text, and images reach you as images when the model takes them. Other binary contents are described by type and size, not shown. A resource link in an MCP tool result can be read with this tool. Fails when the server is not running, offers no resources, or has no resource at the uri."
	readMCPResourceServerDescription = "The MCP server that owns the resource."
	//nolint:lll // One model-facing description.
	readMCPResourceURIDescription = "The resource uri exactly as listMcpResources or an MCP tool result gave it, or one built from a resource template."
)

type listMCPResourcesInput struct {
	Server          string `json:"server"`
	Cursor          string `json:"cursor"`
	TemplatesCursor string `json:"templatesCursor"`
}

type listMCPResourcesOutput struct {
	Servers []mcpResourceListing `json:"servers"`
}

// mcpResourceListing is one server's page of resources and templates. Error
// says why a server listed with others could not be listed.
type mcpResourceListing struct {
	Server          string             `json:"server"`
	Resources       []mcpResourceEntry `json:"resources,omitempty"`
	Templates       []mcpTemplateEntry `json:"templates,omitempty"`
	NextCursor      string             `json:"nextCursor,omitempty"`
	TemplatesCursor string             `json:"nextTemplatesCursor,omitempty"`
	Error           string             `json:"error,omitempty"`
}

type mcpResourceEntry struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type mcpTemplateEntry struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type readMCPResourceInput struct {
	Server string `json:"server"`
	URI    string `json:"uri"`
}

// jsonSchemaObject is a tool's argument schema. A map marshals with sorted
// keys, so the schema bytes stay the same while the servers do.
type jsonSchemaObject = map[string]any

const (
	schemaKeyType                 = "type"
	schemaKeyDescription          = "description"
	schemaKeyEnum                 = "enum"
	schemaKeyProperties           = "properties"
	schemaKeyRequired             = "required"
	schemaKeyAdditionalProperties = "additionalProperties"
	schemaTypeObject              = "object"

	mcpArgumentServer          = "server"
	mcpArgumentCursor          = "cursor"
	mcpArgumentTemplatesCursor = "templatesCursor"
	mcpArgumentURI             = "uri"
	mcpArgumentName            = "name"
	mcpArgumentArguments       = "arguments"
)

// mcpServerProperty is a server argument limited to the servers that offer
// a feature this turn.
func mcpServerProperty(
	description string,
	servers []string,
) jsonSchemaObject {
	return jsonSchemaObject{
		schemaKeyType:        mcpSchemaTypeString,
		schemaKeyDescription: description,
		schemaKeyEnum:        servers,
	}
}

func stringProperty(description string) jsonSchemaObject {
	return jsonSchemaObject{
		schemaKeyType: mcpSchemaTypeString, schemaKeyDescription: description,
	}
}

func objectSchema(
	properties jsonSchemaObject,
	required ...string,
) (json.RawMessage, error) {
	schema := jsonSchemaObject{
		schemaKeyType: schemaTypeObject, schemaKeyProperties: properties,
		schemaKeyAdditionalProperties: false,
	}
	if len(required) > 0 {
		schema[schemaKeyRequired] = required
	}

	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode MCP tool schema")
	}

	return encoded, nil
}

// mcpResourceTools are listMcpResources and readMcpResource, offered only
// while a running server offers resources. Their server argument lists the
// servers that do when the tool set is built, so it stays fixed for the turn.
func (r *Runtime) mcpResourceTools(
	onPostRun elelem.MessageInjector,
) []elelem.Tool {
	servers := r.mcp.ResourceServers()
	if len(servers) == 0 {
		return nil
	}

	listSchema, err := objectSchema(jsonSchemaObject{
		mcpArgumentServer: mcpServerProperty(
			listMCPResourcesServerDescription, servers),
		mcpArgumentCursor: stringProperty(listMCPResourcesCursorDescription),
		mcpArgumentTemplatesCursor: stringProperty(
			listMCPResourcesTemplatesCursorDescription),
	})
	if err != nil {
		return nil
	}

	readSchema, err := objectSchema(jsonSchemaObject{
		mcpArgumentServer: mcpServerProperty(
			readMCPResourceServerDescription, servers),
		mcpArgumentURI: stringProperty(readMCPResourceURIDescription),
	}, mcpArgumentServer, mcpArgumentURI)
	if err != nil {
		return nil
	}

	list := hostTool(toolNameListMCPResources, listMCPResourcesDescription,
		string(listSchema), r.listMCPResources, onPostRun)

	return []elelem.Tool{list, {
		Name:                   toolNameReadMCPResource,
		Description:            readMCPResourceDescription,
		ArgumentsSchema:        readSchema,
		Handler:                r.readMCPResourceHandler(),
		PostRunMessageInjector: onPostRun,
	}}
}

// mcpListServers is the one server a list call names, or every server that
// offers the feature. A cursor needs the server it came from.
func mcpListServers(
	offering []string,
	server string,
	hasCursor bool,
) ([]string, error) {
	if server == "" {
		if hasCursor {
			return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
				"a cursor needs the server it came from; set server")
		}

		return offering, nil
	}

	if !slices.Contains(offering, server) {
		return nil, ctxerrors.Wrapf(commerr.ErrValidationFailed,
			"no running MCP server named %q offers this; use one of %v",
			server, offering)
	}

	return []string{server}, nil
}

func (r *Runtime) listMCPResources(
	ctx context.Context,
	input listMCPResourcesInput,
) (listMCPResourcesOutput, error) {
	hasCursor := input.Cursor != "" || input.TemplatesCursor != ""

	servers, err := mcpListServers(r.mcp.ResourceServers(), input.Server,
		hasCursor)
	if err != nil {
		return listMCPResourcesOutput{}, err
	}

	output := listMCPResourcesOutput{
		Servers: make([]mcpResourceListing, 0, len(servers)),
	}

	for _, server := range servers {
		listing, err := r.listMCPServerResources(ctx, server, input)
		if err != nil {
			if isTurnEndingError(err) || input.Server != "" {
				return listMCPResourcesOutput{}, err
			}

			listing.Error = toolErrorMessage(err)
		}

		output.Servers = append(output.Servers, listing)
	}

	return output, nil
}

// listMCPServerResources lists one page of a server's resources, of its
// templates, or both when no cursor asks for one of them.
func (r *Runtime) listMCPServerResources(
	ctx context.Context,
	server string,
	input listMCPResourcesInput,
) (mcpResourceListing, error) {
	listing := mcpResourceListing{Server: server}
	listsResources := input.TemplatesCursor == ""
	listsTemplates := input.Cursor == ""

	if listsResources {
		page, err := r.mcp.ListResources(ctx, server, input.Cursor)
		if err != nil {
			return listing, ctxerrors.Wrap(err, "list MCP resources")
		}

		listing.Resources = mcpResourceEntries(page.Resources)
		listing.NextCursor = page.NextCursor
	}

	if listsTemplates {
		page, err := r.mcp.ListResourceTemplates(ctx, server,
			input.TemplatesCursor)
		if err != nil {
			return listing, ctxerrors.Wrap(err, "list MCP resource templates")
		}

		listing.Templates = mcpTemplateEntries(page.Templates)
		listing.TemplatesCursor = page.NextCursor
	}

	return listing, nil
}

func mcpResourceEntries(resources []mcp.Resource) []mcpResourceEntry {
	entries := make([]mcpResourceEntry, 0, len(resources))
	for _, resource := range resources {
		entries = append(entries, mcpResourceEntry{
			URI: resource.URI, Name: resource.Name,
			Title: workflowUTF8Prefix(resource.Title, mcpListTextMaxBytes),
			Description: workflowUTF8Prefix(resource.Description,
				mcpListTextMaxBytes),
			MIMEType: resource.MIMEType, Size: resource.Size,
		})
	}

	return entries
}

func mcpTemplateEntries(templates []mcp.ResourceTemplate) []mcpTemplateEntry {
	entries := make([]mcpTemplateEntry, 0, len(templates))
	for _, template := range templates {
		entries = append(entries, mcpTemplateEntry{
			URITemplate: template.URITemplate, Name: template.Name,
			Title: workflowUTF8Prefix(template.Title, mcpListTextMaxBytes),
			Description: workflowUTF8Prefix(template.Description,
				mcpListTextMaxBytes),
			MIMEType: template.MIMEType,
		})
	}

	return entries
}

// readMCPResourceHandler reads a resource and returns its text, with its
// images routed the way an MCP tool's images are.
func (r *Runtime) readMCPResourceHandler() elelem.ToolHandler {
	return func(
		ctx context.Context,
		call elelem.ToolInput,
	) (elelem.ToolResult, error) {
		ctx = contextWithParentToolCallID(ctx, call.CallID)

		var input readMCPResourceInput
		if err := decodeToolArguments(call.Arguments, &input); err != nil {
			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		asked := r.mcpElicitations.Load()

		contents, err := r.mcp.ReadResource(ctx, input.Server, input.URI)
		if err != nil {
			if isTurnEndingError(err) {
				return elelem.ToolResult{}, ctxerrors.Wrap(err,
					"read MCP resource")
			}

			return elelem.NewToolErrorResult(toolErrorMessage(err)), nil
		}

		return r.mcpShowResult(ctx, toolNameReadMCPResource,
			mcp.ResourceResult(contents), asked)
	}
}
