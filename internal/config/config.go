// Package config loads, validates and hot-reloads the discovery2api gateway configuration.
package config

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the full gateway configuration. All durations are strings accepted
// by time.ParseDuration (e.g. "60s", "10m", "1h").
type Config struct {
	Host       string     `yaml:"host"`
	Port       int        `yaml:"port"`
	TLS        TLS        `yaml:"tls"`
	APIKeys    []APIKeyEntry `yaml:"api-keys"`
	Upstream   Upstream   `yaml:"upstream"`
	Account    Account    `yaml:"account"`
	Proxy      Proxy      `yaml:"proxy"`
	RateLimit  RateLimit  `yaml:"rate-limit"`
	Log        Log        `yaml:"log"`
	Metrics    Metrics    `yaml:"metrics"`
	Management Management `yaml:"remote-management"`
}

// Account holds the upstream Intern Discovery account quota info shown in the panel.
// The upstream has no API-key-authenticated usage endpoint; these fields are
// filled manually from the web console (/console/usage) and displayed in the
// gateway panel for monitoring.
type Account struct {
	TokenQuota int64  `yaml:"token-quota" json:"token_quota"`
	TokenUsed  int64  `yaml:"token-used" json:"token_used"`
	Formula    string `yaml:"formula" json:"formula"`
}

type TLS struct {
	Enable bool   `yaml:"enable"`
	Cert   string `yaml:"cert"`
	Key    string `yaml:"key"`
}

// UpstreamKey is one Intern Discovery API key (xxx). Weight is used by the weighted
// round-robin picker; <=0 means 1.
type UpstreamKey struct {
	Key    string `yaml:"key"`
	Weight int    `yaml:"weight"`
	Proxy  string `yaml:"proxy"` // optional per-key proxy override (URL or "none")
}

// APIKeyEntry is one downstream key clients use to call this gateway.
type APIKeyEntry struct {
	Key  string `yaml:"key" json:"key"`
	Name string `yaml:"name" json:"name"`
}

// Upstream describes the single Intern Discovery API endpoint. The base URL is shared by
// all three interfaces per the API documentation.
type Upstream struct {
	BaseURL        string        `yaml:"base-url"`
	Keys           []UpstreamKey `yaml:"keys"`
	Timeout        Duration      `yaml:"timeout"`
	ConnectTimeout Duration      `yaml:"connect-timeout"`
	DefaultModel   string        `yaml:"default-model"`
	// ForceModel rewrites request body "model" to DefaultModel so clients that
	// send a stale/other model id still work (Intern Discovery only exposes one model).
	ForceModel bool `yaml:"force-model"`
}

type Proxy struct {
	// Policy: round-robin | random | sticky-key
	Policy       string       `yaml:"policy"`
	HealthEvery  Duration     `yaml:"health-every"`
	FailCooldown Duration     `yaml:"fail-cooldown"`
	SOCKS5       []ProxyEntry `yaml:"socks5"`
}

type ProxyEntry struct {
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

type RateLimit struct {
	// RespectHeader parses upstream x-rpm-limit / x-rpm-remaining / Retry-After.
	RespectHeader bool `yaml:"respect-header"`
	// MinRPMReserve skips a key when its observed x-rpm-remaining is below this.
	MinRPMReserve int `yaml:"min-rpm-reserve"`
	// Cooldown429 is used when the upstream gives no Retry-After.
	Cooldown429 Duration `yaml:"cooldown-429"`
	// Cooldown5xx applies to 5xx responses.
	Cooldown5xx Duration `yaml:"cooldown-5xx"`
	// ErrThreshold consecutive errors before a key is cooled down.
	ErrThreshold int `yaml:"err-threshold"`
	// ErrCooldown duration of the consecutive-error cooldown.
	ErrCooldown Duration `yaml:"err-cooldown"`
	// DisableOn401 permanently disables a key whose key is invalid/revoked.
	DisableOn401 bool `yaml:"disable-on-401"`
	// MaxRetries is the number of cross-key retries per request.
	MaxRetries int `yaml:"max-retries"`
	// RetryOn lists upstream HTTP status codes that trigger a retry.
	RetryOn []int `yaml:"retry-on"`
	// Backoff is the base sleep between retries (exponential: backoff*2^attempt).
	Backoff Duration `yaml:"backoff"`
}

type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

type Metrics struct {
	Enabled    bool     `yaml:"enabled"`
	StateFile  string   `yaml:"state-file"`
	FlushEvery Duration `yaml:"flush-every"`
}

type Management struct {
	AllowRemote bool   `yaml:"allow-remote"`
	SecretKey   string `yaml:"secret-key"`
}

// Duration is a time.Duration that (un)marshals from/to a duration string.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	return d.Set(s)
}

