package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2" // nosemgrep // Seeded so scenarios replay, never secrets.
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
)

// maxExecOutputBytes bounds how much of an exec step's output the driver
// log keeps.
const maxExecOutputBytes = 8192

// errDisconnect stops a turn's stream on purpose so the driver can reattach.
var errDisconnect = ctxerrors.Wrap(commerr.ErrClosed, "planned disconnect")

// errTurnEnded stops a reattached stream once the turn's last frame arrives.
var errTurnEnded = ctxerrors.Wrap(commerr.ErrClosed, "turn ended")

// runner executes scenario steps against one controller.
type runner struct {
	client     *controlclient.Client
	control    *controller
	record     *recorder
	sessions   map[string]uuid.UUID
	outputDir  string
	baseURL    string
	token      string
	httpClient *http.Client

	randomMu sync.Mutex
	random   *rand.Rand

	liveMu    sync.Mutex
	liveCount int
}

func (r *runner) runSteps(ctx context.Context, steps []step) {
	for _, current := range steps {
		r.runStep(ctx, current)
	}
}

func (r *runner) runStep(ctx context.Context, current step) {
	started := time.Now()

	r.record.event(ctx, "step.started", map[string]any{
		"op":       current.Op,
		fieldLabel: current.Label,
		"session":  current.Session,
	})

	err := r.dispatch(ctx, current)
	detail := map[string]any{
		"op":       current.Op,
		fieldLabel: current.Label,
		"seconds":  time.Since(started).Seconds(),
	}

	if err != nil {
		detail[fieldErr] = err.Error()
	}

	r.record.event(ctx, "step.finished", detail)
}

func (r *runner) dispatch(ctx context.Context, current step) error {
	switch current.Op {
	case opSend:
		return r.send(ctx, current)
	case opParallel:
		r.parallel(ctx, current.Steps)

		return nil
	case opRepeat:
		r.repeat(ctx, current)

		return nil
	case opAwaitIdle:
		return r.awaitIdle(ctx, current)
	case opPostEvent:
		return r.postEvent(ctx, current)
	case opStop, opKill, opRestart, opExec, opSleep:
		return r.dispatchControl(ctx, current)
	}

	return ctxerrors.Wrapf(commerr.ErrValidationFailed, "op %q", current.Op)
}

// dispatchControl runs the steps that act on processes and time rather
// than on a turn.
func (r *runner) dispatchControl(ctx context.Context, current step) error {
	switch current.Op {
	case opSend, opParallel, opRepeat, opAwaitIdle, opPostEvent:
		return ctxerrors.Wrapf(commerr.ErrInvalidState, "op %q", current.Op)
	case opStop:
		_, err := r.client.CancelSession(ctx, r.sessions[current.Session])
		if err != nil {
			return ctxerrors.Wrap(err, "stop the session")
		}

		return nil
	case opKill:
		return r.control.kill(ctx, current.Target)
	case opRestart:
		return r.restart(ctx)
	case opExec:
		return r.exec(ctx, current)
	case opSleep:
		return sleep(ctx, time.Duration(current.Seconds)*time.Second)
	}

	return ctxerrors.Wrapf(commerr.ErrValidationFailed, "op %q", current.Op)
}

func (r *runner) parallel(ctx context.Context, steps []step) {
	var wg sync.WaitGroup

	for _, current := range steps {
		wg.Go(func() {
			r.runStep(ctx, current)
		})
	}

	wg.Wait()
}

func (r *runner) repeat(ctx context.Context, current step) {
	for iteration := 1; iteration <= current.Count; iteration++ {
		for _, inner := range expandIteration(current.Steps, iteration) {
			if inner.Kill != nil && inner.Kill.EveryNth > 0 &&
				iteration%inner.Kill.EveryNth != 0 {
				inner.Kill = nil
			}

			r.runStep(ctx, inner)
		}
	}
}

func (r *runner) restart(ctx context.Context) error {
	if err := r.control.stop(ctx); err != nil {
		return err
	}

	return r.control.start(ctx)
}

func (r *runner) exec(ctx context.Context, current step) error {
	//nolint:gosec // The task author's scenario names the command.
	command := exec.CommandContext(
		ctx,
		current.Command[0],
		current.Command[1:]...,
	)

	output, err := command.CombinedOutput()
	if len(output) > maxExecOutputBytes {
		output = output[:maxExecOutputBytes]
	}

	r.record.event(ctx, "exec.output", map[string]any{
		fieldLabel: current.Label,
		"output":   string(output),
	})

	if err != nil {
		return ctxerrors.Wrap(err, "run the exec command")
	}

	return nil
}

// killPoint picks the tool call that triggers a kill, drawing from the
// seeded source when the kill gives a range.
func (r *runner) killPoint(spec *kill) int {
	if spec == nil || spec.OnFile != "" {
		return 0
	}

	if spec.AfterToolUsesMax <= spec.AfterToolUses {
		return spec.AfterToolUses
	}

	r.randomMu.Lock()
	defer r.randomMu.Unlock()

	span := spec.AfterToolUsesMax - spec.AfterToolUses + 1

	return spec.AfterToolUses + r.random.IntN(span)
}

