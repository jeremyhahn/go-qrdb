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
	"strings"
)

// sealedWindowProbe is the abstraction the middleware needs to know
// whether the storage barrier is currently sealed. Implemented by
// *seal.StorageBarrier in production; defined as an interface so
// tests can substitute a deterministic fake without depending on the
// full barrier construction machinery.
type sealedWindowProbe interface {
	IsSealed() bool
}

// SealedWindowAllowlist is the canonical set of REST paths that
// remain operative while the barrier is sealed. Each entry is
// either an exact path match or a prefix (entries ending in "/" are
// matched as prefixes). The allowlist is exposed as a package-level
// var so callers can compose with project-specific extensions if
// they ever need to, but the canonical list is the one initialized
// here.
var SealedWindowAllowlist = []string{
	"/v1/health",
	"/v1/barrier/status",
	"/v1/barrier/initialize",
	"/v1/barrier/unseal",
	"/v1/barrier/shamir/initialize",
	"/v1/barrier/shamir/unseal-share",
	"/v1/barrier/shamir/unseal-shares",
	"/v1/barrier/shamir/verify",
	"/v1/barrier/recovery/generate",
	"/v1/barrier/recovery/recover",
	"/v1/barrier/recovery/status",
	"/v1/init/",
	"/v1/bootstrap/",
}

// sealedResponseBody is the JSON envelope returned to clients while
// the barrier is sealed and they hit a non-allowlist endpoint.
const sealedResponseBody = `{"error":"barrier sealed","code":"SEALED"}`

// SealedWindowMiddleware returns an http middleware that 503s every
// request not on the allowlist while the supplied barrier is
// sealed. Once the barrier is unsealed the middleware becomes a
// passthrough; on re-seal it engages again. The same middleware
// instance is safe to keep installed across the lifetime of the
// process.
//
// allowlist may be nil, in which case SealedWindowAllowlist is used.
// Path matching: entries ending in "/" match by prefix; everything
// else matches exactly.
//
// The middleware sets a Retry-After: 5 header on rejected responses
// so well-behaved clients back off during the unseal window.
func SealedWindowMiddleware(barrier sealedWindowProbe, allowlist []string) func(http.Handler) http.Handler {
	if allowlist == nil {
		allowlist = SealedWindowAllowlist
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if barrier == nil || !barrier.IsSealed() {
				next.ServeHTTP(w, r)
				return
			}
			if pathInAllowlist(r.URL.Path, allowlist) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(sealedResponseBody))
		})
	}
}

// pathInAllowlist returns true if path matches an entry in the
// allowlist. Entries ending in "/" are treated as prefix matches;
// all others match exactly.
func pathInAllowlist(path string, allowlist []string) bool {
	for _, entry := range allowlist {
		if strings.HasSuffix(entry, "/") {
			if strings.HasPrefix(path, entry) {
				return true
			}
			continue
		}
		if path == entry {
			return true
		}
	}
	return false
}
