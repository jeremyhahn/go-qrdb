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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// SOPINMetadataKey is the gRPC metadata key (and the Unix socket's, which
// serves the same gRPC wire) that carries the in-band SO PIN. It is
// SOPINHeader in the lower case gRPC metadata requires; MCP, served over
// HTTP, reads SOPINHeader itself.
const SOPINMetadataKey = "x-qrdb-so-pin"

// SOFactor is the second factor presented with one request, bound into the
// request context by the transport that received it (WithSOFactor) and read
// by SOGate.Enforce. A transport that binds none presents an insecure,
// PIN-less factor, which the gate refuses: forgetting to bind it fails closed.
// Build one with NewSOFactor, which seals the PIN into protected memory.
type SOFactor struct {
	// PIN is the SO PIN the caller presented, sealed, or nil when none was
	// presented or the request did not arrive over TLS. Destroy destroys it;
	// both the gate and the binding transport call that.
	PIN *secure.Secret
	// Secure reports whether the request arrived over TLS. The PIN is never
	// consulted on a request that did not.
	Secure bool
	// ClientIP is the caller's address, for the audit record.
	ClientIP string
	// sealErr is why a presented PIN could not be sealed. The gate refuses
	// an SO-gated request carrying one as unavailable rather than as PIN-less:
	// the officer presented a PIN, and the node could not hold it.
	sealErr error
}

// NewSOFactor builds the factor for one request. pin is consumed: it is
// wiped on every path, and sealed into PIN only when the request arrived over
// TLS (isSecure) and pin is not empty -- a PIN sent in plaintext is never
// held. A failure to seal is recorded in the factor, and the gate refuses an
// SO-gated request carrying it with *SOPINUnavailableError; a request the
// gate does not cover is unaffected.
func NewSOFactor(ctx context.Context, pin []byte, isSecure bool, clientIP string) SOFactor {
	f := SOFactor{Secure: isSecure, ClientIP: clientIP}
	if !isSecure || len(pin) == 0 {
		secure.Wipe(pin)
		return f
	}
	// NewSecret wipes pin on every path, including failure.
	f.PIN, f.sealErr = secure.NewSecret(ctx, pin)
	return f
}

// Destroy destroys the factor's sealed PIN. It is idempotent, so the binding
// transport and the gate may both call it.
func (f SOFactor) Destroy() {
	f.PIN.Destroy()
}

type soFactorKey struct{}

// WithSOFactor binds the second factor presented with a request into ctx.
func WithSOFactor(ctx context.Context, f SOFactor) context.Context {
	return context.WithValue(ctx, soFactorKey{}, f)
}

// WipeSOFactor destroys the PIN of the factor bound into ctx by WithSOFactor.
// A transport defers it where it binds the factor, so the PIN is destroyed
// even on a request the gate never sees -- one whose permission is not
// SO-gated. Enforce destroys it too; destroying twice is harmless.
func WipeSOFactor(ctx context.Context) {
	soFactorFromContext(ctx).Destroy()
}

// soFactorFromContext returns the factor bound by WithSOFactor, or the zero
// factor (insecure, no PIN) when none was bound.
func soFactorFromContext(ctx context.Context) SOFactor {
	f, _ := ctx.Value(soFactorKey{}).(SOFactor)
	return f
}

// SOGate is the SO dual-factor gate, ONE implementation shared by every
// transport: REST (SOMiddleware), gRPC and Unix (SOGateUnaryInterceptor,
// SOGateStreamInterceptor), QUIC and MCP (Enforce, called after their own
// authorization step). For a permission in SOPermissions it requires, in
// order:
//
//  1. a request that arrived over TLS, so the PIN is never read off a
//     plaintext connection;
//  2. an authenticated identity holding the SO role, recognised by the
//     identifier its own tenant's role store persists the role under
//     (role.HoldsSystemRole) -- never by name (go-qrdb#569);
//  3. a present and valid SO PIN, verified by the SOPINVerifier.
//
// The PIN outcome -- missing, invalid or accepted -- is audited through
// audit.EmitGated under the operator's audit failure policy, and a record
// that cannot be written refuses the request under audit.FailClosed
// (go-qrdb#445). Every refusal is typed, so each transport maps it through
// ClassifyAuthError exactly as it maps its other authentication failures.
type SOGate struct {
	roles    role.NameResolver
	verifier SOPINVerifier
	audit    audit.AuditLogger
	policy   audit.FailurePolicy
	logger   *slog.Logger
}

