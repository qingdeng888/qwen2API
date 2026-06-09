package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// SPAHandler serves static files from a directory with SPA fallback to index.html
type SPAHandler struct {
	apiMux    *http.ServeMux
	staticDir string
	hasStatic bool
}

func NewSPAHandler(apiMux *http.ServeMux, staticDir string) http.Handler {
	_, err := os.Stat(filepath.Join(staticDir, "index.html"))
	return &SPAHandler{
		apiMux:    apiMux,
		staticDir: staticDir,
		hasStatic: err == nil,
	}
}

func (h *SPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// API routes always go to the mux
	if isAPIRoute(path) {
		h.apiMux.ServeHTTP(w, r)
		return
	}

	// If no frontend built, serve API only
	if !h.hasStatic {
		h.apiMux.ServeHTTP(w, r)
		return
	}

	// Try to serve static file
	filePath := filepath.Join(h.staticDir, path)
	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		http.ServeFile(w, r, filePath)
		return
	}

	// SPA fallback: serve index.html for all non-API, non-file routes
	indexPath := filepath.Join(h.staticDir, "index.html")
	http.ServeFile(w, r, indexPath)
}

func isAPIRoute(path string) bool {
	apiPrefixes := []string{
		"/api/",
		"/v1/",
		"/v1beta/",
		"/chat/",
		"/messages",
		"/anthropic/",
		"/models/",
		"/images/",
		"/embeddings",
		"/responses",
		"/healthz",
		"/readyz",
		"/keepalive",
	}
	for _, prefix := range apiPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	// Exact matches
	if path == "/api" || path == "/healthz" || path == "/readyz" || path == "/keepalive" {
		return true
	}
	return false
}
