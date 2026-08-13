package server

import (
	"net/http"
	"strings"

	updatepkg "autoto/internal/update"
)

// ShellLifecycleHost is optional desktop-only lifecycle control (autostart).
// Browser and remote sessions never receive a non-nil host.
type ShellLifecycleHost interface {
	AutostartStatus() (enabled bool, strategy, path string, err error)
	AutostartEnable() error
	AutostartDisable() error
	// HandleDeepLink is informational for the shell (focus + navigate). HTTP
	// callers only forward a validated autoto:// URL; they cannot run host commands.
	NotifyDeepLink(raw string) error
}

// ShellUpdateHost stages a local binary after user confirmation. It must not
// download from the network or apply the replace inside the request path.
type ShellUpdateHost interface {
	// StageLocalUpdate copies sourcePath into the home updates/staged dir.
	StageLocalUpdate(sourcePath, version, sha256 string) (updatepkg.PendingReplace, error)
	PendingUpdate() (updatepkg.PendingReplace, bool, error)
	ClearPendingUpdate() error
}

func (s *Server) SetShellLifecycleHost(host ShellLifecycleHost) {
	if s == nil {
		return
	}
	s.shellDialogMu.Lock()
	s.shellLifecycleHost = host
	s.shellDialogMu.Unlock()
}

func (s *Server) SetShellUpdateHost(host ShellUpdateHost) {
	if s == nil {
		return
	}
	s.shellDialogMu.Lock()
	s.shellUpdateHost = host
	s.shellDialogMu.Unlock()
}

func (s *Server) shellLifecycle() ShellLifecycleHost {
	if s == nil {
		return nil
	}
	s.shellDialogMu.RLock()
	defer s.shellDialogMu.RUnlock()
	return s.shellLifecycleHost
}

func (s *Server) shellUpdate() ShellUpdateHost {
	if s == nil {
		return nil
	}
	s.shellDialogMu.RLock()
	defer s.shellDialogMu.RUnlock()
	return s.shellUpdateHost
}

func (s *Server) mountDesktopShellExtraRoutes(r interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
	Delete(pattern string, h http.HandlerFunc)
}) {
	r.Get("/api/desktop/autostart", s.desktopAutostartGet)
	r.Post("/api/desktop/autostart", s.desktopAutostartPost)
	r.Delete("/api/desktop/autostart", s.desktopAutostartDelete)
	r.Post("/api/desktop/deep-link", s.desktopDeepLinkPost)
	r.Get("/api/desktop/update/pending", s.desktopUpdatePendingGet)
	r.Post("/api/desktop/update/stage", s.desktopUpdateStagePost)
	r.Delete("/api/desktop/update/pending", s.desktopUpdatePendingDelete)
}

func (s *Server) requireShellLoopback(w http.ResponseWriter, r *http.Request) bool {
	if s.remoteAccessGateRequired(r) {
		writeError(w, http.StatusForbidden, "desktop shell APIs require local access")
		return false
	}
	if !trustedLoopbackPeer(r) {
		writeError(w, http.StatusForbidden, "desktop shell APIs require loopback peer")
		return false
	}
	// Always require the process-local API token. Loopback alone is not a
	// desktop-shell identity: SSH/TCP port forwards present as 127.0.0.1 and
	// non-browser clients previously skipped the token check.
	if !s.validHeaderToken(r) {
		writeError(w, http.StatusUnauthorized, "missing or invalid local API token")
		return false
	}
	return true
}

func (s *Server) desktopAutostartGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireShellLoopback(w, r) {
		return
	}
	host := s.shellLifecycle()
	if host == nil {
		writeError(w, http.StatusNotFound, "desktop autostart unavailable")
		return
	}
	enabled, strategy, path, err := host.AutostartStatus()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"enabled":  enabled,
		"strategy": strategy,
		"path":     path,
	})
}

