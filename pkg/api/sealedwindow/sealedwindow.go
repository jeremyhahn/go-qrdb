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

// Package sealedwindow defines the canonical, transport-neutral set of
// operations that remain reachable while the storage barrier is sealed.
//
// Every protocol transport (REST, gRPC, native QUIC, MCP) derives its
// sealed-window allowlist from the single Table in this package, so the
// per-transport lists can never drift apart again. A verb is present in
// Table when at least one transport exposes the operation; a transport
// whose Names entry is empty simply has no surface for that verb.
//
// Deliberate exclusions (never allowlisted on any transport):
//
//   - barrier seal and Shamir seal-vote — sealing is only meaningful
//     while the barrier is unsealed (see docs/seal/sealed-window.md).
//   - barrier enable-encryption — the migration requires an unsealed
//     data plane.
//   - Shamir share management (list/delete shares) — administrative
//     operations served by the regular post-unseal listener.
//   - custodian enroll/revoke — SO-gated governance operations served
//     post-unseal.
//   - officer list, recovery-key generate / delete / status, and rekey.
//     Every verb that reaches the boot-time sealed-window listener does so
//     WITHOUT authentication, because that listener has no identity plane
//     to authenticate against. Rekey and the three recovery-key verbs
//     refuse while the barrier is sealed (seal.ErrSealed), so allowlisting
//     them bought nothing but an anonymous destructive surface. Officer
//     list is served post-unseal to authenticated callers.
//
// Every entry in Table is therefore an ANONYMOUS verb on that listener and
// must be justified as one: liveness, credential possession (unseal,
// recovery-recover, root-token), first-boot trust-on-first-use
// (initialize, officer registration), or a handshake whose binding
// authorization the handler enforces (the share and certificate claim
// flows, and ceremony-sign-csr's SO PIN).
package sealedwindow

import (
	"context"
	"strings"
	"sync"
)

// Verb is a transport-neutral identifier for one sealed-window operation.
type Verb string

// Canonical sealed-window verbs. The set is the union of every operation
// any transport historically allowlisted, minus the deliberate exclusions
// documented in the package comment.
const (
	VerbHealth Verb = "health"
	VerbStatus Verb = "status"

	VerbBarrierStatus     Verb = "barrier-status"
	VerbBarrierInitialize Verb = "barrier-initialize"
	VerbBarrierUnseal     Verb = "barrier-unseal"

	VerbShamirInitialize   Verb = "shamir-initialize"
	VerbShamirUnsealShare  Verb = "shamir-unseal-share"
	VerbShamirUnsealShares Verb = "shamir-unseal-shares"

	VerbRecoveryRecover Verb = "recovery-recover"

	VerbRootToken Verb = "root-token"

	VerbOfficerRegister    Verb = "officer-register"
	VerbOfficerClaimStatus Verb = "officer-claim-status"
	VerbShareClaimBegin    Verb = "share-claim-begin"
	VerbShareClaimComplete Verb = "share-claim-complete"

	VerbRecoveryOfficerRegister    Verb = "recovery-officer-register"
	VerbRecoveryShareClaimBegin    Verb = "recovery-share-claim-begin"
	VerbRecoveryShareClaimComplete Verb = "recovery-share-claim-complete"

	VerbCeremonyInitialize           Verb = "ceremony-initialize"
	VerbCeremonyStatus               Verb = "ceremony-status"
	VerbCeremonyClaimCertBegin       Verb = "ceremony-claim-cert-begin"
	VerbCeremonyClaimCertComplete    Verb = "ceremony-claim-cert-complete"
	VerbCeremonyOfficerShareBegin    Verb = "ceremony-officer-share-begin"
	VerbCeremonyOfficerShareComplete Verb = "ceremony-officer-share-complete"
	VerbCeremonySignCSR              Verb = "ceremony-sign-csr"
	VerbCeremonyClaimShareBegin      Verb = "ceremony-claim-share-begin"
	VerbCeremonyClaimShareEnd        Verb = "ceremony-claim-share-complete"
	VerbCeremonySubmitUnsealShare    Verb = "ceremony-submit-unseal-share"
)