func (r *runner) awaitIdle(ctx context.Context, current step) error {
	timeout := secondsOr(current.TimeoutSeconds, defaultIdleTimeout)
	settle := secondsOr(current.SettleSeconds, defaultIdleSettle)
	deadline := time.Now().Add(timeout)
	idleSince := time.Time{}

	for time.Now().Before(deadline) {
		isBusy, err := r.isBusy(ctx, r.sessions[current.Session])

		switch {
		case err != nil || isBusy:
			idleSince = time.Time{}
		case idleSince.IsZero():
			idleSince = time.Now()
		case time.Since(idleSince) >= settle:
			return nil
		}

		if err := sleep(ctx, idlePollInterval); err != nil {
			return err
		}
	}

	return ctxerrors.Wrap(commerr.ErrTimeout, "session never went idle")
}

// isBusy reports whether the session runs a turn or a child agent. A child
// can outlive the turn that launched it and wake the root when it ends.
func (r *runner) isBusy(
	ctx context.Context,
	sessionID uuid.UUID,
) (bool, error) {
	session, err := r.client.GetSession(ctx, sessionID)
	if err != nil {
		return false, ctxerrors.Wrap(err, "read the session")
	}

	if session.ActiveTurn {
		return true, nil
	}

	response, err := r.sessionRequest(
		ctx,
		http.MethodGet,
		runningAgentsPath,
		nil,
		sessionID,
	)
	if err != nil {
		return false, err
	}

	defer func() {
		_ = response.Body.Close() // Fully read below, or the status failed.
	}()

	if response.StatusCode != http.StatusOK {
		return false, ctxerrors.Wrapf(
			commerr.ErrFetchFailed,
			"agent runs answered %d",
			response.StatusCode,
		)
	}

	page := agentRunPage{}
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		return false, ctxerrors.Wrap(err, "decode the agent runs")
	}

	return len(page.Agents) > 0, nil
}

// agentRunPage reads only how many runs GET /api/v1/session/agents listed.
type agentRunPage struct {
	Agents []json.RawMessage `json:"agents"`
}

// sessionRequest sends one session-scoped API request with the session
// header and, when configured, the bearer token.
func (r *runner) sessionRequest(
	ctx context.Context,
	method, path string,
	body []byte,
	sessionID uuid.UUID,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		r.baseURL+path,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build the request")
	}

	if body != nil {
		request.Header.Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeJSON,
		)
	}

	request.Header.Set(headerSessionID, sessionID.String())

	if r.token != "" {
		request.Header.Set(
			aichteeteapee.HeaderNameAuthorization,
			bearerScheme+r.token,
		)
	}

	response, err := r.httpClient.Do(request)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "%s %s", method, path)
	}

	return response, nil
}

// noticeRequest is the body of POST /api/v1/session/notices.
type noticeRequest struct {
	Type     string         `json:"type"`
	Summary  string         `json:"summary"`
	Data     map[string]any `json:"data,omitempty"`
	Delivery string         `json:"delivery,omitempty"`
}

func (r *runner) postEvent(ctx context.Context, current step) error {
	body, err := json.Marshal(noticeRequest{
		Type:     current.Type,
		Summary:  current.Summary,
		Data:     current.Data,
		Delivery: current.Delivery,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode the notice")
	}

	response, err := r.sessionRequest(
		ctx,
		http.MethodPost,
		noticesPath,
		body,
		r.sessions[current.Session],
	)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close() // The status is all the driver reads.
	}()

	r.record.event(ctx, "notice.posted", map[string]any{
		"type":   current.Type,
		"status": response.StatusCode,
	})

	if response.StatusCode >= http.StatusBadRequest {
		return ctxerrors.Wrapf(
			commerr.ErrPublishFailed,
			"notice answered %d",
			response.StatusCode,
		)
	}

	return nil
}

func (r *runner) openLiveFile() (*os.File, error) {
	r.liveMu.Lock()
	r.liveCount++
	index := r.liveCount
	r.liveMu.Unlock()

	path := filepath.Join(
		r.outputDir,
		liveFilePrefix+strconv.Itoa(index)+jsonlSuffix,
	)

	file, err := os.Create(path)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create the live frames file")
	}

	return file, nil
}

func secondsOr(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}

	return time.Duration(seconds) * time.Second
}

func sleep(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctxerrors.Wrap(ctx.Err(), "sleep")
	case <-time.After(duration):
		return nil
	}
}

func logRecovered(ctx context.Context, recovered any) {
	if recovered == nil {
		return
	}

	ctxscope.GetLogger(ctx).Error("driver goroutine panic", "panic", recovered)
}

func isTurnEnd(frameType string) bool {
	switch frameType {
	case frameTurnCompleted, frameTurnFailed, frameTurnCancelled:
		return true
	}

	return false
}

func sendData(current step) wsapi.MessageSendData {
	return wsapi.MessageSendData{Message: current.Message}
}

func isPlanned(err error) bool {
	return errors.Is(err, errDisconnect) || errors.Is(err, errTurnEnded)
}
