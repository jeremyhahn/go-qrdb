// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

const (
	// SOPINHeader is the HTTP request header carrying the in-band SO PIN.
	// This header is ONLY accepted over TLS 1.3; requests over plain HTTP
	// are rejected before the header is read.
	SOPINHeader = "X-QRDB-SO-PIN"
)

// SOPermissions is the set of permissions that require SO dual-factor
// authentication (mTLS identity + in-band PIN). Any route mapped to one
// of these permissions will be gated by SOMiddleware.
var SOPermissions = map[permission.Permission]struct{}{
	permission.CeremonyInitialize:   {},
	permission.CeremonySignCSR:      {},
	permission.SealRotateRootKey:    {},
	permission.SealRekey:            {},
	permission.SealGenerateRecovery: {},
	permission.SealInitializeShamir: {},
}

// SOPINVerifier is the interface consumed by SOMiddleware to verify the SO PIN.
// It is satisfied by *auth.SOAuthenticator.
type SOPINVerifier interface {
	// VerifySOPIN returns nil when the presentedPIN matches the committed hash.
	VerifySOPIN(ctx context.Context, tenantID string, presentedPIN []byte) error
}

// SOMiddleware is the REST face of SOGate, the one SO dual-factor gate every
// transport shares. It returns an http.Handler that enforces SO dual-factor
// authentication for SO-permissioned routes. The required permission for
// the request's route is resolved from the RESTRouteRegistry the REST
// server populates at route-registration time. It wraps the provided
// base handler and:
//
//  1. Passes through every route that is not mapped to an SO-only
//     permission (SOPermissions) — behavior outside the SO surface is
//     unchanged.
//  2. Rejects SO-gated requests over plain HTTP (no TLS state) with 401
//     so the PIN is never read off an unencrypted connection.
//  3. Requires the identity to hold the SO role, resolved by identifier
//     against roles, and the X-QRDB-SO-PIN header to be present and valid.
//  4. Emits an audit event on both success and failure, and REFUSES the
//     request when that event cannot be written and policy is
//     audit.FailClosed — the zero value (go-qrdb#445).
//  5. Delegates to the base handler on success.
//
// roles is the role store the SO role is resolved against: the caller must
// hold, in its own tenant, the identifier under which that store persists the
// SO role (role.HoldsSystemRole). A nil roles refuses every SO-gated request
// with 503 rather than guessing.
//
// The identity's TenantID is used to look up the stored PIN commit.
// If the identity has no tenant (cross-tenant/system identity), the
// SO PIN check is applied to the empty-string tenant key — callers
// should ensure SetSOPIN is called with the appropriate tenantID.
//
// policy is the operator's auth.audit_failure_policy, the SAME value the
// AuthMiddleware one layer out was built with. It is a PARAMETER rather than a
// package-level global because this is a free function and a global posture is
// a posture a second embedder in the same process can silently change under
// the first. Its zero value is audit.FailClosed, so a caller that has not been
// updated to pass one gets the secure posture; that is why the parameter sits
// beside auditLogger rather than at the end, where a variadic option would
// have made "nobody passed it" and "somebody chose fail-closed" the same
// shape.
func SOMiddleware(
	routes *RESTRouteRegistry,
	roles role.NameResolver,
	verifier SOPINVerifier,
	auditLogger audit.AuditLogger,
	policy audit.FailurePolicy,
	logger *slog.Logger,
	next http.Handler,
) http.Handler {
	gate := NewSOGate(roles, verifier, auditLogger, policy, logger)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Determine the required permission for this route. A route that is
		// not permission-mapped, or whose permission is outside
		// SOPermissions, is not SO-gated: the gate passes it through, so the
		// middleware never changes behavior outside the SO surface.
		perm, kind, hasPerm := routes.Lookup(r.Method, r.URL.Path)
		if !hasPerm || kind != RESTRoutePermission {
			next.ServeHTTP(w, r)
			return
		}

		factor := SOFactorFromHTTPRequest(r)
		defer factor.Destroy()
		ctx := WithSOFactor(r.Context(), factor)

		if err := gate.Enforce(ctx, perm, r.URL.Path, r.Method); err != nil {
			setRetryAfterHeader(w, err)
			WriteJSONError(w, HTTPStatus(err, http.StatusServiceUnavailable), err.Error())
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SOFactorFromHTTPRequest builds the SO factor an HTTP request presents, for
// the REST middleware and the MCP endpoint alike. The SOPINHeader is read
// only when the request arrived over TLS: a PIN sent in plaintext is never
// held, and the gate refuses the request as insecure. The caller destroys the
// factor (SOFactor.Destroy, or WipeSOFactor once it is bound).
func SOFactorFromHTTPRequest(r *http.Request) SOFactor {
	isSecure := r.TLS != nil
	var pin []byte
	if isSecure {
		if h := r.Header.Get(SOPINHeader); h != "" {
			pin = []byte(h)
		}
	}
	return NewSOFactor(r.Context(), pin, isSecure, r.RemoteAddr)
}
