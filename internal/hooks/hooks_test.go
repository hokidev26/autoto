package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeGateway struct {
	shell func(ShellRequest) (GatewayResult, error)
	http  func(HTTPRequest) (GatewayResult, error)
	llm   func(LLMRequest) (GatewayResult, error)
}

func (g fakeGateway) ExecuteShell(_ context.Context, request ShellRequest) (GatewayResult, error) {
	if g.shell == nil {
		return GatewayResult{}, errors.New("unexpected shell")
	}
	return g.shell(request)
}
func (g fakeGateway) ExecuteHTTP(_ context.Context, request HTTPRequest) (GatewayResult, error) {
	if g.http == nil {
		return GatewayResult{}, errors.New("unexpected http")
	}
	return g.http(request)
}
func (g fakeGateway) ExecuteLLM(_ context.Context, request LLMRequest) (GatewayResult, error) {
	if g.llm == nil {
		return GatewayResult{}, errors.New("unexpected llm")
	}
	return g.llm(request)
}

func validShellHook() Hook {
	return Hook{ID: "hook-1", Name: "Audit", Enabled: true, Event: EventToolAfter, Scope: Scope{Kind: ScopeGlobal}, Priority: 20, Mode: ModeAsync, FailurePolicy: FailureRetry, Action: Action{Kind: ActionShell, Shell: &ShellAction{Executable: "audit-helper", CWD: "tools/hooks", CanonicalStdinV1: true}}}
}

func TestNormalizeValidateAndMatch(t *testing.T) {
	hook, err := NormalizeAndValidateHook(validShellHook())
	if err != nil {
		t.Fatal(err)
	}
	if hook.Action.Shell.TimeoutSeconds != DefaultTimeoutSeconds {
		t.Fatalf("timeout=%d", hook.Action.Shell.TimeoutSeconds)
	}
	event := Event{Name: EventToolAfter, RunID: "run-1", ProjectID: "project-1", AgentID: "agent-1", ToolName: "read_file", Attributes: map[string]string{"result": "ok"}}
	if !Matches(hook, event) {
		t.Fatal("global hook should match")
	}
	hook.Scope = Scope{Kind: ScopeProject, ID: "other"}
	if Matches(hook, event) {
		t.Fatal("wrong project scope matched")
	}
	hook.Scope.ID = "project-1"
	hook.Filter.ToolNames = []string{"read_*"}
	hook.Filter.Attributes = map[string][]string{"result": {"ok"}}
	if !Matches(hook, event) {
		t.Fatal("filtered hook did not match")
	}
}

func TestStrictModeAndActionValidation(t *testing.T) {
	cases := map[string]Hook{
		"before async":      func() Hook { h := validShellHook(); h.Event = EventRunBefore; h.Mode = ModeAsync; return h }(),
		"async fail run":    func() Hook { h := validShellHook(); h.FailurePolicy = FailureFailRun; return h }(),
		"absolute cwd":      func() Hook { h := validShellHook(); h.Action.Shell.CWD = "C:\\workspace"; return h }(),
		"unix absolute cwd": func() Hook { h := validShellHook(); h.Action.Shell.CWD = "/workspace"; return h }(),
		"workspace escape":  func() Hook { h := validShellHook(); h.Action.Shell.CWD = "../outside"; return h }(),
		"background":        func() Hook { h := validShellHook(); h.Action.Shell.Detached = true; return h }(),
		"shell interpreter": func() Hook { h := validShellHook(); h.Action.Shell.Executable = "bash"; return h }(),
		"duplicate env case": func() Hook {
			h := validShellHook()
			h.Action.Shell.Env = map[string]string{"TOKEN": "one", "token": "two"}
			return h
		}(),
		"env secret overlap": func() Hook {
			h := validShellHook()
			h.Action.Shell.Env = map[string]string{"TOKEN": "one"}
			h.Action.Shell.SecretRefs = map[string]string{"token": "env:HOOK_TOKEN"}
			return h
		}(),
	}
	for name, hook := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeAndValidateHook(hook); err == nil {
				t.Fatal("invalid hook accepted")
			}
		})
	}
}

