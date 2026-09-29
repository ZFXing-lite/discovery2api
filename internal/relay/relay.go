// Package relay forwards downstream requests to the Intern Discovery API through the key
// pool and optional SOCKS5 proxy pool. All three documented interfaces share
// the same upstream base URL:
//
//	POST {base}/v1/chat/completions   Authorization: Bearer <key>
//	POST {base}/v1/messages           x-api-key: <key>
//	POST {base}/v1/responses          Authorization: Bearer <key>
//
// Retryable failures are retried across keys (round-robin weighted pick) with
// exponential backoff; once a streaming response has started writing to the
// client no rotation is possible anymore, so mid-stream failures degrade into
// an error event rather than a broken connection.
package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ZFXing-lite/discovery2api/internal/keypool"
	"github.com/ZFXing-lite/discovery2api/internal/proxypool"
)

// Kind identifies which Intern Discovery interface a request belongs to.
type Kind string

const (
	KindChat      Kind = "chat"      // /v1/chat/completions
	KindMessages  Kind = "messages"  // /v1/messages
	KindResponses Kind = "responses" // /v1/responses
)

// PathOf maps a downstream path to a Kind ("" when unknown).
func PathOf(path string) Kind {
	switch path {
	case "/v1/chat/completions":
		return KindChat
	case "/v1/messages":
		return KindMessages
	case "/v1/responses":
		return KindResponses
	}
	return ""
}

// UpstreamPath returns the upstream path (identical to the downstream path).
func (k Kind) UpstreamPath() string {
	switch k {
	case KindChat:
		return "/v1/chat/completions"
	case KindMessages:
		return "/v1/messages"
	case KindResponses:
		return "/v1/responses"
	}
	return ""
}

// Config holds the hot-reloadable relayer settings.
type Config struct {
	BaseURL        string
	DefaultModel   string
	ForceModel     bool
	Timeout        time.Duration
	ConnectTimeout time.Duration
	MaxRetries     int
	MaxBodyBytes   int64
	// Keepalive emits an SSE comment this often while waiting for chunks.
	Keepalive time.Duration
}

// UsageHook is called with token usage parsed from a successful non-streaming
// response (best effort; zeros when absent).
type UsageHook func(keyID string, kind Kind, model string, promptTokens, completionTokens int)

// Relayer forwards requests upstream.
type Relayer struct {
	pool    *keypool.Pool
	proxies *proxypool.Pool
	cfg     atomic.Pointer[Config]
	onUsage UsageHook
	log     *slog.Logger
	rtCache sync.Map // cacheKey -> http.RoundTripper (per key+timeout reuse)
}

func New(pool *keypool.Pool, proxies *proxypool.Pool, cfg Config, onUsage UsageHook) *Relayer {
	r := &Relayer{pool: pool, proxies: proxies, onUsage: onUsage,
		log: slog.With("component", "relay")}
	r.cfg.Store(&cfg)
	return r
}

func (r *Relayer) Update(cfg Config) { r.cfg.Store(&cfg) }

// InvalidateKey drops cached transports for the given key ID so subsequent
// requests build fresh connections. Called when a key is disabled or removed
// so stale keep-alive connections to a dead/revoked upstream are not reused.
func (r *Relayer) InvalidateKey(keyID string) {
	prefix := keyID + "|"
	r.rtCache.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), prefix) {
			r.rtCache.Delete(k)
		}
		return true
	})
}

// maxBody protects the gateway; the API itself documents no body-size cap.
const defaultMaxBody = 64 << 20 // 64 MiB

