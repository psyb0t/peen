// Package modeljson encodes JSON that a model reads as text.
package modeljson

import (
	"bytes"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
)

// Marshal encodes value like json.Marshal, except that it leaves <, > and &
// as they are. A model reads a tool result or an event as text: an escaped
// < in a file it was shown does not match the file when the model
// copies it into an edit, and every escape costs tokens. Callers that place
// the result inside a tagged block escape that block's own tags themselves.
func Marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer

	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); err != nil {
		return nil, ctxerrors.Wrap(err, "encode model-facing json")
	}

	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
