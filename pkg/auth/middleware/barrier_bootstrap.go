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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// This file is the single home of the tenant-barrier BOOTSTRAP authorization
// rules — the two windows in which a dedicated-isolation tenant's own
// authenticated identity substitutes for an RBAC grant that structurally
// cannot exist yet. Every transport (REST, gRPC, gRPC-over-UDS, QUIC, MCP)
// authorizes these operations through the two methods below so the rules
// cannot drift per transport; the corresponding routes/methods/tools are
// registered as permission.IdentityGated and the RBAC check runs here, in
// the handler path, where the fallback can be evaluated.
//
// Window 1 — trust-on-first-init (AuthorizeTenantBarrierInit): a
// self-managed tenant's IAM records are encrypted BEHIND the barrier being
// initialized, so before the first successful init no role able to satisfy
// tenant_barrier:init can exist. The window closes at initialization and
// never reopens. Over the network, init is two calls: init/begin
// (BeginTenantBarrierInit) issues a nonce bound to the connection, and
// init/complete (AuthorizeTenantBarrierInitProof) demands, on top of the
// window, a signature over it and the connection's TLS exporter by the first
// admin's key (pkg/initproof). Nothing -- certificate, role or operator
// permission -- substitutes for that proof.
//
// Window 2 — the sealed window (AuthorizeTenantBarrierSealedOp): on a node
// where the tenant's barrier is sealed (or its key not yet delivered), IAM
// cannot be decrypted, so no caller can hold tenant_barrier:status/unseal —
// yet those are precisely the operations that end the state. The fallback
// is accepted only when the auth middleware itself attested, at
// authentication time, that the tenant's IAM was unreachable (the sealed
// marker stamped by finalizeIdentity); once the barrier is unsealed the
// marker is never stamped and RBAC is the only path.

// TenantBarrierStatusReader reports a tenant barrier's initialization state.
// It is satisfied by tenant.TenantServicer; the narrow interface keeps the
// middleware's dependency to the one call the trust-on-first-init rule needs.
type TenantBarrierStatusReader interface {
	TenantBarrierStatus(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error)
}

