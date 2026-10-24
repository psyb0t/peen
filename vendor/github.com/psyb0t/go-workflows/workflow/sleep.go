package workflow

import (
	"time"

	"github.com/psyb0t/ctxerrors"
)

// Sleep sleeps for the given duration.
func Sleep(ctx Context, d time.Duration) error {
	_, err := ScheduleTimer(ctx, d, WithTimerName("Sleep")).Get(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "execute sleep")
	}

	return nil
}
