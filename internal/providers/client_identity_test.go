package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoto/internal/anthropicauth"
	"autoto/internal/config"
)

func TestWithClientIdentity(t *testing.T) {
	t.Parallel()
	const autotoPrompt = "You are Autoto's agent runtime."
	if got := WithClientIdentity(autotoPrompt, ""); got != autotoPrompt {
		t.Fatalf("default identity changed the prompt: %q", got)
	}
	if got := WithClientIdentity(autotoPrompt, "autoto"); got != autotoPrompt {
		t.Fatalf("autoto identity changed the prompt: %q", got)
	}
	got := WithClientIdentity(autotoPrompt, config.ClientIdentityCodex)
	if !strings.HasPrefix(got, CodexCLIIdentity+"\n\n") || !strings.HasSuffix(got, autotoPrompt) {
		t.Fatalf("codex identity was not prepended: %q", got)
	}
	if again := WithClientIdentity(got, config.ClientIdentityCodex); again != got {
		t.Fatalf("identity prefix should be idempotent:\n%s\n%s", got, again)
	}
	claude := WithClientIdentity(autotoPrompt, config.ClientIdentityClaudeCode)
	if !strings.HasPrefix(claude, anthropicauth.ClaudeCodeIdentity+"\n\n") {
		t.Fatalf("claude-code identity was not prepended: %q", claude)
	}
}

func TestApplyConfiguredClientIdentitySkipsGateway(t *testing.T) {
	t.Parallel()
	cfg := config.ProviderConfig{ClientIdentity: config.ClientIdentityCodex}
	internal := applyConfiguredClientIdentity(cfg, GenerateRequest{SystemPrompt: "Autoto", Scenario: CallScenarioInternal})
	if internal.SystemPrompt == "Autoto" {
		t.Fatal("internal request should receive the configured identity")
	}
	gateway := applyConfiguredClientIdentity(cfg, GenerateRequest{SystemPrompt: "Autoto", Scenario: CallScenarioGateway})
	if gateway.SystemPrompt != "Autoto" {
		t.Fatalf("gateway request must keep the caller's prompt, got %q", gateway.SystemPrompt)
	}
}

func TestOpenAICompatibleMessagesPrependClientIdentity(t *testing.T) {
	t.Parallel()
	req := applyConfiguredClientIdentity(config.ProviderConfig{ClientIdentity: config.ClientIdentityCodex}, GenerateRequest{
		SystemPrompt: "Autoto system prompt",
		Messages:     []Message{{Role: "user", Content: "hello"}},
	})
	messages := openAICompatibleMessages(req)
	if len(messages) == 0 {
		t.Fatal("expected system message")
	}
	content, _ := messages[0]["content"].(string)
	if messages[0]["role"] != "system" || !strings.HasPrefix(content, CodexCLIIdentity+"\n\nAutoto system prompt") {
		t.Fatalf("unexpected compatible messages: %+v", messages)
	}
}

func TestCodexInstructionsPrependClientIdentity(t *testing.T) {
	t.Parallel()
	req := applyConfiguredClientIdentity(config.ProviderConfig{ClientIdentity: config.ClientIdentityCodex}, GenerateRequest{
		SystemPrompt: "be useful",
		Messages:     []Message{{Role: "user", Content: "hello"}},
	})
	payload, err := buildCodexResponsesPayload(req, "gpt-a", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	instructions, _ := payload["instructions"].(string)
	if instructions != CodexCLIIdentity+"\n\nbe useful" {
		t.Fatalf("unexpected Codex instructions: %q", instructions)
	}
}

func TestAnthropicClientIdentityIsFirstSystemBlock(t *testing.T) {
	t.Parallel()
	_, system, _ := anthropicMessages([]Message{{Role: "user", Content: "hello"}}, "Autoto system prompt", "claude-sonnet-4-5")
	system = prependAnthropicClientIdentity(system, ClientIdentityText(config.ClientIdentityClaudeCode))
	if len(system) != 2 || system[0].Text != anthropicauth.ClaudeCodeIdentity || system[1].Text != "Autoto system prompt" {
		t.Fatalf("unexpected Anthropic system blocks: %+v", system)
	}
	again := prependAnthropicClientIdentity(system, ClientIdentityText(config.ClientIdentityClaudeCode))
	if len(again) != 2 || again[0].Text != anthropicauth.ClaudeCodeIdentity {
		t.Fatalf("Anthropic identity should not duplicate: %+v", again)
	}
}

func TestAnthropicGenerateUsesConfiguredClientIdentity(t *testing.T) {
	var system []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Anthropic request: %v", err)
		}
		system = system[:0]
		for _, block := range body.System {
			system = append(system, block.Text)
		}
		writeAnthropicSuccessStream(w, "ok")
	}))
	defer server.Close()

	provider := NewAnthropicProvider(config.ProviderConfig{
		BaseURL:        server.URL,
		APIKey:         "test-key",
		Model:          "claude-sonnet-4-5",
		MaxTokens:      4096,
		ClientIdentity: config.ClientIdentityClaudeCode,
	})
	events, err := provider.Generate(context.Background(), GenerateRequest{
		SystemPrompt: "Autoto system prompt",
		Messages:     []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatal(event.Text)
		}
	}
	if len(system) != 2 || system[0] != anthropicauth.ClaudeCodeIdentity || system[1] != "Autoto system prompt" {
		t.Fatalf("API-key request did not prepend configured identity: %+v", system)
	}
}
