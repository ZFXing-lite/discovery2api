package server

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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
//   - The last-good body is persisted to disk, so a restart (or a cold start
//     while upstream is unreachable) serves the previously known model list
//     instead of the static fallback catalog.
type ModelCache struct {
	mu       sync.Mutex
	body     []byte
	age      time.Time // when body was fetched
	lastOK   time.Time // last successful refresh (any status 200)
	lastErr  error
	lastStat int // last refresh status (0 = network error)
	path     string
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

// NewModelCacheWithPath is NewModelCache plus disk persistence: a previously
// saved body is loaded at construction and every successful refresh is written
// back. A missing/corrupt file is ignored (cold cache, as before).
func NewModelCacheWithPath(path string) *ModelCache {
	c := &ModelCache{path: path}
	c.loadFromDisk()
	return c
}

// loadFromDisk seeds the cache from the persisted body. Used only at startup.
func (c *ModelCache) loadFromDisk() {
	if c.path == "" {
		return
	}
	b, err := os.ReadFile(c.path)
	if err != nil || len(b) == 0 {
		return
	}
	st, statErr := os.Stat(c.path)
	c.mu.Lock()
	c.body = b
	c.lastStat = 200
	if statErr == nil {
		// The body is only as fresh as the file it came from.
		c.age = st.ModTime()
		c.lastOK = st.ModTime()
	}
	c.mu.Unlock()
	log.Printf("model cache warmed from disk path=%s bytes=%d age=%s", c.path, len(b), time.Since(st.ModTime()))
}

// persist writes body to disk atomically. Callers must not hold c.mu.
func (c *ModelCache) persist(body []byte) {
	path := c.path
	if path == "" || len(body) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("model cache persist: mkdir %s: %v", filepath.Dir(path), err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		log.Printf("model cache persist: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("model cache persist: rename %s: %v", tmp, err)
	}
}

// SetFetch replaces the fetch function (used by New to install a
// transport-aware fetcher, and by tests).
func (c *ModelCache) SetFetch(f func() ([]byte, int, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetch = f
}

// Set stores a 2xx body obtained by an external fetcher (the health probe)
// and persists it for the next process start.
func (c *ModelCache) Set(body []byte) {
	if len(body) == 0 {
		return
	}
	c.mu.Lock()
	changed := !equalBytes(c.body, body)
	c.body = body
	c.age = time.Now()
	c.lastOK = time.Now()
	c.lastStat = 200
	c.lastErr = nil
	c.mu.Unlock()
	if changed {
		c.persist(body)
	}
}

// equalBytes avoids rewriting the file when the probe keeps returning the
// same model list (the probe runs far more often than the list changes).
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
	b := c.body
	c.mu.Unlock()
	if len(b) > 0 {
		return b, nil
	}
	c.mu.Lock()
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
	c.mu.Lock()
	fetch := c.fetch
	c.mu.Unlock()
	if fetch == nil {
		return // no fetcher installed (tests, or proxy-less startup)
	}
	body, status, err := fetch()
	c.mu.Lock()
	c.lastStat = status
	c.lastErr = err
	if err == nil && status == 200 && len(body) > 0 {
		changed := !equalBytes(c.body, body)
		c.body = body
		c.age = time.Now()
		c.lastOK = time.Now()
		c.mu.Unlock()
		if changed {
			c.persist(body)
		}
		return
	}
	c.mu.Unlock()
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
	if interval <= 0 {
		interval = 15 * time.Minute
	}
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