// Names holds the per-transport wire names for one canonical verb. An
// empty field means the verb has no surface on that transport.
type Names struct {
	// REST entries: a path, or a method-qualified "METHOD /path". A path
	// ending in "/" is a prefix match; anything else matches exactly. An
	// unqualified entry matches every method (see RESTEntryMatches).
	REST []string
	// GRPC is the method tail — the suffix after the last "/" in
	// grpc.UnaryServerInfo.FullMethod.
	GRPC string
	// QUIC is the native QUIC method name (e.g. "barrier.Unseal").
	QUIC string
	// MCP is the MCP tool name (e.g. "qrdb_barrier_unseal").
	MCP string
	// PreOperationalOnly marks a verb whose sealed-window AUTHENTICATION
	// exemption additionally requires the init ceremony to still be
	// pre-operational. It is the state-gate axis of this table, mirroring
	// go-quicbaas/pkg/api/sealedwindow's field of the same name so the two
	// canonical tables diff mechanically.
	//
	// It is set for the officer registration verbs: RegisterOfficer
	// (pkg/ceremony/ceremony_officer.go) CA-signs any caller-supplied CSR
	// and assigns a Shamir share index with no service-layer
	// authentication, so an exemption that survives ceremony completion
	// would let an unauthenticated caller enroll an officer during any
	// restart-while-sealed. Officer enrollment belongs to the enrollment
	// ceremony; once the ceremony is operational the exemption must lapse.
	//
	// The barrier / unseal verbs are deliberately NOT marked: a daemon
	// restarted after a completed ceremony is operational AND sealed, and
	// unseal must remain reachable without a token because a sealed IAM
	// plane cannot validate one.
	PreOperationalOnly bool

	// Transition marks a verb that can move the barrier out of the sealed
	// state -- initialize it or unseal it. On the boot-time sealed-window
	// listener such a request runs alone: it waits for every anonymous
	// request already in flight, and none starts while it runs, so no request
	// admitted under seal is still running when the barrier unseals (see
	// Admission).
	Transition bool
}

// PreOperationalOnly reports whether verb v's sealed-window authentication
// exemption is gated on a pre-operational ceremony. Consumers deriving an
// authentication bypass from this table MUST drop the verb from any
// state-independent exemption set and fail closed when the ceremony state
// cannot be determined.
func PreOperationalOnly(v Verb) bool {
	return Table[v].PreOperationalOnly
}

// Verbs is the canonical verb list in stable derivation order.
var Verbs = []Verb{
	VerbHealth,
	VerbStatus,
	VerbBarrierStatus,
	VerbBarrierInitialize,
	VerbBarrierUnseal,
	VerbShamirInitialize,
	VerbShamirUnsealShare,
	VerbShamirUnsealShares,
	VerbRecoveryRecover,
	VerbRootToken,
	VerbOfficerRegister,
	VerbOfficerClaimStatus,
	VerbShareClaimBegin,
	VerbShareClaimComplete,
	VerbRecoveryOfficerRegister,
	VerbRecoveryShareClaimBegin,
	VerbRecoveryShareClaimComplete,
	VerbCeremonyInitialize,
	VerbCeremonyStatus,
	VerbCeremonyClaimCertBegin,
	VerbCeremonyClaimCertComplete,
	VerbCeremonyOfficerShareBegin,
	VerbCeremonyOfficerShareComplete,
	VerbCeremonySignCSR,
	VerbCeremonyClaimShareBegin,
	VerbCeremonyClaimShareEnd,
	VerbCeremonySubmitUnsealShare,
}

