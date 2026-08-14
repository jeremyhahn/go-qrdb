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

package sealedwindow_test

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// TestTableCoversEveryVerb verifies that every canonical verb has a Table
// entry with at least one transport name, and that Table has no entries
// missing from Verbs (the ordered derivation list).
func TestTableCoversEveryVerb(t *testing.T) {
	if len(sealedwindow.Verbs) != len(sealedwindow.Table) {
		t.Fatalf("Verbs (%d) and Table (%d) disagree", len(sealedwindow.Verbs), len(sealedwindow.Table))
	}
	for _, v := range sealedwindow.Verbs {
		names, ok := sealedwindow.Table[v]
		if !ok {
			t.Fatalf("verb %q has no Table entry", v)
		}
		if len(names.REST) == 0 && names.GRPC == "" && names.QUIC == "" && names.MCP == "" {
			t.Errorf("verb %q has no transport surface at all", v)
		}
	}
}

// TestVerbsAreUnique guards against a duplicated verb silently shadowing
// another Table entry.
func TestVerbsAreUnique(t *testing.T) {
	seen := make(map[sealedwindow.Verb]struct{}, len(sealedwindow.Verbs))
	for _, v := range sealedwindow.Verbs {
		if _, dup := seen[v]; dup {
			t.Errorf("verb %q appears more than once in Verbs", v)
		}
		seen[v] = struct{}{}
	}
}

// TestRESTAllowlistContents verifies the REST derivation includes the
// unified entries (including the historically missing rekey, root-token,
// recovery-delete, and ceremony verbs), preserves the REST-only prefixes,
// and contains no duplicates.
func TestRESTAllowlistContents(t *testing.T) {
	list := sealedwindow.RESTAllowlist()
	set := make(map[string]struct{}, len(list))
	for _, p := range list {
		if _, dup := set[p]; dup {
			t.Errorf("duplicate REST allowlist entry %q", p)
		}
		set[p] = struct{}{}
	}

	want := []string{
		"/status",
		"/v1/barrier/status",
		"/v1/barrier/initialize",
		"/v1/barrier/unseal",
		"/v1/barrier/shamir/initialize",
		"/v1/barrier/shamir/unseal-share",
		"/v1/barrier/shamir/unseal-shares",
		"/v1/barrier/shamir/verify",
		"/v1/barrier/rekey",
		"/v1/barrier/recovery/generate",
		"/v1/barrier/recovery/recover",
		"/v1/barrier/recovery",
		"/v1/barrier/recovery/status",
		"/v1/barrier/root-token",
		"/v1/barrier/officers",
		"/v1/barrier/officers/claim-status",
		"/v1/barrier/officers/",
		"/v1/barrier/recovery/officers",
		"/v1/barrier/recovery/officers/",
		"/v1/ceremony/initialize",
		"/v1/ceremony/status",
		"/v1/ceremony/claim-cert/begin",
		"/v1/ceremony/claim-cert/complete",
		"/v1/ceremony/officer-share/begin", "/v1/ceremony/officer-share/complete",
		"/v1/ceremony/sign-csr",
		"/v1/ceremony/claim-share/begin",
		"/v1/ceremony/claim-share/complete",
		"/v1/barrier/unseal-share",
		"/v1/init/",
		"/v1/bootstrap/",
		"/v1/cluster/",
	}
	for _, p := range want {
		if _, ok := set[p]; !ok {
			t.Errorf("REST allowlist missing %q", p)
		}
	}
	if len(list) != len(want) {
		t.Errorf("REST allowlist has %d entries, want %d: %v", len(list), len(want), list)
	}
}

// TestGRPCAllowlistContents verifies the gRPC derivation includes the
// historically missing ceremony claim-share and submit-unseal-share
// methods and excludes the deliberate exclusions.
func TestGRPCAllowlistContents(t *testing.T) {
	list := sealedwindow.GRPCAllowlist()
	set := make(map[string]struct{}, len(list))
	for _, m := range list {
		if _, dup := set[m]; dup {
			t.Errorf("duplicate gRPC allowlist entry %q", m)
		}
		set[m] = struct{}{}
	}
	for _, m := range []string{
		"Health", "Status",
		"BarrierStatus", "BarrierInitialize", "BarrierUnseal",
		"BarrierShamirInitialize", "BarrierShamirUnsealShare", "BarrierShamirUnsealShares",
		"BarrierRekey",
		"BarrierRecoveryGenerate", "BarrierRecoveryRecover", "BarrierRecoveryDelete", "BarrierRecoveryStatus",
		"BarrierRootToken",
		"BarrierOfficerRegister", "BarrierOfficerList", "BarrierOfficerClaimStatus",
		"BarrierShareClaimBegin", "BarrierShareClaimComplete",
		"BarrierRecoveryOfficerRegister", "BarrierRecoveryShareClaimBegin", "BarrierRecoveryShareClaimComplete",
		"CeremonyInitialize", "CeremonyStatus",
		"CeremonyClaimCertBegin", "CeremonyClaimCertComplete",
		"CeremonyBeginClaimShare", "CeremonyCompleteClaimShare", "CeremonySignCSR",
		"CeremonyClaimShareBegin", "CeremonyClaimShareComplete",
		"CeremonySubmitUnsealShare", "CeremonyEnrollExternalOfficer",
	} {
		if _, ok := set[m]; !ok {
			t.Errorf("gRPC allowlist missing %q", m)
		}
	}
}

