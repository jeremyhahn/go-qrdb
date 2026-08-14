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
package sealedwindow

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
	VerbShamirVerify       Verb = "shamir-verify"

	VerbRekey Verb = "rekey"

	VerbRecoveryGenerate Verb = "recovery-generate"
	VerbRecoveryRecover  Verb = "recovery-recover"
	VerbRecoveryDelete   Verb = "recovery-delete"
	VerbRecoveryStatus   Verb = "recovery-status"

	VerbRootToken Verb = "root-token"

	VerbOfficerRegister    Verb = "officer-register"
	VerbOfficerList        Verb = "officer-list"
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
	VerbCeremonyEnrollExtOfficer     Verb = "ceremony-enroll-external-officer"
)

// Names holds the per-transport wire names for one canonical verb. An
// empty field means the verb has no surface on that transport.
type Names struct {
	// REST paths. Entries ending in "/" are prefix matches; everything
	// else matches exactly (see rest.SealedWindowMiddleware).
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
	VerbShamirVerify,
	VerbRekey,
	VerbRecoveryGenerate,
	VerbRecoveryRecover,
	VerbRecoveryDelete,
	VerbRecoveryStatus,
	VerbRootToken,
	VerbOfficerRegister,
	VerbOfficerList,
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
	VerbCeremonyEnrollExtOfficer,
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
	VerbBarrierInitialize: {REST: []string{"/v1/barrier/initialize"}, GRPC: "BarrierInitialize", QUIC: "barrier.Initialize", MCP: "qrdb_barrier_initialize"},
	VerbBarrierUnseal:     {REST: []string{"/v1/barrier/unseal"}, GRPC: "BarrierUnseal", QUIC: "barrier.Unseal", MCP: "qrdb_barrier_unseal"},

	VerbShamirInitialize:   {REST: []string{"/v1/barrier/shamir/initialize"}, GRPC: "BarrierShamirInitialize", QUIC: "barrier.ShamirInitialize", MCP: "qrdb_barrier_shamir_initialize"},
	VerbShamirUnsealShare:  {REST: []string{"/v1/barrier/shamir/unseal-share"}, GRPC: "BarrierShamirUnsealShare", QUIC: "barrier.ShamirUnsealShare", MCP: "qrdb_barrier_shamir_unseal_share"},
	VerbShamirUnsealShares: {REST: []string{"/v1/barrier/shamir/unseal-shares"}, GRPC: "BarrierShamirUnsealShares", QUIC: "barrier.ShamirUnsealShares", MCP: "qrdb_barrier_shamir_unseal_shares"},
	// shamir-verify has no gRPC RPC or MCP tool; REST and native QUIC only.
	VerbShamirVerify: {REST: []string{"/v1/barrier/shamir/verify"}, QUIC: "barrier.ShamirVerify"},

	VerbRekey: {REST: []string{"/v1/barrier/rekey"}, GRPC: "BarrierRekey", QUIC: "barrier.Rekey", MCP: "qrdb_barrier_rekey"},

	VerbRecoveryGenerate: {REST: []string{"/v1/barrier/recovery/generate"}, GRPC: "BarrierRecoveryGenerate", QUIC: "barrier.RecoveryGenerate", MCP: "qrdb_barrier_recovery_generate"},
	VerbRecoveryRecover:  {REST: []string{"/v1/barrier/recovery/recover"}, GRPC: "BarrierRecoveryRecover", QUIC: "barrier.RecoveryRecover", MCP: "qrdb_barrier_recovery_recover"},
	// REST recovery-delete is DELETE /v1/barrier/recovery (exact path).
	VerbRecoveryDelete: {REST: []string{"/v1/barrier/recovery"}, GRPC: "BarrierRecoveryDelete", QUIC: "barrier.RecoveryDelete", MCP: "qrdb_barrier_recovery_delete"},
	VerbRecoveryStatus: {REST: []string{"/v1/barrier/recovery/status"}, GRPC: "BarrierRecoveryStatus", QUIC: "barrier.RecoveryStatus", MCP: "qrdb_barrier_recovery_status"},

	VerbRootToken: {REST: []string{"/v1/barrier/root-token"}, GRPC: "BarrierRootToken", QUIC: "barrier.RootToken", MCP: "qrdb_barrier_root_token"},

	// GET and POST /v1/barrier/officers share one path (method-dispatched).
	// officer-register is PreOperationalOnly: RegisterOfficer CA-signs any
	// caller-supplied CSR and assigns a share index with no service-layer
	// auth, so its authentication exemption must lapse the moment the
	// enrollment ceremony reaches operational (see Names.PreOperationalOnly).
	VerbOfficerRegister:    {REST: []string{"/v1/barrier/officers"}, GRPC: "BarrierOfficerRegister", QUIC: "barrier.OfficerRegister", MCP: "qrdb_barrier_officer_register", PreOperationalOnly: true},
	VerbOfficerList:        {REST: []string{"/v1/barrier/officers"}, GRPC: "BarrierOfficerList", QUIC: "barrier.OfficerList", MCP: "qrdb_barrier_officer_list"},
	VerbOfficerClaimStatus: {REST: []string{"/v1/barrier/officers/claim-status"}, GRPC: "BarrierOfficerClaimStatus", QUIC: "barrier.OfficerClaimStatus", MCP: "qrdb_barrier_officer_claim_status"},
	// The path-param claim routes (/{username}/claim/begin|complete) are
	// covered by the /v1/barrier/officers/ prefix, shared by both verbs.
	VerbShareClaimBegin:    {REST: []string{"/v1/barrier/officers/"}, GRPC: "BarrierShareClaimBegin", QUIC: "barrier.ShareClaimBegin", MCP: "qrdb_barrier_share_claim_begin"},
	VerbShareClaimComplete: {REST: []string{"/v1/barrier/officers/"}, GRPC: "BarrierShareClaimComplete", QUIC: "barrier.ShareClaimComplete", MCP: "qrdb_barrier_share_claim_complete"},

	// recovery-officer-register shares officer-register's PreOperationalOnly
	// rationale: it enrolls a recovery officer via the same
	// CSR-sign-and-assign-share flow with no service-layer auth.
	VerbRecoveryOfficerRegister:    {REST: []string{"/v1/barrier/recovery/officers"}, GRPC: "BarrierRecoveryOfficerRegister", QUIC: "barrier.RecoveryOfficerRegister", MCP: "qrdb_barrier_recovery_officer_register", PreOperationalOnly: true},
	VerbRecoveryShareClaimBegin:    {REST: []string{"/v1/barrier/recovery/officers/"}, GRPC: "BarrierRecoveryShareClaimBegin", QUIC: "barrier.RecoveryShareClaimBegin", MCP: "qrdb_barrier_recovery_share_claim_begin"},
	VerbRecoveryShareClaimComplete: {REST: []string{"/v1/barrier/recovery/officers/"}, GRPC: "BarrierRecoveryShareClaimComplete", QUIC: "barrier.RecoveryShareClaimComplete", MCP: "qrdb_barrier_recovery_share_claim_complete"},

	VerbCeremonyInitialize:        {REST: []string{"/v1/ceremony/initialize"}, GRPC: "CeremonyInitialize", QUIC: "ceremony.Initialize", MCP: "qrdb_ceremony_initialize"},
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
	VerbCeremonySubmitUnsealShare:    {REST: []string{"/v1/barrier/unseal-share"}, GRPC: "CeremonySubmitUnsealShare", QUIC: "ceremony.SubmitUnsealShare", MCP: "qrdb_ceremony_submit_unseal_share"},
	// REST serves this verb at /v1/init/enroll-external-officer, which is
	// already covered by the /v1/init/ prefix in RESTExtra. No native QUIC
	// method or MCP tool exists.
	VerbCeremonyEnrollExtOfficer: {GRPC: "CeremonyEnrollExternalOfficer"},
}

// RESTExtra lists REST-only allowlist entries that have no
// transport-neutral verb: the first-boot / bootstrap flows and the
// cluster peer key-transport receiver, all of which must operate in
// every daemon state. Entries ending in "/" are prefix matches.
var RESTExtra = []string{
	"/v1/init/",
	"/v1/bootstrap/",
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
