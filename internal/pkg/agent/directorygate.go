package agent

import (
	"context"
	"sync"

	"github.com/psyb0t/ctxerrors"
)

// directoryGate lets ordinary tool calls share a scope while a hop waits
// for complete tool lifecycles, including hooks and context injection.
type directoryGate struct {
	mutex          sync.Mutex
	changed        chan struct{}
	readers        int
	waitingWriters int
	writer         bool
}

func (g *directoryGate) acquire(
	ctx context.Context,
	exclusive bool,
) (func(), error) {
	g.mutex.Lock()
	if g.changed == nil {
		g.changed = make(chan struct{})
	}

	if exclusive {
		g.waitingWriters++
	}

	for {
		if err := ctx.Err(); err != nil {
			if exclusive {
				g.waitingWriters--
				g.notifyLocked()
			}
			g.mutex.Unlock()

			return nil, ctxerrors.Wrap(err, "wait for directory scope")
		}

		if g.canEnter(exclusive) {
			if exclusive {
				g.waitingWriters--
				g.writer = true
			} else {
				g.readers++
			}
			g.mutex.Unlock()

			return g.release(exclusive), nil
		}

		changed := g.changed
		g.mutex.Unlock()

		select {
		case <-ctx.Done():
		case <-changed:
		}

		g.mutex.Lock()
	}
}

func (g *directoryGate) canEnter(exclusive bool) bool {
	if g.writer {
		return false
	}

	if exclusive {
		return g.readers == 0
	}

	return g.waitingWriters == 0
}

func (g *directoryGate) notifyLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *directoryGate) release(exclusive bool) func() {
	var once sync.Once

	return func() {
		once.Do(func() {
			g.mutex.Lock()
			defer g.mutex.Unlock()

			if exclusive {
				g.writer = false
			} else {
				g.readers--
			}

			g.notifyLocked()
		})
	}
}
