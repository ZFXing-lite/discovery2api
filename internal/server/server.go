// Package server wires the HTTP front-end: routes for the three Intern Discovery
// interfaces, downstream api-key authentication, CORS, request logging,
// health endpoints and a small management API.
package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ZFXing-lite/discovery2api/internal/config"
	"github.com/ZFXing-lite/discovery2api/internal/credits"
	"github.com/ZFXing-lite/discovery2api/internal/keypool"
	"github.com/ZFXing-lite/discovery2api/internal/metrics"
	"github.com/ZFXing-lite/discovery2api/internal/proxypool"
	"github.com/ZFXing-lite/discovery2api/internal/relay"
)

// ProxyRebuilder rebuilds the live SOCKS5 pool from config. main wires this
// so panel edits and file reloads both swap the pool the relayer dials
// through, instead of logging "restart to apply" and leaving the old pool.
type ProxyRebuilder func(cfg *config.Config)

// Server is the gateway HTTP front-end.
type Server struct {
	cfg     atomic.Pointer[config.Config]
	relayer *relay.Relayer
	pool    *keypool.Pool
	proxies *proxypool.Pool
	metrics *metrics.Recorder
	stats   *Stats
	mgmtOn  atomic.Bool
	// mgmtMu serializes read-modify-write of the live config so concurrent
	// management requests cannot drop each other's updates.
	mgmtMu     sync.Mutex
	log        *slog.Logger
	started    time.Time
	configPath string
	rebuild    ProxyRebuilder
	// savedAt is the mtime of the config file written by mutateConfig. The
	// file watcher uses it to ignore its own write, which would otherwise
	// reload a copy that dropped the just-applied change when env overrides
	// are in play, or race the live pool.
	savedAt atomic.Int64
	// lastPersistErr is the most recent config save failure, so the panel can
	// say why an add failed instead of a generic conflict.
	lastPersistErr atomic.Value
	// credits probes upstream keys for 墨点 (ink points) balance status.
	credits *credits.Probe
}

func New(cfg *config.Config, relayer *relay.Relayer, pool *keypool.Pool,
	proxies *proxypool.Pool, rec *metrics.Recorder, configPath string) *Server {
	s := &Server{relayer: relayer, pool: pool, proxies: proxies, metrics: rec,
		stats: NewStats(), configPath: configPath,
		log: slog.With("component", "server"), started: time.Now()}
	s.cfg.Store(cfg)
	s.mgmtOn.Store(true)
	// Credits probe: lightweight GET /v1/models to detect 墨点 exhaustion.
	timeout := time.Duration(cfg.Credits.Timeout)
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	s.credits = credits.New(cfg.Upstream.BaseURL, timeout)
	return s
}

func (s *Server) Update(cfg *config.Config) {
	s.cfg.Store(cfg)
	s.mgmtOn.Store(true)
}

// SetProxyRebuilder installs the hook that rebuilds the SOCKS5 pool after a
// config mutation or reload.
func (s *Server) SetProxyRebuilder(fn ProxyRebuilder) { s.rebuild = fn }

// MarkSaved records the mtime of a config write the watcher should skip.
func (s *Server) MarkSaved(t time.Time) { s.savedAt.Store(t.UnixNano()) }

// OwnWrite reports whether mod is the config file this process just wrote.
func (s *Server) OwnWrite(mod time.Time) bool {
	saved := s.savedAt.Load()
	if saved == 0 || mod.IsZero() {
		return false
	}
	return mod.UnixNano() == saved
}

// ApplyReload is the hot-reload entry point: it publishes the freshly loaded
// config and reconciles the key pool to it. Mutations made through the
// management API go through mutateConfig, which persists before publishing, so
// a reload can only ever see a config file that matches the live state.
func (s *Server) ApplyReload(cfg *config.Config, pool *keypool.Pool) {
	s.mgmtMu.Lock()
	defer s.mgmtMu.Unlock()
	s.Update(cfg)
	pool.SyncKeys(toPoolKeys(cfg))
}

