package contextvalue

import "github.com/psyb0t/go-workflows/internal/sync"

type logScopeKey struct{}

func WithLogScope(ctx sync.Context, data []byte) sync.Context {
	return sync.WithValue(ctx, logScopeKey{}, append([]byte(nil), data...))
}

func LogScope(ctx sync.Context) []byte {
	data, ok := ctx.Value(logScopeKey{}).([]byte)
	if !ok {
		return []byte("{}")
	}
	return append([]byte(nil), data...)
}
