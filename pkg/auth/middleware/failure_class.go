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
	"net/http"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// AuthFailureClass is WHAT KIND of failure an authentication or authorization
// error represents, independent of any transport.
//
// It exists because the answer was being spelled out once per transport and
// then, on the tenant-barrier surface, not at all: eleven REST call sites and
// seven gRPC ones hard-coded "permission denied" for every error the
// authorization gate could return. That was survivable while
// *StoreUnavailableError almost never escaped those gates -- and stopped being
// survivable with go-qrdb#335, because *audit.GateError escapes them RELIABLY (the
// bootstrap-window fallback's own audit write fails for the same reason the
// first one did). An operator recovering a node with a full audit disk would
// have been told "you are not permitted" by the unseal endpoint.
//
// The middleware owns WHAT KIND OF FAILURE THIS IS. Each transport owns HOW IT
// SAYS THAT on its own wire. Splitting it the other way is what let four
// transports drift.
type AuthFailureClass uint8

const (
	// AuthFailureUnknown is an error this package does not recognise. Every
	// mapper takes the caller's own fallback for it rather than guessing,
	// because the right guess differs by call site: an authentication
	// middleware that cannot classify a failure means "unauthenticated",
	// while an authorization gate running AFTER authentication means
	// "unauthorized". Collapsing those was the one way this refactor could
	// have introduced a regression.
	AuthFailureUnknown AuthFailureClass = iota

	// AuthFailureUnauthenticated -- the credential is missing, malformed,
	// expired or revoked. Present a credential. 401 / Unauthenticated.
	AuthFailureUnauthenticated

	// AuthFailureUnauthorized -- the credential is good and the grant is
	// absent. Retrying is futile. 403 / PermissionDenied.
	AuthFailureUnauthorized

	// AuthFailureUnavailable -- NEITHER the credential nor the grant failed.
	// This node could not answer: it could not read the user store
	// (*StoreUnavailableError -- which includes a sealed tenant barrier,
	// tenant.ErrTenantSealed), could not read revocation state
	// (*auth.RevocationCheckError), or could not resolve which tenant a
	// revocation targets (*CertSerialUnresolvableError). RETRY IS THE HONEST
	// INSTRUCTION. 503 / Unavailable.
	//
	// It does NOT cover a failure to RECORD a decision -- see
	// AuthFailureAuditUnavailable, its sibling below. The two used to be one
	// class, and every one of the transport statuses is identical between
	// them; they are split because control-flow code -- "was this refusal an
	// audit-sink outage, or the sealed-tenant signal a bootstrap fallback
	// exists to act on?" -- could otherwise test the wide class and get the
	// wrong answer for whichever member it did not mean (go-qrdb#460). A
	// transport mapping the two identically is correct and intended; a
	// control-flow branch conflating them is the bug this split exists to
	// make impossible.
	AuthFailureUnavailable

	// AuthFailureRateLimited -- credential and grant are both fine and the
	// caller is over its quota. Back off and retry.
	// 429 / ResourceExhausted.
	AuthFailureRateLimited

	// AuthFailureAuditUnavailable -- the decision itself could not be
	// RECORDED (*audit.GateError): the credential may be good, the grant may
	// be good, or the SO PIN may be good, and this node refused anyway
	// because it could not write the outcome to its audit sink. Every
	// transport answers it exactly as AuthFailureUnavailable -- 503 /
	// Unavailable, retryable -- so a caller deciding HOW TO ANSWER THE WIRE
	// never needs to tell the two apart.
	//
	// A caller deciding CONTROL FLOW does. The tenant-barrier bootstrap
	// fallbacks (barrier_bootstrap.go) and the SO-PIN gate (so_middleware.go)
	// must refuse outright on THIS class -- an unrecorded decision is not a
	// verdict the fallback may act on -- while a sealed tenant
	// (AuthFailureUnavailable) is precisely the signal those fallbacks exist
	// to admit. Before this class existed both cases were
	// AuthFailureUnavailable, and the only thing keeping a control-flow
	// branch from conflating them was a comment and a test at each call site
	// (isAuditFailure); now the type itself says which is which, and a
	// caller can safely switch on the class. See docs/audit/engineering.md
	// ("The SO path specifically") for the worked example, and
	// go-qrdb#335/#445 for why an audit-sink outage produces an error here
	// at all.
	AuthFailureAuditUnavailable
)