// Table maps every canonical verb to its per-transport names. This is the
// single source of truth for the sealed-window allowlists.
var Table = map[Verb]Names{
	// Health is deliberately NOT REST-allowlisted: while the barrier is
	// sealed, GET /health must return the sealed-window 503 envelope
	// ({"error":"barrier sealed","code":"SEALED"}) rather than passing
	// through to the admin health handler. In sealed-window boot mode the
	// server has no admin service, so a pass-through would 404; short-
	// circuiting it in the middleware yields the documented sealed status.
	// The gRPC/QUIC/MCP health surfaces remain allowlisted as before.
	VerbHealth: {GRPC: "Health", QUIC: "admin.Health", MCP: "qrdb_health"},
	VerbStatus: {REST: []string{"/status"}, GRPC: "Status", QUIC: "admin.Status", MCP: "qrdb_status"},

	VerbBarrierStatus:     {REST: []string{"/v1/barrier/status"}, GRPC: "BarrierStatus", QUIC: "barrier.Status", MCP: "qrdb_barrier_status"},
	VerbBarrierInitialize: {REST: []string{"/v1/barrier/initialize"}, GRPC: "BarrierInitialize", QUIC: "barrier.Initialize", MCP: "qrdb_barrier_initialize", Transition: true},
	VerbBarrierUnseal:     {REST: []string{"/v1/barrier/unseal"}, GRPC: "BarrierUnseal", QUIC: "barrier.Unseal", MCP: "qrdb_barrier_unseal", Transition: true},

	VerbShamirInitialize:   {REST: []string{"/v1/barrier/shamir/initialize"}, GRPC: "BarrierShamirInitialize", QUIC: "barrier.ShamirInitialize", MCP: "qrdb_barrier_shamir_initialize", Transition: true},
	VerbShamirUnsealShare:  {REST: []string{"/v1/barrier/shamir/unseal-share"}, GRPC: "BarrierShamirUnsealShare", QUIC: "barrier.ShamirUnsealShare", MCP: "qrdb_barrier_shamir_unseal_share", Transition: true},
	VerbShamirUnsealShares: {REST: []string{"/v1/barrier/shamir/unseal-shares"}, GRPC: "BarrierShamirUnsealShares", QUIC: "barrier.ShamirUnsealShares", MCP: "qrdb_barrier_shamir_unseal_shares", Transition: true},

	VerbRecoveryRecover: {REST: []string{"/v1/barrier/recovery/recover"}, GRPC: "BarrierRecoveryRecover", QUIC: "barrier.RecoveryRecover", MCP: "qrdb_barrier_recovery_recover", Transition: true},

	VerbRootToken: {REST: []string{"/v1/barrier/root-token"}, GRPC: "BarrierRootToken", QUIC: "barrier.RootToken", MCP: "qrdb_barrier_root_token"},

	// GET and POST /v1/barrier/officers share one path (method-dispatched),
	// so officer-register is method-qualified: GET, the officer list, is not
	// allowlisted.
	// officer-register is PreOperationalOnly: RegisterOfficer CA-signs any
	// caller-supplied CSR and assigns a share index with no service-layer
	// auth, so its authentication exemption must lapse the moment the
	// enrollment ceremony reaches operational (see Names.PreOperationalOnly).
	VerbOfficerRegister:    {REST: []string{"POST /v1/barrier/officers"}, GRPC: "BarrierOfficerRegister", QUIC: "barrier.OfficerRegister", MCP: "qrdb_barrier_officer_register", PreOperationalOnly: true},
	VerbOfficerClaimStatus: {REST: []string{"/v1/barrier/officers/claim-status"}, GRPC: "BarrierOfficerClaimStatus", QUIC: "barrier.OfficerClaimStatus", MCP: "qrdb_barrier_officer_claim_status"},
	// The path-param claim routes (/{username}/claim/begin|complete) are
	// covered by the /v1/barrier/officers/ prefix, shared by both verbs.
	VerbShareClaimBegin:    {REST: []string{"/v1/barrier/officers/"}, GRPC: "BarrierShareClaimBegin", QUIC: "barrier.ShareClaimBegin", MCP: "qrdb_barrier_share_claim_begin"},
	VerbShareClaimComplete: {REST: []string{"/v1/barrier/officers/"}, GRPC: "BarrierShareClaimComplete", QUIC: "barrier.ShareClaimComplete", MCP: "qrdb_barrier_share_claim_complete"},

	// recovery-officer-register shares officer-register's PreOperationalOnly
	// rationale: it enrolls a recovery officer via the same
	// CSR-sign-and-assign-share flow with no service-layer auth.
	VerbRecoveryOfficerRegister:    {REST: []string{"POST /v1/barrier/recovery/officers"}, GRPC: "BarrierRecoveryOfficerRegister", QUIC: "barrier.RecoveryOfficerRegister", MCP: "qrdb_barrier_recovery_officer_register", PreOperationalOnly: true},
	VerbRecoveryShareClaimBegin:    {REST: []string{"/v1/barrier/recovery/officers/"}, GRPC: "BarrierRecoveryShareClaimBegin", QUIC: "barrier.RecoveryShareClaimBegin", MCP: "qrdb_barrier_recovery_share_claim_begin"},
	VerbRecoveryShareClaimComplete: {REST: []string{"/v1/barrier/recovery/officers/"}, GRPC: "BarrierRecoveryShareClaimComplete", QUIC: "barrier.RecoveryShareClaimComplete", MCP: "qrdb_barrier_recovery_share_claim_complete"},

	VerbCeremonyInitialize:        {REST: []string{"/v1/ceremony/initialize"}, GRPC: "CeremonyInitialize", QUIC: "ceremony.Initialize", MCP: "qrdb_ceremony_initialize", Transition: true},
	VerbCeremonyStatus:            {REST: []string{"/v1/ceremony/status"}, GRPC: "CeremonyStatus", QUIC: "ceremony.Status", MCP: "qrdb_ceremony_status"},
	VerbCeremonyClaimCertBegin:    {REST: []string{"/v1/ceremony/claim-cert/begin"}, GRPC: "CeremonyClaimCertBegin", QUIC: "ceremony.ClaimCertBegin", MCP: "qrdb_ceremony_claim_cert_begin"},
	VerbCeremonyClaimCertComplete: {REST: []string{"/v1/ceremony/claim-cert/complete"}, GRPC: "CeremonyClaimCertComplete", QUIC: "ceremony.ClaimCertComplete", MCP: "qrdb_ceremony_claim_cert_complete"},
	// The officer share-claim handshake. Distinct from the custodian
	// factor-based claim below (VerbCeremonyClaimShareBegin/End), which owns
	// /v1/ceremony/claim-share/*.
	VerbCeremonyOfficerShareBegin:    {REST: []string{"/v1/ceremony/officer-share/begin"}, GRPC: "CeremonyBeginClaimShare", QUIC: "ceremony.BeginClaimShare", MCP: "qrdb_ceremony_begin_claim_share"},
	VerbCeremonyOfficerShareComplete: {REST: []string{"/v1/ceremony/officer-share/complete"}, GRPC: "CeremonyCompleteClaimShare", QUIC: "ceremony.CompleteClaimShare", MCP: "qrdb_ceremony_complete_claim_share"},
	VerbCeremonySignCSR:              {REST: []string{"/v1/ceremony/sign-csr"}, GRPC: "CeremonySignCSR", QUIC: "ceremony.SignCSR", MCP: "qrdb_ceremony_sign_csr"},
	VerbCeremonyClaimShareBegin:      {REST: []string{"/v1/ceremony/claim-share/begin"}, GRPC: "CeremonyClaimShareBegin", QUIC: "ceremony.ClaimShareBegin", MCP: "qrdb_ceremony_claim_share_begin"},
	VerbCeremonyClaimShareEnd:        {REST: []string{"/v1/ceremony/claim-share/complete"}, GRPC: "CeremonyClaimShareComplete", QUIC: "ceremony.ClaimShareComplete", MCP: "qrdb_ceremony_claim_share_complete"},
	VerbCeremonySubmitUnsealShare:    {REST: []string{"/v1/barrier/unseal-share"}, GRPC: "CeremonySubmitUnsealShare", QUIC: "ceremony.SubmitUnsealShare", MCP: "qrdb_ceremony_submit_unseal_share", Transition: true},
}

