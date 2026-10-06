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

package holderconv

import (
	"errors"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTenantBarrierRosterToPB_Empty asserts an empty roster encodes to nil,
// matching holderwire.TenantBarrierRosterFrom's empty-input shortcut on the
// JSON-carrying transports.
// TestFactorParamsError pins the error's message and Unwrap.
func TestFactorParamsError(t *testing.T) {
	cause := errors.New("boom")
	err := &FactorParamsError{Factor: "fido2", Err: cause}
	if err.Error() != "holderconv: factor fido2 params: boom" {
		t.Errorf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Errorf("Unwrap() did not expose %v", cause)
	}
}

// TestFactorsToPB_EmptyNilAndParams covers factorsToPB's three shapes: no
// factors (empty, non-nil slice -- callers range over it unconditionally),
// a factor with no params (nil ParamsJson, not an empty-object encoding),
// and a factor with params (JSON-encoded).
func TestFactorsToPB_EmptyNilAndParams(t *testing.T) {
	empty, err := factorsToPB(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("factorsToPB(nil) = (%+v, %v), want (empty non-nil slice, nil)", empty, err)
	}

	out, err := factorsToPB([]transport.HolderFactorEnrolment{
		{Name: "mtls"},
		{Name: "fido2", Params: map[string]any{"k": "v"}},
	})
	if err != nil {
		t.Fatalf("factorsToPB: %v", err)
	}
	if len(out) != 2 || out[0].GetName() != "mtls" || out[0].GetParamsJson() != nil {
		t.Errorf("factorsToPB()[0] = %+v, want Name=mtls ParamsJson=nil", out[0])
	}
	if out[1].GetName() != "fido2" || string(out[1].GetParamsJson()) != `{"k":"v"}` {
		t.Errorf("factorsToPB()[1] = %+v", out[1])
	}
}

// TestFactorsToPB_Error asserts an unencodable params map is refused via
// FactorParamsError, naming the offending factor.
func TestFactorsToPB_Error(t *testing.T) {
	_, err := factorsToPB([]transport.HolderFactorEnrolment{
		{Name: "fido2", Params: map[string]any{"bad": make(chan int)}},
	})
	var fpErr *FactorParamsError
	if !errors.As(err, &fpErr) || fpErr.Factor != "fido2" {
		t.Fatalf("factorsToPB(unencodable) = %v, want *FactorParamsError{Factor: fido2}", err)
	}
}

// TestParseTime covers parseTime's three outcomes directly: empty input,
// a malformed timestamp, and a valid RFC3339Nano one.
func TestParseTime(t *testing.T) {
	if got := parseTime(""); !got.IsZero() {
		t.Errorf("parseTime(\"\") = %v, want zero", got)
	}
	if got := parseTime("not-a-time"); !got.IsZero() {
		t.Errorf("parseTime(malformed) = %v, want zero", got)
	}
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := parseTime(want.Format(time.RFC3339Nano)); !got.Equal(want) {
		t.Errorf("parseTime(valid) = %v, want %v", got, want)
	}
}

// TestRegisterToPB covers the system-barrier registration encode: every
// field, including factors, and the error path propagated from
// factorsToPB.
func TestRegisterToPB(t *testing.T) {
	req := &transport.OfficerRegisterRequest{
		Username: "alice", Role: "so", CSRPEM: "csr-a",
		EncryptionKEM: "dhkem-p256", EncryptionKey: []byte{9},
		Factors:    []transport.HolderFactorEnrolment{{Name: "fido2"}},
		Designated: "fido2",
	}
	out, err := RegisterToPB(req)
	if err != nil {
		t.Fatalf("RegisterToPB: %v", err)
	}
	if out.GetUsername() != "alice" || out.GetRole() != "so" || string(out.GetCsrPem()) != "csr-a" ||
		out.GetEncryptionKem() != "dhkem-p256" || len(out.GetEncryptionKey()) != 1 ||
		len(out.GetFactors()) != 1 || out.GetDesignated() != "fido2" {
		t.Errorf("RegisterToPB = %+v", out)
	}

	if _, err := RegisterToPB(&transport.OfficerRegisterRequest{
		Factors: []transport.HolderFactorEnrolment{{Name: "f", Params: map[string]any{"x": make(chan int)}}},
	}); err == nil {
		t.Error("RegisterToPB must propagate a malformed factor params error")
	}
}

// TestTenantRegisterToPB mirrors TestRegisterToPB for the tenant-scoped
// request, which additionally carries TenantID.
func TestTenantRegisterToPB(t *testing.T) {
	req := &transport.TenantOfficerRegisterRequest{
		TenantID: "acme", Username: "bob", Role: "custodian", CSRPEM: "csr-b",
	}
	out, err := TenantRegisterToPB(req)
	if err != nil {
		t.Fatalf("TenantRegisterToPB: %v", err)
	}
	if out.GetTenantId() != "acme" || out.GetUsername() != "bob" || out.GetRole() != "custodian" || string(out.GetCsrPem()) != "csr-b" {
		t.Errorf("TenantRegisterToPB = %+v", out)
	}

	if _, err := TenantRegisterToPB(&transport.TenantOfficerRegisterRequest{
		Factors: []transport.HolderFactorEnrolment{{Name: "f", Params: map[string]any{"x": make(chan int)}}},
	}); err == nil {
		t.Error("TenantRegisterToPB must propagate a malformed factor params error")
	}
}

// TestRegisterFromPB_And_TenantRegisterFromPB decodes a registration result
// on both the system and tenant paths; they decode the identical shape.
func TestRegisterFromPB_And_TenantRegisterFromPB(t *testing.T) {
	resp := RegisterFromPB(&pb.BarrierOfficerRegisterResponse{
		CertPem: []byte("cert"), Role: "so", Designated: "mtls", EncryptionKem: "dhkem-p256", Factors: []string{"mtls"},
	})
	if resp.CertPEM != "cert" || resp.Role != "so" || resp.Designated != "mtls" || resp.EncryptionKEM != "dhkem-p256" || len(resp.Factors) != 1 {
		t.Errorf("RegisterFromPB = %+v", resp)
	}

	tresp := TenantRegisterFromPB(&pb.TenantBarrierOfficerRegisterResponse{
		CertPem: []byte("cert2"), Role: "custodian",
	})
	if tresp.CertPEM != "cert2" || tresp.Role != "custodian" {
		t.Errorf("TenantRegisterFromPB = %+v", tresp)
	}
}

// TestStatusesFromPB covers the empty case (empty, non-nil slice) and a
// full decode including both parseTime branches (a set EnrolledAt and an
// empty, still-pending ClaimedAt).
func TestStatusesFromPB(t *testing.T) {
	if got := StatusesFromPB(nil); got == nil || len(got) != 0 {
		t.Errorf("StatusesFromPB(nil) = %+v, want empty non-nil slice", got)
	}

	enrolled := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out := StatusesFromPB([]*pb.OfficerStatus{{
		Username: "alice", Role: "so", CertThumbprint: "tp", ShareIndex: 1,
		Designated: "mtls", Factors: []string{"mtls"}, EncryptionKem: "dhkem-p256",
		EnrolledAt: enrolled.Format(time.RFC3339Nano), ClaimedAt: "", SharePending: true,
	}})
	if len(out) != 1 {
		t.Fatalf("StatusesFromPB() = %d entries, want 1", len(out))
	}
	o := out[0]
	if o.Username != "alice" || o.ShareIndex != 1 || o.CertThumbprint != "tp" || !o.EnrolledAt.Equal(enrolled) ||
		!o.ClaimedAt.IsZero() || !o.SharePending {
		t.Errorf("StatusesFromPB()[0] = %+v", o)
	}
}

// TestClaimBeginFromPB covers both the set and empty expires_at branches.
func TestClaimBeginFromPB(t *testing.T) {
	if got := ClaimBeginFromPB("nonce", ""); got.NonceHex != "nonce" || !got.ExpiresAt.IsZero() {
		t.Errorf("ClaimBeginFromPB(no expiry) = %+v", got)
	}
	expires := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got := ClaimBeginFromPB("nonce2", expires.Format(time.RFC3339Nano))
	if got.NonceHex != "nonce2" || !got.ExpiresAt.Equal(expires) {
		t.Errorf("ClaimBeginFromPB(expiry) = %+v, want ExpiresAt=%v", got, expires)
	}
}

// TestBeginToPB_And_TenantBeginToPB covers both quorum-begin encoders.
func TestBeginToPB_And_TenantBeginToPB(t *testing.T) {
	out := BeginToPB(&transport.QuorumBeginRequest{Domain: "unseal", Op: "unseal", Username: "alice", Factor: "mtls"})
	if out.GetDomain() != "unseal" || out.GetOp() != "unseal" || out.GetUsername() != "alice" || out.GetFactor() != "mtls" {
		t.Errorf("BeginToPB = %+v", out)
	}
	tout := TenantBeginToPB(&transport.TenantQuorumBeginRequest{TenantID: "acme", Op: "seal", Username: "bob"})
	if tout.GetTenantId() != "acme" || tout.GetOp() != "seal" || tout.GetUsername() != "bob" {
		t.Errorf("TenantBeginToPB = %+v", tout)
	}
}

// TestChallengeFromPB decodes a full quorum challenge, including the
// parseTime path over ExpiresAt.
func TestChallengeFromPB(t *testing.T) {
	expires := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out := ChallengeFromPB(&pb.BarrierQuorumChallenge{
		ChallengeId: "c1", Domain: "unseal", Op: "unseal", Username: "alice", Factor: "mtls",
		Nonce: []byte{1, 2, 3}, MethodData: map[string]string{"purpose": "unseal"},
		ExpiresAt: expires.Format(time.RFC3339Nano),
	})
	if out.ChallengeID != "c1" || out.Domain != "unseal" || out.Op != "unseal" || out.Username != "alice" ||
		out.Factor != "mtls" || len(out.Nonce) != 3 || out.MethodData["purpose"] != "unseal" || !out.ExpiresAt.Equal(expires) {
		t.Errorf("ChallengeFromPB = %+v", out)
	}
}

// TestSubmitToPB_And_TenantSubmitToPB covers both quorum-submit encoders.
func TestSubmitToPB_And_TenantSubmitToPB(t *testing.T) {
	out := SubmitToPB(&transport.QuorumSubmitRequest{
		Domain: "unseal", Op: "unseal", Username: "alice", ChallengeID: "c1", Factor: "mtls",
		Response: []byte("r"), ResponseMetadata: map[string]string{"m": "v"}, Share: []byte("s"),
	})
	if out.GetDomain() != "unseal" || out.GetChallengeId() != "c1" || string(out.GetResponse()) != "r" ||
		out.GetResponseMetadata()["m"] != "v" || string(out.GetShare()) != "s" {
		t.Errorf("SubmitToPB = %+v", out)
	}
	tout := TenantSubmitToPB(&transport.TenantQuorumSubmitRequest{
		TenantID: "acme", Op: "seal", Username: "bob", ChallengeID: "c2", Response: []byte("r2"), Share: []byte("s2"),
	})
	if tout.GetTenantId() != "acme" || tout.GetChallengeId() != "c2" || string(tout.GetResponse()) != "r2" || string(tout.GetShare()) != "s2" {
		t.Errorf("TenantSubmitToPB = %+v", tout)
	}
}

// TestOutcomeFromPB decodes a completed-round outcome, including the
// parseTime path over RootTokenCreatedAt.
func TestOutcomeFromPB(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out := OutcomeFromPB(&pb.BarrierQuorumOutcome{
		Domain: "unseal", Op: "unseal", Required: 2, Submitted: 2, Complete: true,
		RootToken: "tok", RootTokenCreatedAt: created.Format(time.RFC3339Nano),
		Threshold: 2, TotalShares: 3, ShareCount: 3,
	})
	if out.Required != 2 || out.Submitted != 2 || !out.Complete || out.RootToken != "tok" ||
		!out.RootTokenCreatedAt.Equal(created) || out.Threshold != 2 || out.TotalShares != 3 || out.ShareCount != 3 {
		t.Errorf("OutcomeFromPB = %+v", out)
	}
}

// TestRoundFromPB decodes an open round, including the parseTime path over
// ExpiresAt.
func TestRoundFromPB(t *testing.T) {
	expires := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out := RoundFromPB(&pb.BarrierQuorumRound{
		Domain: "unseal", Op: "rekey", Required: 2, Submitted: 1, ExpiresAt: expires.Format(time.RFC3339Nano),
		NewThreshold: 3, Policy: "designated", Remove: []string{"carol"},
	})
	if out.Required != 2 || out.Submitted != 1 || !out.ExpiresAt.Equal(expires) || out.NewThreshold != 3 ||
		out.Policy != "designated" || len(out.Remove) != 1 || out.Remove[0] != "carol" {
		t.Errorf("RoundFromPB = %+v", out)
	}
}

// TestRekeyRevokeEncoders covers the four rekey/revoke encoders (system and
// tenant scope).
func TestRekeyRevokeEncoders(t *testing.T) {
	r := RekeyToPB(&transport.RekeyStartRequest{Domain: "unseal", Threshold: 2, Policy: "designated", SOPin: "pin"})
	if r.GetDomain() != "unseal" || r.GetThreshold() != 2 || r.GetPolicy() != "designated" || r.GetSoPin() != "pin" {
		t.Errorf("RekeyToPB = %+v", r)
	}
	rv := RevokeToPB(&transport.OfficerRevokeRequest{Domain: "unseal", Username: "alice", Threshold: 2, SOPin: "pin"})
	if rv.GetDomain() != "unseal" || rv.GetUsername() != "alice" || rv.GetThreshold() != 2 || rv.GetSoPin() != "pin" {
		t.Errorf("RevokeToPB = %+v", rv)
	}
	tr := TenantRekeyToPB(&transport.TenantRekeyStartRequest{TenantID: "acme", Threshold: 2, Policy: "designated"})
	if tr.GetTenantId() != "acme" || tr.GetThreshold() != 2 || tr.GetPolicy() != "designated" {
		t.Errorf("TenantRekeyToPB = %+v", tr)
	}
	trv := TenantRevokeToPB(&transport.TenantOfficerRevokeRequest{TenantID: "acme", Username: "bob", Threshold: 2})
	if trv.GetTenantId() != "acme" || trv.GetUsername() != "bob" || trv.GetThreshold() != 2 {
		t.Errorf("TenantRevokeToPB = %+v", trv)
	}
}

// TestCeremonyInitToPB covers the system ceremony's own inline-officer
// encode: every field, the officer-factor error path, and the empty
// (zero-officer) shape.
func TestCeremonyInitToPB(t *testing.T) {
	req := &transport.CeremonyInitializeRequest{
		SOPin: "so", UserPin: "user", CredentialSealStrategy: "shamir", Threshold: 2, Policy: "designated",
		Officers: []transport.CeremonyOfficerRequest{{
			Username: "alice", Role: "so", CSRPEM: "a", EncryptionKEM: "x25519", EncryptionKey: []byte("pub"),
			Factors: []transport.HolderFactorEnrolment{{Name: "fido2"}}, Designated: "fido2",
		}},
		Hostname: "db.example.com", SANs: []string{"db.example.com"},
		AdminCSRPEM: "csr", AdminGenerate: false, Secret: []byte("pw"),
	}
	out, err := CeremonyInitToPB(req)
	if err != nil {
		t.Fatalf("CeremonyInitToPB: %v", err)
	}
	if out.GetSoPin() != "so" || out.GetThreshold() != 2 || out.GetPolicy() != "designated" || len(out.GetOfficers()) != 1 ||
		out.GetOfficers()[0].GetUsername() != "alice" || out.GetHostname() != "db.example.com" ||
		len(out.GetSans()) != 1 || string(out.GetSecret()) != "pw" {
		t.Errorf("CeremonyInitToPB = %+v", out)
	}

	empty, err := CeremonyInitToPB(&transport.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitToPB(empty): %v", err)
	}
	if len(empty.GetOfficers()) != 0 {
		t.Errorf("CeremonyInitToPB(empty).Officers = %+v, want empty", empty.GetOfficers())
	}

	if _, err := CeremonyInitToPB(&transport.CeremonyInitializeRequest{
		Officers: []transport.CeremonyOfficerRequest{{Username: "bob", Factors: []transport.HolderFactorEnrolment{
			{Name: "fido2", Params: map[string]any{"x": make(chan int)}},
		}}},
	}); err == nil {
		t.Error("CeremonyInitToPB must propagate a malformed officer factor params error")
	}
}

// TestCeremonyInitFromPB covers the decode side: a full result with
// holders, and the empty (zero-holder) shape.
func TestCeremonyInitFromPB(t *testing.T) {
	out := CeremonyInitFromPB(&pb.CeremonyInitializeResponse{
		SpkiPin: "pin", CaCertPem: "ca", TlsCertPem: "tls", State: "operational", ShareCount: 2,
		Holders:               []*pb.CeremonyHolderCert{{Username: "alice", Role: "so", CertPem: []byte("cert")}},
		AdminCertPem:          "admin-cert",
		AdminKeyPem:           []byte("admin-key"),
		AdminCredentialSource: "generated",
	})
	if out.SPKIPin != "pin" || out.CACertPEM != "ca" || out.TLSCertPEM != "tls" || out.State != "operational" ||
		out.ShareCount != 2 || len(out.Holders) != 1 || out.Holders[0].Username != "alice" ||
		out.AdminCertPEM != "admin-cert" || string(out.AdminKeyPEM) != "admin-key" || out.AdminCredentialSource != "generated" {
		t.Errorf("CeremonyInitFromPB = %+v", out)
	}

	empty := CeremonyInitFromPB(&pb.CeremonyInitializeResponse{})
	if len(empty.Holders) != 0 {
		t.Errorf("CeremonyInitFromPB(empty).Holders = %+v, want empty", empty.Holders)
	}
}

func TestTenantBarrierRosterToPB_Empty(t *testing.T) {
	out, err := TenantBarrierRosterToPB(nil)
	if err != nil || out != nil {
		t.Errorf("TenantBarrierRosterToPB(nil) = (%v, %v), want (nil, nil)", out, err)
	}
}

// TestTenantBarrierRosterToPB_Success encodes a two-officer roster and
// asserts every field, including a factor enrolment, survives the
// conversion.
func TestTenantBarrierRosterToPB_Success(t *testing.T) {
	officers := []transport.OfficerRegisterRequest{
		{
			Username: "alice", Role: "so", CSRPEM: "csr-a",
			EncryptionKEM: "dhkem-p256", EncryptionKey: []byte{1, 2},
			Factors:    []transport.HolderFactorEnrolment{{Name: "fido2", Params: map[string]any{"k": "v"}}},
			Designated: "fido2",
		},
		{Username: "bob", Role: "custodian", CSRPEM: "csr-b"},
	}
	out, err := TenantBarrierRosterToPB(officers)
	if err != nil {
		t.Fatalf("TenantBarrierRosterToPB: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("TenantBarrierRosterToPB() = %d entries, want 2", len(out))
	}
	if out[0].GetUsername() != "alice" || out[0].GetRole() != "so" || string(out[0].GetCsrPem()) != "csr-a" ||
		out[0].GetEncryptionKem() != "dhkem-p256" || len(out[0].GetEncryptionKey()) != 2 ||
		len(out[0].GetFactors()) != 1 || out[0].GetFactors()[0].GetName() != "fido2" || out[0].GetDesignated() != "fido2" {
		t.Errorf("TenantBarrierRosterToPB()[0] = %+v", out[0])
	}
	if out[1].GetUsername() != "bob" || out[1].GetRole() != "custodian" || string(out[1].GetCsrPem()) != "csr-b" {
		t.Errorf("TenantBarrierRosterToPB()[1] = %+v", out[1])
	}
}

// TestTenantBarrierRosterToPB_Error asserts an unencodable factor params
// map is refused via FactorParamsError, matching factorsToPB's own
// contract, rather than silently dropping the roster entry.
func TestTenantBarrierRosterToPB_Error(t *testing.T) {
	_, err := TenantBarrierRosterToPB([]transport.OfficerRegisterRequest{
		{Username: "bob", Factors: []transport.HolderFactorEnrolment{{Name: "fido2", Params: map[string]any{"x": make(chan int)}}}},
	})
	var fpErr *FactorParamsError
	if !errors.As(err, &fpErr) || fpErr.Factor != "fido2" {
		t.Fatalf("TenantBarrierRosterToPB(unencodable) = %v, want *FactorParamsError{Factor: fido2}", err)
	}
}

// TestTenantBarrierHoldersFromPB_Empty asserts an empty holder list decodes
// to nil.
func TestTenantBarrierHoldersFromPB_Empty(t *testing.T) {
	if got := TenantBarrierHoldersFromPB(nil); got != nil {
		t.Errorf("TenantBarrierHoldersFromPB(nil) = %+v, want nil", got)
	}
}

// TestTenantBarrierHoldersFromPB_Success decodes the certificates an inline
// roster's initialization issued, in order.
func TestTenantBarrierHoldersFromPB_Success(t *testing.T) {
	in := []*pb.CeremonyHolderCert{
		{Username: "alice", Role: "so", CertPem: []byte("cert-a")},
		{Username: "bob", Role: "custodian", CertPem: []byte("cert-b")},
	}
	out := TenantBarrierHoldersFromPB(in)
	if len(out) != 2 {
		t.Fatalf("TenantBarrierHoldersFromPB() = %d entries, want 2", len(out))
	}
	if out[0].Username != "alice" || out[0].Role != "so" || out[0].CertPEM != "cert-a" {
		t.Errorf("TenantBarrierHoldersFromPB()[0] = %+v", out[0])
	}
	if out[1].Username != "bob" || out[1].Role != "custodian" || out[1].CertPEM != "cert-b" {
		t.Errorf("TenantBarrierHoldersFromPB()[1] = %+v", out[1])
	}
}
