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

package holderpb

import (
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestRegistration pins the registration conversions and the params check.
func TestRegistration(t *testing.T) {
	req, err := RegisterFromPB(&pb.BarrierOfficerRegisterRequest{
		Username: "carol", Role: "custodian", CsrPem: []byte("csr"), EncryptionKem: "dhkem-x25519", EncryptionKey: []byte{1},
		Factors: []*pb.HolderFactorEnrolment{{Name: "fido2", ParamsJson: []byte(`{"public_key":"abc"}`)}, {Name: "piv"}}, Designated: "fido2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Role != seal.RoleCustodian || len(req.Factors) != 2 || req.Factors[0].Params["public_key"] != "abc" || req.Designated != "fido2" {
		t.Errorf("RegisterFromPB = %+v", req)
	}
	_, err = RegisterFromPB(&pb.BarrierOfficerRegisterRequest{Factors: []*pb.HolderFactorEnrolment{{Name: "x", ParamsJson: []byte("[1]")}}})
	var fpe *FactorParamsError
	if !errors.As(err, &fpe) || fpe.Error() == "" || fpe.Unwrap() == nil {
		t.Errorf("params that are not an object must be refused: %v", err)
	}
	if _, err := TenantRegisterFromPB(&pb.TenantBarrierOfficerRegisterRequest{Factors: []*pb.HolderFactorEnrolment{{Name: "x", ParamsJson: []byte("nope")}}}); err == nil {
		t.Error("tenant registration must check params too")
	}
	treq, err := TenantRegisterFromPB(&pb.TenantBarrierOfficerRegisterRequest{TenantId: "acme", Username: "d", CsrPem: []byte("c")})
	if err != nil || treq.Username != "d" {
		t.Errorf("TenantRegisterFromPB = %+v, %v", treq, err)
	}
	resp := RegisterToPB(&service.BarrierOfficerRegisterResult{CertPEM: []byte("cert"), Role: seal.RoleSecurityOfficer, Factors: []string{"mtls"}})
	if string(resp.GetCertPem()) != "cert" || resp.GetRole() != "so" {
		t.Errorf("RegisterToPB = %+v", resp)
	}
	tresp := TenantRegisterToPB(&ceremony.RegisterOfficerResult{CertPEM: []byte("c2"), Role: seal.RoleCustodian})
	if string(tresp.GetCertPem()) != "c2" || tresp.GetRole() != "custodian" {
		t.Errorf("TenantRegisterToPB = %+v", tresp)
	}
}

// TestQuorumConversions pins the challenge, submission, outcome and round
// conversions.
func TestQuorumConversions(t *testing.T) {
	now := time.Now().UTC()
	st := StatusesToPB([]*ceremony.OfficerStatus{{Username: "a", ShareIndex: 1, EnrolledAt: now, ClaimedAt: now}, {Username: "b"}})
	if st[0].GetClaimedAt() == "" || st[1].GetClaimedAt() != "" {
		t.Errorf("StatusesToPB claimed = %q / %q", st[0].GetClaimedAt(), st[1].GetClaimedAt())
	}
	ch := ChallengeToPB(&ceremony.QuorumChallenge{ChallengeID: "c", Domain: seal.DomainUnseal, Op: seal.OpUnseal, Holder: "a", Nonce: []byte{1}, ExpiresAt: now})
	if ch.GetChallengeId() != "c" || ch.GetUsername() != "a" || ch.GetExpiresAt() == "" {
		t.Errorf("ChallengeToPB = %+v", ch)
	}
	sub := SubmitFromPB(&pb.BarrierQuorumSubmitRequest{Domain: "unseal", Op: "seal", Username: "a", Share: []byte("s")}, "peer")
	if sub.PeerCertThumbprint != "peer" || sub.Op != seal.OpSeal {
		t.Errorf("SubmitFromPB = %+v", sub)
	}
	tsub := TenantSubmitFromPB(&pb.TenantBarrierQuorumSubmitRequest{Op: "unseal", Username: "a"}, "")
	if tsub.Domain != seal.DomainUnseal || tsub.Holder != "a" {
		t.Errorf("TenantSubmitFromPB = %+v", tsub)
	}
	out := OutcomeToPB(&service.QuorumOutcomeInfo{Complete: true, RootToken: &service.RootTokenInfo{Token: "t", CreatedAt: now}, Rekey: &service.ShamirInitInfo{TotalShares: 3}})
	if out.GetRootToken() != "t" || out.GetTotalShares() != 3 {
		t.Errorf("OutcomeToPB = %+v", out)
	}
	r := RoundToPB(&seal.QuorumRoundInfo{Op: seal.OpRekey, NewThreshold: 2, Remove: []string{"x"}, ExpiresAt: now})
	if r.GetOp() != "rekey" || r.GetNewThreshold() != 2 || len(r.GetRemove()) != 1 {
		t.Errorf("RoundToPB = %+v", r)
	}
}

// TestCeremonyInit pins the protobuf ceremony initialization conversions.
func TestCeremonyInit(t *testing.T) {
	opts, err := CeremonyInitFromPB(&pb.CeremonyInitializeRequest{
		SoPin: "so", UserPin: "user", CredentialSealStrategy: "shamir", Threshold: 2, Policy: "designated",
		Officers: []*pb.CeremonyOfficer{{
			Username: "alice", Role: "so", CsrPem: "a",
			EncryptionKem: "x25519", EncryptionKey: []byte("pub"),
			Factors:    []*pb.HolderFactorEnrolment{{Name: "fido2"}},
			Designated: "fido2",
		}},
		Hostname: "db.example.com", Sans: []string{"db.example.com"},
	})
	if err != nil {
		t.Fatalf("CeremonyInitFromPB: %v", err)
	}
	if opts.SOPin != "so" || opts.Threshold != 2 || opts.Policy != seal.FactorPolicyDesignated || len(opts.Officers) != 1 ||
		opts.Officers[0].Role != seal.RoleSecurityOfficer || opts.Officers[0].CSRPEM != "a" || opts.Hostname != "db.example.com" {
		t.Errorf("CeremonyInitFromPB = %+v", opts)
	}
	// The rich holder fields -- EncryptionKEM/EncryptionKey/Factors/
	// Designated -- must survive the same way BarrierOfficerRegisterRequest's
	// do (go-qrdb#4578 review item 1): Initialize is the only place Shamir
	// custody over the system barrier is ever established.
	o := opts.Officers[0]
	if o.EncryptionKEM != "x25519" || string(o.EncryptionKey) != "pub" || len(o.Factors) != 1 ||
		o.Factors[0].Name != "fido2" || o.Designated != "fido2" {
		t.Errorf("CeremonyInitFromPB officer enrichment = %+v", o)
	}
	empty, err := CeremonyInitFromPB(&pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitFromPB(empty): %v", err)
	}
	if empty.Officers != nil {
		t.Errorf("no holders must stay nil: %+v", empty.Officers)
	}
	if _, err := CeremonyInitFromPB(&pb.CeremonyInitializeRequest{
		Officers: []*pb.CeremonyOfficer{{Username: "bob", Factors: []*pb.HolderFactorEnrolment{{Name: "fido2", ParamsJson: []byte("not-json")}}}},
	}); err == nil {
		t.Error("malformed factor params_json must be refused, not silently dropped")
	}
	resp := CeremonyInitToPB(&service.CeremonyInitResult{
		SPKIPin: "pin", CACertPEM: []byte("ca"), State: "initialized", ShareCount: 1,
		Holders: []service.CeremonyHolderCert{{Username: "alice", Role: seal.RoleSecurityOfficer, CertPEM: []byte("cert")}},
	})
	if resp.GetSpkiPin() != "pin" || resp.GetCaCertPem() != "ca" || resp.GetShareCount() != 1 || len(resp.GetHolders()) != 1 ||
		resp.GetHolders()[0].GetRole() != "so" || string(resp.GetHolders()[0].GetCertPem()) != "cert" {
		t.Errorf("CeremonyInitToPB = %+v", resp)
	}
}

// TestFactorParamsError_IsInvalidArgument pins that malformed factor params
// are the caller's input on every transport, including when wrapped.
func TestFactorParamsError_IsInvalidArgument(t *testing.T) {
	err := &FactorParamsError{Factor: "piv", Err: errors.New("not an object")}
	for _, e := range []error{err, &wrappedError{inner: err}} {
		if got := errclass.Classify(e); got != errclass.InvalidArgument {
			t.Fatalf("Classify(%v) = %v, want InvalidArgument", e, got)
		}
	}
}

// wrappedError hides an error behind one layer of wrapping.
type wrappedError struct{ inner error }

func (w *wrappedError) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedError) Unwrap() error { return w.inner }
