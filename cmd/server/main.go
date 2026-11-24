// Command discovery2api runs the Intern Discovery API gateway.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ZFXing-lite/discovery2api/internal/config"
	"github.com/ZFXing-lite/discovery2api/internal/keypool"
	"github.com/ZFXing-lite/discovery2api/internal/metrics"
	"github.com/ZFXing-lite/discovery2api/internal/proxypool"
	"github.com/ZFXing-lite/discovery2api/internal/relay"
	"github.com/ZFXing-lite/discovery2api/internal/server"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "c", "config.yaml", "config file path")
	flag.Parse()
	if v := strings.TrimSpace(os.Getenv("DISCOVERY2API_CONFIG")); v != "" {
		cfgPath = v
	}

	if err := ensureConfig(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(2)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(2)
	}
	setupLogger(cfg)

	if len(cfg.Upstream.Keys) == 0 {
		slog.Info("no upstream keys yet; open the panel and add them, /healthz stays 503 until then")
	}
	slog.Info("discovery2api starting",
		"listen", cfg.ListenAddr(), "base-url", cfg.Upstream.BaseURL,
		"keys", len(cfg.Upstream.Keys), "proxies", countProxies(cfg),
		"model", cfg.Upstream.DefaultModel,
		"auth-required", cfg.AuthRequired())
	warnPlaceholders(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Upstream key pool.
	pool := keypool.New(toPoolKeys(cfg), toSettings(cfg), config.ResolvePath(cfg.Metrics.StateFile, "."))
	if err := pool.Restore(); err != nil {
		slog.Warn("restore pool state failed", "err", err)
	}

	// SOCKS5 proxy pool.
	proxies := proxypool.New(toProxyEntries(cfg), cfg.Proxy.Policy, time.Duration(cfg.Proxy.FailCooldown))
	var proxyMu sync.Mutex

	// Usage metrics.
	rec := metrics.New(config.ResolvePath(cfg.Metrics.StateFile, ".") + ".usage")
	if cfg.Metrics.Enabled {
		if err := rec.Restore(); err != nil {
			slog.Warn("restore usage failed", "err", err)
		}
	}

	// Usage metrics. Recording is gated by an atomic flag that follows config
	// reloads; reading the cfg variable itself from the request goroutine would
	// race with the watcher reassigning it.
	var metricsEnabled atomic.Bool
	metricsEnabled.Store(cfg.Metrics.Enabled)

	relayer := relay.New(pool, proxies, relay.Config{
		BaseURL:                cfg.Upstream.BaseURL,
		DefaultModel:           cfg.Upstream.DefaultModel,
		ForceModel:             cfg.Upstream.ForceModel,
		Timeout:                time.Duration(cfg.Upstream.Timeout),
		ConnectTimeout:         time.Duration(cfg.Upstream.ConnectTimeout),
		MaxRetries:             cfg.RateLimit.MaxRetries,
		RateLimitRetries:       cfg.RateLimit.RateLimitRetries,
		Transient400Retries:    cfg.RateLimit.Transient400Retries,
		Transient400MinElapsed: time.Duration(cfg.RateLimit.Transient400MinElapsed),
		Keepalive:              20 * time.Second,
	}, func(keyID string, kind relay.Kind, model string, prompt, completion int) {
		if metricsEnabled.Load() {
			rec.Record(keyID, model, prompt, completion)
		}
	})

	srv := server.New(cfg, relayer, pool, proxies, rec, cfgPath)
	// One-time upgrade repair: builds before this one attributed transport
	// (proxy) failures to keys, leaving hundreds of perfectly good keys in a
	// "cooling" state for days. Their recorded reason is the giveaway, so
	// clear exactly those and leave rate-limit/5xx cooldowns alone.
	if n := pool.ReviveStaleLinkRests("consecutive failures"); n > 0 {
		slog.Info("startup: cleared cooldowns wrongly attributed to keys", "keys", n)
	}
	// Panel edits and file reloads both rebuild the live proxy pool. The
	// relayer holds the pool pointer, so we swap the entries in place.
	srv.SetProxyRebuilder(func(newCfg *config.Config) {
		proxyMu.Lock()
		defer proxyMu.Unlock()
		next := proxypool.New(toProxyEntries(newCfg), newCfg.Proxy.Policy, time.Duration(newCfg.Proxy.FailCooldown))
		proxies.Replace(next)
		slog.Info("proxy pool rebuilt", "proxies", countProxies(newCfg), "policy", newCfg.Proxy.Policy)
	})

	// Hot reload: settings, relayer config, routes and the key list all follow
	// config changes (the management panel writes config.yaml too).
	watcher := config.NewWatcher(cfgPath, func(newCfg *config.Config) {
		pool.SetSettings(toSettings(newCfg))
		// Config file is authoritative; reconcile the pool under the same lock
		// the management API uses, so a reload can't race a live mutation.
		srv.ApplyReload(newCfg, pool)
		proxyMu.Lock()
		reloaded := proxypool.New(toProxyEntries(newCfg), newCfg.Proxy.Policy, time.Duration(newCfg.Proxy.FailCooldown))
		proxies.Replace(reloaded)
		proxyMu.Unlock()
		relayer.Update(relay.Config{
			BaseURL:                newCfg.Upstream.BaseURL,
			DefaultModel:           newCfg.Upstream.DefaultModel,
			ForceModel:             newCfg.Upstream.ForceModel,
			Timeout:                time.Duration(newCfg.Upstream.Timeout),
			ConnectTimeout:         time.Duration(newCfg.Upstream.ConnectTimeout),
			MaxRetries:             newCfg.RateLimit.MaxRetries,
			RateLimitRetries:       newCfg.RateLimit.RateLimitRetries,
			Transient400Retries:    newCfg.RateLimit.Transient400Retries,
			Transient400MinElapsed: time.Duration(newCfg.RateLimit.Transient400MinElapsed),
			Keepalive:              20 * time.Second,
		})
		srv.Update(newCfg)
		metricsEnabled.Store(newCfg.Metrics.Enabled)
		cfg = newCfg
	})

	var handler atomic.Value
	handler.Store(srv.Handler())
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr(),
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.Load().(http.Handler).ServeHTTP(w, r) }),
		ReadHeaderTimeout: 30 * time.Second,
	}

	watcher.SetSkipReload(srv.OwnWrite)
	go pool.FlushLoop(ctx, time.Duration(cfg.Metrics.FlushEvery))
	go watcher.Run(ctx, cfg)
	go proxyHealth(ctx, proxies, cfg)
	// Model cache: warm every model-cache-every (default 15m). The probe loop
	// also feeds it for free, so this loop only catches list changes the
	// sampled keys missed. A 60s cadence was pure upstream load: the list
	// changes at most a few times a day.
	cacheEvery := time.Duration(cfg.Upstream.Health.ModelCacheEvery)
	if cacheEvery <= 0 {
		cacheEvery = 15 * time.Minute
	}
	srv.StartModelCache(cacheEvery)
	go upstreamHealth(ctx, pool, proxies, cfg, srv.ModelCache())
	// Credits (墨点) probe: disabled — all keys share one account, probing 800
	// keys every 5 minutes wastes upstream RPM quota for no actionable signal.
	// srv.StartCreditsLoop(ctx, time.Duration(cfg.Credits.CheckEvery))
	if cfg.Metrics.Enabled {
		go rec.FlushLoop(ctx, time.Duration(cfg.Metrics.FlushEvery))
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.ListenAddr())
		if cfg.TLS.Enable {
			serveErr <- httpSrv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
		} else {
			serveErr <- httpSrv.ListenAndServe()
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-serveErr:
		slog.Error("server failed", "err", err)
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
	slog.Info("discovery2api stopped")
}

