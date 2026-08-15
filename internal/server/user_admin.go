package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

const userAccessKeyPrefix = "atk_"

type userAccountResponse struct {
	db.User
	PasswordSet bool               `json:"passwordSet"`
	ProjectIDs  []string           `json:"projectIds"`
	KeyCount    int                `json:"keyCount"`
	Keys        []db.UserAccessKey `json:"keys,omitempty"`
}

type createGuestRequest struct {
	Handle     string   `json:"handle"`
	Password   string   `json:"password"`
	ProjectIDs []string `json:"projectIds"`
	KeyLabel   string   `json:"keyLabel"`
	IssueKey   bool     `json:"issueKey"`
}

type createGuestResponse struct {
	userAccountResponse
	AccessKey string `json:"accessKey,omitempty"`
}

type issueAccessKeyRequest struct {
	Label string `json:"label"`
}

type issueAccessKeyResponse struct {
	Key       db.UserAccessKey `json:"key"`
	AccessKey string           `json:"accessKey"`
}

type replaceMembershipsRequest struct {
	ProjectIDs []string `json:"projectIds"`
}

func newUserAccessKeyToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return userAccessKeyPrefix + hex.EncodeToString(buf), nil
}

func (s *Server) requireAdminUser(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return db.User{}, false
	}
	if !userIsAdmin(user) {
		writeError(w, http.StatusForbidden, "administrator access required")
		return db.User{}, false
	}
	return user, true
}

func (s *Server) listUserAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminUser(w, r); !ok {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	accounts := make([]userAccountResponse, 0, len(users))
	for _, user := range users {
		account, err := s.userAccountResponse(r, user, true)
		if err != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return
		}
		accounts = append(accounts, account)
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) createGuestAccount(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdminUser(w, r)
	if !ok {
		return
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	if !hasUsers {
		writeError(w, http.StatusConflict, "create an administrator account before adding guests")
		return
	}
	var request createGuestRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	password := strings.TrimSpace(request.Password)
	issueKey := request.IssueKey || password == ""
	if password == "" && !issueKey {
		writeError(w, http.StatusBadRequest, "guest accounts need a password or an access key")
		return
	}
	if password != "" && !validUserPasswordLength(password) {
		writeError(w, http.StatusBadRequest, "password must be between 8 and 1024 bytes")
		return
	}
	passwordHash := ""
	if password != "" {
		hash, err := hashUserPassword(password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "hash password: "+err.Error())
			return
		}
		passwordHash = hash
	}
	if err := s.requireAdminProjectAccess(w, r, admin, request.ProjectIDs); err != nil {
		return
	}
	user, err := s.store.CreateGuestUser(r.Context(), request.Handle, passwordHash)
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	if err := s.store.ReplaceUserProjectMemberships(r.Context(), user.ID, request.ProjectIDs); err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	response := createGuestResponse{}
	account, err := s.userAccountResponse(r, user, true)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	response.userAccountResponse = account
	if issueKey {
		token, key, err := s.issueUserAccessKey(r, user.ID, request.KeyLabel)
		if err != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return
		}
		response.AccessKey = token
		response.Keys = []db.UserAccessKey{key}
		response.KeyCount = 1
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) issueGuestAccessKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminUser(w, r); !ok {
		return
	}
	user, ok := s.loadManagedGuest(w, r)
	if !ok {
		return
	}
	var request issueAccessKeyRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	token, key, err := s.issueUserAccessKey(r, user.ID, request.Label)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, issueAccessKeyResponse{Key: key, AccessKey: token})
}

func (s *Server) revokeGuestAccessKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminUser(w, r); !ok {
		return
	}
	user, ok := s.loadManagedGuest(w, r)
	if !ok {
		return
	}
	if err := s.store.RevokeUserAccessKey(r.Context(), user.ID, chi.URLParam(r, "keyId")); err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) replaceGuestMemberships(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdminUser(w, r)
	if !ok {
		return
	}
	user, ok := s.loadManagedGuest(w, r)
	if !ok {
		return
	}
	var request replaceMembershipsRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.requireAdminProjectAccess(w, r, admin, request.ProjectIDs); err != nil {
		return
	}
	if err := s.store.ReplaceUserProjectMemberships(r.Context(), user.ID, request.ProjectIDs); err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	account, err := s.userAccountResponse(r, user, true)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) deleteUserAccount(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireAdminUser(w, r)
	if !ok {
		return
	}
	user, ok := s.loadManagedUser(w, r)
	if !ok {
		return
	}
	if user.ID == admin.ID {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	if userIsAdmin(user) {
		count, err := s.store.CountUsersByRole(r.Context(), "admin")
		if err != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return
		}
		if count <= 1 {
			writeError(w, http.StatusConflict, "cannot delete the last administrator")
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), user.ID); err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) loadManagedUser(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	user, err := s.store.GetUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return db.User{}, false
	}
	return user, true
}

func (s *Server) loadManagedGuest(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	user, ok := s.loadManagedUser(w, r)
	if !ok {
		return db.User{}, false
	}
	if !userIsGuest(user) {
		writeError(w, http.StatusBadRequest, "access keys and project memberships are only managed for guest accounts")
		return db.User{}, false
	}
	return user, true
}

func (s *Server) requireAdminProjectAccess(w http.ResponseWriter, r *http.Request, admin db.User, projectIDs []string) error {
	for _, projectID := range projectIDs {
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			continue
		}
		allowed, err := s.store.CanAccessProject(r.Context(), admin.ID, projectID)
		if err != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return err
		}
		if !allowed {
			writeError(w, http.StatusNotFound, "resource not found")
			return errProjectAccessDenied
		}
	}
	return nil
}

func (s *Server) issueUserAccessKey(r *http.Request, userID, label string) (string, db.UserAccessKey, error) {
	token, err := newUserAccessKeyToken()
	if err != nil {
		return "", db.UserAccessKey{}, err
	}
	key, err := s.store.CreateUserAccessKey(r.Context(), db.UserAccessKey{
		UserID:    userID,
		TokenHash: db.HashSessionToken(token),
		Label:     strings.TrimSpace(label),
	})
	if err != nil {
		return "", db.UserAccessKey{}, err
	}
	return token, key, nil
}

func (s *Server) userAccountResponse(r *http.Request, user db.User, includeKeys bool) (userAccountResponse, error) {
	passwordSet, err := s.store.UserPasswordSet(r.Context(), user.ID)
	if err != nil {
		return userAccountResponse{}, err
	}
	projectIDs, err := s.store.ListProjectIDsForUser(r.Context(), user.ID)
	if err != nil {
		return userAccountResponse{}, err
	}
	if projectIDs == nil {
		projectIDs = []string{}
	}
	account := userAccountResponse{User: user, PasswordSet: passwordSet, ProjectIDs: projectIDs}
	if includeKeys {
		keys, err := s.store.ListUserAccessKeys(r.Context(), user.ID)
		if err != nil {
			return userAccountResponse{}, err
		}
		if keys == nil {
			keys = []db.UserAccessKey{}
		}
		account.Keys = keys
		account.KeyCount = len(keys)
		return account, nil
	}
	count, err := s.store.CountUserAccessKeys(r.Context(), user.ID)
	if err != nil {
		return userAccountResponse{}, err
	}
	account.KeyCount = count
	return account, nil
}

var errProjectAccessDenied = errors.New("project access denied")
