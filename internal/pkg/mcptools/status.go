package mcptools

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/psyb0t/elelem/mcp"
)

// errorLocationMarker starts the source locations ctxerrors appends, which
// a status message leaves out.
const errorLocationMarker = " ["

// Status is what a session's worker knows about one configured server.
type Status struct {
	// Name is the server name. It is empty when a whole mcp.json file failed.
	Name      string    `json:"name"`
	Source    Source    `json:"source"`
	Transport Transport `json:"transport,omitempty"`
	State     State     `json:"state"`
	// Tools lists the exposed names of the server's tools.
	Tools []string `json:"tools"`
	// Error says why the server failed.
	Error string `json:"error,omitempty"`
	// Warnings lists problems that did not stop the server, such as a tool
	// in the allowlist the server does not offer.
	Warnings []string `json:"warnings,omitempty"`

	// ProtocolVersion is the MCP version the connection speaks.
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	// ServerInfo is what the server says it is.
	ServerInfo *ServerInfo `json:"serverInfo,omitempty"`
	// Features lists the capabilities the server advertised.
	Features []Feature `json:"features,omitempty"`
	// InstructionsBytes is the size of the instructions the server sent,
	// after the PEEN_MCP_MAX_INSTRUCTIONS_BYTES cut. InstructionsIncluded
	// says they reach the system prompt, and InstructionsCut that they were
	// longer than the limit.
	InstructionsBytes    int  `json:"instructionsBytes,omitempty"`
	InstructionsIncluded bool `json:"instructionsIncluded,omitempty"`
	InstructionsCut      bool `json:"instructionsCut,omitempty"`
	// Prompts lists the prompts the user can invoke, at most
	// maxStatusPrompts.
	Prompts []PromptInfo `json:"prompts,omitempty"`
}

// ServerInfo is a server's own name, title, and version, each cut to
// maxServerInfoBytes.
type ServerInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// PromptInfo is one prompt a server offers, with its texts cut to
// maxPromptTextBytes.
type PromptInfo struct {
	Name        string               `json:"name"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description,omitempty"`
	Arguments   []PromptArgumentInfo `json:"arguments"`
}

// PromptArgumentInfo is one argument a prompt takes.
type PromptArgumentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
}

func failedStatus(
	name string,
	source Source,
	transport Transport,
	err error,
) Status {
	return Status{
		Name: name, Source: source, Transport: transport,
		State: StateFailed, Tools: []string{}, Error: errorMessage(err),
	}
}

func (s Status) clone() Status {
	s.Tools = slices.Clone(s.Tools)
	s.Warnings = slices.Clone(s.Warnings)
	s.Features = slices.Clone(s.Features)
	s.Prompts = slices.Clone(s.Prompts)

	return s
}

// errorMessage is err's text without the source locations.
func errorMessage(err error) string {
	message := err.Error()
	if cut := strings.Index(message, errorLocationMarker); cut > 0 {
		message = message[:cut]
	}

	return strings.TrimSpace(message)
}

// readyStatus describes a running server from what it said when it
// connected. includeInstructions says whether PEEN_MCP_MAX_INSTRUCTIONS_BYTES
// lets any instructions into the prompt.
func (s *serverState) readyStatus(includeInstructions bool) Status {
	config := s.config
	info := s.connection.Info()

	tools := s.connection.Tools()
	names := make([]string, 0, len(tools))

	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	status := Status{
		Name: config.Name, Source: config.Source,
		Transport: config.Transport, State: StateReady,
		Tools: names, Warnings: s.connection.Warnings(),

		ProtocolVersion:   cutText(info.ProtocolVersion, maxServerInfoBytes),
		Features:          features(info.Capabilities),
		InstructionsBytes: len(info.Instructions),
		InstructionsCut:   info.InstructionsCut,
		InstructionsIncluded: includeInstructions && config.Instructions &&
			info.Instructions != "",
		Prompts: slices.Clone(s.prompts),
	}

	if info.Name != "" {
		status.ServerInfo = &ServerInfo{
			Name:    cutText(info.Name, maxServerInfoBytes),
			Title:   cutText(info.Title, maxServerInfoBytes),
			Version: cutText(info.Version, maxServerInfoBytes),
		}
	}

	return status
}

// features lists what a server advertised, in a fixed order.
func features(capabilities mcp.Capabilities) []Feature {
	listed := make([]Feature, 0)

	for _, feature := range []struct {
		name    Feature
		offered bool
	}{
		{FeatureTools, capabilities.Tools},
		{FeatureResources, capabilities.Resources},
		{FeatureResourceSubscribe, capabilities.ResourceSubscribe},
		{FeaturePrompts, capabilities.Prompts},
		{FeatureCompletions, capabilities.Completions},
	} {
		if feature.offered {
			listed = append(listed, feature.name)
		}
	}

	return listed
}

// promptInfos converts the first prompts a server listed for a status.
func promptInfos(prompts []mcp.Prompt) []PromptInfo {
	infos := make([]PromptInfo, 0, min(len(prompts), maxStatusPrompts))

	for _, prompt := range prompts[:min(len(prompts), maxStatusPrompts)] {
		arguments := make([]PromptArgumentInfo, 0, len(prompt.Arguments))
		for _, argument := range prompt.Arguments {
			arguments = append(arguments, PromptArgumentInfo{
				Name:        argument.Name,
				Description: cutText(argument.Description, maxPromptTextBytes),
				Required:    argument.Required,
			})
		}

		infos = append(infos, PromptInfo{
			Name:        prompt.Name,
			Title:       cutText(prompt.Title, maxPromptTextBytes),
			Description: cutText(prompt.Description, maxPromptTextBytes),
			Arguments:   arguments,
		})
	}

	return infos
}

// cutText is text cut to at most maxBytes without splitting a character.
func cutText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}

	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}

	return text[:end]
}
