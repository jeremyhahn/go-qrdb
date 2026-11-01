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
	p := newCORSPolicy(cfg)
	return p.headers(p.preflight(next))
}

// corsPolicy is one CORSConfig compiled for matching. Its two layers,
// headers and preflight, together make corsMiddleware; the server mounts
// them apart so a layer between them -- the client-address limiter, which
// charges a preflight -- answers an allowed origin with the same
// Access-Control-Allow-Origin a served request gets.
type corsPolicy struct {
	allowAll bool
	origins  map[string]struct{}

	allowHeaders string
	maxAge       string
	allowMethods string
}

func newCORSPolicy(cfg CORSConfig) *corsPolicy {
	p := &corsPolicy{
		allowAll: len(cfg.AllowedOrigins) == 1 && cfg.AllowedOrigins[0] == "*",
		// Pre-compute static header values.
		allowHeaders: strings.Join(cfg.AllowedHeaders, ", "),
		maxAge:       strconv.Itoa(cfg.MaxAge),
		allowMethods: "GET, POST, PUT, DELETE, OPTIONS",
	}
	// Build origin lookup set for O(1) matching when not allowing all.
	p.origins = make(map[string]struct{}, len(cfg.AllowedOrigins))
	if !p.allowAll {
		for _, o := range cfg.AllowedOrigins {
			p.origins[o] = struct{}{}
		}
	}
	return p
}

// allowed reports whether r is a CORS request from an allowed origin.
func (p *corsPolicy) allowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Not a CORS request.
		return false
	}
	if p.allowAll {
		return true
	}
	_, ok := p.origins[origin]
	return ok
}

// headers sets the response headers every answer to an allowed origin
// carries, whichever layer below writes that answer.
func (p *corsPolicy) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.allowed(r) {
			h := w.Header()
			if p.allowAll {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
				h.Add("Vary", "Origin")
			}
		}
		next.ServeHTTP(w, r)
	})
}

// preflight answers an allowed origin's OPTIONS request. It runs below
// headers, which has already set Access-Control-Allow-Origin.
func (p *corsPolicy) preflight(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions || !p.allowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Methods", p.allowMethods)
		h.Set("Access-Control-Allow-Headers", p.allowHeaders)
		h.Set("Access-Control-Max-Age", p.maxAge)
		w.WriteHeader(http.StatusNoContent)
	})
}
