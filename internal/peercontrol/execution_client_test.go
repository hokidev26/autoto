package peercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newExecutionClientHost serves the session handshake plus one execution handler,
// so each test below only has to describe the device-facing behaviour it cares
// about.
func newExecutionClientHost(t *testing.T, path string, handler func(http.ResponseWriter, *http.Request, string)) (*Client, *httptest.Server) {
	t.Helper()
	identity := testIdentity(t)
	token, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case issueSessionChallengeEndpoint:
			_ = json.NewEncoder(writer).Encode(IssueSessionChallengeResponse{Challenge: signedClientTestChallenge(t, identity, "pair-1", server.URL)})
		case establishSessionEndpoint:
			_ = json.NewEncoder(writer).Encode(EstablishSessionResponse{BearerToken: token, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)})
		case path:
			handler(writer, request, token)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return newLoopbackPeerClient(t, server, identity, ClientOptions{}), server
}

func TestExecutionHeartbeatRefreshesStaleBearerAndRetriesOnce(t *testing.T) {
	var calls atomic.Int32
	client, _ := newExecutionClientHost(t, executionHeartbeatEndpoint, func(writer http.ResponseWriter, request *http.Request, token string) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(writer).Encode(ExecutionHeartbeatResponse{
			ProtocolVersion: ProtocolVersion, DeviceID: "device-1", Status: "ready", QueuedTasks: 2, LeaseMaxSeconds: 600, HeartbeatSeconds: 30,
		})
	})

	response, err := client.ExecutionHeartbeat(context.Background(), ExecutionHeartbeatRequest{PairingID: "pair-1", DeviceID: "device-1", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "ready" || response.QueuedTasks != 2 || calls.Load() != 2 {
		t.Fatalf("heartbeat did not recover from a stale bearer: %+v calls=%d", response, calls.Load())
	}
}

func TestClaimExecutionTaskAcceptsEmptyQueueAndRejectsMalformedTasks(t *testing.T) {
	leaseUntil := time.Now().UTC().Add(time.Minute)
	cases := []struct {
		name     string
		task     *ExecutionTask
		wantTask bool
		wantErr  bool
	}{
		{name: "empty queue"},
		{name: "leased task", task: &ExecutionTask{TaskID: "task-1", AgentID: "agent-1", Revision: 2, LeaseUntil: leaseUntil}, wantTask: true},
		{name: "missing task id", task: &ExecutionTask{AgentID: "agent-1", Revision: 2, LeaseUntil: leaseUntil}, wantErr: true},
		{name: "missing agent", task: &ExecutionTask{TaskID: "task-1", Revision: 2, LeaseUntil: leaseUntil}, wantErr: true},
		{name: "unusable revision", task: &ExecutionTask{TaskID: "task-1", AgentID: "agent-1", LeaseUntil: leaseUntil}, wantErr: true},
		{name: "expired lease", task: &ExecutionTask{TaskID: "task-1", AgentID: "agent-1", Revision: 2, LeaseUntil: time.Now().UTC().Add(-time.Minute)}, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			task := testCase.task
			client, _ := newExecutionClientHost(t, executionClaimEndpoint, func(writer http.ResponseWriter, _ *http.Request, _ string) {
				_ = json.NewEncoder(writer).Encode(ExecutionClaimResponse{ProtocolVersion: ProtocolVersion, Task: task})
			})
			response, err := client.ClaimExecutionTask(context.Background(), ExecutionClaimRequest{PairingID: "pair-1", DeviceID: "device-1", LeaseSeconds: 60})
			if testCase.wantErr {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("malformed task was accepted: %+v %v", response.Task, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if testCase.wantTask != (response.Task != nil) {
				t.Fatalf("unexpected task presence: %+v", response.Task)
			}
		})
	}
}

func TestReportExecutionTaskDoesNotRetryAfterUnauthorized(t *testing.T) {
	var calls atomic.Int32
	client, _ := newExecutionClientHost(t, executionReportEndpoint, func(writer http.ResponseWriter, _ *http.Request, _ string) {
		calls.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
	})

	// A report carries an outcome, so replaying it after an authorization failure
	// could double-report work the host already settled.
	if _, err := client.ReportExecutionTask(context.Background(), ExecutionReportRequest{
		PairingID: "pair-1", DeviceID: "device-1", TaskID: "task-1", Revision: 2, Status: "succeeded",
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unexpected report error: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("report was retried %d times", calls.Load())
	}
}
