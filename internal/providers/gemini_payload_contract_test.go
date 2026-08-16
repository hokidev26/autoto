package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/subscriptionauth"
)

// Cloud Code is a proto-backed API: a field it does not recognise is answered
// with 400 INVALID_ARGUMENT rather than ignored, and only the first offender is
// named. One stray key therefore disables the provider outright instead of
// degrading it, which is exactly what a "stream": true inside the inner request
// object did — every model call failed until it was removed.
//
// These allow lists are a wire contract, not a style preference. Adding a key
// here must be backed by a real request that Google accepted.
var (
	geminiCloudCodeTopLevelFields = map[string]struct{}{
		"project":            {},
		"request":            {},
		"model":              {},
		"userAgent":          {},
		"requestType":        {},
		"requestId":          {},
		"enabledCreditTypes": {},
	}
	geminiCloudCodeRequestFields = map[string]struct{}{
		"contents":          {},
		"sessionId":         {},
		"systemInstruction": {},
		"generationConfig":  {},
		"tools":             {},
		"toolConfig":        {},
		// Verified against the live endpoint on 2026-07-31 with
		// gemini-3.1-flash-image: accepted, and the generation still succeeded.
		"safetySettings": {},
	}
	geminiCloudCodeGenerationFields = map[string]struct{}{
		"maxOutputTokens":    {},
		"thinkingConfig":     {},
		"responseModalities": {},
		// Verified against the live endpoint on 2026-07-31 with
		// gemini-3.1-flash-image: accepted, and it genuinely takes effect —
		// 1:1/2K returned 2.8 MB, 16:9/4K returned 10 MB, 21:9/1K returned
		// 460 KB from the same prompt.
		"imageConfig": {},
		// Verified the same way. The upstream serves one image per call, so this
		// pins the expectation rather than widening it.
		"candidateCount": {},
	}
)

// findGeminiCloudCodeUnknownFields walks only the three envelope levels this
// package constructs itself. It deliberately does not recurse into contents or
// tools: those carry caller-supplied text and JSON Schema and are legitimately
// open-ended, so policing them would make the guard brittle without catching
// anything the envelope levels miss.
func findGeminiCloudCodeUnknownFields(body []byte) []string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return []string{"<malformed body>"}
	}
	unknown := []string{}
	collect := func(prefix string, object map[string]json.RawMessage, allowed map[string]struct{}) {
		for key := range object {
			if _, ok := allowed[key]; !ok {
				unknown = append(unknown, prefix+key)
			}
		}
	}
	collect("", top, geminiCloudCodeTopLevelFields)

	inner := map[string]json.RawMessage{}
	if raw, ok := top["request"]; ok {
		if err := json.Unmarshal(raw, &inner); err != nil {
			return append(unknown, "request:<not an object>")
		}
	}
	collect("request.", inner, geminiCloudCodeRequestFields)

	if raw, ok := inner["generationConfig"]; ok {
		generation := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &generation); err != nil {
			return append(unknown, "request.generationConfig:<not an object>")
		}
		collect("request.generationConfig.", generation, geminiCloudCodeGenerationFields)
	}
	sort.Strings(unknown)
	return unknown
}

// omittedTopLevel names fields this payload is expected not to carry. Image
// generation drops enabledCreditTypes because that endpoint rejects it. Agent
// requests include it, matching Antigravity-Manager.
func assertGeminiCloudCodePayloadClean(t *testing.T, payload map[string]any, wantRequestKeys []string, omittedTopLevel ...string) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if unknown := findGeminiCloudCodeUnknownFields(encoded); len(unknown) > 0 {
		t.Fatalf("payload sends unknown fields %v; Cloud Code answers those with 400 INVALID_ARGUMENT. If Google really accepts one, add it to the allow list in this file.", unknown)
	}
	omitted := make(map[string]struct{}, len(omittedTopLevel))
	for _, key := range omittedTopLevel {
		omitted[key] = struct{}{}
		if _, present := payload[key]; present {
			t.Fatalf("payload must not carry top-level field %q", key)
		}
	}
	for key := range geminiCloudCodeTopLevelFields {
		if _, skip := omitted[key]; skip {
			continue
		}
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload is missing required top-level field %q", key)
		}
	}
	inner, ok := payload["request"].(map[string]any)
	if !ok {
		t.Fatalf("request field is %T, want map", payload["request"])
	}
	keys := make([]string, 0, len(inner))
	for key := range inner {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if fmt.Sprint(keys) != fmt.Sprint(wantRequestKeys) {
		t.Fatalf("inner request keys = %v, want %v", keys, wantRequestKeys)
	}
}