// RESTExtra lists REST-only allowlist entries that have no
// transport-neutral verb: the cluster peer key-transport receiver, which
// must operate in every daemon state. Entries ending in "/" are prefix
// matches.
//
// There is deliberately no /v1/bootstrap/ entry, because no route lives
// under it: a prefix allowlisted over an empty subtree silently exempts
// whatever is registered there next. There is no /v1/init/ entry either.
// Its one route, enroll-external-officer, is an SO-only administrative
// enrollment that records caller-supplied trust material. On the
// sealed-window listener it would have been anonymous.
var RESTExtra = []string{
	// Cluster peer key-transport paths must work regardless of sealed
	// state so follower replicas can receive their tenant barrier key
	// before unsealing. The receiver self-authorises via node-cert +
	// placement check; the auth middleware also pre-exempts /v1/cluster/
	// unconditionally.
	"/v1/cluster/",
}

// RESTAllowlist derives the REST sealed-window allowlist from Table and
// RESTExtra, deduplicating shared paths while preserving derivation order.
func RESTAllowlist() []string {
	seen := make(map[string]struct{})
	var out []string
	appendPath := func(p string) {
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, v := range Verbs {
		for _, p := range Table[v].REST {
			appendPath(p)
		}
	}
	for _, p := range RESTExtra {
		appendPath(p)
	}
	return out
}

// GRPCAllowlist derives the gRPC sealed-window allowlist (method tails)
// from Table in derivation order.
//
// The entries are bare method names (the trailing component after the last
// "/" in a gRPC FullMethod). They are NOT suitable for matching against a
// request's FullMethod directly: a bare tail like "Status" would match the
// same-named method on ANY co-registered gRPC service. Callers that gate live
// requests must derive the fully-qualified allowlist via
// GRPCFullMethodAllowlist so the service is pinned.
func GRPCAllowlist() []string {
	var out []string
	for _, v := range Verbs {
		if m := Table[v].GRPC; m != "" {
			out = append(out, m)
		}
	}
	return out
}

// GRPCFullMethodAllowlist derives the gRPC sealed-window allowlist as
// fully-qualified "/{serviceName}/{method}" paths — the exact form of
// grpc.UnaryServerInfo.FullMethod — by qualifying every GRPCAllowlist tail
// with serviceName (e.g. "qrdb.v1.QRDBService").
//
// Matching a request's FullMethod against these fully-qualified paths (rather
// than the bare method tail) ensures that only the allowlisted methods on the
// intended service pass the sealed window: a same-named method on any OTHER
// co-registered gRPC service (e.g. "/qrdb.v1.TenantBarrierService/Status") is
// rejected while the barrier is sealed. serviceName MUST be the canonical
// gRPC service name taken from the generated ServiceDesc so it cannot drift.
func GRPCFullMethodAllowlist(serviceName string) []string {
	tails := GRPCAllowlist()
	out := make([]string, 0, len(tails))
	for _, m := range tails {
		out = append(out, "/"+serviceName+"/"+m)
	}
	return out
}

// QUICAllowlist derives the native QUIC sealed-window allowlist from Table.
func QUICAllowlist() map[string]struct{} {
	out := make(map[string]struct{}, len(Verbs))
	for _, v := range Verbs {
		if m := Table[v].QUIC; m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

// MCPAllowlist derives the MCP tool-name sealed-window allowlist from Table.
func MCPAllowlist() map[string]struct{} {
	out := make(map[string]struct{}, len(Verbs))
	for _, v := range Verbs {
		if m := Table[v].MCP; m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

// RESTEntryMatches reports whether one REST allowlist entry admits a request
// with the given method and path. An entry is "/path" (every method) or
// "METHOD /path". A path ending in "/" matches by prefix; anything else
// matches exactly.
func RESTEntryMatches(entry, method, path string) bool {
	if m, p, qualified := strings.Cut(entry, " "); qualified {
		if m != method {
			return false
		}
		entry = p
	}
	if strings.HasSuffix(entry, "/") {
		return strings.HasPrefix(path, entry)
	}
	return path == entry
}

// RESTPreOperationalOnly derives the REST entries of the PreOperationalOnly
// verbs from Table, in the RESTAllowlist entry format.
func RESTPreOperationalOnly() []string {
	var out []string
	for _, v := range Verbs {
		if Table[v].PreOperationalOnly {
			out = append(out, Table[v].REST...)
		}
	}
	return out
}

// GRPCFullMethodPreOperationalOnly derives the fully-qualified gRPC methods
// of the PreOperationalOnly verbs on serviceName.
func GRPCFullMethodPreOperationalOnly(serviceName string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.PreOperationalOnly && n.GRPC != "" {
			out["/"+serviceName+"/"+n.GRPC] = struct{}{}
		}
	}
	return out
}

// QUICPreOperationalOnly derives the native QUIC methods of the
// PreOperationalOnly verbs.
func QUICPreOperationalOnly() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.PreOperationalOnly && n.QUIC != "" {
			out[n.QUIC] = struct{}{}
		}
	}
	return out
}

// MCPPreOperationalOnly derives the MCP tools of the PreOperationalOnly
// verbs.
func MCPPreOperationalOnly() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.PreOperationalOnly && n.MCP != "" {
			out[n.MCP] = struct{}{}
		}
	}
	return out
}

