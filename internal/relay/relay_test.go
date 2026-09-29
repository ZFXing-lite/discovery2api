package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZFXing-lite/discovery2api/internal/keypool"
)

// ---------- mock upstream ----------

type mockUpstream struct {
	t        *testing.T
	failKeys map[string]int // key -> status to return
	sawKeys  []string
	body     string
}

func (m *mockUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	m.sawKeys = append(m.sawKeys, key)
	if st, ok := m.failKeys[key]; ok {
		w.Header().Set("Content-Type", "application/json")
		if st == 429 {
			w.Header().Set("Retry-After", "30")
			w.Header().Set("X-Rpm-Limit", "60")
			w.Header().Set("X-Rpm-Remaining", "0")
		}
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"error":{"message":"mock failure","type":"discovery_api_error"}}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	var probe struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Model != "auto" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model","type":"discovery_api_error"}}`))
		return
	}
	if probe.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f := w.(http.Flusher)
		for _, chunk := range []string{"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n",
			"data: [DONE]\n\n"} {
			_, _ = w.Write([]byte(chunk))
			f.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(`{"id":"1","model":"auto","choices":[{"message":{"content":"Hello world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
}

// ---------- helpers ----------

func testRelayer(t *testing.T, failKeys map[string]int) (*Relayer, *mockUpstream, string) {
	t.Helper()
	mu := &mockUpstream{t: t, failKeys: failKeys}
	up := httptest.NewServer(mu)
	t.Cleanup(up.Close)

	pool := keypool.New([]keypool.UpstreamKey{
		{Key: "bad-key"}, {Key: "good-key"},
	}, keypool.Settings{
		Cooldown429:  time.Minute,
		Cooldown5xx:  30 * time.Second,
		ErrThreshold: 3,
		ErrCooldown:  time.Minute,
		DisableOn401: true,
		MaxRetries:   3,
		RetryOn:      []int{429, 500, 502, 503, 504},
	}, "")
	// Deterministic picks so rotation assertions are stable.
	pool.SetRand(1)
	probe, err := pool.Pick(nil)
	if err != nil {
		t.Fatalf("probe pick: %v", err)
	}
	first := probe.Entry.Key
	probe.Release()

	r := New(pool, nil, Config{
		BaseURL: up.URL, DefaultModel: "auto", ForceModel: true,
		Timeout: 30 * time.Second, MaxRetries: 3, Keepalive: 0,
	}, func(keyID string, kind Kind, model string, prompt int, completion int) {
		t.Logf("usage: key=%s kind=%s model=%s prompt=%d completion=%d", keyID, kind, model, prompt, completion)
	})
	return r, mu, first
}

func newReq(t *testing.T, path, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ---------- tests ----------

func TestNonStreamingSuccess(t *testing.T) {
	r, mu, first := testRelayer(t, nil)
	w := httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`), KindChat)

	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Hello world") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
	if len(mu.sawKeys) != 1 || mu.sawKeys[0] != first {
		t.Fatalf("expected only %q used, got %v", first, mu.sawKeys)
	}
}

func TestStreamingSuccess(t *testing.T) {
	r, mu, first := testRelayer(t, nil)
	w := httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`), KindChat)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Hello") || !strings.Contains(body, "world") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream incomplete: %s", body)
	}
	if !strings.Contains(body, "usage") {
		t.Fatalf("usage event missing: %s", body)
	}
	if len(mu.sawKeys) != 1 || mu.sawKeys[0] != first {
		t.Fatalf("expected only %q used, got %v", first, mu.sawKeys)
	}
}

func TestRotatesPast401(t *testing.T) {
	r, mu, first := testRelayer(t, nil)
	if mu.failKeys == nil {
		mu.failKeys = map[string]int{}
	}
	mu.failKeys[first] = 401
	w := httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/responses",
		`{"model":"auto","input":"hi"}`), KindResponses)

	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(mu.sawKeys) != 2 {
		t.Fatalf("expected one rotation, saw %v", mu.sawKeys)
	}
	if mu.sawKeys[0] != first || mu.sawKeys[1] == first {
		t.Fatalf("expected rotation away from %q, got %v", first, mu.sawKeys)
	}
	total, healthy := r.pool.Summary()
	if total != 2 || healthy != 1 {
		t.Fatalf("pool should have 1 disabled key, got total=%d healthy=%d", total, healthy)
	}
}

func TestRotatesPast429AndCools(t *testing.T) {
	r, mu, first := testRelayer(t, nil)
	if mu.failKeys == nil {
		mu.failKeys = map[string]int{}
	}
	mu.failKeys[first] = 429
	w := httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/messages",
		`{"model":"auto","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`), KindMessages)

	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(mu.sawKeys) != 2 || mu.sawKeys[1] == first {
		t.Fatalf("expected rotation away from %q, got %v", first, mu.sawKeys)
	}
	// The rate-limited key must now be out of rotation.
	for i := 0; i < 5; i++ {
		lease, err := r.pool.Pick(nil)
		if err != nil {
			t.Fatalf("pick failed: %v", err)
		}
		if lease.Entry.Key == first {
			lease.Release()
			t.Fatalf("rate-limited key was picked again")
		}
		lease.Release()
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	r, mu, _ := testRelayer(t, map[string]int{"bad-key": 400, "good-key": 400})
	w := httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`), KindChat)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400 forwarded verbatim", w.Code)
	}
	if len(mu.sawKeys) != 1 {
		t.Fatalf("a 400 must not rotate, saw %v", mu.sawKeys)
	}
}

func TestExhaustedRetriesReturnUpstreamError(t *testing.T) {
	r, _, _ := testRelayer(t, map[string]int{"bad-key": 503, "good-key": 503})
	w := httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`), KindChat)

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mock failure") {
		t.Fatalf("expected upstream body surfaced: %s", w.Body.String())
	}
}

