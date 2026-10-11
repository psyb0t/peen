// Package envtemplate renders Peen's commented .env file.
//
// One template is the source of both .env.example and the .env that
// `peen config init` writes, so the example a reader copies and the file an
// installer writes cannot drift apart.
package envtemplate

import (
	"bytes"
	_ "embed"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

// ControlSpace is where Peen itself runs. The peen command reads it, and the
// controller ignores it.
type ControlSpace = string

const (
	// ControlSpaceHost runs the Peen binary on the host.
	ControlSpaceHost ControlSpace = "host"
	// ControlSpaceDocker runs Peen in a container.
	ControlSpaceDocker ControlSpace = "docker"
)

const templateName = "env.tmpl"

// lineBreakCharacters are refused in every value. A value is written as one
// `KEY=value` line of a Docker --env-file, so a line break would let a value
// add a setting of its own.
const lineBreakCharacters = "\r\n"

//go:embed env.tmpl
var templateText string

// containerNamePattern is the name rule the Docker daemon applies to a
// container name. Refusing a bad name here fails the install instead of the
// first `docker run`.
var containerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// Values are the settings an installer knows before Peen first starts. An
// empty field renders the template's commented placeholder or default, so the
// zero Values renders .env.example.
type Values struct {
	ConfigDirectory   string
	StateDirectory    string
	WorkspaceRoot     string
	DefaultAgentSpace string
	WorkerImage       string
	HTTPListenAddress string
	HostUsername      string
	HostHome          string
	ControlSpace      ControlSpace
	Image             string
	ContainerName     string
}

// Validate refuses values that would render a file Peen or the peen command
// cannot use: a line break in any value, a relative path, an agent space that
// is neither control nor docker:<profile>, a control space other than host or
// docker, and a container name Docker would reject. Every refusal wraps
// commerr.ErrValidationFailed.
func (v Values) Validate() error {
	if err := v.validateLineBreaks(); err != nil {
		return err
	}

	if err := v.validatePaths(); err != nil {
		return err
	}

	if err := validateAgentSpace(v.DefaultAgentSpace); err != nil {
		return err
	}

	if err := validateControlSpace(v.ControlSpace); err != nil {
		return err
	}

	if v.ContainerName != "" &&
		!containerNamePattern.MatchString(v.ContainerName) {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"PEEN_CONTAINER_NAME %q must match %s",
			v.ContainerName,
			containerNamePattern,
		)
	}

	return nil
}

// Render validates values and returns the commented env file.
func Render(values Values) ([]byte, error) {
	if err := values.Validate(); err != nil {
		return nil, err
	}

	envTemplate, err := template.New(templateName).
		Option("missingkey=error").
		Parse(templateText)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "parse the env file template")
	}

	var rendered bytes.Buffer
	if err := envTemplate.Execute(&rendered, values); err != nil {
		return nil, ctxerrors.Wrap(err, "render the env file template")
	}

	return rendered.Bytes(), nil
}

func (v Values) settings() map[string]string {
	return map[string]string{
		"PEEN_CONFIG_DIR":          v.ConfigDirectory,
		"PEEN_STATE_DIR":           v.StateDirectory,
		"PEEN_WORKSPACE_ROOT":      v.WorkspaceRoot,
		"PEEN_DEFAULT_AGENT_SPACE": v.DefaultAgentSpace,
		"PEEN_WORKER_IMAGE":        v.WorkerImage,
		"PEEN_HTTP_LISTEN_ADDRESS": v.HTTPListenAddress,
		"PEEN_HOST_USERNAME":       v.HostUsername,
		"PEEN_HOST_HOME":           v.HostHome,
		"PEEN_CONTROL_SPACE":       v.ControlSpace,
		"PEEN_IMAGE":               v.Image,
		"PEEN_CONTAINER_NAME":      v.ContainerName,
	}
}

func (v Values) validateLineBreaks() error {
	for key, value := range v.settings() {
		if strings.ContainsAny(value, lineBreakCharacters) {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"%s must not contain a line break",
				key,
			)
		}
	}

	return nil
}

func (v Values) validatePaths() error {
	paths := map[string]string{
		"PEEN_CONFIG_DIR":     v.ConfigDirectory,
		"PEEN_STATE_DIR":      v.StateDirectory,
		"PEEN_WORKSPACE_ROOT": v.WorkspaceRoot,
		"PEEN_HOST_HOME":      v.HostHome,
	}

	for key, path := range paths {
		if path != "" && !filepath.IsAbs(path) {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"%s must be an absolute path, got %q",
				key,
				path,
			)
		}
	}

	return nil
}

// validateAgentSpace applies the controller's own agent space naming rule, so
// a file this package writes never names an agent space the controller would
// refuse by its shape. Whether the named profile exists is the controller's
// decision at startup.
func validateAgentSpace(agentSpace string) error {
	if agentSpace == "" || agentSpace == worker.AgentSpaceControl {
		return nil
	}

	if !strings.HasPrefix(agentSpace, worker.DockerSpacePrefix) {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"PEEN_DEFAULT_AGENT_SPACE must be %s or %s<profile>, got %q",
			worker.AgentSpaceControl,
			worker.DockerSpacePrefix,
			agentSpace,
		)
	}

	// The profile name rule lives on AgentSpace.Validate. The grants given
	// here are the built-in defaults, so only the name can fail.
	space := worker.AgentSpace{
		Name:    agentSpace,
		Kind:    worker.KindDocker,
		Docker:  worker.DockerAccessNone,
		Network: worker.NetworkModeBridge,
	}
	if err := space.Validate(); err != nil {
		return ctxerrors.Wrap(err, "validate PEEN_DEFAULT_AGENT_SPACE")
	}

	return nil
}

func validateControlSpace(controlSpace ControlSpace) error {
	switch controlSpace {
	case "", ControlSpaceHost, ControlSpaceDocker:
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"PEEN_CONTROL_SPACE must be %s or %s, got %q",
		ControlSpaceHost,
		ControlSpaceDocker,
		controlSpace,
	)
}
