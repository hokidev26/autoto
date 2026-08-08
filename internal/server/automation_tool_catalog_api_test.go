package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/tools"
)

func TestAutomationToolCatalogRoutesKeepInstallConfigureAndEnableSeparate(t *testing.T) {
	ctx := context.Background()
	homeDir := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "catalog-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	definition := automationTestDefinition(t, "playwright-mcp")
	commandCalls := 0
	catalog := NewAutomationToolCatalog(homeDir, store, AutomationToolCatalogOptions{
		GOOS: "windows",
		LookPath: func(name string) (string, error) {
			if name == "node" || name == "npm" {
				return filepath.Join(homeDir, "runtime", name+".exe"), nil
			}
			return "", errors.New("not found")
		},
		RunCommand: func(_ context.Context, command AutomationToolCommand) (AutomationToolCommandResult, error) {
			commandCalls++
			if reflect.DeepEqual(command.Args, []string{"--version"}) {
				return AutomationToolCommandResult{Output: "v22.0.0"}, nil
			}
			writeValidAutomationTestInstallation(t, automationTestArgValue(command.Args, "--prefix"), definition)
			return AutomationToolCommandResult{Output: "fake npm install"}, nil
		},
	})
	app := New(config.Config{}, store, nil, nil)
	app.SetAutomationToolCatalog(catalog)
	routes := app.Routes()

	list := httptest.NewRecorder()
	routes.ServeHTTP(list, newTestRequest(http.MethodGet, "/api/optional-tools/automation", nil))
	if list.Code != http.StatusOK || commandCalls != 0 {
		t.Fatalf("catalog listing must be read-only: status=%d calls=%d body=%s", list.Code, commandCalls, list.Body.String())
	}
	var items []AutomationToolCatalogItem
	if err := json.NewDecoder(list.Body).Decode(&items); err != nil || len(items) != len(automationToolDefinitions) {
		t.Fatalf("unexpected catalog response: err=%v items=%+v", err, items)
	}

	missingToken := httptest.NewRecorder()
	routes.ServeHTTP(missingToken, newTestRequest(http.MethodPost, "/api/optional-tools/automation/playwright-mcp/install", nil))
	if missingToken.Code != http.StatusForbidden || commandCalls != 0 {
		t.Fatalf("install without canonical token must fail before runner: status=%d calls=%d", missingToken.Code, commandCalls)
	}
	legacyRequest := newTestRequest(http.MethodPost, "/api/optional-tools/automation/playwright-mcp/install", nil)
	legacyRequest.Header.Set(nonCanonicalLocalTokenHeader, app.localToken)
	legacy := httptest.NewRecorder()
	routes.ServeHTTP(legacy, legacyRequest)
	if legacy.Code != http.StatusForbidden || commandCalls != 0 {
		t.Fatalf("legacy token must not authorize installation: status=%d calls=%d", legacy.Code, commandCalls)
	}
	remoteRequest := newTestRequest(http.MethodPost, "/api/optional-tools/automation/playwright-mcp/install", nil)
	remoteRequest.Host = "remote.example.test"
	markRemoteHTTPS(remoteRequest)
	remoteRequest.Header.Set(localTokenHeader, app.localToken)
	remote := httptest.NewRecorder()
	routes.ServeHTTP(remote, remoteRequest)
	if remote.Code == http.StatusOK || commandCalls != 0 {
		t.Fatalf("remote installation with leaked local token must fail: status=%d calls=%d", remote.Code, commandCalls)
	}

	unknownRequest := newTestRequest(http.MethodPost, "/api/optional-tools/automation/unknown/install", nil)
	unknownRequest.Header.Set(localTokenHeader, app.localToken)
	unknown := httptest.NewRecorder()
	routes.ServeHTTP(unknown, unknownRequest)
	if unknown.Code != http.StatusNotFound || commandCalls != 0 {
		t.Fatalf("unknown tool must be rejected before runner: status=%d calls=%d body=%s", unknown.Code, commandCalls, unknown.Body.String())
	}
	externalRequest := newTestRequest(http.MethodPost, "/api/optional-tools/automation/power-automate-desktop/install", nil)
	externalRequest.Header.Set(localTokenHeader, app.localToken)
	external := httptest.NewRecorder()
	routes.ServeHTTP(external, externalRequest)
	if external.Code != http.StatusConflict || commandCalls != 0 {
		t.Fatalf("external tool must not use managed installer: status=%d calls=%d body=%s", external.Code, commandCalls, external.Body.String())
	}

	installRequest := newTestRequest(http.MethodPost, "/api/optional-tools/automation/playwright-mcp/install", nil)
	installRequest.Header.Set(localTokenHeader, app.localToken)
	install := httptest.NewRecorder()
	routes.ServeHTTP(install, installRequest)
	if install.Code != http.StatusOK || commandCalls != 2 {
		t.Fatalf("local canonical install failed: status=%d calls=%d body=%s", install.Code, commandCalls, install.Body.String())
	}
	var installed AutomationToolCatalogItem
	if err := json.NewDecoder(install.Body).Decode(&installed); err != nil {
		t.Fatal(err)
	}
	if !installed.Installed || installed.Configured || installed.Enabled {
		t.Fatalf("installation crossed a workflow boundary: %+v", installed)
	}
	servers, err := store.ListMCPServers(ctx)
	if err != nil || len(servers) != 0 {
		t.Fatalf("install unexpectedly changed MCP registry: err=%v servers=%+v", err, servers)
	}

	configure := httptest.NewRecorder()
	routes.ServeHTTP(configure, newTestRequest(http.MethodPost, "/api/optional-tools/automation/playwright-mcp/configure", nil))
	if configure.Code != http.StatusOK || commandCalls != 2 {
		t.Fatalf("local configure failed or executed a process: status=%d calls=%d body=%s", configure.Code, commandCalls, configure.Body.String())
	}
	var configured AutomationToolCatalogItem
	if err := json.NewDecoder(configure.Body).Decode(&configured); err != nil {
		t.Fatal(err)
	}
	if !configured.Configured || configured.Enabled || configured.MCPServerID != tools.ManagedAutomationMCPServerID(definition.ID) {
		t.Fatalf("configure must create a disabled stable MCP record: %+v", configured)
	}
	server, err := store.GetMCPServer(ctx, configured.MCPServerID)
	if err != nil || server.Enabled {
		t.Fatalf("stored MCP server must remain disabled: err=%v server=%+v", err, server)
	}

	configureAgain := httptest.NewRecorder()
	routes.ServeHTTP(configureAgain, newTestRequest(http.MethodPost, "/api/optional-tools/automation/playwright-mcp/configure", nil))
	if configureAgain.Code != http.StatusOK || commandCalls != 2 {
		t.Fatalf("configure must be idempotent and process-free: status=%d calls=%d", configureAgain.Code, commandCalls)
	}
	servers, err = store.ListMCPServers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatalf("idempotent configure duplicated the MCP record: err=%v servers=%+v", err, servers)
	}

	patchBody, _ := json.Marshal(map[string]any{"enabled": true})
	patchRequest := newTestRequest(http.MethodPatch, "/api/mcp/servers/"+configured.MCPServerID, bytes.NewReader(patchBody))
	patchRequest.Header.Set("Content-Type", "application/json")
	patch := httptest.NewRecorder()
	routes.ServeHTTP(patch, patchRequest)
	if patch.Code != http.StatusOK || commandCalls != 2 {
		t.Fatalf("existing MCP enable flow failed or started a process: status=%d calls=%d body=%s", patch.Code, commandCalls, patch.Body.String())
	}
	enabled, err := store.GetMCPServer(ctx, configured.MCPServerID)
	if err != nil || !enabled.Enabled {
		t.Fatalf("explicit enable step did not update MCP registry: err=%v server=%+v", err, enabled)
	}

	fullToken, _, err := app.newRemoteAccessSession(remoteAccessModeFull)
	if err != nil {
		t.Fatal(err)
	}
	remoteInstall := newTestRequest(http.MethodPost, "/api/optional-tools/automation/playwright-mcp/install", nil)
	remoteInstall.Host = "remote.example.test"
	markRemoteHTTPS(remoteInstall)
	remoteInstall.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: fullToken})
	remoteInstall.Header.Set(localTokenHeader, app.localToken)
	remoteInstallRecorder := httptest.NewRecorder()
	routes.ServeHTTP(remoteInstallRecorder, remoteInstall)
	if remoteInstallRecorder.Code != http.StatusForbidden || commandCalls != 2 {
		t.Fatalf("even full remote sessions must not install host packages: status=%d calls=%d body=%s", remoteInstallRecorder.Code, commandCalls, remoteInstallRecorder.Body.String())
	}
}

