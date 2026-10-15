package runtimecontext

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/psyb0t/ctxscope"
	"github.com/stretchr/testify/assert"
)

func TestWithLoggerFromPreservesDestinationScope(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		hasSource bool
	}{
		{name: "source logger", hasSource: true},
		{name: "missing source retains destination logger"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sourceRecords, destinationRecords bytes.Buffer
			destination := WithLogger(ctxscope.Set(t.Context(),
				ctxscope.Attr("session_id", "destination-scope")),
				slog.New(slog.NewJSONHandler(&destinationRecords, nil)))
			source := t.Context()
			used, unused := &destinationRecords, &sourceRecords
			if tc.hasSource {
				source = WithLogger(source,
					slog.New(slog.NewJSONHandler(&sourceRecords, nil)))
				used, unused = &sourceRecords, &destinationRecords
			}
			Logger(WithLoggerFrom(destination, source)).Info("copied logger")
			assert.Contains(t, used.String(), `"session_id":"destination-scope"`)
			assert.Empty(t, unused.String())
		})
	}
}
