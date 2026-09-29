package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ZFXing-lite/discovery2api/internal/config"
	"github.com/ZFXing-lite/discovery2api/internal/proxypool"
)

// loginGuard rate-limits management-key guesses per IP: after maxLoginFailures
// wrong keys, that IP is locked out for loginLockout. Brute-forcing the
// management secret then costs ~minutes per guess.
const (
	maxLoginFailures = 4
	loginLockout     = 15 * time.Minute
)

// loginState tracks wrong-password attempts for one source IP. LockedUntil is
// set when the counter reaches maxLoginFailures and cleared once it expires,
// so a correct password works again after the lockout instead of staying
// locked forever.
type loginState struct {
	mu          sync.Mutex
	fails       int
	lockedUntil time.Time
}

var loginFails sync.Map // ip -> *loginState

// management exposes runtime controls plus the web panel. The whole group is
// disabled (404) when remote-management.secret-key is empty.
func (s *Server) management(w http.ResponseWriter, r *http.Request) {
	if !s.mgmtOn.Load() {
		writeJSON(w, http.StatusNotFound, errBody("not found", "not_found"))
		return
	}
	cfg := s.cfg.Load()

	// The panel itself is public HTML; its JS authenticates every API call.
	// Serve it even when the secret is still empty, so a first boot shows the
	// login page (and a clear "password not set" error) instead of a bare 404.
	p := strings.TrimPrefix(r.URL.Path, "/v0/management")
	if p == "" || p == "/" || p == "/panel" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(panelHTML))
		return
	}

	// Management disabled: 404 without touching the login guard, so probing a
	// disabled endpoint cannot lock out a real IP.
	if strings.TrimSpace(cfg.Management.SecretKey) == "" {
		writeJSON(w, http.StatusNotFound, errBody("not found", "not_found"))
		return
	}

	// Credential for the JSON API.
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Management-Key"))
	}
	ip := clientIPOf(r)
	if locked, until := loginLocked(ip); locked {
		slog.Warn("management login locked out", "ip", ip, "until", until.Format(time.RFC3339))
		writeJSON(w, http.StatusTooManyRequests, errBody("too many failed attempts, try again later", "rate_limited"))
		return
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(cfg.Management.SecretKey), []byte(token)) != 1 {
		loginFail(ip)
		writeJSON(w, http.StatusUnauthorized, errBody("invalid management key", "invalid_mgmt_key"))
		return
	}
	loginReset(ip)
	if !cfg.Management.AllowRemote && !isLoopback(r) {
		writeJSON(w, http.StatusForbidden, errBody("remote management disabled", "remote_forbidden"))
		return
	}

	switch {
	case p == "/endpoints":
		writeJSON(w, http.StatusOK, map[string]any{"endpoints": []string{
			"/config", "/keys", "/keys/bulk", "/keys/batch", "/api-keys", "/usage", "/stats", "/proxies", "/settings", "/panel",
		}})
	case p == "/config":
		b, err := cfg.SnapshotJSON()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errBody(err.Error(), "snapshot_failed"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)

	case p == "/keys" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"keys": s.pool.Status()})
	case p == "/keys" && r.Method == http.MethodPost:
		s.addUpstreamKey(w, r)
	case p == "/keys/bulk" && r.Method == http.MethodPost:
		s.bulkAddUpstreamKeys(w, r)
	case p == "/keys/batch" && r.Method == http.MethodPost:
		s.batchUpstreamKeys(w, r)
	case strings.HasPrefix(p, "/keys/") && r.Method == http.MethodDelete:
		id := idFromPath(p)
		// Mutate config first and persist; then drop from the live pool. The
		// reverse order left a live key with no config entry on partial failure,
		// and a later reload would resurrect it.
		removed := s.mutateConfig(func(c *config.Config) bool { return c.RemoveUpstreamKey(id) })
		if !removed {
			writeJSON(w, http.StatusNotFound, errBody("unknown key id", "unknown_key"))
			return
		}
		s.pool.RemoveKey(id) // id is gone from config; pool may not have it
		s.relayer.InvalidateKey(id)
		slog.Info("upstream key removed", "id", id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case strings.HasPrefix(p, "/keys/") && strings.HasSuffix(p, "/disable") && r.Method == http.MethodPost:
		id := idFromPath(p)
		if s.pool.Disable(id) {
			s.relayer.InvalidateKey(id)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		} else {
			writeJSON(w, http.StatusNotFound, errBody("unknown key id", "unknown_key"))
		}
	case strings.HasPrefix(p, "/keys/") && strings.HasSuffix(p, "/enable") && r.Method == http.MethodPost:
		if s.pool.Enable(idFromPath(p)) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		} else {
			writeJSON(w, http.StatusNotFound, errBody("unknown key id", "unknown_key"))
		}

	case p == "/api-keys" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"keys": cfg.APIKeysMasked()})
	case p == "/api-keys" && r.Method == http.MethodPost:
		s.addAPIKey(w, r)
	case strings.HasPrefix(p, "/api-keys/") && r.Method == http.MethodDelete:
		id := idFromPath(p)
		if !s.mutateConfig(func(c *config.Config) bool { return c.RemoveAPIKey(id) }) {
			writeJSON(w, http.StatusNotFound, errBody("unknown api key id", "unknown_key"))
			return
		}
		slog.Info("downstream api key removed", "id", id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case p == "/usage":
		if s.metrics == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		writeJSON(w, http.StatusOK, s.metrics.Snapshot())

	case p == "/stats":
		s.statsHandler(w, r)

	case p == "/proxies" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"proxies": proxyStatusList(s.proxies)})
	case p == "/proxies" && r.Method == http.MethodPut:
		s.replaceProxies(w, r)

	case p == "/settings" && r.Method == http.MethodGet:
		s.settingsHandler(w, r)
	case p == "/settings" && r.Method == http.MethodPut:
		s.updateSettings(w, r)

	default:
		// Known endpoints with the wrong method return 405 instead of 404, so a
		// GET link cannot silently perform a mutation and the caller learns why.
		if isKnownManagementPath(p) {
			writeJSON(w, http.StatusMethodNotAllowed, errBody("method not allowed", "method_not_allowed"))
			return
		}
		writeJSON(w, http.StatusNotFound, errBody("unknown management endpoint", "not_found"))
	}
}

