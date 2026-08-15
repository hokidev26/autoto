package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
)

type wsTestFrame struct {
	Type           string `json:"type"`
	Protocol       int    `json:"protocol"`
	StreamSession  string `json:"streamSession"`
	LatestSequence uint64 `json:"latestSequence"`
	Reason         string `json:"reason"`
	Sequence       uint64 `json:"sequence"`
	Text           string `json:"text"`
}

func TestAgentWSProtocol2ReplaysFromCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	app, server, store, agent := newWSTestServer(t, ctx)
	defer store.Close()
	defer server.Close()

	app.hub.Publish(agentpkg.Event{Type: "agent.text", AgentID: agent.ID, Text: "one"})
	app.hub.Publish(agentpkg.Event{Type: "agent.text", AgentID: agent.ID, Text: "two"})
	bootstrap := app.hub.SubscribeProtocol(ctx, agentpkg.SubscribeOptions{AgentID: agent.ID})

	conn := dialWSTest(t, ctx, server.URL, app.localToken, url.Values{
		"id":            {agent.ID},
		"protocol":      {"2"},
		"streamSession": {bootstrap.StreamSession},
		"after":         {"1"},
	})
	defer conn.CloseNow()

	connected := readWSTestFrame(t, ctx, conn)
	if connected.Type != "connected" || connected.Protocol != agentpkg.ProtocolVersion || connected.StreamSession != bootstrap.StreamSession || connected.LatestSequence != 2 {
		t.Fatalf("unexpected connected frame: %+v", connected)
	}
	replay := readWSTestFrame(t, ctx, conn)
	if replay.Type != "agent.text" || replay.Text != "two" || replay.Protocol != agentpkg.ProtocolVersion || replay.StreamSession != bootstrap.StreamSession || replay.Sequence != 2 {
		t.Fatalf("unexpected replay frame: %+v", replay)
	}
}

func TestAgentWSProtocol2ResyncAndAgentValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	app, server, store, agent := newWSTestServer(t, ctx)
	defer store.Close()
	defer server.Close()

	app.hub.Publish(agentpkg.Event{Type: "agent.text", AgentID: agent.ID, Text: "one"})
	conn := dialWSTest(t, ctx, server.URL, app.localToken, url.Values{
		"id":            {agent.ID},
		"protocol":      {"2"},
		"streamSession": {"wrong-session"},
		"after":         {"1"},
	})
	frame := readWSTestFrame(t, ctx, conn)
	conn.CloseNow()
	if frame.Type != "resync_required" || frame.Protocol != agentpkg.ProtocolVersion || frame.Reason != string(agentpkg.ResyncSessionMismatch) || frame.StreamSession == "" {
		t.Fatalf("unexpected resync frame: %+v", frame)
	}

	query := url.Values{"id": {"missing-agent"}, "protocol": {"2"}}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/agent?" + query.Encode()
	_, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{server.URL}, localTokenHeader: []string{app.localToken}}})
	if err == nil {
		t.Fatal("expected unknown agent websocket dial to fail")
	}
	if response == nil || response.StatusCode != http.StatusNotFound {
		if response == nil {
			t.Fatal("expected unknown-agent HTTP response")
		}
		t.Fatalf("expected 404 for unknown agent, got %d", response.StatusCode)
	}
}

func TestRemoteAgentWebSocketClosesWhenSessionIsRevoked(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "remote-ws-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Remote WebSocket", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret", AllowRemoteFullAccess: true, DefaultRemoteAccessMode: remoteAccessModeFull}}, store, nil, agentpkg.NewHub())
	token, _, err := app.newRemoteAccessSession(remoteAccessModeFull)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Routes())
	defer server.Close()

	query := url.Values{"id": {agent.ID}, "protocol": {"2"}}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/agent?" + query.Encode()
	headers := http.Header{
		"Origin":            []string{"https://remote.example.test"},
		"CF-Connecting-IP":  []string{"203.0.113.45"},
		"X-Forwarded-Host":  []string{"remote.example.test"},
		"X-Forwarded-Proto": []string{"https"},
		"Cookie":            []string{(&http.Cookie{Name: remoteAccessCookieName, Value: token}).String()},
	}
	conn, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		if response != nil {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("remote websocket dial failed with status %d: %s: %v", response.StatusCode, body, err)
		}
		t.Fatal(err)
	}
	defer conn.CloseNow()
	connected := readWSTestFrame(t, ctx, conn)
	if connected.Type != "connected" {
		t.Fatalf("unexpected connected frame: %+v", connected)
	}

	app.revokeRemoteAccessSession(token)
	readCtx, readCancel := context.WithTimeout(ctx, time.Second)
	defer readCancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("revoked remote session left the Agent websocket open")
	}
}

