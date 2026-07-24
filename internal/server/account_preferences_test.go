package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func newAccountPreferencesTestServer(t *testing.T) (*Server, *db.Store) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "preferences.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{
		Auth: config.AuthConfig{RegistrationOpen: true},
		Security: config.SecurityConfig{
			AccessPasswordHash:      hash,
			AllowRemoteFullAccess:   true,
			DefaultRemoteAccessMode: remoteAccessModeFull,
			CredentialRevision:      1,
		},
	}, store, nil, nil)
	return app, store
}

func accountPreferencesRequest(t *testing.T, app *Server, method, path, body string, configure func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := newTestRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if configure != nil {
		configure(request)
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

func decodeAccountPreferencesResponse(t *testing.T, recorder *httptest.ResponseRecorder) accountPreferencesResponse {
	t.Helper()
	var response accountPreferencesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode preferences response: %v; body=%s", err, recorder.Body.String())
	}
	return response
}

func testAvatarDataURL(width, height int) string {
	var buffer bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			picture.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	if err := jpeg.Encode(&buffer, picture, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func accountPreferencesPatchBody(revision int64, displayName string) string {
	body, _ := json.Marshal(map[string]any{
		"expectedRevision": revision,
		"profile": map[string]any{
			"displayName": displayName, "roleLabel": " Developer ", "avatarInitials": "ab", "avatarDataUrl": testAvatarDataURL(2, 2),
			"gitName": " Example ", "gitEmail": " example@example.test ", "workspaceLabel": " Workspace ",
		},
		"preferredModel": "provider:model",
		"setupVersion":   db.AccountPreferencesCurrentSetupVersion,
		"modelVisibility": map[string]any{
			"hiddenModels": map[string]bool{"provider:hidden": true}, "showUnconfiguredProviders": true,
		},
	})
	return string(body)
}

func accountPreferencesImportBody(displayName string) string {
	body, _ := json.Marshal(map[string]any{
		"version": 1,
		"profile": map[string]any{
			"displayName": displayName, "roleLabel": "Imported", "avatarInitials": "im", "avatarDataUrl": testAvatarDataURL(2, 2),
			"gitName": "Importer", "gitEmail": "import@example.test", "workspaceLabel": "Imported workspace",
		},
		"preferredModel": "provider:imported",
		"modelVisibility": map[string]any{
			"hiddenModels": map[string]bool{"provider:old": true}, "showUnconfiguredProviders": false,
		},
	})
	return string(body)
}

func withLocalPreferencesToken(app *Server) func(*http.Request) {
	return func(request *http.Request) { request.Header.Set(localTokenHeader, app.localToken) }
}

func withUserSession(cookie *http.Cookie) func(*http.Request) {
	return func(request *http.Request) { request.AddCookie(cookie) }
}

func withRemotePreferencesSession(t *testing.T, app *Server, mode string) func(*http.Request) {
	t.Helper()
	token, _, err := app.newRemoteAccessSession(mode)
	if err != nil {
		t.Fatal(err)
	}
	return func(request *http.Request) {
		request.Host = "remote.example.test"
		markRemoteHTTPS(request)
		request.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: token})
	}
}

func TestAccountPreferencesDefaultPatchConflictAndImportOnce(t *testing.T) {
	t.Run("default patch and conflict", func(t *testing.T) {
		app, _ := newAccountPreferencesTestServer(t)
		get := accountPreferencesRequest(t, app, http.MethodGet, "/api/preferences", "", withLocalPreferencesToken(app))
		if get.Code != http.StatusOK {
			t.Fatalf("default GET returned %d: %s", get.Code, get.Body.String())
		}
		initial := decodeAccountPreferencesResponse(t, get)
		if initial.ScopeKey != "instance:default" || initial.Profile.AvatarInitials != "AT" || initial.ModelVisibility.HiddenModels == nil || initial.SetupVersion != 0 || initial.LocalStorageImportVersion != 0 {
			t.Fatalf("unexpected default preferences: %+v", initial)
		}

		patch := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", accountPreferencesPatchBody(initial.Revision, " Alice "), withLocalPreferencesToken(app))
		if patch.Code != http.StatusOK {
			t.Fatalf("PATCH returned %d: %s", patch.Code, patch.Body.String())
		}
		updated := decodeAccountPreferencesResponse(t, patch)
		if updated.Revision <= initial.Revision || updated.Profile.DisplayName != "Alice" || updated.Profile.AvatarInitials != "AB" || updated.Profile.AvatarDataURL != testAvatarDataURL(2, 2) || updated.Profile.RoleLabel != "Developer" || updated.PreferredModel != "provider:model" || updated.SetupVersion != db.AccountPreferencesCurrentSetupVersion || !updated.ModelVisibility.HiddenModels["provider:hidden"] || !updated.ModelVisibility.ShowUnconfiguredProviders {
			t.Fatalf("unexpected patched preferences: %+v", updated)
		}

		conflict := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", accountPreferencesPatchBody(initial.Revision, "Stale"), withLocalPreferencesToken(app))
		if conflict.Code != http.StatusConflict {
			t.Fatalf("stale PATCH returned %d: %s", conflict.Code, conflict.Body.String())
		}
		var conflictBody struct {
			Error   string                     `json:"error"`
			Current accountPreferencesResponse `json:"current"`
		}
		if err := json.Unmarshal(conflict.Body.Bytes(), &conflictBody); err != nil {
			t.Fatal(err)
		}
		if conflictBody.Error == "" || conflictBody.Current.Revision != updated.Revision || conflictBody.Current.Profile.DisplayName != "Alice" {
			t.Fatalf("conflict did not include the current safe snapshot: %+v", conflictBody)
		}
	})

	t.Run("local storage import is applied once", func(t *testing.T) {
		app, _ := newAccountPreferencesTestServer(t)
		first := accountPreferencesRequest(t, app, http.MethodPost, "/api/preferences/import-local", accountPreferencesImportBody("First import"), withLocalPreferencesToken(app))
		if first.Code != http.StatusOK {
			t.Fatalf("first import returned %d: %s", first.Code, first.Body.String())
		}
		firstResponse := decodeAccountPreferencesResponse(t, first)
		if firstResponse.LocalStorageImportVersion != 1 || firstResponse.SetupVersion != 0 || firstResponse.Profile.DisplayName != "First import" {
			t.Fatalf("unexpected first import response: %+v", firstResponse)
		}
		second := accountPreferencesRequest(t, app, http.MethodPost, "/api/preferences/import-local", accountPreferencesImportBody("Second import"), withLocalPreferencesToken(app))
		if second.Code != http.StatusOK {
			t.Fatalf("second import returned %d: %s", second.Code, second.Body.String())
		}
		secondResponse := decodeAccountPreferencesResponse(t, second)
		if secondResponse.Profile.DisplayName != "First import" || secondResponse.Revision != firstResponse.Revision || secondResponse.LocalStorageImportVersion != 1 {
			t.Fatalf("second import overwrote the first: first=%+v second=%+v", firstResponse, secondResponse)
		}
	})
}

func TestAccountPreferencesAvatarValidation(t *testing.T) {
	app, _ := newAccountPreferencesTestServer(t)
	get := accountPreferencesRequest(t, app, http.MethodGet, "/api/preferences", "", withLocalPreferencesToken(app))
	initial := decodeAccountPreferencesResponse(t, get)
	valid := testAvatarDataURL(2, 2)
	invalidBodies := map[string]string{
		"wrong mime":              `data:image/png;base64,AAAA`,
		"bad base64":              `data:image/jpeg;base64,not-valid`,
		"oversized encoded value": "data:image/jpeg;base64," + strings.Repeat("A", accountPreferencesMaxAvatarDataURLLength),
		"oversized decoded value": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, accountPreferencesMaxAvatarBytes+1)),
		"oversized dimensions":    testAvatarDataURL(accountPreferencesMaxAvatarDimension+1, 1),
	}
	for name, avatarDataURL := range invalidBodies {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"expectedRevision": initial.Revision,
				"profile": map[string]any{
					"avatarInitials": "AV", "avatarDataUrl": avatarDataURL,
				},
			})
			response := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", string(body), withLocalPreferencesToken(app))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid avatar returned %d: %s", response.Code, response.Body.String())
			}
		})
	}

	body, _ := json.Marshal(map[string]any{
		"expectedRevision": initial.Revision,
		"profile":          map[string]any{"displayName": "Photo", "avatarInitials": "PH", "avatarDataUrl": valid},
	})
	response := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", string(body), withLocalPreferencesToken(app))
	if response.Code != http.StatusOK {
		t.Fatalf("valid avatar returned %d: %s", response.Code, response.Body.String())
	}
	updated := decodeAccountPreferencesResponse(t, response)
	if updated.Profile.AvatarDataURL != valid {
		t.Fatalf("valid avatar was not persisted: %q", updated.Profile.AvatarDataURL)
	}
}

func TestAccountPreferencesNoUserAuthenticationMatrix(t *testing.T) {
	type endpoint struct {
		name, method, path, body string
	}
	endpoints := []endpoint{
		{name: "get", method: http.MethodGet, path: "/api/preferences"},
		{name: "patch", method: http.MethodPatch, path: "/api/preferences", body: accountPreferencesPatchBody(0, "Allowed")},
		{name: "import", method: http.MethodPost, path: "/api/preferences/import-local", body: accountPreferencesImportBody("Allowed")},
	}
	for _, endpoint := range endpoints {
		endpoint := endpoint
		t.Run(endpoint.name, func(t *testing.T) {
			t.Run("canonical local token", func(t *testing.T) {
				app, _ := newAccountPreferencesTestServer(t)
				response := accountPreferencesRequest(t, app, endpoint.method, endpoint.path, endpoint.body, withLocalPreferencesToken(app))
				if response.Code != http.StatusOK {
					t.Fatalf("canonical token returned %d: %s", response.Code, response.Body.String())
				}
			})
			t.Run("full remote session", func(t *testing.T) {
				app, _ := newAccountPreferencesTestServer(t)
				response := accountPreferencesRequest(t, app, endpoint.method, endpoint.path, endpoint.body, withRemotePreferencesSession(t, app, remoteAccessModeFull))
				if response.Code != http.StatusOK {
					t.Fatalf("full remote session returned %d: %s", response.Code, response.Body.String())
				}
			})
			for _, denied := range []struct {
				name      string
				want      int
				configure func(*Server) func(*http.Request)
			}{
				{name: "missing local token", want: http.StatusUnauthorized, configure: func(*Server) func(*http.Request) { return nil }},
				{name: "wrong local token", want: http.StatusUnauthorized, configure: func(*Server) func(*http.Request) {
					return func(request *http.Request) { request.Header.Set(localTokenHeader, "wrong") }
				}},
				{name: "restricted remote", want: http.StatusForbidden, configure: func(app *Server) func(*http.Request) {
					return withRemotePreferencesSession(t, app, remoteAccessModeRestricted)
				}},
				{name: "restricted remote with local token", want: http.StatusForbidden, configure: func(app *Server) func(*http.Request) {
					remote := withRemotePreferencesSession(t, app, remoteAccessModeRestricted)
					return func(request *http.Request) {
						remote(request)
						request.Header.Set(localTokenHeader, app.localToken)
					}
				}},
				{name: "unauthenticated remote", want: http.StatusUnauthorized, configure: func(*Server) func(*http.Request) {
					return func(request *http.Request) { request.Host = "remote.example.test"; markRemoteHTTPS(request) }
				}},
				{name: "unauthenticated remote with local token", want: http.StatusUnauthorized, configure: func(app *Server) func(*http.Request) {
					return func(request *http.Request) {
						request.Host = "remote.example.test"
						markRemoteHTTPS(request)
						request.Header.Set(localTokenHeader, app.localToken)
					}
				}},
			} {
				denied := denied
				t.Run(denied.name, func(t *testing.T) {
					app, _ := newAccountPreferencesTestServer(t)
					response := accountPreferencesRequest(t, app, endpoint.method, endpoint.path, endpoint.body, denied.configure(app))
					if response.Code != denied.want {
						t.Fatalf("denied request returned %d want %d: %s", response.Code, denied.want, response.Body.String())
					}
				})
			}
		})
	}
}

