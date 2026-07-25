package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"autoto/internal/db"
)

func TestPromptDefinitionHandlersCASRestoreAndSummarySeparation(t *testing.T) {
	app := newProfileDefinitionTestServer(t)
	createBody := `{"scope":{"scope":"project","projectId":"p"},"key":"global-guidance","displayName":"Global guidance","summary":"Untrusted context","layer":"global_user","content":"Use project naming."}`
	createdResponse := definitionHandlerRequest(t, app.createPromptDefinition, http.MethodPost, "/api/prompt-definitions", createBody, "")
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created db.PromptDefinition
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Layer != db.PromptLayerGlobalUser || created.Content == "" {
		t.Fatalf("unexpected create: %+v", created)
	}

	list := definitionHandlerRequest(t, app.listPromptDefinitions, http.MethodGet, "/api/prompt-definitions?scope=project&projectId=p", "", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), "Use project naming") || strings.Contains(list.Body.String(), `"content"`) {
		t.Fatalf("list leaked prompt body: %s", list.Body.String())
	}

	updateBody := `{"expectedRevision":1,"key":"global-guidance","displayName":"Global guidance","summary":"System extension","layer":"system_extension","content":"Prefer bounded changes."}`
	updatedResponse := definitionHandlerRequest(t, app.updatePromptDefinition, http.MethodPut, "/api/prompt-definitions/"+created.ID, updateBody, created.ID)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	deleteResponse := definitionHandlerRequest(t, app.deletePromptDefinition, http.MethodDelete, "/api/prompt-definitions/"+created.ID, `{"expectedRevision":2}`, created.ID)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	restoreResponse := definitionHandlerRequest(t, app.restorePromptDefinition, http.MethodPost, "/api/prompt-definitions/"+created.ID+"/restore", `{"expectedRevision":3,"sourceRevision":1}`, created.ID)
	if restoreResponse.Code != http.StatusOK || !strings.Contains(restoreResponse.Body.String(), `"layer":"global_user"`) {
		t.Fatalf("restore = %d: %s", restoreResponse.Code, restoreResponse.Body.String())
	}
}

func TestPromptDefinitionHandlersRejectImmutableAndUnknownFields(t *testing.T) {
	app := newProfileDefinitionTestServer(t)
	for _, body := range []string{
		`{"scope":{"scope":"global"},"key":"x","displayName":"X","layer":"platform","content":"override"}`,
		`{"scope":{"scope":"global"},"key":"x","displayName":"X","layer":"global_user","content":"ok","actor":"forged"}`,
		`{"scope":{"scope":"workspace","projectId":"p"},"key":"x","displayName":"X","layer":"global_user","content":"ok"}`,
	} {
		response := definitionHandlerRequest(t, app.createPromptDefinition, http.MethodPost, "/api/prompt-definitions", body, "")
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid prompt returned %d: %s", response.Code, response.Body.String())
		}
	}
}
