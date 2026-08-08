package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

func newQueueAPITestServer(t *testing.T) (*Server, *db.Store, db.Agent) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, _, agent, err := store.CreateProject(context.Background(), "Queue", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(attachmentTestProvider{})
	hub := agentpkg.NewHub()
	runner := agentpkg.NewRunner(store, registry, nil, hub, config.AgentConfig{})
	return New(config.Config{}, store, runner, hub, registry), store, agent
}

// markAgentBusy is what makes a parked message stay parked. Posting to the
// queue schedules a drain, and an idle agent sends the follow-up immediately, so
// a test that wants to inspect the queue has to reproduce the situation the
// queue exists for: a run already in flight.
func markAgentBusy(t *testing.T, store *db.Store, agentID string) {
	t.Helper()
	if _, err := store.CreateRun(context.Background(), db.Run{AgentID: agentID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
}

func queueMultipartBody(t *testing.T, fields map[string]string, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		part, err := writer.CreateFormFile("files", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

// Parking a file has to work from the same multipart request that sending one
// uses, so the client does not need a second upload shape for the queue.
func TestEnqueueMultipartMessageParksAttachment(t *testing.T) {
	app, store, agent := newQueueAPITestServer(t)
	markAgentBusy(t, store, agent.ID)

	body, contentType := queueMultipartBody(t, map[string]string{"text": "read this later", "mode": "plan"}, "queued.txt", "parked bytes")
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/queue", body)
	request.Header.Set("Content-Type", contentType)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var parked db.QueuedMessage
	if err := json.NewDecoder(recorder.Body).Decode(&parked); err != nil {
		t.Fatal(err)
	}
	if parked.Text != "read this later" || parked.RunMode != "plan" {
		t.Fatalf("parked message = %+v", parked)
	}
	if len(parked.Attachments) != 1 || parked.Attachments[0].Filename != "queued.txt" {
		t.Fatalf("parked attachments = %+v", parked.Attachments)
	}
	// Blobs are never serialized, so the response carries metadata only.
	if parked.Attachments[0].Data != nil || parked.Attachments[0].ModelData != nil {
		t.Fatal("queue response leaked attachment bytes")
	}

	recorder = httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/queue", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected queue 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var listed struct {
		Queue []db.QueuedMessage `json:"queue"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Queue) != 1 || len(listed.Queue[0].Attachments) != 1 {
		t.Fatalf("listed queue = %+v", listed.Queue)
	}
	if listed.Queue[0].Attachments[0].ID != parked.Attachments[0].ID {
		t.Fatalf("listed attachment id = %q, want %q", listed.Queue[0].Attachments[0].ID, parked.Attachments[0].ID)
	}
	// The bytes are in the database even though the wire form omits them.
	claimed, ok, err := store.ClaimNextQueuedMessage(context.Background(), agent.ID)
	if err != nil || !ok {
		t.Fatalf("claim failed: %v ok=%v", err, ok)
	}
	if len(claimed.Attachments) != 1 || string(claimed.Attachments[0].Data) != "parked bytes" {
		t.Fatalf("claimed attachment = %+v", claimed.Attachments)
	}
}

// An image with no caption is a normal send, so the queue accepts it too.
func TestEnqueueMultipartMessageAcceptsAttachmentWithoutText(t *testing.T) {
	app, store, agent := newQueueAPITestServer(t)
	markAgentBusy(t, store, agent.ID)

	body, contentType := queueMultipartBody(t, nil, "silent.txt", "no caption")
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/queue", body)
	request.Header.Set("Content-Type", contentType)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var parked db.QueuedMessage
	if err := json.NewDecoder(recorder.Body).Decode(&parked); err != nil {
		t.Fatal(err)
	}
	if parked.Text != "" || len(parked.Attachments) != 1 {
		t.Fatalf("attachment-only parked message = %+v", parked)
	}

	// Neither text nor a file is still nothing to send.
	empty, emptyType := queueMultipartBody(t, map[string]string{"text": "  "}, "", "")
	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/queue", empty)
	request.Header.Set("Content-Type", emptyType)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty multipart park, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// The JSON body predates attachments and still has to behave exactly as it did.
func TestEnqueueJSONMessageStillParksTextOnly(t *testing.T) {
	app, store, agent := newQueueAPITestServer(t)
	markAgentBusy(t, store, agent.ID)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/queue", bytes.NewBufferString(`{"text":"plain follow-up","mode":"plan"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var parked db.QueuedMessage
	if err := json.NewDecoder(recorder.Body).Decode(&parked); err != nil {
		t.Fatal(err)
	}
	if parked.Text != "plain follow-up" || parked.RunMode != "plan" || len(parked.Attachments) != 0 {
		t.Fatalf("json parked message = %+v", parked)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("attachments")) {
		t.Fatal("a text-only queue entry should not carry an attachments field")
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/queue", bytes.NewBufferString(`{"text":"   "}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for blank JSON text, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// The whole point of parking files server-side is that the drain sends them
// with no browser involved, so the attachments have to survive the claim and
// land on the message the run is built from.
func TestDrainMessageQueueHandsAttachmentsToTheSendPath(t *testing.T) {
	app, store, agent := newQueueAPITestServer(t)
	ctx := context.Background()

	if _, err := store.EnqueueMessage(ctx, db.QueuedMessage{
		AgentID: agent.ID,
		Text:    "use the file",
		Attachments: []db.Attachment{
			{Filename: "drained.txt", MIMEType: "text/plain", Kind: "text", SizeBytes: 11, Data: []byte("drained ok!"), ExtractedText: "drained ok!"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	app.drainMessageQueue(agent.ID)

	messages, err := store.ListMessagesWithAttachmentData(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sent *db.Message
	for i := range messages {
		if messages[i].Role == "user" {
			sent = &messages[i]
			break
		}
	}
	if sent == nil {
		t.Fatalf("drain sent no user message: %+v", messages)
	}
	if sent.ContentText != "use the file" {
		t.Fatalf("sent text = %q", sent.ContentText)
	}
	if len(sent.Attachments) != 1 || string(sent.Attachments[0].Data) != "drained ok!" {
		t.Fatalf("drain dropped the parked attachment: %+v", sent.Attachments)
	}
	remaining, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("queue still holds %d messages after a successful drain", len(remaining))
	}
	waitForAgentIdle(t, store, agent.ID)
}
