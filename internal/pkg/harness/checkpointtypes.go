package harness

type checkpointInstruction struct {
	Kind     SourceKind `json:"kind"`
	Source   string     `json:"source"`
	Priority int        `json:"priority"`
	Content  string     `json:"content"`
	Hash     string     `json:"hash"`
	Scope    string     `json:"scope"`
}

type checkpointSkill struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Source        string         `json:"source"`
	Directory     string         `json:"directory"`
	Hash          string         `json:"hash"`
	Metadata      map[string]any `json:"metadata"`
	Homepage      string         `json:"homepage"`
	Permissions   map[string]any `json:"permissions"`
	UserInvocable bool           `json:"userInvocable"`
	License       string         `json:"license"`
	Compatibility string         `json:"compatibility"`
	AllowedTools  string         `json:"allowedTools"`
}

type checkpointAgent struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       string   `json:"source"`
	Instructions string   `json:"instructions"`
	AllowedTools []string `json:"allowedTools"`
	Hash         string   `json:"hash"`
}

type checkpointEventHandler struct {
	Type         string `json:"type"`
	Agent        string `json:"agent"`
	Delivery     string `json:"delivery"`
	Source       string `json:"source"`
	Instructions string `json:"instructions"`
	Hash         string `json:"hash"`
}

func mapCheckpointValues[T, R any](values []T, convert func(T) R) []R {
	if values == nil {
		return nil
	}

	result := make([]R, len(values))
	for index, value := range values {
		result[index] = convert(value)
	}

	return result
}
