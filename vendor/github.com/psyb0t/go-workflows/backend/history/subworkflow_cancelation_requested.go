package history

import (
	"github.com/psyb0t/go-workflows/core"
)

type SubWorkflowCancellationRequestedAttributes struct {
	SubWorkflowInstance *core.WorkflowInstance `json:"sub_workflow_instance,omitempty"`
}
