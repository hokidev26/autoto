package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoto/internal/config"
)

type stubShellDialogHost struct {
	confirmFn   func(ctx context.Context, message, title string) (bool, error)
	alertFn     func(ctx context.Context, message, title string) error
	directoryFn func(ctx context.Context, title, defaultPath string) (string, bool, error)
	fileFn      func(ctx context.Context, title, defaultPath string, filters []ShellFileFilter) (string, bool, error)
}

func (h stubShellDialogHost) Confirm(ctx context.Context, message, title string) (bool, error) {
	if h.confirmFn != nil {
		return h.confirmFn(ctx, message, title)
	}
	return false, nil
}

func (h stubShellDialogHost) Alert(ctx context.Context, message, title string) error {
	if h.alertFn != nil {
		return h.alertFn(ctx, message, title)
	}
	return nil
}

func (h stubShellDialogHost) PickDirectory(ctx context.Context, title, defaultPath string) (string, bool, error) {
	if h.directoryFn != nil {
		return h.directoryFn(ctx, title, defaultPath)
	}
	return "", true, nil
}

func (h stubShellDialogHost) PickFile(ctx context.Context, title, defaultPath string, filters []ShellFileFilter) (string, bool, error) {
	if h.fileFn != nil {
		return h.fileFn(ctx, title, defaultPath, filters)
	}
	return "", true, nil
}

func desktopDialogJSONBody(t *testing.T, payload map[string]any) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

func TestDesktopDialogConfirmUnavailableWithoutHost(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/confirm", desktopDialogJSONBody(t, map[string]any{"message": "sure?"}))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:7788"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDesktopDialogConfirmLocal(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	var seen string
	app.SetShellDialogHost(stubShellDialogHost{
		confirmFn: func(_ context.Context, message, _ string) (bool, error) {
			seen = message
			return true, nil
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/confirm", desktopDialogJSONBody(t, map[string]any{"message": "delete project?"}))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:7788"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(localTokenHeader, app.localToken)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["accepted"] != true {
		t.Fatalf("body=%v", body)
	}
	if seen != "delete project?" {
		t.Fatalf("seen message %q", seen)
	}

	// Port-forward style loopback without token must not open dialogs.
	noToken := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/confirm", desktopDialogJSONBody(t, map[string]any{"message": "delete project?"}))
	noToken.RemoteAddr = "127.0.0.1:12345"
	noToken.Host = "127.0.0.1:7788"
	noToken.Header.Set("Content-Type", "application/json")
	noTokenRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(noTokenRec, noToken)
	if noTokenRec.Code == http.StatusOK {
		t.Fatal("loopback without local token must not open desktop dialogs")
	}
}

func TestDesktopDialogRejectsRemote(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	app.SetShellDialogHost(stubShellDialogHost{
		confirmFn: func(context.Context, string, string) (bool, error) {
			t.Fatal("host must not be called for remote peers")
			return false, nil
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/confirm", desktopDialogJSONBody(t, map[string]any{"message": "no"}))
	req.RemoteAddr = "203.0.113.10:4444"
	req.Host = "example.com"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("remote must not reach desktop dialogs, status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDesktopDialogAlertLocal(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	called := false
	app.SetShellDialogHost(stubShellDialogHost{
		alertFn: func(_ context.Context, message, _ string) error {
			called = message == "hello"
			return nil
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/alert", desktopDialogJSONBody(t, map[string]any{"message": "hello"}))
	req.RemoteAddr = "127.0.0.1:9"
	req.Host = "127.0.0.1:7788"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(localTokenHeader, app.localToken)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("alert host was not called")
	}
}

func TestDesktopDialogOpenDirectoryLocal(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	app.SetShellDialogHost(stubShellDialogHost{
		directoryFn: func(_ context.Context, title, defaultPath string) (string, bool, error) {
			if title == "" {
				t.Fatal("expected title")
			}
			if defaultPath != "/tmp" {
				t.Fatalf("defaultPath=%q", defaultPath)
			}
			return "/Users/demo/project", false, nil
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/open-directory", desktopDialogJSONBody(t, map[string]any{
		"title":       "Pick",
		"defaultPath": "/tmp",
	}))
	req.RemoteAddr = "127.0.0.1:9"
	req.Host = "127.0.0.1:7788"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(localTokenHeader, app.localToken)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["canceled"] != false || body["path"] != "/Users/demo/project" {
		t.Fatalf("body=%v", body)
	}
}

func TestDesktopDialogOpenFileCanceled(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	app.SetShellDialogHost(stubShellDialogHost{
		fileFn: func(context.Context, string, string, []ShellFileFilter) (string, bool, error) {
			return "", true, nil
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/open-file", desktopDialogJSONBody(t, map[string]any{
		"title":   "Open",
		"filters": []map[string]string{{"name": "JSON", "pattern": "*.json"}},
	}))
	req.RemoteAddr = "127.0.0.1:9"
	req.Host = "127.0.0.1:7788"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(localTokenHeader, app.localToken)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["canceled"] != true {
		t.Fatalf("body=%v", body)
	}
}

// These routes are already gated on a loopback peer and the process-local token,
// so the bound is defence in depth: a buggy or hostile local client must not be
// able to grow the desktop process by streaming an endless body into a decoder.
func TestDesktopDialogRejectsOversizedBody(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	called := false
	app.SetShellDialogHost(stubShellDialogHost{
		confirmFn: func(_ context.Context, _, _ string) (bool, error) {
			called = true
			return true, nil
		},
	})
	// Valid JSON, just far larger than any dialog message.
	oversized := `{"message":"` + strings.Repeat("a", maxShellRequestBytes+1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/confirm", strings.NewReader(oversized))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:7788"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(localTokenHeader, app.localToken)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body must be rejected, got status %d body %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("an oversized body must not reach the dialog host")
	}
}

// The picker routes accept an absent body on purpose: opening a picker with no
// title is valid. Bounding the body must not have turned that into an error.
func TestDesktopDialogPickerAcceptsEmptyBody(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	app.SetShellDialogHost(stubShellDialogHost{
		directoryFn: func(_ context.Context, _, _ string) (string, bool, error) {
			return "C:\\chosen", false, nil
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/dialog/open-directory", http.NoBody)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = "127.0.0.1:7788"
	req.Header.Set(localTokenHeader, app.localToken)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("an empty picker body must still work, got status %d body %s", rec.Code, rec.Body.String())
	}
}