// MarshalYAML emits the duration as a human string ("1m", "10s") so a saved
// config round-trips through UnmarshalYAML.
func (d Duration) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

func (d Duration) String() string { return time.Duration(d).String() }

// Set parses a duration string; used by env overrides too.
func (d *Duration) Set(s string) error {
	if s == "" {
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

const (
	DefaultBaseURL    = "https://discovery-api.intern-ai.org.cn"
	DefaultModel      = "auto"
	DefaultListenPort = 8319
)

// Default returns a usable zero-config configuration.
func Default() *Config {
	return &Config{
		Host: "",
		Port: DefaultListenPort,
		Upstream: Upstream{
			BaseURL:        DefaultBaseURL,
			DefaultModel:   DefaultModel,
			ForceModel:     false,
			Timeout:        Duration(10 * time.Minute),
			ConnectTimeout: Duration(15 * time.Second),
		},
		Proxy: Proxy{
			Policy:       "round-robin",
			HealthEvery:  Duration(30 * time.Second),
			FailCooldown: Duration(30 * time.Second),
		},
		RateLimit: RateLimit{
			RespectHeader: true,
			MinRPMReserve: 1,
			Cooldown429:   Duration(60 * time.Second),
			Cooldown5xx:   Duration(30 * time.Second),
			ErrThreshold:  3,
			ErrCooldown:   Duration(10 * time.Minute),
			DisableOn401:  true,
			MaxRetries:    3,
			RetryOn:       []int{429, 500, 502, 503, 504},
			Backoff:       Duration(time.Second),
		},
		Log: Log{Level: "info", Format: "text"},
		Metrics: Metrics{
			Enabled:    true,
			FlushEvery: Duration(5 * time.Second),
		},
		// The panel is the setup UI, so a deployed instance must be reachable
		// from a browser. The password is still required; see normalize.
		Management: Management{AllowRemote: true},
	}
}

// Load reads path (YAML). A missing path is fine: defaults are returned and the
// file is not created. DISCOVERY2API_* environment variables override fields.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read config: %w", err)
			}
			slog.Warn("config file not found, using defaults", "path", path)
		} else if len(raw) > 0 {
			dec := yaml.NewDecoder(strings.NewReader(string(raw)))
			dec.KnownFields(true)
			if err := dec.Decode(cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
		}
	}
	if err := applyEnv(cfg); err != nil {
		return nil, err
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) normalize() error {
	if c.Port <= 0 {
		c.Port = DefaultListenPort
	}
	c.Upstream.BaseURL = strings.TrimRight(strings.TrimSpace(c.Upstream.BaseURL), "/")
	if c.Upstream.BaseURL == "" {
		c.Upstream.BaseURL = DefaultBaseURL
	}
	if c.Upstream.DefaultModel == "" {
		c.Upstream.DefaultModel = DefaultModel
	}
	if c.Upstream.Timeout <= 0 {
		c.Upstream.Timeout = Duration(10 * time.Minute)
	}
	if c.Upstream.ConnectTimeout <= 0 {
		c.Upstream.ConnectTimeout = Duration(15 * time.Second)
	}
	// An empty key list is allowed so the process can boot and the panel can
	// add the first key. /healthz stays 503 until one exists.
	seen := map[string]bool{}
	for i := range c.Upstream.Keys {
		k := strings.TrimSpace(c.Upstream.Keys[i].Key)
		c.Upstream.Keys[i].Key = k
		if k == "" {
			return fmt.Errorf("upstream.keys[%d].key is empty", i)
		}

		if seen[k] {
			return fmt.Errorf("upstream.keys[%d].key is duplicated: %s", i, mask(k))
		}
		seen[k] = true
		if c.Upstream.Keys[i].Weight <= 0 {
			c.Upstream.Keys[i].Weight = 1
		}
	}
	switch strings.ToLower(c.Proxy.Policy) {
	case "", "round-robin", "random", "sticky-key":
	default:
		return fmt.Errorf("proxy.policy %q is invalid (use round-robin|random|sticky-key)", c.Proxy.Policy)
	}
	if c.RateLimit.MaxRetries < 0 {
		c.RateLimit.MaxRetries = 0
	}
	if c.RateLimit.Cooldown429 <= 0 {
		c.RateLimit.Cooldown429 = Duration(time.Minute)
	}
	if c.RateLimit.Cooldown5xx <= 0 {
		c.RateLimit.Cooldown5xx = Duration(30 * time.Second)
	}
	if c.RateLimit.ErrThreshold <= 0 {
		c.RateLimit.ErrThreshold = 3
	}
	if c.RateLimit.ErrCooldown <= 0 {
		c.RateLimit.ErrCooldown = Duration(10 * time.Minute)
	}
	if c.RateLimit.Backoff < 0 {
		c.RateLimit.Backoff = 0
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
	if c.Metrics.Enabled && c.Metrics.StateFile == "" {
		c.Metrics.StateFile = "state.json"
	}
	if c.Metrics.FlushEvery <= 0 {
		c.Metrics.FlushEvery = Duration(5 * time.Second)
	}
	if strings.TrimSpace(c.Management.SecretKey) == "" {
		return fmt.Errorf("panel password is required: set DISCOVERY2API_MGMT_KEY or remote-management.secret-key")
	}
	return nil
}

// ListenAddr returns host:port.
func (c *Config) ListenAddr() string { return fmt.Sprintf("%s:%d", c.Host, c.Port) }

// UpstreamKeysFlat returns the key strings in config order.
func (c *Config) UpstreamKeysFlat() []string {
	out := make([]string, 0, len(c.Upstream.Keys))
	for _, k := range c.Upstream.Keys {
		out = append(out, k.Key)
	}
	return out
}

// AuthRequired reports whether downstream api-keys gate access.
func (c *Config) AuthRequired() bool {
	for _, k := range c.APIKeys {
		if strings.TrimSpace(k.Key) != "" {
			return true
		}
	}
	return false
}

// IsAuthorized checks a downstream credential against configured api-keys.
// Constant-time-ish compare; empty config = open gateway.
func (c *Config) IsAuthorized(token string) bool {
	if !c.AuthRequired() {
		return true
	}
	for _, k := range c.APIKeys {
		if subtleEqual(strings.TrimSpace(k.Key), token) {
			return true
		}
	}
	return false
}

func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// --- env overrides -------------------------------------------------------

func applyEnv(c *Config) error {
	get := func(name string) string { return os.Getenv(name) }
	if v := strings.TrimSpace(get("DISCOVERY2API_LISTEN")); v != "" {
		c.Host = v
	}
	if v := strings.TrimSpace(get("DISCOVERY2API_PORT")); v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err != nil {
			return fmt.Errorf("DISCOVERY2API_PORT: %w", err)
		}
		c.Port = p
	}
	if v := strings.TrimSpace(get("DISCOVERY2API_BASE_URL")); v != "" {
		c.Upstream.BaseURL = v
	}
	if v := strings.TrimSpace(get("DISCOVERY2API_MODEL")); v != "" {
		c.Upstream.DefaultModel = v
	}
	// Comma/space separated keys; also accepts the raw key list.
	if v := strings.TrimSpace(get("DISCOVERY2API_KEYS")); v != "" {
		for _, k := range splitList(v) {
			c.Upstream.Keys = append(c.Upstream.Keys, UpstreamKey{Key: k})
		}
	}
	if v := strings.TrimSpace(get("DISCOVERY2API_API_KEYS")); v != "" {
		for _, k := range splitList(v) {
			c.APIKeys = append(c.APIKeys, APIKeyEntry{Key: k})
		}
	}
	if v := strings.TrimSpace(get("DISCOVERY2API_PROXIES")); v != "" {
		for _, u := range splitList(v) {
			c.Proxy.SOCKS5 = append(c.Proxy.SOCKS5, ProxyEntry{URL: u})
		}
	}
	if v := strings.TrimSpace(get("DISCOVERY2API_LOG_LEVEL")); v != "" {
		c.Log.Level = v
	}
	if v := strings.TrimSpace(get("DISCOVERY2API_MGMT_KEY")); v != "" {
		c.Management.SecretKey = v
	}
	// DISCOVERY2API_ALLOW_REMOTE opens the management panel to the public
	// internet. It only takes effect together with DISCOVERY2API_MGMT_KEY (or a
	// secret-key in config): a panel with no password is never remote-enabled.
	if v := strings.TrimSpace(get("DISCOVERY2API_ALLOW_REMOTE")); v != "" {
		c.Management.AllowRemote = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	return nil
}

func splitList(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// --- runtime mutation (management API) -----------------------------------

// UpstreamKeyID returns the stable id used by the pool for a raw key value.
func UpstreamKeyID(key string) string {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return strconv.FormatUint(h, 16)
}

// AddUpstreamKey adds an Intern Discovery key (deduped); returns its id.
func (c *Config) AddUpstreamKey(key string, weight int, proxy string) string {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return ""
	}
	if weight <= 0 {
		weight = 1
	}
	for i := range c.Upstream.Keys {
		if strings.TrimSpace(c.Upstream.Keys[i].Key) == key {
			c.Upstream.Keys[i].Weight = weight
			c.Upstream.Keys[i].Proxy = strings.TrimSpace(proxy)
			return UpstreamKeyID(key)
		}
	}
	c.Upstream.Keys = append(c.Upstream.Keys, UpstreamKey{
		Key: key, Weight: weight, Proxy: strings.TrimSpace(proxy),
	})
	return UpstreamKeyID(key)
}

// RemoveUpstreamKey removes a key by its id (as returned by UpstreamKeyID).
func (c *Config) RemoveUpstreamKey(id string) bool {
	for i, k := range c.Upstream.Keys {
		if UpstreamKeyID(k.Key) == id {
			c.Upstream.Keys = append(c.Upstream.Keys[:i], c.Upstream.Keys[i+1:]...)
			return true
		}
	}
	return false
}

// AddAPIKey adds a downstream (client) key with a name, deduped by key.
func (c *Config) AddAPIKey(key, name string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for _, k := range c.APIKeys {
		if strings.TrimSpace(k.Key) == key {
			return false
		}
	}
	c.APIKeys = append(c.APIKeys, APIKeyEntry{Key: key, Name: strings.TrimSpace(name)})
	return true
}

// RemoveAPIKey removes a downstream key by its id.
func (c *Config) RemoveAPIKey(id string) bool {
	for i, k := range c.APIKeys {
		if UpstreamKeyID(strings.TrimSpace(k.Key)) == id {
			c.APIKeys = append(c.APIKeys[:i], c.APIKeys[i+1:]...)
			return true
		}
	}
	return false
}

// APIKeysMasked returns downstream keys with their ids, names and masked values.
func (c *Config) APIKeysMasked() []map[string]string {
	out := make([]map[string]string, 0, len(c.APIKeys))
	for _, k := range c.APIKeys {
		key := strings.TrimSpace(k.Key)
		if key == "" {
			continue
		}
		out = append(out, map[string]string{"id": UpstreamKeyID(key), "key": mask(key), "name": k.Name})
	}
	return out
}

// Save writes the config back to path atomically (tmp + rename, 0600).
// Comments in the original file are not preserved: values are.
func (c *Config) Save(path string) error {
	if path == "" {
		return fmt.Errorf("no config path")
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err == nil {
		return nil
	}
	// Docker bind-mounts a single file, and rename into that mount point fails
	// with EBUSY. Write the validated temp content in place instead.
	defer os.Remove(tmp)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return nil
}

// --- hot reload ----------------------------------------------------------

// Watcher reloads the config file on SIGHUP and on file change, calling cb with
// the new configuration. Errors are logged, never fatal.
type Watcher struct {
	path string
	mu   sync.Mutex
	cur  *Config
	cb   func(*Config)
	stop chan struct{}
	// skip reports a modtime this process just wrote, so the watcher does not
	// reload its own save and race the live config.
	skip func(time.Time) bool
}

func NewWatcher(path string, cb func(*Config)) *Watcher {
	return &Watcher{path: path, cb: cb, stop: make(chan struct{})}
}

// SetSkipReload ignores file changes whose modtime skip reports as our own
// write. External edits still reload.
func (w *Watcher) SetSkipReload(skip func(time.Time) bool) { w.skip = skip }

func (w *Watcher) Current() *Config {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cur
}

// Run blocks until ctx is done.
func (w *Watcher) Run(ctx context.Context, initial *Config) {
	w.mu.Lock()
	w.cur = initial
	w.mu.Unlock()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	mod := fileMod(w.path)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-ticker.C:
			if m := fileMod(w.path); !m.IsZero() && mod != m {
				mod = m
				if w.skip != nil && w.skip(m) {
					slog.Debug("config change is our own write, skip reload", "path", w.path)
					continue
				}
				w.reload()
			}
		}
	}
}

