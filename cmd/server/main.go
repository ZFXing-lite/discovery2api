// Command discovery2api runs the Intern Discovery API gateway.
package main

import (
	"context"
	"flag"
	"fmt"
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
		BaseURL:        cfg.Upstream.BaseURL,
		DefaultModel:   cfg.Upstream.DefaultModel,
		ForceModel:     cfg.Upstream.ForceModel,
		Timeout:        time.Duration(cfg.Upstream.Timeout),
		ConnectTimeout: time.Duration(cfg.Upstream.ConnectTimeout),
		MaxRetries:     cfg.RateLimit.MaxRetries,
		Keepalive:      20 * time.Second,
	}, func(keyID string, kind relay.Kind, model string, prompt, completion int) {
		if metricsEnabled.Load() {
			rec.Record(keyID, model, prompt, completion)
		}
	})

	srv := server.New(cfg, relayer, pool, proxies, rec, cfgPath)
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
			BaseURL:        newCfg.Upstream.BaseURL,
			DefaultModel:   newCfg.Upstream.DefaultModel,
			ForceModel:     newCfg.Upstream.ForceModel,
			Timeout:        time.Duration(newCfg.Upstream.Timeout),
			ConnectTimeout: time.Duration(newCfg.Upstream.ConnectTimeout),
			MaxRetries:     newCfg.RateLimit.MaxRetries,
			Keepalive:      20 * time.Second,
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
	go upstreamHealth(ctx, pool, cfg)
	// Credits (墨点) probe: checks all keys every 5 minutes by default.
	srv.StartCreditsLoop(ctx, time.Duration(cfg.Credits.CheckEvery))
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

// upstreamHealth pings the upstream API every 30s. On failure it cools every
// non-disabled key for 30s so the pool stops sending traffic until the next
// probe succeeds. This catches 502/503 outages faster than waiting for client
// requests to hit them one key at a time.
func upstreamHealth(ctx context.Context, pool *keypool.Pool, cfg *config.Config) {
	base := strings.TrimRight(cfg.Upstream.BaseURL, "/")
	if base == "" {
		return
	}
	probeURL := base + "/v1/models"
	every := 30 * time.Second
	coolDur := 30 * time.Second
	t := time.NewTicker(every)
	defer t.Stop()
	client := &http.Client{Timeout: 10 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Pick a key so the probe authenticates and the upstream
			// actually serves a response instead of a 401 that tells
			// us nothing about model availability.
			lease, err := pool.Pick(nil)
			authHeader := ""
			if err == nil {
				authHeader = lease.Entry.Key
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
			if err != nil {
				if authHeader != "" {
					lease.Release()
				}
				continue
			}
			if authHeader != "" {
				req.Header.Set("Authorization", "Bearer "+authHeader)
			}
			resp, err := client.Do(req)
			if authHeader != "" {
				lease.Release()
			}
			if err != nil {
				n := pool.HealthCheckAll(coolDur, "upstream probe: "+err.Error())
				if n > 0 {
					slog.Warn("upstream health probe failed", "url", probeURL, "err", err, "keys_cooled", n)
				}
				continue
			}
			resp.Body.Close()
			if resp.StatusCode >= 500 {
				n := pool.HealthCheckAll(coolDur, fmt.Sprintf("upstream probe: HTTP %d", resp.StatusCode))
				if n > 0 {
					slog.Warn("upstream health probe got 5xx", "url", probeURL, "status", resp.StatusCode, "keys_cooled", n)
				}
			} else {
				// Upstream responded healthy. If every key was in cooldown (total
				// outage), clear them so the gateway resumes immediately instead
				// of waiting for each cooldown to expire individually.
				if n := pool.ClearCooldowns(); n > 0 {
					slog.Info("upstream probe recovered; cleared cooldowns", "url", probeURL, "keys", n)
				}
			}
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
