// Package keypool is the upstream Intern Discovery API key pool. It handles multi-key
// rotation (weighted round-robin + top-N weighted random), RPM-aware cooldowns
// driven by the upstream x-rpm-* headers, circuit breaking, degradation, and
// optional state persistence.
//
// Design borrows from autoclaw2api / workbuddy2api: one mutex serializes all
// state transitions; "healthy" is an OR-gate over several expiry timestamps
// (the latest expiry wins, cooldowns never stack); a monotonic sequence number
// drives LRU fallback so wall-clock resolution can't starve entries.
package keypool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CoolKind describes why a key is temporarily out of rotation.
type CoolKind string

const (
	CoolNone   CoolKind = ""
	CoolRate   CoolKind = "rate_limit" // 429, or RPM reserve exhausted
	CoolServer CoolKind = "server"     // 5xx
	CoolError  CoolKind = "errors"     // consecutive errors
	CoolBreak  CoolKind = "breaker"    // circuit breaker
)

// Limits carries the Intern Discovery rate-limit policy parsed from response headers.
type Limits struct {
	RPMLimit     int
	RPMRemaining int
	RetryAfter   time.Duration
	TPMLimit     int // x-tpm-limit: token-per-minute ceiling
	TPMRemaining int // x-tpm-remaining: tokens left this minute
}

// ErrNoKeys means no usable key is available.
var ErrNoKeys = errors.New("no usable upstream key")

// NoKeysError carries the soonest recovery time so the caller can wait instead
// of immediately failing or sending traffic to a cooled-down key.
type NoKeysError struct {
	NextAt time.Time // zero when no key exists at all
}

func (e *NoKeysError) Error() string { return "no usable upstream key" }
func (e *NoKeysError) Unwrap() error { return ErrNoKeys }

// Settings tunes pool behaviour; hot-reloadable.
type Settings struct {
	MinRPMReserve int           `yaml:"min-rpm-reserve" json:"min_rpm_reserve"`
	Cooldown429   time.Duration `yaml:"cooldown-429" json:"cooldown_429"`
	Cooldown5xx   time.Duration `yaml:"cooldown-5xx" json:"cooldown_5xx"`
	ErrThreshold  int           `yaml:"err-threshold" json:"err_threshold"`
	ErrCooldown   time.Duration `yaml:"err-cooldown" json:"err_cooldown"`
	DisableOn401  bool          `yaml:"disable-on-401" json:"disable_on_401"`
	MaxRetries    int           `yaml:"max-retries" json:"max_retries"`
	RetryOn       []int         `yaml:"retry-on" json:"retry_on"`
	Backoff       time.Duration `yaml:"backoff" json:"backoff"`
	// SelectionPolicy controls how Pick chooses among eligible keys.
	// "weighted" (default) does weighted random with LRU tie-break;
	// "round-robin" picks the least-recently-used key deterministically.
	SelectionPolicy string `yaml:"selection-policy" json:"selection_policy"`
}

// UpstreamKey is the configured view of a key.
type UpstreamKey struct {
	Key    string
	Weight int
	Proxy  string
}

// Entry is one upstream key and its live state.
type Entry struct {
	ID     string `json:"id"`
	Key    string `json:"-"`
	Weight int    `json:"weight"`
	Proxy  string `json:"proxy,omitempty"`

	disabled       bool      `json:"-"` // system: key invalid/revoked (401)
	manualDisabled bool      `json:"-"` // ops: disabled via management API
	until          time.Time `json:"-"`
	coolKind       CoolKind  `json:"-"`
	reason         string    `json:"-"`
	failCount      int       `json:"-"` // consecutive upstream failures
	breakerRetry   int       `json:"-"` // breaker half-open cycle
	breakerUntil   time.Time `json:"-"`
	degradeUntil   time.Time `json:"-"`

	// RPM observations from x-rpm-* headers.
	rpmLimit     int       `json:"-"`
	rpmRemaining int       `json:"-"`
	rpmResetAt   time.Time `json:"-"`

	inflight atomic.Int64
	usedSeq  uint64
	lastUsed time.Time

	successCount  int64
	errorCount    int64
	cooldownCount int // 429 escalation counter for cooldown ladder

	// TPM (token-per-minute) observations from x-tpm-* headers.
	tpmLimit     int       `json:"-"`
	tpmRemaining int       `json:"-"`
	tpmResetAt   time.Time `json:"-"`
}