// AuthorizeTenantBarrierInit authorizes a tenant barrier initialization.
//
// An INITIALIZED barrier demands the full tenant_barrier:init RBAC check,
// exactly like every other barrier operation. A barrier that has NEVER been
// initialized additionally accepts the tenant's own authenticated identity
// without a role — trust-on-first-init. Possession of a cluster-CA-issued
// mTLS certificate carrying the tenant's identity is the same credential the
// per-tenant officer-claim endpoints (permission.IdentityGated) already
// accept during their own bootstrap handshakes. Re-initialization of an
// initialized barrier requires the RBAC grant (and is refused as
// already-initialized by the service regardless).
func (m *AuthMiddleware) AuthorizeTenantBarrierInit(ctx context.Context, tenantID string, status TenantBarrierStatusReader) error {
	rbacErr := m.AuthorizeTenant(ctx, tenantID, permission.TenantBarrierInit)
	if rbacErr == nil {
		return nil
	}

	// AN AUDIT FAILURE IS NOT AN RBAC VERDICT, so it must not open the
	// bootstrap window.
	//
	// AuthorizeTenant can now return *audit.GateError instead of a decision
	// (go-qrdb#335), and every test below treats a non-nil rbacErr as "RBAC
	// said no" -- so the fallback ran on a request RBAC had never actually
	// refused. It cost a TenantBarrierStatus call, then a SECOND
	// auditDecision: another write to the sink that had just failed, which is
	// the retry this feature forbids, and a second
	// qrdb_auth_audit_write_failures_total{outcome="denied"} increment for ONE
	// decision. That undercuts the counter's stated meaning -- a non-zero rate
	// is meant to be the exact size of the gap in the trail, not a multiple of
	// it.
	//
	// Returning immediately is also the right ANSWER: the caller is refused
	// either way, and *audit.GateError carries "retry", which the RBAC denial it
	// would otherwise be flattened into does not.
	//
	// NARROWLY the AuthFailureAuditUnavailable class, not AuthFailureUnavailable.
	// The sealed-tenant signal (*StoreUnavailableError wrapping
	// tenant.ErrTenantSealed) is exactly what these fallbacks EXIST to act on
	// -- a tenant whose IAM is encrypted behind the barrier it is trying to
	// unseal -- and go-qrdb#460 split it into its own class precisely so this
	// check cannot drift back onto the wide one that used to contain both.
	//
	// The same holds for an attested-caller refusal (go-qrdb#278): RBAC
	// GRANTED that request and the peer's measured state refused it, so it is
	// the opposite of the denial this window substitutes for. Treating it as
	// one would let an unattested peer through the gate by way of the
	// bootstrap window.
	//
	// A RATE-LIMIT REFUSAL IS NOT AN RBAC VERDICT EITHER. The limiter runs
	// before RBAC is consulted, so a *RateLimitedError says nothing about
	// whether the caller holds the grant -- and treating it as a denial let
	// the fallback below issue the grant anyway, which made the policy on
	// this permission unenforceable for exactly the callers the window
	// admits. The limiter's answer is final.
	//
	// NOR IS A TENANT-STATUS REFUSAL. decide refuses a suspended or disabled
	// tenant's identity before RBAC is consulted, so an embedded caller
	// holding a context authenticated while the tenant was active would
	// otherwise be granted init or unseal of a suspended tenant's barrier
	// here. Nor is a tenant status that could not be READ
	// (*TenantStatusUnavailableError): the tenant was never found active.
	// isGateRefusal is the one predicate for all of them.
	if isGateRefusal(rbacErr) {
		return rbacErr
	}

	// The bootstrap windows exist for PER-TENANT barriers only: the system
	// tenant has no tenant barrier, and the tenant-scoped RBAC rule
	// (ScopeTenantOnly) denies it deliberately — the fallback must never
	// reopen that door.
	if tenantid.IsSystem(tenantID) {
		return rbacErr
	}
	caller := auth.IdentityFromContext(ctx)
	if caller == nil || caller.TenantID != tenantID || status == nil {
		return rbacErr
	}
	// A share-holder certificate proves custody of a quorum share, not an
	// operator identity entitled to initialize a tenant's IAM barrier.
	// IsHolderIdentity callers are never enriched from the user store and
	// so will never hold tenant_barrier:init; admitting one here would let
	// a custodian or Security Officer stand up a tenant's entire identity
	// store through a window meant for the tenant's own first operator.
	if auth.IsHolderIdentity(caller) {
		return rbacErr
	}
	st, err := status.TenantBarrierStatus(ctx, tenantID)
	if err != nil || st == nil || st.Initialized {
		return rbacErr
	}
	// Trust on first init is not trust in ANY certificate of the tenant: a
	// tenant onboarded with its first admin admits only that admin's
	// certificate, and under the "required" binding posture a tenant with no
	// onboarded admin admits nobody.
	if err := m.checkFirstInitCertificate(ctx, caller, tenantID); err != nil {
		return err
	}
	// The RBAC denial above was already audited; record the final decision
	// too so the audit trail shows the request was ultimately allowed.
	//
	// PROPAGATED, not discarded: this line is the bootstrap window issuing a
	// GRANT that RBAC refused, which is the single most consequential
	// authorization decision on this surface. If it cannot be recorded, the
	// configured audit failure policy decides, exactly as it does for every
	// other grant (see auditDecision).
	return m.grantBootstrapFallback(ctx, caller, permission.TenantBarrierInit)
}

// TenantBarrierInitProof is the proof of possession a tenant barrier
// init/complete carries (pkg/initproof): the nonce init/begin issued on this
// connection, the DER SubjectPublicKeyInfo of the signing key, and its
// signature over the tenant id, the nonce and this connection's TLS exporter.
// Every field is public; the private key never leaves the caller's signer.
type TenantBarrierInitProof struct {
	Nonce     []byte
	PublicKey []byte
	Signature []byte
}

// WithInitChallengeBook replaces the book of tenant barrier init challenges
// New creates on the real clock. Tests use it to put the book on a fake
// clock; nothing else needs to.
func WithInitChallengeBook(b *initproof.Book) Option {
	return func(m *AuthMiddleware) { m.initChallenges = b }
}

// BeginTenantBarrierInit is init/begin: it admits the caller exactly as
// AuthorizeTenantBarrierInit does -- the pending first admin's certificate
// authenticates it -- and issues a single-use nonce bound to this
// connection's TLS exporter, which init/complete must sign.
//
// A connection with no TLS 1.3 exporter is refused with
// *tenant.FirstAdminProofError: no complete could ever succeed on it. A node
// holding initproof.DefaultMaxOutstanding unexpired challenges refuses with
// *initproof.TooManyChallengesError until some lapse.
func (m *AuthMiddleware) BeginTenantBarrierInit(ctx context.Context, tenantID string, status TenantBarrierStatusReader) (*initproof.Challenge, error) {
	if err := m.AuthorizeTenantBarrierInit(ctx, tenantID, status); err != nil {
		return nil, err
	}
	return m.initVerifier().Begin(tenantID, auth.TLSStateFromContext(ctx))
}