// RESTTransition derives the REST entries of the Transition verbs.
func RESTTransition() []string {
	var out []string
	for _, v := range Verbs {
		if Table[v].Transition {
			out = append(out, Table[v].REST...)
		}
	}
	return out
}

// GRPCFullMethodTransition derives the fully-qualified gRPC methods of the
// Transition verbs on serviceName.
func GRPCFullMethodTransition(serviceName string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.Transition && n.GRPC != "" {
			out["/"+serviceName+"/"+n.GRPC] = struct{}{}
		}
	}
	return out
}

// QUICTransition derives the native QUIC methods of the Transition verbs.
func QUICTransition() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.Transition && n.QUIC != "" {
			out[n.QUIC] = struct{}{}
		}
	}
	return out
}

// MCPTransition derives the MCP tools of the Transition verbs.
func MCPTransition() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.Transition && n.MCP != "" {
			out[n.MCP] = struct{}{}
		}
	}
	return out
}

// Admission closes the window between the boot-time sealed-window listener's
// check of the barrier and the request it admits. A gate samples the barrier
// when a request arrives; without Admission a request admitted while sealed
// could still be running when another request unseals the barrier, and would
// then run anonymously against an unsealed barrier.
//
// Every request on that listener holds Admission for its whole duration:
// a Transition request exclusively, every other request shared. A Transition
// request therefore waits for the requests already in flight, none starts
// while it runs, and a request admitted after it samples the new state and is
// refused. One Admission is shared by all of a listener's transports. The
// zero value is ready to use.
type Admission struct {
	mu sync.RWMutex
}