// Pool manages all upstream keys.
type Pool struct {
	mu       sync.Mutex
	byID     map[string]*Entry
	order    []string // stable config order
	seq      uint64   // monotonic LRU counter
	settings atomic.Pointer[Settings]
	rng      *mrand.Rand
	dirty    atomic.Bool

	persistPath string
	stopCh      chan struct{}
}

// New creates a pool from configured keys. Duplicate keys are dropped.
func New(keys []UpstreamKey, s Settings, persistPath string) *Pool {
	p := &Pool{
		byID:        map[string]*Entry{},
		rng:         mrand.New(mrand.NewSource(time.Now().UnixNano())),
		persistPath: persistPath,
		stopCh:      make(chan struct{}),
	}
	p.settings.Store(&s)
	seen := map[string]bool{}
	for _, k := range keys {
		k.Key = strings.TrimSpace(k.Key)
		if k.Key == "" || seen[k.Key] {
			continue
		}
		seen[k.Key] = true
		id := idOf(k.Key)
		w := k.Weight
		if w <= 0 {
			w = 1
		}
		e := &Entry{ID: id, Key: k.Key, Weight: w, Proxy: strings.TrimSpace(k.Proxy)}
		p.byID[id] = e
		p.order = append(p.order, id)
	}
	return p
}

func idOf(key string) string {
	// FNV-1a 64, hex, for a stable log-safe id.
	var h uint64 = 1469598103934665603
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return strconv.FormatUint(h, 16)
}

func (p *Pool) SetSettings(s Settings) { p.settings.Store(&s) }
func (p *Pool) Settings() Settings     { return *p.settings.Load() }

// SetRand replaces the picker's random source with a deterministic one. This
// is a test hook; production pools seed from the clock.
func (p *Pool) SetRand(seed int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rng = mrand.New(mrand.NewSource(seed))
}

// healthy reports whether the entry may serve traffic right now. All expiry
// timestamps are OR-gated: the latest expiry wins and cooldowns never stack.
func (e *Entry) healthy(now time.Time) bool {
	if e.disabled || e.manualDisabled {
		return false
	}
	if !e.until.IsZero() && now.Before(e.until) {
		return false
	}
	if !e.breakerUntil.IsZero() && now.Before(e.breakerUntil) {
		return false
	}
	if !e.degradeUntil.IsZero() && now.Before(e.degradeUntil) {
		return false
	}
	return true
}

// rpmExhausted applies the MinRPMReserve policy: when the upstream reports few
// remaining requests this minute, sit out the rest of the window.
func (e *Entry) rpmExhausted(s Settings, now time.Time) bool {
	if !e.rpmResetAt.After(now) {
		return false
	}
	if e.rpmLimit <= 0 {
		return false
	}
	return e.rpmRemaining < s.MinRPMReserve
}

func (e *Entry) resetAt() time.Time {
	best := e.until
	for _, t := range []time.Time{e.breakerUntil, e.degradeUntil} {
		if t.After(best) {
			best = t
		}
	}
	return best
}

// Lease is a picked key with an in-flight slot that must be released.
type Lease struct {
	Entry *Entry
	// ProxyURL is the entry's per-key proxy override, if any.
	ProxyURL string
}

// Release returns the in-flight slot. Call exactly once.
func (l *Lease) Release() {
	if l != nil && l.Entry != nil {
		l.Entry.inflight.Add(-1)
	}
}

