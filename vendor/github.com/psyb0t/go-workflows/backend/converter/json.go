package converter

import (
	"encoding/json"

	"github.com/psyb0t/ctxerrors"

	"github.com/psyb0t/go-workflows/backend/payload"
)

type jsonConverter struct{}

func (jc *jsonConverter) To(v interface{}) (payload.Payload, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode workflow payload")
	}
	return data, nil
}

func (jc *jsonConverter) From(data payload.Payload, vptr interface{}) error {
	if err := json.Unmarshal(data, vptr); err != nil {
		return ctxerrors.Wrap(err, "decode workflow payload")
	}
	return nil
}