// isKnownManagementPath reports whether p matches a route whose method differs
// from the one the caller used.
func isKnownManagementPath(p string) bool {
	switch {
	case strings.HasPrefix(p, "/keys/"):
		return strings.HasSuffix(p, "/disable") || strings.HasSuffix(p, "/enable")
	case strings.HasPrefix(p, "/api-keys/"):
		return true
	case p == "/proxies", p == "/settings", p == "/keys", p == "/keys/bulk", p == "/keys/batch", p == "/api-keys":
		return true
	}
	return false
}

// validDiscoveryKey is the panel/API gate. Keys already in config.yaml are not
// re-checked, so a hand-edited file can still boot; new keys typed into the
// panel just need to be non-empty, no whitespace, reasonable length.
func validDiscoveryKey(key string) bool {
	if len(key) < 3 || len(key) > 512 {
		return false
	}
	return !strings.ContainsAny(key, " \t\r\n")
}

func proxyStatusList(p *proxypool.Pool) []proxypool.Status {
	if p == nil {
		return nil
	}
	return p.Status()
}

// addUpstreamKey adds an Intern Discovery key at runtime: pool first (immediate effect),
// then config (survives restart), then persist.
func (s *Server) addUpstreamKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key    string `json:"key"`
		Weight int    `json:"weight"`
		Proxy  string `json:"proxy"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error(), "bad_request"))
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	if !validDiscoveryKey(req.Key) {
		writeJSON(w, http.StatusBadRequest, errBody("key 不能为空、不能含空格、长度 3-512", "bad_request"))
		return
	}
	existed := s.pool.Has(req.Key)
	id := s.pool.AddKey(req.Key, req.Weight, req.Proxy)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errBody("key 无效：不能为空或含空格", "bad_request"))
		return
	}
	if !s.mutateConfig(func(c *config.Config) bool {
		return c.AddUpstreamKey(req.Key, req.Weight, req.Proxy) != ""
	}) {
		// The pool insert already happened. Roll it back, otherwise the panel
		// shows the new key and a failure toast at the same time, and a
		// restart drops the key because it never reached the config file.
		if !existed {
			s.pool.RemoveKey(id)
		}
		writeJSON(w, http.StatusInternalServerError, errBody(s.persistErr(), "persist_failed"))
		return
	}
	slog.Info("upstream key added", "id", id, "weight", req.Weight)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// bulkAddUpstreamKeys imports many keys at once. Each line is one key; blank
// lines and #-comments are ignored and duplicates are collapsed.
func (s *Server) bulkAddUpstreamKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys   []string `json:"keys"`
		Weight int      `json:"weight"`
		Proxy  string   `json:"proxy"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error(), "bad_request"))
		return
	}
	if weight := req.Weight; weight <= 0 {
		req.Weight = 1
	}
	proxy := strings.TrimSpace(req.Proxy)

	seen := map[string]bool{}
	added, updated, skipped := 0, 0, 0
	var ids []string
	for _, raw := range req.Keys {
		k := strings.TrimSpace(raw)
		if k == "" || strings.HasPrefix(k, "#") {
			skipped++
			continue
		}
		if !validDiscoveryKey(k) {
			skipped++
			continue
		}
		if seen[k] {
			skipped++
			continue
		}
		seen[k] = true
		existed := s.pool.Has(k)
		id := s.pool.AddKey(k, req.Weight, proxy)
		if id == "" {
			skipped++
			continue
		}
		persisted := s.mutateConfig(func(c *config.Config) bool {
			return c.AddUpstreamKey(k, req.Weight, proxy) != ""
		})
		if !persisted {
			if !existed {
				s.pool.RemoveKey(id)
			}
			writeJSON(w, http.StatusInternalServerError, errBody(s.persistErr(), "persist_failed"))
			return
		}
		if existed {
			updated++
		} else {
			added++
			ids = append(ids, id)
		}
	}
	slog.Info("bulk import", "added", added, "updated", updated, "skipped", skipped)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "added": added, "updated": updated, "skipped": skipped, "ids": ids,
	})
}

