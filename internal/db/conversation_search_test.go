package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchAgentMessagesFindsCurrentTurnsAndSkipsToolRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, _, visible, err := store.CreateProject(ctx, "Visible", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, hidden, err := store.CreateProject(ctx, "Hidden", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.AddMessage(ctx, Message{AgentID: visible.ID, Role: "user", ContentText: "Please review the dashboard heatmap"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, Message{AgentID: visible.ID, Role: "assistant", ContentText: "The heatmap is on the homepage."}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, Message{AgentID: visible.ID, Role: "user", ParentToolID: "tool-1", ContentText: "heatmap tool dump"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, Message{AgentID: hidden.ID, Role: "user", ContentText: "heatmap should not leak across agents"}); err != nil {
		t.Fatal(err)
	}

	hits, err := store.SearchAgentMessages(ctx, []string{visible.ID}, "heatmap", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected two visible hits, got %+v", hits)
	}
	for _, hit := range hits {
		if hit.AgentID != visible.ID {
			t.Fatalf("search escaped the allowed agent set: %+v", hit)
		}
		if hit.Snippet == "" || strings.Contains(strings.ToLower(hit.Snippet), "tool dump") {
			t.Fatalf("unexpected snippet: %+v", hit)
		}
	}

	none, err := store.SearchAgentMessages(ctx, nil, "heatmap", 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("empty agent set must not query: %v %+v", err, none)
	}
}

func TestSearchAgentMessagesTreatsPercentAsLiteral(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "search-literal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Literal", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "progress 100% complete"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "any other line"}); err != nil {
		t.Fatal(err)
	}
	hits, err := store.SearchAgentMessages(ctx, []string{agent.ID}, "100%", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Snippet, "100%") {
		t.Fatalf("LIKE wildcards must not broaden the query: %+v", hits)
	}
}