func (w *Watcher) reload() {
	cfg, err := Load(w.path)
	if err != nil {
		slog.Error("config reload failed", "err", err)
		return
	}
	w.mu.Lock()
	w.cur = cfg
	w.mu.Unlock()
	slog.Info("config reloaded", "path", w.path)
	if w.cb != nil {
		w.cb(cfg)
	}
}

func fileMod(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// Mask hides all but the first 4 and last 2 chars of a secret, for logs.
func mask(s string) string {
	if len(s) <= 6 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-6) + s[len(s)-2:]
}

// MaskKey is the exported masking helper used across packages.
func MaskKey(s string) string { return mask(s) }

// SnapshotJSON serializes the non-secret parts of the config for the management
// API. The management secret, TLS key path and proxy credentials are masked or
// dropped: this response would otherwise hand those secrets to anyone holding
// the management key.
func (c *Config) SnapshotJSON() ([]byte, error) {
	type outKey struct {
		Key    string `json:"key"`
		Weight int    `json:"weight"`
		Proxy  string `json:"proxy,omitempty"`
	}
	keys := make([]outKey, 0, len(c.Upstream.Keys))
	for _, k := range c.Upstream.Keys {
		keys = append(keys, outKey{Key: mask(k.Key), Weight: k.Weight, Proxy: k.Proxy})
	}
	proxies := make([]string, 0, len(c.Proxy.SOCKS5))
	for _, p := range c.Proxy.SOCKS5 {
		proxies = append(proxies, maskProxyURL(p.URL))
	}
	return json.MarshalIndent(&struct {
		Host         string      `json:"host"`
		Port         int         `json:"port"`
		TLS          tlsOut      `json:"tls"`
		Upstream     upstreamOut `json:"upstream"`
		Account      Account     `json:"account"`
		Proxy        proxyOut    `json:"proxy"`
		RateLimit    rateOut     `json:"rate-limit"`
		Log          logOut      `json:"log"`
		Metrics      metricsOut  `json:"metrics"`
		Management   mgmtOut     `json:"remote-management"`
		KeysMasked   []outKey    `json:"upstream-keys-masked"`
		APIKeys      []APIKeyMasked `json:"api-keys-masked"`
		AuthRequired bool        `json:"auth-required"`
	}{
		Host: c.Host, Port: c.Port,
		TLS: tlsOut{Enable: c.TLS.Enable, Cert: c.TLS.Cert, Key: maskPathKey(c.TLS.Key)},
		Upstream: upstreamOut{
			BaseURL: c.Upstream.BaseURL, DefaultModel: c.Upstream.DefaultModel,
			ForceModel: c.Upstream.ForceModel, Timeout: c.Upstream.Timeout.String(),
			ConnectTimeout: c.Upstream.ConnectTimeout.String(), Keys: len(c.Upstream.Keys),
		},
		Account: c.Account,
		Proxy: proxyOut{Policy: c.Proxy.Policy, HealthEvery: c.Proxy.HealthEvery.String(),
			FailCooldown: c.Proxy.FailCooldown.String(), SOCKS5: proxies},
		RateLimit: rateOut{
			RespectHeader: c.RateLimit.RespectHeader, MinRPMReserve: c.RateLimit.MinRPMReserve,
			Cooldown429: c.RateLimit.Cooldown429.String(), Cooldown5xx: c.RateLimit.Cooldown5xx.String(),
			ErrThreshold: c.RateLimit.ErrThreshold, ErrCooldown: c.RateLimit.ErrCooldown.String(),
			DisableOn401: c.RateLimit.DisableOn401, MaxRetries: c.RateLimit.MaxRetries,
			RetryOn: c.RateLimit.RetryOn, Backoff: c.RateLimit.Backoff.String(),
		},
		Log: logOut{Level: c.Log.Level, Format: c.Log.Format},
		Metrics: metricsOut{Enabled: c.Metrics.Enabled, StateFile: c.Metrics.StateFile,
			FlushEvery: c.Metrics.FlushEvery.String()},
		Management: mgmtOut{AllowRemote: c.Management.AllowRemote,
			SecretKeySet: strings.TrimSpace(c.Management.SecretKey) != ""},
		KeysMasked:   keys,
		APIKeys:      c.APIKeysMaskedPlain(),
		AuthRequired: c.AuthRequired(),
	}, "", "  ")
}

