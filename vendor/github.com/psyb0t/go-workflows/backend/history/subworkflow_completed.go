package history

import "github.com/psyb0t/go-workflows/backend/payload"

type SubWorkflowCompletedAttributes struct {
	Result payload.Payload `json:"result,omitempty"`
}
