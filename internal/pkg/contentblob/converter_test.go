package contentblob

import (
	"context"
	"sync"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryStore struct {
	mutex  sync.Mutex
	blobs  map[string][]byte
	puts   int
	gets   int
	putErr error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{blobs: map[string][]byte{}}
}

func (s *memoryStore) PutContentBlobs(_ context.Context, blobs []Blob) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.putErr != nil {
		return s.putErr
	}

	s.puts++

	for _, blob := range blobs {
		s.blobs[blob.Hash] = blob.Data
	}

	return nil
}

func (s *memoryStore) GetContentBlobs(
	_ context.Context,
	hashes []string,
) (map[string][]byte, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.gets++

	found := map[string][]byte{}
	for _, hash := range hashes {
		if data, ok := s.blobs[hash]; ok {
			found[hash] = data
		}
	}

	return found, nil
}

func TestConverterRoundTripsWorkflowValues(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	converter := NewConverter(store)

	value := testConversation{
		SystemPrompt: longText("system"),
		Messages:     []testMessage{{Role: "user", Content: longText("ask")}},
	}

	encoded, err := converter.To(value)
	require.NoError(t, err)
	assert.True(t, IsCompacted(encoded))
	assert.Len(t, store.blobs, 2)

	// A fresh converter has an empty cache, so it must read from the store,
	// as a worker does after a restart.
	var decoded testConversation
	require.NoError(t, NewConverter(store).From(encoded, &decoded))
	assert.Equal(t, value, decoded)
}

func TestConverterDecodesPlainPayloads(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()

	var decoded map[string]int
	require.NoError(t, NewConverter(store).From([]byte(`{"a":1}`), &decoded))
	assert.Equal(t, map[string]int{"a": 1}, decoded)
	assert.Zero(t, store.gets)
}

func TestConverterStoresAndReadsEachBlobOnce(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	converter := NewConverter(store)
	value := []string{longText("repeated")}

	first, err := converter.To(value)
	require.NoError(t, err)

	second, err := converter.To(value)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, store.puts, "a cached blob is not stored again")

	reader := NewConverter(store)

	for range 3 {
		var decoded []string
		require.NoError(t, reader.From(first, &decoded))
		assert.Equal(t, value, decoded)
	}

	assert.Equal(t, 1, store.gets, "a cached blob is not read again")
}

func TestConverterReportsStoreFailures(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	store.putErr = errStoreOffline

	_, err := NewConverter(store).To([]string{longText("lost")})
	require.ErrorIs(t, err, store.putErr)

	compacted, _ := Compact(encode(t, []string{longText("missing")}))

	var decoded []string
	err = NewConverter(newMemoryStore()).From(compacted, &decoded)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestConverterIgnoresForgedStoreData(t *testing.T) {
	t.Parallel()

	compacted, blobs := Compact(encode(t, []string{longText("real")}))
	require.Len(t, blobs, 1)

	store := newMemoryStore()
	store.blobs[blobs[0].Hash] = []byte(`"forged"`)

	var decoded []string
	err := NewConverter(store).From(compacted, &decoded)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestCacheEvictsLeastRecentlyUsedOverBudget(t *testing.T) {
	t.Parallel()

	cache := NewCache(10)
	cache.Add("a", []byte("aaaa"))
	cache.Add("b", []byte("bbbb"))

	_, found := cache.Get("a")
	require.True(t, found)

	cache.Add("c", []byte("cccc"))

	assert.True(t, cache.Contains("a"), "a was used last, so it stays")
	assert.False(t, cache.Contains("b"), "b was the least recently used")
	assert.True(t, cache.Contains("c"))

	cache.Add("huge", make([]byte, 11))
	assert.False(t, cache.Contains("huge"), "a blob over the budget is skipped")
	assert.True(t, cache.Contains("c"))
}

func TestCachedFetchChecksEachBlobOnceAndDropsForgeries(t *testing.T) {
	t.Parallel()

	_, blobs := Compact(encode(t, []string{longText("real")}))
	require.Len(t, blobs, 1)

	genuine := blobs[0]
	forgedHash := hashOf([]byte(`"other"`))
	loads := 0

	fetch := CachedFetch(NewCache(DefaultCacheBytes),
		func([]string) (map[string][]byte, error) {
			loads++

			return map[string][]byte{
				genuine.Hash: genuine.Data,
				forgedHash:   []byte(`"not other"`),
			}, nil
		})

	for range 3 {
		found, err := fetch([]string{genuine.Hash})
		require.NoError(t, err)
		assert.Equal(t, genuine.Data, found[genuine.Hash])
	}

	assert.Equal(t, 1, loads, "a verified blob is answered from the cache")

	found, err := fetch([]string{forgedHash})
	require.NoError(t, err)
	assert.NotContains(t, found, forgedHash,
		"bytes that do not match their hash are dropped")
}
