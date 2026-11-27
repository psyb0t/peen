package mcptools

import (
	"context"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem/mcp"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

// Elicitor asks the user a server's question and returns the answer. server
// is the name of the server that asked. It runs while the request that needed
// the answer waits.
type Elicitor func(
	ctx context.Context,
	server string,
	request mcp.ElicitRequest,
) (mcp.ElicitResult, error)

// ServerInstructions is one ready server's instructions for the system
// prompt: at most Limit bytes of them, with Cut set when they were longer.
type ServerInstructions struct {
	Server string
	Source Source
	Text   string
	Cut    bool
	Limit  int
}

// SetElicitor routes every server's questions to elicitor. Nil declines
// them.
func (m *Manager) SetElicitor(elicitor Elicitor) {
	if m == nil {
		return
	}

	m.elicitorMutex.Lock()
	defer m.elicitorMutex.Unlock()

	m.elicitor = elicitor
}

// elicitHandler answers one server's questions through the Elicitor the
// runtime set, or declines them when none is set.
func (m *Manager) elicitHandler(server string) mcp.ElicitHandler {
	return func(
		ctx context.Context,
		request mcp.ElicitRequest,
	) (mcp.ElicitResult, error) {
		m.elicitorMutex.RLock()
		elicitor := m.elicitor
		m.elicitorMutex.RUnlock()

		if elicitor == nil {
			runtimecontext.Logger(ctx).Info("MCP elicitation declined",
				"server", server, "reason", "no_elicitor")

			return mcp.ElicitResult{Action: mcp.ElicitActionDecline}, nil
		}

		return elicitor(ctx, server, request)
	}
}

// listPrompts records the server's first prompts for its status. A server
// that offers none, or whose prompts are turned off, keeps an empty list. A
// failure keeps the previous list.
func (s *serverState) listPrompts(ctx context.Context, timeout time.Duration) {
	if !s.config.Prompts || !s.connection.Info().Capabilities.Prompts {
		s.prompts = nil

		return
	}

	listCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	page, err := s.connection.ListPrompts(listCtx, "")
	if err != nil {
		runtimecontext.Logger(ctx).Warn(
			"MCP server prompts could not be listed",
			"server", s.config.Name,
			"reason", "list_prompts_failed",
			"err", err,
		)

		return
	}

	s.prompts = promptInfos(page.Prompts)
}

// readyServers returns the running servers in configuration order. It waits
// for a Start in progress, and returns none before any Start.
func (m *Manager) readyServers() []*serverState {
	if m == nil || !m.requested.Load() {
		return nil
	}

	<-m.started

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	ready := make([]*serverState, 0, len(m.servers))

	for _, server := range m.servers {
		if server.connection == nil || server.connection.Err() != nil {
			continue
		}

		ready = append(ready, server)
	}

	return ready
}

// Instructions returns the instructions of every running server that sent
// some and whose mcp.json entry leaves instructions on, in configuration
// order. With MaxInstructionsBytes zero it returns none.
func (m *Manager) Instructions() []ServerInstructions {
	if m == nil || m.options.MaxInstructionsBytes <= 0 {
		return nil
	}

	instructions := make([]ServerInstructions, 0)

	for _, server := range m.readyServers() {
		info := server.connection.Info()
		if !server.config.Instructions || info.Instructions == "" {
			continue
		}

		instructions = append(instructions, ServerInstructions{
			Server: server.config.Name, Source: server.config.Source,
			Text: info.Instructions, Cut: info.InstructionsCut,
			Limit: m.options.MaxInstructionsBytes,
		})
	}

	return instructions
}

// ResourceServers names the running servers that offer resources and whose
// mcp.json entry leaves resources on, in configuration order.
func (m *Manager) ResourceServers() []string {
	return m.serversWith(FeatureResources)
}

// PromptServers names the running servers that offer prompts and whose
// mcp.json entry leaves prompts on, in configuration order.
func (m *Manager) PromptServers() []string {
	return m.serversWith(FeaturePrompts)
}

func (m *Manager) serversWith(feature Feature) []string {
	names := make([]string, 0)

	for _, server := range m.readyServers() {
		if server.offers(feature) {
			names = append(names, server.config.Name)
		}
	}

	return names
}

// offers reports whether the server advertised feature and its mcp.json
// entry leaves it on.
func (s *serverState) offers(feature Feature) bool {
	capabilities := s.connection.Info().Capabilities

	switch feature {
	case FeatureResources:
		return s.config.Resources && capabilities.Resources
	case FeaturePrompts:
		return s.config.Prompts && capabilities.Prompts
	default:
		return false
	}
}

// featureConnection is the running server named name, when it offers
// feature.
func (m *Manager) featureConnection(
	ctx context.Context,
	name string,
	feature Feature,
) (*mcp.Connection, error) {
	if m == nil {
		return nil, ctxerrors.Wrap(ErrServerUnavailable, "MCP is off")
	}

	m.Start(ctx)

	for _, server := range m.readyServers() {
		if server.config.Name != name {
			continue
		}

		if !server.offers(feature) {
			return nil, ctxerrors.Wrapf(mcp.ErrNotSupported,
				"MCP server %q offers no %s, or its mcp.json entry turns "+
					"them off", name, feature)
		}

		return server.connection, nil
	}

	return nil, ctxerrors.Wrapf(ErrServerUnavailable,
		"no running MCP server is named %q", name)
}

// ListResources lists one page of a server's resources.
func (m *Manager) ListResources(
	ctx context.Context,
	server, cursor string,
) (mcp.ResourcePage, error) {
	connection, err := m.featureConnection(ctx, server, FeatureResources)
	if err != nil {
		return mcp.ResourcePage{}, err
	}

	page, err := connection.ListResources(ctx, cursor)
	if err != nil {
		return mcp.ResourcePage{}, ctxerrors.Wrapf(err,
			"list the resources of MCP server %q", server)
	}

	return page, nil
}

// ListResourceTemplates lists one page of a server's resource templates.
func (m *Manager) ListResourceTemplates(
	ctx context.Context,
	server, cursor string,
) (mcp.ResourceTemplatePage, error) {
	connection, err := m.featureConnection(ctx, server, FeatureResources)
	if err != nil {
		return mcp.ResourceTemplatePage{}, err
	}

	page, err := connection.ListResourceTemplates(ctx, cursor)
	if err != nil {
		return mcp.ResourceTemplatePage{}, ctxerrors.Wrapf(err,
			"list the resource templates of MCP server %q", server)
	}

	return page, nil
}

// ReadResource reads one resource from a server.
func (m *Manager) ReadResource(
	ctx context.Context,
	server, uri string,
) ([]mcp.ResourceContents, error) {
	connection, err := m.featureConnection(ctx, server, FeatureResources)
	if err != nil {
		return nil, err
	}

	contents, err := connection.ReadResource(ctx, uri)
	if err != nil {
		return nil, ctxerrors.Wrapf(err,
			"read resource %q from MCP server %q", uri, server)
	}

	return contents, nil
}

// ListPrompts lists one page of a server's prompts.
func (m *Manager) ListPrompts(
	ctx context.Context,
	server, cursor string,
) (mcp.PromptPage, error) {
	connection, err := m.featureConnection(ctx, server, FeaturePrompts)
	if err != nil {
		return mcp.PromptPage{}, err
	}

	page, err := connection.ListPrompts(ctx, cursor)
	if err != nil {
		return mcp.PromptPage{}, ctxerrors.Wrapf(err,
			"list the prompts of MCP server %q", server)
	}

	return page, nil
}

// GetPrompt fills in one of a server's prompts.
func (m *Manager) GetPrompt(
	ctx context.Context,
	server, name string,
	arguments map[string]string,
) (mcp.PromptResult, error) {
	connection, err := m.featureConnection(ctx, server, FeaturePrompts)
	if err != nil {
		return mcp.PromptResult{}, err
	}

	result, err := connection.GetPrompt(ctx, name, arguments)
	if err != nil {
		return mcp.PromptResult{}, ctxerrors.Wrapf(err,
			"get prompt %q from MCP server %q", name, server)
	}

	return result, nil
}
