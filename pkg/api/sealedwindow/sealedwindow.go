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
// A sealed node serves the unseal call, and answers one status question:
// whether it is initialized. The unseal call is the path that submits unseal
// material: a passphrase unseal, a holder quorum's factor challenge and share
// submission, the share claim that hands a holder the share it submits, and
// -- on an authenticated listener only -- the clearing of a cluster-wide
// seal. Every other call, health and every other status call included, is
// refused with the typed sealed refusal (see ReasonSealed); unseal progress
// travels in the unseal call's own response.
//
// Barrier status is the status question. On the boot-time sealed-window
// listener it reports the barrier sealed and whether it is initialized, and
// nothing else: no strategy, no share counts, no cluster seal state, nothing
// read from the store. It is how a client tells a node that needs its first
// boot from one that needs unsealing. It is ListenerOnly: a node the cluster
// holds or cuts off while its own barrier is unsealed gates on the regular,
// authenticated listener, which refuses it, so an operator reads the seal
// record from a serving node.
//
// A node whose barrier has never been initialized has no key to seal, and
// it also reports sealed: its first-boot verbs -- initialize, the
// enrollment ceremony's holder registration and certificate signing -- are
// PreOperationalOnly, served only while enrollment is still open, and
// refused like every other call once the barrier is initialized.
//
// Every protocol transport (REST, gRPC, native QUIC, MCP, and the
// in-process embedded transport) derives its
// sealed-window allowlist from the single Table in this package, so the
// per-transport lists can never drift apart again. A verb is present in
// Table when at least one transport exposes the operation; a transport
// whose Names entry is empty simply has no surface for that verb.
//
// Deliberate exclusions (never allowlisted on any transport):
//
//   - health, node status and ceremony status. A sealed node answers no
//     status question but barrier status: a caller that reaches it and is
//     refused with the typed sealed refusal has learned it is up and
//     sealed, which is everything a sealed node can truthfully say.
//   - barrier seal -- sealing is only meaningful while the barrier is
//     unsealed (see docs/seal/sealed-window.md). A holder quorum voting to
//     seal submits through the quorum verbs, whose service refuses every
//     op but unseal and recovery on a sealed barrier.
//   - barrier enable-encryption -- the migration requires an unsealed
//     data plane.
//   - holder list, recovery-key generate / delete / status, rekey start and
//     holder revocation. Every verb that reaches the boot-time sealed-window
//     listener does so WITHOUT authentication, because that listener has no
//     identity plane to authenticate against.
//
// Every entry in Table is therefore an ANONYMOUS verb on that listener and
// must be justified as one: a flag that carries no secret (barrier status),
// credential possession (unseal), first-boot trust-on-first-use (initialize,
// holder registration), or a handshake whose binding authorization the
// handler enforces (the envelope claims, the holder quorum's factor proof,
// and ceremony-sign-csr's SO PIN).
package sealedwindow

import (
	"context"
	"strings"
	"sync"
)

// Verb is a transport-neutral identifier for one sealed-window operation.
type Verb string

// Canonical sealed-window verbs: the unseal call, the first-boot verbs, and
// ceremony status, which no sealed node serves but the awaiting-admin-
// credential gate does (see AwaitingAdminCredentialVerbs).
const (
	VerbBarrierStatus     Verb = "barrier-status"
	VerbBarrierInitialize Verb = "barrier-initialize"
	VerbBarrierUnseal     Verb = "barrier-unseal"

	VerbShamirInitialize Verb = "shamir-initialize"

	VerbOfficerRegister    Verb = "officer-register"
	VerbShareClaimBegin    Verb = "share-claim-begin"
	VerbShareClaimComplete Verb = "share-claim-complete"

	VerbRecoveryOfficerRegister    Verb = "recovery-officer-register"
	VerbRecoveryShareClaimBegin    Verb = "recovery-share-claim-begin"
	VerbRecoveryShareClaimComplete Verb = "recovery-share-claim-complete"

	VerbQuorumBegin  Verb = "quorum-begin"
	VerbQuorumSubmit Verb = "quorum-submit"

	VerbCeremonyInitialize Verb = "ceremony-initialize"
	VerbCeremonyStatus     Verb = "ceremony-status"
	VerbCeremonySignCSR    Verb = "ceremony-sign-csr"

	// VerbBarrierClusterUnseal clears a cluster-wide seal of the node
	// barrier. It is AuthenticatedOnly.
	VerbBarrierClusterUnseal Verb = "barrier-cluster-unseal"
)

