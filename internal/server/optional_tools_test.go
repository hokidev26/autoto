package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/db"
	"autoto/internal/tools"
)

type optionalToolsFakeTool struct {
	name        string
	description string
	metadata    tools.CatalogMetadata
}

func (tool optionalToolsFakeTool) Name() string        { return tool.name }
func (tool optionalToolsFakeTool) Description() string { return tool.description }
func (tool optionalToolsFakeTool) Schema() any {
	return map[string]any{"properties": map[string]any{"token": map[string]any{"default": "secret-default"}}}
}
func (tool optionalToolsFakeTool) Risk(json.RawMessage) tools.Risk { return tools.RiskRead }
func (tool optionalToolsFakeTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	return tools.Result{}, nil
}
func (tool optionalToolsFakeTool) CatalogMetadata() tools.CatalogMetadata { return tool.metadata }

func openOptionalToolsHandler(t *testing.T) (http.Handler, *db.Store) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "optional-tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewOptionalToolsHandler(store, OptionalToolCatalogFunc(func(context.Context) ([]tools.Tool, error) {
		return []tools.Tool{
			optionalToolsFakeTool{name: "plugin__search", description: "Search safely", metadata: tools.CatalogMetadata{Domain: "plugins", DisplayName: "Plugin Search", Source: "plugin", SourceID: "plugin-1"}},
			optionalToolsFakeTool{name: "read", description: "Read files"},
		}, nil
	}))
	return handler, store
}

func optionalToolsRequest(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var input *bytes.Reader
	if body == nil {
		input = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, input)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeOptionalToolsResponse[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response %d %s: %v", response.Code, response.Body.String(), err)
	}
	return value
}

func TestOptionalToolsCatalogGroupsMetadataWithoutSchemasOrSecrets(t *testing.T) {
	handler, _ := openOptionalToolsHandler(t)
	response := optionalToolsRequest(t, handler, http.MethodGet, "/catalog", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "secret-default") || strings.Contains(body, "schema") {
		t.Fatalf("catalog leaked tool schema or secret-bearing default: %s", body)
	}
	var payload struct {
		Tools []optionalToolCatalogItem `json:"tools"`
	}
	payload = decodeOptionalToolsResponse[struct {
		Tools []optionalToolCatalogItem `json:"tools"`
	}](t, response)
	if len(payload.Tools) != 2 || payload.Tools[0].Domain != "filesystem" || payload.Tools[1].Domain != "plugins" {
		t.Fatalf("unexpected grouped catalog: %+v", payload.Tools)
	}
	if payload.Tools[1].DisplayName != "Plugin Search" || payload.Tools[1].SourceID != "plugin-1" {
		t.Fatalf("optional catalog metadata was not used: %+v", payload.Tools[1])
	}
}

