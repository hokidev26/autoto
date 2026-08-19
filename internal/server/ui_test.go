package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"autoto/internal/config"
)

// Browsers key ES module identity on the full URL, so a ?v= query string on an
// import forks that module into a second instance with duplicated state; the
// i18n locale split was the visible symptom. Freshness comes from the content
// ETag plus stale-while-revalidate, so no embedded source may reference a
// ?v= stamp.
func TestEmbeddedStaticSourcesCarryNoVersionQueryStrings(t *testing.T) {
	versioned := regexp.MustCompile(`\?v=[A-Za-z0-9-]`)
	err := fs.WalkDir(staticFiles, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch path.Ext(name) {
		case ".mjs", ".js", ".html", ".css":
		default:
			return nil
		}
		data, readErr := fs.ReadFile(staticFiles, name)
		if readErr != nil {
			return readErr
		}
		if versioned.Match(data) {
			t.Errorf("%s references a ?v= cache stamp; rely on ETag revalidation instead", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUIAssetsRevalidateInsteadOfRedownloading(t *testing.T) {
	// The frontend is ~190 modules and several megabytes of text. Serving it
	// with no-store meant every byte came down again on every load, which is
	// worst on a phone. Assets must instead carry a content ETag so the browser
	// can be told "unchanged" without a body.
	srv := &Server{}
	router := chi.NewRouter()
	srv.mountUI(router)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/ui/modules/dom.mjs", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("expected the asset to be served, got %d", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("asset carries no ETag, so the browser can never revalidate it")
	}
	if cache := first.Header().Get("Cache-Control"); strings.Contains(cache, "no-store") {
		t.Fatalf("no-store defeats the ETag entirely, got %q", cache)
	}
	if cache := first.Header().Get("Cache-Control"); !strings.Contains(cache, "private") || !strings.Contains(cache, "stale-while-revalidate") {
		t.Fatalf("shared CDNs must not store UI assets, and reload must not block on 304, got Cache-Control %q", cache)
	}
	if cache := first.Header().Get("Cache-Control"); strings.Contains(cache, "no-cache") {
		t.Fatalf("no-cache forces a blocking revalidation before paint, got %q", cache)
	}
	if cdn := first.Header().Get("CDN-Cache-Control"); cdn != "no-store" {
		t.Fatalf("CDN-Cache-Control = %q, want no-store", cdn)
	}
	if first.Body.Len() == 0 {
		t.Fatal("expected the first response to carry the asset")
	}

	// The ETag has to come from the bytes, not from a build stamp or a mtime:
	// go:embed reports a zero mtime, and serving a stale asset after a rebuild
	// would be far worse than an extra request.
	data, err := staticFiles.ReadFile("static/modules/dom.mjs")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if want := `W/"` + hex.EncodeToString(sum[:8]) + `"`; etag != want {
		t.Fatalf("ETag is not a content hash: got %q want %q", etag, want)
	}

	second := httptest.NewRecorder()
	revalidate := httptest.NewRequest(http.MethodGet, "/ui/modules/dom.mjs", nil)
	revalidate.Header.Set("If-None-Match", etag)
	router.ServeHTTP(second, revalidate)
	if second.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on revalidation, got %d", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("a 304 must carry no body, got %d bytes", second.Body.Len())
	}
}

func TestLoopbackUIAssetsMustRevalidateBeforePaint(t *testing.T) {
	// Desktop WebView talks to 127.0.0.1. Stale-while-revalidate would keep
	// yesterday's modules on screen after a replaced exe; loopback is fast
	// enough to wait for the 304. Tunneled clients keep SWR (see the test above).
	srv := &Server{}
	router := chi.NewRouter()
	srv.mountUI(router)

	req := httptest.NewRequest(http.MethodGet, "/ui/modules/dom.mjs", nil)
	req.Host = "127.0.0.1:7788"
	req.RemoteAddr = "127.0.0.1:4321"
	first := httptest.NewRecorder()
	router.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("expected the asset to be served, got %d", first.Code)
	}
	cache := first.Header().Get("Cache-Control")
	if !strings.Contains(cache, "private") || !strings.Contains(cache, "must-revalidate") {
		t.Fatalf("loopback UI assets must revalidate before paint, got Cache-Control %q", cache)
	}
	if strings.Contains(cache, "stale-while-revalidate") {
		t.Fatalf("loopback must not serve stale JS after a replaced exe, got Cache-Control %q", cache)
	}
	if first.Header().Get("CDN-Cache-Control") != "no-store" {
		t.Fatalf("CDN-Cache-Control = %q, want no-store", first.Header().Get("CDN-Cache-Control"))
	}
}

func TestUIStylesheetIsServedAsOneCascade(t *testing.T) {
	// styles.css is an @import index on disk so tests can audit cascade order.
	// Serving those imports as-is made a phone wait on sixteen stylesheet
	// round trips before the boot overlay finished painting.
	srv := &Server{}
	router := chi.NewRouter()
	srv.mountUI(router)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/ui/styles.css", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("expected the bundled stylesheet, got %d", first.Code)
	}
	body := first.Body.String()
	if strings.Contains(body, `@import url("styles/`) {
		t.Fatal("GET /ui/styles.css still lists @import urls instead of the cascade")
	}
	if !strings.Contains(body, ".boot-transition") {
		t.Fatal("bundled stylesheet is missing base.css")
	}
	if !strings.Contains(body, ".git-modal-body") {
		t.Fatal("bundled stylesheet is missing a later cascade file")
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("bundled stylesheet carries no ETag")
	}
	sum := sha256.Sum256(first.Body.Bytes())
	if want := `W/"` + hex.EncodeToString(sum[:8]) + `"`; etag != want {
		t.Fatalf("bundled ETag is not a content hash: got %q want %q", etag, want)
	}

	second := httptest.NewRecorder()
	revalidate := httptest.NewRequest(http.MethodGet, "/ui/styles.css", nil)
	revalidate.Header.Set("If-None-Match", etag)
	router.ServeHTTP(second, revalidate)
	if second.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on bundled stylesheet revalidation, got %d", second.Code)
	}
}