// TestGRPCFullMethodAllowlist verifies the fully-qualified derivation prefixes
// every tail with the supplied service name and stays in lockstep with the
// bare-tail GRPCAllowlist (same length, one-to-one), so a same-tail method on
// another service cannot be admitted.
func TestGRPCFullMethodAllowlist(t *testing.T) {
	const svc = "qrdb.v1.QRDBService"
	full := sealedwindow.GRPCFullMethodAllowlist(svc)
	tails := sealedwindow.GRPCAllowlist()
	if len(full) != len(tails) {
		t.Fatalf("full allowlist has %d entries, tail allowlist has %d", len(full), len(tails))
	}
	set := make(map[string]struct{}, len(full))
	for i, m := range full {
		want := "/" + svc + "/" + tails[i]
		if m != want {
			t.Errorf("entry %d = %q, want %q", i, m, want)
		}
		if _, dup := set[m]; dup {
			t.Errorf("duplicate full-method entry %q", m)
		}
		set[m] = struct{}{}
	}
	// Health/Status must be present as fully-qualified QRDBService methods and
	// NOT as a bare tail that would collide with another service.
	if _, ok := set["/qrdb.v1.QRDBService/Health"]; !ok {
		t.Error("full allowlist missing /qrdb.v1.QRDBService/Health")
	}
	if _, ok := set["Health"]; ok {
		t.Error("full allowlist must not contain bare tail Health")
	}
}

// TestQUICAllowlistContents verifies the QUIC derivation includes the
// historically missing barrier.ShamirVerify method.
func TestQUICAllowlistContents(t *testing.T) {
	set := sealedwindow.QUICAllowlist()
	for _, m := range []string{
		"admin.Health", "admin.Status",
		"barrier.Status", "barrier.Initialize", "barrier.Unseal",
		"barrier.ShamirInitialize", "barrier.ShamirUnsealShare", "barrier.ShamirUnsealShares",
		"barrier.ShamirVerify",
		"barrier.Rekey",
		"barrier.RecoveryGenerate", "barrier.RecoveryRecover", "barrier.RecoveryDelete", "barrier.RecoveryStatus",
		"barrier.RootToken",
		"barrier.OfficerRegister", "barrier.OfficerList", "barrier.OfficerClaimStatus",
		"barrier.ShareClaimBegin", "barrier.ShareClaimComplete",
		"barrier.RecoveryOfficerRegister", "barrier.RecoveryShareClaimBegin", "barrier.RecoveryShareClaimComplete",
		"ceremony.Initialize", "ceremony.Status",
		"ceremony.ClaimCertBegin", "ceremony.ClaimCertComplete",
		"ceremony.BeginClaimShare", "ceremony.CompleteClaimShare", "ceremony.SignCSR",
		"ceremony.ClaimShareBegin", "ceremony.ClaimShareComplete",
		"ceremony.SubmitUnsealShare",
	} {
		if _, ok := set[m]; !ok {
			t.Errorf("QUIC allowlist missing %q", m)
		}
	}
}

// TestMCPAllowlistContents verifies the MCP derivation matches the tool
// surface (no new tools were invented for verbs MCP does not expose).
func TestMCPAllowlistContents(t *testing.T) {
	set := sealedwindow.MCPAllowlist()
	for _, m := range []string{
		"qrdb_health", "qrdb_status",
		"qrdb_barrier_status", "qrdb_barrier_initialize", "qrdb_barrier_unseal",
		"qrdb_barrier_shamir_initialize", "qrdb_barrier_shamir_unseal_share", "qrdb_barrier_shamir_unseal_shares",
		"qrdb_barrier_rekey",
		"qrdb_barrier_recovery_generate", "qrdb_barrier_recovery_recover",
		"qrdb_barrier_recovery_delete", "qrdb_barrier_recovery_status",
		"qrdb_barrier_root_token",
		"qrdb_barrier_officer_register", "qrdb_barrier_officer_list", "qrdb_barrier_officer_claim_status",
		"qrdb_barrier_share_claim_begin", "qrdb_barrier_share_claim_complete",
		"qrdb_barrier_recovery_officer_register",
		"qrdb_barrier_recovery_share_claim_begin", "qrdb_barrier_recovery_share_claim_complete",
		"qrdb_ceremony_initialize", "qrdb_ceremony_status",
		"qrdb_ceremony_claim_cert_begin", "qrdb_ceremony_claim_cert_complete",
		"qrdb_ceremony_begin_claim_share", "qrdb_ceremony_complete_claim_share", "qrdb_ceremony_sign_csr",
		"qrdb_ceremony_claim_share_begin", "qrdb_ceremony_claim_share_complete",
		"qrdb_ceremony_submit_unseal_share",
	} {
		if _, ok := set[m]; !ok {
			t.Errorf("MCP allowlist missing %q", m)
		}
	}
	// shamir-verify and enroll-external-officer have no MCP tools.
	for _, m := range []string{"qrdb_barrier_shamir_verify", "qrdb_ceremony_enroll_external_officer"} {
		if _, ok := set[m]; ok {
			t.Errorf("MCP allowlist must not contain nonexistent tool %q", m)
		}
	}
}