func (s *Server) desktopAutostartPost(w http.ResponseWriter, r *http.Request) {
	if !s.requireShellLoopback(w, r) {
		return
	}
	host := s.shellLifecycle()
	if host == nil {
		writeError(w, http.StatusNotFound, "desktop autostart unavailable")
		return
	}
	if err := host.AutostartEnable(); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	enabled, strategy, path, _ := host.AutostartStatus()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled, "strategy": strategy, "path": path})
}

func (s *Server) desktopAutostartDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireShellLoopback(w, r) {
		return
	}
	host := s.shellLifecycle()
	if host == nil {
		writeError(w, http.StatusNotFound, "desktop autostart unavailable")
		return
	}
	if err := host.AutostartDisable(); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": false})
}

type deepLinkRequest struct {
	URL string `json:"url"`
}

func (s *Server) desktopDeepLinkPost(w http.ResponseWriter, r *http.Request) {
	if !s.requireShellLoopback(w, r) {
		return
	}
	host := s.shellLifecycle()
	if host == nil {
		writeError(w, http.StatusNotFound, "desktop deep link host unavailable")
		return
	}
	var req deepLinkRequest
	if !decodeShellRequest(w, r, &req, "invalid deep link request") {
		return
	}
	raw := strings.TrimSpace(req.URL)
	if raw == "" || !strings.HasPrefix(strings.ToLower(raw), "autoto:") {
		writeError(w, http.StatusBadRequest, "url must be an autoto:// deep link")
		return
	}
	if err := host.NotifyDeepLink(raw); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type stageUpdateRequest struct {
	SourcePath string `json:"sourcePath"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
}

func (s *Server) desktopUpdatePendingGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireShellLoopback(w, r) {
		return
	}
	host := s.shellUpdate()
	if host == nil {
		writeError(w, http.StatusNotFound, "desktop update host unavailable")
		return
	}
	pending, ok, err := host.PendingUpdate()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"pending": true,
		"version": pending.Version,
		"path":    pending.StagedPath,
		"sha256":  pending.SHA256,
	})
}

func (s *Server) desktopUpdateStagePost(w http.ResponseWriter, r *http.Request) {
	if !s.requireShellLoopback(w, r) {
		return
	}
	host := s.shellUpdate()
	if host == nil {
		writeError(w, http.StatusNotFound, "desktop update host unavailable")
		return
	}
	var req stageUpdateRequest
	if !decodeShellRequest(w, r, &req, "invalid stage request") {
		return
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	version := strings.TrimSpace(req.Version)
	sha256 := strings.ToLower(strings.TrimSpace(req.SHA256))
	if sourcePath == "" || version == "" {
		writeError(w, http.StatusBadRequest, "sourcePath and version are required")
		return
	}
	if sha256 == "" {
		writeError(w, http.StatusBadRequest, "sha256 is required")
		return
	}
	// Require an unforgeable native confirmation when a dialog host is present.
	// Without a host (tests/stubs), the loopback+token gate remains the boundary.
	if dialog := s.shellDialog(); dialog != nil {
		accepted, err := dialog.Confirm(r.Context(),
			"Stage local desktop update "+version+" from:\n"+sourcePath+"\n\nThis only copies the file; it does not replace the running process.",
			"Stage desktop update")
		if err != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return
		}
		if !accepted {
			writeError(w, http.StatusConflict, "update staging canceled")
			return
		}
	}
	pending, err := host.StageLocalUpdate(sourcePath, version, sha256)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"pending": true,
		"version": pending.Version,
		"path":    pending.StagedPath,
		"sha256":  pending.SHA256,
		// Staged only. A future host-side apply helper must verify SHA and replace.
		"apply": "not_implemented",
		"note":  "Staged only. Remote sessions cannot stage or apply. No automatic restart/replace is performed yet.",
	})
}

func (s *Server) desktopUpdatePendingDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireShellLoopback(w, r) {
		return
	}
	host := s.shellUpdate()
	if host == nil {
		writeError(w, http.StatusNotFound, "desktop update host unavailable")
		return
	}
	if err := host.ClearPendingUpdate(); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": false})
}