// Names holds the per-transport wire names for one canonical verb. An
// empty field means the verb has no surface on that transport.
type Names struct {
	// REST entries: a path, or a method-qualified "METHOD /path". A path
	// ending in "/" is a prefix match; a "{name}" segment matches exactly
	// one non-empty path segment, as in a net/http.ServeMux pattern;
	// anything else matches exactly. An unqualified entry matches every
	// method (see RESTEntryMatches).
	REST []string
	// GRPC is the method tail — the suffix after the last "/" in
	// grpc.UnaryServerInfo.FullMethod.
	GRPC string
	// QUIC is the native QUIC method name (e.g. "barrier.Unseal").
	QUIC string
	// MCP is the MCP tool name (e.g. "qrdb_barrier_unseal").
	MCP string
	// Embedded is the in-process embedded transport's method name (e.g.
	// "BarrierUnseal"). The embedded transport has no boot-time listener:
	// it is always the regular, authenticated side of the gate.
	Embedded string
	// PreOperationalOnly marks a first-boot verb: one a sealed node serves
	// only while officer enrollment is still open -- the barrier has never
	// been initialized and the ceremony has not reached operational (see
	// ceremony.CeremonyService.EnrollmentOpen). Once the barrier is
	// initialized a sealed node holds a key it can only be unsealed with,
	// and a first-boot verb is refused like every other call that is not
	// the unseal call. It mirrors go-quicbaas/pkg/api/sealedwindow's field
	// of the same name so the two canonical tables diff mechanically.
	//
	// It is set for initialize, the holder registration verbs and
	// sign-csr. RegisterOfficer (pkg/ceremony) has the CA issue a holder
	// certificate for any caller-supplied CSR with no service-layer
	// authentication, so an exemption that survived the first boot would
	// let an unauthenticated caller enrol a holder during any
	// restart-while-sealed.
	//
	// The unseal verbs are deliberately NOT marked: a daemon restarted after
	// a completed ceremony is operational AND sealed, and unseal must remain
	// reachable without a token because a sealed IAM plane cannot validate
	// one.
	PreOperationalOnly bool

	// Transition marks a verb that can move the barrier out of the sealed
	// state -- initialize it or unseal it. On the boot-time sealed-window
	// listener such a request runs alone: it waits for every anonymous
	// request already in flight, and none starts while it runs, so no request
	// admitted under seal is still running when the barrier unseals (see
	// Admission).
	Transition bool

	// AuthenticatedOnly marks a verb the regular listener admits while its
	// gate is closed -- so it still runs behind that listener's
	// authentication -- and the boot-time sealed-window listener never
	// serves, because every request there is anonymous. Cluster unseal is
	// the one: a node held by a cluster seal refuses everything outside the
	// allowlist, and clearing the seal is an authorized platform act, not a
	// credential-possession one.
	AuthenticatedOnly bool

	// ListenerOnly is AuthenticatedOnly's mirror: a verb the boot-time
	// sealed-window listener serves while the node barrier is sealed, and
	// the regular listener refuses while its gate is closed. That gate is
	// closed with the node barrier unsealed only when a cluster seal holds
	// the node, cuts it off or is not yet confirmed, and such a node answers
	// no status question; an operator reads the seal record from a serving
	// node. Barrier status is the one.
	ListenerOnly bool
}