// Admit holds the admission for one request and returns its release, which
// the caller must call when the request has completed.
func (a *Admission) Admit(transition bool) (release func()) {
	if transition {
		a.mu.Lock()
		return a.mu.Unlock
	}
	a.mu.RLock()
	return a.mu.RUnlock
}

// ListenerProbe is what the boot-time sealed-window listener's gate reads for
// every request: whether the barrier is sealed, and whether officer
// enrollment is still open to an unauthenticated caller (see
// ceremony.CeremonyService.EnrollmentOpen). An implementation answers
// EnrollmentOpen false whenever it cannot tell.
type ListenerProbe interface {
	IsSealed() bool
	EnrollmentOpen() bool
	// Admit holds the listener's Admission for one request; see Admission.
	Admit(transition bool) (release func())
}

// OrFailClosed returns p, or when p is nil a ListenerProbe that reports the
// barrier unsealed and enrollment closed, so a listener gate built without a
// probe refuses every request.
func OrFailClosed(p ListenerProbe) ListenerProbe {
	if p == nil {
		return failClosed{}
	}
	return p
}

// failClosed is the probe OrFailClosed substitutes for a missing one.
type failClosed struct{}

func (failClosed) IsSealed() bool       { return false }
func (failClosed) EnrollmentOpen() bool { return false }
func (failClosed) Admit(bool) func()    { return func() {} }

