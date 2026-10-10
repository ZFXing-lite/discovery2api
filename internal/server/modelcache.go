package server

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// ModelCache shields /v1/models from upstream instability. The upstream
// /v1/models endpoint is used both by clients and by our own health probing;
// when upstream blips (timeouts, 5xx, 429), a cacheless gateway turns each
// client /v1/models call into a direct upstream failure.
//
// Design:
//   - get(): serve last-good body immediately if fresh enough (max-stale).
//     If empty, exactly one synchronous fetch runs (singleflight) and the
//     caller waits on it — cold start must not serve garbage.
//   - background loop (Start): refresh every refresh-interval; on failure
//     retry with capped exponential backoff instead of hammering upstream.
//   - A failed refresh NEVER drops the last-good body (stale-while-error).
//   - The probe loop feeds probe results into the cache via Set (the probe
//     itself fetches /v1/models, so its 2xx body is free cache data).
type ModelCache struct {
	mu       sync.Mutex
	body     []byte
	age      time.Time // when body was fetched
	lastOK   time.Time // last successful refresh (any status 200)
	lastErr  error
	lastStat int // last refresh status (0 = network error)
	sfmu     sync.Mutex
	sfdone   chan struct{} // non-nil while a synchronous fetch is in flight
	fetch    func() (body []byte, status int, err error)
}

// NewModelCache builds a cache with a nil fetch. New (server) installs a real
// fetcher right after construction; tests install their own via SetFetch.
// A nil fetch makes doRefresh a safe no-op that keeps stale data.
func NewModelCache() *ModelCache {
	return &ModelCache{}
}

// SetFetch replaces the fetch function (used by New to install a
// transport-aware fetcher, and by tests).
func (c *ModelCache) SetFetch(f func() ([]byte, int, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetch = f
}

// Set stores a 2xx body obtained by an external fetcher (the health probe).
func (c *ModelCache) Set(body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = body
	c.age = time.Now()
	c.lastOK = time.Now()
	c.lastStat = 200
	c.lastErr = nil
}

// get returns the cached body. When the cache is cold, at most one fetch
// runs (singleflight) and EVERY caller — including the one that launched
// it — waits for the result. A failed fetch leaves the cache empty and
// returns the error to all waiters.
func (c *ModelCache) get() ([]byte, error) {
	c.mu.Lock()
	if len(c.body) > 0 {
		b := c.body
		c.mu.Unlock()
		return b, nil
	}
	c.mu.Unlock()

	// Cold cache: single synchronous fetch (singleflight).
	c.sfmu.Lock()
	launched := false
	if c.sfdone == nil {
		c.sfdone = make(chan struct{})
		launched = true
	}
	ch := c.sfdone
	c.sfmu.Unlock()
	if launched {
		go func() {
			defer close(ch)
			c.doRefresh()
			c.sfmu.Lock()
			c.sfdone = nil
			c.sfmu.Unlock()
		}()
	}
	<-ch
	c.mu.Lock()
	if len(c.body) > 0 {
		b := c.body
		c.mu.Unlock()
		return b, nil
	}
	err := c.lastErr
	stat := c.lastStat
	c.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("cache cold and refresh failed (status=%d)", stat)
	}
	return nil, err
}

// doRefresh performs one fetch and stores the result (success or last-good).
func (c *ModelCache) doRefresh() {
	body, status, err := c.fetch()
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.lastStat = status
	c.lastErr = err
	if err == nil && status == 200 && len(body) > 0 {
		c.body = body
		c.age = now
		c.lastOK = now
		return
	}
	// Failure: keep last-good body, record why.
	log.Printf("model cache refresh failed status=%d err=%v", status, err)
}

// Age returns how old the cached body is (zero if none).
func (c *ModelCache) Age() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.age.IsZero() {
		return 0
	}
	return time.Since(c.age)
}

// LastOK returns when the last successful refresh happened.
func (c *ModelCache) LastOK() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastOK
}

// State describes the cache for the panel: age of data, refresh health.
func (c *ModelCache) State() (age time.Duration, lastOK time.Time, lastErr error, lastStat int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	age = 0
	if !c.age.IsZero() {
		age = time.Since(c.age)
	}
	return age, c.lastOK, c.lastErr, c.lastStat
}

// Start launches the background refresh loop. It refreshes every interval;
// on failure it retries with capped exponential backoff (base → 10min max)
// so an unstable upstream is not hammered every interval.
func (c *ModelCache) Start(interval time.Duration) {
	go func() {
		backoff := interval
		for {
			c.doRefresh()
			c.mu.Lock()
			ok := c.lastStat == 200 && c.lastErr == nil
			c.mu.Unlock()
			if ok {
				backoff = interval
			} else {
				backoff *= 2
				if backoff > 10*time.Minute {
					backoff = 10 * time.Minute
				}
			}
			select {
			case <-time.After(backoff):
			}
		}
	}()
}