// Pick selects a key for the request, excluding ids in tried and keys that are
// cooled down / out of RPM budget. It returns ErrNoKeys when nothing is usable.
func (p *Pool) Pick(tried map[string]bool) (Lease, error) {
	s := p.Settings()
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()

	eligible := make([]*Entry, 0, len(p.order))
	for _, id := range p.order {
		e := p.byID[id]
		if e == nil || tried[id] {
			continue
		}
		if !e.healthy(now) {
			continue
		}
		if e.rpmExhausted(s, now) {
			continue
		}
		eligible = append(eligible, e)
	}

	if len(eligible) == 0 {
		// Fallback: find the entry that recovers soonest (never disabled ones)
		// and return a NoKeysError with its recovery time so the caller can wait
		// rather than sending traffic to a key that is still cooling down.
		var best *Entry
		for _, id := range p.order {
			e := p.byID[id]
			if e == nil || tried[id] || e.disabled || e.manualDisabled {
				continue
			}
			if best == nil {
				best = e
				continue
			}
			bt, et := best.resetAt(), e.resetAt()
			if et.Before(bt) || (et.IsZero() && !bt.IsZero()) {
				best = e
			}
		}
		if best == nil {
			return Lease{}, &NoKeysError{}
		}
		return Lease{}, &NoKeysError{NextAt: best.resetAt()}
	}

	// Selection policy: "round-robin" picks the least-recently-used eligible
	// key deterministically (pure RR, ignores weight beyond tie-break);
	// anything else does weighted random with LRU tie-break (default).
	if s.SelectionPolicy == "round-robin" {
		var chosen *Entry
		for _, e := range eligible {
			if chosen == nil || e.usedSeq < chosen.usedSeq {
				chosen = e
			}
		}
		return p.lockLease(chosen), nil
	}

	// Weighted top-5 selection, with a Fisher-Yates shuffle first so equal
	// weights are not punished by stable sort order.
	p.rng.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })
	sort.Slice(eligible, func(i, j int) bool {
		wi, wj := eligible[i].effWeight(s, now), eligible[j].effWeight(s, now)
		if wi != wj {
			return wi > wj
		}
		return eligible[i].usedSeq < eligible[j].usedSeq // LRU tie-break
	})
	if len(eligible) > 5 {
		eligible = eligible[:5]
	}

	total := 0
	for _, e := range eligible {
		total += e.effWeight(s, now)
	}
	r := 0
	if total > 0 {
		r = p.rng.Intn(total)
	}
	chosen := eligible[len(eligible)-1]
	for _, e := range eligible {
		r -= e.effWeight(s, now)
		if r < 0 {
			chosen = e
			break
		}
	}
	return p.lockLease(chosen), nil
}

func (p *Pool) lockLease(e *Entry) Lease {
	p.seq++
	e.usedSeq = p.seq
	e.lastUsed = time.Now()
	e.inflight.Add(1)
	return Lease{Entry: e, ProxyURL: e.Proxy}
}

// effWeight is the pick weight: config weight damped when the key is burning
// through its RPM budget, when it has many in-flight requests (least-connections),
// and when its recent success rate is poor — so near-limit / busy / sick keys are
// naturally deprioritized without ever being hard-excluded.
func (e *Entry) effWeight(s Settings, now time.Time) int {
	w := e.Weight
	if e.Weight <= 0 {
		w = 1
	}
	if e.rpmLimit > 0 && e.rpmResetAt.After(now) && e.rpmRemaining < e.rpmLimit {
		// remaining/limit ratio, at least 1 to keep the key pickable.
		ratio := float64(e.rpmRemaining+1) / float64(e.rpmLimit)
		if ratio < 1 {
			if scaled := int(float64(w) * ratio); scaled >= 1 {
				w = scaled
			}
		}
	}
	// least-connections: deprioritize keys with many in-flight requests.
	if n := e.inflight.Load(); n > 0 {
		if scaled := int(float64(w) / float64(1+n)); scaled >= 1 {
			w = scaled
		}
	}
	// success-rate damping: keys with poor recent success rate get deprioritized.
	total := e.successCount + e.errorCount
	if total >= 10 {
		sr := float64(e.successCount) / float64(total)
		if sr < 0.5 {
			if scaled := int(float64(w) * sr * 2); scaled >= 1 {
				w = scaled
			}
		}
	}
	// TPM damping: when the upstream reports a token-per-minute budget and the
	// key is burning through it, deprioritize so the key that still has budget
	// gets picked first.
	if e.tpmLimit > 0 && e.tpmResetAt.After(now) && e.tpmRemaining < e.tpmLimit {
		tpmRatio := float64(e.tpmRemaining+1) / float64(e.tpmLimit)
		if tpmRatio < 1 {
			if scaled := int(float64(w) * tpmRatio); scaled >= 1 {
				w = scaled
			}
		}
	}
	return w
}

// --- result accounting ----------------------------------------------------

