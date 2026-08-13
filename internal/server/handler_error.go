package server

import (
	"errors"
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

func writeAPIError(w http.ResponseWriter, err error) {
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
	writeError(w, statusFromError(err), err.Error())
}
