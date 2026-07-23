package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"autoto/internal/config"
)

func TestTemporaryTunnelInstallRouteIsLocalOnlyAndDoesNotAutoStart(t *testing.T) {
	managedPath := filepath.Join(t.TempDir(), "bin", "cloudflared")
	installer := &fakeCloudflaredInstaller{supported: true, path: managedPath}
	commandCalls := 0
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath:  func(string) (string, error) { return "", errors.New("not found") },
		installer: installer,
		command: func(context.Context, string, ...string) temporaryTunnelProcess {
			commandCalls++
			return newFakeTemporaryTunnelProcess()
		},
	})
	app := New(config.Config{}, nil, nil, nil)
	app.SetTemporaryTunnelManager(manager)

	missingToken := newTestRequest(http.MethodPost, temporaryTunnelInstallPath, nil)
	missingTokenRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(missingTokenRecorder, missingToken)
	if missingTokenRecorder.Code == http.StatusOK {
		t.Fatal("loopback install without the local token must fail")
	}

	remote := newTestRequest(http.MethodPost, temporaryTunnelInstallPath, nil)
	remote.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(remote)
	remote.Header.Set(localTokenHeader, app.localToken)
	remoteRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(remoteRecorder, remote)
	if remoteRecorder.Code == http.StatusOK {
		t.Fatal("remote requests must not install cloudflared")
	}

	request := newTestRequest(http.MethodPost, temporaryTunnelInstallPath, nil)
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var snapshot TemporaryTunnelSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Available || snapshot.Installable || snapshot.Status != temporaryTunnelIdle {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if commandCalls != 0 {
		t.Fatalf("installation unexpectedly started a tunnel: calls=%d", commandCalls)
	}
}

func TestTemporaryTunnelInstallRouteMapsUnsupportedPlatformToConflict(t *testing.T) {
	manager := newTemporaryTunnelManager("127.0.0.1:7788", temporaryTunnelOptions{
		lookPath:  func(string) (string, error) { return "", errors.New("not found") },
		installer: &fakeCloudflaredInstaller{supported: false, path: filepath.Join(t.TempDir(), "bin", "cloudflared")},
	})
	app := New(config.Config{}, nil, nil, nil)
	app.SetTemporaryTunnelManager(manager)
	request := newTestRequest(http.MethodPost, temporaryTunnelInstallPath, nil)
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
