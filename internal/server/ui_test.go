package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

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
