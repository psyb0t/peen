package peen

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/naming"
)

// ModelResolver resolves models at turn time. Returned clients must be non-nil.
type ModelResolver interface {
	ResolveModel(string) (ModelClient, error)
}

type modelResolverAdapter struct{ resolver ModelResolver }

func (a modelResolverAdapter) ResolveModel(
	name string,
) (agent.ModelClient, error) {
	value, err := a.resolver.ResolveModel(name)
	if err != nil {
		return agent.ModelClient{}, ctxerrors.Wrap(
			err,
			"resolve embedding model",
		)
	}

	if value.Client == nil || strings.TrimSpace(value.Model.ID) == "" {
		return agent.ModelClient{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"resolved model requires client and ID",
		)
	}

	return agent.ModelClient{Client: value.Client, Model: value.Model}, nil
}

// Tool supplies a JSON object schema and a Go handler. Names matching built-in
// workspace tools replace them. Messaging names remain reserved.
type Tool struct {
	Name            string
	Description     string
	ArgumentsSchema json.RawMessage
	Handler         func(context.Context, ToolInput) (ToolResult, error)
}

type ToolInput struct {
	Name      string
	CallID    string
	Arguments json.RawMessage
}

type ToolResult struct {
	Content  string
	IsError  bool
	Metadata map[string]any
}

const (
	objectSchemaType   = "object"
	toolSendMessage    = "sendMessage"
	toolWaitMessages   = "waitMessages"
	toolStopAgent      = "stopAgent"
	toolAsk            = "ask"
	schemaReferenceKey = "$ref"
)

func isMessagingTool(name string) bool {
	return name == toolSendMessage || name == toolWaitMessages ||
		name == toolStopAgent || name == toolAsk
}

func customTools(definitions []Tool) ([]elelem.Tool, error) {
	result := make([]elelem.Tool, 0, len(definitions))

	seen := map[string]bool{}
	for _, definition := range definitions {
		if !naming.IsValid(definition.Name) ||
			isMessagingTool(definition.Name) ||
			seen[definition.Name] || definition.Handler == nil ||
			strings.TrimSpace(definition.Description) == "" {
			return nil, ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"invalid or repeated custom tool",
			)
		}

		seen[definition.Name] = true

		tool, err := customTool(definition)
		if err != nil {
			return nil, err
		}

		result = append(result, tool)
	}

	return result, nil
}

func customTool(definition Tool) (elelem.Tool, error) {
	schema := &openapi3.Schema{}
	if err := json.Unmarshal(definition.ArgumentsSchema, schema); err != nil {
		return elelem.Tool{}, ctxerrors.Wrap(err, "parse custom tool schema")
	}

	var raw any
	if err := json.Unmarshal(definition.ArgumentsSchema, &raw); err != nil {
		return elelem.Tool{}, ctxerrors.Wrap(err, "parse schema references")
	}

	if schema.Type == nil ||
		!schema.Type.Is(objectSchemaType) || hasSchemaRef(raw) {
		return elelem.Tool{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"tool schema must be a self-contained object",
		)
	}

	if err := schema.Validate(context.Background()); err != nil {
		return elelem.Tool{}, ctxerrors.Wrap(err, "validate custom tool schema")
	}

	return elelem.Tool{
		Name: definition.Name, Description: definition.Description,
		ArgumentsSchema: slices.Clone(definition.ArgumentsSchema),
		Handler: func(ctx context.Context, input elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			var arguments any
			if err := json.Unmarshal(input.Arguments, &arguments); err != nil {
				return elelem.ToolResult{}, ctxerrors.Wrap(
					err,
					"decode tool arguments",
				)
			}

			if err := schema.VisitJSON(arguments); err != nil {
				return elelem.ToolResult{}, ctxerrors.Wrap(
					commerr.ErrValidationFailed,
					"custom tool arguments violate schema",
				)
			}

			value, err := definition.Handler(ctx, ToolInput{
				Name: input.Name, CallID: input.CallID,
				Arguments: slices.Clone(input.Arguments),
			})
			if err != nil {
				return elelem.ToolResult{}, ctxerrors.Wrap(
					err,
					"run custom tool",
				)
			}

			return elelem.ToolResult{
				Content: value.Content,
				IsError: value.IsError, Metadata: value.Metadata,
			}, nil
		},
	}, nil
}

func hasSchemaRef(value any) bool {
	switch object := value.(type) {
	case map[string]any:
		if _, found := object[schemaReferenceKey]; found {
			return true
		}

		for _, child := range object {
			if hasSchemaRef(child) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(object, hasSchemaRef) {
			return true
		}
	}

	return false
}
