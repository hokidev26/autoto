package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
)

func setupToolByID(t *testing.T, response setupStatusResponse, id string) setupToolStatus {
	t.Helper()
	for _, tool := range response.Tools {
		if tool.ID == id {
			return tool
		}
	}
	t.Fatalf("setup response is missing tool %q: %+v", id, response.Tools)
	return setupToolStatus{}
}

func TestDetectSetupStatusNormalizesToolsWithoutLeakingPaths(t *testing.T) {
	paths := map[string]string{
		"cmd":    `C:\secret\system32\cmd.exe`,
		"winget": `C:\secret\WindowsApps\winget.exe`,
		"git":    `C:\secret\Git\cmd\git.exe`,
	}
	probe := setupProbe{
		GOOS:          "windows",
		GOARCH:        "amd64",
		Now:           func() time.Time { return time.Date(2026, 7, 24, 12, 30, 0, 0, time.FixedZone("test", 2*60*60)) },
		DatabaseReady: func(context.Context) bool { return true },
		LookPath: func(name string) (string, error) {
			if path, ok := paths[name]; ok {
				return path, nil
			}
			return "", errors.New("missing")
		},
		RunVersion: func(_ context.Context, executable string, args ...string) (string, error) {
			if executable == paths["cmd"] && len(args) == 3 && args[0] == "/C" && args[1] == "exit" && args[2] == "0" {
				return "", nil
			}
			if executable == paths["git"] && len(args) == 1 && args[0] == "--version" {
				return "git version 2.50.1.windows.1\nPATH=C:\\secret\\bin\nTOKEN=private", nil
			}
			t.Fatalf("unexpected version probe: executable=%q args=%v", executable, args)
			return "", errors.New("unexpected probe")
		},
	}

	status := detectSetupStatus(context.Background(), probe)
	if status.GeneratedAt != "2026-07-24T10:30:00Z" {
		t.Fatalf("generatedAt = %q", status.GeneratedAt)
	}
	if status.Platform.OS != "windows" || status.Platform.Arch != "amd64" {
		t.Fatalf("unexpected platform: %+v", status.Platform)
	}
	if !status.PackageManager.Available || status.PackageManager.Name != "winget" {
		t.Fatalf("unexpected package manager: %+v", status.PackageManager)
	}
	if !status.Database.Available || !status.Database.Required || status.Database.Status != "available" {
		t.Fatalf("unexpected database status: %+v", status.Database)
	}
	if shell := setupToolByID(t, status, "shell"); !shell.Available || !shell.Required || !shell.Recommended || shell.BuiltIn {
		t.Fatalf("unexpected shell status: %+v", shell)
	}
	if search := setupToolByID(t, status, "search"); !search.Available || !search.BuiltIn || search.Required || !search.Recommended {
		t.Fatalf("unexpected built-in search status: %+v", search)
	}
	if git := setupToolByID(t, status, "git"); !git.Available || git.Required || !git.Recommended || git.Version != "2.50.1.windows.1" {
		t.Fatalf("unexpected git status: %+v", git)
	}
	for _, id := range []string{"go", "node"} {
		tool := setupToolByID(t, status, id)
		if tool.Available || tool.Required || tool.Recommended || tool.BuiltIn || tool.InstallCommand == "" {
			t.Fatalf("unexpected optional tool status for %s: %+v", id, tool)
		}
	}

	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, secret := range []string{`C:\secret`, "PATH=", "TOKEN="} {
		if strings.Contains(body, secret) {
			t.Fatalf("setup response leaked %q: %s", secret, body)
		}
	}
}

