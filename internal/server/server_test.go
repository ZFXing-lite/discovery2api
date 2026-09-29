package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZFXing-lite/discovery2api/internal/config"
	"github.com/ZFXing-lite/discovery2api/internal/keypool"
	"github.com/ZFXing-lite/discovery2api/internal/metrics"
	"github.com/ZFXing-lite/discovery2api/internal/relay"
)

func apiKeysFromStrings(keys []string) []config.APIKeyEntry {
	out := make([]config.APIKeyEntry, len(keys))
	for i, k := range keys {
		out[i] = config.APIKeyEntry{Key: k}
	}
	return out
}

func testServer(t *testing.T, apiKeys ...string) (*Server, *keypool.Pool) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"1","model":"auto","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(up.Close)

	pool := keypool.New([]keypool.UpstreamKey{{Key: "test"}}, keypool.Settings{MaxRetries: 2, RetryOn: []int{429, 500}}, "")
	r := relay.New(pool, nil, relay.Config{
		BaseURL: up.URL, DefaultModel: "auto", ForceModel: true,
		Timeout: 10_000_000_000,
	}, nil)

	cfg := &config.Config{Host: "", Port: 8319, APIKeys: apiKeysFromStrings(apiKeys)}
	cfg.Upstream.DefaultModel = "auto"
	return New(cfg, r, pool, nil, metrics.New(""), "config.yaml"), pool
}

func do(t *testing.T, s *Server, method, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(`{"model":"auto","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	loginReset("") // keep the login guard from leaking across tests
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	loginReset("")
	return w
}

func TestAuthRejected(t *testing.T) {
	s, _ := testServer(t, "gw-secret")
	if w := do(t, s, "POST", "/v1/chat/completions", ""); w.Code != 401 {
		t.Fatalf("missing key: got %d", w.Code)
	}
	if w := do(t, s, "POST", "/v1/chat/completions", "wrong"); w.Code != 401 {
		t.Fatalf("wrong key: got %d", w.Code)
	}
	if w := do(t, s, "POST", "/v1/chat/completions", "gw-secret"); w.Code != 200 {
		t.Fatalf("valid key: got %d body %s", w.Code, w.Body.String())
	}
	// x-api-key form must work too.
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"auto","messages":[]}`))
	req.Header.Set("x-api-key", "gw-secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("x-api-key auth: got %d", w.Code)
	}
}

func TestAuthOpenWhenNoKeys(t *testing.T) {
	s, _ := testServer(t)
	if w := do(t, s, "POST", "/v1/chat/completions", ""); w.Code != 200 {
		t.Fatalf("open gateway: got %d", w.Code)
	}
}

func TestModelsEndpoint(t *testing.T) {
	s, _ := testServer(t)
	w := do(t, s, "GET", "/v1/models", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "auto") {
		t.Fatalf("models: %d %s", w.Code, w.Body.String())
	}
}

func TestHealthAndStatus(t *testing.T) {
	s, _ := testServer(t)
	w := do(t, s, "GET", "/healthz", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ready":true`) {
		t.Fatalf("healthz: %d %s", w.Code, w.Body.String())
	}
	w = do(t, s, "GET", "/status", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "keys") {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	// Key ids must never appear raw; they are hashes.
}

func TestManagementDisabledWithoutPassword(t *testing.T) {
	s, _ := testServer(t)
	cfg := s.cfg.Load()
	cfg.Management.SecretKey = ""
	s.Update(cfg)
	w := do(t, s, "GET", "/v0/management/keys", "")
	if w.Code != 404 {
		t.Fatalf("management API must be 404 without secret-key: got %d", w.Code)
	}
	page := do(t, s, "GET", "/v0/management/panel", "")
	if page.Code != 200 {
		t.Fatalf("panel page should still explain the missing password, got %d", page.Code)
	}
}

func TestManagementWithSecretKey(t *testing.T) {
	s, _ := testServer(t)
	cfg := s.cfg.Load()
	cfg.Management.SecretKey = "mgt-secret"
	cfg.Management.AllowRemote = true // tests are not loopback-origin
	s.Update(cfg)                     // mgmt enable flag is computed here

	w := do(t, s, "GET", "/v0/management/keys", "mgt-secret")
	if w.Code != 200 {
		t.Fatalf("keys listing: got %d %s", w.Code, w.Body.String())
	}
	w = do(t, s, "GET", "/v0/management/config", "mgt-secret")
	if w.Code != 200 || strings.Contains(w.Body.String(), "test") {
		t.Fatalf("config must mask keys: %d %s", w.Code, w.Body.String())
	}
	w = do(t, s, "GET", "/v0/management/keys", "bad")
	if w.Code != 401 {
		t.Fatalf("bad mgmt key: got %d", w.Code)
	}
}

func TestUnknownPath(t *testing.T) {
	s, _ := testServer(t)
	w := do(t, s, "POST", "/v1/nope", "")
	if w.Code != 404 {
		t.Fatalf("unknown path: got %d", w.Code)
	}
}

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(httptest.NewRecorder(), nil)))
	m.Run()
}
