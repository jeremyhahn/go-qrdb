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

import "net/http"

// canonicalPathMiddleware makes every layer of the REST stack route and
// authorize on ONE path: the decoded r.URL.Path.
//
// Before it, they did not agree. The authorization registry, the sealed-window
// allowlist, the SO gate and every handler resolve r.URL.Path, which Go has
// already percent-decoded; chi v5 routes on r.URL.RawPath whenever the request
// carried an escape that decoding changes (%2F above all). One request could
// therefore be AUTHORIZED as one route and SERVED by another:
//
//   - DELETE /v1/databases/prod%2Fx was authorized as /v1/databases/prod/x (no
//     such route: passed through as outside the API surface) and routed by chi
//     to DELETE /v1/databases/{name}, whose handler read "prod" from the
//     decoded path and dropped it -- under a deny-all authorizer;
//   - a module route could be authorized under a weak permission and served by
//     a stronger one's handler;
//   - a HandlePublic route's decoded path could open a protected handler with
//     no authentication at all.
//
// Clearing RawPath makes chi route on the decoded path too, so the route that
// is authorized is the route that is served. It is safe for every handler:
// each already reads r.URL.Path, and a wildcard route (/kv/*) matches the
// decoded key exactly as before. A name that itself contains "/" was never
// addressable through a single-segment route -- the handlers split the decoded
// path -- and now simply routes as the path it decodes to.
//
// The request is copied, not mutated: middleware must not change the caller's
// *http.Request.
func canonicalPathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawPath != "" {
			u := *r.URL
			u.RawPath = ""
			r2 := r.WithContext(r.Context())
			r2.URL = &u
			r = r2
		}
		next.ServeHTTP(w, r)
	})
}