// anonymousKey is the context key WithAnonymous sets.
type anonymousKey struct{}

// WithAnonymous marks ctx as a request the boot-time sealed-window listener
// admitted. That listener has no authentication middleware, so every request
// it serves is anonymous, and a handler that answers differently to an
// anonymous caller reads the mark with Anonymous. The listener's gates set it
// on every request they admit.
func WithAnonymous(ctx context.Context) context.Context {
	return context.WithValue(ctx, anonymousKey{}, true)
}

// Anonymous reports whether ctx carries the WithAnonymous mark.
func Anonymous(ctx context.Context) bool {
	v, _ := ctx.Value(anonymousKey{}).(bool)
	return v
}

// Request is what a sealed-window gate knows about one request.
type Request struct {
	// Sealed is the barrier state sampled for this request.
	Sealed bool
	// Allowlisted reports whether the request names a Table verb on its
	// transport.
	Allowlisted bool
	// PreOperationalOnly reports whether that verb is PreOperationalOnly.
	PreOperationalOnly bool
	// Listener marks the boot-time sealed-window listener's gate.
	Listener bool
	// EnrollmentOpen is ListenerProbe.EnrollmentOpen, sampled for this
	// request. Only a PreOperationalOnly request on the listener reads it.
	EnrollmentOpen bool
}

// Decision is a sealed-window gate's verdict on one request.
type Decision int

const (
	// Serve passes the request on to the listener's own handling.
	Serve Decision = iota
	// RefuseSealed refuses a request that is not on the allowlist while the
	// barrier is sealed.
	RefuseSealed
	// RefuseClosed refuses a request on the boot-time sealed-window listener
	// once the barrier is unsealed.
	RefuseClosed
	// RefuseEnrollmentClosed refuses a PreOperationalOnly request on the
	// boot-time sealed-window listener once officer enrollment has closed.
	RefuseEnrollmentClosed
)

// ClosedMessage is the refusal text of the boot-time sealed-window listener
// once its barrier is unsealed. The regular listener takes over, and the
// client retries there.
const ClosedMessage = "sealed-window listener closed: barrier unsealed"

// EnrollmentClosedMessage is the refusal text of an anonymous officer
// registration once the enrollment ceremony is over. Officers are registered
// after that through an authenticated listener.
const EnrollmentClosedMessage = "officer enrollment is closed to unauthenticated callers: register officers through an authenticated listener"

// Decide is the one sealed-window admission rule every transport applies.
//
// A gate on a regular listener serves every request once the barrier is
// unsealed, and the listener's own authentication decides. While the barrier
// is sealed it serves the allowlist, still behind that authentication.
//
// The boot-time sealed-window listener has no authentication middleware,
// because while the barrier is sealed there is no identity plane to
// authenticate against, so its allowlist is anonymous. It must stop serving
// the moment the barrier unseals, not when it is next polled and stopped:
// otherwise every route registered on it runs anonymously against an
// unsealed barrier. It therefore refuses every request once unsealed,
// allowlisted or not. While sealed it also refuses a PreOperationalOnly verb
// (officer registration) once enrollment has closed, because that exemption
// is justified only while the enrollment ceremony can still use a new
// officer.
func Decide(r Request) Decision {
	switch {
	case !r.Sealed && r.Listener:
		return RefuseClosed
	case !r.Sealed:
		return Serve
	case !r.Allowlisted:
		return RefuseSealed
	case r.Listener && r.PreOperationalOnly && !r.EnrollmentOpen:
		return RefuseEnrollmentClosed
	default:
		return Serve
	}
}
