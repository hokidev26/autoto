package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
)

func newSetupInstallTestServer(t *testing.T) *Server {
	t.Helper()
	app := New(config.Config{}, nil, nil, nil)
	app.setupProbeFactory = func() setupProbe {
		return setupProbe{
			GOOS: "windows", GOARCH: "amd64",
			LookPath: func(name string) (string, error) {
				if name == "winget" {
					return `C:\fake\winget.exe`, nil
				}
				return "", errors.New("missing")
			},
			RunVersion: func(context.Context, string, ...string) (string, error) {
				return "", errors.New("no probes in install tests")
			},
		}
	}
	return app
}

func postSetupInstall(t *testing.T, app *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := newTestRequest(http.MethodPost, "/api/setup/install", strings.NewReader(body))
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

func getSetupInstallStatus(t *testing.T, app *Server) setupInstallStatusResponse {
	t.Helper()
	request := newTestRequest(http.MethodGet, "/api/setup/install/status", nil)
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("install status returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var response setupInstallStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func waitForSetupInstallJob(t *testing.T, app *Server, tool, wantStatus string) setupInstallJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, job := range getSetupInstallStatus(t, app).Jobs {
			if job.Tool == tool && job.Status == wantStatus {
				return job
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %q never reached status %q: %+v", tool, wantStatus, getSetupInstallStatus(t, app).Jobs)
	return setupInstallJob{}
}

func TestSetupInstallRunsAllowlistedPackageManagerCommand(t *testing.T) {
	app := newSetupInstallTestServer(t)
	var mu sync.Mutex
	var gotExecutable string
	var gotArgs []string
	app.setupInstallRunner = func(_ context.Context, executable string, args ...string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		gotExecutable = executable
		gotArgs = args
		return "installed", nil
	}

	recorder := postSetupInstall(t, app, `{"tool":"git"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("install returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var started setupInstallStartResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Job.Tool != "git" || started.Job.Status != setupInstallStatusRunning {
		t.Fatalf("unexpected started job: %+v", started.Job)
	}
	if started.Job.Command != setupInstallCommand("winget", "git") {
		t.Fatalf("unexpected command: %q", started.Job.Command)
	}

	job := waitForSetupInstallJob(t, app, "git", setupInstallStatusSucceeded)
	if job.Output != "installed" || job.Error != "" || job.FinishedAt == "" {
		t.Fatalf("unexpected finished job: %+v", job)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotExecutable != `C:\fake\winget.exe` {
		t.Fatalf("runner executable = %q", gotExecutable)
	}
	wantArgs := strings.Fields(setupInstallCommand("winget", "git"))[1:]
	if strings.Join(gotArgs, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("runner args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestSetupInstallReportsFailureOutput(t *testing.T) {
	app := newSetupInstallTestServer(t)
	app.setupInstallRunner = func(context.Context, string, ...string) (string, error) {
		return "network unreachable", errors.New("exit status 1")
	}
	if recorder := postSetupInstall(t, app, `{"tool":"node"}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("install returned %d: %s", recorder.Code, recorder.Body.String())
	}
	job := waitForSetupInstallJob(t, app, "node", setupInstallStatusFailed)
	if job.Error != "exit status 1" || job.Output != "network unreachable" {
		t.Fatalf("unexpected failed job: %+v", job)
	}
}

func TestSetupInstallRejectsUnknownToolsAndConcurrentRuns(t *testing.T) {
	app := newSetupInstallTestServer(t)
	for _, body := range []string{`{"tool":"curl"}`, `{"tool":""}`, `{"tool":"git; rm -rf /"}`, `not json`} {
		if recorder := postSetupInstall(t, app, body); recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q returned %d, want 400", body, recorder.Code)
		}
	}

	release := make(chan struct{})
	app.setupInstallRunner = func(ctx context.Context, _ string, _ ...string) (string, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "", nil
	}
	if recorder := postSetupInstall(t, app, `{"tool":"git"}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("first install returned %d: %s", recorder.Code, recorder.Body.String())
	}
	busy := postSetupInstall(t, app, `{"tool":"node"}`)
	if busy.Code != http.StatusConflict {
		t.Fatalf("concurrent install returned %d, want 409", busy.Code)
	}
	var busyResponse setupInstallStartResponse
	if err := json.Unmarshal(busy.Body.Bytes(), &busyResponse); err != nil {
		t.Fatal(err)
	}
	if busyResponse.Job.Tool != "git" || busyResponse.Job.Status != setupInstallStatusRunning {
		t.Fatalf("busy response should describe the running job: %+v", busyResponse.Job)
	}
	close(release)
	waitForSetupInstallJob(t, app, "git", setupInstallStatusSucceeded)
}

func TestSetupInstallRequiresPackageManagerAndLocalToken(t *testing.T) {
	app := newSetupInstallTestServer(t)
	app.setupProbeFactory = func() setupProbe {
		return setupProbe{
			GOOS: "linux", GOARCH: "amd64",
			LookPath: func(string) (string, error) { return "", errors.New("missing") },
		}
	}
	if recorder := postSetupInstall(t, app, `{"tool":"git"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("install without package manager returned %d, want 409", recorder.Code)
	}

	unauthenticated := newTestRequest(http.MethodPost, "/api/setup/install", strings.NewReader(`{"tool":"git"}`))
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, unauthenticated)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("install without local token returned %d, want 401", recorder.Code)
	}
}
