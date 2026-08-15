package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestListTaskWorkspaceIncludesEmptyAgentsAndStatusCounts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "task-workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, workline, agent, err := store.CreateProject(ctx, "Workspace", "secret description", t.TempDir(), "fake:primary", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	emptyAgent, err := store.CreateAgent(ctx, Agent{WorklineID: workline.ID, Type: "primary", Title: "Empty", Model: "fake:empty", PermissionMode: "readOnly", CWD: project.GitPath})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []SpecTask{
		{AgentID: agent.ID, Text: "todo task", Status: "todo"},
		{AgentID: agent.ID, Text: "doing task", Status: "doing"},
		{AgentID: agent.ID, Text: "blocked task", Status: "blocked"},
		{AgentID: agent.ID, Text: "done task", Status: "done"},
		{AgentID: agent.ID, Text: "second done task", Status: "done"},
	} {
		if _, err := store.CreateSpecTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	workspace, err := store.ListTaskWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Projects) != 1 || workspace.Projects[0].ID != project.ID {
		t.Fatalf("unexpected projects: %+v", workspace.Projects)
	}
	if len(workspace.Projects[0].Worklines) != 1 || len(workspace.Projects[0].Agents) != 2 {
		t.Fatalf("unexpected workspace hierarchy: %+v", workspace)
	}
	agents := workspace.Projects[0].Agents
	byID := make(map[string]TaskWorkspaceAgent, len(agents))
	for _, item := range agents {
		byID[item.ID] = item
	}
	primary := byID[agent.ID]
	if len(primary.Tasks) != 5 || primary.Counts != (SpecTaskStatusCounts{Todo: 1, Doing: 1, Blocked: 1, Done: 2, Total: 5}) {
		t.Fatalf("unexpected primary task aggregate: %+v", primary)
	}
	if workspace.Projects[0].Counts != primary.Counts || workspace.Summary.ProjectCount != 1 || workspace.Summary.AgentCount != 2 || workspace.Summary.Total != 5 {
		t.Fatalf("unexpected project or workspace summary: project=%+v summary=%+v", workspace.Projects[0].Counts, workspace.Summary)
	}
	empty := byID[emptyAgent.ID]
	if empty.Tasks == nil || len(empty.Tasks) != 0 || empty.Counts != (SpecTaskStatusCounts{}) || empty.SpecRevision != 0 {
		t.Fatalf("agent without tasks must remain visible with empty task data: %+v", empty)
	}
}

func TestCreateStandaloneConversationIsAtomicAndExcludedFromTaskWorkspace(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "standalone-conversation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user, err := store.CreateUser(ctx, "conversation-owner", "password-hash")
	if err != nil {
		t.Fatal(err)
	}
	project, workline, agent, err := store.CreateStandaloneConversationForUser(ctx, user.ID, "  Research chat  ", "fake:chat")
	if err != nil {
		t.Fatal(err)
	}
	if project.FlowMode != ProjectFlowModeConversation || project.Name != "Research chat" || project.GitPath != "" {
		t.Fatalf("unexpected conversation project: %+v", project)
	}
	if workline.ProjectID != project.ID || workline.WorktreePath != "" || !workline.IsRoot {
		t.Fatalf("unexpected conversation workline: %+v", workline)
	}
	if agent.WorklineID != workline.ID || agent.Type != "primary" || agent.PermissionMode != "readOnly" || agent.CWD != "" || agent.Model != "fake:chat" {
		t.Fatalf("unexpected conversation agent: %+v", agent)
	}
	allowed, err := store.CanAccessAgent(ctx, user.ID, agent.ID)
	if err != nil || !allowed {
		t.Fatalf("conversation owner membership was not created atomically: allowed=%v err=%v", allowed, err)
	}
	flowMode, err := store.GetAgentProjectFlowMode(ctx, agent.ID)
	if err != nil || flowMode != ProjectFlowModeConversation {
		t.Fatalf("unexpected agent project flow mode %q: %v", flowMode, err)
	}
	workspace, err := store.ListTaskWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Projects) != 0 || workspace.Summary.ProjectCount != 0 || workspace.Summary.AgentCount != 0 {
		t.Fatalf("standalone conversation leaked into task workspace: %+v", workspace)
	}
}