// initVerifier is the proof verifier over this node's book of init
// challenges.
func (m *AuthMiddleware) initVerifier() *TenantBarrierInitVerifier {
	return &TenantBarrierInitVerifier{book: m.initChallenges}
}

// AuthorizeTenantBarrierInitProof is init/complete's gate: the caller must
// pass AuthorizeTenantBarrierInit AND present proof of possession of the
// first admin's key over this connection. No certificate, role or operator
// permission substitutes for the proof -- an RBAC grant admits the caller to
// the gate, never past the proof.
//
// The key the proof must be by is the one the tenant's pending first admin
// records. A tenant with no pending admin -- its admin was bound at create,
// or the record was consumed by an earlier init -- is held to the key of the
// certificate on this connection, so the proof still binds the request to
// the authenticated key and to this connection.
//
// Every refusal of the proof itself is *tenant.FirstAdminProofError; the
// nonce is spent by the attempt whatever its outcome.
func (m *AuthMiddleware) AuthorizeTenantBarrierInitProof(ctx context.Context, tenantID string, status TenantBarrierStatusReader, proof *TenantBarrierInitProof) error {
	if err := m.AuthorizeTenantBarrierInit(ctx, tenantID, status); err != nil {
		return err
	}
	return m.verifyTenantBarrierInitProof(ctx, tenantID, proof)
}

// verifyTenantBarrierInitProof checks proof against this connection and the
// first admin's key (TenantBarrierInitVerifier.Complete).
func (m *AuthMiddleware) verifyTenantBarrierInitProof(ctx context.Context, tenantID string, proof *TenantBarrierInitProof) error {
	return m.initVerifier().Complete(tenantID, auth.TLSStateFromContext(ctx), proof, func() (string, error) {
		return m.initProofThumbprint(ctx, tenantID)
	})
}

// initProofThumbprint returns the SPKI thumbprint the init proof must be by:
// the pending first admin's, the pinned first admin's once that record is
// consumed, or with neither, the certificate's on this connection. Empty
// means there is no key the proof could be by. A failure to read either
// record fails closed.
func (m *AuthMiddleware) initProofThumbprint(ctx context.Context, tenantID string) (string, error) {
	pending, pinned, err := m.firstInitAdmin(ctx, tenantID)
	switch {
	case err != nil:
		return "", err
	case pending != nil:
		return pending.Thumbprint, nil
	case pinned != "":
		return pinned, nil
	}
	return presentedThumbprint(ctx), nil
}

// firstInitAdmin reads who may initialize tenantID's barrier on a node where
// it is not initialized yet: the pending first admin while the tenant's
// identity records are still to be written, and the thumbprint pinned for the
// barrier when its owner was onboarded. The pending record is consumed when
// the admin's account is created, which can happen before every node serving
// the barrier is initialized -- a node that was down initializes later -- so
// the pin, written once and never consumed, is what the window falls back to.
func (m *AuthMiddleware) firstInitAdmin(ctx context.Context, tenantID string) (*tenantadmin.Pending, string, error) {
	if m.pendingAdmins != nil {
		pending, err := m.pendingAdmins.Get(ctx, tenantID)
		if err != nil {
			return nil, "", &StoreUnavailableError{Err: err}
		}
		if pending != nil {
			return pending, "", nil
		}
	}
	if m.firstAdminPins == nil {
		return nil, "", nil
	}
	pinned, err := m.firstAdminPins.TenantFirstAdmin(ctx, tenantID)
	if err != nil {
		return nil, "", &StoreUnavailableError{Err: err}
	}
	return nil, pinned, nil
}

// PendingTenantAdmins reads the first admin a tenant was onboarded with
// before its identity records could be written (pkg/tenantadmin).
// *tenantadmin.KVStore implements it.
type PendingTenantAdmins interface {
	Get(ctx context.Context, tenantID string) (*tenantadmin.Pending, error)
}

// WithPendingTenantAdmins gives the trust-on-first-init window of a tenant
// barrier the record of the admin each tenant was onboarded with, so the
// window admits that admin's certificate and no other.
func WithPendingTenantAdmins(p PendingTenantAdmins) Option {
	return func(m *AuthMiddleware) { m.pendingAdmins = p }
}

