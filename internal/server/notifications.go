package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	agentpkg "autoto/internal/agent"
	"autoto/internal/db"
	"autoto/internal/network"
)

const webhookNotifyTimeout = 5 * time.Second

type WebhookNotifier struct {
	store  *db.Store
	client *http.Client
}

type notificationSettingsPayload struct {
	Enabled          bool   `json:"enabled"`
	WebhookURL       string `json:"webhookUrl"`
	NotifyOnApproval bool   `json:"notifyOnApproval"`
	NotifyOnDone     bool   `json:"notifyOnDone"`
	NotifyOnError    bool   `json:"notifyOnError"`
}

type webhookPayload struct {
	Kind      string                 `json:"kind"`
	Event     string                 `json:"event"`
	RunID     string                 `json:"runId,omitempty"`
	AgentID   string                 `json:"agentId,omitempty"`
	Status    string                 `json:"status,omitempty"`
	Error     string                 `json:"errorMessage,omitempty"`
	Summary   *webhookRunSummary     `json:"summary,omitempty"`
	Tool      *webhookToolSummary    `json:"tool,omitempty"`
	CreatedAt string                 `json:"createdAt"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
}

type webhookRunSummary struct {
	MessageCount     int64   `json:"messageCount"`
	ToolCallCount    int64   `json:"toolCallCount"`
	PendingApprovals int64   `json:"pendingApprovals"`
	DeniedToolCalls  int64   `json:"deniedToolCalls"`
	ErrorToolCalls   int64   `json:"errorToolCalls"`
	APIRequestCount  int64   `json:"apiRequestCount"`
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CostUSD          float64 `json:"costUsd"`
}

type webhookToolSummary struct {
	ToolUseID string `json:"toolUseId,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
}

func NewWebhookNotifier(store *db.Store) *WebhookNotifier {
	return newWebhookNotifier(store)
}

// newWebhookNotifier builds the notifier on the hardened public-egress path:
// every resolved address is validated against the public policy, the dialer
// receives the validated literal IP (closing the DNS-rebinding window), and
// each redirect hop is revalidated. The options seam exists so tests can
// inject a resolver and dialer without weakening the production constructor.
func newWebhookNotifier(store *db.Store, opts ...network.Option) *WebhookNotifier {
	return &WebhookNotifier{store: store, client: &http.Client{
		Transport:     network.NewPublicDirectTransport(opts...),
		Timeout:       webhookNotifyTimeout,
		CheckRedirect: network.RedirectPolicy(network.PolicyPublicDirect, opts...),
	}}
}

func (n *WebhookNotifier) Notify(ctx context.Context, event agentpkg.NotificationEvent) {
	if n == nil || n.store == nil {
		return
	}
	settings, err := n.store.GetNotificationSettings(ctx)
	if err != nil {
		slog.Warn("load notification settings failed", "event", event.Event, "runId", event.RunID, "error", err)
		return
	}
	if !shouldSendNotification(settings, event.Event) {
		return
	}
	if err := n.send(ctx, settings.WebhookURL, n.payload(ctx, event)); err != nil {
		slog.Warn("send webhook notification failed", "event", event.Event, "runId", event.RunID, "error", err)
	}
}

func (n *WebhookNotifier) SendTest(ctx context.Context) error {
	if n == nil || n.store == nil {
		return errors.New("webhook notifier is not initialized")
	}
	settings, err := n.store.GetNotificationSettings(ctx)
	if err != nil {
		return err
	}
	payload := webhookPayload{Kind: "notification.test", Event: "test", Status: "test", CreatedAt: db.Now(), Meta: map[string]interface{}{"source": "Autoto"}}
	return n.send(ctx, settings.WebhookURL, payload)
}

