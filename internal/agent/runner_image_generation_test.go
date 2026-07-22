package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/imageassets"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type imageScriptedProvider struct {
	*scriptedProvider
	imageModels map[string]bool
}

func (p *imageScriptedProvider) Capabilities() providers.Capabilities {
	capabilities := p.scriptedProvider.Capabilities()
	capabilities.ImageGeneration = true
	return capabilities
}

func (p *imageScriptedProvider) ModelCapabilities(model string) providers.ModelCapabilities {
	capabilities := p.scriptedProvider.ModelCapabilities(model)
	capabilities.ImageGenerationKnown = true
	capabilities.ImageGeneration = p.imageModels[model]
	return capabilities
}

func TestRunnerEnablesImageGenerationOnlyForCapableChatModel(t *testing.T) {
	provider := &imageScriptedProvider{
		scriptedProvider: &scriptedProvider{turns: [][]providers.Event{
			{{Type: "done", Done: true}},
			{{Type: "done", Done: true}},
			{{Type: "text", Text: "summary"}, {Type: "done", Done: true}},
		}},
		imageModels: map[string]bool{"image": true},
	}
	registry := providers.NewRegistry()
	registry.Register(provider)
	runner := NewRunner(nil, registry, tools.NewRegistry(), NewHub(), config.AgentConfig{SummaryModel: "fake:image"})

	if _, err, _ := runner.runModelTurnAttempt(context.Background(), "agent", "run", provider, "image", "", nil, nil, "auto", false); err != nil {
		t.Fatal(err)
	}
	if _, err, _ := runner.runModelTurnAttempt(context.Background(), "agent", "run", provider, "text", "", nil, nil, "auto", false); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.summarizeWithModel(context.Background(), "", []db.Message{{Role: "user", ContentText: "summarize me"}}); err != nil {
		t.Fatal(err)
	}
	if !provider.request(0).EnableImageGeneration {
		t.Fatalf("capable chat model did not receive image generation: %+v", provider.request(0))
	}
	if provider.request(1).EnableImageGeneration {
		t.Fatalf("incapable chat model received image generation: %+v", provider.request(1))
	}
	if provider.request(2).EnableImageGeneration {
		t.Fatalf("summary request must not enable image generation: %+v", provider.request(2))
	}
}

func TestImageGenerationEventStartsOutputPreventsRetryAndSanitizesWS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := NewHub()
	subscription := hub.Subscribe(ctx, "agent")
	pngData := agentTestPNG(t, 2, 3)
	provider := &imageScriptedProvider{
		scriptedProvider: &scriptedProvider{turns: [][]providers.Event{
			{
				{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{GenerationID: "generation-1", Status: "completed", OutputIndex: 0, RevisedPrompt: strings.Repeat("界", maxImageGenerationEventPromptRunes+50), Data: pngData, MIME: "image/png", Width: 2, Height: 3}},
				{Type: "error", Text: "temporary 500 after image output"},
			},
			{{Type: "done", Done: true}},
		}},
		imageModels: map[string]bool{"image": true},
	}
	runner := &Runner{hub: hub, cfg: config.AgentConfig{MaxTransientRetries: 1, FirstTokenTimeoutMs: 10}}

	result, err := runner.runModelTurn(ctx, "agent", "run", provider, "image", "", nil, nil, "auto", false)
	if err == nil || !strings.Contains(err.Error(), "temporary 500") {
		t.Fatalf("expected provider error after image output, got %v", err)
	}
	if provider.requestCount() != 1 {
		t.Fatalf("image output must prevent transparent retry, got %d requests", provider.requestCount())
	}
	if result.FirstOutputAt.IsZero() || len(result.GeneratedImages) != 1 {
		t.Fatalf("image event did not start and collect model output: %+v", result)
	}

	var status Event
	for {
		select {
		case event := <-subscription:
			if event.Type == "image_generation.status" {
				status = event
			}
		default:
			goto drained
		}
	}

