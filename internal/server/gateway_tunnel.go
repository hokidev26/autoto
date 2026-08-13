package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

const gatewayTunnelInstallPath = "/api/gateway/tunnel/install"

// gatewayTunnelResponse adds the ready-to-paste base URL to the raw tunnel
// snapshot. Callers of the shared API need the origin with /v1 on the end, and
// composing it here keeps one definition of that shape instead of repeating it
// in every client.
type gatewayTunnelResponse struct {
	TemporaryTunnelSnapshot
	PublicAPIBaseURL string `json:"publicApiBaseUrl,omitempty"`
	// GatewayRunning distinguishes "nothing is listening yet" from a tunnel
	// failure, so the UI can point at the actual thing to fix.
	GatewayRunning bool `json:"gatewayRunning"`
	// ActiveKeys reports how many keys could authenticate right now. Exposing
	// the endpoint with none is harmless but useless, and worth telling the user
	// before they hand out a URL.
	ActiveKeys int `json:"activeKeys"`
}

func (s *Server) gatewayTunnelSnapshot(r *http.Request) gatewayTunnelResponse {
	if s.apiTunnel == nil {
		return gatewayTunnelResponse{
			TemporaryTunnelSnapshot: TemporaryTunnelSnapshot{
				Available: false,
				Status:    temporaryTunnelUnavailable,
				Error:     "shared API tunnel manager is unavailable",
			},
		}
	}
	// Reading status is also when a gateway that moved out from under a running
	// tunnel gets noticed, in case the config change arrived by some path other
	// than the settings endpoint.
	if _, changed := s.apiTunnel.RevalidateAddress(r.Context()); changed {
		slog.Warn("shared API tunnel closed because the gateway address changed")
	}
	return s.decorateGatewayTunnel(r, s.apiTunnel.Snapshot())
}

func (s *Server) decorateGatewayTunnel(r *http.Request, snapshot TemporaryTunnelSnapshot) gatewayTunnelResponse {
	response := gatewayTunnelResponse{TemporaryTunnelSnapshot: snapshot}
	if controller := s.gatewayRuntimeController(); controller != nil {
		response.GatewayRunning = controller.Status().Running
	}
	if url := strings.TrimRight(strings.TrimSpace(snapshot.PublicURL), "/"); url != "" {
		response.PublicAPIBaseURL = url + "/v1"
	}
	response.ActiveKeys = s.activeGatewayKeyCount(r)
	return response
}

func (s *Server) activeGatewayKeyCount(r *http.Request) int {
	if s.store == nil {
		return 0
	}
	keys, err := s.store.ListGatewayKeys(r.Context())
	if err != nil {
		return 0
	}
	now := s.now()
	count := 0
	for _, key := range keys {
		if gatewayKeyIsActive(key, now) {
			count++
		}
	}
	return count
}

func (s *Server) getGatewayTunnel(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	writeJSON(w, http.StatusOK, s.gatewayTunnelSnapshot(r))
}

func (s *Server) installGatewayTunnel(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	if s.apiTunnel == nil {
		writeError(w, http.StatusServiceUnavailable, "shared API tunnel manager is unavailable")
		return
	}
	snapshot, err := s.apiTunnel.InstallCloudflared(r.Context())
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, errCloudflaredInstallUnsupported), errors.Is(err, errCloudflaredInstallInProgress), snapshot.Status == temporaryTunnelRunning, snapshot.Status == temporaryTunnelStarting, snapshot.Status == temporaryTunnelStopping:
			status = http.StatusConflict
		}
		s.writeRequestError(w, r, status, err)
		return
	}
	writeJSON(w, http.StatusOK, s.decorateGatewayTunnel(r, snapshot))
}

// startGatewayTunnel publishes the shared API. Unlike the tunnel in front of
// Autoto's own listener, this one is not gated on an access password: the
// gateway authenticates with its own keys and serves only /v1, so the password
// that protects the management UI is not what stands between a caller and this
// endpoint.
func (s *Server) startGatewayTunnel(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	if s.apiTunnel == nil {
		writeError(w, http.StatusServiceUnavailable, "shared API tunnel manager is unavailable")
		return
	}
	snapshot, err := s.apiTunnel.StartTunnel(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, s.decorateGatewayTunnel(r, snapshot))
}

func (s *Server) stopGatewayTunnel(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	if s.apiTunnel == nil {
		writeJSON(w, http.StatusOK, s.gatewayTunnelSnapshot(r))
		return
	}
	snapshot, err := s.apiTunnel.StopTunnel(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, s.decorateGatewayTunnel(r, snapshot))
}