// String returns a short name for logs and test failure messages.
func (c AuthFailureClass) String() string {
	switch c {
	case AuthFailureUnauthenticated:
		return "unauthenticated"
	case AuthFailureUnauthorized:
		return "unauthorized"
	case AuthFailureUnavailable:
		return "unavailable"
	case AuthFailureRateLimited:
		return "rate-limited"
	case AuthFailureAuditUnavailable:
		return "audit-unavailable"
	default:
		return "unknown"
	}
}

// Retryable reports whether a caller should try the same request again.
//
// It is the question a transport with no status code -- MCP, whose tool
// results carry text -- still has to answer, so that its message does not tell
// an operator "authorization denied" about a full audit disk.
func (c AuthFailureClass) Retryable() bool {
	return c == AuthFailureUnavailable || c == AuthFailureRateLimited || c == AuthFailureAuditUnavailable
}

// transientMarker is the capability role.StoreError and tenant.StoreError
// both implement: the PRODUCER -- the code that actually saw the underlying
// store failure -- declares whether it is worth retrying, at the point the
// error is constructed (role.newStoreError, tenant.newStoreError).
//
// This replaces matching on Op/Detail strings: those name WHERE a failure
// happened for a log message, not WHAT KIND it is, and nothing tied the two
// together. A producer renaming "primary lookup" to something else left a
// string set matching nothing and silently reopened a false-denial class --
// a mutation that survived the whole suite. Checking a method the producer
// implements instead means a renamed label cannot desync from the
// classification it used to carry, because there is no longer a second copy
// of the label to desync from.
//
// The method is named ReadTransient, not the shorter Transient, because
// *service.FactorMigrationRefusedError already has its own Transient() bool
// answering an unrelated question ("does a later migration run still have
// work to do"); errors.As below matches on the METHOD SET, so that type
// would otherwise satisfy this interface by accident and have its retry
// signal misread as "safe to retry now".
type transientMarker interface {
	ReadTransient() bool
}

// resolveErrorIsTransient reports whether cause -- the error a
// *rbac.ResolveError wraps -- is one this package recognises as a transient
// failure of the role or tenant store, safe to report as retryable (go-qrdb
// strict-isolation phase 0 review findings X1, BL1 and BL2).
//
// Deliberately an ALLOW-list:
//
//   - context.DeadlineExceeded/Canceled is the primary's own bounded resolve
//     (resolveSyncTimeout) timing out, or the caller's outer context ending.
//   - tenant.ErrTenantSealed / seal.ErrSealed: a barrier-isolated tenant's
//     role store resolved to a sealed barrier (routingRoleService.storeFor ->
//     resolver.Resolve), retryable once the tenant is unsealed -- the same
//     condition pkg/api/errclass's dominant rules give the same class for
//     every other data-plane read.
//   - seal.ErrBarrierSealed: go-quicraft's sentinel for the same read on a
//     node barrier re-sealed at runtime, retryable once it is unsealed.
//   - anything in the cause chain implementing transientMarker, asked
//     directly rather than matched by type or label: role.StoreError and
//     tenant.StoreError both decide at construction (not here) whether the
//     read that produced them is worth retrying -- "unmarshal" (corrupt
//     stored data) and a sealed-barrier-backed store's decrypt/authentication
//     refusal (seal.ErrDecryptionFailed) are never transient, because no
//     retry un-corrupts a record or decrypts a tampered ciphertext.
//
// *role.RoleNotFoundError (the key is simply absent, including the
// cross-tenant case kvRoleStore.GetRole's ownership check answers with) and
// *role.ValidationError (a malformed request) are different types entirely
// that implement no such method, so errors.As below never matches them. See
// the "rbac.ResolveError" rule below for why this is an allow-list rather
// than naming every definitive type instead.
func resolveErrorIsTransient(cause error) bool {
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, context.Canceled) {
		return true
	}
	if errors.Is(cause, tenant.ErrTenantSealed) || errors.Is(cause, seal.ErrSealed) ||
		errors.Is(cause, seal.ErrBarrierSealed) {
		return true
	}
	var marker transientMarker
	if errors.As(cause, &marker) {
		return marker.ReadTransient()
	}
	return false
}