// NewSOGate builds the gate. roles is the role store the SO role is resolved
// against; a nil roles refuses every SO-gated request as unavailable rather
// than guessing. verifier checks the PIN and must not be nil. policy is the
// operator's auth.audit_failure_policy, the same value the AuthMiddleware was
// built with. A nil logger discards.
func NewSOGate(roles role.NameResolver, verifier SOPINVerifier, auditLogger audit.AuditLogger, policy audit.FailurePolicy, logger *slog.Logger) *SOGate {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &SOGate{roles: roles, verifier: verifier, audit: auditLogger, policy: policy, logger: logger}
}

// Enforce applies the gate to a request needing perm. It returns nil for a
// permission outside SOPermissions, and for one inside it only when all three
// requirements hold. resource and action name the operation in the audit
// record. The identity and the factor are read from ctx.
func (g *SOGate) Enforce(ctx context.Context, perm permission.Permission, resource, action string) error {
	if _, requiresSO := SOPermissions[perm]; !requiresSO {
		return nil
	}
	factor := soFactorFromContext(ctx)
	// The server's sealed PIN is destroyed once the gate has decided,
	// admitted or refused, on every path out of here.
	defer factor.Destroy()
	if !factor.Secure {
		return &SOFactorError{Reason: SOReasonInsecureTransport}
	}

	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return &SORoleRequiredError{}
	}
	isSO, err := role.HoldsSystemRole(ctx, g.roles, identity.TenantID, identity.Roles, role.RoleNameSO)
	if err != nil {
		g.logger.Error("SO role could not be resolved",
			slog.String("actor", identity.Subject),
			slog.String("resource", resource),
			slog.String("error", err.Error()))
		return &SORoleUnresolvedError{Err: err}
	}
	if !isSO {
		return &SORoleRequiredError{}
	}

	// ONE emitter for all three outcomes, so the gating decision is made once
	// rather than restated at each site.
	emit := func(outcome string) error {
		return audit.EmitGated(ctx, g.audit, soDualFactorComponent, g.policy, audit.AuditEvent{
			Actor:    identity.Subject,
			TenantID: identity.TenantID,
			Role:     role.RoleNameSO,
			Resource: strings.ToLower(resource),
			Action:   strings.ToLower(action),
			Outcome:  outcome,
			ClientIP: factor.ClientIP,
		})
	}

	if factor.sealErr != nil {
		return &SOPINUnavailableError{Err: factor.sealErr}
	}
	if factor.PIN == nil {
		if gateErr := emit(audit.OutcomeFailure + ":missing-so-pin"); gateErr != nil {
			return gateErr
		}
		return &SOFactorError{Reason: SOReasonPINRequired}
	}

	// A PIN the node cannot open is a node-side fault, refused before any
	// verification outcome is audited: the officer's PIN was never judged.
	var verifyErr error
	if err := withSOPIN(ctx, factor.PIN, func(pin []byte) error {
		verifyErr = g.verifier.VerifySOPIN(ctx, identity.TenantID, pin)
		return nil
	}); err != nil {
		return err
	}
	if err := verifyErr; err != nil {
		// The verifier's OWN audit gate (auth.SOAuthenticator fails closed on
		// its unrecordable outcome, go-qrdb#445) is a node-side fault, not a
		// bad PIN: it is returned as is, so it maps to 503 / Unavailable and
		// an officer holding a good PIN is never told to re-enrol. See
		// isAuditFailure for why the narrow class is the test.
		if isAuditFailure(err) {
			return err
		}
		// A lockout is not a wrong PIN: the PIN was never compared, and the
		// officer must be told to wait (429, with the delay), not to fix a
		// PIN that may be right.
		var locked *auth.SOPINLockedError
		if errors.As(err, &locked) {
			return err
		}
		if emitErr := emit(audit.OutcomeFailure + ":invalid-so-pin"); emitErr != nil {
			return emitErr
		}
		g.logger.Warn("SO PIN verification failed",
			slog.String("actor", identity.Subject),
			slog.String("resource", resource))
		return &SOFactorError{Reason: SOReasonPINInvalid}
	}

	// The accepted factor is recorded or the request does not proceed
	// (go-qrdb#445): the authorization decision and the SO factor are two
	// distinct assertions, and an SO operation is the most privileged action
	// the system offers.
	return emit(audit.OutcomeSuccess)
}

