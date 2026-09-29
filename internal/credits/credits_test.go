package credits

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// nilTransportFactory returns nil (direct connection) for all keys.
func nilTransportFactory() TransportFactory { return nil }

func TestCheckOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := New(srv.URL, 5*time.Second)
	st := p.Check(context.Background(), "k1", "test-key", "", nilTransportFactory())

	if !st.HasCredits {
		t.Errorf("expected HasCredits=true, got false")
	}
	if st.Exhausted {
		t.Errorf("expected Exhausted=false, got true")
	}
	if st.StatusCode != http.StatusOK {
		t.Errorf("expected StatusCode=200, got %d", st.StatusCode)
	}
	if st.LastError != "" {
		t.Errorf("expected no error, got %q", st.LastError)
	}
}

func TestCheckQuotaExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"quota_exceeded","message":"墨点已耗尽"}}`))
	}))
	defer srv.Close()

	p := New(srv.URL, 5*time.Second)
	st := p.Check(context.Background(), "k1", "test-key", "", nilTransportFactory())

	if st.HasCredits {
		t.Errorf("expected HasCredits=false, got true")
	}
	if !st.Exhausted {
		t.Errorf("expected Exhausted=true, got false")
	}
	if st.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected StatusCode=429, got %d", st.StatusCode)
	}
}

func TestCheckRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"请求过于频繁"}}`))
	}))
	defer srv.Close()

	p := New(srv.URL, 5*time.Second)
	st := p.Check(context.Background(), "k1", "test-key", "", nilTransportFactory())

	if !st.HasCredits {
		t.Errorf("expected HasCredits=true (rate limited but credits OK), got false")
	}
	if st.Exhausted {
		t.Errorf("expected Exhausted=false, got true")
	}
	if st.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected StatusCode=429, got %d", st.StatusCode)
	}
}

func TestCheckAll(t *testing.T) {
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"quota_exceeded","message":"exhausted"}}`))
		}
	}))
	defer srv.Close()

	p := New(srv.URL, 5*time.Second)
	keys := []KeyInfo{
		{ID: "k1", Key: "key1"},
		{ID: "k2", Key: "key2"},
		{ID: "k3", Key: "key3"},
		{ID: "k4", Key: "key4"},
		{ID: "k5", Key: "key5"},
	}
	p.CheckAll(context.Background(), keys, nilTransportFactory())

	statuses := p.Status()
	if len(statuses) != 5 {
		t.Errorf("expected 5 statuses, got %d", len(statuses))
	}

	// All keys should have been checked (LastCheck should be non-zero).
	for _, st := range statuses {
		if st.LastCheck.IsZero() {
			t.Errorf("key %s: LastCheck is zero", st.ID)
		}
		if st.StatusCode == 0 {
			t.Errorf("key %s: StatusCode is 0", st.ID)
		}
	}
}

func TestStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := New(srv.URL, 5*time.Second)

	// Before any check, Status() should be empty.
	if statuses := p.Status(); len(statuses) != 0 {
		t.Errorf("expected 0 statuses before any check, got %d", len(statuses))
	}

	// Check one key.
	st := p.Check(context.Background(), "k1", "test-key", "", nilTransportFactory())
	if !st.HasCredits {
		t.Errorf("expected HasCredits=true, got false")
	}

	// StatusByID should return the cached result.
	cached, ok := p.StatusByID("k1")
	if !ok {
		t.Fatalf("expected StatusByID to find k1")
	}
	if !cached.HasCredits {
		t.Errorf("expected cached HasCredits=true, got false")
	}
	if cached.StatusCode != http.StatusOK {
		t.Errorf("expected cached StatusCode=200, got %d", cached.StatusCode)
	}

	// Status() should now have one entry.
	statuses := p.Status()
	if len(statuses) != 1 {
		t.Errorf("expected 1 status, got %d", len(statuses))
	}

	// StatusByID for unknown id should return false.
	if _, ok := p.StatusByID("unknown"); ok {
		t.Errorf("expected StatusByID to return false for unknown id")
	}
}

func TestSummarize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer exhausted-key" {
			w.WriteHeader(http.StatusTooManyRequests)
			body, _ := json.Marshal(map[string]any{
				"error": map[string]any{"code": "quota_exceeded"},
			})
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := New(srv.URL, 5*time.Second)
	keys := []KeyInfo{
		{ID: "k1", Key: "good-key-1"},
		{ID: "k2", Key: "good-key-2"},
		{ID: "k3", Key: "exhausted-key"},
	}
	p.CheckAll(context.Background(), keys, nilTransportFactory())

	// 5 total keys in pool, 3 checked (2 good, 1 exhausted), 2 unchecked.
	sm := p.Summarize(5)
	if sm.Total != 5 {
		t.Errorf("expected Total=5, got %d", sm.Total)
	}
	if sm.WithCredits != 2 {
		t.Errorf("expected WithCredits=2, got %d", sm.WithCredits)
	}
	if sm.Exhausted != 1 {
		t.Errorf("expected Exhausted=1, got %d", sm.Exhausted)
	}
	if sm.Unchecked != 2 {
		t.Errorf("expected Unchecked=2, got %d", sm.Unchecked)
	}
}

func TestCheckUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := New(srv.URL, 5*time.Second)
	st := p.Check(context.Background(), "k1", "bad-key", "", nilTransportFactory())

	if st.HasCredits {
		t.Errorf("expected HasCredits=false for 401, got true")
	}
	if st.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected StatusCode=401, got %d", st.StatusCode)
	}
}

func TestCheckContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	p := New(srv.URL, 5*time.Second)
	st := p.Check(ctx, "k1", "test-key", "", nilTransportFactory())

	if st.HasCredits {
		t.Errorf("expected HasCredits=false for canceled context, got true")
	}
	if st.LastError == "" {
		t.Errorf("expected LastError to be non-empty for canceled context")
	}
}

func TestNewDefaults(t *testing.T) {
	// New with zero timeout should default to 10s.
	p := New("http://example.com", 0)
	if p.timeout != 10*time.Second {
		t.Errorf("expected default timeout 10s, got %v", p.timeout)
	}
	if p.baseURL != "http://example.com" {
		t.Errorf("expected baseURL http://example.com, got %s", p.baseURL)
	}
}

func TestCheckAllEmpty(t *testing.T) {
	p := New("http://example.com", 5*time.Second)
	// Should not panic on empty keys.
	p.CheckAll(context.Background(), nil, nil)
	if statuses := p.Status(); len(statuses) != 0 {
		t.Errorf("expected 0 statuses for empty keys, got %d", len(statuses))
	}
}

func TestCheckAllMaxConcurrent(t *testing.T) {
	// Verify that CheckAll respects the max 5 concurrent limit.
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := concurrent.Add(1)
		for {
			old := maxConcurrent.Load()
			if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		concurrent.Add(-1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := New(srv.URL, 5*time.Second)
	keys := make([]KeyInfo, 10)
	for i := range keys {
		keys[i] = KeyInfo{ID: fmt.Sprintf("k%d", i), Key: fmt.Sprintf("key-%d", i)}
	}
	p.CheckAll(context.Background(), keys, nilTransportFactory())

	if max := maxConcurrent.Load(); max > 5 {
		t.Errorf("expected max concurrent <= 5, got %d", max)
	}
}
