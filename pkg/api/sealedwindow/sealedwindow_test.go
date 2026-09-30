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
	"context"
	"strings"
	"testing"
	"time"

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

// TestRESTAllowlistContents pins the exact REST derivation: every entry is
// an anonymous verb on the boot-time sealed-window listener, so adding one
// is a security-posture change reviewed as such.
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
		"POST /v1/barrier/officers",
		"POST /v1/barrier/officers/{username}/claim/begin",
		"POST /v1/barrier/officers/{username}/claim/complete",
		"POST /v1/barrier/recovery/officers",
		"POST /v1/barrier/recovery/officers/{username}/claim/begin",
		"POST /v1/barrier/recovery/officers/{username}/claim/complete",
		"/v1/barrier/quorum/begin",
		"/v1/barrier/quorum/submit",
		"/v1/ceremony/initialize",
		"/v1/ceremony/status",
		"/v1/ceremony/sign-csr",
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
		"BarrierShamirInitialize",
		"BarrierOfficerRegister",
		"BarrierShareClaimBegin", "BarrierShareClaimComplete",
		"BarrierRecoveryOfficerRegister", "BarrierRecoveryShareClaimBegin", "BarrierRecoveryShareClaimComplete",
		"BarrierQuorumBegin", "BarrierQuorumSubmit",
		"CeremonyInitialize", "CeremonyStatus", "CeremonySignCSR",
	} {
		if _, ok := set[m]; !ok {
			t.Errorf("gRPC allowlist missing %q", m)
		}
	}
	if len(list) != 17 {
		t.Errorf("gRPC allowlist has %d entries, want 17: %v", len(list), list)
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

// TestQUICAllowlistContents pins the QUIC derivation.
func TestQUICAllowlistContents(t *testing.T) {
	set := sealedwindow.QUICAllowlist()
	for _, m := range []string{
		"admin.Health", "admin.Status",
		"barrier.Status", "barrier.Initialize", "barrier.Unseal",
		"barrier.ShamirInitialize",
		"barrier.OfficerRegister",
		"barrier.ShareClaimBegin", "barrier.ShareClaimComplete",
		"barrier.RecoveryOfficerRegister", "barrier.RecoveryShareClaimBegin", "barrier.RecoveryShareClaimComplete",
		"barrier.QuorumBegin", "barrier.QuorumSubmit",
		"ceremony.Initialize", "ceremony.Status", "ceremony.SignCSR",
	} {
		if _, ok := set[m]; !ok {
			t.Errorf("QUIC allowlist missing %q", m)
		}
	}
	if len(set) != 17 {
		t.Errorf("QUIC allowlist has %d entries, want 17", len(set))
	}
}

// TestMCPAllowlistContents verifies the MCP derivation matches the tool
// surface (no new tools were invented for verbs MCP does not expose).
func TestMCPAllowlistContents(t *testing.T) {
	set := sealedwindow.MCPAllowlist()
	for _, m := range []string{
		"qrdb_health", "qrdb_status",
		"qrdb_barrier_status", "qrdb_barrier_initialize", "qrdb_barrier_unseal",
		"qrdb_barrier_shamir_initialize",
		"qrdb_barrier_officer_register",
		"qrdb_barrier_share_claim_begin", "qrdb_barrier_share_claim_complete",
		"qrdb_barrier_recovery_officer_register",
		"qrdb_barrier_recovery_share_claim_begin", "qrdb_barrier_recovery_share_claim_complete",
		"qrdb_barrier_quorum_begin", "qrdb_barrier_quorum_submit",
		"qrdb_ceremony_initialize", "qrdb_ceremony_status", "qrdb_ceremony_sign_csr",
	} {
		if _, ok := set[m]; !ok {
			t.Errorf("MCP allowlist missing %q", m)
		}
	}
	if len(set) != 17 {
		t.Errorf("MCP allowlist has %d entries, want 17", len(set))
	}
	// shamir-verify has no MCP tool.
	for _, m := range []string{"qrdb_barrier_shamir_verify"} {
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

	// The anonymous surface trimmed from the boot-time listener: verbs the
	// service refuses while sealed, the officer list, the SO-only external
	// officer enrollment, and prefixes over subtrees with no route.
	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/barrier/officers"},
		{"DELETE", "/v1/barrier/recovery"},
		{"POST", "/v1/barrier/recovery/generate"},
		{"GET", "/v1/barrier/recovery/status"},
		{"POST", "/v1/barrier/rekey"},
		{"POST", "/v1/barrier/rekey/start"},
		{"POST", "/v1/barrier/officers/revoke"},
		{"POST", "/v1/barrier/recovery/officers/revoke"},
		{"POST", "/v1/barrier/officers/alice/claim/other"},
		{"POST", "/v1/barrier/officers/alice/claim/begin/extra"},
		{"POST", "/v1/barrier/officers//claim/begin"},
		{"POST", "/v1/barrier/officers/alice"},
		{"POST", "/v1/barrier/shamir/verify"},
		{"POST", "/v1/init/enroll-external-officer"},
		{"POST", "/v1/bootstrap/anything"},
	} {
		for _, entry := range sealedwindow.RESTAllowlist() {
			if sealedwindow.RESTEntryMatches(entry, req.method, req.path) {
				t.Errorf("REST allowlist entry %q admits %s %s", entry, req.method, req.path)
			}
		}
	}

	grpcSet := make(map[string]struct{})
	for _, m := range sealedwindow.GRPCAllowlist() {
		grpcSet[m] = struct{}{}
	}
	for _, m := range []string{
		"BarrierSeal", "BarrierShamirSealVote", "BarrierEnableEncryption", "Seal", "ShamirSealVote",
		"BarrierOfficerList", "BarrierRecoveryDelete", "BarrierRecoveryGenerate", "BarrierRecoveryStatus",
		"BarrierRekey", "BarrierRekeyStart", "BarrierOfficerRevoke", "CeremonyEnrollExternalOfficer",
	} {
		if _, ok := grpcSet[m]; ok {
			t.Errorf("gRPC allowlist must not contain %q", m)
		}
	}

	quicSet := sealedwindow.QUICAllowlist()
	for _, m := range []string{
		"barrier.Seal", "barrier.ShamirSealVote", "barrier.EnableEncryption",
		"barrier.ShamirListShares", "barrier.ShamirDeleteShare", "barrier.ShamirDeleteAllShares",
		"barrier.OfficerList", "barrier.RecoveryDelete", "barrier.RecoveryGenerate", "barrier.RecoveryStatus",
		"barrier.Rekey", "barrier.RekeyStart", "barrier.OfficerRevoke", "barrier.ShamirVerify",
	} {
		if _, ok := quicSet[m]; ok {
			t.Errorf("QUIC allowlist must not contain %q", m)
		}
	}

	mcpSet := sealedwindow.MCPAllowlist()
	for _, m := range []string{
		"qrdb_barrier_seal", "qrdb_barrier_shamir_seal_vote", "qrdb_barrier_enable_encryption",
		"qrdb_barrier_officer_list", "qrdb_barrier_recovery_delete", "qrdb_barrier_recovery_generate",
		"qrdb_barrier_recovery_status", "qrdb_barrier_rekey", "qrdb_barrier_rekey_start", "qrdb_barrier_officer_revoke",
	} {
		if _, ok := mcpSet[m]; ok {
			t.Errorf("MCP allowlist must not contain %q", m)
		}
	}
}

// TestRESTPrefixConventions verifies every REST entry is a path starting
// with "/", optionally qualified by an upper-case method, matching the
// middleware's matching rules.
func TestRESTPrefixConventions(t *testing.T) {
	for _, p := range sealedwindow.RESTAllowlist() {
		path := p
		if m, rest, qualified := strings.Cut(p, " "); qualified {
			if m == "" || m != strings.ToUpper(m) {
				t.Errorf("REST entry %q: method %q must be upper case", p, m)
			}
			path = rest
		}
		if !strings.HasPrefix(path, "/") {
			t.Errorf("REST entry %q must name a path starting with '/'", p)
		}
	}
}

// TestRESTEntryMatches covers the three entry shapes: exact, prefix, and
// method-qualified.
func TestRESTEntryMatches(t *testing.T) {
	cases := []struct {
		entry, method, path string
		want                bool
	}{
		{"/v1/barrier/unseal", "POST", "/v1/barrier/unseal", true},
		{"/v1/barrier/unseal", "GET", "/v1/barrier/unseal", true},
		{"/v1/barrier/unseal", "POST", "/v1/barrier/unseal/x", false},
		{"/v1/barrier/officers/", "POST", "/v1/barrier/officers/alice/claim/begin", true},
		{"/v1/barrier/officers/", "GET", "/v1/barrier/officers", false},
		{"POST /v1/barrier/officers", "POST", "/v1/barrier/officers", true},
		{"POST /v1/barrier/officers", "GET", "/v1/barrier/officers", false},
		{"POST /v1/barrier/officers", "POST", "/v1/barrier/officers/x", false},
		{"POST /v1/cluster/", "POST", "/v1/cluster/keydelivery", true},
		{"POST /v1/cluster/", "GET", "/v1/cluster/keydelivery", false},
		{"/v1/items/{id}/claim", "GET", "/v1/items/7/claim", true},
		{"/v1/items/{id}/claim", "GET", "/v1/items/abc/claim", true},
		{"/v1/items/{id}/claim", "GET", "/v1/items//claim", false},
		{"/v1/items/{id}/claim", "GET", "/v1/items/7/claim/", false},
		{"/v1/items/{id}/claim", "GET", "/v1/items/7/8/claim", false},
		{"/v1/items/{id}/claim", "GET", "/v1/items/7", false},
		{"/v1/items/{id}/claim", "GET", "/v1/items/{id}/claim/x", false},
		{"POST /v1/barrier/officers/{username}/claim/begin", "POST", "/v1/barrier/officers/alice/claim/begin", true},
		{"POST /v1/barrier/officers/{username}/claim/begin", "GET", "/v1/barrier/officers/alice/claim/begin", false},
	}
	for _, c := range cases {
		if got := sealedwindow.RESTEntryMatches(c.entry, c.method, c.path); got != c.want {
			t.Errorf("RESTEntryMatches(%q, %q, %q) = %v, want %v", c.entry, c.method, c.path, got, c.want)
		}
	}
}

// TestDecide pins the one admission rule every transport applies. The
// boot-time listener (listener true) must refuse everything once the
// barrier is unsealed -- allowlisted verbs included -- because it has no
// authentication; a regular listener passes everything once unsealed to
// its own authentication.
func TestDecide(t *testing.T) {
	type R = sealedwindow.Request
	cases := []struct {
		req  R
		want sealedwindow.Decision
	}{
		// Regular listener: allowlist while sealed, everything once unsealed.
		{R{Sealed: true, Allowlisted: true}, sealedwindow.Serve},
		{R{Sealed: true}, sealedwindow.RefuseSealed},
		{R{Allowlisted: true}, sealedwindow.Serve},
		{R{}, sealedwindow.Serve},
		// The regular listener leaves officer registration to its own
		// authentication, whatever the enrollment state.
		{R{Sealed: true, Allowlisted: true, PreOperationalOnly: true}, sealedwindow.Serve},
		// Boot-time listener.
		{R{Sealed: true, Allowlisted: true, Listener: true}, sealedwindow.Serve},
		{R{Sealed: true, Listener: true}, sealedwindow.RefuseSealed},
		{R{Allowlisted: true, Listener: true}, sealedwindow.RefuseClosed},
		{R{Listener: true}, sealedwindow.RefuseClosed},
		{R{Sealed: true, Allowlisted: true, PreOperationalOnly: true, Listener: true, EnrollmentOpen: true}, sealedwindow.Serve},
		{R{Sealed: true, Allowlisted: true, PreOperationalOnly: true, Listener: true}, sealedwindow.RefuseEnrollmentClosed},
		{R{Allowlisted: true, PreOperationalOnly: true, Listener: true, EnrollmentOpen: true}, sealedwindow.RefuseClosed},
	}
	for _, c := range cases {
		if got := sealedwindow.Decide(c.req); got != c.want {
			t.Errorf("Decide(%+v) = %v, want %v", c.req, got, c.want)
		}
	}
}

// TestPreOperationalOnlyDerivations: every transport's derivation names
// exactly the officer and recovery-officer registration verbs.
func TestPreOperationalOnlyDerivations(t *testing.T) {
	rest := sealedwindow.RESTPreOperationalOnly()
	if len(rest) != 2 || rest[0] != "POST /v1/barrier/officers" || rest[1] != "POST /v1/barrier/recovery/officers" {
		t.Errorf("RESTPreOperationalOnly = %v", rest)
	}
	grpc := sealedwindow.GRPCFullMethodPreOperationalOnly("qrdb.v1.QRDBService")
	for _, m := range []string{"/qrdb.v1.QRDBService/BarrierOfficerRegister", "/qrdb.v1.QRDBService/BarrierRecoveryOfficerRegister"} {
		if _, ok := grpc[m]; !ok {
			t.Errorf("GRPCFullMethodPreOperationalOnly missing %s", m)
		}
	}
	quic := sealedwindow.QUICPreOperationalOnly()
	mcp := sealedwindow.MCPPreOperationalOnly()
	for set, names := range map[string][]string{
		"quic": {"barrier.OfficerRegister", "barrier.RecoveryOfficerRegister"},
		"mcp":  {"qrdb_barrier_officer_register", "qrdb_barrier_recovery_officer_register"},
	} {
		got := map[string]map[string]struct{}{"quic": quic, "mcp": mcp}[set]
		if len(got) != len(names) {
			t.Errorf("%s derivation has %d entries, want %d", set, len(got), len(names))
		}
		for _, n := range names {
			if _, ok := got[n]; !ok {
				t.Errorf("%s derivation missing %s", set, n)
			}
		}
	}
	if len(grpc) != 2 {
		t.Errorf("GRPCFullMethodPreOperationalOnly has %d entries, want 2", len(grpc))
	}
}

// TestOrFailClosed: a missing listener probe reads as unsealed with
// enrollment closed, so a gate built from it refuses everything.
func TestOrFailClosed(t *testing.T) {
	p := sealedwindow.OrFailClosed(nil)
	if p.IsSealed() || p.EnrollmentOpen(context.Background()) {
		t.Fatal("OrFailClosed(nil) must report unsealed and enrollment closed")
	}
	type probe struct{ sealedwindow.ListenerProbe }
	given := probe{p}
	if sealedwindow.OrFailClosed(given) != given {
		t.Fatal("OrFailClosed must return a non-nil probe unchanged")
	}
}

// TestPreOperationalOnlyFlags locks the exact set of verbs whose sealed-window
// authentication exemption is gated on a pre-operational ceremony. The two
// holder registration verbs are flagged because RegisterOfficer has the CA
// issue a holder certificate for any caller-supplied CSR with no
// service-layer auth: an exemption surviving ceremony completion would let an
// unauthenticated caller enrol a holder during any restart-while-sealed.
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
		sealedwindow.VerbQuorumBegin,
		sealedwindow.VerbQuorumSubmit,
		sealedwindow.VerbBarrierStatus,
	} {
		if sealedwindow.PreOperationalOnly(v) {
			t.Errorf("unseal-flow verb %q must not be PreOperationalOnly", v)
		}
	}
}

