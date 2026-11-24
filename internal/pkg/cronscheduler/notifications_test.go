package cronscheduler

import (
	"context"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type notificationSink struct {
	sent chan control.CronNotification
	err  error
}

func (s notificationSink) NotifyCron(
	_ context.Context, notification control.CronNotification,
) error {
	s.sent <- notification

	return s.err
}

func TestTelegramCronNotificationFailureKeepsRecordedOutcome(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	job := f.job()
	job.TelegramChatID = -123
	options := f.options(job)
	sink := notificationSink{
		sent: make(chan control.CronNotification, 1), err: commerr.ErrUnavailable,
	}
	options.Notifier = sink
	scheduler, err := New(t.Context(), options)
	require.NoError(t, err)
	t.Cleanup(scheduler.Close)
	f.clock.advance(time.Second)
	require.NoError(t, scheduler.step(t.Context()))
	input := nextInput(t, f)
	waitIdle(t, scheduler)
	var notification control.CronNotification

	select {
	case notification = <-sink.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled notification was not delivered")
	}
	assert.Equal(t, input.RequestID, notification.RunID)
	assert.Equal(t, input.SessionID, notification.SessionID)
	assert.EqualValues(t, -123, notification.ChatID)
	assert.Contains(t, notification.Text, "completed")
	runs, _, err := f.store.ListRuns(t.Context(), job.Name, 10, 0)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, models.CronRunStateCompleted, runs[0].State)
}
