package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"autoto/internal/db"
)

const (
	accountPreferencesRequestBytes           int64 = 256 << 10
	accountPreferencesMaxAvatarBytes               = 96 << 10
	accountPreferencesMaxAvatarDataURLLength       = 140_000
	accountPreferencesMaxAvatarDimension           = 512
)

const accountPreferencesInstanceID = "default"

type accountPreferencesProfile struct {
	DisplayName    string `json:"displayName"`
	RoleLabel      string `json:"roleLabel"`
	AvatarInitials string `json:"avatarInitials"`
	AvatarDataURL  string `json:"avatarDataUrl"`
	GitName        string `json:"gitName"`
	GitEmail       string `json:"gitEmail"`
	WorkspaceLabel string `json:"workspaceLabel"`
}

type accountPreferencesModelVisibility struct {
	HiddenModels              map[string]bool `json:"hiddenModels"`
	ShowUnconfiguredProviders bool            `json:"showUnconfiguredProviders"`
}

type accountPreferencesResponse struct {
	ScopeKey                  string                            `json:"scopeKey"`
	Profile                   accountPreferencesProfile         `json:"profile"`
	PreferredModel            string                            `json:"preferredModel"`
	ModelVisibility           accountPreferencesModelVisibility `json:"modelVisibility"`
	SetupVersion              int                               `json:"setupVersion"`
	Revision                  int64                             `json:"revision"`
	LocalStorageImportVersion int                               `json:"localStorageImportVersion"`
	UpdatedAt                 string                            `json:"updatedAt"`
}

type patchAccountPreferencesRequest struct {
	ExpectedRevision int64                              `json:"expectedRevision"`
	Profile          *accountPreferencesProfile         `json:"profile,omitempty"`
	PreferredModel   *string                            `json:"preferredModel,omitempty"`
	ModelVisibility  *accountPreferencesModelVisibility `json:"modelVisibility,omitempty"`
	SetupVersion     *int                               `json:"setupVersion,omitempty"`
}

type importAccountPreferencesRequest struct {
	Version         int                               `json:"version"`
	Profile         accountPreferencesProfile         `json:"profile"`
	PreferredModel  string                            `json:"preferredModel"`
	ModelVisibility accountPreferencesModelVisibility `json:"modelVisibility"`
}

func defaultAccountPreferencesProfile() accountPreferencesProfile {
	return accountPreferencesProfile{
		RoleLabel:      "Local developer",
		AvatarInitials: "AT",
		WorkspaceLabel: "Autoto Local",
	}
}

func defaultAccountPreferencesModelVisibility() accountPreferencesModelVisibility {
	return accountPreferencesModelVisibility{HiddenModels: map[string]bool{}}
}