// authFailureRules is the ORDERED classification chain, first-match-wins.
// Below the audit.GateError and PeerAttestationRefusedError rules (placed at
// the front for reasons of their own, see their comments), the remaining
// order reproduces, exactly, the sequence
// mapHTTPAuthError and mapGRPCAuthError each used to spell out separately --
// so neither changes behaviour for any error either already recognised.
//
// A slice rather than a map keyed by type: errors.As has to run against a
// typed target, and the chain is first-match-wins, which a map cannot express.
// Adding a class here reaches all four transports at once, which is the point.
var authFailureRules = []struct {
	name  string
	match func(error) (AuthFailureClass, bool)
}{
	// FIRST, ahead of auth.AuthError and StoreUnavailableError, and
	// deliberately NOT in the order mapHTTPAuthError/mapGRPCAuthError used to
	// spell out (that history is why every OTHER rule below keeps its
	// relative position).
	//
	// errors.As walks err's ENTIRE unwrap chain, not just its outermost
	// layer, and *audit.GateError.Err is the audit SINK's own failure -- a
	// value this package does not control, because the sink is pluggable.
	// If a sink implementation's write error happens to be, or itself wrap,
	// an *auth.AuthError or *StoreUnavailableError, checking those rules
	// first would find that INNER, coincidental match and classify the
	// failure as a credential or store problem -- when the fact that
	// actually matters, and is true regardless of what the sink's error
	// looks like, is that this node could not RECORD the decision at all.
	// isAuditFailure (barrier_bootstrap.go) and the SO-PIN gate
	// (so_middleware.go) both depend on AuthFailureAuditUnavailable being
	// exactly this reliable: checking it last would leave both fallbacks
	// exposed to a sink whose error type this package cannot constrain.
	//
	// The credential is valid and the grant -- or the SO PIN -- may be too.
	// ONE rule covers every gate, because go-qrdb#445 made them one type; a
	// second error type for the SO factor would have meant a second rule
	// here and a second mapping in each of the four transports.
	//
	// AuthFailureAuditUnavailable, NOT AuthFailureUnavailable -- see the two
	// classes' doc comments (go-qrdb#460). Every transport maps both to the
	// same status, so this line changes no transport's answer; it changes
	// what a control-flow branch can safely test.
	{"audit.GateError", func(err error) (AuthFailureClass, bool) {
		var e *audit.GateError
		return AuthFailureAuditUnavailable, errors.As(err, &e)
	}},
	// The peer is authenticated and authorized; its measured state is not
	// acceptable, so the answer is "forbidden". The one exception is a
	// verifier that could not decide: that is a statement about this node,
	// not the peer, and retrying may succeed. It is still a refusal.
	//
	// This rule sits directly below the audit rule and ABOVE every rule that
	// could match the wrapped Err: a verifier error is the verifier's own
	// type -- an injected implementation this package does not control -- and
	// errors.As walks the whole chain, so a later position would classify the
	// refusal by whatever that type happens to be.
	{"PeerAttestationRefusedError", func(err error) (AuthFailureClass, bool) {
		var e *PeerAttestationRefusedError
		if !errors.As(err, &e) {
			return AuthFailureUnknown, false
		}
		if e.Reason == PeerAttestationVerifierError {
			return AuthFailureUnavailable, true
		}
		return AuthFailureUnauthorized, true
	}},
	// The SO gates' five refusals (so_gate.go), ahead of the
	// generic rules so a role store error wrapped by SORoleUnresolvedError is
	// classified by what the gate concluded, not by whatever that error
	// happens to wrap.
	{"SOFactorError", func(err error) (AuthFailureClass, bool) {
		var e *SOFactorError
		return AuthFailureUnauthenticated, errors.As(err, &e)
	}},
	{"SORoleRequiredError", func(err error) (AuthFailureClass, bool) {
		var e *SORoleRequiredError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	{"SORoleConflictError", func(err error) (AuthFailureClass, bool) {
		var e *SORoleConflictError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	{"SORoleUnresolvedError", func(err error) (AuthFailureClass, bool) {
		var e *SORoleUnresolvedError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	{"SOPINUnavailableError", func(err error) (AuthFailureClass, bool) {
		var e *SOPINUnavailableError
		if !errors.As(err, &e) {
			return AuthFailureUnknown, false
		}
		// go-enclave v0.7.0-alpha: a context that ends while the PIN is
		// being sealed or opened fails with an error matching BOTH the
		// enclave sentinel and ctx.Err(). That is the caller giving up, not
		// a node-side fault — decline here so errclass's own contextRules
		// (which run after every family block) report it as a cancellation
		// or a deadline instead of "unavailable".
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return AuthFailureUnknown, false
		}
		return AuthFailureUnavailable, true
	}},
	// The chain was refused while this node could not read all of the trust
	// set it verifies against, so the refusal says nothing about the
	// certificate: retry. Ahead of auth.AuthError for the reason the
	// attestation rule is ahead of everything: the wrapped refusal is an
	// injected IssuerVerifier's own error, and errors.As walks the chain.
	{"auth.TrustStateUnavailableError", func(err error) (AuthFailureClass, bool) {
		var e *auth.TrustStateUnavailableError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	// The read behind an authenticator met its shard electing a leader or
	// moving it (service.LeadershipTransient): the credential was never
	// judged, and the next attempt, after the election, can succeed. Ahead of
	// auth.AuthError because an election is never a credential failure: a
	// chain carrying an *auth.AuthError beside the election -- an
	// authenticator joining its verdict with the read that failed under it --
	// would otherwise answer 401 and send a valid caller to re-authenticate
	// (go-qrdb#1280). Below the audit and attestation rules for the reasons
	// their comments give.
	{"service.LeadershipTransient", func(err error) (AuthFailureClass, bool) {
		return AuthFailureUnavailable, service.LeadershipTransient(err)
	}},
	{"auth.AuthError", func(err error) (AuthFailureClass, bool) {
		var authErr *auth.AuthError
		if !errors.As(err, &authErr) {
			return AuthFailureUnknown, false
		}
		switch authErr.Code {
		case "unauthorized":
			return AuthFailureUnauthorized, true
		case "unauthenticated", "no_peer_cert", "invalid_cert", "cert_revoked":
			return AuthFailureUnauthenticated, true
		}
		// An AuthError carrying some other code falls THROUGH to the rules
		// below, exactly as it did before. It is not classified here.
		return AuthFailureUnknown, false
	}},
	{"StoreUnavailableError", func(err error) (AuthFailureClass, bool) {
		var e *StoreUnavailableError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	// The role store could not decide the identity's TenantOwner; retryable.
	{"TenantOwnerUnresolvedError", func(err error) (AuthFailureClass, bool) {
		var e *TenantOwnerUnresolvedError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	// The user lookup met a sealed node barrier; retryable once unsealed.
	{"BarrierSealedError", func(err error) (AuthFailureClass, bool) {
		var e *BarrierSealedError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	// The RBAC authorizer's role-resolution failure
	// (rbac.resolvePermissionSet): the primary (linearizable) role store
	// failed or timed out, and either no StaleRead fallback is configured or
	// -- go-qrdb strict-isolation phase 0 review finding B1 -- the caller's
	// tenant is not the system tenant the fallback is confined to, so it was
	// never consulted. That is not a verdict about the identity's roles: the
	// node could not resolve them, and the caller gets the retryable 503 that
	// describes, exactly as the user-lookup fallback's *StoreUnavailableError
	// does above.
	//
	// This is an ALLOW-list (resolveErrorIsTransient), not an exclude-list --
	// review finding X1. *rbac.ResolveError wraps whatever the primary
	// returned VERBATIM, and that includes DEFINITIVE answers: a deleted (or
	// never-existent) role is *role.RoleNotFoundError, a malformed request is
	// *role.ValidationError, and a resolver contract violation is
	// *role.InvalidResolverResultError. None of those is retryable -- retrying
	// a RoleNotFoundError never finds the role, and a ValidationError's
	// request never becomes valid -- so classifying every ResolveError as
	// Unavailable turned a deleted role (401/403 on dev/main) into a
	// PERMANENT retryable 503 on every request its holders made. An
	// exclude-list would need to name every definitive type this package will
	// ever meet; an allow-list instead names only what IS safe to retry, so a
	// future definitive error type this rule does not yet recognise falls
	// through unclassified (the caller's own fallback) rather than silently
	// becoming retryable.
	{"rbac.ResolveError", func(err error) (AuthFailureClass, bool) {
		var re *rbac.ResolveError
		if !errors.As(err, &re) {
			return AuthFailureUnknown, false
		}
		if !resolveErrorIsTransient(re.Err) {
			return AuthFailureUnknown, false
		}
		return AuthFailureUnavailable, true
	}},
	// The certificate may be perfectly good and this node could not read
	// revocation state. Telling a legitimate caller its credential is bad
	// would send it to re-enrol; telling it to retry is the truth.
	{"auth.RevocationCheckError", func(err error) (AuthFailureClass, bool) {
		var e *auth.RevocationCheckError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	// Likewise the tenant the certificate names: this node could not read
	// its record, which says nothing about whether it exists.
	{"auth.TenantLookupError", func(err error) (AuthFailureClass, bool) {
		var e *auth.TenantLookupError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	{"UserDisabledError", func(err error) (AuthFailureClass, bool) {
		var e *UserDisabledError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	// The same answer as a refused account: the credential is valid, the
	// principal's tenant is out of service.
	{"TenantNotActiveError", func(err error) (AuthFailureClass, bool) {
		var e *TenantNotActiveError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	// The credential is valid and the account is fine -- this WAY in is not
	// one the account may use, and retrying it will never work.
	{"AuthMethodNotPermittedError", func(err error) (AuthFailureClass, bool) {
		var e *AuthMethodNotPermittedError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	// The certificate is not this account's credential at all, so the caller
	// never established who it is: 401, not 403. Contrast the rule immediately
	// above, where the credential IS the account's -- there, retrying is
	// futile and the answer is "forbidden"; here, presenting the certificate
	// the account actually enrolled works.
	{"CertBindingNotPermittedError", func(err error) (AuthFailureClass, bool) {
		var e *CertBindingNotPermittedError
		return AuthFailureUnauthenticated, errors.As(err, &e)
	}},
	// Same reasoning as the rule above: under the "required" binding posture
	// an account with no enrolled key cannot establish who it is by
	// certificate, and enrolling the presented key is what makes it work.
	{"CertBindingRequiredError", func(err error) (AuthFailureClass, bool) {
		var e *CertBindingRequiredError
		return AuthFailureUnauthenticated, errors.As(err, &e)
	}},
	// A tenant barrier's trust-on-first-init window reached with the wrong
	// certificate, or under "required" for a tenant with no onboarded admin.
	// The caller DID establish who it is -- the window is only reached with
	// a verified certificate -- and that identity may not initialize this
	// barrier: 403, and retrying with the same certificate never works.
	{"FirstInitCertificateMismatchError", func(err error) (AuthFailureClass, bool) {
		var e *FirstInitCertificateMismatchError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	{"FirstInitNotOnboardedError", func(err error) (AuthFailureClass, bool) {
		var e *FirstInitNotOnboardedError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	// A tenant barrier init completed without proof of possession of the
	// first admin's key over this connection: the caller has not proved who
	// it is, and a fresh nonce from init/begin is the remedy -- 401, not 403.
	{"FirstAdminProofError", func(err error) (AuthFailureClass, bool) {
		var e *tenant.FirstAdminProofError
		return AuthFailureUnauthenticated, errors.As(err, &e)
	}},
	// The node holds its limit of unexpired init challenges; one lapsing is
	// what lets the next begin through, so the caller backs off and retries.
	{"TooManyChallengesError", func(err error) (AuthFailureClass, bool) {
		var e *initproof.TooManyChallengesError
		return AuthFailureRateLimited, errors.As(err, &e)
	}},
	{"AuthChainError", func(err error) (AuthFailureClass, bool) {
		var e *AuthChainError
		return AuthFailureUnauthenticated, errors.As(err, &e)
	}},
	// The node could not establish WHICH tenant a revocation targets, so it
	// could not decide. 503, not 403: the caller's grant was never consulted,
	// and answering "forbidden" would send an entitled operator to fix a
	// permission that is not the problem.
	{"CertSerialUnresolvableError", func(err error) (AuthFailureClass, bool) {
		var e *CertSerialUnresolvableError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	{"RateLimitedError", func(err error) (AuthFailureClass, bool) {
		var e *RateLimitedError
		return AuthFailureRateLimited, errors.As(err, &e)
	}},
	// A tenant SO PIN locked out after repeated failures: the officer may be
	// entitled and their PIN right, and must wait the backoff out.
	{"SOPINLockedError", func(err error) (AuthFailureClass, bool) {
		var e *auth.SOPINLockedError
		return AuthFailureRateLimited, errors.As(err, &e)
	}},
	// The "awaiting admin credential" gate's refusal (go-qrdb#548 review item
	// 3): the node is up and serving the two verbs that boot state allows,
	// but this request names neither. Neither the credential nor a grant
	// failed -- there IS no bound credential yet -- so this is the same
	// shape as AuthFailureUnavailable's other members: retry is the honest
	// instruction, and it becomes true the moment an admin certificate
	// binds.
	{"AwaitingAdminCredentialError", func(err error) (AuthFailureClass, bool) {
		var e *AwaitingAdminCredentialError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	// A holder certificate reaching an authenticated-only route it has no
	// business on (go-qrdb#560 review): the credential is valid and the
	// caller IS authenticated, but this way in is not one a holder
	// certificate may use, exactly like AuthMethodNotPermittedError above.
	{"HolderIdentityNotPermittedError", func(err error) (AuthFailureClass, bool) {
		var e *HolderIdentityNotPermittedError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
}

// ClassifyAuthError reports what kind of failure err represents, or
// AuthFailureUnknown when this package does not recognise it.
//
// A nil error is AuthFailureUnknown: there is no failure to classify, and a
// caller that reaches a mapper with nil has a bug this function must not
// paper over by inventing a success class.
func ClassifyAuthError(err error) AuthFailureClass {
	if err == nil {
		return AuthFailureUnknown
	}
	for _, rule := range authFailureRules {
		if class, ok := rule.match(err); ok {
			return class
		}
	}
	return AuthFailureUnknown
}

// httpStatusByClass maps a classified failure onto its HTTP status.
var httpStatusByClass = map[AuthFailureClass]int{
	AuthFailureUnauthenticated:  http.StatusUnauthorized,
	AuthFailureUnauthorized:     http.StatusForbidden,
	AuthFailureUnavailable:      http.StatusServiceUnavailable,
	AuthFailureRateLimited:      http.StatusTooManyRequests,
	AuthFailureAuditUnavailable: http.StatusServiceUnavailable,
}

// HTTPStatus returns the HTTP status err means, or fallback when this package
// does not recognise it.
//
// THE FALLBACK IS THE CALLER'S TO CHOOSE, and the two callers choose
// differently on purpose:
//
//   - The REST auth middleware passes http.StatusUnauthorized. It runs
//     authentication as well as authorization, so an unrecognised failure
//     there most likely means the caller never established who it was.
//   - A handler-level authorization gate -- the tenant-barrier surface --
//     passes http.StatusForbidden. Authentication has already succeeded by
//     the time those run, so 401 would be a lie and a regression on the
//     status those sites answered before this function existed.
//
// Hard-coding one default for both is what would have turned this fix into a
// silent behaviour change on every unrecognised error.
func HTTPStatus(err error, fallback int) int {
	if code, ok := httpStatusByClass[ClassifyAuthError(err)]; ok {
		return code
	}
	return fallback
}

// grpcCodeByClass maps a classified failure onto its gRPC code.
//
// ResourceExhausted for the rate-limited class, not PermissionDenied: it is
// the standard signal a gRPC client library maps to "back off and retry"
// rather than "re-authenticate" or "do not retry at all".
var grpcCodeByClass = map[AuthFailureClass]codes.Code{
	AuthFailureUnauthenticated:  codes.Unauthenticated,
	AuthFailureUnauthorized:     codes.PermissionDenied,
	AuthFailureUnavailable:      codes.Unavailable,
	AuthFailureRateLimited:      codes.ResourceExhausted,
	AuthFailureAuditUnavailable: codes.Unavailable,
}

// GRPCError converts err to a gRPC status error, using fallback for anything
// this package does not recognise. See HTTPStatus for why the fallback is the
// caller's.
func GRPCError(err error, fallback codes.Code) error {
	if err == nil {
		return nil
	}
	code, ok := grpcCodeByClass[ClassifyAuthError(err)]
	if !ok {
		code = fallback
	}
	st := status.New(code, err.Error())
	if delay, named := RetryAfter(err); named {
		if detailed, derr := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(delay)}); derr == nil {
			st = detailed
		}
	}
	return st.Err()
}
