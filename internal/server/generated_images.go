package server

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	"autoto/internal/imageassets"
)

func (s *Server) getGeneratedImage(w http.ResponseWriter, r *http.Request) {
	if s.store == nil || s.generatedImages == nil {
		writeError(w, http.StatusNotFound, "generated image not found")
		return
	}
	asset, err := s.store.GetGeneratedImage(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "messageId"), chi.URLParam(r, "assetId"))
	if err != nil {
		if db.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "generated image not found")
			return
		}
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	if asset.Status != "ready" || asset.MIMEType != "image/png" {
		writeError(w, http.StatusNotFound, "generated image not found")
		return
	}
	file, err := s.generatedImages.Open(asset.StorageKey, imageassets.Expected{
		SHA256: asset.SHA256, ByteSize: asset.ByteSize, Width: asset.Width, Height: asset.Height,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "generated image not found")
		return
	}
	defer file.Close()

	etag := `"` + asset.SHA256 + `"`
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", strconv.FormatInt(asset.ByteSize, 10))
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": safeGeneratedImageFilename(asset.Filename)}))
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, file)
}

func safeGeneratedImageFilename(filename string) string {
	filename = path.Base(strings.ReplaceAll(filename, `\`, "/"))
	var builder strings.Builder
	for _, char := range filename {
		if unicode.IsControl(char) || char == '/' || char == '\\' {
			continue
		}
		if builder.Len() >= 180 {
			break
		}
		builder.WriteRune(char)
	}
	filename = strings.Trim(builder.String(), " .")
	if filename == "" || filename == "." || filename == ".." {
		filename = "generated-image.png"
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".png") {
		filename += ".png"
	}
	return filename
}
