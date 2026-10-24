package contentblob

import (
	"context"
	"encoding/json"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/go-workflows/backend/converter"
	"github.com/psyb0t/go-workflows/backend/payload"
)

const (
	// DefaultCacheBytes bounds the blob bytes one cache keeps in memory. A
	// workflow replays its whole history on every task, and a store loads the
	// same contexts again and again, so the same blobs are read repeatedly.
	DefaultCacheBytes = 64 << 20

	// converterStoreTimeout bounds one blob put or get. The SDK's converter
	// interface carries no context.
	converterStoreTimeout = time.Minute
)

// Store persists blobs. Putting a blob that is already stored is a no-op.
type Store interface {
	PutContentBlobs(ctx context.Context, blobs []Blob) error
	GetContentBlobs(ctx context.Context, hashes []string) (
		map[string][]byte, error)
}

// Converter is a go-workflows payload converter that stores repeated parts
// once. Workflow inputs and results repeat the turn context in every event,
// and a compacted payload holds each repeat as a hash.
type Converter struct {
	store Store
	cache *Cache
	fetch FetchFunc
}

var _ converter.Converter = (*Converter)(nil)

// NewConverter returns a converter backed by store.
func NewConverter(store Store) *Converter {
	c := &Converter{store: store, cache: NewCache(DefaultCacheBytes)}
	c.fetch = CachedFetch(c.cache, c.load)

	return c
}

// To encodes v as JSON, stores its repeated parts, and returns the compacted
// payload. Blobs this converter already stored or read are not stored again.
func (c *Converter) To(v any) (payload.Payload, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode workflow payload")
	}

	compacted, blobs := Compact(encoded)

	missing := make([]Blob, 0, len(blobs))
	for _, blob := range blobs {
		if !c.cache.Contains(blob.Hash) {
			missing = append(missing, blob)
		}
	}

	if len(missing) != 0 {
		ctx, cancel := context.WithTimeout(context.Background(),
			converterStoreTimeout)
		defer cancel()

		if err := c.store.PutContentBlobs(ctx, missing); err != nil {
			return nil, ctxerrors.Wrap(err, "store workflow payload blobs")
		}

		for _, blob := range missing {
			c.cache.Add(blob.Hash, blob.Data)
		}
	}

	return compacted, nil
}

// From restores a payload To wrote, or a plain JSON payload, into vptr.
func (c *Converter) From(data payload.Payload, vptr any) error {
	expanded, err := Expand(data, c.fetch)
	if err != nil {
		return ctxerrors.Wrap(err, "expand workflow payload")
	}

	if err := json.Unmarshal(expanded, vptr); err != nil {
		return ctxerrors.Wrap(err, "decode workflow payload")
	}

	return nil
}

func (c *Converter) load(hashes []string) (map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(),
		converterStoreTimeout)
	defer cancel()

	stored, err := c.store.GetContentBlobs(ctx, hashes)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workflow payload blobs")
	}

	return stored, nil
}
