package history

import "github.com/psyb0t/go-workflows/backend/payload"

type ExecutionContinuedAsNewAttributes struct {
	Result payload.Payload `json:"result,omitempty"`

	ContinuedExecutionID string `json:"continued_execution_id,omitempty"`
}