// Handle serves one downstream request of the given kind.
func (r *Relayer) Handle(w http.ResponseWriter, req *http.Request, kind Kind) {
	start := time.Now()
	cfg := *r.cfg.Load()
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBody
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, cfg.MaxBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, map[string]any{
			"error": map[string]any{
				"message": "failed to read request body: " + err.Error(),
				"type":    "discovery2api_error", "code": "read_body_failed",
			}})
		return
	}
	if int64(len(body)) > cfg.MaxBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("request body exceeds %d bytes", cfg.MaxBodyBytes),
				"type":    "discovery2api_error", "code": "body_too_large",
			}})
		return
	}

	streaming := isStream(body)
	if cfg.ForceModel {
		if patched, ok := forceModel(body, cfg.DefaultModel); ok {
			body = patched
		}
	}

	tried := map[string]bool{}
	maxAttempts := cfg.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastStatus int
	var lastUpstreamBody []byte
	var lastErr string
	var poolErr error
	committed := false // true once any bytes were written to the client

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := r.pool.RetryDelay(attempt - 1)
			r.log.Debug("retrying with another key", "attempt", attempt, "delay", delay)
			select {
			case <-req.Context().Done():
				return
			case <-time.After(delay):
			}
		}

		lease, perr := r.pool.Pick(tried)
		if perr != nil {
			// All keys are cooling down. Wait for the soonest recovery instead of
			// immediately failing — but only when we still have retry budget and
			// the client hasn't disconnected.
			var nk *keypool.NoKeysError
			if errors.As(perr, &nk) && !nk.NextAt.IsZero() && attempt+1 < maxAttempts {
				wait := time.Until(nk.NextAt)
				if wait > 0 && wait < 30*time.Second {
					r.log.Debug("all keys cooling, waiting for recovery", "wait", wait, "next_at", nk.NextAt)
					select {
					case <-req.Context().Done():
						return
					case <-time.After(wait):
					}
					continue // retry Pick without consuming an attempt
				}
			}
			poolErr = perr
			break // no key left; report the last upstream error below
		}
		tried[lease.Entry.ID] = true

		status, upstreamBody, streamed, ferr, limits := r.forwardOnce(w, req, kind, cfg, lease, body, streaming)
		lastStatus = status
		if streamed {
			committed = true
		}

		o := keypool.Outcome{
			StatusCode: status,
			Err:        ferr,
			Streamed:   streamed,
			Limits:     limits,
		}
		if upstreamBody != nil {
			lastUpstreamBody = upstreamBody
		}
		if ferr != nil {
			lastErr = ferr.Error()
		} else if status >= 400 && upstreamBody != nil {
			lastErr = truncate(string(upstreamBody), 200)
		}

		r.pool.NoteResult(lease, o)
		lease.Release()

		if ferr == nil && status < 400 {
			// Success. Streaming responses (including a non-SSE body forwarded
			// by pipeStream) were already written to the client; buffered ones
			// still need to be sent here.
			if !streamed && upstreamBody != nil {
				hdr := w.Header()
				hdr.Set("Content-Type", "application/json")
				writeBody(w, status, upstreamBody)
			}
			r.log.Info("served", "kind", kind, "key", lease.Entry.ID,
				"status", status, "stream", streaming, "took", time.Since(start))
			return
		}

		// Decide whether a rotation can help.
		if !r.shouldRotate(status, ferr, streamed) {
			break
		}
	}

	// Exhausted: surface the last upstream response verbatim, but only if the
	// client response is not already committed — appending a JSON error body to
	// a half-written SSE stream would corrupt the response (and superfluous
	// WriteHeader would be logged by net/http).
	if !committed && lastUpstreamBody != nil {
		hdr := w.Header()
		hdr.Set("Content-Type", "application/json")
		writeBody(w, lastStatus, lastUpstreamBody)
		r.log.Warn("request failed after retries", "kind", kind, "status", lastStatus, "err", lastErr)
		return
	}
	if committed {
		// The error was already injected into the stream (or the client went
		// away). Nothing more can be written.
		r.log.Warn("request failed after stream committed", "kind", kind, "status", lastStatus, "err", lastErr)
		return
	}
	code := http.StatusBadGateway
	errCode := "upstream_unavailable"
	if lastStatus == 429 {
		code = http.StatusTooManyRequests
		errCode = "rate_limited"
	} else if lastStatus == 401 {
		code = http.StatusUnauthorized
		errCode = "invalid_api_key"
	} else if lastStatus == 502 {
		code = http.StatusBadGateway
		errCode = "upstream_bad_gateway"
	} else if lastStatus == 503 {
		code = http.StatusServiceUnavailable
		errCode = "upstream_unavailable"
	} else if lastStatus > 0 {
		code = lastStatus
		errCode = fmt.Sprintf("upstream_%d", lastStatus)
	}
	if poolErr != nil {
		// Every key is disabled or cooling down.
		code = http.StatusServiceUnavailable
		errCode = "all_keys_exhausted"
		lastErr = firstNonEmpty(lastErr,
			"all upstream keys are disabled or cooling down; try again later")
	} else if lastStatus == 502 && lastErr == "" {
		lastErr = "upstream server returned 502 Bad Gateway (temporary upstream failure)"
	}
	writeJSON(w, code, map[string]any{
		"error": map[string]any{
			"message": firstNonEmpty(lastErr, "upstream request failed"),
			"type":    "discovery2api_error", "code": errCode,
		}})
	r.log.Warn("request failed", "kind", kind, "status", code, "err", lastErr, "took", time.Since(start))
}

