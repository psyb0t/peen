package cronscheduler

import "time"

const (
	pollInterval           = time.Second
	defaultMaxMessageBytes = 262144
	secondsPerSearchStep   = 2
	reasonRunPanicked      = "cronRunPanicked"
	reasonRecoveryPanicked = "cronRecoveryPanicked"
)