// addAPIKey generates a downstream key in sk-xxxxxx format from a user-supplied name.
func (s *Server) addAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Key  string `json:"key"` // optional: explicit key (back-compat for tests)
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error(), "bad_request"))
		return
	}
	var key string
	if k := strings.TrimSpace(req.Key); k != "" {
		key = k
	} else {
		key = genAPIKey()
	}
	name := strings.TrimSpace(req.Name)
	if errText := s.mutateReason(func(c *config.Config) bool { return c.AddAPIKey(key, name) }); errText != "" {
		code := http.StatusConflict
		kind := "duplicate"
		if errText != "empty or duplicate" {
			code = http.StatusInternalServerError
			kind = "persist_failed"
		}
		writeJSON(w, code, errBody(errText, kind))
		return
	}
	id := config.UpstreamKeyID(key)
	slog.Info("downstream api key added", "id", id, "name", name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "key": key, "name": name})
}

// genAPIKey returns sk- + 32 random hex chars.
func genAPIKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use time-based pseudo-random (extremely unlikely path).
		for i := range b {
			b[i] = byte(time.Now().UnixNano() >> uint(i))
		}
	}
	return "sk-" + hexEncode(b)
}

func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0xf]
	}
	return string(out)
}

// batchUpstreamKeys performs bulk enable/disable/delete on upstream keys.
func (s *Server) batchUpstreamKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error(), "bad_request"))
		return
	}
	if len(req.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("no ids provided", "bad_request"))
		return
	}
	switch req.Action {
	case "del":
		removed := 0
		for _, id := range req.IDs {
			if s.mutateConfig(func(c *config.Config) bool { return c.RemoveUpstreamKey(id) }) {
				s.pool.RemoveKey(id)
				s.relayer.InvalidateKey(id)
				removed++
			}
		}
		slog.Info("batch delete upstream keys", "count", removed)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": removed})
	case "disable":
		affected := 0
		for _, id := range req.IDs {
			if s.pool.Disable(id) {
				s.relayer.InvalidateKey(id)
				affected++
			}
		}
		slog.Info("batch disable upstream keys", "count", affected)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected})
	case "enable":
		affected := 0
		for _, id := range req.IDs {
			if s.pool.Enable(id) {
				affected++
			}
		}
		slog.Info("batch enable upstream keys", "count", affected)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": affected})
	default:
		writeJSON(w, http.StatusBadRequest, errBody("unknown action: use del/disable/enable", "bad_request"))
	}
}

