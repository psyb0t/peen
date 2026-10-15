package agent

import (
	"maps"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

type workflowHookState struct {
	Messages map[string][]string `json:"messages,omitempty"`
	Denials  map[string]string   `json:"denials,omitempty"`
}

func (h *toolHookRuntime) workflowState() *workflowHookState {
	if h == nil {
		return nil
	}

	h.mutex.Lock()
	defer h.mutex.Unlock()

	if len(h.messages) == 0 && len(h.denials) == 0 {
		return nil
	}

	return cloneWorkflowHookState(&workflowHookState{
		Messages: h.messages, Denials: h.denials,
	})
}

func (h *toolHookRuntime) restoreWorkflowState(state *workflowHookState) error {
	if h == nil {
		if state == nil {
			return nil
		}

		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"hook state requires a hook runtime")
	}

	if err := validateWorkflowHookState(state); err != nil {
		return ctxerrors.Wrap(err, "validate saved hook state")
	}

	retained := cloneWorkflowHookState(state)

	h.mutex.Lock()
	defer h.mutex.Unlock()

	h.messages, h.denials = retained.Messages, retained.Denials

	return nil
}

func cloneWorkflowHookState(state *workflowHookState) *workflowHookState {
	cloned := &workflowHookState{
		Messages: make(map[string][]string), Denials: make(map[string]string),
	}
	if state == nil {
		return cloned
	}

	for callID, messages := range state.Messages {
		cloned.Messages[callID] = append([]string(nil), messages...)
	}

	maps.Copy(cloned.Denials, state.Denials)

	return cloned
}

func validateWorkflowHookState(state *workflowHookState) error {
	if state == nil {
		return nil
	}

	for callID := range state.Messages {
		if strings.TrimSpace(callID) == "" {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"saved hook message has no call identity")
		}
	}

	for callID := range state.Denials {
		if strings.TrimSpace(callID) == "" {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"saved hook denial has no call identity")
		}
	}

	return nil
}
