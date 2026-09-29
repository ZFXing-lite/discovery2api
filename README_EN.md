# discovery2api

English | [简体中文](README.md)

A dedicated gateway for the [Intern Discovery Dawn Preview API](https://discovery-api.intern-ai.org.cn/docs).
Point any OpenAI / Anthropic / Responses-compatible client at it, fill in one or
more `` keys, and it takes care of key rotation, rate-limit handling,
retries and SOCKS5 egress.

Built with the same patterns as [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI),
[autoclaw2api](https://github.com/ZFXing-lite/autoclaw2api) and
[workbuddy2api](https://github.com/Sliverkiss/workbuddy2api): a single binary,
one YAML config, no database, zero runtime dependencies.

## Why

The Intern Discovery API rate-limits per account (`x-rpm-limit` / `x-rpm-remaining`,
`Retry-After` on 429) and accepts three interfaces on one base URL. This
gateway:

- rotates requests across multiple keys so one account's limit does not stop
  your clients,
- reads the documented rate-limit headers and sits a key out for the rest of
  the minute window instead of hammering it into a 429,
- retries retryable failures (429/5xx/transport) on another key, but never
  rotates a 400-class client error, and never rotates once a stream has
  started,
- can send all upstream traffic through a pool of SOCKS5 proxies.

## Quick start

```bash
git clone https://github.com/ZFXing-lite/discovery2api
cd discovery2api
cp config.example.yaml config.yaml
# edit config.yaml: put your  keys under upstream.keys
go run ./cmd/server -c config.yaml
```

Or with Docker:

```bash
docker build -t discovery2api .
docker run -p 8319:8319 -v "$PWD/config.yaml:/app/config.yaml" -v discovery2api-state:/app/state discovery2api
```

Then point a client at the gateway (all three interfaces share the same URL):

```bash
export OPENAI_API_KEY=gw-change-me-1
curl -X POST http://127.0.0.1:8319/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}'
```

| Client | Base URL | Interface |
| --- | --- | --- |
| OpenAI SDK / curl | `http://host:8319/v1` | `/v1/chat/completions` |
| Anthropic SDK / Claude Code | `http://host:8319` | `/v1/messages` (`x-api-key`) |
| Codex / Qoder | `http://host:8319/v1` | `/v1/responses` |

`GET /v1/models` returns the fixed `auto` entry.

## Configuration

Everything lives in `config.yaml` (env overrides: `DISCOVERY2API_KEYS`,
`DISCOVERY2API_API_KEYS`, `DISCOVERY2API_PROXIES`, `DISCOVERY2API_BASE_URL`,
`DISCOVERY2API_PORT`, ...). The file is hot-reloaded on change.

```yaml
port: 8319
api-keys: ["gw-change-me-1"]        # keys clients use on THIS gateway

upstream:
  base-url: "https://discovery-api.intern-ai.org.cn"
  default-model: "auto"
  force-model: true                  # rewrite request "model" to default-model
  keys:
    - key: "xxx"
      weight: 1                      # weighted round-robin
      proxy: ""                      # optional per-key socks5:// override, or "none"
    - key: "yyy"

proxy:
  policy: "round-robin"              # round-robin | random | sticky-key
  health-every: 30s
  fail-cooldown: 30s
  socks5:
    - url: "socks5://user:pass@1.2.3.4:1080"

rate-limit:
  respect-header: true               # parse x-rpm-* / Retry-After
  min-rpm-reserve: 1                 # skip a key with fewer remaining requests
  cooldown-429: 60s                  # used when upstream sends no Retry-After
  cooldown-5xx: 30s
  err-threshold: 3                   # consecutive errors -> cooldown
  err-cooldown: 10m
  disable-on-401: true               # invalid/revoked key is taken out for good
  max-retries: 3                     # cross-key retries
  retry-on: [429, 500, 502, 503, 504]
  backoff: 1s                        # exponential, +/-25% jitter
```

## Key pool behaviour

- **Selection**: candidates are shuffled, sorted by an effective weight (config
  weight damped by observed remaining RPM), truncated to the top 5 and picked
  weighted-randomly, with an LRU tie-break. All-cooled pools fall back to the
  key that recovers soonest.
- **Cooldowns** are OR-gated over a single expiry timestamp: a repeated 429
  while cooling never stacks penalties. 429 honours `Retry-After` (capped at
  10m), 5xx uses `cooldown-5xx`, and consecutive failures open a circuit
  breaker with bounded exponential backoff (10m, 20m, 40m ... capped at 6h).
- **401** disables the key permanently (per the docs, that key is invalid or
  revoked) until you re-enable it through the management API or a restart.
- **Transport failures** (proxy down, timeouts) degrade a key out of rotation
  for `err-cooldown` without blaming the key itself.
- **Persistence**: pool state and usage counters are written atomically
  (`state.json`, tmp + rename, 0600) every few seconds and restored on start.

## Endpoints

| Path | Description |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat Completions |
| `POST /v1/messages` | Anthropic Messages (`x-api-key`) |
| `POST /v1/responses` | OpenAI Responses |
| `GET /v1/models` | Fixed model catalog |
| `GET /healthz` | Liveness (503 when no key is usable) |
| `GET /status` | Masked pool + usage snapshot |
| `*/v0/management/*` | Ops API (disabled unless `remote-management.secret-key` set) |

Management API (loopback-only by default):

```bash
curl -H "Authorization: Bearer $MGMT_KEY" http://127.0.0.1:8319/v0/management/keys
curl -X POST -H "Authorization: Bearer $MGMT_KEY" \
  http://127.0.0.1:8319/v0/management/keys/<id>/disable
curl -X POST -H "Authorization: Bearer $MGMT_KEY" \
  http://127.0.0.1:8319/v0/management/keys/<id>/enable
```

Keys are never logged or returned in the management API — only a stable,
masked hash id (`status.json` style: `****ey`).

## Streaming

SSE responses are streamed straight through with per-chunk flushing
(`X-Accel-Buffering: no` so nginx does not buffer them) and SSE comment
keepalives while waiting for the first token. The response headers are only
committed after the first upstream chunk, so a pre-first-byte upstream failure
can still rotate to another key; a failure mid-stream is injected as an SSE
error event instead of cutting the connection.

## Development

```bash
go test ./...          # unit + end-to-end tests against a mock upstream
go build -ldflags="-s -w" -o discovery2api ./cmd/server
```

## Notes and limits

- Intern Discovery's rate limit is per **account** and shared by every key under it, so
  rotation helps when your keys come from different accounts; it cannot raise a
  single account's RPM.
- `auto` is text-only (256K context); the gateway forwards
  request bodies as-is and does not transcode multimodal input.
- Output-length caps (`max_completion_tokens` / `max_tokens` /
  `max_output_tokens`, 1-65536) are enforced upstream.

## License

MIT.