// PreOperationalOnly reports whether verb v is a first-boot verb, served by
// a sealed node only while enrollment is open. Consumers deriving an
// authentication bypass from this table MUST drop the verb from any
// state-independent exemption set and fail closed when the enrollment state
// cannot be determined.
func PreOperationalOnly(v Verb) bool {
	return Table[v].PreOperationalOnly
}

// Verbs is the sealed surface in stable derivation order: every Table verb a
// sealed node serves. VerbCeremonyStatus is in Table and not here.
var Verbs = []Verb{
	VerbBarrierStatus,
	VerbBarrierInitialize,
	VerbBarrierUnseal,
	VerbShamirInitialize,
	VerbOfficerRegister,
	VerbShareClaimBegin,
	VerbShareClaimComplete,
	VerbRecoveryOfficerRegister,
	VerbRecoveryShareClaimBegin,
	VerbRecoveryShareClaimComplete,
	VerbQuorumBegin,
	VerbQuorumSubmit,
	VerbCeremonyInitialize,
	VerbCeremonySignCSR,
	VerbBarrierClusterUnseal,
}

// Table maps every canonical verb to its per-transport names. This is the
// single source of truth for the sealed-window allowlists.
var Table = map[Verb]Names{
	// Barrier status is the one status question a sealed node answers:
	// whether it is initialized, so a client can tell a node waiting for its
	// first boot from one waiting to be unsealed. It is ListenerOnly: a node
	// a cluster seal holds or cuts off refuses it on the regular listener.
	VerbBarrierStatus: {REST: []string{"/v1/barrier/status"}, GRPC: "BarrierStatus", QUIC: "barrier.Status", MCP: "qrdb_barrier_status", Embedded: "BarrierStatus", ListenerOnly: true},
	// Initialize is a first-boot verb: an initialized barrier refuses it
	// anyway, so a sealed node that holds a key never serves it.
	VerbBarrierInitialize: {REST: []string{"/v1/barrier/initialize"}, GRPC: "BarrierInitialize", QUIC: "barrier.Initialize", MCP: "qrdb_barrier_initialize", Embedded: "BarrierInitialize", Transition: true, PreOperationalOnly: true},
	VerbBarrierUnseal:     {REST: []string{"/v1/barrier/unseal"}, GRPC: "BarrierUnseal", QUIC: "barrier.Unseal", MCP: "qrdb_barrier_unseal", Embedded: "BarrierUnseal", Transition: true},

	VerbShamirInitialize: {REST: []string{"/v1/barrier/shamir/initialize"}, GRPC: "BarrierShamirInitialize", QUIC: "barrier.ShamirInitialize", MCP: "qrdb_barrier_shamir_initialize", Embedded: "BarrierInitializeShamir", Transition: true, PreOperationalOnly: true},

	// GET and POST /v1/barrier/officers share one path (method-dispatched),
	// so officer-register is method-qualified: GET, the holder list, is not
	// allowlisted.
	// officer-register is PreOperationalOnly: RegisterOfficer has the CA
	// issue a holder certificate for the caller's CSR, so its exemption
	// must lapse the moment enrollment closes (see Names.PreOperationalOnly).
	VerbOfficerRegister: {REST: []string{"POST /v1/barrier/officers"}, GRPC: "BarrierOfficerRegister", QUIC: "barrier.OfficerRegister", MCP: "qrdb_barrier_officer_register", Embedded: "OfficerRegister", PreOperationalOnly: true},
	// A share claim hands a holder the share it then submits to the quorum,
	// so it is part of the unseal call: a barrier migrated to Shamir comes
	// back sealed, and its holders claim their envelopes from the sealed
	// node before M of them unseal it.
	//
	// The claim routes carry the holder in a path segment. They are listed
	// as one-segment patterns, never as an /v1/barrier/officers/ prefix: a
	// prefix would also admit POST /v1/barrier/officers/revoke, a rekey that
	// removes a holder, which is not a sealed-window operation.
	VerbShareClaimBegin:    {REST: []string{"POST /v1/barrier/officers/{username}/claim/begin"}, GRPC: "BarrierShareClaimBegin", QUIC: "barrier.ShareClaimBegin", MCP: "qrdb_barrier_share_claim_begin", Embedded: "ShareClaimBegin"},
	VerbShareClaimComplete: {REST: []string{"POST /v1/barrier/officers/{username}/claim/complete"}, GRPC: "BarrierShareClaimComplete", QUIC: "barrier.ShareClaimComplete", MCP: "qrdb_barrier_share_claim_complete", Embedded: "ShareClaimComplete"},

	// recovery-officer-register shares officer-register's PreOperationalOnly
	// rationale: it enrols a recovery holder through the same flow.
	VerbRecoveryOfficerRegister:    {REST: []string{"POST /v1/barrier/recovery/officers"}, GRPC: "BarrierRecoveryOfficerRegister", QUIC: "barrier.RecoveryOfficerRegister", MCP: "qrdb_barrier_recovery_officer_register", Embedded: "RecoveryOfficerRegister", PreOperationalOnly: true},
	VerbRecoveryShareClaimBegin:    {REST: []string{"POST /v1/barrier/recovery/officers/{username}/claim/begin"}, GRPC: "BarrierRecoveryShareClaimBegin", QUIC: "barrier.RecoveryShareClaimBegin", MCP: "qrdb_barrier_recovery_share_claim_begin", Embedded: "RecoveryShareClaimBegin"},
	VerbRecoveryShareClaimComplete: {REST: []string{"POST /v1/barrier/recovery/officers/{username}/claim/complete"}, GRPC: "BarrierRecoveryShareClaimComplete", QUIC: "barrier.RecoveryShareClaimComplete", MCP: "qrdb_barrier_recovery_share_claim_complete", Embedded: "RecoveryShareClaimComplete"},

	// The holder quorum: a holder answers a factor challenge and submits its
	// share. Unseal and recovery happen here, so both must be reachable
	// while sealed; the factor proof, verified against the roster the
	// wrapped root key is sealed under, is the credential. The quorum
	// service refuses every other op while the barrier is sealed, and a
	// submission reports the unseal's progress in its own response. A
	// submission can complete an unseal, so it is a Transition.
	VerbQuorumBegin:  {REST: []string{"/v1/barrier/quorum/begin"}, GRPC: "BarrierQuorumBegin", QUIC: "barrier.QuorumBegin", MCP: "qrdb_barrier_quorum_begin", Embedded: "BarrierQuorumBegin"},
	VerbQuorumSubmit: {REST: []string{"/v1/barrier/quorum/submit"}, GRPC: "BarrierQuorumSubmit", QUIC: "barrier.QuorumSubmit", MCP: "qrdb_barrier_quorum_submit", Embedded: "BarrierQuorumSubmit", Transition: true},

	VerbCeremonyInitialize: {REST: []string{"/v1/ceremony/initialize"}, GRPC: "CeremonyInitialize", QUIC: "ceremony.Initialize", MCP: "qrdb_ceremony_initialize", Embedded: "CeremonyInitialize", Transition: true, PreOperationalOnly: true},
	// Ceremony status is not on the sealed surface (it is absent from
	// Verbs); it is here for AwaitingAdminCredentialVerbs, a state of an
	// unsealed node.
	VerbCeremonyStatus: {REST: []string{"/v1/ceremony/status"}, GRPC: "CeremonyStatus", QUIC: "ceremony.Status", MCP: "qrdb_ceremony_status", Embedded: "CeremonyStatus"},
	// ceremony-sign-csr is PreOperationalOnly for the same reason the officer
	// registration verbs are: minting a NEW CA-signed certificate from a
	// caller-supplied CSR needs the cluster CA key (Key A), which is
	// barrier-encrypted and reachable only through an unsealed
	// cakey.Provider once the ceremony is operational. Before that -- the
	// initial M-of-N ceremony -- Key A is still the in-memory
	// cakey.StaticProvider created at generation (see
	// docs/certificate-authority/architecture.md, "Signing only while
	// unsealed"), which is what this exemption is scoped to. An officer who
	// already holds a certificate still unseals with it on every later
	// restart: verifying a presented certificate needs only the CA
	// certificate (public), never this verb.
	VerbCeremonySignCSR: {REST: []string{"/v1/ceremony/sign-csr"}, GRPC: "CeremonySignCSR", QUIC: "ceremony.SignCSR", MCP: "qrdb_ceremony_sign_csr", Embedded: "CeremonySignCSR", PreOperationalOnly: true},

	VerbBarrierClusterUnseal: {REST: []string{"/v1/barrier/cluster-unseal"}, GRPC: "BarrierClusterUnseal", QUIC: "barrier.ClusterUnseal", MCP: "qrdb_barrier_cluster_unseal", Embedded: "BarrierClusterUnseal", AuthenticatedOnly: true},
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

// EmbeddedAllowlist returns the embedded transport's method names that
// remain reachable while its gate is closed.
func EmbeddedAllowlist() map[string]struct{} {
	out := make(map[string]struct{}, len(Verbs))
	for _, v := range Verbs {
		if m := Table[v].Embedded; m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

// RESTEntryMatches reports whether one REST allowlist entry admits a request
// with the given method and path. An entry is "/path" (every method) or
// "METHOD /path". A path ending in "/" matches by prefix; a path with a
// "{name}" segment matches paths of the same shape, the "{name}" segment
// matching exactly one non-empty path segment; anything else matches
// exactly.
func RESTEntryMatches(entry, method, path string) bool {
	if m, p, qualified := strings.Cut(entry, " "); qualified {
		if m != method {
			return false
		}
		entry = p
	}
	switch {
	case strings.HasSuffix(entry, "/"):
		return strings.HasPrefix(path, entry)
	case strings.Contains(entry, "{"):
		return segmentsMatch(path, entry)
	default:
		return path == entry
	}
}

// segmentsMatch reports whether path has pattern's shape: the same number of
// "/"-separated segments, each equal to the pattern's, except that a "{name}"
// pattern segment matches any one non-empty segment.
func segmentsMatch(path, pattern string) bool {
	got := strings.Split(path, "/")
	want := strings.Split(pattern, "/")
	if len(got) != len(want) {
		return false
	}
	for i, w := range want {
		if strings.HasPrefix(w, "{") && strings.HasSuffix(w, "}") {
			if got[i] == "" {
				return false
			}
			continue
		}
		if got[i] != w {
			return false
		}
	}
	return true
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

// EmbeddedPreOperationalOnly derives the embedded transport's methods of the
// PreOperationalOnly verbs.
func EmbeddedPreOperationalOnly() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.PreOperationalOnly && n.Embedded != "" {
			out[n.Embedded] = struct{}{}
		}
	}
	return out
}

// RESTAuthenticatedOnly derives the REST entries of the AuthenticatedOnly
// verbs.
func RESTAuthenticatedOnly() []string {
	var out []string
	for _, v := range Verbs {
		if Table[v].AuthenticatedOnly {
			out = append(out, Table[v].REST...)
		}
	}
	return out
}

// GRPCFullMethodAuthenticatedOnly derives the fully-qualified gRPC methods of
// the AuthenticatedOnly verbs on serviceName.
func GRPCFullMethodAuthenticatedOnly(serviceName string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.AuthenticatedOnly && n.GRPC != "" {
			out["/"+serviceName+"/"+n.GRPC] = struct{}{}
		}
	}
	return out
}

// QUICAuthenticatedOnly derives the native QUIC methods of the
// AuthenticatedOnly verbs.
func QUICAuthenticatedOnly() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.AuthenticatedOnly && n.QUIC != "" {
			out[n.QUIC] = struct{}{}
		}
	}
	return out
}

