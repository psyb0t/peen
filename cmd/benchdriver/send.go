package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
)

// sendState follows one send step's turn: it counts tool calls, fires the
// planned kill, answers questions, and records the live frames of a
// planned disconnect.
type sendState struct {
	runner  *runner
	current step
	killAt  int

	mu             sync.Mutex
	toolUses       int
	answered       int
	lastSequence   int64
	isKilled       bool
	isDisconnected bool
	live           *json.Encoder
	killDone       sync.WaitGroup
}

// liveFrame is one line of a live frames file.
type liveFrame struct {
	Phase    string `json:"phase"`
	Sequence *int64 `json:"sequence"`
	Type     string `json:"type"`
}

const (
	livePhaseFirst    = "first"
	livePhaseReattach = "reattach"
)

// frameEnvelope reads the routing fields of a raw frame.
type frameEnvelope struct {
	Metadata struct {
		Sequence *int64 `json:"sequence"`
	} `json:"metadata"`
}

// toolUseData reads a tool.use frame's tool name.
type toolUseData struct {
	Name string `json:"name"`
}

func (r *runner) send(ctx context.Context, current step) error {
	state := &sendState{
		runner:  r,
		current: current,
		killAt:  r.killPoint(current.Kill),
	}

	if current.Kill != nil {
		r.record.event(ctx, "kill.planned", map[string]any{
			fieldLabel:      current.Label,
			"target":        current.Kill.Target,
			"afterToolUses": state.killAt,
			"onTool":        current.Kill.OnTool,
			"onFile":        current.Kill.OnFile,
		})
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()

	if current.Kill != nil && current.Kill.OnFile != "" {
		if err := removeKillFile(current.Kill.OnFile); err != nil {
			return err
		}

		state.killDone.Add(1)

		go state.watchKillFile(ctx, watchCtx)
	}

	if current.DisconnectAfterToolUses > 0 {
		file, err := r.openLiveFile()
		if err != nil {
			return err
		}

		defer func() {
			if err := file.Close(); err != nil {
				ctxscope.GetLogger(ctx).Warn("live file close", "err", err)
			}
		}()

		state.live = json.NewEncoder(file)
	}

	err := state.runTurn(ctx)

	stopWatch()
	state.killDone.Wait()

	return err
}

func (s *sendState) runTurn(ctx context.Context) error {
	r := s.runner
	sessionID := r.sessions[s.current.Session]
	timeout := secondsOr(s.current.TimeoutSeconds, defaultTurnTimeout)

	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := r.client.RunTurn(
		turnCtx,
		sessionID,
		sendData(s.current),
		s.handle(ctx),
	)

	if errors.Is(err, errDisconnect) {
		err = s.reattach(ctx, sessionID)
	}

	s.mu.Lock()
	detail := map[string]any{
		fieldLabel: s.current.Label,
		"outcome":  result.Outcome,
		"code":     result.FailureCode,
		"toolUses": s.toolUses,
		"killed":   s.isKilled,
	}
	isKilled := s.isKilled
	s.mu.Unlock()

	if err != nil && !isPlanned(err) {
		detail[fieldErr] = err.Error()
	}

	r.record.event(ctx, "turn.finished", detail)

	// A killed controller drops the socket mid-turn. That is the point of
	// the kill, not a driver failure.
	if err == nil || isKilled || isPlanned(err) {
		return nil
	}

	return ctxerrors.Wrap(err, "run the turn")
}

func (s *sendState) handle(ctx context.Context) controlclient.TurnHandler {
	return func(
		turn *controlclient.TurnConnection,
		frame controlclient.TurnFrame,
	) error {
		s.mu.Lock()
		defer s.mu.Unlock()

		sequence := frameSequence(frame.Raw)
		if sequence != nil {
			s.lastSequence = *sequence
		}

		s.writeLive(ctx, livePhaseFirst, sequence, frame.Type)

		switch frame.Type {
		case frameToolUse:
			return s.onToolUse(ctx, frame)
		case frameQuestionAsked:
			s.answer(ctx, turn)
		}

		return nil
	}
}

func (s *sendState) onToolUse(
	ctx context.Context,
	frame controlclient.TurnFrame,
) error {
	if s.countsTool(frame.Data) {
		s.toolUses++
	}

	if s.killAt > 0 && !s.isKilled && s.toolUses >= s.killAt {
		s.isKilled = true
		s.killDone.Add(1)

		go s.fireKill(ctx)
	}

	disconnectAt := s.current.DisconnectAfterToolUses
	if disconnectAt > 0 && !s.isDisconnected && s.toolUses >= disconnectAt {
		s.isDisconnected = true

		return errDisconnect
	}

	return nil
}

func (s *sendState) countsTool(data json.RawMessage) bool {
	if s.current.Kill == nil || s.current.Kill.OnTool == "" {
		return true
	}

	use := toolUseData{}
	if err := json.Unmarshal(data, &use); err != nil {
		return false
	}

	return use.Name == s.current.Kill.OnTool
}

// watchKillFile fires the kill once the send's kill file exists. It
// stops without a kill when watchCtx ends, which happens when the turn
// finishes first. The kill itself runs on ctx, so a restart delay
// outlives the turn the kill ended.
func (s *sendState) watchKillFile(ctx, watchCtx context.Context) {
	ticker := time.NewTicker(killFilePoll)
	defer ticker.Stop()

	for {
		_, err := os.Stat(s.current.Kill.OnFile)
		if err == nil {
			s.mu.Lock()
			s.isKilled = true
			s.mu.Unlock()

			s.fireKill(ctx)

			return
		}

		select {
		case <-watchCtx.Done():
			s.killDone.Done()

			return
		case <-ticker.C:
		}
	}
}

// removeKillFile clears a kill file a previous send or hook left, so
// only a file written during this send fires the kill.
func removeKillFile(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return ctxerrors.Wrap(err, "remove the kill file")
}

func (s *sendState) fireKill(ctx context.Context) {
	defer s.killDone.Done()
	defer func() {
		logRecovered(ctx, recover())
	}()

	spec := s.current.Kill
	r := s.runner

	delay := time.Duration(spec.DelayMilliseconds) * time.Millisecond
	if err := sleep(ctx, delay); err != nil {
		return
	}

	if spec.Target == killTargetStop {
		s.stopTurn(ctx)

		return
	}

	if err := r.control.kill(ctx, spec.Target); err != nil {
		r.record.event(ctx, "kill.failed", map[string]any{
			"target": spec.Target,
			fieldErr: err.Error(),
		})

		return
	}

	if spec.Target != killTargetController || spec.RestartAfterSeconds <= 0 {
		return
	}

	restartDelay := time.Duration(spec.RestartAfterSeconds) * time.Second
	if err := sleep(ctx, restartDelay); err != nil {
		return
	}

	if err := r.control.start(ctx); err != nil {
		r.record.event(ctx, "restart.failed", map[string]any{
			fieldErr: err.Error(),
		})
	}
}

// stopTurn cancels the session's running turn through the API.
func (s *sendState) stopTurn(ctx context.Context) {
	r := s.runner

	_, err := r.client.CancelSession(ctx, r.sessions[s.current.Session])
	if err != nil {
		r.record.event(ctx, "stop.failed", map[string]any{
			fieldErr: err.Error(),
		})

		return
	}

	r.record.event(ctx, "turn.stopped", map[string]any{
		fieldLabel: s.current.Label,
	})
}

func (s *sendState) answer(
	ctx context.Context,
	turn *controlclient.TurnConnection,
) {
	if s.answered >= len(s.current.Answers) {
		s.runner.record.event(ctx, "question.unanswered", map[string]any{
			fieldLabel: s.current.Label,
		})

		return
	}

	reply := s.current.Answers[s.answered]
	s.answered++

	if err := turn.Reply(reply); err != nil {
		s.runner.record.event(ctx, "answer.failed", map[string]any{
			fieldErr: err.Error(),
		})
	}
}

// reattach follows the rest of a turn over the feed after a planned
// disconnect, resuming after the last sequence the first socket saw.
func (s *sendState) reattach(ctx context.Context, sessionID uuid.UUID) error {
	s.mu.Lock()
	cursor := s.lastSequence
	s.mu.Unlock()

	s.runner.record.event(ctx, "socket.reattached", map[string]any{
		"after": cursor,
	})

	attachCtx, cancel := context.WithTimeout(ctx, reattachTimeout)
	defer cancel()

	err := s.runner.client.Attach(
		attachCtx,
		sessionID,
		&cursor,
		func(event controlclient.AttachEvent) error {
			s.mu.Lock()
			s.writeLive(ctx, livePhaseReattach, event.Sequence, event.Type)
			s.mu.Unlock()

			if isTurnEnd(event.Type) {
				return errTurnEnded
			}

			return nil
		},
	)

	if err == nil || isPlanned(err) {
		return nil
	}

	return ctxerrors.Wrap(err, "reattach to the session")
}

func (s *sendState) writeLive(
	ctx context.Context,
	phase string,
	sequence *int64,
	frameType string,
) {
	if s.live == nil {
		return
	}

	err := s.live.Encode(liveFrame{
		Phase:    phase,
		Sequence: sequence,
		Type:     frameType,
	})
	if err != nil {
		ctxscope.GetLogger(ctx).Warn("live frame write failed", "err", err)
	}
}

func frameSequence(raw json.RawMessage) *int64 {
	envelope := frameEnvelope{}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}

	return envelope.Metadata.Sequence
}

// ensureOutputDir makes the driver's output directory.
func ensureOutputDir(path string) error {
	if err := os.MkdirAll(path, outputDirMode); err != nil {
		return ctxerrors.Wrap(err, "create the output directory")
	}

	return nil
}
