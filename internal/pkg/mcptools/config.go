package mcptools

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
)

const configOnlySecretsMessage = "envFrom and bearerTokenEnv are only " +
	"allowed in the config directory"

// serverNamePattern is a camelCase server name: a lowercase letter, then
// letters and digits.
var serverNamePattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

// Server is one validated MCP server entry.
type Server struct {
	Name      string
	Source    Source
	Transport Transport

	Command string
	Args    []string
	Env     map[string]string
	// EnvFrom maps a variable the server receives to the worker variable
	// that holds its value, so a secret never sits in mcp.json.
	EnvFrom map[string]string
	// Directory is where a stdio server runs. It is absolute.
	Directory string

	URL     string
	Headers map[string]string
	// BearerTokenEnv names the worker variable sent as
	// "Authorization: Bearer <value>".
	BearerTokenEnv string

	// Tools limits the server's tools to these names. Empty keeps them all.
	Tools []string
}

// LoadOptions says where to read mcp.json and what it may use.
type LoadOptions struct {
	// ConfigDirectory is PEEN_CONFIG_DIR. Its .agents/mcp.json is trusted.
	ConfigDirectory string
	// Workspace is the session workspace. Its .agents/mcp.json is read only
	// when WorkspaceEnabled is set, and a stdio server runs there.
	Workspace        string
	WorkspaceEnabled bool
	// SecretEnv lists the variables envFrom and bearerTokenEnv may name.
	SecretEnv []string
}

type serverDocument struct {
	Servers map[string]json.RawMessage `json:"mcpServers"`
}

type rawServer struct {
	Type           string            `json:"type"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	EnvFrom        map[string]string `json:"envFrom"`
	Cwd            string            `json:"cwd"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	BearerTokenEnv string            `json:"bearerTokenEnv"`
	Tools          []string          `json:"tools"`
}

// Load reads the config directory's mcp.json and, when allowed, the
// workspace's. A file or entry that fails validation does not stop the
// others: it comes back as a failed status that names the problem.
func Load(options LoadOptions) ([]Server, []Status) {
	servers, failures := loadFile(options, SourceConfig,
		options.ConfigDirectory)

	if options.WorkspaceEnabled && options.Workspace != "" {
		servers, failures = mergeWorkspaceServers(options, servers, failures)
	}

	if len(servers) > maxServers {
		for _, server := range servers[maxServers:] {
			failures = append(failures, failedStatus(server.Name,
				server.Source, server.Transport, ctxerrors.Wrapf(
					ErrInvalidConfig, "more than %d servers", maxServers,
				)))
		}

		servers = servers[:maxServers]
	}

	return servers, failures
}

// mergeWorkspaceServers adds the workspace's servers. A workspace server
// never replaces one the config directory defines.
func mergeWorkspaceServers(
	options LoadOptions,
	servers []Server,
	failures []Status,
) ([]Server, []Status) {
	workspaceServers, workspaceFailures := loadFile(options,
		SourceWorkspace, options.Workspace)
	failures = append(failures, workspaceFailures...)

	for _, server := range workspaceServers {
		if slices.ContainsFunc(servers, func(existing Server) bool {
			return existing.Name == server.Name
		}) {
			failures = append(failures, failedStatus(server.Name,
				server.Source, server.Transport, ctxerrors.Wrap(
					ErrInvalidConfig,
					"the config directory already defines this server",
				)))

			continue
		}

		servers = append(servers, server)
	}

	return servers, failures
}

func loadFile(
	options LoadOptions,
	source Source,
	layer string,
) ([]Server, []Status) {
	if layer == "" {
		return nil, nil
	}

	content, found, err := readConfigFile(
		filepath.Join(layer, agentsDirectoryName, FileName),
	)
	if err != nil {
		return nil, []Status{failedStatus("", source, "", err)}
	}

	if !found {
		return nil, nil
	}

	entries, err := decodeDocument(content)
	if err != nil {
		return nil, []Status{failedStatus("", source, "", err)}
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}

	slices.Sort(names)

	servers := make([]Server, 0, len(names))
	failures := make([]Status, 0)

	for _, name := range names {
		server, err := parseServer(options, source, name, entries[name])
		if err != nil {
			failures = append(failures, failedStatus(name, source,
				server.Transport, err))

			continue
		}

		servers = append(servers, server)
	}

	return servers, failures
}

func readConfigFile(path string) ([]byte, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, ctxerrors.Wrapf(errors.Join(ErrInvalidConfig, err),
			"open %s", FileName)
	}

	content, readErr := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	closeErr := file.Close()

	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, false, ctxerrors.Wrapf(errors.Join(ErrInvalidConfig, err),
			"read %s", FileName)
	}

	if len(content) > maxConfigBytes {
		return nil, false, ctxerrors.Wrapf(ErrInvalidConfig,
			"%s is larger than %d bytes", FileName, maxConfigBytes)
	}

	return content, true, nil
}

func decodeDocument(content []byte) (map[string]json.RawMessage, error) {
	var document serverDocument
	if err := decodeStrict(content, &document); err != nil {
		return nil, ctxerrors.Wrapf(errors.Join(ErrInvalidConfig, err),
			"decode %s", FileName)
	}

	return document.Servers, nil
}

func decodeStrict(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		return ctxerrors.Wrap(err, "decode JSON")
	}

	if decoder.More() {
		return ctxerrors.Wrap(ErrInvalidConfig, "trailing data after JSON")
	}

	return nil
}

