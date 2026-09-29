// Package proxypool maintains a pool of SOCKS5 endpoints and produces
// http.Transports whose DialContext picks a healthy proxy per connection.
package proxypool

import (
	"context"
	"fmt"
	"log/slog"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Policy controls how a proxy is chosen for a connection.
type Policy string

const (
	PolicyRoundRobin Policy = "round-robin"
	PolicyRandom     Policy = "random"
	PolicyStickyKey  Policy = "sticky-key"
)

// Pool is a concurrency-safe SOCKS5 proxy pool. A zero pool (Empty()==true)
// yields direct (no-proxy) transports.
type Pool struct {
	mu             sync.RWMutex
	entries        []*entry
	policy         Policy
	failCooldown   time.Duration
	rr             uint64 // round-robin counter
	rng            *mrand.Rand
	transportCache sync.Map // key -> *http.Transport (keyed for reuse)
}

type entry struct {
	url        string
	weight     int
	healthy    bool
	failUntil  time.Time
	failCount  int
	successCnt int64
	lastUsed   time.Time
}

// available reports whether the entry may be picked. A proxy is available when
// its fail-cooldown has expired; the healthy flag is NOT a gate — a single
// failure must not permanently kill a proxy that could recover on the next dial.
func (e *entry) available(now time.Time) bool {
	return e.failUntil.IsZero() || !now.Before(e.failUntil)
}

// New builds a pool from configured entries. Bad URLs are logged and skipped
// rather than fatal.
func New(urls []struct {
	URL    string
	Weight int
}, policy string, failCooldown time.Duration) *Pool {
	p := &Pool{
		policy:       Policy(strings.ToLower(strings.TrimSpace(policy))),
		failCooldown: failCooldown,
		rng:          mrand.New(mrand.NewSource(time.Now().UnixNano())),
	}
	if p.policy == "" {
		p.policy = PolicyRoundRobin
	}
	seen := map[string]bool{}
	for _, u := range urls {
		raw := strings.TrimSpace(u.URL)
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		if _, _, _, err := parseProxyURL(raw); err != nil {
			slog.Warn("skipping invalid proxy", "url", maskProxy(raw), "err", err)
			continue
		}
		w := u.Weight
		if w <= 0 {
			w = 1
		}
		p.entries = append(p.entries, &entry{url: raw, weight: w, healthy: true})
	}
	return p
}

// Replace swaps the live proxy list. In-flight dials keep the entry they
// already picked; the next dial sees the new list. A nil next clears the pool.
func (p *Pool) Replace(next *Pool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if next == nil {
		p.entries = nil
		return
	}
	next.mu.RLock()
	defer next.mu.RUnlock()
	p.entries = next.entries
	p.policy = next.policy
	p.failCooldown = next.failCooldown
	p.rr = 0
}

// Empty reports whether the pool has no usable proxy (direct connection).
func (p *Pool) Empty() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.entries) == 0
}

// ParseURL validates a socks5 URL. The panel uses it before persisting a
// proxy list so a bad entry never reaches the live pool.
func ParseURL(raw string) error {
	_, _, _, err := parseProxyURL(raw)
	return err
}

func parseProxyURL(raw string) (addr, user, pass string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("parse: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "socks5" && scheme != "socks5h" && scheme != "socks" {
		return "", "", "", fmt.Errorf("unsupported scheme %q (want socks5/socks5h)", u.Scheme)
	}
	if u.Host == "" {
		return "", "", "", fmt.Errorf("missing host:port")
	}
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	return u.Host, user, pass, nil
}

// Pick selects a proxy URL for the given key hint (used by sticky-key).
// Returns "" when the pool is empty (direct).
func (p *Pool) Pick(keyHint string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.entries) == 0 {
		return ""
	}
	now := time.Now()
	available := make([]*entry, 0, len(p.entries))
	for _, e := range p.entries {
		if e.available(now) {
			available = append(available, e)
		}
	}
	// Everything cooled down: fall back to the soonest-recovering entry.
	if len(available) == 0 {
		slog.Warn("all proxies in cooldown, using least-failed", "count", len(p.entries))
		best := p.entries[0]
		for _, e := range p.entries[1:] {
			if e.failUntil.Before(best.failUntil) || best.failUntil.IsZero() {
				best = e
			}
		}
		best.lastUsed = now
		return best.url
	}

	var chosen *entry
	switch p.policy {
	case PolicyStickyKey:
		idx := stableIndex(keyHint, len(available))
		chosen = available[idx]
	case PolicyRandom:
		chosen = available[p.weightedRand(available)]
	default: // round-robin
		idx := p.rr % uint64(len(available))
		p.rr++
		chosen = available[idx]
	}
	chosen.lastUsed = now
	return chosen.url
}

// weightedRand returns an index into entries chosen with probability
// proportional to weight.
func (p *Pool) weightedRand(entries []*entry) int {
	total := 0
	for _, e := range entries {
		total += e.weight
	}
	if total <= 0 {
		return p.rng.Intn(len(entries))
	}
	r := p.rng.Intn(total)
	for i, e := range entries {
		r -= e.weight
		if r < 0 {
			return i
		}
	}
	return len(entries) - 1
}

// stableIndex maps keyHint deterministically into [0, n) with FNV-1a 64. The
// same key always lands on the same proxy while the entry list is unchanged —
// that is the point of the sticky-key policy.
func stableIndex(keyHint string, n int) int {
	if n <= 1 {
		return 0
	}
	var hash uint64 = 1469598103934665603
	for i := 0; i < len(keyHint); i++ {
		hash ^= uint64(keyHint[i])
		hash *= 1099511628211
	}
	return int(hash % uint64(n))
}

