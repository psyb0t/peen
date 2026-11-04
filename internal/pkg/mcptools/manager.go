package mcptools

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/mcp"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

// Options builds a Manager.
type Options struct {
	Servers []Server
	// Failures are the entries Load refused. They are reported in Status.
	Failures []Status
	// BaseEnvironment is the environment every stdio server starts with,
	// before its env and envFrom. It is the agent command environment, so
	// it holds no Peen secret.
	BaseEnvironment []string
	// LookupEnv reads the worker variables envFrom and bearerTokenEnv name.
	LookupEnv func(string) (string, bool)
	// StartTimeout bounds starting one server and listing its tools.
	StartTimeout time.Duration
	// HTTPTransport carries Streamable HTTP requests. Nil takes
	// http.DefaultTransport.
	HTTPTransport http.RoundTripper
	// Reload reads the configuration again, for Refresh. Nil keeps the
	// configuration the Manager was built with.
	Reload func() ([]Server, []Status)
}

// Manager owns a worker's MCP server connections. Start connects them,
// Status starts them if nothing has, and Close stops them.
type Manager struct {
	options Options

	startOnce sync.Once
	// started closes when Start has connected every server.
	started   chan struct{}
	requested atomic.Bool

	mutex   sync.RWMutex
	servers []*serverState
	closed  bool
}

type serverState struct {
	config     Server
	connection *mcp.Connection
	// failure says why the server did not start. Nil when it did.
	failure error
}

// New returns a Manager for options. It starts nothing.
func New(options Options) *Manager {
	if options.StartTimeout <= 0 {
		options.StartTimeout = DefaultStartTimeout
	}

	if options.LookupEnv == nil {
		options.LookupEnv = func(string) (string, bool) { return "", false }
	}

	return &Manager{options: options, started: make(chan struct{})}
}

// Configured reports whether any server or refused entry exists, so a
// caller can skip MCP work for a session that configures none.
func (m *Manager) Configured() bool {
	if m == nil {
		return false
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	return len(m.options.Servers) > 0 || len(m.options.Failures) > 0
}

// Tools returns the tools of every server still connected, in
// configuration order. It waits for a Start in progress. Before any Start
// it returns none.
func (m *Manager) Tools() []elelem.Tool {
	if m == nil || !m.requested.Load() {
		return nil
	}

	<-m.started

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	tools := make([]elelem.Tool, 0)

	for _, server := range m.servers {
		if server.connection == nil || server.connection.Err() != nil {
			continue
		}

		tools = append(tools, server.connection.Tools()...)
	}

	return tools
}

// Status starts the servers once and reports every configured server and
// every refused entry.
func (m *Manager) Status(ctx context.Context) []Status {
	if m == nil {
		return nil
	}

	m.Start(ctx)

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	statuses := make([]Status, 0, len(m.servers)+len(m.options.Failures))
	for _, server := range m.servers {
		statuses = append(statuses, server.status())
	}

	for _, failure := range m.options.Failures {
		statuses = append(statuses, failure.clone())
	}

	return statuses
}

func (s *serverState) status() Status {
	config := s.config

	if s.failure != nil {
		return failedStatus(config.Name, config.Source, config.Transport,
			s.failure)
	}

	if err := s.connection.Err(); err != nil {
		return failedStatus(config.Name, config.Source, config.Transport, err)
	}

	tools := s.connection.Tools()
	names := make([]string, 0, len(tools))

	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	return Status{
		Name: config.Name, Source: config.Source,
		Transport: config.Transport, State: StateReady,
		Tools: names, Warnings: s.connection.Warnings(),
	}
}

// Close stops every server. It is safe to call more than once.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}

	m.mutex.Lock()
	if m.closed {
		m.mutex.Unlock()

		return nil
	}

	m.closed = true
	servers := slices.Clone(m.servers)
	m.mutex.Unlock()

	return closeServers(servers)
}

// closeServers stops every connected server in servers, in parallel.
func closeServers(servers []*serverState) error {
	errs := make([]error, len(servers))

	var group sync.WaitGroup

	for index, server := range servers {
		if server.connection == nil {
			continue
		}

		group.Go(func() {
			errs[index] = server.connection.Close()
		})
	}

	group.Wait()

	if err := errors.Join(errs...); err != nil {
		return ctxerrors.Wrap(err, "close MCP servers")
	}

	return nil
}

// connectAll starts every server in servers, in parallel, in configuration
// order.
func (m *Manager) connectAll(
	ctx context.Context,
	servers []Server,
) []*serverState {
	states := make([]*serverState, len(servers))

	var group sync.WaitGroup

	for index, server := range servers {
		group.Go(func() {
			states[index] = m.connect(ctx, server)
		})
	}

	group.Wait()

	return states
}

// Start connects every server, once. Later calls wait for the first to
// finish. ctx bounds the starts, not the connections, which live until
// Close.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}

	m.requested.Store(true)

	m.startOnce.Do(func() {
		defer close(m.started)

		m.mutex.Lock()
		defer m.mutex.Unlock()

		if m.closed {
			return
		}

		m.servers = m.connectAll(ctx, m.options.Servers)
	})
}

func (m *Manager) connect(
	ctx context.Context,
	server Server,
) *serverState {
	logger := runtimecontext.Logger(ctx)
	state := &serverState{config: server}

	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error("MCP server start panicked",
				"server", server.Name,
				"panic", recovered,
				"stack", string(debug.Stack()),
			)

			state.failure = ctxerrors.Wrap(ErrServerUnavailable,
				"start panicked")
		}
	}()

	target, err := m.target(server)
	if err != nil {
		state.failure = err

		return state
	}

	startCtx, cancel := context.WithTimeout(ctx, m.options.StartTimeout)
	defer cancel()

	connection, err := mcp.Connect(startCtx, target)
	if err != nil {
		logger.Warn("MCP server failed to start",
			"server", server.Name,
			"transport", server.Transport,
			"reason", "start_failed",
			"err", err,
		)

		state.failure = err

		return state
	}

	logger.Info("MCP server started",
		"server", server.Name,
		"transport", server.Transport,
		"tool_count", len(connection.Tools()),
	)

	state.connection = connection

	return state
}

// target is the elelem server for a configured one, with its secrets read
// from the worker's environment.
func (m *Manager) target(server Server) (mcp.Server, error) {
	target := mcp.Server{
		Name: server.Name, Tools: server.Tools, ClientName: clientName,
	}

	if server.Transport == TransportHTTP {
		target.URL = server.URL
		target.Headers = server.Headers
		target.HTTPTransport = m.options.HTTPTransport

		if server.BearerTokenEnv != "" {
			token, err := m.secret(server.BearerTokenEnv)
			if err != nil {
				return mcp.Server{}, err
			}

			target.BearerToken = token
		}

		return target, nil
	}

	environment := slices.Clone(m.options.BaseEnvironment)
	for name, value := range server.Env {
		environment = append(environment, name+"="+value)
	}

	for name, variable := range server.EnvFrom {
		value, err := m.secret(variable)
		if err != nil {
			return mcp.Server{}, err
		}

		environment = append(environment, name+"="+value)
	}

	target.Command = server.Command
	target.Args = server.Args
	target.Env = environment
	target.Dir = server.Directory

	return target, nil
}

// secret reads one worker variable. The error names the variable, never
// its value.
func (m *Manager) secret(variable string) (string, error) {
	value, found := m.options.LookupEnv(variable)
	if !found || value == "" {
		return "", ctxerrors.Wrapf(ErrInvalidConfig,
			"variable %q is empty", variable)
	}

	return value, nil
}
