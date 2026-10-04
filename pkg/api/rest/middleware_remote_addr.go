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

// remoteAddrMiddleware carries the caller's remote address into the request
// context through auth.WithRemoteAddr, the per-source fallback
// auth.CallerPrincipal uses for an SO PIN check made by a caller neither
// authenticated nor presenting a TLS-verified certificate (go-qrdb#560
// review C).
//
// It is the OUTERMOST wrapper of all -- outside even
// AwaitingAdminCredentialMiddleware's bypass to the raw router -- because
// that bypass exists precisely for the caller this fix targets: a reissue
// request reaching ceremony-initialize while the node is "awaiting admin
// credential" has no bound identity to authenticate as and skips authMW
// (and therefore RESTMiddleware's own TLS-state injection) entirely. Without
// this wrapper such a request would reach auth.CallerPrincipal with no
// remote address in context at all, falling back to "" and sharing the one
// anonymous lockout bucket with every other unauthenticated source on this
// listener -- the exact bucket-sharing go-qrdb#560 review C fixes.
func remoteAddrMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithRemoteAddr(r.Context(), r.RemoteAddr)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