func (n *WebhookNotifier) payload(ctx context.Context, event agentpkg.NotificationEvent) webhookPayload {
	payload := webhookPayload{
		Kind:      "run." + notificationEventKind(event.Event),
		Event:     event.Event,
		RunID:     event.RunID,
		AgentID:   event.AgentID,
		Status:    event.Status,
		Error:     event.Error,
		CreatedAt: db.Now(),
	}
	if event.ToolUseID != "" || event.ToolName != "" {
		payload.Tool = &webhookToolSummary{ToolUseID: event.ToolUseID, ToolName: event.ToolName}
	}
	if event.RunID != "" && event.AgentID != "" {
		if summary, err := n.store.RunSummary(ctx, event.AgentID, event.RunID); err == nil {
			payload.Summary = &webhookRunSummary{
				MessageCount: summary.MessageCount, ToolCallCount: summary.ToolCallCount,
				PendingApprovals: summary.PendingApprovals, DeniedToolCalls: summary.DeniedToolCalls, ErrorToolCalls: summary.ErrorToolCalls,
				APIRequestCount: summary.APIRequestCount, InputTokens: summary.InputTokens, OutputTokens: summary.OutputTokens, CostUSD: summary.CostUSD,
			}
		}
	}
	return payload
}

func (n *WebhookNotifier) send(ctx context.Context, webhookURL string, payload webhookPayload) error {
	webhookURL = strings.TrimSpace(webhookURL)
	if err := validateWebhookURL(webhookURL, true); err != nil {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, webhookNotifyTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Autoto-Webhook/1.0")
	response, err := n.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", response.StatusCode)
	}
	return nil
}

func shouldSendNotification(settings db.NotificationSettings, event string) bool {
	if !settings.Enabled || strings.TrimSpace(settings.WebhookURL) == "" {
		return false
	}
	switch event {
	case "approval_required":
		return settings.NotifyOnApproval
	case "completed", "interrupted", "superseded":
		return settings.NotifyOnDone
	case "error":
		return settings.NotifyOnError
	default:
		return false
	}
}

func notificationEventKind(event string) string {
	switch event {
	case "approval_required":
		return "approval_required"
	case "completed", "error", "interrupted", "superseded":
		return event
	default:
		return "event"
	}
}

func (s *Server) getNotificationSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetNotificationSettings(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) updateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var req notificationSettingsPayload
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := validateWebhookURL(req.WebhookURL, false); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	updated, err := s.store.UpdateNotificationSettings(r.Context(), db.NotificationSettings{
		Enabled: req.Enabled, WebhookURL: strings.TrimSpace(req.WebhookURL), NotifyOnApproval: req.NotifyOnApproval, NotifyOnDone: req.NotifyOnDone, NotifyOnError: req.NotifyOnError,
	})
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	if s.automation == nil {
		if s.notifier == nil {
			writeError(w, http.StatusServiceUnavailable, "automation manager is unavailable")
			return
		}
		if err := s.notifier.SendTest(r.Context()); err != nil {
			s.writeRequestError(w, r, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sentAt": db.Now()})
		return
	}
	delivery, err := s.automation.EnqueueTest(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sentAt": db.Now(), "deliveryId": delivery.ID})
}

func validateWebhookURL(raw string, required bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return errors.New("webhookUrl is required")
		}
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("webhookUrl must be an absolute http(s) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("webhookUrl must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("webhookUrl must not contain credentials, query parameters, or fragments")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "metadata.google.internal" || strings.HasSuffix(host, ".metadata.google.internal") {
		return errors.New("webhookUrl targets a forbidden metadata host")
	}
	if isLoopbackHost(host) {
		return errors.New("webhookUrl targets a forbidden network address")
	}
	if net.ParseIP(host) != nil {
		// Literal addresses validate without DNS, so the settings API can refuse
		// loopback/private/metadata targets immediately. Hostname targets are
		// resolved and enforced by the hardened transport when a webhook is sent.
		if network.ValidateURL(context.Background(), network.PolicyPublicDirect, parsed) != nil {
			return errors.New("webhookUrl targets a forbidden network address")
		}
	}
	return nil
}
