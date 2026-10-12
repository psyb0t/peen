package peen

type HarnessHookInputMatch struct {
	Exists *bool  `json:"exists,omitempty" yaml:"exists"`
	Equals any    `json:"equals,omitempty" yaml:"equals"`
	Regex  string `json:"regex,omitempty"  yaml:"regex"`
}

// HarnessHookMatch selects hook actions by their structured invocation context.
// Every populated field is required to match.
//
//nolint:tagalign // Preserve the YAML-first tag convention.
type HarnessHookMatch struct {
	Tool       string                           `json:"tool,omitempty"`
	Path       string                           `json:"path,omitempty"`
	Extensions []string                         `yaml:"extensions" json:"extensions,omitempty"` //nolint:lll // Immutable schema tags.
	Root       string                           `json:"root,omitempty"`
	Input      map[string]HarnessHookInputMatch `json:"input,omitempty"`
}

// HarnessHookAction is one ordered operation in a hook group. Command is an
// executable path or name, never shell source; Args are passed verbatim.
// Timeout is a Go duration string such as "30s" bounding one command action;
// empty takes the runner's default.
//
//nolint:tagalign // Preserve the YAML-first tag convention.
type HarnessHookAction struct {
	Name        string            `yaml:"name" json:"name,omitempty"`
	Type        string            `yaml:"type" json:"type"`
	When        HarnessHookMatch  `yaml:"when" json:"when,omitzero"`
	Command     string            `yaml:"command" json:"command,omitempty"`
	Args        []string          `yaml:"args" json:"args,omitempty"`
	Environment map[string]string `yaml:"environment" json:"environment,omitempty"` //nolint:lll // Immutable schema tags.
	WorkingDir  string            `yaml:"workingDir" json:"workingDir,omitempty"`   //nolint:lll // Immutable schema tags.
	Timeout     string            `yaml:"timeout" json:"timeout,omitempty"`
	Message     string            `yaml:"message" json:"message,omitempty"`
	EventType   string            `yaml:"eventType" json:"eventType,omitempty"`
	Summary     string            `yaml:"summary" json:"summary,omitempty"`
	Data        map[string]any    `yaml:"data" json:"data,omitempty"`
	Delivery    string            `yaml:"delivery" json:"delivery,omitempty"`
	Reason      string            `yaml:"reason" json:"reason,omitempty"`
	OnFailure   string            `yaml:"onFailure" json:"onFailure,omitempty"`
}

// Hook is one ordered matcher group from a layered hooks.yaml document.
// ConfigLayer is true only for PEEN_CONFIG_DIR's hook document.
type HarnessHook struct {
	Name        string              `json:"name"`
	Event       string              `json:"event"`
	Match       HarnessHookMatch    `json:"match,omitzero"`
	Actions     []HarnessHookAction `json:"actions"`
	Source      string              `json:"source"`
	Priority    int                 `json:"priority"`
	ConfigLayer bool                `json:"configLayer"`
	Hash        string              `json:"hash"`
}
