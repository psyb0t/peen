package diag

import (
	"context"
	"fmt"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"

	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/core"
)

type instanceTreeBuilder struct {
	b            Backend
	instanceByID map[string]*WorkflowInstanceRef
}

func NewInstanceTreeBuilder(db Backend) *instanceTreeBuilder {
	return &instanceTreeBuilder{
		b:            db,
		instanceByID: map[string]*WorkflowInstanceRef{},
	}
}

func (itb *instanceTreeBuilder) BuildWorkflowInstanceTree(ctx context.Context, instance *core.WorkflowInstance) (*WorkflowInstanceTree, error) {
	instanceState, err := itb.getInstance(ctx, instance)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "getting instance")
	}

	// Get root instance of tree
	rootInstance, err := itb.getRoot(ctx, instanceState)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "getting root instance")
	}

	if rootInstance == nil {
		return nil, ctxerrors.Wrapf(commerr.ErrInvalidState, "no root instance found")
	}

	root := &WorkflowInstanceTree{
		WorkflowInstanceRef: rootInstance,
		Children:            []*WorkflowInstanceTree{},
	}

	s := []*WorkflowInstanceTree{root}
	for len(s) > 0 {
		node := s[0]
		s = s[1:]

		name, hasError, children, err := itb.getNameAndChildren(ctx, node.Instance)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "getting children of instance %s", node.Instance.InstanceID)
		}

		node.WorkflowName = name
		node.Error = hasError

		for _, child := range children {
			t := &WorkflowInstanceTree{
				WorkflowInstanceRef: child,
				Children:            []*WorkflowInstanceTree{},
			}

			// Enqueue
			s = append(s, t)

			// Add to current node
			node.Children = append(node.Children, t)
		}
	}

	return root, nil
}

func (itb *instanceTreeBuilder) getRoot(ctx context.Context, instanceRef *WorkflowInstanceRef) (*WorkflowInstanceRef, error) {
	parentInstance := instanceRef.Instance.Parent
	for parentInstance != nil {
		var err error
		instanceRef, err = itb.getInstance(ctx, parentInstance)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "execute tree")
		}

		parentInstance = instanceRef.Instance.Parent
	}

	return instanceRef, nil
}

func (itb *instanceTreeBuilder) getNameAndChildren(ctx context.Context, instance *core.WorkflowInstance) (string, bool, []*WorkflowInstanceRef, error) {
	h, err := itb.b.GetWorkflowInstanceHistory(ctx, instance, nil)
	if err != nil {
		return "", false, nil, ctxerrors.Wrap(err, "getting instance history")
	}

	workflowName := ""
	hasError := false

	var children []*WorkflowInstanceRef
	for _, event := range h {
		switch event.Type {
		case history.EventType_SubWorkflowScheduled:
			childInstance, err := itb.getInstance(ctx, event.Attributes.(*history.SubWorkflowScheduledAttributes).SubWorkflowInstance)
			if err != nil {
				return "", false, nil, ctxerrors.Wrap(err, "getting child instance")
			}

			children = append(children, childInstance)

		case history.EventType_WorkflowExecutionStarted:
			workflowName = event.Attributes.(*history.ExecutionStartedAttributes).Name

		case history.EventType_WorkflowExecutionFinished:
			// Check if the workflow finished with an error
			if attrs, ok := event.Attributes.(*history.ExecutionCompletedAttributes); ok && attrs.Error != nil {
				hasError = true
			}
		}
	}

	return workflowName, hasError, children, nil
}

func (itb *instanceTreeBuilder) getInstance(ctx context.Context, instance *core.WorkflowInstance) (*WorkflowInstanceRef, error) {
	instanceKey := fmt.Sprintf("%s:%s", instance.InstanceID, instance.ExecutionID)

	if instanceRef, ok := itb.instanceByID[instanceKey]; ok {
		return instanceRef, nil
	}

	instanceRef, err := itb.b.GetWorkflowInstance(ctx, instance)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "execute tree")
	}
	if instanceRef == nil {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "workflow tree instance not found")
	}

	itb.instanceByID[instanceKey] = instanceRef

	return instanceRef, nil
}
