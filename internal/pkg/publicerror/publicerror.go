// Package publicerror turns an error into text that is safe to show a client.
package publicerror

import "strings"

// locationMarker opens the "[file:line in func]" suffix ctxerrors appends to
// every wrapped error. Everything from here on is diagnostic detail for logs.
const locationMarker = " ["

// Message returns err's readable chain without the source locations ctxerrors
// appends. ctxerrors formats as "message: wrapped [file:line in func]", and
// nested wraps append their own marker, so every marker sits after the
// readable chain and the first one is where the text stops being safe to
// publish. A nil error gives an empty string.
func Message(err error) string {
	if err == nil {
		return ""
	}

	message := err.Error()
	if index := strings.Index(message, locationMarker); index >= 0 {
		message = message[:index]
	}

	return strings.TrimSpace(message)
}
