package server

import (
	"sync"
	"time"
)

// epCount is the per-endpoint call bookkeeping used by the panel.
type epCount struct {
	Requests   int64     `json:"requests"`
	Errors     int64     `json:"errors"`
	Inflight   int64     `json:"inflight"`
	LastStatus int       `json:"last_status"`
	LastCall   time.Time `json:"last_call"`
	LastError  string    `json:"last_error,omitempty"`
	LastTook   string    `json:"last_took,omitempty"`
}

// Stats records per-endpoint call counts for the management panel. It is safe
// for concurrent use.
type Stats struct {
	mu sync.Mutex
	m  map[string]*epCount
}

func NewStats() *Stats { return &Stats{m: map[string]*epCount{}} }

func (s *Stats) get(path string) *epCount {
	c, ok := s.m[path]
	if !ok {
		c = &epCount{}
		s.m[path] = c
	}
	return c
}

// Begin marks a request as in-flight.
func (s *Stats) Begin(path string) {
	s.mu.Lock()
	s.get(path).Inflight++
	s.mu.Unlock()
}

// End records the outcome.
func (s *Stats) End(path string, status int, took time.Duration, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.get(path)
	c.Requests++
	c.Inflight--
	if c.Inflight < 0 {
		c.Inflight = 0
	}
	c.LastStatus = status
	c.LastCall = time.Now()
	c.LastTook = took.Round(time.Millisecond).String()
	if status >= 400 || errMsg != "" {
		c.Errors++
		if errMsg != "" {
			c.LastError = truncateMsg(errMsg)
		} else {
			c.LastError = ""
		}
	} else {
		c.LastError = ""
	}
}

// Snapshot returns a copy of all endpoint counters.
func (s *Stats) Snapshot() map[string]*epCount {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*epCount, len(s.m))
	for k, v := range s.m {
		cp := *v
		out[k] = &cp
	}
	return out
}

func truncateMsg(s string) string {
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}