drained:
	if status.Type == "" {
		t.Fatal("missing image_generation.status event")
	}
	allowed := map[string]bool{"requestId": true, "runId": true, "generationId": true, "status": true, "outputIndex": true, "partialIndex": true, "revisedPrompt": true}
	for key := range status.Data {
		if !allowed[key] {
			t.Fatalf("unexpected image status field %q: %+v", key, status.Data)
		}
	}
	if prompt, _ := status.Data["revisedPrompt"].(string); len([]rune(prompt)) > maxImageGenerationEventPromptRunes {
		t.Fatalf("revised prompt was not bounded: %d runes", len([]rune(prompt)))
	}
	encoded := string(status.JSON())
	if strings.Contains(encoded, "iVBOR") || strings.Contains(encoded, "Data") || strings.Contains(encoded, "base64") {
		t.Fatalf("image data leaked into websocket event: %s", encoded)
	}
}

func TestImageGenerationProgressStopsFirstEventTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	provider := &imageThenBlockingProvider{}
	runner := &Runner{cfg: config.AgentConfig{FirstTokenTimeoutMs: 5}}
	_, err, retryable := runner.runModelTurnAttempt(ctx, "agent", "run", provider, "image", "", nil, nil, "auto", false)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "first token timeout") || retryable {
		t.Fatalf("image progress must stop first-event timeout: err=%v retryable=%v", err, retryable)
	}
}

func TestImageGenerationFinalSurvivesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	pngData := agentTestPNG(t, 2, 2)
	provider := &finalImageThenBlockingProvider{data: pngData}
	runner := &Runner{cfg: config.AgentConfig{FirstTokenTimeoutMs: 5}}
	result, err, retryable := runner.runModelTurnAttempt(ctx, "agent", "run", provider, "image", "", nil, nil, "auto", false)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") || retryable {
		t.Fatalf("expected non-retryable cancellation after final image, err=%v retryable=%v", err, retryable)
	}
	if len(result.GeneratedImages) != 1 || !bytes.Equal(result.GeneratedImages[0].Data, pngData) || result.FirstOutputAt.IsZero() {
		t.Fatalf("completed image was lost on cancellation: %+v", result)
	}
}