func TestBuildGeminiCloudCodePayloadSendsOnlyKnownFields(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		assertGeminiCloudCodePayloadClean(t, buildGeminiCloudCodePayload(GenerateRequest{
			Messages: []Message{{Role: "user", Content: "hello"}},
		}, "gemini-3-flash", "project-1", ""), []string{"contents", "sessionId"})
	})

	t.Run("every optional section", func(t *testing.T) {
		assertGeminiCloudCodePayloadClean(t, buildGeminiCloudCodePayload(GenerateRequest{
			SystemPrompt:          "Be concise.",
			Messages:              []Message{{Role: "user", Content: "hello"}},
			Tools:                 []ToolSpec{{Name: "lookup", Description: "Lookup", Schema: map[string]any{"type": "object"}}},
			MaxOutputTokens:       128,
			EnableImageGeneration: true,
		}, "gemini-3-flash", "project-1", "high"),
			[]string{"contents", "generationConfig", "sessionId", "systemInstruction", "toolConfig", "tools"})
	})

	// The image endpoint takes a narrower envelope: it rejects
	// systemInstruction and tools, and carries imageConfig.
	t.Run("image generation", func(t *testing.T) {
		assertGeminiCloudCodePayloadClean(t, buildGeminiCloudCodePayload(GenerateRequest{
			SystemPrompt: "Be concise.",
			Messages:     []Message{{Role: "user", Content: "a red circle"}},
			Tools:        []ToolSpec{{Name: "lookup", Schema: map[string]any{"type": "object"}}},
			ImageOptions: ImageOptions{Size: "16:9", Quality: "hd"},
		}, "gemini-3.1-flash-image", "project-1", "high"),
			[]string{"contents", "generationConfig", "safetySettings", "sessionId"}, "enabledCreditTypes")
	})

	t.Run("claude", func(t *testing.T) {
		assertGeminiCloudCodePayloadClean(t, buildGeminiCloudCodePayload(GenerateRequest{
			SystemPrompt: "Be concise.",
			Messages:     []Message{{Role: "user", Content: "hello"}},
			Tools:        []ToolSpec{{Name: "lookup", Schema: map[string]any{"type": "object"}}},
		}, "claude-sonnet-4-6", "project-1", "high"),
			[]string{"contents", "generationConfig", "sessionId", "systemInstruction", "toolConfig", "tools"})
	})
}

// TestGeminiCloudCodeUnknownFieldDetection keeps the guard itself honest: a
// detector that silently matched everything would make the contract test above
// vacuous, so replay the exact envelope Google rejected in production and
// require it to be flagged.
func TestGeminiCloudCodeUnknownFieldDetection(t *testing.T) {
	const regression = `{"project":"p","model":"m","userAgent":"antigravity","requestType":"agent",` +
		`"requestId":"agent-1","enabledCreditTypes":["GOOGLE_ONE_AI"],` +
		`"request":{"contents":[],"sessionId":"s","stream":true}}`
	if got := findGeminiCloudCodeUnknownFields([]byte(regression)); len(got) != 1 || got[0] != "request.stream" {
		t.Fatalf("detector missed the historical stream regression: %v", got)
	}

	const bogusTopLevel = `{"project":"p","model":"m","userAgent":"antigravity","requestType":"agent",` +
		`"requestId":"agent-1","enabledCreditTypes":["GOOGLE_ONE_AI"],"extra":1,` +
		`"request":{"contents":[],"sessionId":"s"}}`
	if got := findGeminiCloudCodeUnknownFields([]byte(bogusTopLevel)); len(got) != 1 || got[0] != "extra" {
		t.Fatalf("detector missed an unknown top-level field: %v", got)
	}

	const bogusGeneration = `{"project":"p","model":"m","userAgent":"antigravity","requestType":"agent",` +
		`"requestId":"agent-1","enabledCreditTypes":["GOOGLE_ONE_AI"],` +
		`"request":{"contents":[],"sessionId":"s","generationConfig":{"temperature":1}}}`
	if got := findGeminiCloudCodeUnknownFields([]byte(bogusGeneration)); len(got) != 1 || got[0] != "request.generationConfig.temperature" {
		t.Fatalf("detector missed an unknown generationConfig field: %v", got)
	}

	// Caller-supplied schema and content must not be policed.
	const clean = `{"project":"p","model":"m","userAgent":"antigravity","requestType":"agent",` +
		`"requestId":"agent-1","enabledCreditTypes":["GOOGLE_ONE_AI"],` +
		`"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"sessionId":"s",` +
		`"generationConfig":{"maxOutputTokens":8,"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}},` +
		`"tools":[{"functionDeclarations":[{"name":"lookup","parametersJsonSchema":{"anything":"goes"}}]}]}}`
	if got := findGeminiCloudCodeUnknownFields([]byte(clean)); len(got) != 0 {
		t.Fatalf("detector flagged a valid payload: %v", got)
	}
}

// geminiCloudCodeStrictUpstream makes a mock behave like the real control plane.
// The pre-existing failover test decodes the body, checks two fields and returns
// success, so it accepted a payload the live endpoint rejects outright — which is
// why the broken envelope shipped green. Wrapping the handler closes that gap for
// every request the provider makes during a test.
func geminiCloudCodeStrictUpstream(t *testing.T, handler http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("reading Cloud Code request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if unknown := findGeminiCloudCodeUnknownFields(body); len(unknown) > 0 {
			t.Errorf("Cloud Code envelope carries unknown fields %v; the live endpoint answers 400 INVALID_ARGUMENT", unknown)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":{"code":400,"message":"Invalid JSON payload received. Unknown name %q at 'request': Cannot find field.","status":"INVALID_ARGUMENT"}}`, unknown[0])
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		handler(w, r)
	}
}