// TestAnonymous: the mark is absent until set and survives derived contexts.
func TestAnonymous(t *testing.T) {
	ctx := context.Background()
	if sealedwindow.Anonymous(ctx) {
		t.Fatal("a plain context must not be marked anonymous")
	}
	marked := sealedwindow.WithAnonymous(ctx)
	if !sealedwindow.Anonymous(marked) {
		t.Fatal("WithAnonymous did not mark the context")
	}
	child, cancel := context.WithCancel(marked)
	defer cancel()
	if !sealedwindow.Anonymous(child) {
		t.Fatal("the mark must survive a derived context")
	}
}

// TestTransitionDerivations: the Transition verbs are exactly the ones that
// initialize or unseal the barrier, on every transport. A holder quorum
// submission can complete an unseal, so it is one.
func TestTransitionDerivations(t *testing.T) {
	rest := sealedwindow.RESTTransition()
	want := []string{
		"/v1/barrier/initialize", "/v1/barrier/unseal", "/v1/barrier/shamir/initialize",
		"/v1/barrier/quorum/submit", "/v1/ceremony/initialize",
	}
	if len(rest) != len(want) {
		t.Fatalf("RESTTransition = %v, want %v", rest, want)
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Errorf("RESTTransition[%d] = %q, want %q", i, rest[i], want[i])
		}
	}
	if got := sealedwindow.GRPCFullMethodTransition("qrdb.v1.QRDBService"); len(got) != 5 {
		t.Errorf("GRPCFullMethodTransition has %d entries, want 5", len(got))
	}
	if _, ok := sealedwindow.QUICTransition()["barrier.Unseal"]; !ok || len(sealedwindow.QUICTransition()) != 5 {
		t.Errorf("QUICTransition = %v", sealedwindow.QUICTransition())
	}
	if _, ok := sealedwindow.MCPTransition()["qrdb_barrier_unseal"]; !ok || len(sealedwindow.MCPTransition()) != 5 {
		t.Errorf("MCPTransition = %v", sealedwindow.MCPTransition())
	}
}

// TestAdmission: shared admissions run together; a transition waits for them
// and excludes new ones until it is released.
func TestAdmission(t *testing.T) {
	var a sealedwindow.Admission
	r1 := a.Admit(false)
	r2 := a.Admit(false)
	acquired := make(chan func(), 1)
	go func() { acquired <- a.Admit(true) }()
	select {
	case <-acquired:
		t.Fatal("a transition was admitted while shared admissions were held")
	case <-time.After(50 * time.Millisecond):
	}
	r1()
	r2()
	release := <-acquired
	shared := make(chan func(), 1)
	go func() { shared <- a.Admit(false) }()
	select {
	case <-shared:
		t.Fatal("a shared admission was granted while a transition held the admission")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	(<-shared)()

	if r := sealedwindow.OrFailClosed(nil).Admit(true); r == nil {
		t.Fatal("the fail-closed probe must return a release")
	} else {
		r()
	}
}