func TestAssignSpecTaskMovesWithinProjectAndUpdatesRevisions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "assign-task.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, workline, source, err := store.CreateProject(ctx, "Workspace", "", t.TempDir(), "fake:source", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateAgent(ctx, Agent{WorklineID: workline.ID, Type: "primary", Title: "Target", Model: "fake:target", CWD: project.GitPath})
	if err != nil {
		t.Fatal(err)
	}
	sourceBoard, err := store.CreateSpecTask(ctx, SpecTask{AgentID: source.ID, Text: "keep first", Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	sourceBoard, err = store.CreateSpecTask(ctx, SpecTask{AgentID: source.ID, Text: "move protected", Status: "doing", Protected: true})
	if err != nil {
		t.Fatal(err)
	}
	sourceBoard, err = store.CreateSpecTask(ctx, SpecTask{AgentID: source.ID, Text: "keep last", Status: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	targetBoard, err := store.CreateSpecTask(ctx, SpecTask{AgentID: target.ID, Text: "target existing", Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	moving := sourceBoard.Tasks[1]

	if _, err := store.AssignSpecTask(ctx, source.ID, moving.ID, target.ID, moving.Revision, false, "test"); !errors.Is(err, ErrConflict) {
		t.Fatalf("protected assignment without acknowledgement must conflict, got %v", err)
	}
	if _, err := store.AssignSpecTask(ctx, source.ID, moving.ID, target.ID, moving.Revision+1, true, "test"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale assignment revision must conflict, got %v", err)
	}

	result, err := store.AssignSpecTask(ctx, source.ID, moving.ID, target.ID, moving.Revision, true, "test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.AgentID != target.ID || result.Task.Position != 1 || result.Task.Revision != moving.Revision+1 || result.Task.UpdatedAt == moving.UpdatedAt {
		t.Fatalf("unexpected moved task: before=%+v after=%+v", moving, result.Task)
	}
	if result.SourceBoard.Revision != sourceBoard.Revision+1 || result.TargetBoard.Revision != targetBoard.Revision+1 {
		t.Fatalf("both board revisions must increment: source=%+v target=%+v", result.SourceBoard, result.TargetBoard)
	}
	if len(result.SourceBoard.Tasks) != 2 || result.SourceBoard.Tasks[0].Position != 0 || result.SourceBoard.Tasks[1].Position != 1 || result.SourceBoard.Tasks[0].Text != "keep first" || result.SourceBoard.Tasks[1].Text != "keep last" {
		t.Fatalf("source board was not compacted: %+v", result.SourceBoard.Tasks)
	}
	if len(result.TargetBoard.Tasks) != 2 || result.TargetBoard.Tasks[1].ID != moving.ID {
		t.Fatalf("task was not appended to target board: %+v", result.TargetBoard.Tasks)
	}
}

func TestAssignSpecTaskRejectsCrossProjectTarget(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "cross-project-task.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, _, source, err := store.CreateProject(ctx, "Source", "", t.TempDir(), "fake:source", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, target, err := store.CreateProject(ctx, "Target", "", t.TempDir(), "fake:target", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	board, err := store.CreateSpecTask(ctx, SpecTask{AgentID: source.ID, Text: "do not move", Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignSpecTask(ctx, source.ID, board.Tasks[0].ID, target.ID, board.Tasks[0].Revision, false, "test"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-project assignment must conflict, got %v", err)
	}
	after, err := store.GetSpecBoard(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != board.Revision || len(after.Tasks) != 1 || after.Tasks[0].AgentID != source.ID {
		t.Fatalf("cross-project rejection mutated source board: before=%+v after=%+v", board, after)
	}
}
