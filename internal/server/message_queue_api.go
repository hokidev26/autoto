package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"autoto/internal/agent"
	"autoto/internal/db"
)

type queuedMessageRequest struct {
	Text    string `json:"text"`
	Mode    string `json:"mode"`
	Context string `json:"context"`
}

func writeQueuedMessageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrQueuedMessageNotFound):
		writeError(w, http.StatusNotFound, "queued message was not found")
	case errors.Is(err, db.ErrQueuedMessageLimit):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, db.ErrQueuedMessageInvalid):
		writeError(w, http.StatusBadRequest, "queued message is invalid")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) listQueuedMessages(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListQueuedMessages(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeQueuedMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": items})
}

func (s *Server) enqueueMessage(w http.ResponseWriter, r *http.Request) {
	var req queuedMessageRequest
	if err := decodeLimitedJSON(w, r, &req, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateAPIText("text", req.Text, 512<<10, true, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := chi.URLParam(r, "id")
	// Queueing must not become a way around the checks a direct send passes, so
	// the same boundary resolves here before anything is stored.
	if _, _, err := s.messageRunBoundary(r.Context(), agentID, req.Context); err != nil {
		writeError(w, statusFromMessageBoundaryError(err), err.Error())
		return
	}
	createdBy := ""
	if user, ok, err := s.currentUser(r); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if ok {
		createdBy = user.ID
	}
	item, err := s.store.EnqueueMessage(r.Context(), db.QueuedMessage{
		AgentID:    agentID,
		CreatedBy:  createdBy,
		Text:       req.Text,
		RunMode:    req.Mode,
		RunContext: req.Context,
	})
	if err != nil {
		writeQueuedMessageError(w, err)
		return
	}
	// An agent that is already idle should not sit on a fresh follow-up waiting
	// for a run-finished event that has already been and gone.
	s.ScheduleMessageQueueDrain(agentID)
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) updateQueuedMessage(w http.ResponseWriter, r *http.Request) {
	var req queuedMessageRequest
	if err := decodeLimitedJSON(w, r, &req, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateAPIText("text", req.Text, 512<<10, true, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	item, err := s.store.UpdateQueuedMessageText(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "queueId"), req.Text)
	if err != nil {
		writeQueuedMessageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteQueuedMessage(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteQueuedMessage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "queueId")); err != nil {
		writeQueuedMessageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetChainedNotifier installs the notifier that was already registered on the
// runner. The queue drain needs run-finished events, but the runner holds a
// single notifier slot, so this server forwards to the previous one instead of
// displacing it.
func (s *Server) SetChainedNotifier(next agent.Notifier) {
	s.notifierMu.Lock()
	defer s.notifierMu.Unlock()
	s.nextNotifier = next
}

func (s *Server) chainedNotifier() agent.Notifier {
	s.notifierMu.RLock()
	defer s.notifierMu.RUnlock()
	return s.nextNotifier
}

// messageQueueDrainer serialises drains per agent. Two events arriving together
// (a run completing while a device also posts a follow-up) must not both claim
// from the same queue and start two runs.
type messageQueueDrainer struct {
	mu      sync.Mutex
	running map[string]bool
}

func (d *messageQueueDrainer) begin(agentID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running == nil {
		d.running = map[string]bool{}
	}
	if d.running[agentID] {
		return false
	}
	d.running[agentID] = true
	return true
}

func (d *messageQueueDrainer) end(agentID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.running, agentID)
}

// Notify chains onto the runner's notifier so a finished run pulls the next
// queued message. The previous notifier still receives every event.
func (s *Server) Notify(ctx context.Context, event agent.NotificationEvent) {
	if next := s.chainedNotifier(); next != nil {
		next.Notify(ctx, event)
	}
	switch strings.ToLower(strings.TrimSpace(event.Event)) {
	case "completed", "error", "failed", "interrupted", "cancelled", "canceled", "superseded":
		if strings.TrimSpace(event.AgentID) != "" {
			s.ScheduleMessageQueueDrain(event.AgentID)
		}
	}
}

// ScheduleMessageQueueDrain sends the next queued message once the agent is
// free. It returns immediately: the caller is either an HTTP handler or a
// runner notification, and neither should block on a model call.
func (s *Server) ScheduleMessageQueueDrain(agentID string) {
	id := strings.TrimSpace(agentID)
	if id == "" || s.store == nil || s.runner == nil {
		return
	}
	go func() {
		if !s.queueDrainer.begin(id) {
			return
		}
		defer s.queueDrainer.end(id)
		s.drainMessageQueue(id)
	}()
}

func (s *Server) drainMessageQueue(agentID string) {
	ctx := context.Background()
	for {
		// A run still in flight means the user's follow-up is meant to wait.
		if busy, err := s.agentHasActiveRun(ctx, agentID); err != nil || busy {
			return
		}
		item, ok, err := s.store.ClaimNextQueuedMessage(ctx, agentID)
		if err != nil || !ok {
			return
		}
		if err := s.sendQueuedMessage(ctx, item); err != nil {
			// Put it back so the order the user typed in survives a failure.
			_ = s.store.RestoreQueuedMessage(ctx, item)
			return
		}
	}
}

func (s *Server) agentHasActiveRun(ctx context.Context, agentID string) (bool, error) {
	_, err := s.store.ActiveRunSummary(ctx, agentID)
	if err == nil {
		return true, nil
	}
	// An agent with nothing running reads as sql.ErrNoRows. Any other error is a
	// real failure and must not be mistaken for an idle agent, or a drain could
	// start a second run alongside a live one.
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func (s *Server) sendQueuedMessage(ctx context.Context, item db.QueuedMessage) error {
	contextCap, runSource, err := s.messageRunBoundary(ctx, item.AgentID, item.RunContext)
	if err != nil {
		return err
	}
	mode := db.RunExecutionModeExecute
	if runSource != db.RunSourceConversation {
		mode, err = s.reviewModeForMessage(ctx, item.AgentID, item.RunMode)
		if err != nil {
			return err
		}
	}
	// Draining happens without a request, so there is no remote session to cap
	// against; the stored context cap still applies.
	_, err = s.submitReviewRunWithSource(ctx, item.AgentID, item.Text, item.CreatedBy, mode, contextCap, runSource, nil)
	return err
}