// MCPAuthenticatedOnly derives the MCP tools of the AuthenticatedOnly verbs.
func MCPAuthenticatedOnly() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.AuthenticatedOnly && n.MCP != "" {
			out[n.MCP] = struct{}{}
		}
	}
	return out
}

// RESTListenerOnly derives the REST entries of the ListenerOnly verbs.
func RESTListenerOnly() []string {
	var out []string
	for _, v := range Verbs {
		if Table[v].ListenerOnly {
			out = append(out, Table[v].REST...)
		}
	}
	return out
}

// GRPCFullMethodListenerOnly derives the fully-qualified gRPC methods of the
// ListenerOnly verbs on serviceName.
func GRPCFullMethodListenerOnly(serviceName string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.ListenerOnly && n.GRPC != "" {
			out["/"+serviceName+"/"+n.GRPC] = struct{}{}
		}
	}
	return out
}

// QUICListenerOnly derives the native QUIC methods of the ListenerOnly verbs.
func QUICListenerOnly() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.ListenerOnly && n.QUIC != "" {
			out[n.QUIC] = struct{}{}
		}
	}
	return out
}

// MCPListenerOnly derives the MCP tools of the ListenerOnly verbs.
func MCPListenerOnly() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.ListenerOnly && n.MCP != "" {
			out[n.MCP] = struct{}{}
		}
	}
	return out
}

