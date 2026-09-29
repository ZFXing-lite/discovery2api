package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZFXing-lite/discovery2api/internal/config"
	"github.com/ZFXing-lite/discovery2api/internal/keypool"
	"github.com/ZFXing-lite/discovery2api/internal/metrics"
	"github.com/ZFXing-lite/discovery2api/internal/relay"
)

// mgmtServer builds a server whose management API is open (test origin is not
// loopback) and whose config persists to a temp file.
func mgmtServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"model":"auto","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(up.Close)

	pool := keypool.New([]keypool.UpstreamKey{{Key: "orig"}}, keypool.Settings{MaxRetries: 2}, "")
	r := relay.New(pool, nil, relay.Config{
		BaseURL: up.URL, DefaultModel: "auto", ForceModel: true, Timeout: 10_000_000_000,
	}, nil)

	cfg := &config.Config{Host: "127.0.0.1", Port: 8319, APIKeys: []config.APIKeyEntry{{Key: "gw-orig"}, {Key: "gw-other"}}}
	cfg.Upstream.BaseURL = up.URL
	cfg.Upstream.DefaultModel = "auto"
	cfg.Upstream.Keys = []config.UpstreamKey{{Key: "orig"}} // matches the live pool
	cfg.Management.SecretKey = "mgt"
	cfg.Management.AllowRemote = true
	s := New(cfg, r, pool, nil, metrics.New(""), cfgPath)
	return s, cfgPath
}

func mgmtDo(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("X-Management-Key", "mgt")
	req.RemoteAddr = "127.0.0.1:12345" // loopback so mgmtDo works
	loginReset("127.0.0.1")            // keep the login guard from blocking other tests
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	loginReset("127.0.0.1")
	return w
}

func TestPanelServed(t *testing.T) {
	s, _ := mgmtServer(t)
	w := mgmtDo(t, s, "GET", "/v0/management/panel", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "discovery2api") {
		t.Fatalf("panel not served: %d %s", w.Code, firstChars(body))
	}
}

func TestUpstreamKeyAddRemove(t *testing.T) {
	s, cfgPath := mgmtServer(t)

	w := mgmtDo(t, s, "POST", "/v0/management/keys", map[string]any{"key": "new", "weight": 3})
	if w.Code != 200 {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ID == "" {
		t.Fatal("no id returned")
	}

	// The pool must serve with the new key immediately.
	total, healthy := s.pool.Summary()
	if total != 2 || healthy != 2 {
		t.Fatalf("pool not updated: total=%d healthy=%d", total, healthy)
	}
	// And the config file must now contain it (survives restart).
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "new") {
		t.Fatalf("key not persisted: %s", b)
	}
	// Rewritten config must still parse cleanly.
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("persisted config is invalid: %v", err)
	}

	// Remove it.
	w = mgmtDo(t, s, "DELETE", "/v0/management/keys/"+resp.ID, nil)
	if w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	total, _ = s.pool.Summary()
	if total != 1 {
		t.Fatalf("key not removed from pool: %d", total)
	}
}

func TestUnwritableConfigRollsBackKeys(t *testing.T) {
	s, _ := mgmtServer(t)
	// A directory is not a file the server can replace, so the save fails for
	// root too. This is the same outcome as a Docker :ro mount.
	blocked := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	s.configPath = blocked

	up := mgmtDo(t, s, "POST", "/v0/management/keys", map[string]any{"key": "shouldno"})
	if up.Code != 500 || !strings.Contains(up.Body.String(), "persist_failed") {
		t.Fatalf("upstream add must report the save failure: %d %s", up.Code, up.Body.String())
	}
	if total, _ := s.pool.Summary(); total != 1 {
		t.Fatalf("failed upstream add stayed in the pool: %d", total)
	}
	down := mgmtDo(t, s, "POST", "/v0/management/api-keys", map[string]any{"key": "client-nope"})
	if down.Code != 500 || !strings.Contains(down.Body.String(), "persist_failed") {
		t.Fatalf("downstream add must report the save failure: %d %s", down.Code, down.Body.String())
	}
	if n := len(s.cfg.Load().APIKeys); n != 2 {
		t.Fatalf("failed downstream add changed live keys: %d", n)
	}
}

