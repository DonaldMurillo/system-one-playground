package sos

import (
	"container/list"
	"context"
	"sync"
	"time"
)

const semanticMemoMinConfidence = 0.95

type memoizedInterpretation struct {
	Candidate  string
	Confidence float64
	Expires    time.Time
}

type memoEntry struct {
	key   string
	value memoizedInterpretation
}

// InterpretationCache is a bounded, concurrency-safe cache for high-confidence
// structural interpretation decisions. Keys contain masked syntax, never literal values.
type InterpretationCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	items   map[string]*list.Element
	lru     *list.List
	flights map[string]*cacheFlight
}

type cacheFlight struct{ done chan struct{} }

func NewInterpretationCache(max int, ttl time.Duration) *InterpretationCache {
	if max <= 0 {
		max = 1024
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &InterpretationCache{max: max, ttl: ttl, items: map[string]*list.Element{}, lru: list.New(), flights: map[string]*cacheFlight{}}
}

// begin joins identical in-flight analyses without blocking unrelated keys.
// Staged entries become visible only after the owner validates the complete
// canonical program and releases the transaction.
func (c *InterpretationCache) begin(ctx context.Context, key string) (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	for {
		c.mu.Lock()
		flight := c.flights[key]
		if flight == nil {
			flight = &cacheFlight{done: make(chan struct{})}
			c.flights[key] = flight
			c.mu.Unlock()
			return func() {
				c.mu.Lock()
				delete(c.flights, key)
				close(flight.done)
				c.mu.Unlock()
			}, nil
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-flight.done:
		}
	}
}

func (c *InterpretationCache) get(key string) (memoizedInterpretation, bool) {
	if c == nil {
		return memoizedInterpretation{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el := c.items[key]
	if el == nil {
		return memoizedInterpretation{}, false
	}
	v := el.Value.(memoEntry).value
	if time.Now().After(v.Expires) {
		c.lru.Remove(el)
		delete(c.items, key)
		return memoizedInterpretation{}, false
	}
	c.lru.MoveToFront(el)
	return v, true
}

func (c *InterpretationCache) put(key, candidate string, confidence float64) {
	if c == nil || confidence < semanticMemoMinConfidence || candidate == "reject" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v := memoizedInterpretation{Candidate: candidate, Confidence: confidence, Expires: time.Now().Add(c.ttl)}
	if el := c.items[key]; el != nil {
		el.Value = memoEntry{key: key, value: v}
		c.lru.MoveToFront(el)
		return
	}
	el := c.lru.PushFront(memoEntry{key: key, value: v})
	c.items[key] = el
	for c.lru.Len() > c.max {
		last := c.lru.Back()
		delete(c.items, last.Value.(memoEntry).key)
		c.lru.Remove(last)
	}
}

func (c *InterpretationCache) remove(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el := c.items[key]; el != nil {
		c.lru.Remove(el)
		delete(c.items, key)
	}
}