func TestAutomationToolCatalogConfigureRequiresFullRemoteSession(t *testing.T) {
	ctx := context.Background()
	homeDir := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "catalog-remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	definition := automationTestDefinition(t, "chrome-devtools-mcp")
	writeValidAutomationTestInstallation(t, filepath.Join(homeDir, "optional-tools", "automation", definition.ID), definition)
	app := New(config.Config{}, store, nil, nil)
	app.SetAutomationToolCatalog(NewAutomationToolCatalog(homeDir, store, AutomationToolCatalogOptions{
		GOOS:     "windows",
		LookPath: func(name string) (string, error) { return filepath.Join(homeDir, "runtime", name+".exe"), nil },
		RunCommand: func(context.Context, AutomationToolCommand) (AutomationToolCommandResult, error) {
			t.Fatal("configure must not execute a process")
			return AutomationToolCommandResult{}, nil
		},
	}))
	routes := app.Routes()

	restrictedToken, _, err := app.newRemoteAccessSession(remoteAccessModeRestricted)
	if err != nil {
		t.Fatal(err)
	}
	restrictedRequest := newTestRequest(http.MethodPost, "/api/optional-tools/automation/chrome-devtools-mcp/configure", nil)
	restrictedRequest.Host = "remote.example.test"
	markRemoteHTTPS(restrictedRequest)
	restrictedRequest.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: restrictedToken})
	restricted := httptest.NewRecorder()
	routes.ServeHTTP(restricted, restrictedRequest)
	if restricted.Code != http.StatusForbidden {
		t.Fatalf("restricted remote session configured MCP: status=%d body=%s", restricted.Code, restricted.Body.String())
	}

	fullToken, _, err := app.newRemoteAccessSession(remoteAccessModeFull)
	if err != nil {
		t.Fatal(err)
	}
	fullRequest := newTestRequest(http.MethodPost, "/api/optional-tools/automation/chrome-devtools-mcp/configure", nil)
	fullRequest.Host = "remote.example.test"
	markRemoteHTTPS(fullRequest)
	fullRequest.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: fullToken})
	full := httptest.NewRecorder()
	routes.ServeHTTP(full, fullRequest)
	if full.Code != http.StatusOK {
		t.Fatalf("full remote session should use existing management guard: status=%d body=%s", full.Code, full.Body.String())
	}
	server, err := store.GetMCPServer(ctx, tools.ManagedAutomationMCPServerID(definition.ID))
	if err != nil || server.Enabled {
		t.Fatalf("remote full configure must still create disabled record: err=%v server=%+v", err, server)
	}
}

func TestAutomationToolCatalogUnavailableReturnsServiceUnavailable(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	for _, request := range []*http.Request{
		newTestRequest(http.MethodGet, "/api/optional-tools/automation", nil),
		newTestRequest(http.MethodPost, "/api/optional-tools/automation/playwright-mcp/configure", nil),
	} {
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected unavailable catalog status, got %d: %s", recorder.Code, recorder.Body.String())
		}
	}
}