// Outcome summarizes an upstream attempt for NoteResult.
type Outcome struct {
	StatusCode int
	Err        error
	// Limits parsed from response headers (zero values = absent).
	Limits Limits
	// Streamed reports that bytes were already written to the client: such
	// failures must not rotate to another key for this request.
	Streamed bool
}

// NoteResult updates pool state after an upstream attempt.
func (p *Pool) NoteResult(l Lease, o Outcome) {
	if l.Entry == nil {
		return
	}
	e := l.Entry
	s := p.Settings()
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirty.Store(true)

	e.applyLimits(o.Limits, now)

	switch {
	case o.Err != nil && o.StatusCode == 0:
		// Transport error (connect/read timeout, proxy failure). Not the
		// key's fault, but consecutive failures degrade it out of rotation.
		e.errorCount++
		e.failCount++
		if e.failCount >= s.ErrThreshold {
			e.degradeUntil = now.Add(s.ErrCooldown)
			e.failCount = 0
			slog.Warn("key degraded after consecutive transport errors",
				"key", e.ID, "until", e.degradeUntil.Format(time.RFC3339))
		}
	case o.Err != nil:
		// A failure after a 2xx status was received — most often a stream that
		// broke mid-way. It must not count as a success, and the existing
		// cooldowns have to survive: clearing them would let a sick key keep
		// serving. Treat it as a soft error without clearing breaker state.
		e.errorCount++
		e.failCount++
		slog.Warn("upstream call failed after 2xx", "key", e.ID, "status", o.StatusCode, "err", o.Err)
	case o.StatusCode == 401:
		e.errorCount++
		if s.DisableOn401 {
			e.disabled = true
			e.reason = "invalid or revoked api key (401)"
			slog.Error("key disabled (401)", "key", e.ID)
		}
	case o.StatusCode == 429:
		e.errorCount++
		d := s.Cooldown429
		if o.Limits.RetryAfter > 0 {
			if o.Limits.RetryAfter > 24*time.Hour {
				o.Limits.RetryAfter = 24 * time.Hour
			}
			d = o.Limits.RetryAfter
		} else {
			// Escalating cooldown ladder: [2m, 10m, 1h, 1d] then capped at 1d.
			ladder := []time.Duration{2 * time.Minute, 10 * time.Minute, time.Hour, 24 * time.Hour}
			idx := e.cooldownCount
			if idx >= len(ladder) {
				idx = len(ladder) - 1
			}
			d = ladder[idx]
			e.cooldownCount++
		}
		e.cool(now, CoolRate, d, "upstream 429 rate limit")
	case o.StatusCode >= 500:
		e.errorCount++
		e.failCount++
		e.cool(now, CoolServer, s.Cooldown5xx, fmt.Sprintf("upstream %d", o.StatusCode))
		if e.failCount >= s.ErrThreshold {
			e.breakerRetry++
			d := s.ErrCooldown * time.Duration(1<<min(e.breakerRetry, 4))
			if d > 6*time.Hour {
				d = 6 * time.Hour
			}
			e.breakerUntil = now.Add(d)
			e.failCount = 0
			slog.Warn("circuit breaker opened", "key", e.ID, "until", e.breakerUntil.Format(time.RFC3339))
		}
	case o.StatusCode >= 400:
		// Client error (bad request body etc.): rotating keys cannot help, so
		// we count it but do not punish the key.
		e.errorCount++
	default:
		e.successCount++
		e.failCount = 0
		e.breakerRetry = 0
		e.breakerUntil = time.Time{}
		e.degradeUntil = time.Time{}
		e.until = time.Time{}
		e.coolKind = CoolNone
		e.reason = ""
		e.cooldownCount = 0
	}
}

// cool applies a cooldown, OR-gated: a new cooldown never shortens an existing
// one, so a retry hitting 429 again cannot stack penalties.
func (e *Entry) cool(now time.Time, kind CoolKind, d time.Duration, reason string) {
	until := now.Add(d)
	if existing := e.resetAt(); existing.After(until) {
		until = existing
	}
	e.until = until
	e.coolKind = kind
	e.reason = reason
	e.failCount = 0
	slog.Info("key cooldown", "key", e.ID, "kind", kind, "until", until.Format(time.RFC3339), "reason", reason)
}