func TestAccountPreferencesUserScopeIsolationAndNoTokenBypass(t *testing.T) {
	app, store := newAccountPreferencesTestServer(t)
	firstCookie := registerCollaborationTestUser(t, app, "preference-first")
	secondCookie := registerCollaborationTestUser(t, app, "preference-second")
	firstUser, _, err := store.GetUserByHandle(context.Background(), "preference-first")
	if err != nil {
		t.Fatal(err)
	}

	for _, endpoint := range []struct{ method, path, body string }{
		{method: http.MethodGet, path: "/api/preferences"},
		{method: http.MethodPatch, path: "/api/preferences", body: accountPreferencesPatchBody(0, "Denied")},
		{method: http.MethodPost, path: "/api/preferences/import-local", body: accountPreferencesImportBody("Denied")},
	} {
		withoutSession := accountPreferencesRequest(t, app, endpoint.method, endpoint.path, endpoint.body, nil)
		if withoutSession.Code != http.StatusUnauthorized {
			t.Fatalf("%s without user session returned %d: %s", endpoint.path, withoutSession.Code, withoutSession.Body.String())
		}
		localTokenOnly := accountPreferencesRequest(t, app, endpoint.method, endpoint.path, endpoint.body, withLocalPreferencesToken(app))
		if localTokenOnly.Code != http.StatusUnauthorized {
			t.Fatalf("%s allowed local-token user bypass: %d %s", endpoint.path, localTokenOnly.Code, localTokenOnly.Body.String())
		}
	}

	firstGet := accountPreferencesRequest(t, app, http.MethodGet, "/api/preferences", "", withUserSession(firstCookie))
	if firstGet.Code != http.StatusOK {
		t.Fatalf("first GET returned %d: %s", firstGet.Code, firstGet.Body.String())
	}
	firstInitial := decodeAccountPreferencesResponse(t, firstGet)
	if firstInitial.ScopeKey != "user:"+firstUser.ID {
		t.Fatalf("unexpected first user scope: %+v", firstInitial)
	}
	firstPatch := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", accountPreferencesPatchBody(firstInitial.Revision, "First only"), withUserSession(firstCookie))
	if firstPatch.Code != http.StatusOK {
		t.Fatalf("first PATCH returned %d: %s", firstPatch.Code, firstPatch.Body.String())
	}

	secondGet := accountPreferencesRequest(t, app, http.MethodGet, "/api/preferences", "", withUserSession(secondCookie))
	if secondGet.Code != http.StatusOK {
		t.Fatalf("second GET returned %d: %s", secondGet.Code, secondGet.Body.String())
	}
	secondInitial := decodeAccountPreferencesResponse(t, secondGet)
	if secondInitial.ScopeKey == firstInitial.ScopeKey || secondInitial.Profile.DisplayName != "" {
		t.Fatalf("second user observed first user's preferences: %+v", secondInitial)
	}

	forged := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", `{"expectedRevision":0,"userId":"`+firstUser.ID+`","preferredModel":"forged:model"}`, withUserSession(secondCookie))
	if forged.Code != http.StatusBadRequest {
		t.Fatalf("forged userId returned %d: %s", forged.Code, forged.Body.String())
	}
	firstAfterForgery := accountPreferencesRequest(t, app, http.MethodGet, "/api/preferences", "", withUserSession(firstCookie))
	firstCurrent := decodeAccountPreferencesResponse(t, firstAfterForgery)
	if firstCurrent.Profile.DisplayName != "First only" || firstCurrent.PreferredModel != "provider:model" {
		t.Fatalf("forged userId changed the first user's preferences: %+v", firstCurrent)
	}
}