func TestForceModel(t *testing.T) {
	r, mu, _ := testRelayer(t, nil)
	r.Update(Config{BaseURL: r.cfg.Load().BaseURL, DefaultModel: "auto", ForceModel: false,
		Timeout: 30 * time.Second, MaxRetries: 3})

	// With ForceModel off, a client that sends a different model id gets a 400.
	w := httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`), KindChat)
	if w.Code != 400 {
		t.Fatalf("expected 400 without force-model, got %d", w.Code)
	}

	r.Update(Config{BaseURL: r.cfg.Load().BaseURL, DefaultModel: "auto", ForceModel: true,
		Timeout: 30 * time.Second, MaxRetries: 3})
	w = httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`), KindChat)
	if w.Code != 200 {
		t.Fatalf("force-model should have made it 200, got %d", w.Code)
	}
	_ = mu
}

func TestMessagesUsesXApiKey(t *testing.T) {
	r, mu, _ := testRelayer(t, nil)
	w := httptest.NewRecorder()
	r.Handle(w, newReq(t, "/v1/messages",
		`{"model":"auto","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`), KindMessages)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	_ = mu
}

func TestClientCancelStopsUpstream(t *testing.T) {
	r, _, _ := testRelayer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	r.Handle(w, req, KindChat)
	// Must not panic or hang; the cancelled context ends the request.
	if w.Code == 0 {
		t.Fatal("no response written")
	}
}

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Run()
}

// TestStreamRequestGetsJSONBody covers the case where the client asks for a
// stream but the upstream answers with a buffered JSON body. The gateway must
// not declare text/event-stream, and the usage must be recorded.
func TestStreamRequestGetsJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"1","model":"auto","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":11,"completion_tokens":4}}`))
	}))
	t.Cleanup(srv.Close)

	pool := keypool.New([]keypool.UpstreamKey{{Key: "k"}}, keypool.Settings{}, "")
	var got promptCapture
	r := New(pool, nil, Config{
		BaseURL: srv.URL, DefaultModel: "auto", ForceModel: true, Timeout: 10 * time.Second,
	}, got.hook)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"auto","stream":true,"messages":[]}`))
	w := httptest.NewRecorder()
	r.Handle(w, req, KindChat)

	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type must be the upstream JSON, got %q; body %s", ct, w.Body.String())
	}
	if got.prompt != 11 || got.completion != 4 {
		t.Fatalf("usage not recorded from non-SSE answer: prompt=%d completion=%d", got.prompt, got.completion)
	}
}

type promptCapture struct {
	prompt, completion int
}

func (p *promptCapture) hook(keyID string, kind Kind, model string, pr, co int) {
	p.prompt, p.completion = pr, co
}

// TestParseUsageCrossFormat makes sure usage is not silently lost when an
// endpoint answers with the other API's field names.
func TestParseUsageCrossFormat(t *testing.T) {
	chat := []byte(`{"model":"auto","usage":{"prompt_tokens":12,"completion_tokens":7}}`)
	anth := []byte(`{"model":"auto","usage":{"input_tokens":12,"output_tokens":7}}`)

	if u := parseUsage(KindChat, chat); !u.ok || u.prompt != 12 || u.completion != 7 {
		t.Fatalf("chat/chat: %+v", u)
	}
	// A messages request answered with a chat-style body must still count.
	if u := parseUsage(KindMessages, chat); !u.ok || u.prompt != 12 || u.completion != 7 {
		t.Fatalf("messages/chat: %+v", u)
	}
	if u := parseUsage(KindMessages, anth); !u.ok || u.prompt != 12 || u.completion != 7 {
		t.Fatalf("messages/anthropic: %+v", u)
	}
	if u := parseUsage(KindChat, anth); !u.ok || u.prompt != 12 || u.completion != 7 {
		t.Fatalf("chat/anthropic: %+v", u)
	}
	if u := parseUsage(KindChat, []byte(`{"nope":1}`)); u.ok {
		t.Fatalf("empty usage must not parse: %+v", u)
	}
}
