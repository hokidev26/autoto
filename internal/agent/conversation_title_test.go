package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

func TestSanitizeConversationTitle(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain title", raw: "Fix the login redirect", want: "Fix the login redirect"},
		{name: "surrounding quotes", raw: `"Fix the login redirect"`, want: "Fix the login redirect"},
		{name: "backticks and bold", raw: "**`Fix the login redirect`**", want: "Fix the login redirect"},
		{name: "markdown heading", raw: "# Fix the login redirect", want: "Fix the login redirect"},
		{name: "trailing punctuation", raw: "Fix the login redirect.", want: "Fix the login redirect"},
		{name: "cjk decoration", raw: "「修复登录跳转。」", want: "修复登录跳转"},
		{name: "collapsed whitespace", raw: "  Fix   the \t login  redirect  ", want: "Fix the login redirect"},
		{name: "control characters", raw: "Fix\x00the\x07login redirect", want: "Fix the login redirect"},
		{name: "over long", raw: strings.Repeat("ab ", 40), want: truncateRunes(strings.Repeat("ab ", 40), conversationTitleMaxRunes)},
		{name: "multi line rejected", raw: "Fix the login redirect\nAlso update the docs", want: ""},
		{name: "fenced block rejected", raw: "```\nFix the login redirect\n```", want: ""},
		{name: "empty", raw: "   ", want: ""},
		{name: "punctuation only", raw: `"***"`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeConversationTitle(tc.raw); got != tc.want {
				t.Fatalf("sanitizeConversationTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// UpdateAgentTitle rejects anything over 200 bytes, so the rune cap has to hold
// for the widest multi-byte scripts as well.
func TestSanitizeConversationTitleStaysInsideStoreLimit(t *testing.T) {
	title := sanitizeConversationTitle(strings.Repeat("字", 200))
	if title == "" || len(title) > 200 {
		t.Fatalf("sanitized title is unusable for UpdateAgentTitle: %q (%d bytes)", title, len(title))
	}
}

func TestUntitledConversation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
		want  bool
	}{
		{name: "empty", title: "", want: true},
		{name: "blank", title: "   ", want: true},
		{name: "store placeholder", title: "New conversation", want: true},
		{name: "localized placeholder", title: "新建对话", want: true},
		{name: "user title", title: "Payments migration", want: false},
		{name: "project title", title: "Demo", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := untitledConversation(tc.title); got != tc.want {
				t.Fatalf("untitledConversation(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}

func TestAutoTitleConversationSkipsCallsItMustNotMake(t *testing.T) {
	for _, tc := range []struct {
		name         string
		title        string
		summaryModel string
		wantError    bool
	}{
		{name: "already titled", title: "Payments migration", summaryModel: "fake:test"},
		{name: "unconfigured summary model", title: "New conversation", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, agent := newConversationTitleTestStore(t, tc.title)
			defer store.Close()
			provider := newConversationTitleProvider("Generated title")
			runner := newAgentTestRunner(store, provider, config.AgentConfig{SummaryModel: tc.summaryModel})

			err := runner.autoTitleConversation(ctx, agent.ID, "please rename me")
			if (err != nil) != tc.wantError {
				t.Fatalf("auto title returned %v, wantError=%v", err, tc.wantError)
			}
			if provider.requestCount() != 0 {
				t.Fatalf("summary model was called %d times", provider.requestCount())
			}
			current, err := store.GetAgent(ctx, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Title != tc.title {
				t.Fatalf("title changed to %q, want %q", current.Title, tc.title)
			}
		})
	}
}

// scheduleConversationTitle must decide not to work before it starts a
// goroutine, so an unconfigured summary model costs the submission nothing.
func TestScheduleConversationTitleStaysInertWithoutSummaryModel(t *testing.T) {
	store, agent := newConversationTitleTestStore(t, "New conversation")
	defer store.Close()
	provider := newConversationTitleProvider("Generated title")
	runner := newAgentTestRunner(store, provider, config.AgentConfig{})

	runner.scheduleConversationTitle(agent.ID, "please rename me")
	if provider.requestCount() != 0 {
		t.Fatalf("summary model was called %d times", provider.requestCount())
	}
	runner.titlingMu.Lock()
	pending := len(runner.titling)
	runner.titlingMu.Unlock()
	if pending != 0 {
		t.Fatalf("titling was admitted for %d agents", pending)
	}
}

func TestAutoTitleConversationPersistsAndPublishesSanitizedTitle(t *testing.T) {
	ctx := context.Background()
	store, agent := newConversationTitleTestStore(t, "New conversation")
	defer store.Close()
	provider := newConversationTitleProvider("  **\"Fix the login redirect.\"**\t")
	runner := newAgentTestRunner(store, provider, config.AgentConfig{SummaryModel: "fake:test"})
	events := runner.hub.Subscribe(ctx, agent.ID)

	if err := runner.autoTitleConversation(ctx, agent.ID, "the login page redirects in a loop"); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Title != "Fix the login redirect" {
		t.Fatalf("unexpected stored title %q", current.Title)
	}
	if current.EntityGeneration <= agent.EntityGeneration {
		t.Fatalf("entity generation did not advance: %d -> %d", agent.EntityGeneration, current.EntityGeneration)
	}

	request := provider.request(0)
	if request.Scenario != providers.CallScenarioInternal || len(request.Tools) != 0 {
		t.Fatalf("title request was not isolated: %+v", request)
	}
	if !requestHasUserText(request, "the login page redirects in a loop") || !requestHasUserText(request, "<untrusted_context") {
		t.Fatalf("title request did not wrap the user message as untrusted: %+v", request)
	}

	select {
	case event := <-events:
		if event.Type != "agent.title_updated" || event.AgentID != agent.ID || event.Data["title"] != "Fix the login redirect" {
			t.Fatalf("unexpected title event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the title event")
	}
}

func TestAutoTitleConversationLeavesTitleUnsetOnUnusableOutput(t *testing.T) {
	ctx := context.Background()
	store, agent := newConversationTitleTestStore(t, "New conversation")
	defer store.Close()
	provider := newConversationTitleProvider("Sure! Here is a title:\nFix the login redirect")
	runner := newAgentTestRunner(store, provider, config.AgentConfig{SummaryModel: "fake:test"})

	if err := runner.autoTitleConversation(ctx, agent.ID, "the login page redirects in a loop"); err == nil {
		t.Fatal("expected an unusable title to be rejected")
	}
	current, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Title != "New conversation" {
		t.Fatalf("title was overwritten with junk: %q", current.Title)
	}
}

func newConversationTitleProvider(text string) *scriptedProvider {
	return &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: text},
		{Type: "done", Done: true, StopReason: "stop"},
	}}}
}

func newConversationTitleTestStore(t *testing.T, title string) (*db.Store, db.Agent) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, created, err := store.CreateStandaloneConversation(ctx, title, "fake:test")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	// CreateStandaloneConversation returns the struct it inserted, not the row,
	// so the stored defaults (entity generation) are read back explicitly.
	agent, err := store.GetAgent(ctx, created.ID)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, agent
}
