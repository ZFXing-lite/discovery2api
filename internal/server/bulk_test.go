package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ZFXing-lite/discovery2api/internal/config"
	"github.com/ZFXing-lite/discovery2api/internal/keypool"
	"github.com/ZFXing-lite/discovery2api/internal/metrics"
	"github.com/ZFXing-lite/discovery2api/internal/relay"
)

func bulkServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	pool := keypool.New([]keypool.UpstreamKey{{Key: "old"}}, keypool.Settings{}, "")
	r := relay.New(pool, nil, relay.Config{BaseURL: "http://up.invalid",
		DefaultModel: "auto", ForceModel: true}, nil)
	cfg := &config.Config{Host: "127.0.0.1", Port: 8319, APIKeys: []config.APIKeyEntry{{Key: "gw"}}}
	cfg.Upstream.BaseURL = "http://up.invalid"
	cfg.Upstream.DefaultModel = "auto"
	cfg.Upstream.Keys = []config.UpstreamKey{{Key: "old"}} // matches the live pool
	cfg.Management.SecretKey = "mgt"
	cfg.Management.AllowRemote = true
	return New(cfg, r, pool, nil, metrics.New(""), cfgPath), cfgPath
}

func TestBulkImport(t *testing.T) {
	s, cfgPath := bulkServer(t)

	req := httptest.NewRequest("POST", "/v0/management/keys/bulk",
		strings.NewReader(`{"keys":["aaa1","bbb2","","  aaa1  ","# comment","ccc3","not-a-key"],"weight":2}`))
	req.Header.Set("X-Management-Key", "mgt")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Added   int      `json:"added"`
		Updated int      `json:"updated"`
		Skipped int      `json:"skipped"`
		IDs     []string `json:"ids"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	// 4 new keys; ignored: blank, in-batch duplicate, comment.
	// "not-a-key" passes validation (non-empty, no whitespace, length 3-512).
	if res.Added != 4 || res.Updated != 0 || res.Skipped != 3 {
		t.Fatalf("counts wrong: +%d ~%d /%d (want 4/0/3)", res.Added, res.Updated, res.Skipped)
	}
	if len(res.IDs) != 4 {
		t.Fatalf("ids = %v", res.IDs)
	}

	total, healthy := s.pool.Summary()
	if total != 5 || healthy != 5 {
		t.Fatalf("pool should have 5 keys, got %d/%d", total, healthy)
	}
	b, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(b), "aaa1") || !strings.Contains(string(b), "ccc3") {
		t.Fatalf("keys not persisted: %s", b)
	}
}

func TestBulkImportUpdatesExisting(t *testing.T) {
	s, _ := bulkServer(t)

	req := httptest.NewRequest("POST", "/v0/management/keys/bulk",
		strings.NewReader(`{"keys":["old","newkey1"],"weight":5}`))
	req.Header.Set("X-Management-Key", "mgt")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	var res struct {
		Added   int `json:"added"`
		Updated int `json:"updated"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if w.Code != 200 {
		t.Fatalf("bulk update: %d %s", w.Code, w.Body.String())
	}
	if res.Added != 1 || res.Updated != 1 {
		t.Fatalf("want 1 added 1 updated, got +%d ~%d", res.Added, res.Updated)
	}
	total, _ := s.pool.Summary()
	if total != 2 {
		t.Fatalf("pool should have 2 keys, got %d", total)
	}
	// Weight of the existing key must have been refreshed to 5.
	for _, st := range s.pool.Status() {
		if st.Weight != 5 {
			t.Fatalf("weight not applied: %+v", st)
		}
	}
}

func TestBulkImportRejectsEmpty(t *testing.T) {
	s, _ := bulkServer(t)
	req := httptest.NewRequest("POST", "/v0/management/keys/bulk",
		strings.NewReader(`{"keys":["   ","# only a comment"]}`))
	req.Header.Set("X-Management-Key", "mgt")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("bulk: %d", w.Code)
	}
	var res struct {
		Added int `json:"added"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Added != 0 {
		t.Fatalf("expected 0 added, got %d", res.Added)
	}
}

func TestBulkImportNeedsKey(t *testing.T) {
	s, _ := bulkServer(t)
	req := httptest.NewRequest("POST", "/v0/management/keys/bulk",
		strings.NewReader(`{"keys":["aaa1"]}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unauthenticated bulk must be 401, got %d", w.Code)
	}
}