// mutateConfig applies fn to a copy of the live config, publishes it to all
// components and persists it atomically. The whole read-modify-write is
// serialized: concurrent management requests would otherwise each replay from
// a stale snapshot and silently drop each other's changes.
func (s *Server) mutateConfig(fn func(*config.Config) bool) bool {
	s.mgmtMu.Lock()
	defer s.mgmtMu.Unlock()

	cur := s.cfg.Load()
	cp := *cur
	cp.Upstream.Keys = append([]config.UpstreamKey(nil), cur.Upstream.Keys...)
	cp.APIKeys = append([]config.APIKeyEntry(nil), cur.APIKeys...)
	cp.Proxy.SOCKS5 = append([]config.ProxyEntry(nil), cur.Proxy.SOCKS5...)
	if !fn(&cp) {
		return false
	}
	// Persist before publishing: a failed write would otherwise make the live
	// pool drift from what survives a restart.
	if err := cp.Save(s.configPath); err != nil {
		s.notePersistErr(err)
		slog.Error("persist config failed", "err", err, "path", s.configPath)
		return false
	}
	s.notePersistErr(nil)
	if st, err := os.Stat(s.configPath); err == nil {
		s.MarkSaved(st.ModTime())
	}
	// Config on disk is now authoritative; reconcile the pool to it. Keys still
	// in flight keep working: SyncKeys only drops pool entries that are gone
	// from config, and they were just removed by fn above.
	s.Update(&cp)
	s.pool.SyncKeys(toPoolKeys(&cp))
	if s.rebuild != nil {
		s.rebuild(&cp)
	}
	return true
}

// mutateReason is mutateConfig with a reason the panel can show. An empty
// return means the change was saved.
func (s *Server) mutateReason(fn func(*config.Config) bool) string {
	if s.mutateConfig(fn) {
		return ""
	}
	if msg := s.persistErr(); msg != "" && msg != "保存失败" {
		return msg
	}
	return "empty or duplicate"
}

func (s *Server) notePersistErr(err error) {
	if err == nil {
		s.lastPersistErr.Store("")
		return
	}
	s.lastPersistErr.Store(err.Error())
}

func (s *Server) persistErr() string {
	if v := s.lastPersistErr.Load(); v != nil {
		if msg, ok := v.(string); ok && msg != "" {
			if strings.Contains(msg, "permission denied") || strings.Contains(msg, "read-only") {
				return "配置文件不可写，无法保存。请确认 config.yaml 对运行用户可写，Docker 挂载不要使用 :ro"
			}
			return "配置没有保存：" + msg
		}
	}
	return "保存失败"
}

