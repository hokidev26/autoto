package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5/middleware"
)

//go:embed static/* static/modules/* static/icons/*
var staticFiles embed.FS

const uiDocumentCSP = "object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

// uiAssetETags maps each embedded asset to a hash of its bytes, so the browser
// can revalidate instead of re-downloading. Built once, from content rather
// than modification time: go:embed reports a zero mtime, which is why
// http.FileServer emits no Last-Modified and every conditional request missed.
var uiAssetETags = sync.OnceValue(func() map[string]string {
	tags := make(map[string]string, 256)
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return tags
	}
	_ = fs.WalkDir(static, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		data, readErr := fs.ReadFile(static, path)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		// Weak: the same asset is served both plain and gzipped, and a weak tag
		// is the correct way to say those are the same representation.
		tags[path] = `W/"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return tags
})

func (s *Server) mountUI(r interface {
	Get(pattern string, h http.HandlerFunc)
	Handle(pattern string, h http.Handler)
}) {
	r.Get("/", s.index)
	static, _ := fs.Sub(staticFiles, "static")
	fileServer := http.StripPrefix("/ui/", http.FileServer(http.FS(static)))
	// The frontend ships ~190 modules and roughly 4.6MB of text. no-store meant
	// every one came down again on every load, which is worst on a phone.
	// Compressing them and letting the browser revalidate is the whole fix.
	compress := middleware.Compress(5)
	r.Handle("/ui/*", compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// no-cache is not no-store: it means "ask me first", and the answer is a
		// 304 with no body whenever the asset has not changed. Long-lived
		// immutable caching is deliberately not used here -- most module imports
		// carry no ?v= at all, and the ?v= tags that do exist are hand-written
		// feature names rather than content hashes, so they cannot be trusted to
		// change when a file does. Getting that wrong serves a stale app after a
		// rebuild, which is far worse than one revalidation request.
		if etag, ok := uiAssetETags()[strings.TrimPrefix(r.URL.Path, "/ui/")]; ok {
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			setNoStore(w)
		}
		fileServer.ServeHTTP(w, r)
	})))
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/ui/") {
		http.NotFound(w, r)
		return
	}
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	setNoStore(w)
	setUIDocumentSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.remoteAccessGateRequired(r) {
		// Remote sessions authenticate with their own HttpOnly cookie. Never expose
		// the process-wide canonical local token through a tunneled or exposed page.
		_, _ = w.Write(data)
		return
	}
	setLocalTokenCookie(w, r, s.localToken)
	_, _ = w.Write(injectLocalToken(data, s.localToken))
}

func injectLocalToken(data []byte, token string) []byte {
	encoded, err := json.Marshal(token)
	if err != nil {
		encoded = []byte(`""`)
	}
	snippet := `<script>window.AUTOTO_LOCAL_TOKEN=` + string(encoded) + `;window.CODEHARBOR_LOCAL_TOKEN=window.AUTOTO_LOCAL_TOKEN;</script>`
	text := string(data)
	if strings.Contains(text, "</head>") {
		text = strings.Replace(text, "</head>", snippet+"\n  </head>", 1)
	} else {
		text = snippet + text
	}
	return []byte(text)
}

func setLocalTokenCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     localTokenCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func setUIDocumentSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", uiDocumentCSP)
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
}
