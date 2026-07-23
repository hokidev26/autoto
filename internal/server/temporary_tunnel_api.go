package server

import (
	"errors"
	"net/http"
)

const temporaryTunnelInstallPath = "/api/security/remote-access/tunnel/install"

func (s *Server) temporaryTunnelSnapshot() TemporaryTunnelSnapshot {
	if s.temporaryTunnel == nil {
		return TemporaryTunnelSnapshot{Available: false, Status: temporaryTunnelUnavailable, Error: "temporary tunnel manager is unavailable"}
	}
	return s.temporaryTunnel.Snapshot()
}

func (s *Server) getTemporaryTunnel(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.temporaryTunnelSnapshot())
}

func (s *Server) installTemporaryTunnel(w http.ResponseWriter, r *http.Request) {
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	if s.temporaryTunnel == nil {
		writeError(w, http.StatusServiceUnavailable, "temporary tunnel manager is unavailable")
		return
	}
	snapshot, err := s.temporaryTunnel.InstallCloudflared(r.Context())
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, errCloudflaredInstallUnsupported), errors.Is(err, errCloudflaredInstallInProgress), snapshot.Status == temporaryTunnelRunning, snapshot.Status == temporaryTunnelStarting, snapshot.Status == temporaryTunnelStopping:
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) startTemporaryTunnel(w http.ResponseWriter, r *http.Request) {
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	configured, _ := s.credentialConfigured()
	if !configured {
		writeError(w, http.StatusConflict, "configure an access password before starting a temporary tunnel")
		return
	}
	if s.temporaryTunnel == nil {
		writeError(w, http.StatusServiceUnavailable, "temporary tunnel manager is unavailable")
		return
	}
	snapshot, err := s.temporaryTunnel.StartTunnel(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) stopTemporaryTunnel(w http.ResponseWriter, r *http.Request) {
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	if s.temporaryTunnel == nil {
		writeJSON(w, http.StatusOK, s.temporaryTunnelSnapshot())
		return
	}
	snapshot, err := s.temporaryTunnel.StopTunnel(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
