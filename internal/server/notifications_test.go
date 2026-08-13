package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/network"
)

const testWebhookHost = "hook.autoto.example"

// webhookResolverFunc lets tests answer the hardened transport's DNS lookups
// with deterministic addresses.
type webhookResolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f webhookResolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

// testWebhookEgressOptions maps testWebhookHost to a public address so the
// public-direct policy accepts it, then routes the pinned dial to the loopback
// test server standing in for the public webhook endpoint.
func testWebhookEgressOptions(t *testing.T, backend *httptest.Server) []network.Option {
	t.Helper()
	backendAddr := backend.Listener.Addr().String()
	return []network.Option{
		network.WithResolver(webhookResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host != testWebhookHost {
				return nil, errors.New("unexpected lookup for host " + host)
			}
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		})),
		network.WithDialContext(func(ctx context.Context, netw, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, netw, backendAddr)
		}),
	}
}

func TestNotificationSettingsAPIAndTestWebhook(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	received := make(chan webhookPayload, 1)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("User-Agent"); got != "Autoto-Webhook/1.0" {
			t.Errorf("unexpected webhook user agent %q", got)
		}
		var payload webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook payload: %v", err)
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	app := New(config.Config{}, store, nil, nil)
	app.SetWebhookNotifier(newWebhookNotifier(store, testWebhookEgressOptions(t, webhook)...))
	routes := app.Routes()

	webhookURL := "http://" + testWebhookHost + "/hook"
	putJSON(t, routes, http.MethodPut, "/api/notifications/settings", notificationSettingsPayload{Enabled: true, WebhookURL: webhookURL, NotifyOnApproval: true, NotifyOnDone: true, NotifyOnError: true}, http.StatusOK)

	recorder := httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/notifications/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected settings 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var settings db.NotificationSettings
	if err := json.NewDecoder(recorder.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || settings.WebhookURL != webhookURL || !settings.NotifyOnApproval || !settings.NotifyOnDone || !settings.NotifyOnError {
		t.Fatalf("unexpected notification settings: %+v", settings)
	}

	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPost, "/api/notifications/test", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected test 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	select {
	case payload := <-received:
		if payload.Kind != "notification.test" || payload.Event != "test" || payload.Meta["source"] != "Autoto" {
			t.Fatalf("unexpected webhook payload: %+v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook test payload")
	}
}

func TestNotificationSettingsRejectsInvalidWebhookURL(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	routes := app.Routes()
	for _, webhookURL := range []string{
		"file:///tmp/hook",
		"https://user:password@example.test/hook",
		"https://example.test/hook?token=secret",
		"https://example.test/hook#fragment",
		"http://169.254.169.254/latest/meta-data",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://127.0.0.1:8080/hook",
		"http://[::1]:8080/hook",
		"http://localhost:8080/hook",
		"http://0.0.0.0/hook",
		"http://10.0.0.8/hook",
		"http://192.168.1.20/hook",
		"http://172.16.0.1/hook",
		"http://100.100.100.200/latest/meta-data",
		"http://192.0.0.192/opc/v1/instance/",
	} {
		putJSON(t, routes, http.MethodPut, "/api/notifications/settings", notificationSettingsPayload{Enabled: true, WebhookURL: webhookURL, NotifyOnApproval: true}, http.StatusBadRequest)
	}
}

func TestWebhookNotifierSendsRunNotification(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	received := make(chan webhookPayload, 1)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload webhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook payload: %v", err)
		}
		received <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer webhook.Close()

	_, _, agent, err := store.CreateProject(ctx, "Notify", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateNotificationSettings(ctx, db.NotificationSettings{Enabled: true, WebhookURL: "http://" + testWebhookHost + "/notify", NotifyOnApproval: true, NotifyOnDone: true, NotifyOnError: true}); err != nil {
		t.Fatal(err)
	}

	notifier := newWebhookNotifier(store, testWebhookEgressOptions(t, webhook)...)
	notifier.Notify(ctx, agentpkg.NotificationEvent{Event: "completed", RunID: run.ID, AgentID: agent.ID, Status: "completed"})
	select {
	case payload := <-received:
		if payload.Kind != "run.completed" || payload.RunID != run.ID || payload.Summary == nil || payload.Summary.MessageCount != 1 {
			t.Fatalf("unexpected run notification payload: %+v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for run notification payload")
	}
}

// TestWebhookNotifierRefusesForbiddenTargets covers stored settings that never
// went through the settings API (legacy rows, direct DB writes): loopback,
// private, and metadata destinations must be refused at send time without a
// single connection attempt, whether given as literals or as hostnames that
// resolve into forbidden space.
func TestWebhookNotifierRefusesForbiddenTargets(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var dialCount atomic.Int32
	notifier := newWebhookNotifier(store,
		network.WithResolver(webhookResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host == "internal.autoto.example" {
				return []net.IPAddr{{IP: net.ParseIP("10.0.0.8")}}, nil
			}
			return nil, errors.New("unexpected lookup for host " + host)
		})),
		network.WithDialContext(func(context.Context, string, string) (net.Conn, error) {
			dialCount.Add(1)
			return nil, errors.New("forbidden target must not be dialed")
		}),
	)

	for _, test := range []struct {
		webhookURL       string
		wantPolicyDenied bool
	}{
		{webhookURL: "http://127.0.0.1:9/hook"},
		{webhookURL: "http://[::1]:9/hook"},
		{webhookURL: "http://localhost:9/hook"},
		{webhookURL: "http://10.0.0.8/hook"},
		{webhookURL: "http://192.168.1.20/hook"},
		{webhookURL: "http://172.16.0.1/hook"},
		{webhookURL: "http://169.254.169.254/latest/meta-data"},
		{webhookURL: "http://100.100.100.200/latest/meta-data"},
		{webhookURL: "http://192.0.0.192/opc/v1/instance/"},
		// Public-looking hostname whose DNS answer is private: refused by the
		// hardened transport, not by string validation.
		{webhookURL: "http://internal.autoto.example/hook", wantPolicyDenied: true},
	} {
		if _, err := store.UpdateNotificationSettings(ctx, db.NotificationSettings{Enabled: true, WebhookURL: test.webhookURL, NotifyOnApproval: true, NotifyOnDone: true, NotifyOnError: true}); err != nil {
			t.Fatal(err)
		}
		err := notifier.SendTest(ctx)
		if err == nil {
			t.Fatalf("expected webhook to %q to be refused", test.webhookURL)
		}
		if test.wantPolicyDenied && !errors.Is(err, network.ErrDestinationDenied) {
			t.Fatalf("expected policy denial for %q, got %v", test.webhookURL, err)
		}
	}
	if got := dialCount.Load(); got != 0 {
		t.Fatalf("forbidden webhook targets reached the dialer %d times", got)
	}
}

// TestWebhookNotifierRefusesRedirectToLoopback proves a public webhook cannot
// bounce the notifier into loopback via an HTTP redirect: every hop is
// revalidated under the same public-direct policy.
func TestWebhookNotifierRefusesRedirectToLoopback(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	stolen := make(chan struct{}, 1)
	var redirectTarget string
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/steal" {
			stolen <- struct{}{}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, redirectTarget, http.StatusFound)
	}))
	defer webhook.Close()
	redirectTarget = webhook.URL + "/steal"

	if _, err := store.UpdateNotificationSettings(ctx, db.NotificationSettings{Enabled: true, WebhookURL: "http://" + testWebhookHost + "/hook", NotifyOnApproval: true, NotifyOnDone: true, NotifyOnError: true}); err != nil {
		t.Fatal(err)
	}
	notifier := newWebhookNotifier(store, testWebhookEgressOptions(t, webhook)...)
	err = notifier.SendTest(ctx)
	if !errors.Is(err, network.ErrDestinationDenied) {
		t.Fatalf("expected redirect to loopback to be denied, got %v", err)
	}
	select {
	case <-stolen:
		t.Fatal("redirect to loopback was followed")
	default:
	}
}

func putJSON(t *testing.T, handler http.Handler, method, path string, payload any, wantStatus int) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := newTestRequest(method, path, stringsReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s expected %d, got %d: %s", method, path, wantStatus, recorder.Code, recorder.Body.String())
	}
}

func stringsReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }
