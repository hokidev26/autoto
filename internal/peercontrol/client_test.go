package peercontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type peerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f peerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientRejectsUnsafeOriginsAndDefaultsToSecureTransport(t *testing.T) {
	identity := testIdentity(t)
	for _, origin := range []string{
		"http://example.com",
		"http://127.0.0.1:8080",
		"https://user@example.com",
		"https://example.com/",
		"https://example.com/path",
		"https://example.com?query=1",
		"https://example.com#fragment",
		"ftp://example.com",
	} {
		t.Run(origin, func(t *testing.T) {
			if _, err := NewClient(ClientOptions{Origin: origin, Identity: identity, PeerIdentity: identity.Public()}); err == nil {
				t.Fatalf("unsafe origin %q was accepted", origin)
			}
		})
	}
	if _, err := NewClient(ClientOptions{Origin: "http://localhost:8080", Identity: identity, PeerIdentity: identity.Public(), AllowLoopbackHTTPForTests: true}); err == nil {
		t.Fatal("hostname loopback HTTP was accepted by the test-only option")
	}
	if _, err := NewClient(ClientOptions{Origin: "http://[::1]:8080", Identity: identity, PeerIdentity: identity.Public(), AllowLoopbackHTTPForTests: true}); err != nil {
		t.Fatalf("explicit IPv6 loopback HTTP was rejected: %v", err)
	}

	client, err := NewClient(ClientOptions{Origin: "https://example.com", Identity: identity, PeerIdentity: identity.Public()})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("default client did not enforce a TLS minimum version")
	}
	if client.httpClient.Timeout <= 0 || client.httpClient.CheckRedirect == nil {
		t.Fatal("default client did not enforce total timeout and redirect policy")
	}
}

func TestClientExplicitLoopbackHTTPAndStrictHost(t *testing.T) {
	identity := testIdentity(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin, _ := url.Parse(server.URL)
		if request.Host != origin.Host {
			t.Errorf("Host = %q, want %q", request.Host, origin.Host)
		}
		if request.URL.Path != pollClaimEndpoint || request.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request path/auth: %s %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(PollClaimResponse{
			ProtocolVersion: ProtocolVersion,
			InvitationID:    "invite-1",
			Status:          "claimed",
			Revision:        2,
		})
	}))
	defer server.Close()

	client := newLoopbackPeerClient(t, server, identity, ClientOptions{})
	response, err := client.PollClaim(context.Background(), testPollClaimRequest(t, identity))
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "claimed" || response.Revision != 2 {
		t.Fatalf("unexpected poll response: %+v", response)
	}
}

