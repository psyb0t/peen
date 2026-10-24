package propagators

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"

	"github.com/psyb0t/go-workflows/internal/contextvalue"
	"github.com/psyb0t/go-workflows/workflow"
)

const scopeMetadataKey = "go-workflows-log-scope"

type ScopeContextPropagator struct{}

var _ workflow.ContextPropagator = &ScopeContextPropagator{}

func (*ScopeContextPropagator) Inject(ctx context.Context, metadata *workflow.Metadata) error {
	data, err := ctxscope.ToJSON(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "encode workflow log scope")
	}
	metadata.Set(scopeMetadataKey, string(data))
	return nil
}

func (*ScopeContextPropagator) Extract(ctx context.Context, metadata *workflow.Metadata) (context.Context, error) {
	data := metadata.Get(scopeMetadataKey)
	if data == "" {
		return ctx, nil
	}
	if err := validateScope([]byte(data)); err != nil {
		return ctx, ctxerrors.Wrap(err, "validate activity log scope")
	}
	next, err := ctxscope.FromJSON(ctx, []byte(data))
	if err != nil {
		return ctx, ctxerrors.Wrap(err, "decode workflow log scope")
	}
	return next, nil
}

func (*ScopeContextPropagator) InjectFromWorkflow(ctx workflow.Context, metadata *workflow.Metadata) error {
	metadata.Set(scopeMetadataKey, string(contextvalue.LogScope(ctx)))
	return nil
}

func (*ScopeContextPropagator) ExtractToWorkflow(ctx workflow.Context, metadata *workflow.Metadata) (workflow.Context, error) {
	data := metadata.Get(scopeMetadataKey)
	if data == "" {
		return ctx, nil
	}
	if err := validateScope([]byte(data)); err != nil {
		return ctx, ctxerrors.Wrap(err, "validate workflow log scope")
	}
	return contextvalue.WithLogScope(ctx, []byte(data)), nil
}

func validateScope(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return ctxerrors.Wrap(commerr.ErrInvalidValue, "log scope must be an object")
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return ctxerrors.Wrap(err, "decode log scope")
	}
	for _, value := range values {
		switch value.(type) {
		case string, bool, float64:
		default:
			return ctxerrors.Wrap(commerr.ErrInvalidValue, "log scope values must be primitives")
		}
	}
	return nil
}
