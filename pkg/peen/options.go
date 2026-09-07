package peen

import (
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// validate rejects an Options value New cannot build a runtime from. Bounds a
// caller may leave at zero are filled by withDefaults instead.
func (o Options) validate() error {
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

	if len(o.Models) == 0 {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "models")
	}

	if err := o.validateModelReferences(); err != nil {
		return err
	}

	if o.MaxContextTokens < 0 {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "max context tokens")
	}

	return nil
}

// validateModelReferences rejects a default or compaction model that names no
// entry in Models, which would otherwise surface only once a turn actually
// tries to resolve it.
func (o Options) validateModelReferences() error {
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
	if o.MaxContextTokens <= 0 {
		o.MaxContextTokens = defaultMaxContextTokens
	}

	if o.TurnTimeout <= 0 {
		o.TurnTimeout = defaultTurnTimeout
	}

	return o
}
