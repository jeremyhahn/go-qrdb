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
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig configures Cross-Origin Resource Sharing for the REST API.
type CORSConfig struct {
	// AllowedOrigins is the list of allowed request origins.
	// Use ["*"] to allow all origins.
	AllowedOrigins []string

	// AllowedHeaders is the list of allowed request headers.
	AllowedHeaders []string

	// MaxAge is the maximum age (in seconds) for preflight cache.
	MaxAge int
}

// corsMiddleware wraps an HTTP handler with CORS support. It handles
// preflight OPTIONS requests and sets the appropriate response headers
// for cross-origin access.
func corsMiddleware(cfg CORSConfig, next http.Handler) http.Handler {
	allowAll := len(cfg.AllowedOrigins) == 1 && cfg.AllowedOrigins[0] == "*"

	// Pre-compute static header values.
	allowHeaders := strings.Join(cfg.AllowedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)
	allowMethods := "GET, POST, PUT, DELETE, OPTIONS"

	// Build origin lookup set for O(1) matching when not allowing all.
	origins := make(map[string]struct{}, len(cfg.AllowedOrigins))
	if !allowAll {
		for _, o := range cfg.AllowedOrigins {
			origins[o] = struct{}{}
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Not a CORS request — pass through.
			next.ServeHTTP(w, r)
			return
		}

		// Determine if the origin is allowed.
		allowed := allowAll
		if !allowed {
			_, allowed = origins[origin]
		}
		if !allowed {
			next.ServeHTTP(w, r)
			return
		}

		// Set CORS response headers.
		h := w.Header()
		if allowAll {
			h.Set("Access-Control-Allow-Origin", "*")
		} else {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
		}

		// Handle preflight.
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", allowMethods)
			h.Set("Access-Control-Allow-Headers", allowHeaders)
			h.Set("Access-Control-Max-Age", maxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
