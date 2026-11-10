// Package publicerror turns an error into text that is safe to show a client.
package publicerror

import (
	"errors"
	"strings"
)

// locationMarker opens the "[file:line in func]" suffix ctxerrors appends to
// every wrapped error. Everything from here on is diagnostic detail for logs.
const locationMarker = " ["

// Public is an error whose own text is written for the person who reads it.
// Message returns that text instead of the chain around it, so a limit or
// refusal reads the same wherever it is reported.
type Public interface {
	error
	PublicMessage() string
}

// messageError carries public text over an error that keeps its detail.
type messageError struct {
	text  string
	cause error
}

func (e *messageError) Error() string {
	return e.text
}

// PublicMessage returns the text written for the reader.
func (e *messageError) PublicMessage() string {
	return e.text
}

func (e *messageError) Unwrap() error {
	return e.cause
}

// WithMessage returns an error whose public text is text and that still
// matches cause under errors.Is and errors.As.
func WithMessage(text string, cause error) error {
	return &messageError{text: text, cause: cause}
}

// Message returns err's readable chain without the source locations ctxerrors
// appends. An error in the chain that is Public gives its own text instead. A
// nil error gives an empty string.
func Message(err error) string {
	if err == nil {
		return ""
	}

	var public Public
	if errors.As(err, &public) {
		return public.PublicMessage()
	}

	return Text(err.Error())
}

// Text is Message for error text that was already rendered, such as a
// failure detail a record kept. ctxerrors formats as "message: wrapped
// [file:line in func]", and nested wraps append their own marker, so every
// marker sits after the readable chain and the first one is where the text
// stops being safe to publish.
func Text(message string) string {
	if index := strings.Index(message, locationMarker); index >= 0 {
		message = message[:index]
	}

	return strings.TrimSpace(message)
}