func setupLogger(cfg *config.Config) {
	var level slog.Level
	switch strings.ToLower(cfg.Log.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	var h slog.Handler
	if strings.ToLower(cfg.Log.Format) == "json" {
		h = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		h = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	slog.SetDefault(slog.New(h))
}

// ensureConfig writes the bundled example the first time the configured path
// is missing, so `docker compose up` does not fail on a volume that points at
// nothing. An existing file is never overwritten.
func ensureConfig(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	candidates := []string{"/app/config.example.yaml", "config.example.yaml"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "config.example.yaml"))
	}
	for _, candidate := range candidates {
		raw, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
			return err
		}
		return os.WriteFile(path, raw, 0o600)
	}
	return nil
}

func warnPlaceholders(cfg *config.Config) {
	for _, k := range cfg.Upstream.Keys {
		if isPlaceholder(k.Key) {
			slog.Warn("upstream key looks like a placeholder; clients will get 401 until a real API key is set",
				"key", config.MaskKey(k.Key))
		}
	}
}

func isPlaceholder(k string) bool {
	lk := strings.ToLower(k)
	for _, p := range []string{"xxx", "your", "placeholder", "example", "change_me"} {
		if lk == p {
			return true
		}
	}
	for _, p := range []string{"xxx", "your", "example", "placeholder", "change_me"} {
		if strings.Contains(lk, p) {
			return true
		}
	}
	return false
}

