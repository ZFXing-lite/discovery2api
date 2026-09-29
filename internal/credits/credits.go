// Package credits probes upstream Intern Discovery API keys for credit (墨点)
// status. The upstream has no dedicated balance endpoint and no x-credit
// headers; the only signal is the response to a lightweight GET /v1/models:
//
//   - 200 → key works, account has credits
//   - 429 with error.code=quota_exceeded → credits exhausted
//   - 429 with error.code=rate_limit_exceeded → rate limited but credits OK
//
// 1墨点 ≈ 20,000,000 tokens. All keys under one account share the same balance.
package credits

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CreditStatus is the result of probing one key's credit state.
type CreditStatus struct {
	ID         string    `json:"id"`
	HasCredits bool      `json:"has_credits"`
	Exhausted  bool      `json:"exhausted"`
	LastCheck  time.Time `json:"last_check"`
	LastError  string    `json:"last_error,omitempty"`
	StatusCode int       `json:"status_code"`
}

// Probe checks upstream keys for credit (墨点) status by sending lightweight
// GET /v1/models requests. Results are cached and safe for concurrent use.
type Probe struct {
	mu       sync.RWMutex
	statuses map[string]*CreditStatus
	baseURL  string
	timeout  time.Duration
}

// New creates a Probe that checks keys against baseURL with the given timeout.
func New(baseURL string, timeout time.Duration) *Probe {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Probe{
		statuses: make(map[string]*CreditStatus),
		baseURL:  strings.TrimRight(baseURL, "/"),
		timeout:  timeout,
	}
}

// TransportFactory returns an *http.Transport for a given key id and proxy URL.
// This lets the caller wire in the same SOCKS5 proxy / per-key override logic
// the relay uses, without the credits package depending on relay or proxypool.
type TransportFactory func(id, proxyURL string) *http.Transport

// Check probes a single key and caches the result. The transport factory
// provides the HTTP transport (with optional proxy) for the request.
func (p *Probe) Check(ctx context.Context, id, key, proxyURL string, tf TransportFactory) CreditStatus {
	url := p.baseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		st := CreditStatus{ID: id, LastCheck: time.Now(), LastError: err.Error()}
		p.store(st)
		return st
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")

	var transport *http.Transport
	if tf != nil {
		transport = tf(id, proxyURL)
	}
	client := &http.Client{Timeout: p.timeout}
	if transport != nil {
		client.Transport = transport
	}

	resp, err := client.Do(req)
	if err != nil {
		st := CreditStatus{ID: id, LastCheck: time.Now(), LastError: err.Error()}
		p.store(st)
		return st
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	st := CreditStatus{ID: id, StatusCode: resp.StatusCode, LastCheck: time.Now()}

	switch resp.StatusCode {
	case http.StatusOK:
		st.HasCredits = true
	case http.StatusTooManyRequests:
		// Parse the error body to distinguish quota_exceeded from rate_limit_exceeded.
		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if jsonErr := json.Unmarshal(body, &errResp); jsonErr == nil {
			code := strings.ToLower(errResp.Error.Code)
			switch {
			case strings.Contains(code, "quota_exceeded"):
				st.Exhausted = true
				st.HasCredits = false
			case strings.Contains(code, "rate_limit_exceeded"):
				st.HasCredits = true
				st.Exhausted = false
			default:
				// Unknown 429 code: assume credits OK (rate limited).
				st.HasCredits = true
			}
		} else {
			// Could not parse body: assume rate limited, credits OK.
			st.HasCredits = true
		}
	case http.StatusUnauthorized:
		st.HasCredits = false
		st.LastError = "invalid or revoked key (401)"
	default:
		// Other status codes: assume credits OK (the key authenticated).
		st.HasCredits = resp.StatusCode < 400
		if resp.StatusCode >= 400 {
			st.LastError = "upstream returned HTTP " + http.StatusText(resp.StatusCode)
		}
	}

	p.store(st)
	return st
}

func (p *Probe) store(st CreditStatus) {
	p.mu.Lock()
	cs := p.statuses[st.ID]
	if cs == nil {
		cs = &CreditStatus{}
		p.statuses[st.ID] = cs
	}
	*cs = st
	p.mu.Unlock()
}

// Status returns a snapshot of all cached credit statuses.
func (p *Probe) Status() []CreditStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]CreditStatus, 0, len(p.statuses))
	for _, cs := range p.statuses {
		out = append(out, *cs)
	}
	return out
}

// StatusByID returns the cached status for one key.
func (p *Probe) StatusByID(id string) (CreditStatus, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cs, ok := p.statuses[id]
	if !ok {
		return CreditStatus{}, false
	}
	return *cs, true
}

// KeyInfo carries the id, raw key value and proxy URL for one upstream key.
type KeyInfo struct {
	ID       string
	Key      string
	ProxyURL string
}

// CheckAll probes all keys in parallel (max 5 concurrent). Each key is probed
// with the transport factory; a nil factory means direct connections.
func (p *Probe) CheckAll(ctx context.Context, keys []KeyInfo, tf TransportFactory) {
	if len(keys) == 0 {
		return
	}
	maxConcurrent := 5
	if len(keys) < maxConcurrent {
		maxConcurrent = len(keys)
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, k := range keys {
		if k.Key == "" {
			continue
		}
		wg.Add(1)
		go func(k KeyInfo) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			p.Check(ctx, k.ID, k.Key, k.ProxyURL, tf)
		}(k)
	}
	wg.Wait()
}

// Summary holds aggregate credit status counts.
type Summary struct {
	Total       int `json:"total"`
	WithCredits int `json:"with_credits"`
	Exhausted   int `json:"exhausted"`
	Unchecked   int `json:"unchecked"`
}

// Summarize returns aggregate counts over the cached statuses, given the total
// number of keys in the pool.
func (p *Probe) Summarize(totalKeys int) Summary {
	statuses := p.Status()
	s := Summary{Total: totalKeys}
	checked := len(statuses)
	for _, cs := range statuses {
		if cs.Exhausted {
			s.Exhausted++
		} else if cs.HasCredits {
			s.WithCredits++
		}
	}
	s.Unchecked = totalKeys - checked
	if s.Unchecked < 0 {
		s.Unchecked = 0
	}
	return s
}