// applyLimits records upstream RPM observations and sits the key out for the
// remainder of the window when the reserve is exhausted. The API docs state
// the limit is a fixed per-minute window, so the reset is now + 1m.
func (e *Entry) applyLimits(l Limits, now time.Time) {
	if l.RPMLimit > 0 {
		e.rpmLimit = l.RPMLimit
	}
	if l.RPMRemaining > 0 || l.RPMLimit > 0 {
		e.rpmRemaining = l.RPMRemaining
		e.rpmResetAt = now.Add(time.Minute)
	}
	if l.TPMLimit > 0 {
		e.tpmLimit = l.TPMLimit
	}
	if l.TPMRemaining > 0 || l.TPMLimit > 0 {
		e.tpmRemaining = l.TPMRemaining
		e.tpmResetAt = now.Add(time.Minute)
	}
}

// ParseLimitHeaders extracts the Intern Discovery rate-limit policy from a response.
func ParseLimitHeaders(get func(string) string) Limits {
	var l Limits
	if v := get("Retry-After"); v != "" {
		if secs, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && secs > 0 {
			l.RetryAfter = time.Duration(secs * float64(time.Second))
		}
	}
	if v := get("X-Rpm-Limit"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			l.RPMLimit = n
		}
	}
	if v := get("X-Rpm-Remaining"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			l.RPMRemaining = n
		}
	}
	if v := get("X-Tpm-Limit"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			l.TPMLimit = n
		}
	}
	if v := get("X-Tpm-Remaining"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			l.TPMRemaining = n
		}
	}
	return l
}

// RetryDelay returns the backoff before the next attempt (exponential with
// ±25% jitter, as in workbuddy2api).
func (p *Pool) RetryDelay(attempt int) time.Duration {
	s := p.Settings()
	base := s.Backoff
	if base <= 0 {
		base = time.Second
	}
	d := base * time.Duration(1<<min(attempt, 6))
	// p.rng is not concurrency safe: it is also used under p.mu in Pick.
	p.mu.Lock()
	jitter := 0.75 + p.rng.Float64()*0.5
	p.mu.Unlock()
	return time.Duration(float64(d) * jitter)
}

// IsRetryable reports whether a status code should trigger a cross-key retry.
func (p *Pool) IsRetryable(status int) bool {
	s := p.Settings()
	for _, c := range s.RetryOn {
		if c == status {
			return true
		}
	}
	return false
}

// --- ops controls --------------------------------------------------------

// Disable/Enable/ManualDisable implement the management API.
func (p *Pool) Disable(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byID[id]
	if !ok {
		return false
	}
	e.manualDisabled = true
	p.dirty.Store(true)
	return true
}

func (p *Pool) Enable(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byID[id]
	if !ok {
		return false
	}
	e.manualDisabled = false
	e.disabled = false
	e.reason = ""
	e.until = time.Time{}
	e.breakerUntil = time.Time{}
	e.degradeUntil = time.Time{}
	e.failCount = 0
	p.dirty.Store(true)
	return true
}

// --- runtime key management ----------------------------------------------

// Has reports whether a raw key value is already in the pool.
func (p *Pool) Has(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.byID[idOf(key)]
	return ok
}

// AddKey registers a key at runtime, or updates its weight/proxy when the id
// already exists. Returns the entry id. Existing cooldowns/counts are kept.
func (p *Pool) AddKey(key string, weight int, proxy string) string {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return ""
	}
	if weight <= 0 {
		weight = 1
	}
	id := idOf(key)
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byID[id]; ok {
		e.Weight = weight
		e.Proxy = strings.TrimSpace(proxy)
		return id
	}
	p.byID[id] = &Entry{ID: id, Key: key, Weight: weight, Proxy: strings.TrimSpace(proxy)}
	p.order = append(p.order, id)
	p.dirty.Store(true)
	return id
}

