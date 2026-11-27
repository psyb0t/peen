package agent

import (
	"strings"

	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/mcptools"
)

const (
	mcpInstructionsTemplateName = "mcpInstructions"
	//nolint:lll // One model-facing block.
	mcpInstructionsTemplateText = `<mcpInstructions server="{{attr .Server}}" source="{{attr .Source}}">
{{.Body}}{{if .Cut}}
<note kind="instructionsCut">The server's instructions were longer than {{.Limit}} bytes; this is their start.</note>{{end}}
</mcpInstructions>`

	// mcpPromptSectionKind names an MCP server's instructions when the
	// prompt budget lists the largest sections.
	mcpPromptSectionKind harness.SourceKind = "mcp server"
)

// mcpTextEscaper keeps server-written text from opening or closing a block
// Peen wraps it in, or from posing as a Peen note.
//
//nolint:gochecknoglobals // Stateless replacer.
var mcpTextEscaper = strings.NewReplacer(
	"<mcpInstructions", "&lt;mcpInstructions",
	"</mcpInstructions", "&lt;/mcpInstructions",
	"<mcpPrompt", "&lt;mcpPrompt",
	"</mcpPrompt", "&lt;/mcpPrompt",
	"<promptMessage", "&lt;promptMessage",
	"</promptMessage", "&lt;/promptMessage",
	"<note", "&lt;note",
	"</note", "&lt;/note",
)

// mcpInstructionsData is what mcpInstructionsTemplateText renders.
type mcpInstructionsData struct {
	Server string
	Source string
	Body   string
	Cut    bool
	Limit  int
}

// mcpInstructionBlocks renders the instructions of every running MCP server
// that sent some, in configuration order, as system prompt sections. The
// Manager has already cut each to PEEN_MCP_MAX_INSTRUCTIONS_BYTES and left
// out the servers whose mcp.json entry turns them off.
func (r *Runtime) mcpInstructionBlocks() ([]harness.PromptBlock, error) {
	instructions := r.mcp.Instructions()
	blocks := make([]harness.PromptBlock, 0, len(instructions))

	for _, server := range instructions {
		content, err := renderMCPInstructions(server)
		if err != nil {
			return nil, err
		}

		blocks = append(blocks, harness.PromptBlock{
			Kind: mcpPromptSectionKind, Name: server.Server,
			Content: content,
		})
	}

	return blocks, nil
}

// renderMCPInstructions is one server's <mcpInstructions> block.
func renderMCPInstructions(
	server mcptools.ServerInstructions,
) (string, error) {
	return renderTemplate(mcpInstructionsTemplateName,
		mcpInstructionsTemplateText, mcpInstructionsData{
			Server: server.Server, Source: server.Source,
			Body:  mcpTextEscaper.Replace(server.Text),
			Cut:   server.Cut,
			Limit: server.Limit,
		})
}

// promptBlockContents is the content of each block, in order.
func promptBlockContents(blocks []harness.PromptBlock) []string {
	contents := make([]string, 0, len(blocks))
	for _, block := range blocks {
		contents = append(contents, block.Content)
	}

	return contents
}
