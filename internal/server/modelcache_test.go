package server

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelCacheServesStaleWhileError(t *testing.T) {
	c := NewModelCache()
	var calls atomic.Int32
	c.SetFetch(func() ([]byte, int, error) {
		n := calls.Add(1)
		if n == 1 {
			return []byte(`{"data":[{"id":"auto"}]}`), 200, nil
		}
		return nil, 500, errors.New("upstream down")
	})
	// First get: cold cache, synchronous fetch succeeds.
	b, err := c.get()
	if err != nil || string(b) == "" {
		t.Fatalf("cold get: err=%v body=%s", err, b)
	}
	// A failing refresh must NOT drop the last-good body (stale-while-error).
	c.doRefresh() // calls fetch (fails: n==2 → 500), keeps last-good body
	b, err = c.get()
	if err != nil || string(b) == "" {
		t.Fatalf("stale get: err=%v body=%s", err, b)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 fetch calls, got %d", calls.Load())
	}
}

func TestModelCacheSetFromProbe(t *testing.T) {
	c := NewModelCache()
	c.SetFetch(func() ([]byte, int, error) { return nil, 0, errors.New("never") })
	c.Set([]byte(`{"object":"list"}`))
	b, err := c.get()
	if err != nil || string(b) != `{"object":"list"}` {
		t.Fatalf("get after Set: err=%v body=%s", err, b)
	}
	if _, lastOK, lastErr, stat := c.State(); stat != 200 || !lastOK.IsZero() == false || lastErr != nil {
		t.Fatalf("state: stat=%d lastOK=%v lastErr=%v", stat, lastOK, lastErr)
	}
}

func TestModelCacheSingleflightCold(t *testing.T) {
	c := NewModelCache()
	var inflight, maxInflight atomic.Int32
	c.SetFetch(func() ([]byte, int, error) {
		cur := inflight.Add(1)
		for {
			old := maxInflight.Load()
			if cur <= old || maxInflight.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		inflight.Add(-1)
		return []byte(`{"ok":true}`), 200, nil
	})
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Concurrent cold gets: one triggers the fetch, others wait.
			_, errs[i] = c.get()
		}(i)
	}
	wg.Wait()
	// At most one fetch may be in flight at a time… but our get() callers
	// that find sfdone != nil wait and then read the body, so all callers
	// that waited should see the body. The fetch launcher returns
	// "cache cold: fetch started" — that is acceptable: it means the body
	// is on its way and the next get() will serve it.
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if maxInflight.Load() > 1 {
		t.Fatalf("singleflight violated: %d concurrent fetches", maxInflight.Load())
	}
	c.mu.Lock()
	if len(c.body) == 0 {
		t.Fatal("fetch result missing")
	}
	c.mu.Unlock()
}
