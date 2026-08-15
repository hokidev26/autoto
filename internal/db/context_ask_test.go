package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestReadOwnedChildContextSnapshotReturnsAuthorizedStableProjection(t *testing.T) {
	ctx := context.Background()
	store, _, workline, owner := openContextAskTestStore(t, "authorized.db")
	child := createContextAskTestAgent(t, store, workline.ID, "child", owner.ID, "subagent", "idle", "Child worker", "SYSTEM_PROMPT_SECRET")
	outsider := createContextAskTestAgent(t, store, workline.ID, "outsider", owner.ID, "subagent", "idle", "Sibling", "")

	insertContextAskRun(t, store, "run-old", child.ID, "completed", 1, "2026-01-01T00:00:00Z")
	insertContextAskRun(t, store, "run-new", child.ID, "completed", 2, "2026-01-02T00:00:00Z")
	insertContextAskTask(t, store, "task", owner.ID, BackgroundTaskKindAgent, child.ID)
	mustContextAskExec(t, store, `UPDATE agents SET context_summary = ?, status = 'idle', updated_at = ? WHERE id = ?`, "durable child summary", "2026-01-03T00:00:00Z", child.ID)

	insertContextAskMessage(t, store, "message-old", child.ID, "run-old", "assistant", []byte(`{"type":"text","text":"old"}`), []byte("old"), []byte("PROVIDER_STATE_SECRET"), "2026-01-01T00:00:00Z")
	insertContextAskMessage(t, store, "message-b", child.ID, "run-new", "assistant", []byte(`{"type":"text","text":"b"}`), []byte("b"), []byte("PROVIDER_STATE_SECRET"), "2026-01-02T00:00:00Z")
	insertContextAskMessage(t, store, "message-a", child.ID, "run-new", "assistant", []byte(`{"type":"text","text":"a"}`), []byte("a"), []byte("PROVIDER_STATE_SECRET"), "2026-01-02T00:00:00Z")
	insertContextAskMessage(t, store, "outsider-message", outsider.ID, "", "assistant", []byte(`"outsider"`), []byte("outsider"), nil, "2026-01-04T00:00:00Z")

	insertContextAskToolCall(t, store, "call-old", child.ID, "run-new", "tool-old", []byte(`{"path":"old"}`), []byte(`{"result":"old"}`), "2026-01-01T00:00:00Z")
	insertContextAskToolCall(t, store, "call-b", child.ID, "run-new", "tool-b", []byte(`{"path":"b"}`), []byte(`{"result":"b"}`), "2026-01-02T00:00:00Z")
	insertContextAskToolCall(t, store, "call-a", child.ID, "run-new", "tool-a", []byte(`{"path":"a"}`), []byte(`{"result":"a"}`), "2026-01-02T00:00:00Z")
	insertContextAskToolCall(t, store, "other-run-call", child.ID, "run-old", "tool-other", []byte(`{"path":"other"}`), []byte(`{"result":"other"}`), "2026-01-05T00:00:00Z")
	insertContextAskToolCall(t, store, "outsider-call", outsider.ID, "", "tool-outsider", nil, []byte(`"outsider"`), "2026-01-05T00:00:00Z")

	snapshot, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{
		MessageLimit:    2,
		ToolCallLimit:   2,
		MaxContentBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TaskID != "task" || snapshot.OwnerAgentID != owner.ID || snapshot.ChildAgentID != child.ID || snapshot.ChildAgentName != "Child worker" || snapshot.ChildAgentStatus != "idle" || snapshot.ContextSummary != "durable child summary" {
		t.Fatalf("unexpected identity projection: %+v", snapshot)
	}
	if snapshot.RunID != "run-new" || snapshot.RunStatus != "completed" {
		t.Fatalf("latest child run was not selected: %+v", snapshot)
	}
	if got := contextAskMessageIDs(snapshot.Messages); !reflect.DeepEqual(got, []string{"message-a", "message-b"}) {
		t.Fatalf("messages should contain the latest bounded window in ascending order, got %v", got)
	}
	if snapshot.DurableThroughMessageID != "message-b" {
		t.Fatalf("durable through message = %q, want message-b", snapshot.DurableThroughMessageID)
	}
	if got := contextAskToolCallIDs(snapshot.ToolCalls); !reflect.DeepEqual(got, []string{"call-a", "call-b"}) {
		t.Fatalf("tool calls should contain only the selected run in ascending order, got %v", got)
	}
	if !snapshot.Partial {
		t.Fatal("row limits should mark the snapshot partial")
	}
	if len(snapshot.Digest) != sha256.Size*2 {
		t.Fatalf("digest length = %d, want %d", len(snapshot.Digest), sha256.Size*2)
	}
	withoutDigest := snapshot
	withoutDigest.Digest = ""
	digestInput, err := json.Marshal(withoutDigest)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(digestInput)
	if snapshot.Digest != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("digest mismatch: got %s want %x", snapshot.Digest, wantDigest)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SYSTEM_PROMPT_SECRET", "PROVIDER_STATE_SECRET", "outsider-message", "outsider-call", "other-run-call"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("snapshot leaked %q: %s", secret, encoded)
		}
	}

	complete, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{MessageLimit: 10, ToolCallLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if complete.Partial {
		t.Fatalf("idle child with terminal run and complete windows should not be partial: %+v", complete)
	}
	if got := contextAskMessageIDs(complete.Messages); !reflect.DeepEqual(got, []string{"message-a", "message-b"}) {
		t.Fatalf("selected-run messages must not include older runs: %v", got)
	}
	if got := contextAskToolCallIDs(complete.ToolCalls); !reflect.DeepEqual(got, []string{"call-old", "call-a", "call-b"}) {
		t.Fatalf("unexpected complete tool order: %v", got)
	}
	explicit, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{RunID: "run-new", MessageLimit: 10, ToolCallLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.ContextSummary != "" {
		t.Fatalf("run-specific snapshot mixed agent context summary: %q", explicit.ContextSummary)
	}
	if got := contextAskMessageIDs(explicit.Messages); !reflect.DeepEqual(got, []string{"message-a", "message-b"}) {
		t.Fatalf("run-specific snapshot mixed messages from another run: %v", got)
	}

	repeated, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{MessageLimit: 10, ToolCallLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Digest != complete.Digest {
		t.Fatalf("unchanged durable snapshot digest changed: %s != %s", repeated.Digest, complete.Digest)
	}
}

func TestReadOwnedChildContextSnapshotRejectsEveryRelationshipMismatch(t *testing.T) {
	ctx := context.Background()
	store, _, workline, owner := openContextAskTestStore(t, "authorization.db")
	otherOwner := createContextAskTestAgent(t, store, workline.ID, "other-owner", "", "primary", "idle", "Other owner", "")
	child := createContextAskTestAgent(t, store, workline.ID, "child", owner.ID, "subagent", "idle", "Child", "")
	sibling := createContextAskTestAgent(t, store, workline.ID, "sibling", owner.ID, "subagent", "idle", "Sibling", "")
	grandchild := createContextAskTestAgent(t, store, workline.ID, "grandchild", child.ID, "subagent", "idle", "Grandchild", "")
	primaryTarget := createContextAskTestAgent(t, store, workline.ID, "primary-target", "", "primary", "idle", "Primary target", "")
	wrongParent := createContextAskTestAgent(t, store, workline.ID, "wrong-parent", otherOwner.ID, "subagent", "idle", "Wrong parent", "")
	otherChild := createContextAskTestAgent(t, store, workline.ID, "other-child", otherOwner.ID, "subagent", "idle", "Other child", "")

	insertContextAskRun(t, store, "child-run", child.ID, "completed", 1, "2026-01-01T00:00:00Z")
	insertContextAskRun(t, store, "sibling-run", sibling.ID, "completed", 1, "2026-01-01T00:00:00Z")
	insertContextAskRun(t, store, "other-run", otherChild.ID, "completed", 1, "2026-01-01T00:00:00Z")
	insertContextAskRun(t, store, "owner-run", owner.ID, "completed", 1, "2026-01-01T00:00:00Z")

	insertContextAskTask(t, store, "valid-task", owner.ID, BackgroundTaskKindAgent, child.ID)
	insertContextAskTask(t, store, "grandchild-task", owner.ID, BackgroundTaskKindAgent, grandchild.ID)
	insertContextAskTask(t, store, "self-task", owner.ID, BackgroundTaskKindAgent, owner.ID)
	insertContextAskTask(t, store, "primary-task", owner.ID, BackgroundTaskKindAgent, primaryTarget.ID)
	insertContextAskTask(t, store, "wrong-parent-task", owner.ID, BackgroundTaskKindAgent, wrongParent.ID)
	insertContextAskTask(t, store, "shell-task", owner.ID, BackgroundTaskKindShell, child.ID)
	insertContextAskTask(t, store, "other-owner-task", otherOwner.ID, BackgroundTaskKindAgent, otherChild.ID)
	insertContextAskTask(t, store, "empty-child-task", owner.ID, BackgroundTaskKindAgent, "")

	cases := []struct {
		name   string
		owner  string
		task   string
		option ChildContextSnapshotOptions
	}{
		{name: "cross parent", owner: otherOwner.ID, task: "valid-task"},
		{name: "sibling cannot act as parent", owner: sibling.ID, task: "valid-task"},
		{name: "grandchild is not direct", owner: owner.ID, task: "grandchild-task"},
		{name: "self", owner: owner.ID, task: "self-task"},
		{name: "primary target", owner: owner.ID, task: "primary-task"},
		{name: "task child and parent child disagree", owner: owner.ID, task: "wrong-parent-task"},
		{name: "shell task", owner: owner.ID, task: "shell-task"},
		{name: "other owner task", owner: owner.ID, task: "other-owner-task"},
		{name: "empty child", owner: owner.ID, task: "empty-child-task"},
		{name: "missing task", owner: owner.ID, task: "missing"},
		{name: "blank owner", owner: "", task: "valid-task"},
		{name: "run belongs to owner", owner: owner.ID, task: "valid-task", option: ChildContextSnapshotOptions{RunID: "owner-run"}},
		{name: "run belongs to sibling", owner: owner.ID, task: "valid-task", option: ChildContextSnapshotOptions{RunID: "sibling-run"}},
		{name: "run belongs to other child", owner: owner.ID, task: "valid-task", option: ChildContextSnapshotOptions{RunID: "other-run"}},
		{name: "missing run", owner: owner.ID, task: "valid-task", option: ChildContextSnapshotOptions{RunID: "missing-run"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.ReadOwnedChildContextSnapshot(ctx, test.owner, test.task, test.option)
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("error = %v, want sql.ErrNoRows", err)
			}
		})
	}

	selected, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "valid-task", ChildContextSnapshotOptions{RunID: "child-run"})
	if err != nil {
		t.Fatalf("owned child run should be selectable: %v", err)
	}
	if selected.RunID != "child-run" {
		t.Fatalf("selected run = %q", selected.RunID)
	}

	mustContextAskExec(t, store, `UPDATE background_tasks SET parent_run_id = ?, child_run_id = ? WHERE id = ?`, "owner-run", "child-run", "valid-task")
	bound, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "valid-task", ChildContextSnapshotOptions{ParentRunID: "owner-run", RunID: "child-run"})
	if err != nil || bound.ParentRunID != "owner-run" || bound.TaskChildRunID != "child-run" {
		t.Fatalf("valid task run binding failed: snapshot=%+v err=%v", bound, err)
	}
	for _, option := range []ChildContextSnapshotOptions{
		{ParentRunID: "different-parent-run", RunID: "child-run"},
		{ParentRunID: "owner-run", RunID: "different-child-run"},
	} {
		if _, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "valid-task", option); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("task run binding mismatch error = %v, want sql.ErrNoRows for %+v", err, option)
		}
	}
}