// RemoveKey drops a key by its (masked) id. In-flight requests holding a lease
// keep working; nothing new is routed to it.
func (p *Pool) RemoveKey(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.byID[id]; !ok {
		return false
	}
	delete(p.byID, id)
	for i, x := range p.order {
		if x == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	p.dirty.Store(true)
	return true
}

// SyncKeys reconciles the pool with a fresh key list (from config reload):
// new keys are added, missing keys removed, weights/proxies updated. State of
// keys that survive is untouched.
func (p *Pool) SyncKeys(keys []UpstreamKey) {
	want := map[string]bool{}
	for _, k := range keys {
		k.Key = strings.TrimSpace(k.Key)
		if k.Key == "" {
			continue
		}
		want[idOf(k.Key)] = true
		p.AddKey(k.Key, k.Weight, k.Proxy)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id := range p.byID {
		if !want[id] {
			delete(p.byID, id)
		}
	}
	// Rebuild order following the caller's config order so the selection order
	// stays stable across reloads instead of shuffling by id hash.
	p.order = p.order[:0]
	for _, k := range keys {
		if id := idOf(strings.TrimSpace(k.Key)); id != "" && p.byID[id] != nil {
			p.order = append(p.order, id)
		}
	}
	// Any pool-only keys not in the config list (should not happen after the
	// delete pass above) keep their existing order rather than being dropped.
	for id := range p.byID {
		if !containsIDs(p.order, id) {
			p.order = append(p.order, id)
		}
	}
	p.dirty.Store(true)
}

func containsIDs(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// --- persistence ---------------------------------------------------------

type snapshotEntry struct {
	ID           string `json:"id"`
	Weight       int    `json:"weight"`
	Proxy        string `json:"proxy,omitempty"`
	Disabled     bool   `json:"disabled"`
	ManualOff    bool   `json:"manual_off"`
	Until        string `json:"until,omitempty"`
	CoolKind     string `json:"cool_kind,omitempty"`
	Reason       string `json:"reason,omitempty"`
	BreakerUntil string `json:"breaker_until,omitempty"`
	DegradeUntil string `json:"degrade_until,omitempty"`
	SuccessCount int64  `json:"success_count"`
	ErrorCount   int64  `json:"error_count"`
}

// Persist writes pool state atomically (tmp + rename, 0600). Only durable
// flags are kept; transient RPM observations are not.
func (p *Pool) Persist() error {
	if p.persistPath == "" {
		return nil
	}
	p.mu.Lock()
	entries := make([]snapshotEntry, 0, len(p.order))
	for _, id := range p.order {
		e := p.byID[id]
		if e == nil {
			continue
		}
		entries = append(entries, snapshotEntry{
			ID: e.ID, Weight: e.Weight, Proxy: e.Proxy,
			Disabled: e.disabled, ManualOff: e.manualDisabled,
			Until: ts(e.until), CoolKind: string(e.coolKind), Reason: e.reason,
			BreakerUntil: ts(e.breakerUntil), DegradeUntil: ts(e.degradeUntil),
			SuccessCount: e.successCount, ErrorCount: e.errorCount,
		})
	}
	p.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(p.persistPath), 0o755); err != nil {
		return err
	}
	tmp := p.persistPath + ".tmp"
	b, err := json.MarshalIndent(map[string]any{
		"saved_at": time.Now().UTC().Format(time.RFC3339),
		"entries":  entries,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.persistPath)
}

// Restore re-applies a previously persisted state onto the configured keys.
// Keys no longer present in config are ignored; new keys start clean.
func (p *Pool) Restore() error {
	if p.persistPath == "" {
		return nil
	}
	b, err := os.ReadFile(p.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc struct {
		SavedAt string          `json:"saved_at"`
		Entries []snapshotEntry `json:"entries"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, se := range doc.Entries {
		e, ok := p.byID[se.ID]
		if !ok {
			continue
		}
		e.disabled = se.Disabled
		e.manualDisabled = se.ManualOff
		e.coolKind = CoolKind(se.CoolKind)
		e.reason = se.Reason
		e.successCount = se.SuccessCount
		e.errorCount = se.ErrorCount
		for _, t := range []struct {
			s string
			d *time.Time
		}{{se.Until, &e.until}, {se.BreakerUntil, &e.breakerUntil}, {se.DegradeUntil, &e.degradeUntil}} {
			if t.s != "" {
				if v, err := time.Parse(time.RFC3339, t.s); err == nil {
					*t.d = v
				}
			}
		}
	}
	return nil
}

// FlushLoop periodically persists dirty state and stops on ctx done.
func (p *Pool) FlushLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = p.Persist()
			return
		case <-t.C:
			if p.dirty.Load() {
				if err := p.Persist(); err != nil {
					slog.Error("persist pool state failed", "err", err)
				} else {
					p.dirty.Store(false)
				}
			}
		}
	}
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// --- status ---------------------------------------------------------------

// EntryStatus is the management-API view of a key (never exposes the key).
type EntryStatus struct {
	ID           string    `json:"id"`
	Weight       int       `json:"weight"`
	Proxy        string    `json:"proxy,omitempty"`
	State        string    `json:"state"`
	CoolKind     string    `json:"cool_kind,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Until        time.Time `json:"until,omitempty"`
	RPMLimit     int       `json:"rpm_limit,omitempty"`
	RPMRemaining int       `json:"rpm_remaining,omitempty"`
	TPMLimit     int       `json:"tpm_limit,omitempty"`
	TPMRemaining int       `json:"tpm_remaining,omitempty"`
	Inflight     int64     `json:"inflight"`
	SuccessCount int64     `json:"success_count"`
	ErrorCount   int64     `json:"error_count"`
}

// Status returns a snapshot of every key.
func (p *Pool) Status() []EntryStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := make([]EntryStatus, 0, len(p.order))
	for _, id := range p.order {
		e := p.byID[id]
		if e == nil {
			continue
		}
		st := "healthy"
		switch {
		case e.disabled:
			st = "disabled"
		case e.manualDisabled:
			st = "manual_off"
		case !e.healthy(now):
			st = "cooldown"
		case e.rpmExhausted(p.Settings(), now):
			st = "rpm_reserve"
		}
		out = append(out, EntryStatus{
			ID: e.ID, Weight: e.Weight, Proxy: e.Proxy,
			State: st, CoolKind: string(e.coolKind), Reason: e.reason,
			Until: e.resetAt(), RPMLimit: e.rpmLimit, RPMRemaining: e.rpmRemaining,
			TPMLimit: e.tpmLimit, TPMRemaining: e.tpmRemaining,
			Inflight: e.inflight.Load(), SuccessCount: e.successCount, ErrorCount: e.errorCount,
		})
	}
	return out
}

// Summary returns light-weight counters for logs and health endpoints.
func (p *Pool) Summary() (total, healthy int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, id := range p.order {
		e := p.byID[id]
		if e == nil {
			continue
		}
		total++
		if e.healthy(now) {
			healthy++
		}
	}
	return total, healthy
}

// ClearCooldowns resets all cooldown/breaker state for non-disabled keys, but
// only when no key is currently healthy — so a probe success after a total
// outage recovers the gateway immediately instead of waiting for each key's
// cooldown to expire. When any key is still serving, individual cooldowns are
// left alone so a key that legitimately hit 429 seconds ago stays cooled.
// Called by the background upstream health goroutine on a successful probe.
func (p *Pool) ClearCooldowns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	anyHealthy := false
	for _, id := range p.order {
		e := p.byID[id]
		if e == nil || e.disabled || e.manualDisabled {
			continue
		}
		if e.healthy(now) && !e.rpmExhausted(p.Settings(), now) {
			anyHealthy = true
			break
		}
	}
	if anyHealthy {
		return 0
	}
	n := 0
	for _, id := range p.order {
		e := p.byID[id]
		if e == nil || e.disabled || e.manualDisabled {
			continue
		}
		e.until = time.Time{}
		e.coolKind = CoolNone
		e.reason = ""
		e.breakerUntil = time.Time{}
		e.breakerRetry = 0
		e.degradeUntil = time.Time{}
		e.failCount = 0
		e.cooldownCount = 0
		n++
	}
	if n > 0 {
		slog.Info("upstream probe succeeded; cleared all cooldowns", "keys", n)
	}
	return n
}

// HealthCheckAll cools every non-disabled key for d when the upstream is
// unreachable, so the pool stops sending traffic until the next probe succeeds.
// Called by the background upstream health goroutine in main.
func (p *Pool) HealthCheckAll(d time.Duration, reason string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	n := 0
	for _, id := range p.order {
		e := p.byID[id]
		if e == nil || e.disabled || e.manualDisabled {
			continue
		}
		e.cool(now, CoolServer, d, reason)
		n++
	}
	if n > 0 {
		slog.Warn("upstream health check failed; cooling all keys",
			"keys", n, "until", now.Add(d).Format(time.RFC3339), "reason", reason)
	}
	return n
}
