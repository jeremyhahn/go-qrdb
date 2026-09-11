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
	"errors"
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
//  4. Emits an audit event on both success and failure, and REFUSES the
//     request when that event cannot be written and policy is
//     audit.FailClosed — the zero value (go-qrdb#445).
//  5. Delegates to the base handler on success.
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
	verifier SOPINVerifier,
	auditLogger audit.AuditLogger,
	policy audit.FailurePolicy,
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

		// ONE emitter for all three outcomes, so the gating decision is made
		// once rather than restated at each site -- a later branch that
		// forgot to act on the returned error is how this posture was lost
		// the first time.
		emit := func(outcome string) error {
			return audit.EmitGated(ctx, auditLogger, soDualFactorComponent, policy, audit.AuditEvent{
				Actor:    identity.Subject,
				TenantID: identity.TenantID,
				Role:     role.RoleNameSO,
				Resource: strings.ToLower(r.URL.Path),
				Action:   strings.ToLower(r.Method),
				Outcome:  outcome,
				ClientIP: r.RemoteAddr,
			})
		}

		// Read the SO PIN header.
		pinStr := r.Header.Get(SOPINHeader)
		if pinStr == "" {
			if gateErr := emit(audit.OutcomeFailure + ":missing-so-pin"); gateErr != nil {
				writeGateError(w, gateErr)
				return
			}
			writeJSONError(w, http.StatusUnauthorized, "SO PIN required (X-QRDB-SO-PIN header)")
			return
		}

		// Verify the PIN.
		if err := verifier.VerifySOPIN(ctx, identity.TenantID, []byte(pinStr)); err != nil {
			// THE VERIFIER'S OWN AUDIT GATE, distinguished from a bad PIN by
			// the CONCRETE TYPE and not by the AuthFailureUnavailable class it
			// belongs to.
			//
			// auth.SOAuthenticator.VerifySOPIN now fails closed on its own
			// unrecordable outcome (go-qrdb#445) and returns *audit.GateError
			// instead of ErrSOPINInvalid. Rendering that as 401 "SO PIN
			// verification failed" would tell a security officer holding a
			// perfectly good PIN that the PIN was wrong, on a node whose audit
			// disk was full -- terminal in every client, and the exact
			// misdiagnosis the 503 mapping exists to prevent.
			//
			// Guarding on the class instead would sweep in
			// *StoreUnavailableError, which on this surface carries
			// tenant.ErrTenantSealed -- a signal the sealed-window fallbacks
			// exist to act on, not one to reinterpret here (go-qrdb#460).
			var gateErr *audit.GateError
			if errors.As(err, &gateErr) {
				writeGateError(w, gateErr)
				return
			}
			if emitErr := emit(audit.OutcomeFailure + ":invalid-so-pin"); emitErr != nil {
				writeGateError(w, emitErr)
				return
			}
			logger.Warn("SO PIN verification failed",
				slog.String("actor", identity.Subject),
				slog.String("path", r.URL.Path))
			writeJSONError(w, http.StatusUnauthorized, "SO PIN verification failed")
			return
		}

		// FAIL-CLOSED, on the operator's own lever (go-qrdb#445).
		//
		// This used to be best-effort, on the argument that the middleware
		// runs BEHIND an already-fail-closed authorization decision so the
		// sink cannot be down by the time control reaches here. That argument
		// is real but partial, and it was answered by the ruling that closed
		// #445: the authorization decision and the SO factor are TWO DISTINCT
		// ASSERTIONS about the request, and only one of them was guaranteed
		// recorded. A sink can also fail between the two -- they are separate
		// writes -- and an SO operation is the most privileged action the
		// system offers, so a second factor that passes unrecorded is exactly
		// the event the trail exists to capture.
		//
		// The objection raised at the time was that "two postures with one
		// lever is worse than one stated plainly". The answer is not to keep
		// the weaker posture but to make it ONE posture: policy is the same
		// audit.FailurePolicy the AuthMiddleware was built with, resolved once
		// from auth.audit_failure_policy. There is no second configuration
		// key.
		if gateErr := emit(audit.OutcomeSuccess); gateErr != nil {
			writeGateError(w, gateErr)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// writeGateError answers a request refused because its SO-factor audit record
// could not be written.
//
// The status comes from HTTPStatus, not from a literal, so this surface cannot
// drift from the three other transports' answer to the same class: an
// *audit.GateError is AuthFailureUnavailable, which is 503 -- a retryable
// node-side fault. The fallback is never reached for a *audit.GateError and is
// 503 rather than 401 anyway, because by the time this middleware runs the
// caller has already authenticated.
//
// The refusal is NOT itself audited. The only sink is the one that just
// failed; audit.EmitGated has already counted it on
// qrdb_audit_gate_failures_total and logged it at ERROR.
func writeGateError(w http.ResponseWriter, err error) {
	writeJSONError(w, HTTPStatus(err, http.StatusServiceUnavailable), err.Error())
}