// FirstAdminPins reads the SPKI thumbprint of the first admin each tenant's
// barrier was pinned to when its owner was onboarded; empty when none is.
// *lifecycle.BarrierInit implements it.
type FirstAdminPins interface {
	TenantFirstAdmin(ctx context.Context, tenantID string) (string, error)
}

// WithFirstAdminPins gives the trust-on-first-init window of a tenant barrier
// the pinned first admin, which it admits once the pending record is
// consumed: a node serving the barrier that initializes after the admin's
// account exists still admits that admin's certificate and no other.
func WithFirstAdminPins(p FirstAdminPins) Option {
	return func(m *AuthMiddleware) { m.firstAdminPins = p }
}

// checkFirstInitCertificate decides whether the certificate on this request
// may initialize tenantID's never-initialized barrier through the
// trust-on-first-init window.
//
//   - A tenant onboarded before its barrier existed has a pending admin
//     record: only a certificate whose SPKI is the onboarded one, naming the
//     onboarded admin, may initialize -- whoever initializes the barrier is
//     the admin the platform operator onboarded.
//   - Once that record is consumed -- the admin's account exists, while a
//     node serving the barrier is still to initialize -- only a certificate
//     whose SPKI is the one pinned for the barrier at onboarding may.
//   - With neither, the "enrolled" posture keeps the window as it was: any
//     authenticated identity of the tenant.
//   - With no record under "required", nobody may: a certificate bound to no
//     account cannot initialize a tenant barrier under a posture that admits
//     no unbound certificate anywhere else.
//
// A failure to read the record fails closed.
func (m *AuthMiddleware) checkFirstInitCertificate(ctx context.Context, caller *auth.Identity, tenantID string) error {
	pending, pinned, err := m.firstInitAdmin(ctx, tenantID)
	if err != nil {
		return err
	}
	presented := presentedThumbprint(ctx)
	switch {
	case pending != nil:
		if presented == "" || presented != pending.Thumbprint || caller.AccountName() != pending.Username {
			return &FirstInitCertificateMismatchError{
				TenantID:            tenantID,
				Username:            pending.Username,
				PresentedThumbprint: presented,
			}
		}
	case pinned != "":
		if presented == "" || presented != pinned {
			return &FirstInitCertificateMismatchError{TenantID: tenantID, PresentedThumbprint: presented}
		}
	case m.bindingPolicy == auth.MTLSBindingRequired:
		return &FirstInitNotOnboardedError{TenantID: tenantID}
	}
	return nil
}

// AuthorizeTenantBarrierSealedOp authorizes the tenant barrier operations
// that must remain reachable while the tenant's IAM itself is unreachable:
// status and unseal (perm is TenantBarrierStatus or TenantBarrierUnseal).
// The full RBAC check runs first and wins whenever it can be evaluated. The
// fallback accepts the tenant's OWN authenticated identity only when the
// middleware stamped it as authenticated during a tenant-IAM outage
// (IdentityIAMSealed): the tenant's roles are encrypted behind the sealed
// (or not-yet-delivered) barrier, so no caller could hold the RBAC grant.
func (m *AuthMiddleware) AuthorizeTenantBarrierSealedOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	rbacErr := m.AuthorizeTenant(ctx, tenantID, perm)
	if rbacErr == nil {
		return nil
	}

	// Not an RBAC verdict, so not a reason to open the sealed window — see
	// AuthorizeTenantBarrierInit for why this precedes every other check, and
	// why it is AuthFailureAuditUnavailable specifically rather than
	// AuthFailureUnavailable (which carries the sealed-tenant signal this
	// fallback acts on). An attested-caller refusal is not one either, nor is
	// a rate-limit refusal: the sealed window must not grant past the limiter,
	// nor past a tenant status it could not read.
	if isGateRefusal(rbacErr) {
		return rbacErr
	}

	// Never for the system tenant — see AuthorizeTenantBarrierInit.
	if tenantid.IsSystem(tenantID) {
		return rbacErr
	}
	caller := auth.IdentityFromContext(ctx)
	if caller == nil || caller.TenantID != tenantID || !IdentityIAMSealed(caller) {
		return rbacErr
	}
	// See AuthorizeTenantBarrierInit: a share-holder identity never opens
	// either bootstrap window.
	if auth.IsHolderIdentity(caller) {
		return rbacErr
	}
	// The RBAC denial above was already audited; record the final decision
	// too so the audit trail shows the request was ultimately allowed. It is
	// propagated for the same reason as in AuthorizeTenantBarrierInit: this is
	// a grant RBAC refused.
	return m.grantBootstrapFallback(ctx, caller, perm)
}