// replaceProxies replaces the SOCKS5 pool from the panel. An empty list
// switches the gateway to direct connections. The new pool takes effect on
// the next upstream request.
func (s *Server) replaceProxies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proxies []struct {
			URL    string `json:"url"`
			Weight int    `json:"weight"`
		} `json:"proxies"`
		Policy string `json:"policy"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error(), "bad_request"))
		return
	}
	entries := make([]config.ProxyEntry, 0, len(req.Proxies))
	for _, p := range req.Proxies {
		u := strings.TrimSpace(p.URL)
		if u == "" {
			continue
		}
		if err := proxypool.ParseURL(u); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("invalid proxy "+u+": "+err.Error(), "bad_proxy"))
			return
		}
		wht := p.Weight
		if wht <= 0 {
			wht = 1
		}
		entries = append(entries, config.ProxyEntry{URL: u, Weight: wht})
	}
	policy := strings.ToLower(strings.TrimSpace(req.Policy))
	if !s.mutateConfig(func(c *config.Config) bool {
		c.Proxy.SOCKS5 = entries
		if policy != "" {
			switch policy {
			case "round-robin", "random", "sticky-key":
				c.Proxy.Policy = policy
			default:
				return false
			}
		}
		return true
	}) {
		writeJSON(w, http.StatusBadRequest, errBody("invalid proxy policy or config not persisted", "bad_request"))
		return
	}
	slog.Info("proxy pool replaced", "count", len(entries), "policy", s.cfg.Load().Proxy.Policy)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(entries)})
}

// settingsHandler returns the knobs the panel can edit without a restart.
func (s *Server) settingsHandler(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	acct := cfg.Account
	if acct.Formula == "" || !isASCII(acct.Formula) {
		acct.Formula = "\u672a\u7f13\u5b58\u8f93\u5165 \u00d7 20% + \u8f93\u51fa\uff0c\u7f13\u5b58\u8f93\u5165\u514d\u8d39"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":      cfg.Upstream.BaseURL,
		"default_model": cfg.Upstream.DefaultModel,
		"force_model":   cfg.Upstream.ForceModel,
		"proxy_policy":  cfg.Proxy.Policy,
		"allow_remote":  cfg.Management.AllowRemote,
		"log_level":     cfg.Log.Level,
		"tls_enable":    cfg.TLS.Enable,
		"account":       acct,
	})
}

// isASCII reports whether s contains only ASCII bytes.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// updateSettings applies model / force-model / proxy-policy edits from the
// panel. Empty fields are left unchanged.
func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DefaultModel *string `json:"default_model"`
		ForceModel   *bool   `json:"force_model"`
		ProxyPolicy  *string `json:"proxy_policy"`
		BaseURL      *string `json:"base_url"`
		AllowRemote  *bool   `json:"allow_remote"`
		LogLevel     *string `json:"log_level"`
		TLSEnable    *bool   `json:"tls_enable"`
		TokenQuota   *int64  `json:"token_quota"`
		TokenUsed    *int64  `json:"token_used"`
		Formula      *string `json:"formula"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error(), "bad_request"))
		return
	}
	if !s.mutateConfig(func(c *config.Config) bool {
		if req.DefaultModel != nil {
			m := strings.TrimSpace(*req.DefaultModel)
			if m == "" {
				return false
			}
			c.Upstream.DefaultModel = m
		}
		if req.ForceModel != nil {
			c.Upstream.ForceModel = *req.ForceModel
		}
		if req.BaseURL != nil {
			u := strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/")
			if u == "" {
				return false
			}
			c.Upstream.BaseURL = u
		}
		if req.ProxyPolicy != nil {
			p := strings.ToLower(strings.TrimSpace(*req.ProxyPolicy))
			switch p {
			case "round-robin", "random", "sticky-key":
				c.Proxy.Policy = p
			default:
				return false
			}
		}
		if req.AllowRemote != nil {
			c.Management.AllowRemote = *req.AllowRemote
		}
		if req.LogLevel != nil {
			lv := strings.ToLower(strings.TrimSpace(*req.LogLevel))
			switch lv {
			case "debug", "info", "warn", "error":
				c.Log.Level = lv
			default:
				return false
			}
		}
		if req.TLSEnable != nil {
			c.TLS.Enable = *req.TLSEnable
		}
		if req.TokenQuota != nil {
			c.Account.TokenQuota = *req.TokenQuota
		}
		if req.TokenUsed != nil {
			c.Account.TokenUsed = *req.TokenUsed
		}
		if req.Formula != nil {
			c.Account.Formula = strings.TrimSpace(*req.Formula)
		}
		return true
	}) {
		writeJSON(w, http.StatusBadRequest, errBody("invalid settings or config not persisted", "bad_request"))
		return
	}
	slog.Info("gateway settings updated")
	s.settingsHandler(w, r)
}

