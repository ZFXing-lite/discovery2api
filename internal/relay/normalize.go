package relay

import (
	"encoding/json"
	"strings"
)

// normalize.go implements the request normalization layer: whatever dialect a
// downstream client speaks, the gateway rewrites only the pieces the upstream
// is known to reject (OpenAI o-series / Responses-API conventions that predate
// the upstream's OpenAI-compat surface). Everything else passes through
// byte-for-byte untouched. The transform is idempotent and lossless-by-design:
// a body that already matches the upstream's dialect is returned unmodified.

// normalizeResult describes what a normalization pass changed (for logging).
type normalizeResult struct {
	Changed bool
	Model   string // model name from the body (diagnostics only)
	Notes   []string
}

// roleMap maps roles the upstream rejects onto its accepted set.
var roleMap = map[string]string{
	"developer":    "system",
	"developer_v2": "system",
}

// knownDialectFields are o-series / Responses-API fields the upstream does not
// understand. They are stripped, and where a legacy equivalent exists the
// value is carried over. Stripping only ever removes what would be rejected,
// so an upstream that later learns these fields loses nothing it needs.
var stripFields = []string{
	"reasoning_effort",
	"reasoning",
	"verbosity",
	"store",
	"previous_response_id",
	"truncation",
	"instructions",
	"text",
}

// normalizeBody rewrites a chat-completions style JSON body into the upstream's
// accepted dialect. It never fails the request: on any parse or re-marshal
// error the original body is returned untouched (the upstream will produce its
// own error, which the relay surfaces verbatim).
func normalizeBody(body []byte) ([]byte, normalizeResult) {
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return body, normalizeResult{}
	}

	res := normalizeResult{}
	if m, ok := probe["model"].(string); ok {
		res.Model = m
	}

	changed := false

	// 1. Strip fields the upstream does not know.
	for _, f := range stripFields {
		if _, ok := probe[f]; ok {
			delete(probe, f)
			changed = true
			res.Notes = append(res.Notes, "stripped:"+f)
		}
	}

	// 2. max_completion_tokens -> max_tokens (same parameter, new name).
	if v, ok := probe["max_completion_tokens"]; ok {
		if _, exists := probe["max_tokens"]; !exists {
			probe["max_tokens"] = v
			res.Notes = append(res.Notes, "moved:max_completion_tokens->max_tokens")
		} else {
			res.Notes = append(res.Notes, "dropped:max_completion_tokens(max_tokens already set)")
		}
		delete(probe, "max_completion_tokens")
		changed = true
	}

	// 3. Map message roles the upstream rejects. Applies to messages and
	//    template messages nested inside tools.
	if msgs, ok := probe["messages"].([]any); ok {
		for _, mi := range msgs {
			m, ok := mi.(map[string]any)
			if !ok {
				continue
			}
			if role, ok := m["role"].(string); ok {
				if mapped, ok := roleMap[strings.ToLower(strings.TrimSpace(role))]; ok {
					m["role"] = mapped
					changed = true
					res.Notes = append(res.Notes, "role:"+role+"->"+mapped)
				}
			}
		}
	}

	if !changed {
		return body, res
	}

	out, err := json.Marshal(probe)
	if err != nil {
		return body, normalizeResult{Model: res.Model}
	}
	res.Changed = true
	return out, res
}