func TestHTTPAndSecretReferenceValidation(t *testing.T) {
	base := Hook{ID: "http", Name: "webhook", Enabled: true, Event: EventRunAfter, Scope: Scope{Kind: ScopeGlobal}, Mode: ModeAsync, FailurePolicy: FailureContinue, Action: Action{Kind: ActionHTTP, HTTP: &HTTPAction{URL: "https://hooks.example.test/event", Method: "POST", SecretRefs: map[string]string{"Authorization": "env:HOOK_TOKEN"}}}}
	if _, err := NormalizeAndValidateHook(base); err != nil {
		t.Fatal(err)
	}
	invalid := []string{"ftp://example.test/a", "https://user:pass@example.test/a", "https://example.test/a#fragment", "http://example.test/a"}
	for _, target := range invalid {
		h := base
		copy := *base.Action.HTTP
		copy.URL = target
		h.Action.HTTP = &copy
		if _, err := NormalizeAndValidateHook(h); err == nil {
			t.Fatalf("accepted %s", target)
		}
	}
	h := base
	copy := *base.Action.HTTP
	copy.SecretRefs = map[string]string{"Authorization": "plaintext-token"}
	h.Action.HTTP = &copy
	if _, err := NormalizeAndValidateHook(h); err == nil {
		t.Fatal("plaintext secret accepted")
	}
	for _, unsupported := range []string{"secret:HOOK_TOKEN", "vault:HOOK_TOKEN", "keychain:HOOK_TOKEN"} {
		h := base
		copy := *base.Action.HTTP
		copy.SecretRefs = map[string]string{"Authorization": unsupported}
		h.Action.HTTP = &copy
		if _, err := NormalizeAndValidateHook(h); err == nil {
			t.Fatalf("unsupported secret reference accepted: %s", unsupported)
		}
	}
	for _, header := range []string{"Host", "Content-Length", "Transfer-Encoding"} {
		h := base
		copy := *base.Action.HTTP
		copy.Headers = map[string]string{header: "unsafe"}
		h.Action.HTTP = &copy
		if _, err := NormalizeAndValidateHook(h); err == nil {
			t.Fatalf("reserved HTTP header accepted: %s", header)
		}
	}
	for name, headers := range map[string]map[string]string{
		"invalid name":   {"Bad Header": "value"},
		"invalid value":  {"X-Test": "bad\nvalue"},
		"duplicate case": {"X-Test": "one", "x-test": "two"},
	} {
		h := base
		copy := *base.Action.HTTP
		copy.Headers = headers
		h.Action.HTTP = &copy
		if _, err := NormalizeAndValidateHook(h); err == nil {
			t.Fatalf("%s HTTP headers accepted: %+v", name, headers)
		}
	}
	h = base
	copy = *base.Action.HTTP
	copy.SecretRefs = map[string]string{"Accept": "env:HOOK_TOKEN"}
	h.Action.HTTP = &copy
	if _, err := NormalizeAndValidateHook(h); err == nil {
		t.Fatal("non-sensitive, non-X secret header accepted")
	}
}

func TestSnapshotIsStableAndImmutable(t *testing.T) {
	h1 := validShellHook()
	h1.ID = "b"
	h1.Priority = 1
	h2 := validShellHook()
	h2.ID = "a"
	h2.Priority = 10
	now := time.Date(2026, 7, 24, 1, 2, 3, 0, time.UTC)
	snapshot, err := NewSnapshot([]Hook{h1, h2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Hooks[0].ID != "a" || len(snapshot.Digest) != 64 {
		t.Fatalf("bad snapshot: %+v", snapshot)
	}
	h2.Name = "mutated"
	if snapshot.Hooks[0].Name == "mutated" {
		t.Fatal("snapshot shared caller memory")
	}
}

func TestExecutorOnlyUsesGatewayAndCanonicalStdin(t *testing.T) {
	hook := validShellHook()
	var received ShellRequest
	executor := Executor{Gateway: fakeGateway{shell: func(request ShellRequest) (GatewayResult, error) {
		received = request
		return GatewayResult{Output: []byte(`{"token":"secret-value","ok":true}`)}, nil
	}}, Limiter: NewLimiter(2, 1)}
	event := Event{ID: "event-1", Name: EventToolAfter, RunID: "run-1", ToolName: "read", OccurredAt: time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC), Payload: map[string]json.RawMessage{"input": json.RawMessage(`{"path":"README.md"}`)}}
	result, err := executor.Execute(context.Background(), hook, event)
	if err != nil {
		t.Fatal(err)
	}
	if received.Executable != "audit-helper" || received.CWD != "tools/hooks" {
		t.Fatalf("gateway request: %+v", received)
	}
	var stdin CanonicalEventStdin
	if err := json.Unmarshal(received.Stdin, &stdin); err != nil {
		t.Fatal(err)
	}
	if stdin.SchemaVersion != 1 || stdin.Event != EventToolAfter || stdin.RunID != "run-1" {
		t.Fatalf("stdin=%+v", stdin)
	}
	if strings.Contains(string(result.Output), "secret-value") {
		t.Fatalf("secret leaked: %s", result.Output)
	}
}

func TestLLMGateStrictJSON(t *testing.T) {
	for _, raw := range []string{`{"decision":"allow","extra":true}`, `{"decision":"maybe"}`, `{"decision":"allow"} trailing`, `[]`} {
		if _, err := ParseGateDecision([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	decision, err := ParseGateDecision([]byte(`{"decision":"deny","reason":"token=abc123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != "deny" || strings.Contains(decision.Reason, "abc123") {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestLimiterRecursionAndConcurrency(t *testing.T) {
	limiter := NewLimiter(1, 1)
	ctx, release, err := limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, _, err := limiter.Acquire(ctx); err == nil {
		t.Fatal("recursive acquire succeeded")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := limiter.Acquire(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
