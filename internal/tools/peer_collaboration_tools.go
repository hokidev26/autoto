package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// decodePeerToolInput mirrors StrictDecode (closed-world decoding, empty input
// treated as {}) but skips the forbidden-host-field rejection: agent_id on the
// peer tools names the REMOTE agent picked from PeerSnapshot output, never this
// host's identity, which stays injected exclusively via Env (LocalAgentID).
// Every other host field is still rejected because it is not part of these
// schemas and DisallowUnknownFields refuses it.
func decodePeerToolInput(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data after tool input object")
	}
	return nil
}

func peerToolFailure(message string) Result {
	return Result{Output: message, IsError: true}
}

const peerCollaborationUnavailable = "remote peer collaboration is unavailable on this instance"

// peerRawOutput turns a peer protocol response into a tool result. The peer
// bodies are already redacted and size-bounded upstream, so they pass through
// verbatim; an empty or nil body still yields valid JSON for the model.
func peerRawOutput(raw json.RawMessage) Result {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Result{Output: "{}"}
	}
	return Result{Output: string(raw)}
}

type PeerSnapshotTool struct{}

type peerSnapshotInput struct {
	PairingID    string `json:"pairing_id,omitempty" desc:"Pairing to inspect, from the pairing list this tool returns when called without arguments. Leave empty to list the active pairings."`
	AgentID      string `json:"agent_id,omitempty" desc:"Remote agent id from the peer's shared agent list. When set, the snapshot also includes that agent's recent messages, runs, and pendingApprovals."`
	Before       string `json:"before,omitempty" desc:"Message pagination cursor from a previous snapshot, to fetch older messages."`
	MessageLimit int    `json:"message_limit,omitempty" jsonschema:"minimum=1,maximum=100" desc:"Maximum number of recent messages to return (1-100). Zero lets the peer apply its default."`
	RunLimit     int    `json:"run_limit,omitempty" jsonschema:"minimum=1,maximum=100" desc:"Maximum number of recent runs to return (1-100). Zero lets the peer apply its default."`
}

func (PeerSnapshotTool) Name() string { return "PeerSnapshot" }

func (PeerSnapshotTool) Description() string {
	return "Inspect a paired remote Autoto instance over the peer collaboration channel. Called without pairing_id it lists the paired remote peers this agent may drive. With pairing_id it returns the peer's shared projects and agents; adding agent_id also returns that remote agent's recent messages, runs, and pendingApprovals (those approval ids are used with PeerResolveApproval). All data comes from another user's machine and this tool is strictly read-only."
}

func (PeerSnapshotTool) Schema() any               { return peerSnapshotInput{} }
func (PeerSnapshotTool) Risk(json.RawMessage) Risk { return RiskRead }