// TestDeliberateExclusions verifies that seal, seal-vote,
// enable-encryption, and Shamir share-management operations are excluded
// from every transport derivation — sealing and share administration are
// only meaningful while the barrier is unsealed.
func TestDeliberateExclusions(t *testing.T) {
	restSet := make(map[string]struct{})
	for _, p := range sealedwindow.RESTAllowlist() {
		restSet[p] = struct{}{}
	}
	for _, p := range []string{
		"/v1/barrier/seal",
		"/v1/barrier/shamir/seal-vote",
		"/v1/barrier/enable-encryption",
		"/v1/ceremony/enroll-custodian",
		"/v1/ceremony/revoke-custodian",
	} {
		if _, ok := restSet[p]; ok {
			t.Errorf("REST allowlist must not contain %q", p)
		}
	}

	grpcSet := make(map[string]struct{})
	for _, m := range sealedwindow.GRPCAllowlist() {
		grpcSet[m] = struct{}{}
	}
	for _, m := range []string{"BarrierSeal", "BarrierShamirSealVote", "BarrierEnableEncryption", "Seal", "ShamirSealVote"} {
		if _, ok := grpcSet[m]; ok {
			t.Errorf("gRPC allowlist must not contain %q", m)
		}
	}

	quicSet := sealedwindow.QUICAllowlist()
	for _, m := range []string{
		"barrier.Seal", "barrier.ShamirSealVote", "barrier.EnableEncryption",
		"barrier.ShamirListShares", "barrier.ShamirDeleteShare", "barrier.ShamirDeleteAllShares",
	} {
		if _, ok := quicSet[m]; ok {
			t.Errorf("QUIC allowlist must not contain %q", m)
		}
	}

	mcpSet := sealedwindow.MCPAllowlist()
	for _, m := range []string{"qrdb_barrier_seal", "qrdb_barrier_shamir_seal_vote", "qrdb_barrier_enable_encryption"} {
		if _, ok := mcpSet[m]; ok {
			t.Errorf("MCP allowlist must not contain %q", m)
		}
	}
}

// TestRESTPrefixConventions verifies every REST prefix entry ends with "/"
// and every exact entry starts with "/", matching the middleware's
// matching rules.
func TestRESTPrefixConventions(t *testing.T) {
	for _, p := range sealedwindow.RESTAllowlist() {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("REST entry %q must start with '/'", p)
		}
	}
}

// TestPreOperationalOnlyFlags locks the exact set of verbs whose sealed-window
// authentication exemption is gated on a pre-operational ceremony. The two
// officer registration verbs are flagged because RegisterOfficer CA-signs any
// caller-supplied CSR and assigns a Shamir share index with no service-layer
// auth: an exemption surviving ceremony completion would let an
// unauthenticated caller enroll an officer during any restart-while-sealed.
//
// Flagging a NEW verb here is a security-posture change reviewed as such;
// UN-flagging either officer verb reopens the post-operational enrollment
// hole and must never happen.
func TestPreOperationalOnlyFlags(t *testing.T) {
	want := map[sealedwindow.Verb]bool{
		sealedwindow.VerbOfficerRegister:         true,
		sealedwindow.VerbRecoveryOfficerRegister: true,
	}

	for _, v := range sealedwindow.Verbs {
		got := sealedwindow.PreOperationalOnly(v)
		if got != want[v] {
			t.Errorf("PreOperationalOnly(%q) = %v, want %v", v, got, want[v])
		}
	}

	// The accessor must agree with the table field it reports.
	for v, names := range sealedwindow.Table {
		if sealedwindow.PreOperationalOnly(v) != names.PreOperationalOnly {
			t.Errorf("PreOperationalOnly(%q) disagrees with Table field", v)
		}
	}

	// The unseal path must stay state-independent: a daemon restarted after
	// a completed ceremony is operational AND sealed, and these verbs are
	// the only way back to a usable data plane.
	for _, v := range []sealedwindow.Verb{
		sealedwindow.VerbBarrierUnseal,
		sealedwindow.VerbShamirUnsealShare,
		sealedwindow.VerbShamirUnsealShares,
		sealedwindow.VerbBarrierStatus,
	} {
		if sealedwindow.PreOperationalOnly(v) {
			t.Errorf("unseal-flow verb %q must not be PreOperationalOnly", v)
		}
	}
}
