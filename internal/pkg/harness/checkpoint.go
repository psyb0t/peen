package harness

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const snapshotCheckpointVersion = 1

type snapshotCheckpoint struct {
	Version int             `json:"version"`
	Digest  string          `json:"digest"`
	State   json.RawMessage `json:"state"`
}

type snapshotState struct {
	ConfigRoot    string                   `json:"configRoot"`
	Workspace     string                   `json:"workspace"`
	Hash          string                   `json:"hash"`
	Instructions  []checkpointInstruction  `json:"instructions"`
	Skills        []checkpointSkill        `json:"skills"`
	Agents        []checkpointAgent        `json:"agents"`
	EventHandlers []checkpointEventHandler `json:"eventHandlers"`
	Hooks         []Hook                   `json:"hooks"`
	Manifest      []ManifestEntry          `json:"manifest"`
	Warnings      []Warning                `json:"warnings"`
	SkillContents map[string]string        `json:"skillContents"`
	SkillBudget   int64                    `json:"skillBudget"`
}

// Checkpoint retains resolved content without reading its source files again.
func (s Snapshot) Checkpoint() (json.RawMessage, error) {
	state, err := json.Marshal(snapshotState{
		ConfigRoot: s.configRoot, Workspace: s.workspace, Hash: s.hash,
		Instructions: mapCheckpointValues(s.instructions,
			func(value Instruction) checkpointInstruction {
				return checkpointInstruction(value)
			}),
		Skills: mapCheckpointValues(s.skills,
			func(value Skill) checkpointSkill {
				return checkpointSkill(value)
			}),
		Agents: mapCheckpointValues(s.agents,
			func(value Agent) checkpointAgent {
				return checkpointAgent(value)
			}),
		EventHandlers: mapCheckpointValues(s.eventHandlers,
			func(value EventHandler) checkpointEventHandler {
				return checkpointEventHandler(value)
			}),
		Hooks: s.hooks, Manifest: s.manifest,
		Warnings: s.warnings, SkillContents: s.skillContents,
		SkillBudget: s.skillBudget,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode harness checkpoint state")
	}

	encoded, err := json.Marshal(snapshotCheckpoint{
		Version: snapshotCheckpointVersion,
		Digest:  hashString(string(state)), State: state,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode harness checkpoint")
	}

	return encoded, nil
}

// RestoreCheckpoint validates saved state and never consults the filesystem.
func RestoreCheckpoint(encoded json.RawMessage) (Snapshot, error) {
	var checkpoint snapshotCheckpoint
	if err := decodeSnapshotState(encoded, &checkpoint); err != nil {
		return Snapshot{}, err
	}

	digest := hashString(string(checkpoint.State))
	if checkpoint.Version != snapshotCheckpointVersion ||
		subtle.ConstantTimeCompare([]byte(digest),
			[]byte(checkpoint.Digest)) != 1 {
		return Snapshot{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"harness checkpoint version or digest")
	}

	var state snapshotState
	if err := decodeSnapshotState(checkpoint.State, &state); err != nil {
		return Snapshot{}, err
	}

	if state.SkillBudget < 0 {
		return Snapshot{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"harness checkpoint skill budget")
	}

	return Snapshot{
		configRoot: state.ConfigRoot, workspace: state.Workspace,
		hash: state.Hash,
		instructions: mapCheckpointValues(state.Instructions,
			func(value checkpointInstruction) Instruction {
				return Instruction(value)
			}),
		skills: mapCheckpointValues(state.Skills,
			func(value checkpointSkill) Skill {
				return Skill(value)
			}),
		agents: mapCheckpointValues(state.Agents,
			func(value checkpointAgent) Agent {
				return Agent(value)
			}),
		eventHandlers: mapCheckpointValues(state.EventHandlers,
			func(value checkpointEventHandler) EventHandler {
				return EventHandler(value)
			}),
		hooks: state.Hooks, manifest: state.Manifest, warnings: state.Warnings,
		skillContents: state.SkillContents, skillBudget: state.SkillBudget,
	}, nil
}

func decodeSnapshotState(encoded json.RawMessage, value any) error {
	trimmed := bytes.TrimSpace(encoded)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"harness checkpoint must be an object")
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	if err := decoder.Decode(value); err != nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"decode harness checkpoint")
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"harness checkpoint contains trailing data")
	}

	return nil
}
