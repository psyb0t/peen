package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// A preCompact hook marks a candidate, not an outbound summary request.
func (s *sendState) watchCompaction(ctx, watchCtx context.Context) {
	defer s.killDone.Done()

	sessionID := s.runner.sessions[s.current.Session]
	delay := time.Duration(s.current.Kill.DelayMilliseconds) * time.Millisecond

	var (
		candidate uuid.UUID
		observed  time.Time
	)

	for watchCtx.Err() == nil {
		callID, err := s.runner.runningCompaction(watchCtx, sessionID)
		if err != nil {
			if watchCtx.Err() != nil {
				return
			}

			s.runner.record.event(ctx, "kill.failed", map[string]any{
				fieldErr: err.Error(),
			})

			return
		}

		if callID != candidate {
			candidate, observed = callID, time.Now()
		}

		if watchCtx.Err() == nil && candidate != uuid.Nil &&
			time.Since(observed) >= delay {
			s.killCompaction(ctx, sessionID, candidate)

			return
		}

		if err := sleep(watchCtx, killFilePoll); err != nil {
			return
		}
	}
}

func (s *sendState) killCompaction(ctx context.Context,
	sessionID, callID uuid.UUID,
) {
	s.runner.record.event(ctx, "kill.compactionObserved", map[string]any{
		"modelCallId": callID, "sessionId": sessionID,
	})
	s.mu.Lock()
	s.isKilled = true
	s.mu.Unlock()

	if err := s.runner.control.kill(ctx, s.current.Kill.Target); err != nil {
		s.runner.record.event(ctx, "kill.failed", map[string]any{
			fieldErr: err.Error(),
		})
	}
}

func (r *runner) runningCompaction(ctx context.Context,
	sessionID uuid.UUID,
) (uuid.UUID, error) {
	for offset := 0; ; {
		page := api.ModelRunPage{}

		path := fmt.Sprintf(compactionRunsPath, offset)
		if err := r.readModelPage(ctx, sessionID, path, &page); err != nil {
			return uuid.Nil, err
		}

		for _, run := range page.ModelRuns {
			if run.AgentRunId != nil {
				continue
			}

			id, err := r.runningModelCall(ctx, sessionID, run.Id)
			if err != nil || id != uuid.Nil {
				return id, err
			}
		}

		if !page.HasMore || len(page.ModelRuns) == 0 {
			return uuid.Nil, nil
		}

		offset += len(page.ModelRuns)
	}
}

func (r *runner) runningModelCall(ctx context.Context,
	sessionID, runID uuid.UUID,
) (uuid.UUID, error) {
	for offset := 0; ; {
		page := api.ModelCallPage{}

		path := fmt.Sprintf(modelCallsPath, runID, offset)
		if err := r.readModelPage(ctx, sessionID, path, &page); err != nil {
			return uuid.Nil, err
		}

		for _, call := range page.Calls {
			if call.State == api.ModelCallStateRunning {
				return call.Id, nil
			}
		}

		if !page.HasMore || len(page.Calls) == 0 {
			return uuid.Nil, nil
		}

		offset += len(page.Calls)
	}
}

func (r *runner) readModelPage(ctx context.Context,
	sessionID uuid.UUID, path string, page any,
) error {
	response, err := r.sessionRequest(ctx, http.MethodGet, path, nil, sessionID)
	if err != nil {
		return err
	}
	defer func() {
		_ = response.Body.Close() // Fully read below, or the status failed.
	}()

	if response.StatusCode != http.StatusOK {
		return ctxerrors.Wrapf(commerr.ErrFetchFailed,
			"model observer answered %d", response.StatusCode)
	}

	if err := json.NewDecoder(response.Body).Decode(page); err != nil {
		return ctxerrors.Wrap(err, "decode model observer page")
	}

	return nil
}
