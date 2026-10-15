package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidExecutionFinishStepID(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		step  string
		valid bool
	}{
		{step: "finish", valid: true},
		{step: "finish:1", valid: true},
		{step: "finish:12", valid: true},
		{step: "finish:0"},
		{step: "finish:-1"},
		{step: "finish:+1"},
		{step: "finish:01"},
		{step: "finish:"},
		{step: "other:1"},
		{step: "finish:1:2"},
		{step: "finish: 1"},
		{step: "finish:999999999999999999999999"},
	}
	for _, tc := range testCases {
		t.Run(tc.step, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.valid, validExecutionFinishStepID(tc.step))
		})
	}
}