func TestOptionalToolsAPIInheritanceSoftDeleteCASAndOrphans(t *testing.T) {
	handler, _ := openOptionalToolsHandler(t)
	put := func(body map[string]any) db.ToolAvailabilityRule {
		response := optionalToolsRequest(t, handler, http.MethodPut, "/rules", body)
		if response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("put failed %d: %s", response.Code, response.Body.String())
		}
		return decodeOptionalToolsResponse[db.ToolAvailabilityRule](t, response)
	}
	global := put(map[string]any{"toolName": "read", "scope": "global", "state": "disabled", "expectedRevision": 0})
	_ = global
	project := put(map[string]any{"toolName": "read", "scope": "project", "projectId": "p1", "state": "enabled", "expectedRevision": 0})
	workspace := put(map[string]any{"toolName": "read", "scope": "workspace", "projectId": "p1", "workspaceId": "w1", "state": "disabled", "expectedRevision": 0})
	orphan := put(map[string]any{"toolName": "removed_plugin__tool", "scope": "workspace", "projectId": "p1", "workspaceId": "w1", "state": "disabled", "expectedRevision": 0})

	effectiveResponse := optionalToolsRequest(t, handler, http.MethodGet, "/effective?scope=workspace&projectId=p1&workspaceId=w1", nil)
	if effectiveResponse.Code != http.StatusOK {
		t.Fatalf("effective failed %d: %s", effectiveResponse.Code, effectiveResponse.Body.String())
	}
	var effective struct {
		Tools []optionalToolEffectiveResponse `json:"tools"`
	}
	effective = decodeOptionalToolsResponse[struct {
		Tools []optionalToolEffectiveResponse `json:"tools"`
	}](t, effectiveResponse)
	states := map[string]optionalToolEffectiveResponse{}
	for _, item := range effective.Tools {
		states[item.Name] = item
	}
	if states["read"].Enabled || states["read"].SourceRuleID != workspace.ID {
		t.Fatalf("workspace disabled rule did not shadow project: %+v", states["read"])
	}
	if !states[orphan.ToolName].Orphan {
		t.Fatalf("orphan effective tool was not retained: %+v", states[orphan.ToolName])
	}

	deletedResponse := optionalToolsRequest(t, handler, http.MethodDelete, "/rules/"+workspace.ID, map[string]any{"expectedRevision": workspace.Revision})
	if deletedResponse.Code != http.StatusOK {
		t.Fatalf("delete failed %d: %s", deletedResponse.Code, deletedResponse.Body.String())
	}
	effectiveResponse = optionalToolsRequest(t, handler, http.MethodGet, "/effective?scope=workspace&projectId=p1&workspaceId=w1&toolName=read", nil)
	effective = decodeOptionalToolsResponse[struct {
		Tools []optionalToolEffectiveResponse `json:"tools"`
	}](t, effectiveResponse)
	if len(effective.Tools) != 1 || !effective.Tools[0].Enabled || effective.Tools[0].SourceRuleID != project.ID {
		t.Fatalf("soft deletion did not restore inherited project rule: %+v", effective.Tools)
	}

	stale := optionalToolsRequest(t, handler, http.MethodDelete, "/rules/"+project.ID, map[string]any{"expectedRevision": 99})
	if stale.Code != http.StatusConflict || strings.Contains(strings.ToLower(stale.Body.String()), "sqlite") {
		t.Fatalf("expected sanitized CAS conflict, got %d: %s", stale.Code, stale.Body.String())
	}

	rulesResponse := optionalToolsRequest(t, handler, http.MethodGet, "/rules?scope=workspace&projectId=p1&workspaceId=w1", nil)
	var rules struct {
		Rules []optionalToolRuleResponse `json:"rules"`
	}
	rules = decodeOptionalToolsResponse[struct {
		Rules []optionalToolRuleResponse `json:"rules"`
	}](t, rulesResponse)
	if len(rules.Rules) != 1 || !rules.Rules[0].Orphan || rules.Rules[0].ToolName != orphan.ToolName {
		t.Fatalf("active orphan rule was not listable: %+v", rules.Rules)
	}

	revisionsResponse := optionalToolsRequest(t, handler, http.MethodGet, "/rules/"+workspace.ID+"/revisions", nil)
	if revisionsResponse.Code != http.StatusOK || strings.Contains(revisionsResponse.Body.String(), "api_request") || strings.Contains(revisionsResponse.Body.String(), "actor") {
		t.Fatalf("revision response leaked actor details or failed: %d %s", revisionsResponse.Code, revisionsResponse.Body.String())
	}
}

func TestOptionalToolsAPIStrictValidation(t *testing.T) {
	handler, _ := openOptionalToolsHandler(t)
	cases := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/catalog?extra=1", nil},
		{http.MethodGet, "/rules?scope=global&projectId=p1", nil},
		{http.MethodGet, "/effective?scope=workspace&projectId=p1", nil},
		{http.MethodPut, "/rules", map[string]any{"toolName": "bad tool", "scope": "global", "state": "enabled", "expectedRevision": 0}},
		{http.MethodPut, "/rules", map[string]any{"toolName": " read ", "scope": "global", "state": "enabled", "expectedRevision": 0}},
		{http.MethodPut, "/rules", map[string]any{"toolName": "read", "scope": "global", "state": "inherit", "expectedRevision": 0}},
		{http.MethodPut, "/rules", map[string]any{"toolName": "read", "scope": "global", "state": "enabled", "expectedRevision": 0, "secret": "must-not-echo"}},
	}
	for _, test := range cases {
		response := optionalToolsRequest(t, handler, test.method, test.path, test.body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected %s %s to fail strictly, got %d: %s", test.method, test.path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "must-not-echo") || strings.Contains(strings.ToLower(response.Body.String()), "sqlite") {
			t.Fatalf("validation response leaked input or database details: %s", response.Body.String())
		}
	}
}