func proxyHealth(ctx context.Context, p *proxypool.Pool, cfg *config.Config) {
	if p == nil || p.Empty() {
		return
	}
	every := time.Duration(cfg.Proxy.HealthEvery)
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.HealthCheck(ctx, "discovery-api.intern-ai.org.cn:443")
		}
	}
}

// upstreamHealth is the background outage detector. Every probe round it
// samples several keys (not one) with a lightweight GET /v1/models and a
// short timeout. A single failed round does nothing drastic: the circuit
// only trips after fail-threshold consecutive fully-failed rounds, and the
// probe interval backs off exponentially while upstream stays dark, so a
// flaky upstream is neither hammered nor allowed to blank the pool on one
// blip. 429/401 on the probe are treated as key-level signals (the upstream
// is answering; the sampled key or quota is the problem) and never cool the
// whole pool. On a 2xx probe the response body feeds the model cache.
func upstreamHealth(ctx context.Context, pool *keypool.Pool, proxies *proxypool.Pool, cfg *config.Config, mc *server.ModelCache) {
	base := strings.TrimRight(cfg.Upstream.BaseURL, "/")
	if base == "" {
		return
	}
	probeURL := base + "/v1/models"
	hc := cfg.Upstream.Health
	every := time.Duration(hc.ProbeEvery)
	if every <= 0 {
		every = 30 * time.Second
	}
	coolDur := time.Duration(hc.CoolDuration)
	if coolDur <= 0 {
		coolDur = 30 * time.Second
	}
	sample := hc.SampleSize
	if sample <= 0 {
		sample = 3
	}
	threshold := hc.FailThreshold
	if threshold <= 0 {
		threshold = 3
	}
	maxBackoff := time.Duration(hc.MaxBackoff)
	if maxBackoff <= 0 {
		maxBackoff = 10 * time.Minute
	}
	// Use the SOCKS5 proxy pool for probe requests when configured, so the
	// probe can actually reach the upstream through the egress proxy.
	var probeTransport http.RoundTripper
	if proxies != nil && !proxies.Empty() {
		probeTransport = proxies.Transport("health-probe", nil)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	if probeTransport != nil {
		client.Transport = probeTransport
	}
	fails := 0
	interval := every
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		keys := pool.SampleKeys(sample)
		var okBody []byte
		var okKeys = make([]string, 0, len(keys))
		// A round is failed UNLESS the upstream proves alive through at
		// least one sampled key (any 2xx/429/401/403 answer counts — those
		// all come from a living upstream). With no eligible keys the
		// round is failed as well. Transport errors and 5xx on every key
		// mean the upstream is dark: that is what trips the breaker.
		roundFailed := true
		if len(keys) == 0 {
			// No eligible keys to probe. The pool is empty or fully
			// cooled; nothing to do this round and no signal either way —
			// do not count it as a failure (it would double-cool a pool
			// already in cooldown) nor as a success (which would clear
			// cooldowns without proof).
			continue
		}
		for _, key := range keys {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
			if err != nil {
				continue // request-build bug: keep the pessimistic default
			}
			req.Header.Set("Authorization", "Bearer "+key)
			resp, err := client.Do(req)
			if err != nil {
				slog.Debug("upstream probe: key failed", "err", err)
				continue // transport error: upstream may still be alive via another key
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			switch {
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				// This key answered: it is proven good right now, so any
				// earlier link-failure punishment on it is stale.
				okKeys = append(okKeys, key)
				if okBody == nil && len(body) > 0 {
					okBody = body
				}
				roundFailed = false
			case resp.StatusCode == 429, resp.StatusCode == 401, resp.StatusCode == 403:
				// Upstream is alive; this is a key/quota-level signal.
				// Never a reason to cool the whole pool.
				roundFailed = false
			default:
				slog.Debug("upstream probe: key got status", "status", resp.StatusCode)
			}
		}
		if !roundFailed {
			fails = 0
			interval = every
			// Revive only the keys this round actually proved good. A global
			// ClearCooldowns() would also un-cool keys that are legitimately
			// rate-limited or broken, undoing real protection.
			for _, key := range okKeys {
				pool.NoteProbeSuccess(key)
			}
			// A fully-cooled pool (total outage) is unambiguous: if any key
			// answered, nothing is cooling for a good reason. This only fires
			// when every key is out, so it cannot mask per-key state.
			if n := pool.ClearCooldowns(); n > 0 {
				slog.Info("upstream probe recovered; cleared total-outage cooldowns", "url", probeURL, "keys", n)
			}
			if okBody != nil {
				mc.Set(okBody)
			}
			continue
		}
		fails++
		// Even a fully failed round says nothing about individual keys: the
		// transport path may be the only broken thing. Rest only the sampled
		// keys, and only after they fail three rounds in a row.
		if n := pool.ProbeRound(keys, coolDur); n > 0 {
			slog.Warn("upstream probe: keys rested", "keys", n, "consecutive_round_fails", fails)
		}
		if fails < threshold {
			slog.Warn("upstream probe round failed", "fails", fails, "threshold", threshold)
			interval = every * time.Duration(1<<uint(fails)) // gentle backoff between failed rounds
			if interval > 5*time.Minute {
				interval = 5 * time.Minute
			}
			continue
		}
		// Threshold reached: the upstream is dark through every sampled key.
		// Cool the pool once (HealthCheckAll never extends an existing
		// cooldown) and back the probe off so we stop adding load to a
		// dying upstream.
		n := pool.HealthCheckAll(coolDur, "upstream probe: consecutive failures")
		if n > 0 {
			slog.Warn("upstream health probe failed; cooling keys",
				"url", probeURL, "keys", n, "consecutive_fails", fails)
		}
		interval = every * time.Duration(1<<uint(fails-threshold+1))
		if interval > 10*time.Minute {
			interval = 10 * time.Minute
		}
	}
}

func toPoolKeys(cfg *config.Config) []keypool.UpstreamKey {
	out := make([]keypool.UpstreamKey, 0, len(cfg.Upstream.Keys))
	for _, k := range cfg.Upstream.Keys {
		out = append(out, keypool.UpstreamKey{Key: k.Key, Weight: k.Weight, Proxy: k.Proxy})
	}
	return out
}

func toSettings(cfg *config.Config) keypool.Settings {
	return keypool.Settings{
		MinRPMReserve: cfg.RateLimit.MinRPMReserve,
		Cooldown429:   time.Duration(cfg.RateLimit.Cooldown429),
		Cooldown5xx:   time.Duration(cfg.RateLimit.Cooldown5xx),
		ErrThreshold:  cfg.RateLimit.ErrThreshold,
		ErrCooldown:   time.Duration(cfg.RateLimit.ErrCooldown),
		DisableOn401:  cfg.RateLimit.DisableOn401,
		MaxRetries:    cfg.RateLimit.MaxRetries,
		RetryOn:       cfg.RateLimit.RetryOn,
		Backoff:       time.Duration(cfg.RateLimit.Backoff),
	}
}

func toProxyEntries(cfg *config.Config) []struct {
	URL    string
	Weight int
} {
	out := make([]struct {
		URL    string
		Weight int
	}, 0, len(cfg.Proxy.SOCKS5))
	for _, e := range cfg.Proxy.SOCKS5 {
		out = append(out, struct {
			URL    string
			Weight int
		}{URL: e.URL, Weight: e.Weight})
	}
	return out
}

func countProxies(cfg *config.Config) int { return len(cfg.Proxy.SOCKS5) }
