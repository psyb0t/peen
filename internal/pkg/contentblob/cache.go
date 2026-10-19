package contentblob

import (
	"container/list"
	"sync"
)

// Cache keeps recently used blobs up to a total byte budget, evicting the
// least recently used first. A blob's bytes never change for its hash, so a
// cached blob is always right. It is safe for concurrent use.
type Cache struct {
	mutex   sync.Mutex
	budget  int
	used    int
	order   *list.List
	entries map[string]*list.Element
}

type cacheEntry struct {
	hash string
	data []byte
}

// NewCache returns a cache that holds at most budget bytes of blob data.
func NewCache(budget int) *Cache {
	return &Cache{
		budget:  budget,
		order:   list.New(),
		entries: map[string]*list.Element{},
	}
}

// Get returns the cached blob for hash.
func (c *Cache) Get(hash string) ([]byte, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	element, found := c.entries[hash]
	if !found {
		return nil, false
	}

	c.order.MoveToFront(element)

	entry, _ := element.Value.(cacheEntry)

	return entry.data, true
}

// Contains reports whether hash is cached.
func (c *Cache) Contains(hash string) bool {
	_, found := c.Get(hash)

	return found
}

// Add keeps data unless it alone exceeds the budget. Callers add only blobs
// they verified against their hash.
func (c *Cache) Add(hash string, data []byte) {
	if len(data) > c.budget {
		return
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if element, found := c.entries[hash]; found {
		c.order.MoveToFront(element)

		return
	}

	c.entries[hash] = c.order.PushFront(cacheEntry{hash: hash, data: data})
	c.used += len(data)

	for c.used > c.budget {
		oldest := c.order.Back()
		entry, _ := oldest.Value.(cacheEntry)

		c.order.Remove(oldest)
		delete(c.entries, entry.hash)
		c.used -= len(entry.data)
	}
}

// CachedFetch returns a FetchFunc that answers from cache and reads the rest
// through load, keeping only blobs whose bytes match their hash.
func CachedFetch(cache *Cache, load FetchFunc) FetchFunc {
	return func(hashes []string) (map[string][]byte, error) {
		found := make(map[string][]byte, len(hashes))
		missing := make([]string, 0, len(hashes))

		for _, hash := range hashes {
			if data, ok := cache.Get(hash); ok {
				found[hash] = data

				continue
			}

			missing = append(missing, hash)
		}

		if len(missing) == 0 {
			return found, nil
		}

		loaded, err := load(missing)
		if err != nil {
			return nil, err
		}

		for hash, data := range loaded {
			if !ValidBlob(hash, data) {
				continue
			}

			found[hash] = data
			cache.Add(hash, data)
		}

		return found, nil
	}
}
