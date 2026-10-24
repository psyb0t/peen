package history

import "github.com/psyb0t/go-workflows/backend/payload"

type TraceStartedAttributes struct {
	SpanID payload.Payload `json:"spanID"`
}
