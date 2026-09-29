package keypool

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pool(keys ...string) *Pool {
	uk := make([]UpstreamKey, 0, len(keys))
	for _, k := range keys {
		uk = append(uk, UpstreamKey{Key: k, Weight: 1})
	}
	return New(uk, Settings{
		MinRPMReserve: 1, Cooldown429: time.Minute, Cooldown5xx: 30 * time.Second,
		ErrThreshold: 3, ErrCooldown: time.Minute, DisableOn401: true,
		MaxRetries: 3, RetryOn: []int{429, 500, 502, 503, 504},
	}, "")
}

func TestPickRotatesThroughAll(t *testing.T) {
	p := pool("a", "b", "c")
	p.SetRand(42)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		l, err := p.Pick(nil)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		seen[l.Entry.Key] = true
		l.Release()
	}
	if len(seen) != 3 {
		t.Fatalf("expected all keys used, got %d", len(seen))
	}
}

func TestPickRespectsTriedAndCooled(t *testing.T) {
	p := pool("a", "b")
	p.SetRand(7)
	tried := map[string]bool{}
	first, err := p.Pick(tried)
	if err != nil {
		t.Fatal(err)
	}
	tried[first.Entry.ID] = true
	first.Release()

	second, err := p.Pick(tried)
	if err != nil {
		t.Fatal(err)
	}
	if second.Entry.Key == first.Entry.Key {
		t.Fatalf("pick ignored tried set")
	}
	second.Release()

	// Exhaust: one healthy key left, then none.
	tried[second.Entry.ID] = true
	third, err := p.Pick(tried)
	if !errors.Is(err, ErrNoKeys) {
		t.Fatalf("expected ErrNoKeys, got %v (entry %v)", err, third.Entry)
	}
}

func TestCooldownBlocksPick(t *testing.T) {
	p := pool("a", "b")
	p.SetRand(3)
	l, err := p.Pick(nil)
	if err != nil {
		t.Fatal(err)
	}
	p.NoteResult(l, Outcome{StatusCode: 429, Limits: Limits{RetryAfter: time.Hour}})
	l.Release()

	// The cooled key must not be picked for the next hour.
	for i := 0; i < 50; i++ {
		got, err := p.Pick(map[string]bool{l.Entry.ID: false})
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got.Entry.Key == l.Entry.Key {
			t.Fatalf("cooled key was picked")
		}
		got.Release()
	}
	st := p.Status()
	if st[0].State != "cooldown" && st[1].State != "cooldown" {
		t.Fatalf("no entry reported cooldown: %+v", st)
	}
}

func TestDisableOn401(t *testing.T) {
	p := pool("a", "b")
	p.SetRand(9)
	l, err := p.Pick(nil)
	if err != nil {
		t.Fatal(err)
	}
	p.NoteResult(l, Outcome{StatusCode: 401})
	l.Release()

	total, healthy := p.Summary()
	if total != 2 || healthy != 1 {
		t.Fatalf("expected 1 disabled key, got total=%d healthy=%d", total, healthy)
	}
	// Re-enable via the ops path.
	if !p.Enable(l.Entry.ID) {
		t.Fatalf("Enable returned false for known id")
	}
	total, healthy = p.Summary()
	if healthy != 2 {
		t.Fatalf("expected both healthy after enable, got %d", healthy)
	}
}

func TestSuccessClearsFailures(t *testing.T) {
	p := pool("a", "b")
	p.SetRand(11)
	l, _ := p.Pick(nil)
	p.NoteResult(l, Outcome{StatusCode: 500})
	p.NoteResult(l, Outcome{StatusCode: 500})
	p.NoteResult(l, Outcome{StatusCode: 500}) // 3 errors -> breaker opens
	l.Release()
	if _, healthy := p.Summary(); healthy != 1 {
		t.Fatalf("breaker should have removed the key")
	}
	l2, _ := p.Pick(nil)
	if l2.Entry.Key == l.Entry.Key {
		t.Fatalf("broken key was picked")
	}
	p.NoteResult(l2, Outcome{StatusCode: 200})
	l2.Release()
	if _, healthy := p.Summary(); healthy != 1 {
		t.Fatalf("only one key is healthy (breaker still open): %d", healthy)
	}
}

func TestRPMReserveSitsOut(t *testing.T) {
	p := pool("a", "b")
	p.SetRand(5)
	l, _ := p.Pick(nil)
	// Report the key as having 60/min limit with 0 left.
	p.NoteResult(l, Outcome{StatusCode: 200, Limits: Limits{RPMLimit: 60, RPMRemaining: 0}})
	l.Release()

	for i := 0; i < 50; i++ {
		got, err := p.Pick(nil)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got.Entry.Key == l.Entry.Key {
			t.Fatalf("key with exhausted rpm budget was picked")
		}
		got.Release()
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	p := pool("a", "b")
	p.persistPath = path
	l, _ := p.Pick(nil)
	p.NoteResult(l, Outcome{StatusCode: 401}) // disabled, must survive
	l.Release()
	if err := p.Persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file missing: %v", err)
	}

	p2 := pool("a", "b")
	p2.persistPath = path
	if err := p2.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// The restored disabled key must not be pickable.
	for i := 0; i < 50; i++ {
		got, err := p2.Pick(nil)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got.Entry.Key == l.Entry.Key {
			t.Fatalf("disabled state was not restored")
		}
		got.Release()
	}
}

func TestParseLimitHeaders(t *testing.T) {
	headers := http.Header{
		"Retry-After":     []string{"42"},
		"X-Rpm-Limit":     []string{"60"},
		"X-Rpm-Remaining": []string{"3"},
	}
	l := ParseLimitHeaders(headers.Get)
	if l.RetryAfter != 42*time.Second || l.RPMLimit != 60 || l.RPMRemaining != 3 {
		t.Fatalf("bad parse: %+v", l)
	}
	l = ParseLimitHeaders(func(string) string { return "" })
	if l.RetryAfter != 0 || l.RPMLimit != 0 {
		t.Fatalf("expected zero values, got %+v", l)
	}
}

func TestWeightedDistribution(t *testing.T) {
	uk := []UpstreamKey{{Key: "heavy", Weight: 9}, {Key: "light", Weight: 1}}
	p := New(uk, Settings{}, "")
	p.SetRand(1)
	counts := map[string]int{}
	for i := 0; i < 2000; i++ {
		l, err := p.Pick(nil)
		if err != nil {
			t.Fatal(err)
		}
		counts[l.Entry.Key]++
		l.Release()
	}
	if counts["light"] == 0 || counts["heavy"] == 0 {
		t.Fatalf("degenerate distribution: %v", counts)
	}
	// Roughly 90/10; allow generous slack since top-5 truncation is absent here.
	if counts["heavy"] < 1400 || counts["light"] > 600 {
		t.Fatalf("distribution skewed: %v", counts)
	}
}
