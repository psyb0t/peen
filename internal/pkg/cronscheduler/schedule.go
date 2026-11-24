package cronscheduler

import (
	"time"

	"github.com/robfig/cron/v3"
)

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now().UTC()
}

func (wallClock) After(delay time.Duration) <-chan time.Time {
	return time.After(delay)
}

// latestOccurrence finds the last due tick without walking every missed
// second. Interval descriptors retain the persisted grid across restarts.
func latestOccurrence(
	schedule cron.Schedule,
	first, now time.Time,
) time.Time {
	if first.After(now) {
		return time.Time{}
	}

	if interval, ok := schedule.(cron.ConstantDelaySchedule); ok {
		ticks := int64(now.Sub(first) / interval.Delay)

		return first.Add(time.Duration(ticks * int64(interval.Delay)))
	}

	latest := first

	low, high := first.Unix(), now.Unix()
	for low <= high {
		middle := low + (high-low)/secondsPerSearchStep

		next := schedule.Next(time.Unix(middle, 0).UTC())
		if next.IsZero() || next.After(now) {
			high = middle - 1

			continue
		}

		latest = next
		low = middle + 1
	}

	return latest.UTC()
}
