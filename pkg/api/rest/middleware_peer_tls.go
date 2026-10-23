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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// peerTLSMiddleware carries the request's TLS connection state and v, the
// listener's chain verifier, into the request context through
// auth.WithTLSState and auth.WithPeerChainVerifier, so a handler that binds
// the caller's verified client certificate (auth.VerifiedPeerCertificate)
// resolves it on every path.
//
// It wraps the whole handler, independent of authentication, because two
// paths never reach RESTMiddleware's own binding: the boot-time sealed-window
// listener, which has no authentication, and a re-sealed node's sealed-window
// dispatch to the raw router. A share claim on either would otherwise refuse
// a certificate the client trust pool accepted without crypto/tls building a
// chain. A request with no TLS, or a nil v, leaves that part of the context
// unbound.
func peerTLSMiddleware(v auth.IssuerVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := auth.WithPeerChainVerifier(auth.WithTLSState(r.Context(), r.TLS), v)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