func TestReadOwnedChildContextSnapshotReturnsMessagesWhenChildHasNoRun(t *testing.T) {
	ctx := context.Background()
	store, _, workline, owner := openContextAskTestStore(t, "no-run.db")
	child := createContextAskTestAgent(t, store, workline.ID, "child", owner.ID, "subagent", "idle", "Child", "")
	insertContextAskTask(t, store, "task", owner.ID, BackgroundTaskKindAgent, child.ID)
	insertContextAskMessage(t, store, "message", child.ID, "", "assistant", []byte(`{"text":"durable"}`), []byte("durable"), nil, "2026-01-01T00:00:00Z")

	snapshot, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RunID != "" || snapshot.RunStatus != "" {
		t.Fatalf("child without runs should have an empty run projection: %+v", snapshot)
	}
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].ID != "message" || snapshot.DurableThroughMessageID != "message" {
		t.Fatalf("child messages should remain available without a run: %+v", snapshot)
	}
	if snapshot.Partial {
		t.Fatalf("idle child with a complete no-run snapshot should not be partial: %+v", snapshot)
	}
}

func TestReadOwnedChildContextSnapshotBoundsSortingUTF8AndContentBudget(t *testing.T) {
	ctx := context.Background()
	store, _, workline, owner := openContextAskTestStore(t, "bounds.db")
	child := createContextAskTestAgent(t, store, workline.ID, "child", owner.ID, "subagent", "running", "Child", "")
	insertContextAskRun(t, store, "run", child.ID, "running", 1, "2026-01-01T00:00:00Z")
	insertContextAskTask(t, store, "task", owner.ID, BackgroundTaskKindAgent, child.ID)

	large := bytes.Repeat([]byte("x"), maxChildContextItemContentBytes+1024)
	insertContextAskMessage(t, store, "message-old", child.ID, "run", "assistant", nil, []byte("old"), nil, "2026-01-01T00:00:00Z")
	insertContextAskMessage(t, store, "message-z", child.ID, "run", "assistant", nil, large, nil, "2026-01-02T00:00:00Z")
	insertContextAskMessage(t, store, "message-a", child.ID, "run", "assistant", []byte{0xff, '{'}, []byte{'a', 0xff, 'b'}, nil, "2026-01-02T00:00:00Z")
	insertContextAskToolCall(t, store, "call-z", child.ID, "run", "tool-z", []byte(`{"value":"z"}`), []byte{0xff, 'z'}, "2026-01-02T00:00:00Z")
	insertContextAskToolCall(t, store, "call-a", child.ID, "run", "tool-a", []byte(`{"value":"a"}`), bytes.Repeat([]byte("y"), maxChildContextItemContentBytes+1024), "2026-01-02T00:00:00Z")

	window, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{MessageLimit: 2, ToolCallLimit: 1, MaxContentBytes: maxChildContextContentBytes})
	if err != nil {
		t.Fatal(err)
	}
	if got := contextAskMessageIDs(window.Messages); !reflect.DeepEqual(got, []string{"message-a", "message-z"}) {
		t.Fatalf("same-time messages must sort by ID ascending after selecting the latest window, got %v", got)
	}
	if got := contextAskToolCallIDs(window.ToolCalls); !reflect.DeepEqual(got, []string{"call-z"}) {
		t.Fatalf("tool call latest window/sort mismatch: %v", got)
	}
	for _, message := range window.Messages {
		if !utf8.ValidString(message.ContentText) || !utf8.Valid(message.ContentJSON) {
			t.Fatalf("message content is not UTF-8: %+v", message)
		}
		if len(message.ContentText)+len(message.ContentJSON)+len(message.CommandText) > maxChildContextItemContentBytes {
			t.Fatalf("message exceeded per-item content bound: %d", len(message.ContentText)+len(message.ContentJSON)+len(message.CommandText))
		}
	}
	for _, call := range window.ToolCalls {
		if !utf8.Valid(call.InputJSON) || !utf8.Valid(call.OutputJSON) || !utf8.ValidString(call.ErrorMessage) {
			t.Fatalf("tool content is not UTF-8: %+v", call)
		}
		if len(call.InputJSON)+len(call.OutputJSON)+len(call.ErrorMessage) > maxChildContextItemContentBytes {
			t.Fatalf("tool call exceeded per-item content bound: %d", len(call.InputJSON)+len(call.OutputJSON)+len(call.ErrorMessage))
		}
	}

	budgeted, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{MessageLimit: 3, ToolCallLimit: 2, MaxContentBytes: 17})
	if err != nil {
		t.Fatal(err)
	}
	if total := childContextSnapshotContentBytes(budgeted); total > 17 {
		t.Fatalf("snapshot content bytes = %d, budget = 17", total)
	}
	if !budgeted.Partial {
		t.Fatal("content truncation and live child/run should mark snapshot partial")
	}
	for _, message := range budgeted.Messages {
		if !utf8.ValidString(message.ContentText) || !utf8.Valid(message.ContentJSON) || !utf8.ValidString(message.CommandText) {
			t.Fatalf("budgeted message is not UTF-8: %+v", message)
		}
		if len(message.ContentJSON) > 0 && !json.Valid(message.ContentJSON) {
			t.Fatalf("bounded message JSON is invalid: %q", message.ContentJSON)
		}
	}
	for _, call := range budgeted.ToolCalls {
		for name, raw := range map[string]json.RawMessage{"input": call.InputJSON, "output": call.OutputJSON} {
			if !utf8.Valid(raw) || len(raw) > 0 && !json.Valid(raw) {
				t.Fatalf("bounded tool %s JSON is invalid UTF-8/JSON: %q", name, raw)
			}
		}
	}

	for _, option := range []ChildContextSnapshotOptions{
		{ParentRunID: "parent-without-child-run"},
		{MessageLimit: -1},
		{MessageLimit: maxChildContextMessageLimit + 1},
		{ToolCallLimit: -1},
		{ToolCallLimit: maxChildContextToolCallLimit + 1},
		{MaxContentBytes: -1},
		{MaxContentBytes: maxChildContextContentBytes + 1},
	} {
		if _, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", option); err == nil {
			t.Fatalf("invalid options should fail: %+v", option)
		}
	}
}