func (s *Server) getAccountPreferences(w http.ResponseWriter, r *http.Request) {
	scopeKind, scopeID, ok := s.accountPreferencesScope(w, r)
	if !ok {
		return
	}
	preferences, err := s.store.GetAccountPreferences(r.Context(), scopeKind, scopeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response, err := accountPreferencesResponseFromDB(preferences)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) patchAccountPreferences(w http.ResponseWriter, r *http.Request) {
	scopeKind, scopeID, ok := s.accountPreferencesScope(w, r)
	if !ok {
		return
	}
	var request patchAccountPreferencesRequest
	if err := decodeAccountPreferencesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "expectedRevision must be non-negative")
		return
	}
	if request.Profile == nil && request.PreferredModel == nil && request.ModelVisibility == nil && request.SetupVersion == nil {
		writeError(w, http.StatusBadRequest, "preferences patch must include at least one field")
		return
	}
	if request.SetupVersion != nil && (*request.SetupVersion < 0 || *request.SetupVersion > db.AccountPreferencesCurrentSetupVersion) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("setupVersion must be between 0 and %d", db.AccountPreferencesCurrentSetupVersion))
		return
	}

	patch := db.AccountPreferencesPatch{ExpectedRevision: request.ExpectedRevision}
	if request.Profile != nil {
		profile, err := normalizeAccountPreferencesProfile(*request.Profile)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		raw, err := json.Marshal(profile)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		value := json.RawMessage(raw)
		patch.ProfileJSON = &value
	}
	if request.PreferredModel != nil {
		preferredModel, err := normalizePreferredModel(*request.PreferredModel)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		patch.PreferredModel = &preferredModel
	}
	if request.ModelVisibility != nil {
		modelVisibility, err := normalizeAccountPreferencesModelVisibility(*request.ModelVisibility)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		raw, err := json.Marshal(modelVisibility)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		value := json.RawMessage(raw)
		patch.ModelVisibilityJSON = &value
	}
	if request.SetupVersion != nil {
		patch.SetupVersion = request.SetupVersion
	}

	preferences, err := s.store.PatchAccountPreferences(r.Context(), scopeKind, scopeID, patch)
	if err != nil {
		s.writeAccountPreferencesStoreError(w, r, scopeKind, scopeID, err)
		return
	}
	response, err := accountPreferencesResponseFromDB(preferences)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) importLocalAccountPreferences(w http.ResponseWriter, r *http.Request) {
	scopeKind, scopeID, ok := s.accountPreferencesScope(w, r)
	if !ok {
		return
	}
	var request importAccountPreferencesRequest
	if err := decodeAccountPreferencesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Version != 1 {
		writeError(w, http.StatusBadRequest, "version must be 1")
		return
	}
	profile, err := normalizeAccountPreferencesProfile(request.Profile)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	preferredModel, err := normalizePreferredModel(request.PreferredModel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	modelVisibility, err := normalizeAccountPreferencesModelVisibility(request.ModelVisibility)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	modelVisibilityJSON, err := json.Marshal(modelVisibility)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	preferences, _, err := s.store.ImportAccountPreferences(r.Context(), scopeKind, scopeID, db.AccountPreferencesImport{
		Version:             request.Version,
		ProfileJSON:         json.RawMessage(profileJSON),
		PreferredModel:      preferredModel,
		ModelVisibilityJSON: json.RawMessage(modelVisibilityJSON),
	})
	if err != nil {
		s.writeAccountPreferencesStoreError(w, r, scopeKind, scopeID, err)
		return
	}
	response, err := accountPreferencesResponseFromDB(preferences)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) accountPreferencesScope(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if s.store == nil {
		writeError(w, http.StatusInternalServerError, "preferences store is unavailable")
		return "", "", false
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return "", "", false
	}
	if !hasUsers {
		// Remote sessions that have authenticated (restricted or full mode) may
		// read and write instance-scoped preferences. Preferences are personal
		// display state, not a security-sensitive mutation, so the sensitive
		// local token guard that protects settings mutations is not appropriate
		// here. Unauthenticated remote requests are still rejected below.
		auth := s.remoteAccessAuthentication(r)
		if auth.Remote {
			if !auth.Authenticated {
				writeError(w, http.StatusUnauthorized, "remote access requires authentication to read or write preferences")
				return "", "", false
			}
			return db.AccountPreferenceScopeInstance, accountPreferencesInstanceID, true
		}
		if !s.requireSensitiveLocalToken(w, r) {
			return "", "", false
		}
		return db.AccountPreferenceScopeInstance, accountPreferencesInstanceID, true
	}
	user, ok := s.requireUser(w, r)
	if !ok {
		return "", "", false
	}
	if _, _, err := s.store.ClaimInstanceAccountPreferencesForFirstUser(r.Context(), user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return "", "", false
	}
	return db.AccountPreferenceScopeUser, user.ID, true
}

func (s *Server) writeAccountPreferencesStoreError(w http.ResponseWriter, r *http.Request, scopeKind, scopeID string, err error) {
	if !errors.Is(err, db.ErrConflict) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	current, currentErr := s.store.GetAccountPreferences(r.Context(), scopeKind, scopeID)
	if currentErr != nil {
		writeError(w, http.StatusInternalServerError, currentErr.Error())
		return
	}
	response, responseErr := accountPreferencesResponseFromDB(current)
	if responseErr != nil {
		writeError(w, http.StatusInternalServerError, responseErr.Error())
		return
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "current": response})
}

func decodeAccountPreferencesJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, accountPreferencesRequestBytes))
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			return errors.New("request body exceeds size limit")
		}
		return err
	}
	if !utf8.Valid(body) {
		return errors.New("request body must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func normalizeAccountPreferencesProfile(profile accountPreferencesProfile) (accountPreferencesProfile, error) {
	fields := []struct {
		name    string
		value   *string
		maximum int
	}{
		{name: "profile.displayName", value: &profile.DisplayName, maximum: 80},
		{name: "profile.roleLabel", value: &profile.RoleLabel, maximum: 80},
		{name: "profile.avatarInitials", value: &profile.AvatarInitials, maximum: 4},
		{name: "profile.gitName", value: &profile.GitName, maximum: 120},
		{name: "profile.gitEmail", value: &profile.GitEmail, maximum: 160},
		{name: "profile.workspaceLabel", value: &profile.WorkspaceLabel, maximum: 80},
	}
	for _, field := range fields {
		if !utf8.ValidString(*field.value) {
			return accountPreferencesProfile{}, fmt.Errorf("%s must be valid UTF-8", field.name)
		}
		for _, char := range *field.value {
			if unicode.IsControl(char) {
				return accountPreferencesProfile{}, fmt.Errorf("%s contains invalid control characters", field.name)
			}
		}
		*field.value = strings.TrimSpace(*field.value)
		if utf8.RuneCountInString(*field.value) > field.maximum {
			return accountPreferencesProfile{}, fmt.Errorf("%s exceeds %d characters", field.name, field.maximum)
		}
	}
	profile.AvatarInitials = strings.ToUpper(profile.AvatarInitials)
	if utf8.RuneCountInString(profile.AvatarInitials) > 4 {
		return accountPreferencesProfile{}, errors.New("profile.avatarInitials exceeds 4 characters after normalization")
	}
	avatarDataURL, err := normalizeAccountAvatarDataURL(profile.AvatarDataURL)
	if err != nil {
		return accountPreferencesProfile{}, err
	}
	profile.AvatarDataURL = avatarDataURL
	return profile, nil
}

func normalizeAccountAvatarDataURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	const prefix = "data:image/jpeg;base64,"
	if len(value) > accountPreferencesMaxAvatarDataURLLength || !strings.HasPrefix(value, prefix) {
		return "", errors.New("profile.avatarDataUrl must be a compressed JPEG data URL")
	}
	encoded := strings.TrimPrefix(value, prefix)
	if encoded == "" || len(encoded)%4 != 0 {
		return "", errors.New("profile.avatarDataUrl contains invalid base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("profile.avatarDataUrl contains invalid base64")
	}
	if len(decoded) == 0 || len(decoded) > accountPreferencesMaxAvatarBytes {
		return "", errors.New("profile.avatarDataUrl exceeds the compressed size limit")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil || format != "jpeg" {
		return "", errors.New("profile.avatarDataUrl must contain a valid JPEG image")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > accountPreferencesMaxAvatarDimension || config.Height > accountPreferencesMaxAvatarDimension {
		return "", errors.New("profile.avatarDataUrl exceeds the image dimensions limit")
	}
	return value, nil
}

func normalizePreferredModel(value string) (string, error) {
	if !utf8.ValidString(value) || len([]byte(value)) > 512 || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("preferredModel is invalid")
	}
	return value, nil
}

func normalizeAccountPreferencesModelVisibility(value accountPreferencesModelVisibility) (accountPreferencesModelVisibility, error) {
	if value.HiddenModels == nil {
		return accountPreferencesModelVisibility{}, errors.New("modelVisibility.hiddenModels must be an object")
	}
	if len(value.HiddenModels) > 512 {
		return accountPreferencesModelVisibility{}, errors.New("modelVisibility.hiddenModels exceeds 512 entries")
	}
	normalized := make(map[string]bool, len(value.HiddenModels))
	for key, hidden := range value.HiddenModels {
		if !utf8.ValidString(key) || len([]byte(key)) < 1 || len([]byte(key)) > 512 {
			return accountPreferencesModelVisibility{}, errors.New("modelVisibility.hiddenModels contains an invalid key")
		}
		for _, char := range key {
			if unicode.IsControl(char) {
				return accountPreferencesModelVisibility{}, errors.New("modelVisibility.hiddenModels contains an invalid key")
			}
		}
		normalized[key] = hidden
	}
	value.HiddenModels = normalized
	return value, nil
}

func accountPreferencesResponseFromDB(preferences db.AccountPreferences) (accountPreferencesResponse, error) {
	profile := defaultAccountPreferencesProfile()
	if len(preferences.ProfileJSON) > 0 {
		if err := decodeStoredAccountPreferencesJSON(preferences.ProfileJSON, &profile); err != nil {
			return accountPreferencesResponse{}, fmt.Errorf("decode stored profile: %w", err)
		}
		var err error
		profile, err = normalizeAccountPreferencesProfile(profile)
		if err != nil {
			return accountPreferencesResponse{}, fmt.Errorf("validate stored profile: %w", err)
		}
	}
	modelVisibility := defaultAccountPreferencesModelVisibility()
	if len(preferences.ModelVisibilityJSON) > 0 {
		if err := decodeStoredAccountPreferencesJSON(preferences.ModelVisibilityJSON, &modelVisibility); err != nil {
			return accountPreferencesResponse{}, fmt.Errorf("decode stored model visibility: %w", err)
		}
		var err error
		modelVisibility, err = normalizeAccountPreferencesModelVisibility(modelVisibility)
		if err != nil {
			return accountPreferencesResponse{}, fmt.Errorf("validate stored model visibility: %w", err)
		}
	}
	preferredModel, err := normalizePreferredModel(preferences.PreferredModel)
	if err != nil {
		return accountPreferencesResponse{}, fmt.Errorf("validate stored preferred model: %w", err)
	}
	scopeKey := preferences.ScopeKind + ":" + preferences.ScopeID
	return accountPreferencesResponse{
		ScopeKey:                  scopeKey,
		Profile:                   profile,
		PreferredModel:            preferredModel,
		ModelVisibility:           modelVisibility,
		SetupVersion:              preferences.SetupVersion,
		Revision:                  preferences.Revision,
		LocalStorageImportVersion: preferences.LocalStorageImportVersion,
		UpdatedAt:                 preferences.UpdatedAt,
	}, nil
}

func decodeStoredAccountPreferencesJSON(raw json.RawMessage, destination any) error {
	if !utf8.Valid(raw) {
		return errors.New("stored JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("stored JSON must contain exactly one value")
		}
		return err
	}
	return nil
}