func TestAccountPreferencesFirstUserClaimsInstanceAndSecondUsesDefaults(t *testing.T) {
	app, store := newAccountPreferencesTestServer(t)
	instanceGet := accountPreferencesRequest(t, app, http.MethodGet, "/api/preferences", "", withLocalPreferencesToken(app))
	initial := decodeAccountPreferencesResponse(t, instanceGet)
	instancePatch := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", accountPreferencesPatchBody(initial.Revision, "Inherited instance"), withLocalPreferencesToken(app))
	if instancePatch.Code != http.StatusOK {
		t.Fatalf("instance PATCH returned %d: %s", instancePatch.Code, instancePatch.Body.String())
	}

	firstCookie := registerCollaborationTestUser(t, app, "claim-first")
	firstUser, _, err := store.GetUserByHandle(context.Background(), "claim-first")
	if err != nil {
		t.Fatal(err)
	}
	firstGet := accountPreferencesRequest(t, app, http.MethodGet, "/api/preferences", "", withUserSession(firstCookie))
	if firstGet.Code != http.StatusOK {
		t.Fatalf("first user GET returned %d: %s", firstGet.Code, firstGet.Body.String())
	}
	first := decodeAccountPreferencesResponse(t, firstGet)
	if first.ScopeKey != "user:"+firstUser.ID || first.Profile.DisplayName != "Inherited instance" || first.PreferredModel != "provider:model" || first.SetupVersion != db.AccountPreferencesCurrentSetupVersion {
		t.Fatalf("first user did not inherit instance preferences: %+v", first)
	}

	secondCookie := registerCollaborationTestUser(t, app, "claim-second")
	secondGet := accountPreferencesRequest(t, app, http.MethodGet, "/api/preferences", "", withUserSession(secondCookie))
	if secondGet.Code != http.StatusOK {
		t.Fatalf("second user GET returned %d: %s", secondGet.Code, secondGet.Body.String())
	}
	second := decodeAccountPreferencesResponse(t, secondGet)
	if second.Profile.DisplayName != "" || second.PreferredModel != "" || second.SetupVersion != 0 || len(second.ModelVisibility.HiddenModels) != 0 {
		t.Fatalf("second user should receive defaults, got %+v", second)
	}
}