type tlsOut struct {
	Enable bool   `json:"enable"`
	Cert   string `json:"cert"`
	Key    string `json:"key"`
}
type upstreamOut struct {
	BaseURL        string `json:"base-url"`
	DefaultModel   string `json:"default-model"`
	ForceModel     bool   `json:"force-model"`
	Timeout        string `json:"timeout"`
	ConnectTimeout string `json:"connect-timeout"`
	Keys           int    `json:"keys"`
}
type proxyOut struct {
	Policy       string   `json:"policy"`
	HealthEvery  string   `json:"health-every"`
	FailCooldown string   `json:"fail-cooldown"`
	SOCKS5       []string `json:"socks5"`
}
type rateOut struct {
	RespectHeader bool   `json:"respect-header"`
	MinRPMReserve int    `json:"min-rpm-reserve"`
	Cooldown429   string `json:"cooldown-429"`
	Cooldown5xx   string `json:"cooldown-5xx"`
	ErrThreshold  int    `json:"err-threshold"`
	ErrCooldown   string `json:"err-cooldown"`
	DisableOn401  bool   `json:"disable-on-401"`
	MaxRetries    int    `json:"max-retries"`
	RetryOn       []int  `json:"retry-on"`
	Backoff       string `json:"backoff"`
}
type logOut struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}
type metricsOut struct {
	Enabled    bool   `json:"enabled"`
	StateFile  string `json:"state-file"`
	FlushEvery string `json:"flush-every"`
}
type mgmtOut struct {
	AllowRemote  bool `json:"allow-remote"`
	SecretKeySet bool `json:"secret-key-set"`
}

// APIKeyMasked is one masked downstream key for the snapshot JSON.
type APIKeyMasked struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// APIKeysMaskedPlain returns masked downstream keys with id and name.
func (c *Config) APIKeysMaskedPlain() []APIKeyMasked {
	out := make([]APIKeyMasked, 0, len(c.APIKeys))
	for _, k := range c.APIKeys {
		key := strings.TrimSpace(k.Key)
		if key != "" {
			out = append(out, APIKeyMasked{ID: UpstreamKeyID(key), Key: mask(key), Name: k.Name})
		}
	}
	return out
}

// maskPathKey hides a private-key path's filename while keeping it recognizable.
func maskPathKey(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i+1] + mask(filepath.Base(p))
	}
	return mask(p)
}

// maskProxyURL redacts userinfo from a proxy URL without URL-encoding the
// placeholder (url.String() escapes userinfo, which makes the mask unreadable).
func maskProxyURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.User == nil {
		return u
	}
	parsed.User = nil
	return strings.Replace(parsed.String(), "://", "://***@", 1)
}

// ResolvePath expands ~ and makes a relative path absolute against baseDir.
func ResolvePath(p, baseDir string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}
