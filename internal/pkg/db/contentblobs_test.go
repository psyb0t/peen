package db

import (
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentBlobStoreRoundTripsValidBlobs(t *testing.T) {
	t.Parallel()

	handle, err := Open(t.Context(), Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	_, blobs := contentblob.Compact([]byte(`["` +
		strings.Repeat("stored text ", 100) + `"]`))
	require.Len(t, blobs, 1)

	store := handle.ContentBlobs()
	require.NoError(t, store.PutContentBlobs(t.Context(), blobs))
	require.NoError(t, store.PutContentBlobs(t.Context(), blobs))

	found, err := store.GetContentBlobs(t.Context(),
		[]string{blobs[0].Hash, strings.Repeat("0", 64)})
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{blobs[0].Hash: blobs[0].Data}, found)

	forged := []contentblob.Blob{{Hash: blobs[0].Hash, Data: []byte(`"x"`)}}
	require.ErrorIs(t, store.PutContentBlobs(t.Context(), forged),
		commerr.ErrValidationFailed)

	_, err = store.GetContentBlobs(t.Context(),
		make([]string, maximumContentBlobRequest+1))
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
