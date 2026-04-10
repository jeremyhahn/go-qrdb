// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"io/fs"
	"net/http"
	"strings"
)

// NewSPAHandler creates an http.Handler that serves static files from the
// given filesystem and falls back to index.html for SPA client-side routing.
// Asset paths (containing a dot in the last segment) that don't exist return
// 404; all other paths return index.html.
func NewSPAHandler(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))
	return &spaHandler{
		assets:     assets,
		fileServer: fileServer,
	}
}

type spaHandler struct {
	assets     fs.FS
	fileServer http.Handler
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	// Never intercept API paths — let them return proper 404s.
	if strings.HasPrefix(path, "v1/") ||
		strings.HasPrefix(path, "kv/") ||
		strings.HasPrefix(path, "ts/") ||
		strings.HasPrefix(path, "users/") ||
		strings.HasPrefix(path, "roles/") ||
		strings.HasPrefix(path, "health") ||
		strings.HasPrefix(path, "status") ||
		strings.HasPrefix(path, "peers") ||
		strings.HasPrefix(path, "metrics") ||
		strings.HasPrefix(path, "capabilities") {
		http.NotFound(w, r)
		return
	}

	// Try to serve the exact file.
	if _, err := fs.Stat(h.assets, path); err == nil {
		h.fileServer.ServeHTTP(w, r)
		return
	}

	// If the path looks like a static asset (has a file extension), return 404.
	lastSlash := strings.LastIndex(path, "/")
	lastSegment := path
	if lastSlash >= 0 {
		lastSegment = path[lastSlash+1:]
	}
	if strings.Contains(lastSegment, ".") {
		http.NotFound(w, r)
		return
	}

	// SPA fallback: serve index.html for client-side routing.
	r.URL.Path = "/"
	h.fileServer.ServeHTTP(w, r)
}
