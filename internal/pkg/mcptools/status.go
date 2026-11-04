package mcptools

import (
	"slices"
	"strings"
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