// shouldRotate reports whether switching keys is meaningful for this failure.
func (r *Relayer) shouldRotate(status int, err error, streamed bool) bool {
	if streamed {
		return false // body already committed to the client
	}
	if err != nil {
		return true // transport error: maybe the proxy/egress died
	}
	if r.pool.IsRetryable(status) {
		return true
	}
	// A 401 means this key is invalid/revoked; another key may still be good,
	// and NoteResult has already disabled it so we cannot pick it again.
	return status == 401
}

// forwardOnce performs a single upstream attempt. It returns the upstream
// status, the raw upstream body for non-streaming responses (nil when the
// response was streamed straight to the client), whether the client response
// was already committed, any transport error, and the parsed rate-limit
// headers for the pool.
func (r *Relayer) forwardOnce(w http.ResponseWriter, req *http.Request, kind Kind, cfg Config, lease keypool.Lease, body []byte, streaming bool) (status int, respBody []byte, streamed bool, ferr error, limits keypool.Limits) {
	upURL := strings.TrimRight(cfg.BaseURL, "/") + kind.UpstreamPath()

	upReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, upURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, false, fmt.Errorf("build upstream request: %w", err), limits
	}
	copyForwardHeaders(upReq.Header, req.Header, kind)
	applyAuth(upReq.Header, kind, lease.Entry.Key)

	client := &http.Client{
		Transport: r.transportFor(lease, cfg),
		Timeout:   cfg.Timeout,
	}
	resp, err := client.Do(upReq)
	if err != nil {
		return 0, nil, false, err, limits
	}
	defer resp.Body.Close()

	status = resp.StatusCode
	limits = keypool.ParseLimitHeaders(resp.Header.Get)

	// Non-streaming, or any error: buffer the body so the caller can decide.
	if status >= 400 || !streaming {
		buf, err := io.ReadAll(io.LimitReader(resp.Body, cfg.MaxBodyBytes+1))
		if err != nil {
			return status, nil, false, err, limits
		}
		if streaming && status < 400 {
			// Client asked for a stream but upstream answered with a plain
			// (error-free) body: forward it as-is.
			writeResponseFromUpstream(w, resp, buf)
			r.recordUsage(kind, lease, buf)
			return status, buf, true, nil, limits
		}
		if status < 400 {
			r.recordUsage(kind, lease, buf)
		}
		return status, buf, false, nil, limits
	}

	// Streaming success: pipe the stream, peeking the first chunk before the
	// response headers are committed (a pre-first-byte failure can still
	// rotate to another key), sending keepalive comments while waiting for
	// chunks, and capturing the final usage event for metrics.
	cw, commit, err := r.pipeStream(w, req, resp, cfg)
	if err != nil {
		if !commit {
			// Nothing reached the client: the caller may still rotate.
			return status, nil, false, err, limits
		}
		injectStreamError(w, err)
		return status, nil, true, err, limits
	}
	if cw == nil || cw.n == 0 {
		return status, nil, false, fmt.Errorf("upstream stream ended with no data"), limits
	}
	if !cw.sse {
		// The client asked for a stream but upstream answered with a buffered
		// JSON body. It was already forwarded verbatim with the upstream
		// content-type; report it as committed (streamed) so the caller does
		// not write it a second time, and record usage from the whole body.
		body := cw.whole.Bytes()
		r.recordUsage(kind, lease, body)
		return status, body, true, nil, limits
	}
	if u := findUsageInStream(cw.tail.Bytes(), kind); u.ok {
		r.recordUsageVal(kind, lease, u)
	}
	return status, nil, true, nil, limits
}

