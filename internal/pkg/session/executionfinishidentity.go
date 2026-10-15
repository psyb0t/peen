package session

import (
	"strconv"
	"strings"
)

const executionFinishStepPrefix = ExecutionFinishStepID + ":"

func validExecutionFinishStepID(step string) bool {
	if step == ExecutionFinishStepID {
		return true
	}

	value, found := strings.CutPrefix(step, executionFinishStepPrefix)
	if !found {
		return false
	}

	round, err := strconv.Atoi(value)
	if err != nil || round <= 0 {
		return false
	}

	return strconv.Itoa(round) == value
}
