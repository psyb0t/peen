package cronconfig

import "github.com/robfig/cron/v3"

func newScheduleParser() cron.Parser {
	return cron.NewParser(
		cron.SecondOptional |
			cron.Minute |
			cron.Hour |
			cron.Dom |
			cron.Month |
			cron.Dow |
			cron.Descriptor,
	)
}