func parseServer(
	options LoadOptions,
	source Source,
	name string,
	content json.RawMessage,
) (Server, error) {
	var raw rawServer
	if err := decodeStrict(content, &raw); err != nil {
		return Server{}, ctxerrors.Wrap(errors.Join(ErrInvalidConfig, err),
			"decode server")
	}

	server := Server{
		Name: name, Source: source,
		Transport: serverTransport(raw),
		Tools:     slices.Clone(raw.Tools),
	}

	if err := validateServerName(name); err != nil {
		return server, err
	}

	switch server.Transport {
	case TransportStdio:
		return parseStdioServer(options, server, raw)
	case TransportHTTP:
		return parseHTTPServer(options, server, raw)
	default:
		return server, ctxerrors.Wrapf(ErrInvalidConfig,
			"type %q is not supported, use %q or %q",
			raw.Type, TransportStdio, TransportHTTP)
	}
}

// serverTransport is the declared type, or the one the fields imply.
func serverTransport(raw rawServer) Transport {
	if raw.Type != "" {
		return raw.Type
	}

	if raw.URL != "" {
		return TransportHTTP
	}

	return TransportStdio
}

func validateServerName(name string) error {
	if len(name) > maxServerNameLength || !serverNamePattern.MatchString(name) {
		return ctxerrors.Wrapf(ErrInvalidConfig,
			"server name must be camelCase and at most %d characters",
			maxServerNameLength)
	}

	return nil
}

func parseStdioServer(
	options LoadOptions,
	server Server,
	raw rawServer,
) (Server, error) {
	if strings.TrimSpace(raw.Command) == "" {
		return server, ctxerrors.Wrap(ErrInvalidConfig,
			"a stdio server needs a command")
	}

	if raw.URL != "" || len(raw.Headers) > 0 || raw.BearerTokenEnv != "" {
		return server, ctxerrors.Wrap(ErrInvalidConfig,
			"url, headers, and bearerTokenEnv are for http servers")
	}

	if err := validateEnvironmentNames(raw.Env); err != nil {
		return server, err
	}

	if err := validateSecretReferences(options, server.Source,
		raw.EnvFrom, ""); err != nil {
		return server, err
	}

	server.Command = raw.Command
	server.Args = slices.Clone(raw.Args)
	server.Env = raw.Env
	server.EnvFrom = raw.EnvFrom
	server.Directory = options.Workspace

	if raw.Cwd != "" {
		server.Directory = raw.Cwd
		if !filepath.IsAbs(raw.Cwd) {
			server.Directory = filepath.Join(options.Workspace, raw.Cwd)
		}
	}

	return server, nil
}

func parseHTTPServer(
	options LoadOptions,
	server Server,
	raw rawServer,
) (Server, error) {
	if !strings.HasPrefix(raw.URL, httpsSchemePrefix) &&
		!strings.HasPrefix(raw.URL, httpSchemePrefix) {
		return server, ctxerrors.Wrap(ErrInvalidConfig,
			"an http server needs an http or https url")
	}

	if hasStdioFields(raw) {
		return server, ctxerrors.Wrap(ErrInvalidConfig,
			"command, args, env, envFrom, and cwd are for stdio servers")
	}

	if hasAuthorizationHeader(raw.Headers) {
		return server, ctxerrors.Wrap(ErrInvalidConfig,
			"set the token with bearerTokenEnv, not an Authorization header")
	}

	if err := validateSecretReferences(options, server.Source, nil,
		raw.BearerTokenEnv); err != nil {
		return server, err
	}

	server.URL = raw.URL
	server.Headers = raw.Headers
	server.BearerTokenEnv = raw.BearerTokenEnv

	return server, nil
}

func hasStdioFields(raw rawServer) bool {
	return raw.Command != "" || len(raw.Args) > 0 || len(raw.Env) > 0 ||
		len(raw.EnvFrom) > 0 || raw.Cwd != ""
}

func hasAuthorizationHeader(headers map[string]string) bool {
	for header := range headers {
		if strings.EqualFold(header, aichteeteapee.HeaderNameAuthorization) {
			return true
		}
	}

	return false
}

func validateEnvironmentNames(environment map[string]string) error {
	for name := range environment {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return ctxerrors.Wrapf(ErrInvalidConfig,
				"env has an invalid variable name %q", name)
		}
	}

	return nil
}

// validateSecretReferences allows envFrom and bearerTokenEnv only in the
// config directory and only for variables the operator lists in
// PEEN_MCP_ENV. The agent can write the workspace's mcp.json, so a workspace
// server that could name a variable could send any secret to itself.
func validateSecretReferences(
	options LoadOptions,
	source Source,
	envFrom map[string]string,
	bearerTokenEnv string,
) error {
	if len(envFrom) == 0 && bearerTokenEnv == "" {
		return nil
	}

	if source != SourceConfig {
		return ctxerrors.Wrap(ErrInvalidConfig, configOnlySecretsMessage)
	}

	if err := validateEnvironmentNames(envFrom); err != nil {
		return err
	}

	referenced := make([]string, 0, len(envFrom)+1)
	for _, variable := range envFrom {
		referenced = append(referenced, variable)
	}

	if bearerTokenEnv != "" {
		referenced = append(referenced, bearerTokenEnv)
	}

	for _, variable := range referenced {
		if !slices.Contains(options.SecretEnv, variable) {
			return ctxerrors.Wrapf(ErrInvalidConfig,
				"variable %q is not listed in PEEN_MCP_ENV", variable)
		}
	}

	return nil
}
