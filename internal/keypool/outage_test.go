package keypool

import (
	"testing"
	"time"
)

// HealthCheckAll must never extend an existing cooldown: re-cooling a key
// every probe round would reset its expiry and turn a 30s blip into an
// endless outage (the failure-amplification bug found in production logs).
func TestHealthCheckAllDoesNotExtendCooldown(t *testing.T) {
	p := pool("a", "b")
	// First round: both keys cool for a short window.
	n := p.HealthCheckAll(20*time.Second, "probe round 1")
	if n != 2 {
		t.Fatalf("round 1: expected 2 keys cooled, got %d", n)
	}
	// Second round while still cooling: must be a no-op (n==0), and the
	// original expiry must stand.
	n = p.HealthCheckAll(time.Hour, "probe round 2")
	if n != 0 {
		t.Fatalf("round 2: expected 0 keys re-cooled, got %d", n)
	}
	total, healthy := p.Summary()
	if total != 2 || healthy != 0 {
		t.Fatalf("summary: total=%d healthy=%d", total, healthy)
	}
}

// SampleKeys returns distinct healthy keys and skips cooling ones.
func TestSampleKeys(t *testing.T) {
	p := pool("a", "b", "c", "d")
	p.SetRand(1)
	ks := p.SampleKeys(3)
	if len(ks) != 3 {
		t.Fatalf("expected 3 keys, got %d: %v", len(ks), ks)
	}
	seen := map[string]bool{}
	for _, k := range ks {
		if seen[k] {
			t.Fatalf("duplicate key %q in %v", k, ks)
		}
		seen[k] = true
	}
	// Cool everything: sample must be empty.
	p.HealthCheckAll(time.Minute, "outage")
	if got := p.SampleKeys(3); len(got) != 0 {
		t.Fatalf("expected 0 eligible keys during outage, got %v", got)
	}
}
