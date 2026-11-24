package server

import (
	"sync"
	"time"
)

// ReqLogEntry is one recorded API request, shown on the panel's request-log
// page. All fields carry JSON tags so the management API can serialize the
// slice directly.
type ReqLogEntry struct {
	Time           time.Time `json:"time"`
	Method         string    `json:"method"`
	Path           string    `json:"path"`
	ClientIP       string    `json:"client_ip"`
	Status         int       `json:"status"`
	Duration       string    `json:"duration"`
	ReqBytes       int       `json:"req_bytes"`
	RespBytes      int       `json:"resp_bytes"`
	Model          string    `json:"model"`
	KeyID          string    `json:"key_id"`
	DownstreamKey  string    `json:"downstream_key,omitempty"`
	UpstreamStatus int       `json:"upstream_status"`
	Error          string    `json:"error,omitempty"`
	Streaming      bool      `json:"streaming"`
	Retries        int       `json:"retries"`
}

// ReqLogger is a fixed-capacity ring buffer of request-log entries. It is safe
// for concurrent use. The buffer stores the most recent entries; older ones are
// evicted as new requests arrive.
type ReqLogger struct {
	mu    sync.Mutex
	buf   []ReqLogEntry
	head  int // index of the next write position
	count int // number of valid entries (capped at len(buf))
	cap   int
}

// NewReqLogger creates a ReqLogger that retains the last cap entries.
func NewReqLogger(cap int) *ReqLogger {
	if cap <= 0 {
		cap = 1000
	}
	return &ReqLogger{buf: make([]ReqLogEntry, cap), cap: cap}
}

// Add appends a new entry to the ring buffer. When the buffer is full the
// oldest entry is overwritten.
func (l *ReqLogger) Add(e ReqLogEntry) {
	l.mu.Lock()
	l.buf[l.head] = e
	l.head = (l.head + 1) % l.cap
	if l.count < l.cap {
		l.count++
	}
	l.mu.Unlock()
}

// Recent returns up to n entries in reverse chronological order (newest first).
// If n exceeds the number of stored entries, all stored entries are returned.
func (l *ReqLogger) Recent(n int) []ReqLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > l.count {
		n = l.count
	}
	if n <= 0 {
		return []ReqLogEntry{}
	}
	out := make([]ReqLogEntry, 0, n)
	// head points to the next write slot, so the most recently written entry
	// is at (head-1) mod cap, the previous one at (head-2) mod cap, etc.
	for i := 0; i < n; i++ {
		idx := (l.head - 1 - i + l.cap) % l.cap
		out = append(out, l.buf[idx])
	}
	return out
}
