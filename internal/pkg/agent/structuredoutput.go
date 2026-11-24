package agent

import (
	"bytes"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	outputSchemaResource        = "urn:peen:output"
	MaxTurnTimeoutSeconds       = 86400
	defaultOutputSchemaBytes    = 65536
	defaultOutputSchemaAttempts = 3
)

func outputAttempts(value int) int {
	if value == 0 {
		return defaultOutputSchemaAttempts
	}

	return value
}

func outputSchemaBound(value int) int {
	if value == 0 {
		return defaultOutputSchemaBytes
	}

	return value
}

func needsNewRootTurn(input TurnRequest) bool {
	return input.RequireNewTurn || input.Origin != nil ||
		len(input.OutputSchema) != 0 ||
		input.TimeoutSeconds != 0
}

func requiresNativeWorkflow(input TurnRequest) bool {
	return len(input.OutputSchema) != 0 || !input.Deadline.IsZero()
}

func (r *Runtime) validateRequestedTurnSettings(input TurnRequest) error {
	if err := validateTurnSettingsShape(input.OutputSchema,
		input.TimeoutSeconds, nil); err != nil {
		return err
	}

	if len(input.OutputSchema) == 0 {
		return nil
	}

	return ValidateOutputSchema(input.OutputSchema, r.maxOutputSchemaBytes)
}

func validateTurnSettingsShape(
	schema json.RawMessage,
	seconds int,
	to *uuid.UUID,
) error {
	if seconds < 0 || seconds > MaxTurnTimeoutSeconds ||
		(to != nil && (len(schema) != 0 || seconds != 0)) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"turn settings require a root turn and an in-range timeout")
	}

	return nil
}

// ValidateOutputSchema compiles a bounded, self-contained schema without I/O.
func ValidateOutputSchema(schema json.RawMessage, maximum int) error {
	_, err := compileOutputSchema(schema, maximum)

	return err
}

func compileOutputSchema(
	schema json.RawMessage,
	maximum int,
) (*jsonschema.Schema, error) {
	if len(schema) == 0 || len(schema) > outputSchemaBound(maximum) ||
		!json.Valid(schema) {
		return nil, ctxerrors.Wrap(ErrOutputSchemaInvalid,
			"output schema exceeds its byte bound or is empty")
	}

	var document map[string]any

	decoder := json.NewDecoder(bytes.NewReader(schema))
	decoder.UseNumber()

	if err := decoder.Decode(&document); err != nil || document == nil {
		return nil, ctxerrors.Wrap(ErrOutputSchemaInvalid,
			"output schema must be a JSON object")
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(nil)

	if err := compiler.AddResource(outputSchemaResource, document); err != nil {
		return nil, ctxerrors.Wrap(ErrOutputSchemaInvalid,
			"output schema resource is invalid")
	}

	compiled, err := compiler.Compile(outputSchemaResource)
	if err != nil {
		return nil, ctxerrors.Wrap(ErrOutputSchemaInvalid,
			"output schema cannot compile without external resources")
	}

	return compiled, nil
}