// EmbeddedListenerOnly derives the embedded transport's methods of the
// ListenerOnly verbs. The embedded transport is always the regular side of
// the gate, so while sealed it refuses them.
func EmbeddedListenerOnly() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range Verbs {
		if n := Table[v]; n.ListenerOnly && n.Embedded != "" {
			out[n.Embedded] = struct{}{}
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

// AwaitingAdminCredentialVerbs is the verb set served while a node is in the
// "awaiting admin credential" boot state (go-qrdb#548 review item 3):
// auth.mtls_binding_policy is "required" and the bootstrap admin holds no
// live certificate binding. Reusing the canonical Table rather than a
// second allowlist means the two verbs can never drift from the names
// every other sealed-window derivation already uses.
//
//   - VerbCeremonyStatus -- so an operator (or `qrdb status`-style tooling)
//     can see ceremony state while the node is otherwise refusing
//     everything.
//   - VerbCeremonyInitialize -- CeremonyServiceWithAdminRebind treats a
//     call here as the admin-credential retry once the ceremony is
//     already initialized, which is the ONLY way out of this boot state.
//     Its own request body carries the SO/User PINs CeremonyInitialize
//     verifies, and the service-layer leader gate
//     (CeremonyServiceWithLeaderGate) refuses it on any node that is not
//     the Raft leader -- both already enforced one layer down, so this
//     gate's only job is deciding whether the request reaches that service
//     call at all.
//
// Unlike the sealed-window listener's allowlist, a request admitted here is
// NEVER authenticated: there is no bound identity to authenticate as, which
// is the whole reason the node is in this state. See
// AwaitingAdminCredentialClosedMessage for the refusal every other verb
// gets.
var AwaitingAdminCredentialVerbs = []Verb{VerbCeremonyStatus, VerbCeremonyInitialize}

// RESTAwaitingAdminCredential derives the REST entries of
// AwaitingAdminCredentialVerbs from Table.
func RESTAwaitingAdminCredential() []string {
	var out []string
	for _, v := range AwaitingAdminCredentialVerbs {
		out = append(out, Table[v].REST...)
	}
	return out
}

// GRPCFullMethodAwaitingAdminCredential derives the fully-qualified gRPC
// methods of AwaitingAdminCredentialVerbs on serviceName.
func GRPCFullMethodAwaitingAdminCredential(serviceName string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range AwaitingAdminCredentialVerbs {
		if m := Table[v].GRPC; m != "" {
			out["/"+serviceName+"/"+m] = struct{}{}
		}
	}
	return out
}

// QUICAwaitingAdminCredential derives the native QUIC methods of
// AwaitingAdminCredentialVerbs.
func QUICAwaitingAdminCredential() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range AwaitingAdminCredentialVerbs {
		if m := Table[v].QUIC; m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

// MCPAwaitingAdminCredential derives the MCP tool names of
// AwaitingAdminCredentialVerbs.
func MCPAwaitingAdminCredential() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range AwaitingAdminCredentialVerbs {
		if m := Table[v].MCP; m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

// EmbeddedAwaitingAdminCredential derives the embedded transport's method
// names of AwaitingAdminCredentialVerbs.
func EmbeddedAwaitingAdminCredential() map[string]struct{} {
	out := make(map[string]struct{})
	for _, v := range AwaitingAdminCredentialVerbs {
		if m := Table[v].Embedded; m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

// AwaitingAdminCredentialClosedMessage is the refusal text every transport's
// awaiting-admin-credential gate gives a request that is not on
// AwaitingAdminCredentialVerbs' allowlist, while the node is in that state.
const AwaitingAdminCredentialClosedMessage = "node is awaiting admin credential: only ceremony status and ceremony initialize (the admin-credential retry) are served until an admin certificate binds"

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

// EnrollmentProbe reports whether officer enrollment is still open: the
// barrier has never been initialized and the ceremony has not reached
// operational (see ceremony.CeremonyService.EnrollmentOpen). A sealed
// node's gate reads it for a PreOperationalOnly verb only.
type EnrollmentProbe interface {
	// EnrollmentOpen is read under the request's context, and must return
	// promptly and fail closed -- false -- when that context ends or the
	// state cannot be read.
	EnrollmentOpen(ctx context.Context) bool
}

// ListenerProbe is what the boot-time sealed-window listener's gate reads for
// every request: whether the barrier is sealed, and whether officer
// enrollment is still open. An implementation answers EnrollmentOpen false
// whenever it cannot tell.
type ListenerProbe interface {
	IsSealed() bool
	EnrollmentProbe
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

func (failClosed) IsSealed() bool                      { return false }
func (failClosed) EnrollmentOpen(context.Context) bool { return false }
func (failClosed) Admit(bool) func()                   { return func() {} }

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

// StateProbe is implemented by a probe that can say why it is closed. The
// node gate a cluster seal composes is closed both while the node is sealed
// and while the node cannot confirm the cluster seal state, and every
// transport refuses the two differently: a node that cannot read its seal
// records has not been sealed, and reporting it as sealed sends an operator
// to unseal a barrier that is already unsealed.
type StateProbe interface {
	// SealState reports whether the probe is closed and, when it is,
	// whether only because the node cannot confirm the cluster seal state.
	SealState() (sealed, unconfirmed bool)
}

// Sample reads p once for one request: whether it is closed, and whether
// only because the node cannot confirm the cluster seal state. A probe that
// is not a StateProbe is never unconfirmed, and a nil probe is open.
func Sample(p interface{ IsSealed() bool }) (sealed, unconfirmed bool) {
	if p == nil {
		return false, false
	}
	if sp, ok := p.(StateProbe); ok {
		return sp.SealState()
	}
	return p.IsSealed(), false
}

// Request is what a sealed-window gate knows about one request.
type Request struct {
	// Sealed is the barrier state sampled for this request.
	Sealed bool
	// Unconfirmed reports that the gate is closed only because the node
	// cannot confirm the cluster seal state (see Sample).
	Unconfirmed bool
	// Allowlisted reports whether the request names a Table verb on its
	// transport.
	Allowlisted bool
	// PreOperationalOnly reports whether that verb is PreOperationalOnly.
	PreOperationalOnly bool
	// Listener marks the boot-time sealed-window listener's gate.
	Listener bool
	// EnrollmentOpen is EnrollmentProbe.EnrollmentOpen, sampled for this
	// request. Only a sealed PreOperationalOnly request reads it; a gate
	// with no enrollment probe leaves it false.
	EnrollmentOpen bool
	// AuthenticatedOnly reports whether the verb is AuthenticatedOnly.
	AuthenticatedOnly bool
	// ListenerOnly reports whether the verb is ListenerOnly.
	ListenerOnly bool
}

// Decision is a sealed-window gate's verdict on one request.
type Decision int

const (
	// Serve passes the request on to the listener's own handling.
	Serve Decision = iota
	// RefuseSealed refuses every request a sealed node does not serve: one
	// that is not the unseal call, or a first-boot verb once enrollment
	// has closed. Every transport answers it with its Unavailable status
	// and ReasonSealed.
	RefuseSealed
	// RefuseClosed refuses a request on the boot-time sealed-window listener
	// once the barrier is unsealed.
	RefuseClosed
	// RefuseUnconfirmed refuses every request a node that cannot confirm the
	// cluster seal state does not serve: its lease on the seal records ran
	// out, or it has not read them since it started. The barrier is not
	// sealed and the node keeps its keys, so the refusal is retryable and is
	// not reported as sealed.
	RefuseUnconfirmed
)

// ClosedMessage is the refusal text of the boot-time sealed-window listener
// once its barrier is unsealed. The regular listener takes over, and the
// client retries there.
const ClosedMessage = "sealed-window listener closed: barrier unsealed"

// The structured form of RefuseSealed, which every transport carries beside
// its Unavailable status so a client tells "up and sealed" from any other
// unavailability without parsing text: gRPC as a google.rpc.ErrorInfo
// detail, native QUIC as error-frame info, MCP as the error envelope's
// reason, and REST as the body's "code".
const (
	// ErrorDomain is the ErrorInfo domain of qrdb's structured refusals,
	// shared with pkg/replicaforward.ErrorDomain.
	ErrorDomain = "qrdb.v1"

	// ReasonSealed marks a refusal by a sealed node.
	ReasonSealed = "SEALED"

	// SealedMessage is the refusal text of RefuseSealed.
	SealedMessage = "barrier sealed"
)

// Decide is the one sealed-window admission rule every transport applies.
//
// A sealed node serves the unseal call and the initialized flag (barrier
// status), and nothing else. A gate on a regular listener serves every request once the barrier is unsealed, and
// the listener's own authentication decides. While the barrier is sealed it
// serves the allowlist, still behind that authentication.
//
// The boot-time sealed-window listener has no authentication middleware,
// because while the barrier is sealed there is no identity plane to
// authenticate against, so its allowlist is anonymous. It must stop serving
// the moment the barrier unseals, not when it is next polled and stopped:
// otherwise every route registered on it runs anonymously against an
// unsealed barrier. It therefore refuses every request once unsealed,
// allowlisted or not, and it never serves an AuthenticatedOnly verb. A
// sealed regular listener never serves a ListenerOnly verb.
//
// On either listener a sealed node refuses a PreOperationalOnly verb once
// enrollment has closed: a first-boot verb is justified only while the
// barrier has never been initialized.
//
// A node that cannot confirm the cluster seal state is not sealed: its gate
// serves the allowlist but for barrier status, which it refuses like a held
// node does, and it refuses as RefuseUnconfirmed, never as RefuseSealed.
func Decide(r Request) Decision {
	switch {
	case !r.Sealed && r.Listener:
		return RefuseClosed
	case !r.Sealed:
		return Serve
	case !r.Allowlisted, r.Listener && r.AuthenticatedOnly, !r.Listener && r.ListenerOnly, r.PreOperationalOnly && !r.EnrollmentOpen:
		return closedGateRefusal(r)
	default:
		return Serve
	}
}

// closedGateRefusal is how a closed gate refuses r: as unconfirmed when it is
// closed only because the node cannot confirm the cluster seal state, and as
// sealed otherwise.
func closedGateRefusal(r Request) Decision {
	if r.Unconfirmed {
		return RefuseUnconfirmed
	}
	return RefuseSealed
}
