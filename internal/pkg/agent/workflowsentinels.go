package agent

import (
	"errors"

	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/publicerror"
)

// The type names go-workflows records for the errors below. It keeps only an
// error's text and the name of its Go type when a failure crosses the
// workflow history, so the name is how a failure is recognized afterwards.
// workflowsentinels_test.go checks each against the real type.
const (
	workflowErrorTypeRoundLimit      = "roundLimitError"
	workflowErrorTypeTokenLimit      = "tokenLimitError"
	workflowErrorTypeProviderRefusal = "providerRefusalError"
)

// restoredWorkflowError is an error that crossed the workflow history, with
// the typed errors its chain recorded put back beside it. Its text is the
// original's. errors.Is and errors.As reach the restored errors.
type restoredWorkflowError struct {
	error
	restored []error
}

func (e *restoredWorkflowError) Unwrap() []error {
	return append([]error{e.error}, e.restored...)
}

// restoreWorkflowSentinels returns err with the typed errors go-workflows
// flattened to text and a type name put back. After the history round trip
// errors.Is no longer reaches a turn limit or a provider refusal, so the
// failure would read as an unknown one. An error with nothing to restore is
// returned as it is.
func restoreWorkflowSentinels(err error) error {
	if err == nil {
		return nil
	}

	var restored []error

	walkWorkflowErrors(err, func(node *workflow.Error) {
		if typed := restoredWorkflowType(node); typed != nil {
			restored = append(restored, typed)
		}
	})

	if len(restored) == 0 {
		return err
	}

	return &restoredWorkflowError{error: err, restored: restored}
}

func restoredWorkflowType(node *workflow.Error) error {
	switch node.Type {
	case workflowErrorTypeRoundLimit:
		return &roundLimitError{text: node.Message}
	case workflowErrorTypeTokenLimit:
		return &tokenLimitError{text: node.Message}
	case workflowErrorTypeProviderRefusal:
		return &providerRefusalError{text: node.Message}
	default:
		return nil
	}
}

// walkWorkflowErrors visits every workflow error in err's chain, through
// wrapped and joined errors and through each node's recorded causes.
func walkWorkflowErrors(err error, visit func(*workflow.Error)) {
	if err == nil {
		return
	}

	//nolint:errorlint // The chain is walked one link at a time.
	if node, isNode := err.(*workflow.Error); isNode {
		if node == nil {
			return
		}

		visit(node)
		walkWorkflowErrors(node.Cause, visit)

		for _, cause := range node.Causes {
			walkWorkflowErrors(cause, visit)
		}

		return
	}

	//nolint:errorlint // The chain is walked one link at a time.
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, inner := range wrapped.Unwrap() {
			walkWorkflowErrors(inner, visit)
		}
	case interface{ Unwrap() error }:
		walkWorkflowErrors(wrapped.Unwrap(), visit)
	}
}

// workflowFailureCause returns what a failed workflow reports as its error:
// the typed public error its chain holds, or the cause itself. A public error
// goes out alone so its type name is the one go-workflows records first.
func workflowFailureCause(cause error) error {
	var public publicerror.Public
	if errors.As(cause, &public) {
		return public
	}

	return cause
}