func TestDetectSetupStatusUsesPlatformPackageManagerOrder(t *testing.T) {
	available := map[string]bool{"sh": true, "apt-get": true, "dnf": true, "node": true}
	status := detectSetupStatus(context.Background(), setupProbe{
		GOOS: "linux", GOARCH: "arm64", Now: func() time.Time { return time.Unix(0, 0) },
		LookPath: func(name string) (string, error) {
			if available[name] {
				return "/not-returned/" + name, nil
			}
			return "", errors.New("missing")
		},
		RunVersion: func(_ context.Context, executable string, args ...string) (string, error) {
			if strings.HasSuffix(executable, "/sh") && len(args) == 2 && args[0] == "-c" && args[1] == ":" {
				return "", nil
			}
			if strings.HasSuffix(executable, "/node") {
				return "v22.14.0\n", nil
			}
			return "", errors.New("no version")
		},
	})

	if !status.PackageManager.Available || status.PackageManager.Name != "apt-get" {
		t.Fatalf("package manager preference = %+v", status.PackageManager)
	}
	if shell := setupToolByID(t, status, "shell"); !shell.Available || !shell.Required {
		t.Fatalf("unexpected non-Windows shell: %+v", shell)
	}
	if git := setupToolByID(t, status, "git"); git.Available || !git.Recommended || git.InstallCommand != "sudo apt-get install -y git" {
		t.Fatalf("unexpected git install guidance: %+v", git)
	}
	if node := setupToolByID(t, status, "node"); !node.Available || node.Version != "v22.14.0" || node.InstallCommand != "" {
		t.Fatalf("unexpected node status: %+v", node)
	}
}

func TestDetectSetupStatusRequiresRunnableShell(t *testing.T) {
	status := detectSetupStatus(context.Background(), setupProbe{
		GOOS: "linux", GOARCH: "amd64", Now: func() time.Time { return time.Unix(0, 0) },
		DatabaseReady: func(context.Context) bool { return true },
		LookPath: func(name string) (string, error) {
			if name == "sh" {
				return "/usr/bin/sh", nil
			}
			return "", errors.New("missing")
		},
		RunVersion: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("not executable")
		},
	})
	if shell := setupToolByID(t, status, "shell"); shell.Available || !shell.Required {
		t.Fatalf("unrunnable shell was accepted: %+v", shell)
	}
}

func TestSetupStatusHandlerAndRoute(t *testing.T) {
	probe := setupProbe{
		GOOS: "plan9", GOARCH: "amd64", Now: func() time.Time { return time.Unix(0, 0) },
		LookPath:   func(string) (string, error) { return "", errors.New("missing") },
		RunVersion: func(context.Context, string, ...string) (string, error) { return "", errors.New("missing") },
	}
	recorder := httptest.NewRecorder()
	setupStatusHandler(probe).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("handler returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var response setupStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Platform.OS != "plan9" || !setupToolByID(t, response, "search").BuiltIn {
		t.Fatalf("unexpected handler response: %+v", response)
	}

	app := New(config.Config{}, nil, nil, nil)
	routeRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(routeRecorder, newTestRequest(http.MethodGet, "/api/setup/status", nil))
	if routeRecorder.Code != http.StatusOK {
		t.Fatalf("registered route returned %d: %s", routeRecorder.Code, routeRecorder.Body.String())
	}
	if routeRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("setup status cache header = %q", routeRecorder.Header().Get("Cache-Control"))
	}
	var unavailable setupStatusResponse
	if err := json.Unmarshal(routeRecorder.Body.Bytes(), &unavailable); err != nil {
		t.Fatal(err)
	}
	if unavailable.Database.Available || unavailable.Database.Status != "unavailable" {
		t.Fatalf("nil store did not report database unavailable: %+v", unavailable.Database)
	}

	readyApp, _ := newAccountPreferencesTestServer(t)
	readyRecorder := httptest.NewRecorder()
	readyApp.Routes().ServeHTTP(readyRecorder, newTestRequest(http.MethodGet, "/api/setup/status", nil))
	if readyRecorder.Code != http.StatusOK {
		t.Fatalf("database-ready route returned %d: %s", readyRecorder.Code, readyRecorder.Body.String())
	}
	var ready setupStatusResponse
	if err := json.Unmarshal(readyRecorder.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Database.Available || ready.Database.Status != "available" {
		t.Fatalf("open store did not report database available: %+v", ready.Database)
	}
}

func TestBoundedSetupOutputAndVersionNormalization(t *testing.T) {
	output := &boundedSetupOutput{limit: 4}
	if written, err := output.Write([]byte("123456")); err != nil || written != 6 {
		t.Fatalf("write returned written=%d err=%v", written, err)
	}
	if got := string(output.data); got != "1234" {
		t.Fatalf("bounded output = %q", got)
	}
	if got := normalizeSetupVersion("go version go1.26.5 windows/amd64\nHOME=secret"); got != "1.26.5" {
		t.Fatalf("normalized version = %q", got)
	}
}