func TestAccountPreferencesValidation(t *testing.T) {
	app, _ := newAccountPreferencesTestServer(t)
	invalid := []string{
		`{}`,
		`{"expectedRevision":-1,"preferredModel":"x"}`,
		`{"expectedRevision":0,"userId":"forged","preferredModel":"x"}`,
		`{"expectedRevision":0,"scope":"instance:default","preferredModel":"x"}`,
		`{"expectedRevision":0,"profile":{"displayName":"bad\nname"}}`,
		`{"expectedRevision":0,"preferredModel":"bad\rmodel"}`,
		`{"expectedRevision":0,"setupVersion":-1}`,
		`{"expectedRevision":0,"setupVersion":2}`,
		`{"expectedRevision":0,"setupVersion":1001}`,
		`{"expectedRevision":0,"modelVisibility":{"hiddenModels":null,"showUnconfiguredProviders":false}}`,
		`{"expectedRevision":0,"modelVisibility":{"hiddenModels":{"":true},"showUnconfiguredProviders":false}}`,
	}
	for _, body := range invalid {
		response := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", body, withLocalPreferencesToken(app))
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid PATCH %s returned %d: %s", body, response.Code, response.Body.String())
		}
	}
	tooManyHidden := make(map[string]bool, 513)
	for index := 0; index < 513; index++ {
		tooManyHidden[string(rune(0x1000+index))] = true
	}
	tooManyBody, _ := json.Marshal(map[string]any{"expectedRevision": 0, "modelVisibility": map[string]any{"hiddenModels": tooManyHidden, "showUnconfiguredProviders": false}})
	for name, body := range map[string]string{
		"invalid UTF-8":          string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}),
		"oversized body":         `{"expectedRevision":0,"preferredModel":"` + strings.Repeat("x", int(accountPreferencesRequestBytes)) + `"}`,
		"too many hidden models": string(tooManyBody),
	} {
		response := accountPreferencesRequest(t, app, http.MethodPatch, "/api/preferences", body, withLocalPreferencesToken(app))
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d: %s", name, response.Code, response.Body.String())
		}
	}
	wrongVersion := strings.Replace(accountPreferencesImportBody("invalid"), `"version":1`, `"version":2`, 1)
	response := accountPreferencesRequest(t, app, http.MethodPost, "/api/preferences/import-local", wrongVersion, withLocalPreferencesToken(app))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported import version returned %d: %s", response.Code, response.Body.String())
	}
}
