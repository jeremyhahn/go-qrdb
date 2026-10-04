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

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// awaitingAdminCredentialProbe is the abstraction the gate needs to know
// whether the node is currently "awaiting admin credential" (go-qrdb#548
// review item 3): auth.mtls_binding_policy is "required" and the bootstrap
// admin holds no live certificate binding. Defined as an interface, exactly
// like sealedWindowProbe, so tests can substitute a deterministic fake.
type awaitingAdminCredentialProbe interface {
	AwaitingAdminCredential() bool
}

// AwaitingAdminCredentialAllowlist is the set of REST entries that remain
// reachable while the node is awaiting admin credential. Derived from the
// canonical transport-neutral verb table so it cannot drift from the other
// transports.
var AwaitingAdminCredentialAllowlist = sealedwindow.RESTAwaitingAdminCredential()

// awaitingAdminCredentialResponseBody is the JSON envelope returned for a
// request outside AwaitingAdminCredentialAllowlist while the node is in that
// state.
const awaitingAdminCredentialResponseBody = `{"error":"` + sealedwindow.AwaitingAdminCredentialClosedMessage + `","code":"AWAITING_ADMIN_CREDENTIAL"}`

// AwaitingAdminCredentialMiddleware returns a middleware that, while probe
// reports awaiting, serves ONLY AwaitingAdminCredentialAllowlist, routed
// directly to raw -- bypassing every other middleware in the chain,
// including authMW, since there is no bound identity to authenticate as --
// and refuses everything else with *middleware.AwaitingAdminCredentialError
// (503). Once probe reports false, every request reaches authenticated (the
// server's normal, fully wired chain) unchanged. A nil probe is always
// false: the gate is then a pure passthrough to authenticated.
func AwaitingAdminCredentialMiddleware(probe awaitingAdminCredentialProbe, raw http.Handler) func(authenticated http.Handler) http.Handler {
	return func(authenticated http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if probe == nil || !probe.AwaitingAdminCredential() {
				authenticated.ServeHTTP(w, r)
				return
			}
			if !requestInAllowlist(r, AwaitingAdminCredentialAllowlist) {
				writeAwaitingAdminCredentialRefusal(w)
				return
			}
			raw.ServeHTTP(w, r)
		})
	}
}

// writeAwaitingAdminCredentialRefusal writes the 503 JSON envelope for a
// request the awaiting-admin-credential gate refused.
func writeAwaitingAdminCredentialRefusal(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(authmw.HTTPStatus(&authmw.AwaitingAdminCredentialError{}, http.StatusServiceUnavailable))
	_, _ = w.Write([]byte(awaitingAdminCredentialResponseBody))
}
