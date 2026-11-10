package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// stepOp names one scenario step.
type stepOp string

const (
	opSend      stepOp = "send"
	opParallel  stepOp = "parallel"
	opRepeat    stepOp = "repeat"
	opAwaitIdle stepOp = "awaitIdle"
	opPostEvent stepOp = "postEvent"
	opStop      stepOp = "stop"
	opKill      stepOp = "kill"
	opRestart   stepOp = "restart"
	opExec      stepOp = "exec"
	opSleep     stepOp = "sleep"
)

// killTarget names what a kill stops: a process, or, for stop, the
// session's turn through the API, as a user pressing Stop would.
type killTarget string

const (
	killTargetWorker     killTarget = "worker"
	killTargetController killTarget = "controller"
	killTargetStop       killTarget = "stop"
)

// scenario is the parsed scenario.json a harness task ships.
type scenario struct {
	// Sessions maps a session name to its workspace directory. A scenario
	// without sessions gets one named main at /app.
	Sessions map[string]string `json:"sessions"`

	Steps []step `json:"steps"`

	// SampleMemorySeconds samples the resident memory of the controller
	// and every process under it at this interval. Zero turns it off.
	SampleMemorySeconds int `json:"sampleMemorySeconds"`

	// Seed makes the random kill points of a repeat reproducible. Zero
	// picks one from the clock, and the driver log records it.
	Seed uint64 `json:"seed"`
}

// step is one scenario action. Which fields apply depends on Op.
type step struct {
	Op    stepOp `json:"op"`
	Label string `json:"label"`

	Session string   `json:"session"`
	Message string   `json:"message"`
	Answers []string `json:"answers"`
	Kill    *kill    `json:"kill"`

	// DisconnectAfterToolUses closes the turn's socket after that many
	// tool calls and reattaches from the last sequence it saw.
	DisconnectAfterToolUses int `json:"disconnectAfterToolUses"`

	TimeoutSeconds int `json:"timeoutSeconds"`
	SettleSeconds  int `json:"settleSeconds"`

	Type     string         `json:"type"`
	Summary  string         `json:"summary"`
	Data     map[string]any `json:"data"`
	Delivery string         `json:"delivery"`

	Target  killTarget `json:"target"`
	Command []string   `json:"command"`
	Seconds int        `json:"seconds"`

	Steps []step `json:"steps"`
	Count int    `json:"count"`
}

// kill stops a process while a send's turn runs.
type kill struct {
	Target killTarget `json:"target"`

	// AfterToolUses is the tool call that triggers the kill. With
	// AfterToolUsesMax set, the trigger is drawn from the seeded random
	// source between the two, inclusive.
	AfterToolUses    int `json:"afterToolUses"`
	AfterToolUsesMax int `json:"afterToolUsesMax"`

	// OnTool counts only calls of this tool when set.
	OnTool string `json:"onTool"`

	// OnFile fires the kill once this file appears during the send,
	// instead of counting tool calls. A harness hook writes it to mark a
	// moment no frame shows, such as a compaction starting. The driver
	// removes the file when the send starts.
	OnFile string `json:"onFile"`

	DelayMilliseconds   int `json:"delayMilliseconds"`
	RestartAfterSeconds int `json:"restartAfterSeconds"`

	// EveryNth applies the kill inside a repeat only on iterations that
	// are a multiple of it.
	EveryNth int `json:"everyNth"`
}

func loadScenario(path string) (scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return scenario{}, ctxerrors.Wrap(err, "read the scenario")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	parsed := scenario{}
	if err := decoder.Decode(&parsed); err != nil {
		return scenario{}, ctxerrors.Wrap(err, "decode the scenario")
	}

	if len(parsed.Sessions) == 0 {
		parsed.Sessions = map[string]string{
			defaultSessionName: defaultWorkspace,
		}
	}

	if err := validateSteps(parsed.Steps, parsed.Sessions); err != nil {
		return scenario{}, err
	}

	return parsed, nil
}

func validateSteps(steps []step, sessions map[string]string) error {
	if len(steps) == 0 {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "no steps")
	}

	for index := range steps {
		if err := validateStep(&steps[index], sessions); err != nil {
			return ctxerrors.Wrapf(err, "step %d", index)
		}
	}

	return nil
}

func validateStep(current *step, sessions map[string]string) error {
	if current.Session == "" {
		current.Session = defaultSessionName
	}

	if _, ok := sessions[current.Session]; !ok {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"unknown session %q",
			current.Session,
		)
	}

	switch current.Op {
	case opSend:
		return validateSend(current)
	case opPostEvent:
		return requireText(current.Type, "postEvent needs a type")
	case opKill:
		return validateKillStep(current.Target)
	case opExec:
		if len(current.Command) == 0 {
			return ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"exec needs a command",
			)
		}

		return nil
	case opParallel, opRepeat:
		return validateSteps(current.Steps, sessions)
	case opAwaitIdle, opStop, opRestart, opSleep:
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"unknown op %q",
		current.Op,
	)
}

func validateSend(current *step) error {
	if current.Kill != nil {
		if err := validateTarget(current.Kill.Target); err != nil {
			return err
		}
	}

	return requireText(current.Message, "send needs a message")
}

// validateKillStep refuses stop on a kill step: a kill step acts on a
// process, and the stop op stops a turn.
func validateKillStep(target killTarget) error {
	if target == killTargetStop {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"a kill step cannot stop; use the stop op",
		)
	}

	return validateTarget(target)
}

func requireText(value, message string) error {
	if strings.TrimSpace(value) == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, message)
	}

	return nil
}

func validateTarget(target killTarget) error {
	switch target {
	case killTargetWorker, killTargetController, killTargetStop:
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"unknown kill target %q",
		target,
	)
}

// expandIteration returns a copy of steps with {i} replaced by the
// iteration number in every text field.
func expandIteration(steps []step, iteration int) []step {
	value := strconv.Itoa(iteration)
	expanded := make([]step, len(steps))

	for index, original := range steps {
		current := original
		current.Message = strings.ReplaceAll(
			current.Message,
			iterationToken,
			value,
		)
		current.Summary = strings.ReplaceAll(
			current.Summary,
			iterationToken,
			value,
		)
		current.Label = strings.ReplaceAll(
			current.Label,
			iterationToken,
			value,
		)
		current.Steps = expandIteration(current.Steps, iteration)
		expanded[index] = current
	}

	return expanded
}
