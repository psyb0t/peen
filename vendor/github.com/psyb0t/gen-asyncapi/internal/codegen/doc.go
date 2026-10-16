package codegen

import (
	"encoding/json"
	"strings"
	"text/template"

	asyncapi "github.com/psyb0t/go-asyncapi"
)

// schemaDocTemplate renders a schema's doc comment: its description, default
// and examples, the keywords the generated code does not enforce, and a
// deprecation notice in the form go vet and editors recognize.
func schemaDocTemplate() *template.Template {
	return template.Must(template.New("doc").Parse(
		`{{.Description}}
{{- if .Default}}

Default: {{.Default}}
{{- end}}
{{- if .Examples}}

Examples: {{.Examples}}
{{- end}}
{{- if .Unenforced}}

Not enforced by the generated code: {{.Unenforced}}.
{{- end}}
{{- if .Deprecated}}

Deprecated: the schema marks this as deprecated.
{{- end}}`))
}

type schemaDocData struct {
	Description string
	Default     string
	Examples    string
	Unenforced  string
	Deprecated  bool
}

func schemaDoc(info *schemaInfo) string {
	data := schemaDocData{
		Description: strings.TrimSpace(info.description),
		Unenforced:  strings.Join(info.unenforced, ", "),
		Deprecated:  info.deprecated,
	}

	if info.hasDefault {
		data.Default = jsonText(info.defaultVal)
	}

	examples := make([]string, 0, len(info.examples))
	for _, example := range info.examples {
		examples = append(examples, jsonText(example))
	}

	data.Examples = strings.Join(examples, ", ")

	var rendered strings.Builder
	if err := schemaDocTemplate().Execute(&rendered, data); err != nil {
		return data.Description
	}

	return strings.TrimSpace(rendered.String())
}

// jsonText renders a spec value as compact JSON for a doc comment.
func jsonText(value any) string {
	data, err := json.Marshal(plainValue(value))
	if err != nil {
		return ""
	}

	return string(data)
}

// plainValue turns spec mappings into ordinary maps so they encode as JSON.
func plainValue(value any) any {
	switch typed := value.(type) {
	case *asyncapi.Object:
		result := make(map[string]any, len(typed.Keys))
		for _, key := range typed.Keys {
			result[key] = plainValue(typed.Values[key])
		}

		return result
	case []any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, plainValue(item))
		}

		return items
	default:
		return value
	}
}