// recordUsage parses usage out of a buffered response and emits the hook.
func (r *Relayer) recordUsage(kind Kind, lease keypool.Lease, body []byte) {
	if r.onUsage == nil {
		return
	}
	if u := parseUsage(kind, body); u.ok {
		r.recordUsageVal(kind, lease, u)
	}
}

func (r *Relayer) recordUsageVal(kind Kind, lease keypool.Lease, u usage) {
	if r.onUsage == nil {
		return
	}
	model := u.model
	if model == "" {
		model = r.cfg.Load().DefaultModel
	}
	r.onUsage(lease.Entry.ID, kind, model, u.prompt, u.completion)
}

func (r *Relayer) pipeStream(w http.ResponseWriter, req *http.Request, resp *http.Response, cfg Config) (*countingWriter, bool, error) {
	flusher, _ := w.(http.Flusher)
	cw := &countingWriter{w: w, flusher: flusher, tail: newRing(64 << 10)}

	ch := make(chan readChunk, 32)
	errc := make(chan error, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case ch <- readChunk{data: cp}:
				case <-req.Context().Done():
					errc <- req.Context().Err()
					return
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()

	var ticker *time.Ticker
	var tickC <-chan time.Time
	if cfg.Keepalive > 0 {
		ticker = time.NewTicker(cfg.Keepalive)
		defer ticker.Stop()
		tickC = ticker.C
	}
	committed := false

	for {
		select {
		case c := <-ch:
			if !committed {
				// Peek the first chunk: a client may ask for a stream and get a
				// buffered JSON body (error or non-SSE upstream). Only commit
				// SSE headers when the payload really is a stream, otherwise
				// forward it as a plain response with the upstream content-type.
				if !looksLikeSSE(c.data) {
					cw.sse = false
					ct := resp.Header.Get("Content-Type")
					if ct == "" {
						ct = "application/json"
					}
					w.Header().Set("Content-Type", ct)
					committed = true
					cw.whole.Write(c.data)
					// Drain the rest into whole.
					for {
						select {
						case d := <-ch:
							cw.whole.Write(d.data)
						case err := <-errc:
							cw.n = int64(cw.whole.Len())
							writeBody(w, resp.StatusCode, cw.whole.Bytes())
							if err == io.EOF || err == req.Context().Err() {
								return cw, committed, nil
							}
							return cw, committed, err
						case <-req.Context().Done():
							return cw, committed, req.Context().Err()
						}
					}
				}
				cw.sse = true
				writeSSEHeaders(w)
				committed = true
			}
			if _, err := cw.Write(c.data); err != nil {
				return cw, committed, err
			}
		case err := <-errc:
			if err == io.EOF || err == req.Context().Err() {
				return cw, committed, nil
			}
			return cw, committed, err
		case <-tickC:
			if committed {
				// An SSE comment keeps intermediaries from timing out without
				// delivering any visible content to the client.
				if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
					return cw, committed, err
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		case <-req.Context().Done():
			return cw, committed, req.Context().Err()
		}
	}
}

type readChunk struct {
	data []byte
}

// usage is a best-effort extraction from a response body / stream tail.
type usage struct {
	ok                 bool
	model              string
	prompt, completion int
}

// parseUsage finds the usage object in body. For non-streaming responses the
// object sits at top level; for streams it is inside the last data event, so
// the caller passes the captured stream tail. Each kind tries its native
// field names first and falls back to the other shape: an upstream may answer
// a messages request with a chat-style body, and losing the counters silently
// is worse than crediting them under a different label.
func parseUsage(kind Kind, body []byte) usage {
	if len(body) == 0 {
		return usage{}
	}
	var chat struct {
		Model string `json:"model"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &chat); err != nil || chat.Usage == nil {
		return usage{}
	}
	c := chat.Usage
	if c.InputTokens > 0 || c.OutputTokens > 0 {
		return usage{ok: true, model: chat.Model, prompt: c.InputTokens, completion: c.OutputTokens}
	}
	return usage{ok: true, model: chat.Model, prompt: c.PromptTokens, completion: c.CompletionTokens}
}

// findUsageInStream scans SSE data lines for the last usage payload. If the
// stream tail is not SSE-shaped (upstream answered a stream request with a
// plain JSON body), fall back to parsing the whole buffer.
func findUsageInStream(tail []byte, kind Kind) usage {
	var last []byte
	for _, line := range bytes.Split(tail, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if bytes.Contains(payload, []byte(`"usage"`)) {
			last = payload
		}
	}
	if last != nil {
		return parseUsage(kind, last)
	}
	return parseUsage(kind, tail)
}
func (r *Relayer) transportFor(lease keypool.Lease, cfg Config) http.RoundTripper {
	// Cache the final RoundTripper per (keyID, proxyURL, connectTimeout) so
	// TCP+TLS connections are reused across requests. A config change (e.g.
	// ConnectTimeout) produces a new cache key, so stale entries are simply
	// orphaned rather than serving wrong timeouts.
	cacheKey := lease.Entry.ID + "|px=" + lease.ProxyURL + "|ct=" + strconv.FormatInt(int64(cfg.ConnectTimeout), 10)
	if v, ok := r.rtCache.Load(cacheKey); ok {
		return v.(http.RoundTripper)
	}
	var rt http.RoundTripper
	if ov := strings.TrimSpace(lease.ProxyURL); ov != "" {
		rt = proxypool.TransportForURL(ov)
	} else if r.proxies != nil && !r.proxies.Empty() {
		rt = r.proxies.Transport(lease.Entry.ID, nil)
	} else {
		rt = proxypool.TransportForURL("none")
	}
	if cfg.ConnectTimeout > 0 {
		rt = withConnectTimeout(rt, cfg.ConnectTimeout)
	}
	actual, _ := r.rtCache.LoadOrStore(cacheKey, rt)
	return actual.(http.RoundTripper)
}

// --- header plumbing ------------------------------------------------------

var forwardHeaderAllow = map[string]bool{
	"content-type":                true,
	"accept":                      true,
	"accept-language":             true,
	"anthropic-version":           true,
	"anthropic-beta":              true,
	"x-stainless-arch":            true,
	"x-stainless-os":              true,
	"x-stainless-package-version": true,
	"x-stainless-runtime":         true,
	"x-stainless-runtime-version": true,
	"x-stainless-lang":            true,
	"user-agent":                  true,
}

// copyForwardHeaders forwards a safe subset of client headers upstream. The
// Authorization / x-api-key is never copied here; applyAuth sets it.
func copyForwardHeaders(dst, src http.Header, kind Kind) {
	for k, vs := range src {
		if forwardHeaderAllow[strings.ToLower(k)] {
			for _, v := range vs {
				dst.Add(k, v)
			}
		}
	}
	if dst.Get("Content-Type") == "" {
		dst.Set("Content-Type", "application/json")
	}
	if kind == KindMessages && dst.Get("anthropic-version") == "" {
		dst.Set("anthropic-version", "1.0")
	}
}

// applyAuth sets the interface-specific credential header.
func applyAuth(h http.Header, kind Kind, key string) {
	switch kind {
	case KindMessages:
		h.Set("x-api-key", key)
	default:
		h.Set("Authorization", "Bearer "+key)
	}
}

// writeSSEHeaders prepares the client response for SSE passthrough.
func writeSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // don't let nginx buffer the stream
}

// writeResponseFromUpstream copies an upstream buffered response to the client.
func writeResponseFromUpstream(w http.ResponseWriter, resp *http.Response, body []byte) {
	h := w.Header()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", ct)
	} else {
		h.Set("Content-Type", "application/json")
	}
	writeBody(w, resp.StatusCode, body)
}

// injectStreamError appends an SSE error event without breaking the stream.
func injectStreamError(w http.ResponseWriter, err error) {
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": "upstream stream interrupted: " + err.Error(),
			"type":    "discovery2api_error", "code": "stream_interrupted",
		}})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// --- helpers ---------------------------------------------------------------

// isStream reports whether the body asks for streaming. Parsing failures are
// treated as non-streaming so a malformed body still gets a plain error.
func isStream(body []byte) bool {
	var probe struct {
		Stream any `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	switch v := probe.Stream.(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	}
	return false
}

// forceModel rewrites the model field; returns false when the body is not
// JSON or has no model field.
func forceModel(body []byte, model string) ([]byte, bool) {
	if model == "" {
		return body, false
	}
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return body, false
	}
	if _, ok := probe["model"]; !ok {
		return body, false
	}
	probe["model"] = model
	out, err := json.Marshal(probe)
	if err != nil {
		return body, false
	}
	return out, true
}

type countingWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	n       int64
	tail    *ring
	// sse reports whether the piped payload was really an SSE stream. A client
	// may request a stream and receive a buffered JSON body; whole holds it.
	sse   bool
	whole bytes.Buffer
}

// looksLikeSSE reports whether a first chunk has the shape of an event stream.
func looksLikeSSE(b []byte) bool {
	s := string(b)
	return strings.Contains(s, "data:") || strings.HasPrefix(s, "event:") ||
		strings.HasPrefix(s, ":") || strings.Contains(s, "\n\n")
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.tail != nil {
		c.tail.Write(p[:n])
	}
	if c.flusher != nil {
		c.flusher.Flush()
	}
	return n, err
}

// ring keeps the last maxBytes written so the final SSE usage event can be
// inspected after the stream has been forwarded.
type ring struct {
	buf []byte
	max int
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) Write(p []byte) (int, error) {
	if r.max <= 0 {
		return len(p), nil
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.max:]...)
	}
	return len(p), nil
}

func (r *ring) Bytes() []byte { return r.buf }

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	writeBody(w, code, b)
}

func writeBody(w http.ResponseWriter, code int, b []byte) {
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	_, _ = w.Write(b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// withConnectTimeout bounds connection setup (dial + TLS handshake) so a dead
// upstream is abandoned before cfg.Timeout's full budget is spent. An existing
// DialContext (SOCKS5 or per-key proxy) is wrapped, never replaced: replacing
// it would send traffic that was supposed to go through the proxy pool direct.
func withConnectTimeout(rt http.RoundTripper, d time.Duration) http.RoundTripper {
	t, ok := rt.(*http.Transport)
	if !ok {
		return rt
	}
	cp := t.Clone()
	prev := cp.DialContext
	if prev == nil {
		prev = (&net.Dialer{KeepAlive: 20 * time.Second}).DialContext
	}
	cp.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return prev(ctx, network, addr)
	}
	cp.TLSHandshakeTimeout = d
	cp.ExpectContinueTimeout = d
	return cp
}