// NoteFailure marks a proxy URL as failed and cools it down for failCooldown.
// Unknown URLs are ignored.
func (p *Pool) NoteFailure(proxyURL string) {
	if proxyURL == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.url == proxyURL {
			e.failCount++
			e.healthy = false
			e.failUntil = time.Now().Add(p.failCooldown)
			slog.Warn("proxy failure recorded", "url", maskProxy(e.url),
				"fails", e.failCount, "cooldown", p.failCooldown)
			return
		}
	}
}

// NoteSuccess clears the failure state of a proxy URL and marks it healthy.
func (p *Pool) NoteSuccess(proxyURL string) {
	if proxyURL == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.url == proxyURL {
			e.failCount = 0
			e.healthy = true
			e.failUntil = time.Time{}
			e.successCnt++
			return
		}
	}
}

// dialerFor builds a proxy.Dialer for the given URL.
func dialerFor(rawURL string) (proxy.Dialer, error) {
	addr, user, pass, err := parseProxyURL(rawURL)
	if err != nil {
		return nil, err
	}
	var auth *proxy.Auth
	if user != "" {
		auth = &proxy.Auth{User: user, Password: pass}
	}
	return proxy.SOCKS5("tcp", addr, auth, proxy.Direct)
}

// Transport builds an *http.Transport that dials through a pool proxy chosen
// at dial time. When the pool is empty it returns a direct transport. The
// transport is cached per keyHint so TCP+TLS connections are reused across
// requests instead of being re-established on every call.
//
// The cached transport's DialContext is set once and never mutated; relay wraps
// the result with withConnectTimeout, which Clones the transport (so the
// cached original is untouched) and caches that clone in its own rtCache.
func (p *Pool) Transport(keyHint string, onProxy func(string)) *http.Transport {
	if p == nil || p.Empty() {
		return baseTransport()
	}
	if v, ok := p.transportCache.Load(keyHint); ok {
		return v.(*http.Transport)
	}
	t := baseTransport()
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		proxyURL := p.Pick(keyHint)
		if proxyURL == "" {
			if onProxy != nil {
				onProxy("")
			}
			d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
			return d.DialContext(ctx, network, addr)
		}
		d, err := dialerFor(proxyURL)
		if err != nil {
			p.NoteFailure(proxyURL)
			if onProxy != nil {
				onProxy(proxyURL)
			}
			return nil, fmt.Errorf("socks5 dialer for %s: %w", maskProxy(proxyURL), err)
		}
		cd, ok := d.(proxy.ContextDialer)
		var c net.Conn
		if ok {
			c, err = cd.DialContext(ctx, network, addr)
		} else {
			c, err = d.Dial(network, addr)
		}
		if err != nil {
			p.NoteFailure(proxyURL)
		} else {
			p.NoteSuccess(proxyURL)
		}
		if onProxy != nil {
			onProxy(proxyURL)
		}
		return c, err
	}
	actual, _ := p.transportCache.LoadOrStore(keyHint, t)
	return actual.(*http.Transport)
}

func baseTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 10 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

// transportCache memoizes one transport per explicit proxy URL ("none" = direct).
var transportCache sync.Map

// TransportForURL returns a shared transport pinned to a single proxy URL,
// or a direct transport when url is "" / "none". Invalid URLs fall back to
// direct with a warning so one bad config entry can't break the gateway.
func TransportForURL(rawURL string) *http.Transport {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || strings.EqualFold(rawURL, "none") {
		return baseTransport()
	}
	if v, ok := transportCache.Load(rawURL); ok {
		return v.(*http.Transport)
	}
	t := baseTransport()
	d, err := dialerFor(rawURL)
	if err != nil {
		slog.Warn("per-key proxy invalid, using direct", "url", maskProxy(rawURL), "err", err)
		return baseTransport()
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if cd, ok := d.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, network, addr)
		}
		return d.Dial(network, addr)
	}
	actual, _ := transportCache.LoadOrStore(rawURL, t)
	return actual.(*http.Transport)
}

// HealthCheck pings every proxy through a lightweight TCP connect (via the
// socks5 dialer to the upstream host). A successful dial restores the entry;
// a failed dial cools it down so the picker stops using a dead node. It is
// meant to be run on a ticker.
func (p *Pool) HealthCheck(ctx context.Context, testAddr string) {
	if p == nil || p.Empty() || testAddr == "" {
		return
	}
	p.mu.RLock()
	urls := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		urls = append(urls, e.url)
	}
	p.mu.RUnlock()

	for _, u := range urls {
		select {
		case <-ctx.Done():
			return
		default:
		}
		func() {
			ctxT, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			d, err := dialerFor(u)
			if err != nil {
				return
			}
			cd, ok := d.(proxy.ContextDialer)
			var c net.Conn
			if ok {
				c, err = cd.DialContext(ctxT, "tcp", testAddr)
			} else {
				c, err = d.Dial("tcp", testAddr)
			}
			if err != nil {
				p.NoteFailure(u)
				return
			}
			_ = c.Close()
			p.NoteSuccess(u)
		}()
	}
}

// Status returns a serializable snapshot.
type Status struct {
	URL        string    `json:"url"`
	Weight     int       `json:"weight"`
	Healthy    bool      `json:"healthy"`
	FailCount  int       `json:"fail_count"`
	FailUntil  time.Time `json:"fail_until,omitempty"`
	SuccessCnt int64     `json:"success_count"`
}

func (p *Pool) Status() []Status {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Status, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, Status{
			URL: maskProxy(e.url), Weight: e.weight, Healthy: e.healthy,
			FailCount: e.failCount, FailUntil: e.failUntil, SuccessCnt: e.successCnt,
		})
	}
	return out
}

func maskProxy(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return "***"
	}
	if pu.User != nil {
		pu.User = url.UserPassword("***", "***")
	}
	return pu.String()
}
