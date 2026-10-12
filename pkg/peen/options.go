package peen

import (
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

// validate rejects an Options value New cannot build a runtime from. Bounds a
// caller may leave at zero are filled by withDefaults instead.
func (o Options) validate() error {
	if err := o.validateDependencies(); err != nil {
		return err
	}

	if strings.TrimSpace(o.ConfigDirectory) == "" {
		return ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"config directory",
		)
	}

	if strings.TrimSpace(o.RootAgent) == "" {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "root agent")
	}

	if strings.TrimSpace(o.DefaultModel) == "" {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "default model")
	}

	if len(o.Models) == 0 && o.ModelResolver == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "models")
	}

	if err := o.validateModelReferences(); err != nil {
		return err
	}

	if err := o.validateBounds(); err != nil {
		return err
	}

	return o.validateDisabledTools()
}

func (o Options) validateDisabledTools() error {
	known := agent.WorkspaceToolNames()
	for _, tool := range o.Tools {
		known = append(known, tool.Name)
	}

	seen := make(map[string]bool, len(o.DisableTools))
	for _, name := range o.DisableTools {
		if isMessagingTool(name) {
			return ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"messaging tools cannot be disabled",
			)
		}

		if !slices.Contains(known, name) || seen[name] {
			return ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"unknown or repeated disabled tool",
			)
		}

		seen[name] = true
	}

	return nil
}

func (o Options) validateDependencies() error {
	for _, dependency := range []any{
		o.Filesystem, o.ModelResolver, o.CommandRunner, o.MetricsRegisterer,
	} {
		if dependency == nil {
			continue
		}

		if isNilPointer(dependency) {
			return ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"typed-nil runtime dependency",
			)
		}
	}

	return nil
}

func isNilPointer(value any) bool {
	reflected := reflect.ValueOf(value)

	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func (o Options) validateBounds() error {
	counts := []int{
		o.MaxContextTokens, o.MaxQueuedUserMessages,
		o.MaxMessageBytes, o.MaxSystemPromptBytes, o.MaxStoredMessageBytes,
		o.MaxToolRounds, o.MaxConcurrentTools, o.MaxToolResultTokens,
		o.MaxHookCommandOutput, o.CompactionOutputTokens,
		o.EventLimits.MaxPendingPerSession, o.EventLimits.MaxSummaryBytes,
		o.EventLimits.MaxDataBytes, o.EventLimits.SubscriberBuffer,
	}
	for _, value := range counts {
		if value < 0 {
			return ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"negative runtime bound",
			)
		}
	}

	durations := []time.Duration{
		o.TurnTimeout, o.ToolTimeout,
		o.HookCommandTimeout, o.CompactionTimeout, o.AskTimeout,
	}
	for _, value := range durations {
		if value < 0 {
			return ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"negative runtime timeout",
			)
		}
	}

	if err := tools.ValidateLimits(tools.Limits(o.ToolLimits)); err != nil {
		return ctxerrors.Wrap(err, "validate embedded tool limits")
	}

	if err := agent.ValidateAgentRunLimits(
		agent.AgentRunLimits(o.AgentLimits),
	); err != nil {
		return ctxerrors.Wrap(err, "validate embedded child limits")
	}

	return nil
}

// validateModelReferences rejects a default or compaction model that names no
// entry in Models, which would otherwise surface only once a turn actually
// tries to resolve it.
func (o Options) validateModelReferences() error {
	if o.ModelResolver != nil {
		resolver := modelResolverAdapter{resolver: o.ModelResolver}
		if _, err := resolver.ResolveModel(o.DefaultModel); err != nil {
			return err
		}

		if o.CompactionModel != "" {
			if _, err := resolver.ResolveModel(o.CompactionModel); err != nil {
				return err
			}
		}

		return nil
	}

	if _, ok := o.Models[o.DefaultModel]; !ok {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"default model %q is not present in models",
			o.DefaultModel,
		)
	}

	if o.CompactionModel == "" {
		return nil
	}

	if _, ok := o.Models[o.CompactionModel]; !ok {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"compaction model %q is not present in models",
			o.CompactionModel,
		)
	}

	return nil
}

// withDefaults fills the bounds agent.RuntimeOptions requires but Options
// otherwise lets a caller leave at zero.
func (o Options) withDefaults() Options {
	if o.MaxEventWakesPerHour == 0 {
		o.MaxEventWakesPerHour = defaultMaxEventWakesPerHour
	}

	if o.MaxContextTokens <= 0 {
		o.MaxContextTokens = defaultMaxContextTokens
	}

	if o.TurnTimeout <= 0 {
		o.TurnTimeout = defaultTurnTimeout
	}

	return o
}
