package control_test

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/memlistener"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInProcessEndpointCanBeSetAndCleared(t *testing.T) {
	t.Parallel()
	endpoint := &control.InProcessEndpoint{}
	assert.Nil(t, endpoint.Listener())
	listener := memlistener.New()
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	endpoint.Set(listener)
	assert.Same(t, listener, endpoint.Listener())
	endpoint.Clear()
	assert.Nil(t, endpoint.Listener())
}