// Handler returns the root mux. It is rebuilt on config hot-reload by callers
// that swap the mux via atomic.Pointer (see cmd/server/main.go).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.relay(relay.KindChat))
	mux.HandleFunc("/v1/messages", s.relay(relay.KindMessages))
	mux.HandleFunc("/v1/messages/count_tokens", s.relay(relay.KindMessages))
	mux.HandleFunc("/v1/responses", s.relay(relay.KindResponses))
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/status", s.status)
	mux.HandleFunc("/v0/management/", s.management)
	mux.HandleFunc("/", s.notFound)

	var h http.Handler = mux
	h = s.authMiddleware(h)
	h = s.loggingMiddleware(h)
	h = corsMiddleware(h)
	return h
}

// relay returns a handler for one Intern Discovery interface.
func (s *Server) relay(kind relay.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeJSON(w, http.StatusMethodNotAllowed, errBody("method not allowed", "method_not_allowed"))
			return
		}
		if kind == relay.KindMessages && strings.HasSuffix(r.URL.Path, "/count_tokens") {
			// count_tokens is not documented on Intern Discovery; answer locally.
			writeJSON(w, http.StatusOK, map[string]any{"input_tokens": 0})
			return
		}
		s.relayer.Handle(w, r, kind)
	}
}

// models proxies /v1/models to the upstream when keys are available, falling
// back to a static catalog of known Discovery models when the pool is empty or
// the upstream is unreachable. The Discovery platform supports multiple models,
// so proxying gives clients the real list.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Load()
	baseURL := strings.TrimRight(cfg.Upstream.BaseURL, "/")

	// Try to proxy to upstream when we have keys.
	lease, err := s.pool.Pick(nil)
	if err == nil {
		upURL := baseURL + "/v1/models"
		upReq, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, upURL, nil)
		if reqErr == nil {
			upReq.Header.Set("Authorization", "Bearer "+lease.Entry.Key)
			upReq.Header.Set("Accept", "application/json")
			// Use the relayer's transport so SOCKS5 proxy pool and per-key
			// proxy overrides apply to the models fetch too.
			var client *http.Client
			if s.relayer != nil {
				rt := s.relayer.TransportFor(lease, relay.Config{
					BaseURL:        baseURL,
					ConnectTimeout: time.Duration(cfg.Upstream.ConnectTimeout),
				})
				client = &http.Client{Transport: rt, Timeout: 10 * time.Second}
			} else {
				client = &http.Client{Timeout: 10 * time.Second}
			}
			resp, doErr := client.Do(upReq)
			if doErr == nil {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				lease.Release()
				if readErr == nil && resp.StatusCode == http.StatusOK && len(body) > 0 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(body)
					return
				}
			} else {
				lease.Release()
			}
		} else {
			lease.Release()
		}
	}

	// Fallback: return a static catalog of known Discovery models.
	s.serveStaticModels(w, r, cfg)
}

// knownDiscoveryModels is the fallback catalog used when the upstream is
// unreachable or no keys are configured. These are the models documented on
// the Intern Discovery platform.
var knownDiscoveryModels = []map[string]any{
	{"id": "auto", "object": "model", "owned_by": "discovery", "context_length": 256000},
	{"id": "deepseek-v3", "object": "model", "owned_by": "discovery", "context_length": 64000},
	{"id": "deepseek-r1", "object": "model", "owned_by": "discovery", "context_length": 64000},
	{"id": "glm-4", "object": "model", "owned_by": "discovery", "context_length": 128000},
	{"id": "glm-4-flash", "object": "model", "owned_by": "discovery", "context_length": 128000},
	{"id": "qwen-plus", "object": "model", "owned_by": "discovery", "context_length": 131072},
	{"id": "qwen-turbo", "object": "model", "owned_by": "discovery", "context_length": 8192},
	{"id": "kimi-k1.5", "object": "model", "owned_by": "discovery", "context_length": 128000},
}

