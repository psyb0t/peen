package cache

import (
	"fmt"

	"github.com/psyb0t/go-workflows/core"
)

func getKey(instance *core.WorkflowInstance) string {
	return fmt.Sprintf("%s-%s", instance.InstanceID, instance.ExecutionID)
}
