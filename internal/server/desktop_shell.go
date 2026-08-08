package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// Every shell request below is small metadata: a dialog title and message, a
// deep-link URL, a staged update path. The ceiling is generous against that and
// exists so a buggy or hostile local client cannot grow the desktop process by
// streaming an endless body into a decoder.
//
// These routes are already gated on being a desktop process, a loopback peer and
// the process-local API token, so this is defence in depth rather than a reachable
// hole. It is here because bounding externally supplied bodies is the convention
// everywhere else in this package, and these were the last exceptions.
const maxShellRequestBytes = 64 << 10

// decodeShellRequest bounds the body before decoding it. Returns false when the
// body was unreadable or oversized, having already written the response.
func decodeShellRequest(w http.ResponseWriter, r *http.Request, target any, invalidMessage string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxShellRequestBytes)
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, invalidMessage)
		return false
	}
	return true
}

// ShellFileFilter describes a native open-file dialog filter.
type ShellFileFilter struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"` // e.g. "*.json;*.md"
}

// ShellDialogHost is a shell-only capability used by the optional desktop client
// to show native confirm/alert/file dialogs. It must not expose Agent or other
// product APIs beyond picker results.
type ShellDialogHost interface {
	Confirm(ctx context.Context, message, title string) (bool, error)
	Alert(ctx context.Context, message, title string) error
	// PickDirectory returns a filesystem path. canceled is true when the user
	// dismisses the dialog without choosing.
	PickDirectory(ctx context.Context, title, defaultPath string) (path string, canceled bool, err error)
	// PickFile returns a single filesystem path with optional filters.
	PickFile(ctx context.Context, title, defaultPath string, filters []ShellFileFilter) (path string, canceled bool, err error)
}

// SetShellDialogHost registers (or clears) the native dialog backend for the
// desktop shell. Browser-only processes leave this nil so the endpoints 404.
func (s *Server) SetShellDialogHost(host ShellDialogHost) {
	if s == nil {
		return
	}
	s.shellDialogMu.Lock()
	s.shellDialogHost = host
	s.shellDialogMu.Unlock()
}

func (s *Server) shellDialog() ShellDialogHost {
	if s == nil {
		return nil
	}
	s.shellDialogMu.RLock()
	defer s.shellDialogMu.RUnlock()
	return s.shellDialogHost
}

type shellDialogRequest struct {
	Message     string            `json:"message"`
	Title       string            `json:"title,omitempty"`
	DefaultPath string            `json:"defaultPath,omitempty"`
	Filters     []ShellFileFilter `json:"filters,omitempty"`
}

func (s *Server) mountDesktopShellRoutes(r interface {
	Post(pattern string, h http.HandlerFunc)
}) {
	r.Post("/api/desktop/dialog/confirm", s.desktopDialogConfirm)
	r.Post("/api/desktop/dialog/alert", s.desktopDialogAlert)
	r.Post("/api/desktop/dialog/open-directory", s.desktopDialogOpenDirectory)
	r.Post("/api/desktop/dialog/open-file", s.desktopDialogOpenFile)
}

// Note: autostart / deep-link / local-stage update routes live in desktop_shell_extra.go.

func (s *Server) desktopDialogConfirm(w http.ResponseWriter, r *http.Request) {
	host, ok := s.requireShellDialog(w, r)
	if !ok {
		return
	}
	var req shellDialogRequest
	if !decodeShellRequest(w, r, &req, "invalid dialog request") {
		return
	}
	accepted, err := host.Confirm(r.Context(), req.Message, req.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": accepted})
}

func (s *Server) desktopDialogAlert(w http.ResponseWriter, r *http.Request) {
	host, ok := s.requireShellDialog(w, r)
	if !ok {
		return
	}
	var req shellDialogRequest
	if !decodeShellRequest(w, r, &req, "invalid dialog request") {
		return
	}
	if err := host.Alert(r.Context(), req.Message, req.Title); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) desktopDialogOpenDirectory(w http.ResponseWriter, r *http.Request) {
	host, ok := s.requireShellDialog(w, r)
	if !ok {
		return
	}
	var req shellDialogRequest
	// A body is optional here: opening a picker with no title is valid, so a decode
	// failure is ignored rather than rejected. It still has to be bounded.
	r.Body = http.MaxBytesReader(w, r.Body, maxShellRequestBytes)
	defer r.Body.Close()
	_ = json.NewDecoder(r.Body).Decode(&req)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.Message)
	}
	path, canceled, err := host.PickDirectory(r.Context(), title, req.DefaultPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if canceled || strings.TrimSpace(path) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canceled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canceled": false, "path": path})
}

func (s *Server) desktopDialogOpenFile(w http.ResponseWriter, r *http.Request) {
	host, ok := s.requireShellDialog(w, r)
	if !ok {
		return
	}
	var req shellDialogRequest
	// Optional body, as above.
	r.Body = http.MaxBytesReader(w, r.Body, maxShellRequestBytes)
	defer r.Body.Close()
	_ = json.NewDecoder(r.Body).Decode(&req)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.Message)
	}
	path, canceled, err := host.PickFile(r.Context(), title, req.DefaultPath, req.Filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if canceled || strings.TrimSpace(path) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canceled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canceled": false, "path": path})
}

func (s *Server) requireShellDialog(w http.ResponseWriter, r *http.Request) (ShellDialogHost, bool) {
	host := s.shellDialog()
	if host == nil {
		writeError(w, http.StatusNotFound, "desktop shell dialogs unavailable")
		return nil, false
	}
	if !s.requireShellLoopback(w, r) {
		return nil, false
	}
	return host, true
}
