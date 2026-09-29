// Package metrics records per-key usage counters (requests, tokens) with
// optional periodic snapshotting to a JSON file.
package metrics

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Recorder is a concurrency-safe usage counter.
type Recorder struct {
	mu     sync.Mutex
	keys   map[string]*KeyUsage
	models map[string]*ModelUsage
	path   string
	dirty  atomic.Bool
}

// KeyUsage holds counters for one upstream key.
type KeyUsage struct {
	Requests         int64     `json:"requests"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	Errors           int64     `json:"errors"`
	LastUsed         time.Time `json:"last_used"`
}

// ModelUsage holds counters for one model id.
type ModelUsage struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

func New(path string) *Recorder {
	return &Recorder{keys: map[string]*KeyUsage{}, models: map[string]*ModelUsage{}, path: path}
}

func (r *Recorder) getOrCreate(id string) *KeyUsage {
	k, ok := r.keys[id]
	if !ok {
		k = &KeyUsage{}
		r.keys[id] = k
	}
	return k
}

// Record logs one successful request.
func (r *Recorder) Record(keyID, model string, prompt, completion int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := r.getOrCreate(keyID)
	k.Requests++
	k.PromptTokens += int64(prompt)
	k.CompletionTokens += int64(completion)
	k.LastUsed = time.Now().UTC()
	m, ok := r.models[model]
	if !ok {
		m = &ModelUsage{}
		r.models[model] = m
	}
	m.Requests++
	m.PromptTokens += int64(prompt)
	m.CompletionTokens += int64(completion)
	r.dirty.Store(true)
}

// RecordError counts a failed request against a key.
func (r *Recorder) RecordError(keyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getOrCreate(keyID).Errors++
	r.dirty.Store(true)
}

// Snapshot returns a serializable view (keys are masked ids).
func (r *Recorder) Snapshot() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make(map[string]*KeyUsage, len(r.keys))
	for id, k := range r.keys {
		cp := *k
		keys[id] = &cp
	}
	models := make(map[string]*ModelUsage, len(r.models))
	for m, v := range r.models {
		cp := *v
		models[m] = &cp
	}
	return map[string]any{"keys": keys, "models": models}
}

// Persist writes the snapshot atomically.
func (r *Recorder) Persist() error {
	if r.path == "" {
		return nil
	}
	snap := r.Snapshot()
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// Restore loads a previous snapshot.
func (r *Recorder) Restore() error {
	if r.path == "" {
		return nil
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc struct {
		Keys   map[string]*KeyUsage   `json:"keys"`
		Models map[string]*ModelUsage `json:"models"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, k := range doc.Keys {
		r.keys[id] = k
	}
	for m, v := range doc.Models {
		r.models[m] = v
	}
	return nil
}

// FlushLoop persists dirty state on a ticker and on ctx done.
func (r *Recorder) FlushLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 10 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := r.Persist(); err != nil {
				slog.Error("persist usage failed", "err", err)
			}
			return
		case <-t.C:
			if r.dirty.Load() {
				if err := r.Persist(); err != nil {
					slog.Error("persist usage failed", "err", err)
				} else {
					r.dirty.Store(false)
				}
			}
		}
	}
}