func TestAPIKeyAddTakesEffectImmediately(t *testing.T) {
	s, _ := mgmtServer(t)

	// Call the gateway with a key that does not exist yet.
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[]}`))
	req.Header.Set("Authorization", "Bearer gw-new")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unknown key must be rejected, got %d", w.Code)
	}

	// Add it through the management API.
	if w := mgmtDo(t, s, "POST", "/v0/management/api-keys", map[string]any{"key": "gw-new"}); w.Code != 200 {
		t.Fatalf("add api key: %d %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[]}`))
	req.Header.Set("Authorization", "Bearer gw-new")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("new api key must work immediately, got %d %s", w.Code, w.Body.String())
	}
}

func TestAPIKeyRemoveBlocksImmediately(t *testing.T) {
	s, _ := mgmtServer(t)
	call := func(key string) int {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if call("gw-orig") != 200 {
		t.Fatal("baseline call failed")
	}
	id := config.UpstreamKeyID("gw-orig")
	if w := mgmtDo(t, s, "DELETE", "/v0/management/api-keys/"+id, nil); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if call("gw-orig") != 401 {
		t.Fatalf("removed key must stop working immediately")
	}
}

func TestStatsAndPorts(t *testing.T) {
	s, _ := mgmtServer(t)
	// Generate some traffic (and an error).
	call := func(key string) int {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	call("gw-orig")
	call("gw-orig")
	call("nope")

	w := mgmtDo(t, s, "GET", "/v0/management/stats", nil)
	if w.Code != 200 {
		t.Fatalf("stats: %d", w.Code)
	}
	var stats struct {
		Ports     map[string]any `json:"ports"`
		Endpoints map[string]struct {
			Requests int `json:"requests"`
			Errors   int `json:"errors"`
		} `json:"endpoints"`
		KeysTotal   int `json:"keys_total"`
		KeysHealthy int `json:"keys_healthy"`
	}
	json.Unmarshal(w.Body.Bytes(), &stats)
	if stats.Ports["http"] != "127.0.0.1:8319" {
		t.Fatalf("ports not reported: %v", stats.Ports)
	}
	ep := stats.Endpoints["/v1/chat/completions"]
	if ep.Requests != 3 || ep.Errors != 1 {
		t.Fatalf("endpoint stats wrong: %+v", ep)
	}
	if stats.KeysTotal != 1 || stats.KeysHealthy != 1 {
		t.Fatalf("key summary wrong: %+v", stats)
	}
}

func TestBadManagementKey(t *testing.T) {
	s, _ := mgmtServer(t)
	req := httptest.NewRequest("POST", "/v0/management/keys", strings.NewReader(`{"key":"x"}`))
	req.Header.Set("X-Management-Key", "wrong")
	req.RemoteAddr = "127.0.0.1:12345"
	loginReset("127.0.0.1")
	defer loginReset("127.0.0.1") // this test spends one failure on this IP
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("wrong mgmt key: got %d", w.Code)
	}
}

func firstChars(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func TestSnapshotHidesSecrets(t *testing.T) {
	s, _ := mgmtServer(t)
	cfg := *s.cfg.Load()
	cfg.Management.SecretKey = "super-secret-mgt"
	cfg.Proxy.SOCKS5 = []config.ProxyEntry{
		{URL: "socks5://user:pass@127.0.0.1:1080"},
	}
	s.Update(&cfg)

	req := httptest.NewRequest("GET", "/v0/management/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Management-Key", "super-secret-mgt")
	loginReset("127.0.0.1")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("config: %d", w.Code)
	}
	body := w.Body.String()
	for _, secret := range []string{"super-secret-mgt", "user:pass"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret leaked into /config response: %s", firstChars(body))
		}
	}
	if !strings.Contains(body, "socks5://***@127.0.0.1:1080") {
		t.Fatalf("proxy URL not masked: %s", firstChars(body))
	}
	if !strings.Contains(body, `"secret-key-set": true`) {
		t.Fatalf("secret-key-set flag missing: %s", firstChars(body))
	}
}

func TestLoopbackNotSpoofable(t *testing.T) {
	s, _ := mgmtServer(t)
	s.cfg.Load().Management.AllowRemote = false

	// A remote peer cannot pass as loopback by setting Host: localhost.
	req := httptest.NewRequest("GET", "http://10.0.0.9:8319/v0/management/keys", nil)
	req.Host = "localhost:8319"
	req.RemoteAddr = "10.0.0.9:54321"
	req.Header.Set("X-Management-Key", "mgt")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("spoofed Host must be rejected, got %d %s", w.Code, w.Body.String())
	}

	// A genuine loopback peer is allowed.
	req2 := httptest.NewRequest("GET", "http://127.0.0.1:8319/v0/management/keys", nil)
	req2.RemoteAddr = "127.0.0.1:54321"
	req2.Header.Set("X-Management-Key", "mgt")
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("loopback must be allowed, got %d", w2.Code)
	}
}