// openSealedPIN opens the sealed PIN. A variable so a test can record the
// address of the buffer the PIN is opened into, and prove the verifier is
// handed those locked pages rather than a copy of them (go-qrdb#145).
var openSealedPIN = (*secure.Secret).Open

// withSOPIN opens the sealed PIN and calls fn with its plaintext read in
// place: the borrow on the opened buffer is held across fn, so the verifier
// is handed the PIN's locked pages, never a heap copy (go-qrdb#145). fn may
// use protected memory of its own -- every buffer has its own lock -- and may
// overwrite pin, which is destroyed when fn returns; it must not retain it.
// A PIN that cannot be opened, or opens empty, is *SOPINUnavailableError;
// fn's own error is returned as is.
func withSOPIN(ctx context.Context, sealed *secure.Secret, fn func(pin []byte) error) error {
	buf, err := openSealedPIN(sealed, ctx)
	if err != nil {
		return &SOPINUnavailableError{Err: err}
	}
	defer buf.Destroy()
	var fnErr error
	if err := buf.WithBytes(func(pin []byte) error {
		if len(pin) == 0 {
			return &secure.EmptyBorrowError{}
		}
		fnErr = fn(pin)
		return nil
	}); err != nil {
		return &SOPINUnavailableError{Err: err}
	}
	return fnErr
}

// SOFactorReason is why the SO second factor was refused.
type SOFactorReason string

// The reasons an SO factor is refused. Their text is the refusal message
// every transport returns.
const (
	// SOReasonInsecureTransport: the request did not arrive over TLS.
	SOReasonInsecureTransport SOFactorReason = "SO PIN requires TLS 1.3"
	// SOReasonPINRequired: no SO PIN was presented.
	SOReasonPINRequired SOFactorReason = "SO PIN required"
	// SOReasonPINInvalid: the presented SO PIN did not verify.
	SOReasonPINInvalid SOFactorReason = "SO PIN verification failed"
)

// SOFactorError is the SO gate's refusal of the second factor itself. The
// caller has not proved it is the officer, so it classifies as
// AuthFailureUnauthenticated: 401 / Unauthenticated.
type SOFactorError struct {
	// Reason is why the factor was refused.
	Reason SOFactorReason
}

// Error returns the refusal message.
func (e *SOFactorError) Error() string { return string(e.Reason) }

// SORoleRequiredError is the SO gate's refusal of a caller that does not hold
// the SO role. The credential is good and the role is absent, so it
// classifies as AuthFailureUnauthorized: 403 / PermissionDenied.
type SORoleRequiredError struct{}

// Error returns the refusal message.
func (e *SORoleRequiredError) Error() string { return "SO role required" }

// SORoleConflictError is the strict separation-of-duties refusal of an
// account holding the Security Officer role together with its tenant's Admin
// role: a role write that would leave an account holding both, or an
// SO-exclusive act by a caller that holds both (go-qrdb#1128). The credential
// is good and the combination is forbidden, so it classifies as
// AuthFailureUnauthorized: 403 / PermissionDenied.
type SORoleConflictError struct {
	// TenantID is the tenant whose SO and Admin roles were combined.
	TenantID string
}

// Error returns the refusal message.
func (e *SORoleConflictError) Error() string {
	return "separation of duties: the SO role cannot be held with the admin role of tenant " + e.TenantID
}

// SORoleUnresolvedError is the SO gate's refusal when the role store could
// not say whether the caller holds the SO role. Neither the credential nor
// the role failed, so it classifies as AuthFailureUnavailable: 503 /
// Unavailable, retryable.
type SORoleUnresolvedError struct {
	// Err is the role store's failure.
	Err error
}

// Error returns the refusal message with its cause.
func (e *SORoleUnresolvedError) Error() string {
	return "SO role could not be resolved: " + e.Err.Error()
}

// Unwrap returns the role store's failure.
func (e *SORoleUnresolvedError) Unwrap() error { return e.Err }

// SOPINUnavailableError is the SO gate's refusal when the node could not hold
// or read the presented PIN in protected memory -- sealing it, or opening it
// for the verifier, failed. The officer is not at fault, so it classifies as
// AuthFailureUnavailable: 503 / Unavailable, retryable.
type SOPINUnavailableError struct {
	// Err is the protected-memory failure.
	Err error
}

// Error returns the refusal message with its cause.
func (e *SOPINUnavailableError) Error() string {
	return "SO PIN could not be held in protected memory: " + e.Err.Error()
}

// Unwrap returns the protected-memory failure.
func (e *SOPINUnavailableError) Unwrap() error { return e.Err }
