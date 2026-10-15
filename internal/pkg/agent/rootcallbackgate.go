package agent

import (
	"context"
	"sync"

	"github.com/psyb0t/ctxerrors"
)

type rootCallbackGate struct {
	mutex   sync.Mutex
	changed chan struct{}
	readers int
	writing bool
}

func (g *rootCallbackGate) acquire(
	ctx context.Context,
	exclusive bool,
) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, ctxerrors.Wrap(err, "admit root callback")
		}

		g.mutex.Lock()
		if g.changed == nil {
			g.changed = make(chan struct{})
		}

		if !g.writing && (!exclusive || g.readers == 0) {
			if exclusive {
				g.writing = true
			} else {
				g.readers++
			}

			g.mutex.Unlock()

			return sync.OnceFunc(func() { g.release(exclusive) }), nil
		}

		changed := g.changed
		g.mutex.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctxerrors.Wrap(ctx.Err(),
				"wait for root callback scope")
		case <-changed:
		}
	}
}

func (g *rootCallbackGate) release(exclusive bool) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if exclusive {
		g.writing = false
	} else {
		g.readers--
	}

	close(g.changed)
	g.changed = make(chan struct{})
}