// serveStaticModels returns a static model catalog. Anthropic clients get the
// Anthropic-shaped response; everyone else gets the OpenAI-shaped one.
func (s *Server) serveStaticModels(w http.ResponseWriter, r *http.Request, cfg *config.Config) {
	model := cfg.Upstream.DefaultModel
	if model == "" {
		model = "auto"
	}
	isAnthropic := strings.Contains(strings.ToLower(r.Header.Get("anthropic-version")), "1.0") ||
		strings.Contains(strings.ToLower(r.UserAgent()), "claude")
	if isAnthropic {
		data := make([]map[string]any, 0, len(knownDiscoveryModels))
		for _, m := range knownDiscoveryModels {
			data = append(data, map[string]any{
				"type": "model", "id": m["id"], "display_name": m["id"],
				"created_at": s.started.Unix(),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return
	}
	data := make([]map[string]any, 0, len(knownDiscoveryModels))
	for _, m := range knownDiscoveryModels {
		entry := map[string]any{
			"id": m["id"], "object": "model", "created": s.started.Unix(),
			"owned_by": m["owned_by"], "context_length": m["context_length"],
		}
		data = append(data, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

// healthz reports process liveness. It stays 200 while the process is up so a
// fresh deploy (panel password set, upstream keys still empty) is not marked
// failed by Docker and restarted in a loop. Key readiness is "ready".
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	total, healthy := s.pool.Summary()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "ready": healthy > 0,
		"keys": total, "healthy": healthy,
		"uptime_seconds": time.Since(s.started).Seconds(),
	})
}

// status is a masked view of the pool for operators.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"keys": s.pool.Status(),
	}
	if rec := s.metrics; rec != nil {
		resp["usage"] = rec.Snapshot()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, errBody(
		"unknown path; supported: /v1/chat/completions, /v1/messages, /v1/responses, /v1/models",
		"not_found"))
}

// --- middleware -----------------------------------------------------------

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.cfg.Load()
		// Public endpoints, plus the management group, which carries its own
		// secret-key auth and must stay reachable even when downstream
		// api-keys are configured.
		if r.URL.Path == "/healthz" || r.URL.Path == "/status" ||
			strings.HasPrefix(r.URL.Path, "/v0/management") {
			next.ServeHTTP(w, r)
			return
		}
		// Only the known API endpoints require downstream auth. Unknown paths
		// get 404 directly — returning 401 for scanner probes (.env, /key,
		// /register, …) leaks that a gateway is listening.
		if !isAPIPath(r.URL.Path) {
			s.notFound(w, r)
			return
		}
		if !cfg.AuthRequired() {
			next.ServeHTTP(w, r)
			return
		}
		token := extractToken(r)
		if token == "" || !cfg.IsAuthorized(token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="discovery2api"`)
			writeJSON(w, http.StatusUnauthorized, errBody(
				"missing or invalid api key", "invalid_api_key"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAPIPath reports whether p is one of the documented downstream endpoints.
func isAPIPath(p string) bool {
	switch p {
	case "/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens",
		"/v1/responses", "/v1/models":
		return true
	}
	return false
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if t := strings.TrimSpace(strings.TrimPrefix(h, "Bearer")); t != "" {
			return t
		}
	}
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return strings.TrimSpace(k)
	}
	if k := r.Header.Get("x-api-key"); k != "" {
		return strings.TrimSpace(k)
	}
	return r.URL.Query().Get("key")
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		track := strings.HasPrefix(r.URL.Path, "/v1/")
		if track {
			s.stats.Begin(r.URL.Path)
		}
		rw := &statusWriter{ResponseWriter: w, status: 200}
		// next recovers panics itself, but if it ever does not the deferred End
		// keeps the inflight counter from leaking forever.
		defer func() {
			errMsg := ""
			if rw.status >= 400 {
				errMsg = truncateMsg(rw.bodySnippet())
			}
			if track {
				s.stats.End(r.URL.Path, rw.status, time.Since(start), errMsg)
			}
		}()
		next.ServeHTTP(rw, r)
		s.log.Info("http", "method", r.Method, "path", r.URL.Path,
			"status", rw.status, "bytes", rw.bytes,
			"ip", clientIP(r), "took", time.Since(start).Round(time.Millisecond))
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version, anthropic-beta, Accept")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	// errBuf keeps the beginning of an error response for the panel.
	errBuf []byte
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	n, err := s.ResponseWriter.Write(p)
	s.bytes += n
	if s.status >= 400 && len(s.errBuf) < 512 {
		s.errBuf = append(s.errBuf, p[:min(n, 512-len(s.errBuf))]...)
	}
	return n, err
}

func (s *statusWriter) bodySnippet() string {
	return string(s.errBuf)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- helpers --------------------------------------------------------------

func clientIP(r *http.Request) string {
	if ff := r.Header.Get("X-Forwarded-For"); ff != "" {
		return strings.TrimSpace(strings.Split(ff, ",")[0])
	}
	return r.RemoteAddr
}

// toPoolKeys converts the configured upstream keys to the pool's input.
func toPoolKeys(cfg *config.Config) []keypool.UpstreamKey {
	out := make([]keypool.UpstreamKey, 0, len(cfg.Upstream.Keys))
	for _, k := range cfg.Upstream.Keys {
		out = append(out, keypool.UpstreamKey{Key: k.Key, Weight: k.Weight, Proxy: k.Proxy})
	}
	return out
}

func errBody(msg, code string) map[string]any {
	return map[string]any{"error": map[string]any{
		"message": msg, "type": "discovery2api_error", "code": code,
	}}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

// --- credits (墨点) probe -------------------------------------------------

// creditsTransportFactory returns a TransportFactory for the credits probe that
// respects per-key proxy overrides and the shared SOCKS5 proxy pool, matching
// the relay's transport selection logic.
func (s *Server) creditsTransportFactory() credits.TransportFactory {
	return func(id, proxyURL string) *http.Transport {
		if proxyURL != "" {
			return proxypool.TransportForURL(proxyURL)
		}
		if s.proxies != nil && !s.proxies.Empty() {
			return s.proxies.Transport(id, nil)
		}
		return proxypool.TransportForURL("none")
	}
}

// CheckCredits probes all upstream keys for 墨点 (ink points) credit status.
// It is called by the background loop and by the management API POST endpoint.
func (s *Server) CheckCredits() {
	if s.credits == nil || s.pool == nil {
		return
	}
	rawInfo := s.pool.RawKeyInfo()
	if len(rawInfo) == 0 {
		return
	}
	keys := make([]credits.KeyInfo, 0, len(rawInfo))
	for _, ri := range rawInfo {
		keys = append(keys, credits.KeyInfo{ID: ri.ID, Key: ri.Key, ProxyURL: ri.ProxyURL})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.credits.CheckAll(ctx, keys, s.creditsTransportFactory())
	slog.Info("credits probe completed", "keys", len(keys))
}

// StartCreditsLoop runs a background goroutine that probes all keys every
// checkEvery. It stops when ctx is done.
func (s *Server) StartCreditsLoop(ctx context.Context, checkEvery time.Duration) {
	if s.credits == nil || checkEvery <= 0 {
		return
	}
	go func() {
		// Run once shortly after startup so the panel has data immediately.
		s.CheckCredits()
		t := time.NewTicker(checkEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.CheckCredits()
			}
		}
	}()
}

// CreditsProbe returns the credits probe for direct access (e.g. management API).
func (s *Server) CreditsProbe() *credits.Probe { return s.credits }
