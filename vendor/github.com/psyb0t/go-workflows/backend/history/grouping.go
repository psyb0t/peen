package history

import "github.com/psyb0t/go-workflows/core"

func EventsByWorkflowInstance(events []*WorkflowEvent) map[core.WorkflowInstance][]*WorkflowEvent {
	groupedEvents := make(map[core.WorkflowInstance][]*WorkflowEvent)

	for _, m := range events {
		instance := *m.WorkflowInstance

		if _, ok := groupedEvents[instance]; !ok {
			groupedEvents[instance] = []*WorkflowEvent{}
		}

		groupedEvents[instance] = append(groupedEvents[instance], m)
	}

	return groupedEvents
}
