package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/media"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// writeTestPNG writes a tiny real PNG (produced with image/png, not a fixture)
// to name inside dir and returns its encoded bytes.
func writeTestPNG(t *testing.T, dir, name string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// runSingleToolCallSegment drives one continuation segment through a real
// Read tool call and a following text-only turn, mirroring the pattern used
// by TestSafeContinuationAtSegmentTurnLimitRequiresCompleteToolResult.
func runSingleToolCallSegment(t *testing.T, projectDir, readInput string) (*db.Store, db.Agent, *scriptedProvider, segmentOutcome) {
	t.Helper()
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, projectDir, "acceptEdits")
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "img-read", Name: "Read", Input: json.RawMessage(readInput)}}, {Type: "done", Done: true, StopReason: "tool_use"}},
		{{Type: "text", Text: "read complete"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4, AutoContinuationMode: "safe", ContinuationSegmentTurns: 4, MaxContinuations: 2, MaxTotalTurns: 4, MaxRunDurationMs: 60000, MaxRunTokens: 10000})
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "read the file"})
	if err != nil {
		t.Fatal(err)
	}
	runRequest, err := runner.prepareContinuationRun(ctx, db.Run{AgentID: createdAgent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, runRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, createdAgent.ID, trigger.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	state := continuationRunState{run: run, limits: continuationLimits{segmentTurns: 4, maxTotalTurns: 4, maxTokens: 10000}, deadline: time.Now().Add(time.Minute)}
	outcome, err := runner.runContinuationSegment(ctx, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	return store, createdAgent, provider, outcome
}

func TestToolResultImageAttachmentReachesNextTurnAsImageBlock(t *testing.T) {
	projectDir := t.TempDir()
	pngBytes := writeTestPNG(t, projectDir, "photo.png", 2, 2)

	store, createdAgent, provider, outcome := runSingleToolCallSegment(t, projectDir, `{"file_path":"photo.png"}`)
	defer store.Close()

	if outcome.disposition != segmentComplete || provider.requestCount() != 2 {
		t.Fatalf("expected a completed two-turn segment, got %+v requests=%d", outcome, provider.requestCount())
	}

	// The second model turn must see the image as a native image block, not
	// just the tool's text description.
	second := provider.request(1)
	var imageBlock *providers.ContentBlock
	for i, message := range second.Messages {
		for j, block := range message.Blocks {
			if block.Type == "image" {
				imageBlock = &second.Messages[i].Blocks[j]
			}
		}
	}
	if imageBlock == nil {
		t.Fatalf("expected an image content block in the follow-up turn, got messages: %+v", second.Messages)
	}
	if imageBlock.MIMEType != "image/png" || len(imageBlock.Data) == 0 {
		t.Fatalf("image block missing normalized model data: %+v", imageBlock)
	}
	if imageBlock.Width != 2 || imageBlock.Height != 2 {
		t.Fatalf("image block lost decoded dimensions: %+v", imageBlock)
	}

	// The durable attachment itself must be ready and carry the original bytes.
	messages, err := store.ListMessagesWithAttachmentData(context.Background(), createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var toolResultMessage *db.Message
	for i := range messages {
		if messages[i].ParentToolID == "img-read" {
			toolResultMessage = &messages[i]
		}
	}
	if toolResultMessage == nil || len(toolResultMessage.Attachments) != 1 {
		t.Fatalf("expected exactly one attachment on the tool result message, got %+v", toolResultMessage)
	}
	attachment := toolResultMessage.Attachments[0]
	if attachment.Kind != "image" || attachment.ProcessingStatus != media.ProcessingReady {
		t.Fatalf("attachment was not stored as a ready image: %+v", attachment)
	}
	if attachment.AgentID != createdAgent.ID {
		t.Fatalf("attachment not scoped to agent: %+v", attachment)
	}
	if !bytes.Equal(attachment.Data, pngBytes) {
		t.Fatalf("attachment did not preserve the original file bytes")
	}
}

func TestToolResultNonImageCreatesNoAttachment(t *testing.T) {
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "notes.txt", "just some text"); err != nil {
		t.Fatal(err)
	}

	store, createdAgent, _, outcome := runSingleToolCallSegment(t, projectDir, `{"file_path":"notes.txt"}`)
	defer store.Close()

	if outcome.disposition != segmentComplete {
		t.Fatalf("expected segment to complete, got %+v", outcome)
	}
	messages, err := store.ListMessagesWithAttachmentData(context.Background(), createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var toolResultMessage *db.Message
	for i := range messages {
		if messages[i].ParentToolID == "img-read" {
			toolResultMessage = &messages[i]
		}
	}
	if toolResultMessage == nil {
		t.Fatal("expected a tool result message")
	}
	if len(toolResultMessage.Attachments) != 0 {
		t.Fatalf("a plain text tool result must not create an attachment, got %+v", toolResultMessage.Attachments)
	}
	if toolResultMessage.ContentText == "" {
		t.Fatal("text tool result must still be persisted")
	}
}

func TestToolResultImageAttachmentSkipsOversizedImage(t *testing.T) {
	oversized := make([]byte, maxToolResultImageBytes+1)
	rawResult := tools.Result{
		Output: "Image: image/png, 1x1, huge",
		Meta: map[string]any{
			"path": "big.png",
			"image": map[string]any{
				"mimeType": "image/png",
				"data":     base64.StdEncoding.EncodeToString(oversized),
			},
		},
	}
	attachment, ok := toolResultImageAttachment("agent-1", providers.ToolCall{ID: "t1", Name: "Read"}, rawResult)
	if ok {
		t.Fatalf("expected an oversized image to be rejected, got attachment %+v", attachment)
	}
	if rawResult.IsError {
		t.Fatal("rejecting an attachment must never mark the tool result as an error")
	}
}

func TestToolResultImageAttachmentSkipsCorruptPayload(t *testing.T) {
	// Valid base64, but not decodable image bytes at all.
	garbage := []byte("this is not an image")
	corruptResult := tools.Result{
		Meta: map[string]any{
			"image": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(garbage)},
		},
	}
	if attachment, ok := toolResultImageAttachment("agent-1", providers.ToolCall{ID: "t2", Name: "Read"}, corruptResult); ok {
		t.Fatalf("expected corrupt image bytes to be rejected, got %+v", attachment)
	}

	// Invalid base64 entirely.
	badBase64Result := tools.Result{
		Meta: map[string]any{
			"image": map[string]any{"mimeType": "image/png", "data": "not-valid-base64!!"},
		},
	}
	if attachment, ok := toolResultImageAttachment("agent-1", providers.ToolCall{ID: "t3", Name: "Read"}, badBase64Result); ok {
		t.Fatalf("expected invalid base64 to be rejected, got %+v", attachment)
	}

	// No image key at all (ordinary tool result) must also be a clean no-op.
	if attachment, ok := toolResultImageAttachment("agent-1", providers.ToolCall{ID: "t4", Name: "Bash"}, tools.Result{Output: "ok"}); ok {
		t.Fatalf("expected a non-image tool result to be rejected, got %+v", attachment)
	}

	// An error tool result must never produce an attachment either.
	errResult := tools.Result{IsError: true, Meta: map[string]any{
		"image": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(garbage)},
	}}
	if attachment, ok := toolResultImageAttachment("agent-1", providers.ToolCall{ID: "t5", Name: "Read"}, errResult); ok {
		t.Fatalf("expected an error tool result to be rejected, got %+v", attachment)
	}
}
