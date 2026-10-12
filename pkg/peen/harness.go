package peen

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

// HarnessLayer is an immutable set of relative source files, including
// AGENTS.md and .agents rules, skills, agents, event handlers and hooks.
// Layers follow workspace files in order, so later custom names take priority.
type HarnessLayer struct{ Files map[string]string }

// HarnessSnapshot is the context resolved for one workspace.
type HarnessSnapshot struct {
	Hash          string
	Workspace     string
	Instructions  []HarnessInstruction
	Skills        []HarnessSkill
	Agents        []HarnessAgent
	EventHandlers []HarnessEventHandler
	Warnings      []HarnessWarning
	Hooks         []HarnessHook
	Manifest      []ContextManifestEntry
}

type HarnessInstruction struct {
	Kind     string
	Source   string
	Priority int
	Content  string
	Hash     string
	Scope    string
}

type HarnessSkill struct {
	Name          string
	Description   string
	Source        string
	Directory     string
	Hash          string
	Metadata      map[string]any
	Homepage      string
	Permissions   map[string]any
	UserInvocable bool
	License       string
	Compatibility string
	AllowedTools  string
}

type HarnessAgent struct {
	Name         string
	Description  string
	Source       string
	Instructions string
	AllowedTools []string
	Hash         string
}

type HarnessEventHandler struct {
	Type         string
	Agent        string
	Delivery     string
	Source       string
	Instructions string
	Hash         string
}

type HarnessWarning struct {
	Kind   string
	Source string
	Reason string
}

func memoryHarness(layers []HarnessLayer) ([]workspacefs.Reader, error) {
	readers := make([]workspacefs.Reader, 0, len(layers))
	for _, layer := range layers {
		source, err := workspacefs.NewMemory(layer.Files)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "create memory harness")
		}

		readers = append(readers, workspacefs.ReadOnly{Source: source})
	}

	return readers, nil
}

// Harness rebuilds the snapshot using the same resolver as the next turn.
func (r *Runtime) Harness(
	ctx context.Context,
	workspace string,
) (HarnessSnapshot, error) {
	ctx, finish, err := r.internal.BeginOperation(ctx)
	if err != nil {
		return HarnessSnapshot{}, ctxerrors.Wrap(err, "read embedding harness")
	}
	defer finish()

	if err := ctx.Err(); err != nil {
		return HarnessSnapshot{}, ctxerrors.Wrap(err, "read embedding harness")
	}

	value, err := r.internal.ResolveHarness(workspace)
	if err != nil {
		return HarnessSnapshot{}, ctxerrors.Wrap(err, "read embedding harness")
	}

	result, err := convertRecord[HarnessSnapshot](struct {
		Instructions  []harness.Instruction
		Skills        []harness.Skill
		Agents        []harness.Agent
		EventHandlers []harness.EventHandler
		Warnings      []harness.Warning
		Hooks         []harness.Hook
		Manifest      []harness.ManifestEntry
	}{
		value.Instructions(), value.Skills(), value.Agents(),
		value.EventHandlers(), value.Warnings(),
		value.Hooks(), value.Manifest(),
	}, nil)
	if err != nil {
		return HarnessSnapshot{}, ctxerrors.Wrap(err, "read embedding harness")
	}

	result.Hash = value.Hash()
	result.Workspace = value.Workspace()

	return result, nil
}
