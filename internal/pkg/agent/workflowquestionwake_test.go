package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetainedQuestionWakeHonorsPauseHandlerAndDisabledBus(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                             string
		paused, held, disabled, orphaned bool
	}{
		{name: "unknown child", orphaned: true},
		{name: "paused child", paused: true},
		{name: "configured queue handler", held: true},
		{name: "disabled event bus", disabled: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, input, _ := childQuestionPhaseFixture(t, "1h")
			pre, err := fixture.engine.toolPhase(t.Context(), input)
			require.NoError(t, err)
			input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
			result, err := fixture.engine.toolPhase(t.Context(), input)
			require.NoError(t, err)
			endQuestionParent(t, fixture)
			runtime := fixture.base.runtime
			if tc.paused {
				_, err = fixture.base.store.PauseExecution(t.Context(), fixture.input.SessionID, input.Tool.Round.Execution.ExecutionID, "keep child paused")
				require.NoError(t, err)
			}
			if tc.held {
				writeRuntimeFile(t, filepath.Join(fixture.base.configDirectory, ".agents", "events", events.TypeAgentQuestion+".md"), "---\ntype: agent.question\ndelivery: queue\n---\nKeep child questions queued.")
			}
			if tc.disabled {
				runtime.eventBus = nil
			}
			if tc.orphaned {
				data, err := json.Marshal(events.AgentNoticeData{RunID: uuid.New(), Message: "orphaned question"})
				require.NoError(t, err)
				repo := repositories.Use(fixture.base.handle.GormDB).SessionNotice
				noticeID := result.Output.ChildProjection.Transcript.Question.EventID
				_, err = repo.WithContext(t.Context()).Where(repo.ID.Eq(noticeID)).UpdateSimple(repo.DataJSON.Value(string(data)))
				require.NoError(t, err)
			}

			var calls atomic.Int64
			runtime.SetWakeRunner(func(context.Context, uuid.UUID, MessageRequest) error {
				calls.Add(1)

				return nil
			})
			startQuestionWakeWorker(t, fixture, runtime)
			require.Eventually(t, func() bool {
				runtime.lifecycle.mutex.Lock()
				defer runtime.lifecycle.mutex.Unlock()

				return len(runtime.lifecycle.operations) == 0
			}, 5*time.Second, 10*time.Millisecond)
			assert.Zero(t, calls.Load())
			noticeID := result.Output.ChildProjection.Transcript.Question.EventID
			page, err := fixture.base.store.ListSessionNotices(t.Context(), fixture.input.SessionID, session.ListSessionNoticesOptions{ID: &noticeID})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
		})
	}
}