func TestPrepareGeneratedImagesPublishesSafeMetadataAndHydratesHistory(t *testing.T) {
	ctx := context.Background()
	assets, err := imageassets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{}
	runner.SetGeneratedImageStore(assets)
	pngData := agentTestPNG(t, 4, 5)
	blocks, images, err := runner.prepareGeneratedImages([]providers.ImageGeneration{{GenerationID: "generation-prepared", Status: "completed", OutputIndex: 0, RevisedPrompt: "revised", Data: pngData}}, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || len(images) != 1 || blocks[0].AssetID == "" || blocks[0].AssetID != images[0].ID || blocks[0].Status != "completed" || images[0].Status != "ready" || images[0].Width != 4 || images[0].Height != 5 {
		t.Fatalf("unexpected prepared image data: blocks=%+v images=%+v", blocks, images)
	}
	contentJSON, err := marshalAssistantContentBlocks(blocks)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contentJSON)
	for _, forbidden := range []string{"iVBOR", images[0].SHA256, images[0].StorageKey, assets.Root()} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sensitive asset data %q leaked into content json: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"outputIndex":0`) || !strings.Contains(text, `"assetId":"`+images[0].ID+`"`) {
		t.Fatalf("content json is missing lightweight image metadata: %s", text)
	}
	message := db.Message{Role: "assistant", ContentJSON: contentJSON, GeneratedImages: images}
	hydrated := runner.providerMessageFromDBForContext(ctx, message, false)
	if len(hydrated.Blocks) != 1 || !bytes.Equal(hydrated.Blocks[0].Data, pngData) {
		t.Fatalf("verified generated image history was not hydrated: %+v", hydrated)
	}
	objectPath := filepath.Join(assets.Root(), filepath.FromSlash(images[0].StorageKey))
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	degraded := runner.providerMessageFromDBForContext(ctx, message, false)
	if len(degraded.Blocks) != 1 || degraded.Blocks[0].Type != "text" || !strings.Contains(degraded.Blocks[0].Text, "unavailable") || strings.Contains(degraded.Content, assets.Root()) {
		t.Fatalf("missing image did not degrade safely: %+v", degraded)
	}
}

func TestRunnerPersistsImageOnlyAndHydratesVerifiedHistory(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newImageAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	assets, err := imageassets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pngData := agentTestPNG(t, 4, 5)
	provider := &imageScriptedProvider{
		scriptedProvider: &scriptedProvider{turns: [][]providers.Event{{
			{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{GenerationID: "generation-only", Status: "completed", OutputIndex: 0, RevisedPrompt: "a revised prompt", Data: pngData, MIME: "image/png", Width: 4, Height: 5}},
			{Type: "done", Done: true, StopReason: "end_turn"},
		}}},
		imageModels: map[string]bool{"test": true},
	}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})
	runner.SetGeneratedImageStore(assets)
	if _, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "draw"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.run(ctx, createdAgent.ID, ""); err != nil {
		t.Fatal(err)
	}

	messages, err := store.ListMessagesWithAttachmentData(ctx, createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].ContentText != "" || strings.Contains(messages[1].ContentText, "Done.") {
		t.Fatalf("image-only response should be a real empty-text assistant message: %+v", messages)
	}
	assistant := messages[1]
	if len(assistant.GeneratedImages) != 1 {
		t.Fatalf("generated image metadata missing: %+v", assistant)
	}
	asset := assistant.GeneratedImages[0]
	if asset.Status != "ready" || asset.GenerationID != "generation-only" || asset.Width != 4 || asset.Height != 5 || asset.MIMEType != "image/png" {
		t.Fatalf("unexpected generated image metadata: %+v", asset)
	}
	var blocks []providers.ContentBlock
	if err := json.Unmarshal(assistant.ContentJSON, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Type != "image_generation" || blocks[0].AssetID != asset.ID || blocks[0].Status != "completed" || blocks[0].OutputIndex != 0 || len(blocks[0].Data) != 0 {
		t.Fatalf("unexpected lightweight content blocks: %+v", blocks)
	}
	contentJSON := string(assistant.ContentJSON)
	for _, forbidden := range []string{"iVBOR", asset.SHA256, asset.StorageKey, assets.Root(), `\\`} {
		if forbidden != "" && strings.Contains(contentJSON, forbidden) {
			t.Fatalf("sensitive asset data %q leaked into content_json: %s", forbidden, contentJSON)
		}
	}
	if !strings.Contains(contentJSON, `"outputIndex":0`) {
		t.Fatalf("zero output index must be explicit: %s", contentJSON)
	}
	file, err := assets.Open(asset.StorageKey, imageassets.Expected{SHA256: asset.SHA256, ByteSize: asset.ByteSize, Width: asset.Width, Height: asset.Height})
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	hydrated := runner.providerMessageFromDBForContext(ctx, assistant, false)
	if len(hydrated.Blocks) != 1 || !bytes.Equal(hydrated.Blocks[0].Data, pngData) {
		t.Fatalf("verified history image was not hydrated: %+v", hydrated.Blocks)
	}
	objectPath := filepath.Join(assets.Root(), filepath.FromSlash(asset.StorageKey))
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	degraded := runner.providerMessageFromDBForContext(ctx, assistant, false)
	if len(degraded.Blocks) != 1 || degraded.Blocks[0].Type != "text" || !strings.Contains(degraded.Blocks[0].Text, "unavailable") || strings.Contains(degraded.Content, assets.Root()) {
		t.Fatalf("missing history image did not degrade safely: %+v", degraded)
	}
}

func TestRunnerPersistsTextImageAndImageToolCallBeforeExecution(t *testing.T) {
	t.Run("text and image", func(t *testing.T) {
		ctx := context.Background()
		store, createdAgent := newImageAgentTestStore(t, t.TempDir(), "acceptEdits")
		defer store.Close()
		assets, err := imageassets.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		provider := &imageScriptedProvider{
			scriptedProvider: &scriptedProvider{turns: [][]providers.Event{{
				{Type: "text", Text: "Here is the image."},
				{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{GenerationID: "generation-text", Status: "completed", OutputIndex: 0, Data: agentTestPNG(t, 2, 2)}},
				{Type: "done", Done: true, StopReason: "end_turn"},
			}}},
			imageModels: map[string]bool{"test": true},
		}
		runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})
		runner.SetGeneratedImageStore(assets)
		if _, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "draw"}); err != nil {
			t.Fatal(err)
		}
		if err := runner.run(ctx, createdAgent.ID, ""); err != nil {
			t.Fatal(err)
		}
		messages, err := store.ListMessagesWithAttachmentData(ctx, createdAgent.ID)
		if err != nil {
			t.Fatal(err)
		}
		var blocks []providers.ContentBlock
		if len(messages) != 2 || messages[1].ContentText != "Here is the image." || len(messages[1].GeneratedImages) != 1 || json.Unmarshal(messages[1].ContentJSON, &blocks) != nil || len(blocks) != 2 || blocks[0].Type != "text" || blocks[1].Type != "image_generation" {
			t.Fatalf("text+image response was not preserved: %+v blocks=%+v", messages, blocks)
		}
	})

	t.Run("image and ordinary tool call", func(t *testing.T) {
		ctx := context.Background()
		store, createdAgent := newImageAgentTestStore(t, t.TempDir(), "acceptEdits")
		defer store.Close()
		assets, err := imageassets.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		provider := &imageScriptedProvider{
			scriptedProvider: &scriptedProvider{turns: [][]providers.Event{
				{
					{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{GenerationID: "generation-tool", Status: "completed", OutputIndex: 0, Data: agentTestPNG(t, 3, 3)}},
					{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "tool-1", Name: "ImageProbe", Input: json.RawMessage(`{}`)}},
					{Type: "done", Done: true, StopReason: "tool_use"},
				},
				{{Type: "text", Text: "finished"}, {Type: "done", Done: true, StopReason: "end_turn"}},
			}},
			imageModels: map[string]bool{"test": true},
		}
		providerRegistry := providers.NewRegistry()
		providerRegistry.Register(provider)
		toolRegistry := tools.NewRegistry()
		executions := 0
		toolRegistry.Register(imageProbeTool{verify: func() error {
			executions++
			messages, err := store.ListMessagesWithAttachmentData(ctx, createdAgent.ID)
			if err != nil {
				return err
			}
			if len(messages) < 2 || messages[len(messages)-1].Role != "assistant" || len(messages[len(messages)-1].GeneratedImages) != 1 {
				return fmt.Errorf("assistant image was not durable before tool execution: %+v", messages)
			}
			return nil
		}})
		runner := NewRunner(store, providerRegistry, toolRegistry, NewHub(), config.AgentConfig{MaxTurns: 3})
		runner.SetGeneratedImageStore(assets)
		if _, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "draw then inspect"}); err != nil {
			t.Fatal(err)
		}
		if err := runner.run(ctx, createdAgent.ID, ""); err != nil {
			t.Fatal(err)
		}
		if executions != 1 || provider.requestCount() != 2 {
			t.Fatalf("image generation must not enter the local tool loop: executions=%d requests=%d", executions, provider.requestCount())
		}
		messages, err := store.ListMessagesWithAttachmentData(ctx, createdAgent.ID)
		if err != nil {
			t.Fatal(err)
		}
		var firstAssistant db.Message
		for _, message := range messages {
			if message.Role == "assistant" && len(message.GeneratedImages) > 0 {
				firstAssistant = message
				break
			}
		}
		var blocks []providers.ContentBlock
		if firstAssistant.ID == "" || json.Unmarshal(firstAssistant.ContentJSON, &blocks) != nil || !hasImageTestBlockType(blocks, "tool_use") || !hasImageTestBlockType(blocks, "image_generation") {
			t.Fatalf("image+tool assistant message was not preserved: %+v blocks=%+v", firstAssistant, blocks)
		}
		if !requestContainsHydratedImage(provider.request(1), "generation-tool") {
			t.Fatalf("next model turn did not receive hydrated native image history: %+v", provider.request(1).Messages)
		}
	})
}

func TestRunnerSkipsPartialOnlyMessageAndFailsClosedWithoutImageStore(t *testing.T) {
	t.Run("partial only", func(t *testing.T) {
		ctx := context.Background()
		store, createdAgent := newImageAgentTestStore(t, t.TempDir(), "acceptEdits")
		defer store.Close()
		provider := &imageScriptedProvider{
			scriptedProvider: &scriptedProvider{turns: [][]providers.Event{{
				{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{GenerationID: "generation-partial", Status: "partial", OutputIndex: 0, PartialIndex: 1}},
				{Type: "done", Done: true, StopReason: "end_turn"},
			}}},
			imageModels: map[string]bool{"test": true},
		}
		runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})
		if _, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "draw"}); err != nil {
			t.Fatal(err)
		}
		if err := runner.run(ctx, createdAgent.ID, ""); err != nil {
			t.Fatal(err)
		}
		messages, err := store.ListMessages(ctx, createdAgent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 1 {
			t.Fatalf("partial-only output created an empty assistant message: %+v", messages)
		}
	})

	t.Run("final image without store", func(t *testing.T) {
		ctx := context.Background()
		store, createdAgent := newImageAgentTestStore(t, t.TempDir(), "acceptEdits")
		defer store.Close()
		provider := &imageScriptedProvider{
			scriptedProvider: &scriptedProvider{turns: [][]providers.Event{{
				{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{GenerationID: "generation-missing-store", Status: "completed", OutputIndex: 0, Data: agentTestPNG(t, 2, 2)}},
				{Type: "done", Done: true, StopReason: "end_turn"},
			}}},
			imageModels: map[string]bool{"test": true},
		}
		runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})
		if _, err := store.AddMessage(ctx, db.Message{AgentID: createdAgent.ID, Role: "user", ContentText: "draw"}); err != nil {
			t.Fatal(err)
		}
		err := runner.run(ctx, createdAgent.ID, "")
		if err == nil || !strings.Contains(err.Error(), "generated image store is unavailable") {
			t.Fatalf("expected explicit missing image store error, got %v", err)
		}
		messages, listErr := store.ListMessages(ctx, createdAgent.ID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(messages) != 1 {
			t.Fatalf("missing image store must not write assistant data: %+v", messages)
		}
	})
}

func newImageAgentTestStore(t *testing.T, projectDir, permissionMode string) (*db.Store, db.Agent) {
	t.Helper()
	root := filepath.Join(".tmp-agent-image-tests", db.NewID())
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(".tmp-agent-image-tests", filepath.Base(root))) })
	store, err := db.Open(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, createdAgent, err := store.CreateProject(context.Background(), "Demo", "", projectDir, "fake:test", permissionMode)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return store, createdAgent
}

type imageThenBlockingProvider struct{}

func (*imageThenBlockingProvider) Name() string { return "fake" }
func (*imageThenBlockingProvider) ListModels(context.Context) ([]string, error) {
	return []string{"image"}, nil
}
func (*imageThenBlockingProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Streaming: true, ImageGeneration: true}
}
func (*imageThenBlockingProvider) ModelCapabilities(string) providers.ModelCapabilities {
	return providers.ModelCapabilities{ImageGeneration: true, ImageGenerationKnown: true}
}
func (*imageThenBlockingProvider) Generate(context.Context, providers.GenerateRequest) (<-chan providers.Event, error) {
	out := make(chan providers.Event, 1)
	out <- providers.Event{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{GenerationID: "generation-progress", Status: "generating"}}
	return out, nil
}

type finalImageThenBlockingProvider struct {
	data []byte
}

func (*finalImageThenBlockingProvider) Name() string { return "fake" }
func (*finalImageThenBlockingProvider) ListModels(context.Context) ([]string, error) {
	return []string{"image"}, nil
}
func (*finalImageThenBlockingProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Streaming: true, ImageGeneration: true}
}
func (*finalImageThenBlockingProvider) ModelCapabilities(string) providers.ModelCapabilities {
	return providers.ModelCapabilities{ImageGeneration: true, ImageGenerationKnown: true}
}
func (p *finalImageThenBlockingProvider) Generate(context.Context, providers.GenerateRequest) (<-chan providers.Event, error) {
	out := make(chan providers.Event, 1)
	out <- providers.Event{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{GenerationID: "generation-final", Status: "completed", Data: append([]byte(nil), p.data...)}}
	return out, nil
}

type imageProbeTool struct {
	verify func() error
}

func (tool imageProbeTool) Name() string { return "ImageProbe" }
func (tool imageProbeTool) Description() string {
	return "Verifies generated image persistence ordering."
}
func (tool imageProbeTool) Schema() any { return map[string]any{"type": "object"} }
func (tool imageProbeTool) Risk(json.RawMessage) tools.Risk {
	return tools.RiskRead
}
func (tool imageProbeTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	if tool.verify != nil {
		if err := tool.verify(); err != nil {
			return tools.Result{}, err
		}
	}
	return tools.Result{Output: "ok"}, nil
}

func agentTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 12, G: 34, B: 56, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func hasImageTestBlockType(blocks []providers.ContentBlock, blockType string) bool {
	for _, block := range blocks {
		if block.Type == blockType {
			return true
		}
	}
	return false
}

func requestContainsHydratedImage(request providers.GenerateRequest, generationID string) bool {
	for _, message := range request.Messages {
		for _, block := range message.Blocks {
			if block.Type == "image_generation" && block.GenerationID == generationID && len(block.Data) > 0 {
				return true
			}
		}
	}
	return false
}