// TestGeminiProviderStrictUpstreamAcceptsTheRealEnvelope drives Generate through
// the strict mock, so the payload the provider actually puts on the wire — not a
// hand-built fixture — is held to the contract.
func TestGeminiProviderStrictUpstreamAcceptsTheRealEnvelope(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gemini")
	store := subscriptionauth.NewStore(dir)
	createGeminiProviderTestAccount(t, store, "token-only", "only", "project-only", 1, time.Now().Add(time.Hour))

	var calls atomic.Int32
	server := httptest.NewServer(geminiCloudCodeStrictUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["project"] != "project-only" || body["model"] != "gemini-3-flash" {
			t.Errorf("unexpected Cloud Code body: %v", body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, ok := body["enabledCreditTypes"]; !ok {
			t.Errorf("agent generate must include enabledCreditTypes: %v", body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2}}}\n\n"))
	}))
	defer server.Close()

	provider := newGeminiProviderForTest(config.ProviderConfig{
		Name: "gemini", Type: config.ProviderTypeGemini, Model: "gemini-3-flash",
		Models:              []config.ProviderModelConfig{{Name: "gemini-3-flash", ContextTokenLimit: 1048576}},
		CredentialStorePath: dir,
	}, server.Client(), server.URL)

	events, err := provider.Generate(context.Background(), GenerateRequest{
		SystemPrompt:    "Be concise.",
		Messages:        []Message{{Role: "user", Content: "hi"}},
		Tools:           []ToolSpec{{Name: "lookup", Description: "Lookup", Schema: map[string]any{"type": "object"}}},
		MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, event := range collectGeminiProviderEvents(t, events) {
		if event.Type == "text" {
			text += event.Text
		}
	}
	if text != "hello" || calls.Load() != 1 {
		t.Fatalf("text = %q, calls = %d; want \"hello\" and 1", text, calls.Load())
	}
}

// The quota presence rules are the easiest thing to get wrong here, and getting
// them wrong is worse than showing nothing: remaining_fraction has implicit
// proto3 presence, so the wire format omits it at 0.0. Treating "absent" as full
// would report 100% remaining at the exact moment an account is exhausted.
func TestParseGeminiAvailableModelsQuotaPresence(t *testing.T) {
	const body = `{"models":{
		"gemini-3-flash":       {"displayName":"Flash","quotaInfo":{"remainingFraction":1.0,"resetTime":"2026-08-01T00:00:00Z"}},
		"gemini-3.1-pro-low":   {"displayName":"Pro","quotaInfo":{"remainingFraction":0.256}},
		"gemini-exhausted":     {"displayName":"Spent","quotaInfo":{"resetTime":"2026-08-02T00:00:00Z"}},
		"gemini-no-quota":      {"displayName":"Unknown"},
		"gemini-disabled":      {"disabled":true,"quotaInfo":{"remainingFraction":0.5}},
		"not a model id!":      {"quotaInfo":{"remainingFraction":0.9}}
	}}`

	models, quotas, err := parseGeminiAvailableModels(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(models, ","); got != "gemini-3-flash,gemini-3.1-pro-low,gemini-disabled,gemini-exhausted,gemini-no-quota" {
		t.Fatalf("models = %q", got)
	}

	byModel := map[string]AccountModelQuotaSnapshot{}
	for _, quota := range quotas {
		byModel[quota.Model] = quota
	}
	// A model with no quotaInfo at all reports nothing, which is different from
	// reporting zero, so it must not appear as an exhausted model.
	if _, present := byModel["gemini-no-quota"]; present {
		t.Fatal("a model without quotaInfo must not be reported as having quota")
	}
	if len(quotas) != 4 {
		t.Fatalf("expected 4 quota rows, got %d: %+v", len(quotas), quotas)
	}
	for _, want := range []struct {
		model    string
		percent  int
		reset    string
		disabled bool
	}{
		{"gemini-3-flash", 100, "2026-08-01T00:00:00Z", false},
		{"gemini-3.1-pro-low", 26, "", false},
		{"gemini-exhausted", 0, "2026-08-02T00:00:00Z", false},
		{"gemini-disabled", 50, "", true},
	} {
		got := byModel[want.model]
		if got.RemainingPercent != want.percent || got.Reset != want.reset || got.Disabled != want.disabled {
			t.Errorf("%s = %+v, want percent %d reset %q disabled %v", want.model, got, want.percent, want.reset, want.disabled)
		}
	}
}

func TestParseGeminiAvailableModelsRejectsGarbage(t *testing.T) {
	if _, _, err := parseGeminiAvailableModels(strings.NewReader(`not json`)); err == nil {
		t.Fatal("expected an error for a non-JSON body")
	}
	// An empty envelope is valid, just empty — it must not error, so ListModels
	// still falls through to the statically configured models.
	models, quotas, err := parseGeminiAvailableModels(strings.NewReader(`{}`))
	if err != nil || len(models) != 0 || len(quotas) != 0 {
		t.Fatalf("empty envelope: models=%v quotas=%v err=%v", models, quotas, err)
	}
}
