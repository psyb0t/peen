package history

import (
	"github.com/psyb0t/go-workflows/backend/metadata"
	"github.com/psyb0t/go-workflows/backend/payload"
	"github.com/psyb0t/go-workflows/core"
)

type ActivityScheduledAttributes struct {
	Name string `json:"name,omitempty"`

	Attempt int `json:"attempt,omitempty"`

	Inputs []payload.Payload `json:"inputs,omitempty"`

	Metadata *metadata.WorkflowMetadata `json:"metadata,omitempty"`

	Queue core.Queue `json:"queue,omitempty"`
}