// grantBootstrapFallback is the one way either bootstrap window GRANTS.
//
// A window replaces the RBAC decision that structurally cannot be made; it
// does not replace the attested-caller gate (go-qrdb#278), which is a second
// condition on admission and never depended on RBAC. On the sealed-window
// path the gate had not even run -- authorize() answers a sealed identity's
// request with the sealed-tenant refusal before reaching it -- so a grant
// issued here without it would admit an unattested peer to a gated
// operation. The gate therefore runs on every fallback grant, and its
// refusal is recorded as the decision's outcome exactly as authorize()
// records it.
func (m *AuthMiddleware) grantBootstrapFallback(ctx context.Context, caller *auth.Identity, perm permission.Permission) error {
	gerr := m.checkPeerAttestation(ctx, caller, perm)
	if auditErr := m.auditDecision(ctx, caller, perm, gerr); auditErr != nil {
		return auditErr
	}
	return gerr
}

// isAuditFailure reports whether err is the middleware refusing because it
// could not RECORD a decision, as opposed to a decision it actually made.
//
// The distinction matters wherever a non-nil error is read as "RBAC said no".
// Both bootstrap fallbacks did exactly that, and an *audit.GateError sent them
// down a path meant for a genuine denial. so_middleware.go's SO-PIN gate has
// the identical hazard and uses this same predicate for the same reason.
//
// THE NARROW AuthFailureAuditUnavailable CLASS, never the wider
// AuthFailureUnavailable it used to be part of. The sealed-tenant signal
// (*StoreUnavailableError wrapping tenant.ErrTenantSealed) is
// AuthFailureUnavailable, not this class, and it is the exact signal these
// fallbacks exist to act on. Before go-qrdb#460 split the class, this function
// was the only thing standing between a future caller and testing the wide
// class directly -- a single, easy, silent regression. Now the type itself
// enforces the distinction: ClassifyAuthError cannot return
// AuthFailureAuditUnavailable for a *StoreUnavailableError, so this function
// (or any other code that tests the class directly) is correct by
// construction rather than by vigilance.
func isAuditFailure(err error) bool {
	return ClassifyAuthError(err) == AuthFailureAuditUnavailable
}

// isRateLimited reports whether err is the middleware's rate limiter refusing
// the request. Like an audit failure it is decided before, and independently
// of, RBAC, so neither bootstrap fallback may read it as a denial it can
// override.
func isRateLimited(err error) bool {
	var rl *RateLimitedError
	return errors.As(err, &rl)
}

// isGateRefusal reports whether err is a refusal that is NOT an RBAC verdict:
// the middleware could not record the decision (isAuditFailure), the caller
// is over its rate limit (isRateLimited: authorize returns a
// *RateLimitedError before RBAC is consulted at all), or the attested-caller
// gate refused a request RBAC had granted, or the caller's tenant is
// suspended or disabled (*TenantNotActiveError: decide checks the tenant's
// status before RBAC, at every decision, so a context authenticated while the
// tenant was active is refused once it is not). None of them may be read as
// "RBAC said no", which is the only thing the bootstrap fallbacks exist to act
// on -- a window that opened on a rate-limit refusal would let a throttled
// caller straight through the throttle, and one that opened on a tenant-status
// refusal would initialize or unseal a suspended tenant's barrier.
//
// A status that could not be READ is one too (*TenantStatusUnavailableError,
// whatever it wraps): the tenant was never found active, so a window opened on
// it would grant without the check the status gate exists to make. Only the
// tenant-IAM-sealed signal -- a *StoreUnavailableError wrapping
// tenant.ErrTenantSealed, which decide returns for an identity marked sealed,
// and which is NOT wrapped in *TenantStatusUnavailableError -- is left for
// the fallbacks to act on.
func isGateRefusal(err error) bool {
	if isAuditFailure(err) || isRateLimited(err) {
		return true
	}
	var refused *PeerAttestationRefusedError
	var notActive *TenantNotActiveError
	var unreadable *TenantStatusUnavailableError
	return errors.As(err, &refused) || errors.As(err, &notActive) || errors.As(err, &unreadable)
}
