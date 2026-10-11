package relay

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizeDeveloperRole(t *testing.T) {
	in := []byte(`{"model":"o4-mini","messages":[{"role":"developer","content":"sys"},{"role":"user","content":"hi"}]}`)
	out, res := normalizeBody(in)
	if !res.Changed {
		t.Fatal("expected changed")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	msgs := m["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("developer not mapped: got %v", first["role"])
	}
	if msgs[1].(map[string]any)["role"] != "user" {
		t.Fatal("user role should be untouched")
	}
	if res.Model != "o4-mini" {
		t.Fatalf("model capture failed: %v", res.Model)
	}
}

func TestNormalizeStripAndMove(t *testing.T) {
	in := []byte(`{"model":"x","reasoning_effort":"high","verbosity":"low","store":true,
"max_completion_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	out, res := normalizeBody(in)
	if !res.Changed {
		t.Fatal("expected changed")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	if _, ok := m["reasoning_effort"]; ok {
		t.Fatal("reasoning_effort not stripped")
	}
	if _, ok := m["verbosity"]; ok {
		t.Fatal("verbosity not stripped")
	}
	if _, ok := m["store"]; ok {
		t.Fatal("store not stripped")
	}
	if v, ok := m["max_tokens"].(float64); !ok || v != 100 {
		t.Fatalf("max_completion_tokens not moved to max_tokens: %v", m["max_tokens"])
	}
	if _, ok := m["max_completion_tokens"]; ok {
		t.Fatal("max_completion_tokens should be deleted after move")
	}
}

func TestNormalizeIdempotentAndPassThrough(t *testing.T) {
	// Standard dialect body: must pass through unchanged (byte-for-byte).
	std := []byte(`{"model":"deepseek-v4","max_tokens":50,"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}],"temperature":0.7}`)
	out, res := normalizeBody(std)
	if res.Changed {
		t.Fatal("standard body must not be rewritten")
	}
	if !reflect.DeepEqual(out, std) {
		t.Fatal("standard body must pass through byte-for-byte")
	}

	// Non-JSON body: returned untouched, no panic.
	bin := []byte{0x00, 0x01, 0xff, 0x7f}
	out2, res2 := normalizeBody(bin)
	if res2.Changed || !reflect.DeepEqual(out2, bin) {
		t.Fatal("non-JSON body must pass through untouched")
	}

	// Normalization is idempotent: applying twice yields the same bytes.
	o1, _ := normalizeBody([]byte(`{"model":"o4","max_completion_tokens":9,"messages":[{"role":"developer","content":"x"}]}`))
	o2, res := normalizeBody(o1)
	if res.Changed {
		t.Fatal("second pass should be a no-op")
	}
	if !reflect.DeepEqual(o1, o2) {
		t.Fatal("idempotency broken")
	}
}

func TestNormalizeMaxTokensAlreadySet(t *testing.T) {
	// When max_tokens is already present it wins; max_completion_tokens is dropped.
	in := []byte(`{"model":"x","max_tokens":10,"max_completion_tokens":99,"messages":[{"role":"user","content":"hi"}]}`)
	out, _ := normalizeBody(in)
	var m map[string]any
	json.Unmarshal(out, &m)
	if v, ok := m["max_tokens"].(float64); !ok || v != 10 {
		t.Fatalf("existing max_tokens must win: %v", m["max_tokens"])
	}
}

func TestNormalizePreservesAllOtherFields(t *testing.T) {
	// Unknown/extra fields the upstream tolerates must survive normalization.
	in := []byte(`{"model":"x","top_p":0.9,"user":"u1","metadata":{"a":1},"messages":[{"role":"user","content":"hi"}]}`)
	out, res := normalizeBody(in)
	if res.Changed {
		t.Fatal("nothing to normalize here")
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["user"] != "u1" || m["metadata"].(map[string]any)["a"] != float64(1) {
		t.Fatal("extra fields lost")
	}
}
