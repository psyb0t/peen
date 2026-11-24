package wsapi

import (
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
)

// MarshalJSON preserves an explicitly supplied empty schema object.
func (v MessageSendData) MarshalJSON() ([]byte, error) {
	type plain MessageSendData

	var value any = plain(v)
	if v.OutputSchema != nil {
		value = struct {
			plain
			OutputSchema *map[string]any `json:"outputSchema"`
		}{plain: plain(v), OutputSchema: &v.OutputSchema}
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode message turn settings")
	}

	return encoded, nil
}