func TestReadOwnedChildContextSnapshotDoesNotMutateDurableState(t *testing.T) {
	ctx := context.Background()
	store, _, workline, owner := openContextAskTestStore(t, "readonly.db")
	child := createContextAskTestAgent(t, store, workline.ID, "child", owner.ID, "subagent", "idle", "Child", "SYSTEM_SECRET")
	insertContextAskRun(t, store, "run", child.ID, "completed", 1, "2026-01-01T00:00:00Z")
	insertContextAskTask(t, store, "task", owner.ID, BackgroundTaskKindAgent, child.ID)
	insertContextAskMessage(t, store, "message", child.ID, "run", "assistant", []byte(`{"text":"message"}`), []byte("message"), []byte("PROVIDER_SECRET"), "2026-01-01T00:00:00Z")
	insertContextAskToolCall(t, store, "call", child.ID, "run", "tool", []byte(`{"input":true}`), []byte(`{"output":true}`), "2026-01-01T00:00:00Z")
	mustContextAskExec(t, store, `UPDATE agents SET context_summary = 'summary', updated_at = '2026-01-05T00:00:00Z' WHERE id = ?`, child.ID)

	before := readContextAskDurableState(t, store, child.ID, "run", "message", "call")
	if _, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	after := readContextAskDurableState(t, store, child.ID, "run", "message", "call")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("snapshot read mutated durable state\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestReadOwnedChildContextSnapshotIsSelfConsistentDuringConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	store, path, workline, owner := openContextAskTestStore(t, "concurrent.db")
	child := createContextAskTestAgent(t, store, workline.ID, "child", owner.ID, "subagent", "idle", "Child", "")
	insertContextAskRun(t, store, "run", child.ID, "completed", 1, "2026-01-01T00:00:00Z")
	insertContextAskTask(t, store, "task", owner.ID, BackgroundTaskKindAgent, child.ID)
	insertContextAskMessage(t, store, "message", child.ID, "run", "assistant", []byte(`"A"`), []byte("A"), nil, "2026-01-01T00:00:00Z")
	insertContextAskToolCall(t, store, "call", child.ID, "run", "tool", []byte(`"input"`), []byte(`"A"`), "2026-01-01T00:00:00Z")
	mustContextAskExec(t, store, `UPDATE agents SET context_summary = 'A', status = 'idle', updated_at = '2026-01-01T00:00:00Z' WHERE id = ?`, child.ID)
	if _, err := store.DB().ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}

	writer, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	if _, err := writer.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 1)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		<-start
		for index := 0; index < 80; index++ {
			version := "A"
			agentStatus := "idle"
			runStatus := "completed"
			if index%2 == 1 {
				version = "B"
				agentStatus = "running"
				runStatus = "running"
			}
			tx, err := writer.BeginTx(ctx, nil)
			if err != nil {
				errs <- err
				return
			}
			stamp := fmt.Sprintf("2026-02-01T00:00:%02dZ", index%60)
			if _, err = tx.ExecContext(ctx, `UPDATE agents SET context_summary = ?, status = ?, updated_at = ? WHERE id = ?`, version, agentStatus, stamp, child.ID); err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`, runStatus, stamp, "run")
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE agent_messages SET content_json = ?, content_text = ? WHERE id = ?`, fmt.Sprintf("%q", version), version, "message")
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE agent_tool_calls SET output_json = ?, updated_at = ? WHERE id = ?`, fmt.Sprintf("%q", version), stamp, "call")
			}
			if err != nil {
				_ = tx.Rollback()
				errs <- err
				return
			}
			if err := tx.Commit(); err != nil {
				errs <- err
				return
			}
		}
	}()
	close(start)

	for index := 0; index < 80; index++ {
		snapshot, err := store.ReadOwnedChildContextSnapshot(ctx, owner.ID, "task", ChildContextSnapshotOptions{MessageLimit: 10, ToolCallLimit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Messages) != 1 || len(snapshot.ToolCalls) != 1 {
			t.Fatalf("unexpected concurrent snapshot shape: %+v", snapshot)
		}
		version := snapshot.ContextSummary
		switch version {
		case "A":
			if snapshot.ChildAgentStatus != "idle" || snapshot.RunStatus != "completed" || snapshot.Messages[0].ContentText != "A" || string(snapshot.ToolCalls[0].OutputJSON) != `"A"` || snapshot.Partial {
				t.Fatalf("mixed version A snapshot: %+v", snapshot)
			}
		case "B":
			if snapshot.ChildAgentStatus != "running" || snapshot.RunStatus != "running" || snapshot.Messages[0].ContentText != "B" || string(snapshot.ToolCalls[0].OutputJSON) != `"B"` || !snapshot.Partial {
				t.Fatalf("mixed version B snapshot: %+v", snapshot)
			}
		default:
			t.Fatalf("unknown concurrent snapshot version %q: %+v", version, snapshot)
		}
	}
	wait.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

type contextAskDurableState struct {
	AgentStatus   string
	Summary       string
	AgentUpdated  string
	MessageCount  int
	MessageJSON   string
	MessageText   string
	ProviderState string
	MessageAt     string
	RunStatus     string
	RunUpdated    string
	ToolInput     string
	ToolOutput    string
	ToolStatus    string
	ToolUpdated   string
}

func readContextAskDurableState(t *testing.T, store *Store, childID, runID, messageID, callID string) contextAskDurableState {
	t.Helper()
	ctx := context.Background()
	var state contextAskDurableState
	if err := store.DB().QueryRowContext(ctx, `SELECT status, COALESCE(context_summary,''), updated_at, message_count FROM agents WHERE id = ?`, childID).Scan(&state.AgentStatus, &state.Summary, &state.AgentUpdated, &state.MessageCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COALESCE(content_json,''), COALESCE(content_text,''), COALESCE(provider_state_json,''), created_at FROM agent_messages WHERE id = ?`, messageID).Scan(&state.MessageJSON, &state.MessageText, &state.ProviderState, &state.MessageAt); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT status, updated_at FROM runs WHERE id = ?`, runID).Scan(&state.RunStatus, &state.RunUpdated); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COALESCE(input_json,''), COALESCE(output_json,''), status, updated_at FROM agent_tool_calls WHERE id = ?`, callID).Scan(&state.ToolInput, &state.ToolOutput, &state.ToolStatus, &state.ToolUpdated); err != nil {
		t.Fatal(err)
	}
	return state
}

