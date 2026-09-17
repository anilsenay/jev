package jev

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// Cache stores responses by request key. Implementations must be safe for
// concurrent use and must treat stored responses as read-only.
type Cache interface {
	Get(key string) (*Response, bool)
	Set(key string, resp *Response)
}

// CacheMiddleware serves identical requests (same model, state and questions)
// from cache. Only successful responses are stored. Cached responses report
// the usage of the original request.
func CacheMiddleware(cache Cache) Middleware {
	return func(next Provider) Provider {
		return ProviderFunc(func(ctx context.Context, req *Request) (*Response, error) {
			key, err := cacheKey(req)
			if err != nil {
				return next.Evaluate(ctx, req)
			}
			if resp, ok := cache.Get(key); ok {
				return resp, nil
			}
			resp, err := next.Evaluate(ctx, req)
			if err == nil && resp != nil {
				cache.Set(key, resp)
			}
			return resp, err
		})
	}
}

// cacheKey hashes the request. encoding/json sorts map keys, so equal requests
// produce equal keys.
func cacheKey(req *Request) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// MemoryCache is an in-process LRU [Cache].
type MemoryCache struct {
	mu    sync.Mutex
	size  int
	order *list.List
	items map[string]*list.Element
}

type cacheEntry struct {
	key  string
	resp *Response
}

// NewMemoryCache returns an LRU cache holding up to size responses.
// A size below 1 defaults to 1024.
func NewMemoryCache(size int) *MemoryCache {
	if size < 1 {
		size = 1024
	}
	return &MemoryCache{size: size, order: list.New(), items: make(map[string]*list.Element)}
}

// Get implements [Cache].
func (c *MemoryCache) Get(key string) (*Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*cacheEntry).resp, true
}

// Set implements [Cache].
func (c *MemoryCache) Set(key string, resp *Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry).resp = resp
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&cacheEntry{key, resp})
	if c.order.Len() > c.size {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*cacheEntry).key)
	}
}
