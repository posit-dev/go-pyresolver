// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"container/list"
	"sync"

	"golang.org/x/sync/singleflight"
)

// boundedCache is an LRU cache with an explicit byte budget and singleflight
// coalescing of concurrent misses.
//
// # Why byte accounting is caller-supplied rather than delegated to ristretto
//
// A general-purpose cache cannot size an arbitrary Go value. PPM learned this
// the hard way: its shared ristretto cache cannot size a non-[]byte value, so
// it admits Go objects at cost 0 and they escape the byte budget entirely
// (rstudio/package-manager#19374). PPM's own cachehelpers.BoundedCache exists
// for exactly that reason and takes a caller-supplied sizer. This does the
// same, which also spares a public library a cache dependency.
//
// # Safety contract
//
// An entry is only sound to cache if its key names IMMUTABLE content. A key
// that can describe two different payloads over time will serve a stale one
// until eviction, and no TTL makes that correct -- it only makes the window
// shorter. See CachedJSONIndex for how its key satisfies this, and for the one
// field that does not.
//
// # Copy contract
//
// This cache shares values by reference with every reader, and Get hands the
// SAME value to every goroutine coalesced into one singleflight flight.
// Callers that hand values onward to code which may mutate them must copy on
// BOTH paths -- see CachedJSONIndex.Files.
type boundedCache[V any] struct {
	maxEntries int
	maxBytes   int64
	sizeOf     func(V) int64

	sf singleflight.Group

	mu         sync.Mutex
	entries    map[string]*list.Element
	lru        *list.List // front = most recently used
	totalBytes int64
}

type cacheEntry[V any] struct {
	key   string
	value V
	bytes int64
}

func newBoundedCache[V any](maxEntries int, maxBytes int64, sizeOf func(V) int64) *boundedCache[V] {
	return &boundedCache[V]{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		sizeOf:     sizeOf,
		entries:    make(map[string]*list.Element),
		lru:        list.New(),
	}
}

// lookup returns the cached value for key, promoting it to most-recently-used.
func (c *boundedCache[V]) lookup(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.lru.MoveToFront(el)

	return el.Value.(*cacheEntry[V]).value, true
}

// put stores value under key, evicting least-recently-used entries until both
// budgets are satisfied.
func (c *boundedCache[V]) put(key string, value V) {
	size := c.sizeOf(value)

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[key]; ok {
		existing := el.Value.(*cacheEntry[V])
		c.totalBytes -= existing.bytes
		existing.value = value
		existing.bytes = size
		c.totalBytes += size
		c.lru.MoveToFront(el)
		c.evictLocked()
		return
	}

	// An entry larger than the whole budget is not cached at all. Storing it
	// would evict everything else and then still not fit.
	if c.maxBytes > 0 && size > c.maxBytes {
		return
	}

	c.entries[key] = c.lru.PushFront(&cacheEntry[V]{key: key, value: value, bytes: size})
	c.totalBytes += size
	c.evictLocked()
}

// evictLocked drops least-recently-used entries until both budgets hold.
// Callers must hold c.mu.
func (c *boundedCache[V]) evictLocked() {
	for c.lru.Len() > 0 {
		overEntries := c.maxEntries > 0 && c.lru.Len() > c.maxEntries
		overBytes := c.maxBytes > 0 && c.totalBytes > c.maxBytes
		if !overEntries && !overBytes {
			return
		}

		oldest := c.lru.Back()
		if oldest == nil {
			return
		}
		entry := oldest.Value.(*cacheEntry[V])
		c.lru.Remove(oldest)
		delete(c.entries, entry.key)
		c.totalBytes -= entry.bytes
	}
}

// get returns the cached value for key, building it with build on a miss.
// Concurrent misses for one key are coalesced into a single build.
//
// The returned value is NOT a copy -- see the copy contract on boundedCache.
func (c *boundedCache[V]) get(key string, build func() (V, error)) (V, error) {
	if v, ok := c.lookup(key); ok {
		return v, nil
	}

	res, err, _ := c.sf.Do(key, func() (any, error) {
		// Re-check under the flight: a concurrent flight for this key may have
		// completed and populated the cache between our miss and here.
		if v, ok := c.lookup(key); ok {
			return v, nil
		}

		built, err := build()
		if err != nil {
			return nil, err
		}
		c.put(key, built)
		return built, nil
	})
	if err != nil {
		var zero V
		return zero, err
	}

	v, ok := res.(V)
	if !ok {
		var zero V
		return zero, nil
	}
	return v, nil
}

// stats reports current occupancy, for tests and for a future metrics surface.
func (c *boundedCache[V]) stats() (entries int, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len(), c.totalBytes
}