func TestAgentWebSocketClosesOnlyRevokedUserSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "user-session-ws-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user, err := store.CreateUser(ctx, "socket-user", "test-password-hash")
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProjectForUser(ctx, user.ID, "User Session WebSocket", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	firstToken := "first-user-session-token"
	secondToken := "second-user-session-token"
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	for _, token := range []string{firstToken, secondToken} {
		if _, err := store.CreateAuthSession(ctx, db.AuthSession{
			UserID:    user.ID,
			TokenHash: db.HashSessionToken(token),
			ExpiresAt: expiresAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	app := New(config.Config{}, store, nil, agentpkg.NewHub())
	server := httptest.NewServer(app.Routes())
	defer server.Close()

	first := dialWSTestWithCookies(t, ctx, server.URL, app.localToken, url.Values{
		"id":       {agent.ID},
		"protocol": {"2"},
	}, &http.Cookie{Name: authSessionCookieName, Value: firstToken})
	defer first.CloseNow()
	second := dialWSTestWithCookies(t, ctx, server.URL, app.localToken, url.Values{
		"id":       {agent.ID},
		"protocol": {"2"},
	}, &http.Cookie{Name: authSessionCookieName, Value: secondToken})
	defer second.CloseNow()
	if frame := readWSTestFrame(t, ctx, first); frame.Type != "connected" {
		t.Fatalf("unexpected first connected frame: %+v", frame)
	}
	if frame := readWSTestFrame(t, ctx, second); frame.Type != "connected" {
		t.Fatalf("unexpected second connected frame: %+v", frame)
	}

	logoutRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	logoutRequest.Header.Set(localTokenHeader, app.localToken)
	logoutRequest.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: firstToken})
	logoutResponse, err := server.Client().Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(logoutResponse.Body)
		t.Fatalf("logout returned %d: %s", logoutResponse.StatusCode, body)
	}

	firstReadCtx, firstReadCancel := context.WithTimeout(ctx, time.Second)
	defer firstReadCancel()
	if _, _, err := first.Read(firstReadCtx); err == nil {
		t.Fatal("revoked user session left the Agent websocket open")
	}

	app.hub.Publish(agentpkg.Event{Type: "agent.text", AgentID: agent.ID, Text: "still-authorized"})
	secondReadCtx, secondReadCancel := context.WithTimeout(ctx, time.Second)
	defer secondReadCancel()
	frame := readWSTestFrame(t, secondReadCtx, second)
	if frame.Type != "agent.text" || frame.Text != "still-authorized" {
		t.Fatalf("independent user session was interrupted: %+v", frame)
	}
}