func TestClientClaimRequiresRawSecretAndPollUsesFreshSignedProof(t *testing.T) {
	controller := testIdentity(t)
	host := testIdentity(t)
	secret := bytes.Repeat([]byte{0x5a}, 32)
	var claimProof SignedPairingClaim
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case claimEndpoint:
			var claimRequest ClaimRequest
			decoder := json.NewDecoder(request.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&claimRequest); err != nil {
				t.Error(err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			decoded, err := DecodeInvitationSecretToken(claimRequest.Secret)
			if err != nil || !bytes.Equal(decoded, secret) || HashInvitationSecretHex(decoded) != claimRequest.Proof.Claim.SecretHash {
				t.Errorf("claim secret did not match signed hash: %v", err)
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			if _, err := VerifyPairingClaim(claimRequest.Proof, time.Now().UTC()); err != nil {
				t.Errorf("claim proof did not verify: %v", err)
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			claimProof = claimRequest.Proof
			_ = json.NewEncoder(writer).Encode(ClaimResponse{ProtocolVersion: ProtocolVersion, InvitationID: "invite-1", Status: "claimed", Revision: 2})
		case pollClaimEndpoint:
			var pollRequest PollClaimRequest
			if err := json.NewDecoder(request.Body).Decode(&pollRequest); err != nil {
				t.Error(err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			verified, err := VerifyPairingClaim(pollRequest.Proof, time.Now().UTC())
			if err != nil || verified.Fingerprint != claimProof.Claim.Fingerprint || verified.Nonce == claimProof.Claim.Nonce {
				t.Errorf("poll proof was not fresh and bound to the claimant: verified=%+v err=%v", verified, err)
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(PollClaimResponse{ProtocolVersion: ProtocolVersion, InvitationID: "invite-1", Status: "claimed", Revision: 2})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	envelope, err := NewInvitationEnvelope(server.URL, "invite-1", secret, host.Public(), time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	client := newLoopbackPeerClient(t, server, controller, ClientOptions{PeerIdentity: host.Public()})
	prepared, err := client.PrepareClaim(envelope, "Controller", "controller-installation")
	if err != nil {
		t.Fatal(err)
	}
	withoutSecret := prepared
	withoutSecret.Secret = ""
	if _, err := client.Claim(context.Background(), withoutSecret); !errors.Is(err, ErrProtocol) {
		t.Fatalf("claim without raw secret error = %v", err)
	}
	claimed, err := client.Claim(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != "claimed" || claimed.Revision != 2 {
		t.Fatalf("unexpected claim response: %+v", claimed)
	}
	polled, err := client.PollClaim(context.Background(), PollClaimRequest{Proof: prepared.Proof})
	if err != nil {
		t.Fatal(err)
	}
	if polled.Status != "claimed" || polled.Revision != 2 {
		t.Fatalf("unexpected poll response: %+v", polled)
	}
}

func TestClientAutomaticallySignsChallengeCachesSessionAndSendsBearer(t *testing.T) {
	identity := testIdentity(t)
	token, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	var challengeCalls atomic.Int32
	var establishCalls atomic.Int32
	var taskCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case issueSessionChallengeEndpoint:
			challengeCalls.Add(1)
			_ = json.NewEncoder(writer).Encode(IssueSessionChallengeResponse{Challenge: signedClientTestChallenge(t, identity, "pair-1", server.URL)})
		case establishSessionEndpoint:
			establishCalls.Add(1)
			var establish EstablishSessionRequest
			if err := json.NewDecoder(request.Body).Decode(&establish); err != nil {
				t.Error(err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if err := VerifyChallenge(establish.SignedChallenge, identity.Public().Fingerprint, server.URL, time.Now().UTC()); err != nil {
				t.Errorf("client challenge signature did not verify: %v", err)
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(EstablishSessionResponse{BearerToken: token, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)})
		case sendTaskEndpoint:
			taskCalls.Add(1)
			if got := request.Header.Get("Authorization"); got != "Bearer "+token {
				t.Errorf("Authorization = %q", got)
			}
			_ = json.NewEncoder(writer).Encode(SendTaskResponse{TaskID: "task-1", Status: "accepted", CreatedAt: time.Now().UTC()})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newLoopbackPeerClient(t, server, identity, ClientOptions{})
	for index := 0; index < 2; index++ {
		response, err := client.SendTask(context.Background(), SendTaskRequest{
			PairingID: "pair-1",
			AgentID:   "agent-1",
			Message:   "run tests",
			RequestID: "request-1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.TaskID != "task-1" {
			t.Fatalf("unexpected task response: %+v", response)
		}
	}
	if challengeCalls.Load() != 1 || establishCalls.Load() != 1 || taskCalls.Load() != 2 {
		t.Fatalf("calls challenge=%d establish=%d task=%d", challengeCalls.Load(), establishCalls.Load(), taskCalls.Load())
	}
}

func TestClientDoesNotRetryNonIdempotentRequestAfterUnauthorized(t *testing.T) {
	identity := testIdentity(t)
	token, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	var challengeCalls atomic.Int32
	var establishCalls atomic.Int32
	var taskCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case issueSessionChallengeEndpoint:
			challengeCalls.Add(1)
			_ = json.NewEncoder(writer).Encode(IssueSessionChallengeResponse{Challenge: signedClientTestChallenge(t, identity, "pair-1", server.URL)})
		case establishSessionEndpoint:
			establishCalls.Add(1)
			_ = json.NewEncoder(writer).Encode(EstablishSessionResponse{BearerToken: token, ExpiresAt: time.Now().UTC().Add(time.Minute)})
		case sendTaskEndpoint:
			taskCalls.Add(1)
			writer.WriteHeader(http.StatusUnauthorized)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newLoopbackPeerClient(t, server, identity, ClientOptions{})
	_, err = client.SendTask(context.Background(), SendTaskRequest{PairingID: "pair-1", AgentID: "agent-1", Message: "once", RequestID: "request-1"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("SendTask error = %v", err)
	}
	if challengeCalls.Load() != 1 || establishCalls.Load() != 1 || taskCalls.Load() != 1 {
		t.Fatalf("non-idempotent request was retried: challenge=%d establish=%d task=%d", challengeCalls.Load(), establishCalls.Load(), taskCalls.Load())
	}
}

func TestClientRedirectDoesNotForwardBearer(t *testing.T) {
	identity := testIdentity(t)
	token, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	var attackerCalls atomic.Int32
	var attackerAuthorization atomic.Value
	attacker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attackerCalls.Add(1)
		attackerAuthorization.Store(request.Header.Get("Authorization"))
		writer.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case issueSessionChallengeEndpoint:
			_ = json.NewEncoder(writer).Encode(IssueSessionChallengeResponse{Challenge: signedClientTestChallenge(t, identity, "pair-1", server.URL)})
		case establishSessionEndpoint:
			_ = json.NewEncoder(writer).Encode(EstablishSessionResponse{BearerToken: token, ExpiresAt: time.Now().UTC().Add(time.Minute)})
		case sendTaskEndpoint:
			http.Redirect(writer, request, attacker.URL+"/steal", http.StatusTemporaryRedirect)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newLoopbackPeerClient(t, server, identity, ClientOptions{})
	_, err = client.SendTask(context.Background(), SendTaskRequest{PairingID: "pair-1", AgentID: "agent-1", Message: "do not redirect", RequestID: "request-1"})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("redirect error = %v", err)
	}
	if attackerCalls.Load() != 0 {
		t.Fatalf("redirect target received %d requests with authorization %v", attackerCalls.Load(), attackerAuthorization.Load())
	}
}

func TestClientRejectsUnknownFieldsOversizedBodiesAndChangedFinalOrigin(t *testing.T) {
	identity := testIdentity(t)
	t.Run("unknown field", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, `{"protocolVersion":1,"invitationId":"invite-1","status":"open","revision":1,"unexpected":true}`)
		}))
		defer server.Close()
		client := newLoopbackPeerClient(t, server, identity, ClientOptions{})
		if _, err := client.PollClaim(context.Background(), testPollClaimRequest(t, identity)); !errors.Is(err, ErrProtocol) {
			t.Fatalf("unknown field error = %v", err)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, strings.Repeat("x", 128))
		}))
		defer server.Close()
		client := newLoopbackPeerClient(t, server, identity, ClientOptions{MaxResponseBytes: 64})
		if _, err := client.PollClaim(context.Background(), testPollClaimRequest(t, identity)); !errors.Is(err, ErrProtocol) {
			t.Fatalf("oversized body error = %v", err)
		}
	})

	t.Run("changed final origin", func(t *testing.T) {
		transport := peerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			changed := request.Clone(request.Context())
			changed.URL, _ = url.Parse("https://other.example" + request.URL.Path)
			changed.Host = "other.example"
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"protocolVersion":1,"invitationId":"invite-1","status":"open","revision":1}`)),
				Header:     make(http.Header),
				Request:    changed,
			}, nil
		})
		client, err := NewClient(ClientOptions{Origin: "https://host.example", Identity: identity, PeerIdentity: identity.Public(), Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.PollClaim(context.Background(), testPollClaimRequest(t, identity)); !errors.Is(err, ErrProtocol) {
			t.Fatalf("changed final origin error = %v", err)
		}
	})
}

func TestClientEnforcesTotalTimeout(t *testing.T) {
	identity := testIdentity(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
			return
		case <-time.After(time.Second):
			_, _ = io.WriteString(writer, `{"protocolVersion":1,"invitationId":"invite-1","status":"open","revision":1}`)
		}
	}))
	defer server.Close()
	client := newLoopbackPeerClient(t, server, identity, ClientOptions{Timeout: 20 * time.Millisecond})
	started := time.Now()
	_, err := client.PollClaim(context.Background(), testPollClaimRequest(t, identity))
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("timeout error = %v", err)
	}
	if time.Since(started) >= 500*time.Millisecond {
		t.Fatal("client total timeout was not enforced")
	}
}

func signedClientTestChallenge(t *testing.T, host *Identity, pairingID, origin string) SessionChallenge {
	t.Helper()
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	returnValue, err := SignHostSessionChallenge(host, SessionChallenge{
		ProtocolVersion: ProtocolVersion,
		PairingID:       pairingID,
		Challenge:       challenge,
		ExpiresAt:       time.Now().UTC().Add(time.Minute),
		EndpointOrigin:  origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return returnValue
}

func testPollClaimRequest(t *testing.T, identity *Identity) PollClaimRequest {
	t.Helper()
	claim, err := NewPairingClaim("invite-1", strings.Repeat("a", 64), "Controller", "controller-installation", identity, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := identity.SignPairingClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	return PollClaimRequest{Proof: proof}
}

func newLoopbackPeerClient(t *testing.T, server *httptest.Server, identity *Identity, extra ClientOptions) *Client {
	t.Helper()
	extra.Origin = server.URL
	extra.Identity = identity
	if extra.PeerIdentity.PublicKey == "" {
		extra.PeerIdentity = identity.Public()
	}
	extra.Transport = server.Client().Transport
	extra.AllowLoopbackHTTPForTests = true
	client, err := NewClient(extra)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
