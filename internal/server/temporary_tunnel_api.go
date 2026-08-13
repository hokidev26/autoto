package server

import (
	"net/http"
)

const temporaryTunnelInstallPath = "/api/security/remote-access/tunnel/install"

func (s *Server) temporaryTunnelSnapshot() TemporaryTunnelSnapshot {
	return s.temporaryTunnels().snapshot()
}

func (s *Server) getTemporaryTunnel(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.temporaryTunnelSnapshot())
}

func (s *Server) installTemporaryTunnel(w http.ResponseWriter, r *http.Request) {
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	snapshot, err := s.temporaryTunnels().install(r.Context())
	if err != nil {
		writeAPIError(w, err)
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
	snapshot, err := s.temporaryTunnels().start(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) stopTemporaryTunnel(w http.ResponseWriter, r *http.Request) {
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}
	snapshot, err := s.temporaryTunnels().stop(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