// statsHandler returns ports, endpoint call counters and health for the panel.
func (s *Server) statsHandler(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	total, healthy := s.pool.Summary()
	resp := map[string]any{
		"ports": map[string]any{
			"http":          cfg.ListenAddr(),
			"tls":           cfg.TLS.Enable,
			"auth_required": cfg.AuthRequired(),
			"mgmt_remote":   cfg.Management.AllowRemote,
			"upstream":      cfg.Upstream.BaseURL,
			"default_model": cfg.Upstream.DefaultModel,
		},
		"endpoints":     s.stats.Snapshot(),
		"keys_total":    total,
		"keys_healthy":  healthy,
		"proxies_total": len(cfg.Proxy.SOCKS5),
	}
	if s.proxies != nil {
		resp["proxies"] = s.proxies.Status()
		resp["proxies_total"] = len(s.proxies.Status())
	}
	writeJSON(w, http.StatusOK, resp)
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(v)
}

// idFromPath extracts the key id from "/keys/<id>/disable" style paths.
func idFromPath(p string) string {
	for _, pref := range []string{"/keys/", "/api-keys/"} {
		if strings.HasPrefix(p, pref) {
			rest := p[len(pref):]
			for _, suf := range []string{"/disable", "/enable"} {
				rest = strings.TrimSuffix(rest, suf)
			}
			return path.Clean("/" + rest)[1:]
		}
	}
	return path.Clean("/" + p)[1:]
}

// isLoopback reports whether the request came from this machine. Only
// RemoteAddr is trusted: r.Host and X-Forwarded-For are client-controlled and
// would otherwise bypass allow-remote: false.
func isLoopback(r *http.Request) bool {
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i > 0 {
		ip = ip[:i]
	}
	ip = strings.Trim(ip, "[]")
	switch ip {
	case "127.0.0.1", "::1", "":
		return true
	}
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		switch h {
		case "127.0.0.1", "::1":
			return true
		}
	}
	return false
}

func clientIPOf(r *http.Request) string {
	ip := r.RemoteAddr
	if h, _, err := net.SplitHostPort(ip); err == nil {
		return h
	}
	return ip
}

func loginStateOf(ip string) *loginState {
	v, _ := loginFails.LoadOrStore(ip, &loginState{})
	return v.(*loginState)
}

func loginLocked(ip string) (bool, time.Time) {
	st := loginStateOf(ip)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.lockedUntil.IsZero() {
		return false, time.Time{}
	}
	if time.Now().Before(st.lockedUntil) {
		return true, st.lockedUntil
	}
	// Lockout elapsed: a correct password can get in again.
	st.lockedUntil = time.Time{}
	st.fails = 0
	return false, time.Time{}
}

func loginFail(ip string) {
	st := loginStateOf(ip)
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	if !st.lockedUntil.IsZero() && !now.Before(st.lockedUntil) {
		st.lockedUntil = time.Time{}
		st.fails = 0
	}
	st.fails++
	if st.fails >= maxLoginFailures {
		st.lockedUntil = now.Add(loginLockout)
	}
}

func loginReset(ip string) { loginFails.Delete(ip) }
