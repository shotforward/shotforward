// Package webui serves the embedded ShotForward single-page application.
package webui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed dist/*
var embedded embed.FS

type Handler struct {
	files fs.FS
}

func NewHandler() *Handler {
	files, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic("embedded web UI is unavailable: " + err.Error())
	}
	return &Handler{files: files}
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
	if name == "" || !strings.Contains(path.Base(name), ".") {
		name = "index.html"
	}
	content, err := fs.ReadFile(h.files, name)
	if err != nil {
		if strings.Contains(path.Base(name), ".") {
			http.NotFound(response, request)
			return
		}
		content, err = fs.ReadFile(h.files, "index.html")
		if err != nil {
			http.Error(response, "ShotForward UI is unavailable", http.StatusInternalServerError)
			return
		}
		name = "index.html"
	}

	if name == "index.html" {
		response.Header().Set("Cache-Control", "no-cache")
	} else {
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		response.Header().Set("Content-Type", contentType)
	}
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = response.Write(content)
	}
}