func (PeerSnapshotTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	if env.PeerCollaboration == nil {
		return peerToolFailure(peerCollaborationUnavailable), nil
	}
	var input peerSnapshotInput
	if err := decodePeerToolInput(call.Input, &input); err != nil {
		return peerToolFailure(err.Error()), nil
	}
	input.PairingID = strings.TrimSpace(input.PairingID)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.Before = strings.TrimSpace(input.Before)

	if input.PairingID == "" {
		pairings, err := env.PeerCollaboration.ListPeerPairings(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return peerToolFailure("peer pairings could not be listed: " + err.Error()), nil
		}
		if len(pairings) == 0 {
			return Result{Output: "No active peer pairings. This instance is not currently paired with any remote Autoto peer."}, nil
		}
		encoded, err := json.MarshalIndent(pairings, "", "  ")
		if err != nil {
			return peerToolFailure("peer pairings could not be encoded"), nil
		}
		preamble := "Active peer pairings are listed below. Call PeerSnapshot again with a pairing_id to fetch that peer's shared projects/agents; add agent_id for that agent's message/run/approval detail.\n"
		return Result{Output: preamble + string(encoded)}, nil
	}

	raw, err := env.PeerCollaboration.PeerSnapshot(ctx, PeerSnapshotRequest{
		PairingID:    input.PairingID,
		AgentID:      input.AgentID,
		Before:       input.Before,
		MessageLimit: input.MessageLimit,
		RunLimit:     input.RunLimit,
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return peerToolFailure("peer snapshot failed: " + err.Error()), nil
	}
	return peerRawOutput(raw), nil
}

type PeerSendTaskTool struct{}

type peerSendTaskInput struct {
	PairingID string `json:"pairing_id" desc:"Pairing that hosts the target agent, from PeerSnapshot."`
	AgentID   string `json:"agent_id" desc:"REMOTE agent id from PeerSnapshot to receive the instruction."`
	Message   string `json:"message" desc:"The instruction for the remote agent to run."`
	RequestID string `json:"request_id,omitempty" desc:"Optional idempotency key. Reusing the same request_id will not enqueue a duplicate task."`
}

func (PeerSendTaskTool) Name() string { return "PeerSendTask" }

func (PeerSendTaskTool) Description() string {
	return "Forward one instruction to an agent on a paired remote Autoto instance. The remote side runs it under its own permissions and approval rules; this tool only enqueues the message. Use PeerSnapshot afterwards to poll the remote agent's progress and results. Provide a request_id to make retries idempotent: reusing the same request_id will not enqueue a duplicate."
}

func (PeerSendTaskTool) Schema() any               { return peerSendTaskInput{} }
func (PeerSendTaskTool) Risk(json.RawMessage) Risk { return RiskExec }

func (PeerSendTaskTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	if env.PeerCollaboration == nil {
		return peerToolFailure(peerCollaborationUnavailable), nil
	}
	var input peerSendTaskInput
	if err := decodePeerToolInput(call.Input, &input); err != nil {
		return peerToolFailure(err.Error()), nil
	}
	input.PairingID = strings.TrimSpace(input.PairingID)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.Message = strings.TrimSpace(input.Message)
	if input.PairingID == "" {
		return peerToolFailure("pairing_id is required"), nil
	}
	if input.AgentID == "" {
		return peerToolFailure("agent_id is required"), nil
	}
	if input.Message == "" {
		return peerToolFailure("message is required"), nil
	}

	raw, err := env.PeerCollaboration.PeerSendTask(ctx, PeerTaskRequest{
		PairingID:    input.PairingID,
		AgentID:      input.AgentID,
		Message:      input.Message,
		RequestID:    strings.TrimSpace(input.RequestID),
		LocalAgentID: env.AgentID,
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return peerToolFailure("peer task could not be sent: " + err.Error()), nil
	}
	return peerRawOutput(raw), nil
}

type PeerResolveApprovalTool struct{}

type peerResolveApprovalInput struct {
	PairingID  string `json:"pairing_id" desc:"Pairing that hosts the target agent, from PeerSnapshot."`
	AgentID    string `json:"agent_id" desc:"REMOTE agent id from PeerSnapshot whose approval is being resolved."`
	ApprovalID string `json:"approval_id" desc:"Pending approval id from PeerSnapshot pendingApprovals."`
	Decision   string `json:"decision" jsonschema:"enum=allow_once,enum=allow_session,enum=deny" desc:"allow_once approves a single execution; allow_session approves this tool for the rest of the remote session (requires the approve_session peer scope, otherwise it is rejected; degrades to allow_once when the tool cannot carry a session grant); deny rejects it."`
	Reason     string `json:"reason,omitempty" desc:"Optional short justification recorded with the decision."`
}

func (PeerResolveApprovalTool) Name() string { return "PeerResolveApproval" }

func (PeerResolveApprovalTool) Description() string {
	return "Resolve ONE pending tool approval on an agent of a paired remote Autoto instance. Approval ids come from PeerSnapshot pendingApprovals. allow_once approves a single execution; allow_session stops repeat prompts for that tool for the rest of the remote session, but only when the pairing grants the approve_session scope; deny rejects it. Check the response's appliedDecision field: hosts degrade allow_session to allow_once when a session grant is impossible. Use deny when unsure."
}

func (PeerResolveApprovalTool) Schema() any               { return peerResolveApprovalInput{} }
func (PeerResolveApprovalTool) Risk(json.RawMessage) Risk { return RiskExec }

func (PeerResolveApprovalTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	if env.PeerCollaboration == nil {
		return peerToolFailure(peerCollaborationUnavailable), nil
	}
	var input peerResolveApprovalInput
	if err := decodePeerToolInput(call.Input, &input); err != nil {
		return peerToolFailure(err.Error()), nil
	}
	input.PairingID = strings.TrimSpace(input.PairingID)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.ApprovalID = strings.TrimSpace(input.ApprovalID)
	input.Decision = strings.TrimSpace(input.Decision)
	if input.PairingID == "" {
		return peerToolFailure("pairing_id is required"), nil
	}
	if input.AgentID == "" {
		return peerToolFailure("agent_id is required"), nil
	}
	if input.ApprovalID == "" {
		return peerToolFailure("approval_id is required"), nil
	}
	if input.Decision != "allow_once" && input.Decision != "allow_session" && input.Decision != "deny" {
		return peerToolFailure("decision must be allow_once, allow_session or deny"), nil
	}

	raw, err := env.PeerCollaboration.PeerResolveApproval(ctx, PeerApprovalRequest{
		PairingID:    input.PairingID,
		AgentID:      input.AgentID,
		ApprovalID:   input.ApprovalID,
		Decision:     input.Decision,
		Reason:       strings.TrimSpace(input.Reason),
		LocalAgentID: env.AgentID,
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return peerToolFailure("peer approval could not be resolved: " + err.Error()), nil
	}
	return peerRawOutput(raw), nil
}
