package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestListMessagesIncludesAuthorIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "authors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)
	adminCookie := registerCollaborationTestUser(t, app, "ray")
	registerCollaborationTestUser(t, app, "feng")

	ray, _, err := store.GetUserByHandle(ctx, "ray")
	if err != nil {
		t.Fatal(err)
	}
	feng, _, err := store.GetUserByHandle(ctx, "feng")
	if err != nil {
		t.Fatal(err)
	}
	rayProfile := json.RawMessage(`{"displayName":"管理員雷","avatarInitials":"雷"}`)
	fengProfile := json.RawMessage(`{"displayName":"協作者風","avatarInitials":"風"}`)
	rayPrefs, err := store.GetAccountPreferences(ctx, db.AccountPreferenceScopeUser, ray.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchAccountPreferences(ctx, db.AccountPreferenceScopeUser, ray.ID, db.AccountPreferencesPatch{ExpectedRevision: rayPrefs.Revision, ProfileJSON: &rayProfile}); err != nil {
		t.Fatal(err)
	}
	fengPrefs, err := store.GetAccountPreferences(ctx, db.AccountPreferenceScopeUser, feng.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchAccountPreferences(ctx, db.AccountPreferenceScopeUser, feng.ID, db.AccountPreferencesPatch{ExpectedRevision: fengPrefs.Revision, ProfileJSON: &fengProfile}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "from ray", CreatedBy: ray.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "from feng", CreatedBy: feng.ID}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/messages", nil)
	request.AddCookie(adminCookie)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list messages: %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "avatarDataUrl") || strings.Contains(recorder.Body.String(), "data:image") {
		t.Fatalf("message authors must not include avatars: %s", recorder.Body.String())
	}
	var page db.MessagePage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	authors := map[string]db.MessageAuthor{}
	for _, message := range page.Messages {
		if message.Role != "user" {
			continue
		}
		if message.Author == nil {
			t.Fatalf("user message %q missing author: %+v", message.ContentText, message)
		}
		authors[message.ContentText] = *message.Author
	}
	if authors["from ray"].Handle != "ray" || authors["from ray"].DisplayName != "管理員雷" || authors["from ray"].AvatarInitials != "雷" {
		t.Fatalf("ray author = %+v", authors["from ray"])
	}
	if authors["from feng"].Handle != "feng" || authors["from feng"].DisplayName != "協作者風" || authors["from feng"].AvatarInitials != "風" {
		t.Fatalf("feng author = %+v", authors["from feng"])
	}
}