func TestAgentWSReleasesRegistrationWhenClientDisconnects(t *testing.T) {
	// websocket.Accept hijacks the connection, so r.Context() no longer
	// cancels when the client goes away. The handler only writes; without
	// conn.CloseRead nothing consumed the client's close frame, and on a quiet
	// agent the goroutine, its hub subscription, and the authorized connection
	// registration all leaked until the next write happened to fail.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "ws-disconnect-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user, err := store.CreateUser(ctx, "disconnect-user", "test-password-hash")
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProjectForUser(ctx, user.ID, "Disconnect WebSocket", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	token := "disconnect-session-token"
	if _, err := store.CreateAuthSession(ctx, db.AuthSession{
		UserID:    user.ID,
		TokenHash: db.HashSessionToken(token),
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, agentpkg.NewHub())
	server := httptest.NewServer(app.Routes())
	defer server.Close()

	conn := dialWSTestWithCookies(t, ctx, server.URL, app.localToken, url.Values{
		"id":       {agent.ID},
		"protocol": {"2"},
	}, &http.Cookie{Name: authSessionCookieName, Value: token})
	defer conn.CloseNow()
	if frame := readWSTestFrame(t, ctx, conn); frame.Type != "connected" {
		t.Fatalf("unexpected connected frame: %+v", frame)
	}

	registeredConnections := func() int {
		app.authSessionMu.Lock()
		defer app.authSessionMu.Unlock()
		total := 0
		for _, connections := range app.authSessionConnections {
			total += len(connections)
		}
		return total
	}
	if got := registeredConnections(); got != 1 {
		t.Fatalf("expected one registered websocket connection, got %d", got)
	}

	// No agent events are flowing; only the client's close frame can tell the
	// handler the connection is gone.
	if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for registeredConnections() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("agent websocket handler did not release its registration after the client disconnected")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgentLiveSnapshotV2RouteReturnsAuthoritativeWatermark(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	app, server, store, agent := newWSTestServer(t, ctx)
	defer store.Close()
	defer server.Close()

	app.hub.Publish(agentpkg.Event{Type: "agent.text", AgentID: agent.ID, Text: "snapshot"})
	for i := 0; i < db.DefaultMessagePageLimit+1; i++ {
		if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: fmt.Sprintf("message-%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v2/agents/"+agent.ID+"/live-snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(localTokenHeader, app.localToken)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	var snapshot agentLiveSnapshotResponse
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Protocol != agentpkg.ProtocolVersion || snapshot.Agent.ID != agent.ID || snapshot.Stream.StreamSession == "" || snapshot.Stream.LatestSequence != 1 {
		t.Fatalf("unexpected live snapshot: %+v", snapshot)
	}
	if len(snapshot.Messages) != db.DefaultMessagePageLimit || !snapshot.MessageHasMoreBefore || snapshot.MessageNextBefore == "" {
		t.Fatalf("expected a bounded live snapshot message window, got count=%d hasMore=%v cursor=%q", len(snapshot.Messages), snapshot.MessageHasMoreBefore, snapshot.MessageNextBefore)
	}
	if snapshot.Messages[0].ContentText != "message-001" || snapshot.Messages[len(snapshot.Messages)-1].ContentText != "message-100" {
		t.Fatalf("unexpected live snapshot range: first=%q last=%q", snapshot.Messages[0].ContentText, snapshot.Messages[len(snapshot.Messages)-1].ContentText)
	}
}

func TestAgentWSLegacyConnectionStaysRealtimeOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	app, server, store, agent := newWSTestServer(t, ctx)
	defer store.Close()
	defer server.Close()

	app.hub.Publish(agentpkg.Event{Type: "agent.text", AgentID: agent.ID, Text: "before"})
	conn := dialWSTest(t, ctx, server.URL, app.localToken, url.Values{"id": {agent.ID}})
	defer conn.CloseNow()
	connected := readWSTestFrame(t, ctx, conn)
	if connected.Type != "connected" || connected.Protocol != 0 {
		t.Fatalf("unexpected legacy connected frame: %+v", connected)
	}

	app.hub.Publish(agentpkg.Event{Type: "agent.text", AgentID: agent.ID, Text: "after"})
	live := readWSTestFrame(t, ctx, conn)
	if live.Type != "agent.text" || live.Text != "after" || live.Sequence != 2 {
		t.Fatalf("expected only live legacy event, got %+v", live)
	}
}

func newWSTestServer(t *testing.T, ctx context.Context) (*Server, *httptest.Server, *db.Store, db.Agent) {
	t.Helper()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "ws-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "WebSocket", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, agentpkg.NewHub())
	return app, httptest.NewServer(app.Routes()), store, agent
}

func dialWSTest(t *testing.T, ctx context.Context, baseURL, token string, query url.Values) *websocket.Conn {
	t.Helper()
	return dialWSTestWithCookies(t, ctx, baseURL, token, query)
}

func dialWSTestWithCookies(t *testing.T, ctx context.Context, baseURL, token string, query url.Values, cookies ...*http.Cookie) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws/agent?" + query.Encode()
	headers := http.Header{"Origin": []string{baseURL}}
	headers.Set(localTokenHeader, token)
	for _, cookie := range cookies {
		if cookie != nil {
			headers.Add("Cookie", cookie.String())
		}
	}
	conn, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		if response != nil {
			t.Fatalf("websocket dial failed with status %d: %v", response.StatusCode, err)
		}
		t.Fatalf("websocket dial failed: %v", err)
	}
	return conn
}

func readWSTestFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) wsTestFrame {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var frame wsTestFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decode websocket frame %s: %v", string(data), err)
	}
	return frame
}