func TestManagementLoginRateLimited(t *testing.T) {
	s, _ := mgmtServer(t)
	ip := "192.0.2.1"
	loginReset(ip)
	// Wrong key maxLoginFailures times from one IP; each attempt is a 401...
	for i := 0; i < maxLoginFailures; i++ {
		req := httptest.NewRequest("GET", "/v0/management/keys", nil)
		req.RemoteAddr = ip + ":1111"
		req.Header.Set("X-Management-Key", "wrong")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("attempt %d: want 401, got %d", i, w.Code)
		}
	}
	// ...after which the IP is locked out even with the correct key.
	req := httptest.NewRequest("GET", "/v0/management/keys", nil)
	req.RemoteAddr = ip + ":1111"
	req.Header.Set("X-Management-Key", "mgt")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 429 {
		t.Fatalf("locked-out IP must get 429 even with the right key, got %d", w.Code)
	}
	// A different IP is unaffected.
	req2 := httptest.NewRequest("GET", "/v0/management/keys", nil)
	req2.RemoteAddr = "198.51.100.7:2222"
	req2.Header.Set("X-Management-Key", "mgt")
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("other IP must still work, got %d", w2.Code)
	}
	loginReset(ip) // leave the locked bucket clean for other tests
}

// TestPanelPasswordIsolatedFromAPIKeys is the separation guarantee: the panel
// password (management secret-key) and the downstream keys clients call /v1/*
// with must not authenticate for each other.
func TestPanelPasswordIsolatedFromAPIKeys(t *testing.T) {
	s, _ := mgmtServer(t)
	cfg := *s.cfg.Load()
	cfg.Management.SecretKey = "panel-pass"
	cfg.Management.AllowRemote = true
	cfg.APIKeys = []config.APIKeyEntry{{Key: "client-key"}}
	s.Update(&cfg)
	loginReset("127.0.0.1")

	// 1) Panel password logs into the panel.
	req := httptest.NewRequest("GET", "/v0/management/keys", nil)
	req.RemoteAddr = "10.0.0.9:1111"
	req.Header.Set("X-Management-Key", "panel-pass")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("panel password must open the panel, got %d", w.Code)
	}

	// 2) A downstream API key must NOT log into the panel.
	req2 := httptest.NewRequest("GET", "/v0/management/keys", nil)
	req2.RemoteAddr = "10.0.0.9:1111"
	req2.Header.Set("X-Management-Key", "client-key")
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if w2.Code != 401 {
		t.Fatalf("downstream api key must not open the panel, got %d", w2.Code)
	}

	// 3) The panel password must NOT call /v1/* as a bearer token.
	req3 := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"auto","messages":[]}`))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Authorization", "Bearer panel-pass")
	w3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w3, req3)
	if w3.Code != 401 {
		t.Fatalf("panel password must not call /v1/*, got %d", w3.Code)
	}

	// 4) A downstream key still calls /v1/* fine (unaffected by panel changes).
	req4 := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"auto","messages":[]}`))
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("Authorization", "Bearer client-key")
	w4 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w4, req4)
	if w4.Code == 401 {
		t.Fatalf("downstream key stopped working: %s", w4.Body.String())
	}
}

// TestPanelPublicPageRemoteBlocked: the panel HTML is served to anyone, but
// with allow-remote=false a remote holder of the correct password still gets
// 403 on the data API — and the password itself stays unverifiable.
func TestPanelPublicPageRemoteBlocked(t *testing.T) {
	s, _ := mgmtServer(t)
	cfg := *s.cfg.Load()
	cfg.Management.SecretKey = "panel-pass"
	cfg.Management.AllowRemote = false
	s.Update(&cfg)
	loginReset("10.0.0.9")

	// Page itself loads from anywhere.
	req := httptest.NewRequest("GET", "/v0/management/panel", nil)
	req.RemoteAddr = "10.0.0.9:1111"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "discovery2api") {
		t.Fatalf("panel page must be public, got %d", w.Code)
	}

	// Data API refuses a remote client even with the right password.
	req2 := httptest.NewRequest("GET", "/v0/management/keys", nil)
	req2.RemoteAddr = "10.0.0.9:1111"
	req2.Header.Set("X-Management-Key", "panel-pass")
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if w2.Code != 403 {
		t.Fatalf("remote client must be 403 without allow-remote, got %d", w2.Code)
	}
	loginReset("10.0.0.9")
}
