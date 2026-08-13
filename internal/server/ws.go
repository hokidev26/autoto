package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	agentpkg "autoto/internal/agent"
)

type wsControlFrame struct {
	Type           string  `json:"type"`
	Protocol       int     `json:"protocol,omitempty"`
	StreamSession  string  `json:"streamSession,omitempty"`
	OldestSequence *uint64 `json:"oldestSequence,omitempty"`
	LatestSequence *uint64 `json:"latestSequence,omitempty"`
	Resume         string  `json:"resume,omitempty"`
	Reason         string  `json:"reason,omitempty"`
}

func (s *Server) agentWS(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	if agentID == "" {
		agentID = r.URL.Query().Get("id")
	}
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "id query parameter is required")
		return
	}
	if !s.requireAgentAccess(w, r, agentID) {
		return
	}
	if !s.validateWebSocketRequest(w, r) {
		return
	}
	wsCtx, releaseAuthorization, authorized := s.webSocketAuthorizationContext(r.Context(), r)
	if !authorized {
		writeError(w, http.StatusUnauthorized, "websocket authorization expired")
		return
	}
	defer releaseAuthorization()

	protocol, protocol2, err := websocketProtocol(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "agent store is not initialized")
		return
	}
	if _, err := s.store.GetAgent(r.Context(), agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, statusFromError(err), err.Error())
		return
	}
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "agent event hub is not initialized")
		return
	}

	after, hasAfter, err := websocketAfter(r, protocol2)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// validateWebSocketRequest already applies the trusted forwarded-origin
	// policy. Disable the library's raw Host comparison so TLS-terminating
	// loopback proxies with a backend Host can use the validated external origin.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	// Accept hijacks the connection, so wsCtx (derived from r.Context()) no
	// longer cancels when the client goes away. This handler only writes;
	// CloseRead pumps the read side so close frames and pings keep being
	// handled, and cancels the returned context as soon as the connection
	// closes. That is what releases the hub subscription and the authorized
	// connection registration instead of waiting for the next failed write.
	ctx := conn.CloseRead(wsCtx)
	subscription := s.hub.SubscribeProtocol(ctx, agentpkg.SubscribeOptions{
		AgentID:       agentID,
		StreamSession: r.URL.Query().Get("streamSession"),
		After:         after,
		HasAfter:      hasAfter,
	})
	if subscription.Reason != "" {
		writeWSControl(ctx, conn, wsControlFrame{
			Type:           "resync_required",
			Protocol:       protocol,
			StreamSession:  subscription.StreamSession,
			OldestSequence: &subscription.OldestSequence,
			LatestSequence: &subscription.LatestSequence,
			Resume:         "snapshot_required",
			Reason:         string(subscription.Reason),
		})
		return
	}

	if protocol2 {
		resume := "live"
		if len(subscription.Replay) > 0 {
			resume = "replayed"
		}
		if !writeWSControl(ctx, conn, wsControlFrame{
			Type:           "connected",
			Protocol:       agentpkg.ProtocolVersion,
			StreamSession:  subscription.StreamSession,
			OldestSequence: &subscription.OldestSequence,
			LatestSequence: &subscription.LatestSequence,
			Resume:         resume,
		}) {
			return
		}
	} else if !writeWSControl(ctx, conn, wsControlFrame{Type: "connected"}) {
		return
	}
	for _, event := range subscription.Replay {
		if !writeWSEvent(ctx, conn, event) {
			return
		}
	}

	for {
		select {
		case reason, ok := <-subscription.Resync:
			if !ok {
				return
			}
			writeWSControl(ctx, conn, wsControlFrame{
				Type:           "resync_required",
				Protocol:       agentpkg.ProtocolVersion,
				StreamSession:  subscription.StreamSession,
				OldestSequence: &subscription.OldestSequence,
				LatestSequence: &subscription.LatestSequence,
				Resume:         "snapshot_required",
				Reason:         string(reason),
			})
			return
		case <-ctx.Done():
			return
		case event, ok := <-subscription.Events:
			if !ok {
				select {
				case reason, ok := <-subscription.Resync:
					if ok {
						writeWSControl(ctx, conn, wsControlFrame{
							Type:           "resync_required",
							Protocol:       agentpkg.ProtocolVersion,
							StreamSession:  subscription.StreamSession,
							LatestSequence: &subscription.LatestSequence,
							Reason:         string(reason),
						})
					}
				default:
				}
				return
			}
			if !writeWSEvent(ctx, conn, event) {
				return
			}
		}
	}
}

func websocketProtocol(r *http.Request) (int, bool, error) {
	value := r.URL.Query().Get("protocol")
	if value == "" {
		return 0, false, nil
	}
	protocol, err := strconv.Atoi(value)
	if err != nil || protocol != agentpkg.ProtocolVersion {
		return 0, false, errors.New("protocol must be 2")
	}
	return protocol, true, nil
}

func websocketAfter(r *http.Request, protocol2 bool) (uint64, bool, error) {
	if !protocol2 {
		return 0, false, nil
	}
	value, present := r.URL.Query()["after"]
	if !present || len(value) == 0 || value[0] == "" {
		return 0, false, nil
	}
	after, err := strconv.ParseUint(value[0], 10, 64)
	if err != nil {
		return 0, false, errors.New("after must be an unsigned integer")
	}
	return after, true, nil
}

func writeWSEvent(ctx context.Context, conn *websocket.Conn, event agentpkg.Event) bool {
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, event.JSON()) == nil
}

func writeWSControl(ctx context.Context, conn *websocket.Conn, frame wsControlFrame) bool {
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, marshalWSControl(frame)) == nil
}

func marshalWSControl(frame wsControlFrame) []byte {
	data, _ := json.Marshal(frame)
	return data
}
