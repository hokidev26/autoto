package server

import (
	"errors"
	"log/slog"
	"net/http"
)

type apiError struct {
	status int
	msg    string
}

func (e apiError) Error() string { return e.msg }

func apiErr(status int, msg string) error {
	return apiError{status: status, msg: msg}
}

// writeRequestError answers with the detail the caller is entitled to. A
// loopback request is the operator's own browser, where the real cause is what
// makes a failure diagnosable, so it passes through unchanged. A remote
// session may hold only read access over a tunnel, so it gets a stable generic
// message and the cause goes to the log instead of over the wire.
func (s *Server) writeRequestError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if err == nil {
		return
	}
	if s.remoteAccessAuthentication(r).Remote {
		slog.Warn("request error", "path", r.URL.Path, "status", status, "error", err)
		writeError(w, status, genericRequestErrorMessage(status))
		return
	}
	writeError(w, status, err.Error())
}

func genericRequestErrorMessage(status int) string {
	switch status {
	case http.StatusNotFound:
		return "not found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusBadRequest:
		return "invalid request"
	default:
		return "internal error"
	}
}

func (s *Server) writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	var api apiError
	if errors.As(err, &api) {
		writeError(w, api.status, api.msg)
		return
	}
	var gitErr gitCommandError
	if errors.As(err, &gitErr) {
		writeError(w, gitErr.Status, gitErr.Msg)
		return
	}
	s.writeRequestError(w, r, statusFromError(err), err)
}