// uiDocumentCSPNonceForTest extracts the per-response script nonce from a
// Content-Security-Policy header value.
func uiDocumentCSPNonceForTest(t *testing.T, csp string) string {
	t.Helper()
	const marker = "'nonce-"
	start := strings.Index(csp, marker)
	if start < 0 {
		t.Fatalf("CSP carries no script nonce: %q", csp)
	}
	rest := csp[start+len(marker):]
	end := strings.Index(rest, "'")
	if end <= 0 {
		t.Fatalf("CSP nonce is malformed: %q", csp)
	}
	return rest[:end]
}

func TestUIDocumentCSPLocksScriptsToNoncedSelf(t *testing.T) {
	// The SPA document used to ship without script-src, leaving no second line
	// of defense against XSS. The policy must restrict scripts to same-origin
	// plus a nonce that authorizes only the injected local-token snippet, and
	// the nonce must be fresh on every response or it stops being a nonce.
	app := New(config.Config{}, nil, nil, nil)
	serve := func() (*httptest.ResponseRecorder, string) {
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("expected the document to be served, got %d", recorder.Code)
		}
		return recorder, uiDocumentCSPNonceForTest(t, recorder.Header().Get("Content-Security-Policy"))
	}

	first, firstNonce := serve()
	csp := first.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'self'",
		"script-src 'self' 'nonce-" + firstNonce + "'",
		"object-src 'none'",
		"base-uri 'self'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("document CSP is missing %q: %q", directive, csp)
		}
	}
	if !strings.Contains(first.Body.String(), `<script nonce="`+firstNonce+`">window.AUTOTO_LOCAL_TOKEN=`) {
		t.Fatal("the injected local-token script does not carry the response nonce, so the CSP would block it")
	}

	_, secondNonce := serve()
	if secondNonce == firstNonce {
		t.Fatalf("expected a fresh nonce per response, got %q twice", firstNonce)
	}
}

func TestUIDocumentIsNeverCached(t *testing.T) {
	// index.html carries the canonical local API token for same-machine
	// sessions. Whatever the assets do, the document itself must not be stored.
	srv := &Server{}
	router := chi.NewRouter()
	srv.mountUI(router)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected the document to be served, got %d", recorder.Code)
	}
	if cache := recorder.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
		t.Fatalf("the document must stay no-store, got %q", cache)
	}
	if etag := recorder.Header().Get("ETag"); etag != "" {
		t.Fatalf("the document must not be revalidatable, got ETag %q", etag)
	}
}
