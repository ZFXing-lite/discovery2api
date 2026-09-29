package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZFXing-lite/discovery2api/internal/config"
	"github.com/ZFXing-lite/discovery2api/internal/proxypool"
)

func TestLoginLockoutExpires(t *testing.T) {
	s, _ := mgmtServer(t)
	ip := "203.0.113.9"
	loginReset(ip)
	defer loginReset(ip)

	for i := 0; i < maxLoginFailures; i++ {
		req := httptest.NewRequest("GET", "/v0/management/keys", nil)
		req.RemoteAddr = ip + ":9"
		req.Header.Set("X-Management-Key", "wrong")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("attempt %d: want 401, got %d", i, w.Code)
		}
	}
	locked := httptest.NewRequest("GET", "/v0/management/keys", nil)
	locked.RemoteAddr = ip + ":9"
	locked.Header.Set("X-Management-Key", "mgt")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, locked)
	if w.Code != 429 {
		t.Fatalf("fresh lockout must be 429, got %d", w.Code)
	}

	// The lock is time-based. Once it has elapsed the correct password works
	// again; the old counter never released a locked IP.
	st := loginStateOf(ip)
	st.mu.Lock()
	st.lockedUntil = time.Now().Add(-time.Second)
	st.mu.Unlock()

	again := httptest.NewRequest("GET", "/v0/management/keys", nil)
	again.RemoteAddr = ip + ":9"
	again.Header.Set("X-Management-Key", "mgt")
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, again)
	if w2.Code != 200 {
		t.Fatalf("correct key after lockout must succeed, got %d %s", w2.Code, w2.Body.String())
	}
}

func TestRejectsMalformedUpstreamKey(t *testing.T) {
	s, _ := mgmtServer(t)
	w := mgmtDo(t, s, "POST", "/v0/management/keys", map[string]any{"key": "key with spaces"})
	if w.Code != 400 {
		t.Fatalf("malformed key must be 400, got %d %s", w.Code, w.Body.String())
	}
	if _, healthy := s.pool.Summary(); healthy != 1 {
		t.Fatal("rejected key must not enter the pool")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s, _ := mgmtServer(t)
	w := mgmtDo(t, s, "PUT", "/v0/management/settings", map[string]any{
		"default_model": "auto",
		"force_model":   false,
		"proxy_policy":  "sticky-key",
	})
	if w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		DefaultModel string `json:"default_model"`
		ForceModel   bool   `json:"force_model"`
		ProxyPolicy  string `json:"proxy_policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.DefaultModel != "auto" || got.ForceModel || got.ProxyPolicy != "sticky-key" {
		t.Fatalf("settings not applied: %+v", got)
	}
	// Bad policy is rejected and does not clobber the last good value.
	bad := mgmtDo(t, s, "PUT", "/v0/management/settings", map[string]any{"proxy_policy": "first"})
	if bad.Code != 400 {
		t.Fatalf("bad policy must be 400, got %d", bad.Code)
	}
	if s.cfg.Load().Proxy.Policy != "sticky-key" {
		t.Fatalf("bad policy overwrote live config: %s", s.cfg.Load().Proxy.Policy)
	}
}

func TestProxyPoolReplaceFromPanel(t *testing.T) {
	s, _ := mgmtServer(t)
	var rebuilt int
	s.SetProxyRebuilder(func(cfg *config.Config) {
		rebuilt++
		next := proxypool.New(nil, cfg.Proxy.Policy, time.Second)
		// Build from the config the panel just saved.
		entries := make([]struct {
			URL    string
			Weight int
		}, 0, len(cfg.Proxy.SOCKS5))
		for _, e := range cfg.Proxy.SOCKS5 {
			entries = append(entries, struct {
				URL    string
				Weight int
			}{URL: e.URL, Weight: e.Weight})
		}
		next = proxypool.New(entries, cfg.Proxy.Policy, time.Second)
		if s.proxies == nil {
			s.proxies = next
			return
		}
		s.proxies.Replace(next)
	})
	s.proxies = proxypool.New(nil, "round-robin", time.Second)

	bad := mgmtDo(t, s, "PUT", "/v0/management/proxies", map[string]any{
		"proxies": []map[string]any{{"url": "http://not-socks:1"}},
	})
	if bad.Code != 400 {
		t.Fatalf("bad proxy must be 400, got %d %s", bad.Code, bad.Body.String())
	}

	ok := mgmtDo(t, s, "PUT", "/v0/management/proxies", map[string]any{
		"proxies": []map[string]any{{"url": "socks5://u:p@127.0.0.1:1080", "weight": 2}},
		"policy":  "random",
	})
	if ok.Code != 200 {
		t.Fatalf("replace: %d %s", ok.Code, ok.Body.String())
	}
	if rebuilt == 0 {
		t.Fatal("proxy rebuilder was not called")
	}
	if s.proxies.Empty() {
		t.Fatal("live pool stayed empty after replace")
	}
	st := s.proxies.Status()
	if len(st) != 1 || !strings.Contains(st[0].URL, "127.0.0.1:1080") {
		t.Fatalf("pool snapshot: %+v", st)
	}
	if strings.Contains(st[0].URL, "u:p") {
		t.Fatalf("proxy credentials leaked: %s", st[0].URL)
	}
	if s.cfg.Load().Proxy.Policy != "random" {
		t.Fatalf("policy not saved: %s", s.cfg.Load().Proxy.Policy)
	}
}

func TestPanelOffersSettingsAndProxies(t *testing.T) {
	s, _ := mgmtServer(t)
	w := mgmtDo(t, s, "GET", "/v0/management/panel", nil)
	body := w.Body.String()
	for _, needle := range []string{"保存设置", "保存代理池", "/v0/management/settings", "/v0/management/proxies"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("panel missing %q", needle)
		}
	}
}
