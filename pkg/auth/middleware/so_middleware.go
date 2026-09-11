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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
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
	permission.CeremonyInitialize:      {},
	permission.CeremonyEnrollCustodian: {},
	permission.CeremonyRevokeCustodian: {},
	permission.CeremonySignCSR:         {},
	permission.SealRotateRootKey:       {},
	permission.SealRekey:               {},
	permission.SealGenerateRecovery:    {},
	permission.SealInitializeShamir:    {},
}

// SOPINVerifier is the interface consumed by SOMiddleware to verify the SO PIN.
// It is satisfied by *auth.SOAuthenticator.
type SOPINVerifier interface {
	// VerifySOPIN returns nil when the presentedPIN matches the committed hash.
	VerifySOPIN(ctx context.Context, tenantID string, presentedPIN []byte) error
}

// SOMiddleware returns an http.Handler that enforces SO dual-factor
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
//  3. Requires the X-QRDB-SO-PIN header to be present and valid.
//  4. Emits an audit event on both success and failure.
//  5. Delegates to the base handler on success.
//
// The identity's TenantID is used to look up the stored PIN commit.
// If the identity has no tenant (cross-tenant/system identity), the
// SO PIN check is applied to the empty-string tenant key — callers
// should ensure SetSOPIN is called with the appropriate tenantID.
func SOMiddleware(
	routes *RESTRouteRegistry,
	verifier SOPINVerifier,
	auditLogger audit.AuditLogger,
	logger *slog.Logger,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Determine the required permission for this route.
		perm, kind, hasPerm := routes.Lookup(r.Method, r.URL.Path)
		if !hasPerm || kind != RESTRoutePermission {
			// Not a permission-mapped route; no SO gating needed.
			next.ServeHTTP(w, r)
			return
		}

		// Check whether this permission requires SO dual-factor.
		if _, requiresSO := SOPermissions[perm]; !requiresSO {
			next.ServeHTTP(w, r)
			return
		}

		// SO PIN must only be transmitted over TLS. Reject plain HTTP
		// requests to SO-gated routes before reading the header to
		// prevent credential exposure. Non-SO routes are unaffected so
		// the middleware never changes behavior outside SOPermissions.
		if r.TLS == nil {
			writeJSONError(w, http.StatusUnauthorized, "SO PIN requires TLS 1.3")
			return
		}

		// Extract the identity from context (set by the upstream RESTMiddleware).
		identity := auth.IdentityFromContext(ctx)
		if identity == nil || !identity.HasRole(role.RoleNameSO) {
			writeJSONError(w, http.StatusForbidden, "SO role required")
			return
		}

		// Read the SO PIN header.
		pinStr := r.Header.Get(SOPINHeader)
		if pinStr == "" {
			if auditLogger != nil {
				audit.EmitBestEffort(ctx, auditLogger, "so-dual-factor", audit.AuditEvent{
					Actor:    identity.Subject,
					TenantID: identity.TenantID,
					Role:     role.RoleNameSO,
					Resource: strings.ToLower(r.URL.Path),
					Action:   strings.ToLower(r.Method),
					Outcome:  audit.OutcomeFailure + ":missing-so-pin",
					ClientIP: r.RemoteAddr,
				})
			}
			writeJSONError(w, http.StatusUnauthorized, "SO PIN required (X-QRDB-SO-PIN header)")
			return
		}

		// Verify the PIN.
		if err := verifier.VerifySOPIN(ctx, identity.TenantID, []byte(pinStr)); err != nil {
			if auditLogger != nil {
				audit.EmitBestEffort(ctx, auditLogger, "so-dual-factor", audit.AuditEvent{
					Actor:    identity.Subject,
					TenantID: identity.TenantID,
					Role:     role.RoleNameSO,
					Resource: strings.ToLower(r.URL.Path),
					Action:   strings.ToLower(r.Method),
					Outcome:  audit.OutcomeFailure + ":invalid-so-pin",
					ClientIP: r.RemoteAddr,
				})
			}
			logger.Warn("SO PIN verification failed",
				slog.String("actor", identity.Subject),
				slog.String("path", r.URL.Path))
			writeJSONError(w, http.StatusUnauthorized, "SO PIN verification failed")
			return
		}

		// BEST-EFFORT, and deliberately NOT fail-closed -- a stated
		// asymmetry with AuthMiddleware.authorize, which refuses a decision
		// it could not record.
		//
		// The reason is that this middleware runs BEHIND RESTMiddleware, so
		// the request already carries an authorization decision that was
		// audited under the fail-closed posture: if the sink is down, the
		// request never reaches here. What this record adds is the SECOND
		// factor's outcome, and making it fail closed would introduce a
		// refusal path the operator's auth.audit_failure_policy lever does
		// not reach (SOMiddleware is a free function wired from
		// pkg/api/rest, with no access to the middleware's posture). A
		// security control with two postures and one lever is worse than one
		// posture stated plainly. Extending the lever here is a separate
		// change and a separate decision.
		if auditLogger != nil {
			audit.EmitBestEffort(ctx, auditLogger, "so-dual-factor", audit.AuditEvent{
				Actor:    identity.Subject,
				TenantID: identity.TenantID,
				Role:     role.RoleNameSO,
				Resource: strings.ToLower(r.URL.Path),
				Action:   strings.ToLower(r.Method),
				Outcome:  audit.OutcomeSuccess,
				ClientIP: r.RemoteAddr,
			})
		}

		next.ServeHTTP(w, r)
	})
}