func openContextAskTestStore(t *testing.T, filename string) (*Store, string, Workline, Agent) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, filename)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, workline, owner, err := store.CreateProject(ctx, "Context ask", "", dir, "fake:model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	return store, path, workline, owner
}

func createContextAskTestAgent(t *testing.T, store *Store, worklineID, id, parentID, agentType, status, title, systemPrompt string) Agent {
	t.Helper()
	agent, err := store.CreateAgent(context.Background(), Agent{
		ID:             id,
		WorklineID:     worklineID,
		ParentAgentID:  parentID,
		Type:           agentType,
		Title:          title,
		Model:          "fake:model",
		SystemPrompt:   systemPrompt,
		PermissionMode: "acceptEdits",
		Status:         status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func insertContextAskRun(t *testing.T, store *Store, id, agentID, status string, generation int, createdAt string) {
	t.Helper()
	mustContextAskExec(t, store, `INSERT INTO runs (id, agent_id, status, execution_generation, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, id, agentID, status, generation, createdAt, createdAt)
}

func insertContextAskTask(t *testing.T, store *Store, id, ownerID, kind, childID string) {
	t.Helper()
	mustContextAskExec(t, store, `INSERT INTO background_tasks (id, owner_agent_id, kind, status, child_agent_id, created_at, updated_at) VALUES (?, ?, ?, 'queued', NULLIF(?,''), '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, ownerID, kind, childID)
}

func insertContextAskMessage(t *testing.T, store *Store, id, agentID, runID, role string, contentJSON, contentText, providerState []byte, createdAt string) {
	t.Helper()
	mustContextAskExec(t, store, `INSERT INTO agent_messages (id, agent_id, run_id, role, content_json, provider_state_json, content_text, created_at) VALUES (?, ?, NULLIF(?,''), ?, ?, ?, ?, ?)`, id, agentID, runID, role, contentJSON, providerState, contentText, createdAt)
}

func insertContextAskToolCall(t *testing.T, store *Store, id, agentID, runID, toolUseID string, inputJSON, outputJSON []byte, createdAt string) {
	t.Helper()
	mustContextAskExec(t, store, `INSERT INTO agent_tool_calls (id, agent_id, run_id, tool_use_id, tool_name, input_json, output_json, status, created_at, updated_at) VALUES (?, ?, NULLIF(?,''), ?, 'Read', ?, ?, 'completed', ?, ?)`, id, agentID, runID, toolUseID, inputJSON, outputJSON, createdAt, createdAt)
}

func mustContextAskExec(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.DB().ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func contextAskMessageIDs(messages []AgentMessage) []string {
	ids := make([]string, len(messages))
	for index := range messages {
		ids[index] = messages[index].ID
	}
	return ids
}

func contextAskToolCallIDs(calls []AgentToolCall) []string {
	ids := make([]string, len(calls))
	for index := range calls {
		ids[index] = calls[index].ID
	}
	return ids
}

func childContextSnapshotContentBytes(snapshot ChildContextSnapshot) int {
	total := len(snapshot.ContextSummary)
	for _, message := range snapshot.Messages {
		total += len(message.ContentJSON) + len(message.ContentText) + len(message.CommandText)
	}
	for _, call := range snapshot.ToolCalls {
		total += len(call.InputJSON) + len(call.OutputJSON) + len(call.ErrorMessage)
	}
	return total
}

func TestChildContextSnapshotOptionDefaultsRemainWithinPublishedBounds(t *testing.T) {
	options, err := normalizeChildContextSnapshotOptions(ChildContextSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if options.MessageLimit < 1 || options.MessageLimit > maxChildContextMessageLimit || options.ToolCallLimit < 1 || options.ToolCallLimit > maxChildContextToolCallLimit || options.MaxContentBytes < 1 || options.MaxContentBytes > maxChildContextContentBytes {
		t.Fatalf("invalid normalized defaults: %+v", options)
	}
	if strings.TrimSpace(options.RunID) != "" {
		t.Fatalf("unexpected default run ID %q", options.RunID)
	}
}
