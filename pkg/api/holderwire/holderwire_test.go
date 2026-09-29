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

package holderwire

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRegistration pins the registration conversions in both directions.
func TestRegistration(t *testing.T) {
	factors := []transport.HolderFactorEnrolment{{Name: "fido2", Params: map[string]any{"k": "v"}}}
	req := RegisterFrom(&transport.OfficerRegisterRequest{
		Username: "carol", Role: "custodian", CSRPEM: "csr", EncryptionKEM: "dhkem-x25519",
		EncryptionKey: []byte{1}, Factors: factors, Designated: "fido2",
	})
	if req.Username != "carol" || req.Role != seal.RoleCustodian || string(req.CSRPEM) != "csr" ||
		req.EncryptionKEM != "dhkem-x25519" || len(req.Factors) != 1 || req.Factors[0].Name != "fido2" || req.Designated != "fido2" {
		t.Errorf("RegisterFrom = %+v", req)
	}
	treq := TenantRegisterFrom(&transport.TenantOfficerRegisterRequest{TenantID: "acme", Username: "dave", CSRPEM: "c"})
	if treq.Username != "dave" || treq.Factors != nil {
		t.Errorf("TenantRegisterFrom = %+v", treq)
	}
	resp := RegisterTo(&service.BarrierOfficerRegisterResult{CertPEM: []byte("cert"), Role: seal.RoleSecurityOfficer, Designated: "mtls", EncryptionKEM: "dhkem-p256", Factors: []string{"mtls"}})
	if resp.CertPEM != "cert" || resp.Role != "so" || resp.Designated != "mtls" || resp.EncryptionKEM != "dhkem-p256" {
		t.Errorf("RegisterTo = %+v", resp)
	}
	tresp := TenantRegisterTo(&ceremony.RegisterOfficerResult{CertPEM: []byte("c2"), Role: seal.RoleCustodian})
	if tresp.CertPEM != "c2" || tresp.Role != "custodian" {
		t.Errorf("TenantRegisterTo = %+v", tresp)
	}
}

// TestStatusesAndChallenge pins the status and challenge conversions.
func TestStatusesAndChallenge(t *testing.T) {
	now := time.Now().UTC()
	st := StatusesTo([]*ceremony.OfficerStatus{{Username: "a", Role: seal.RoleCustodian, ShareIndex: 2, Factors: []string{"mtls"}, EnrolledAt: now, SharePending: true}})
	if len(st) != 1 || st[0].Username != "a" || st[0].Role != "custodian" || st[0].ShareIndex != 2 || !st[0].SharePending || !st[0].EnrolledAt.Equal(now) {
		t.Errorf("StatusesTo = %+v", st[0])
	}
	ch := ChallengeTo(&ceremony.QuorumChallenge{ChallengeID: "c", Domain: seal.DomainUnseal, Op: seal.OpUnseal, Holder: "a", Factor: "mtls", Nonce: []byte{1}, ExpiresAt: now})
	if ch.ChallengeID != "c" || ch.Domain != "unseal" || ch.Op != "unseal" || ch.Username != "a" || ch.Factor != "mtls" {
		t.Errorf("ChallengeTo = %+v", ch)
	}
	b := BeginFrom(&transport.QuorumBeginRequest{Domain: "recovery", Op: "recover", Username: "r", Factor: "fido2"})
	if b.Domain != seal.DomainRecovery || b.Op != seal.OpRecover || b.Username != "r" || b.Factor != "fido2" {
		t.Errorf("BeginFrom = %+v", b)
	}
}

// TestSubmissions pins that the peer thumbprint comes from the transport,
// never the request, and that tenant submissions are unseal-domain.
func TestSubmissions(t *testing.T) {
	sub := SubmitFrom(&transport.QuorumSubmitRequest{Domain: "unseal", Op: "seal", Username: "a", ChallengeID: "c", Factor: "mtls", Response: []byte{1}, Share: []byte("s")}, "peer")
	if sub.PeerCertThumbprint != "peer" || sub.Op != seal.OpSeal || string(sub.Share) != "s" {
		t.Errorf("SubmitFrom = %+v", sub)
	}
	tsub := TenantSubmitFrom(&transport.TenantQuorumSubmitRequest{TenantID: "acme", Op: "unseal", Username: "a", ChallengeID: "c", Share: []byte("s")}, "")
	if tsub.Domain != seal.DomainUnseal || tsub.Holder != "a" || tsub.Proof.ChallengeID != "c" {
		t.Errorf("TenantSubmitFrom = %+v", tsub)
	}
}

// TestOutcomeAndRound pins the outcome and round conversions.
func TestOutcomeAndRound(t *testing.T) {
	now := time.Now().UTC()
	out := OutcomeTo(&service.QuorumOutcomeInfo{Domain: seal.DomainUnseal, Op: seal.OpRootToken, Required: 2, Submitted: 2, Complete: true,
		RootToken: &service.RootTokenInfo{Token: "tok", CreatedAt: now}, Rekey: &service.ShamirInitInfo{Threshold: 2, TotalShares: 3, ShareCount: 3}})
	if !out.Complete || out.RootToken != "tok" || !out.RootTokenCreatedAt.Equal(now) || out.TotalShares != 3 {
		t.Errorf("OutcomeTo = %+v", out)
	}
	bare := OutcomeTo(&service.QuorumOutcomeInfo{Required: 2, Submitted: 1})
	if bare.RootToken != "" || bare.ShareCount != 0 {
		t.Errorf("OutcomeTo(bare) = %+v", bare)
	}
	r := RoundTo(&seal.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Required: 2, NewThreshold: 2, Policy: seal.FactorPolicyAnyEnrolled, Remove: []string{"x"}})
	if r.Op != "rekey" || r.NewThreshold != 2 || r.Policy != "any-enrolled" || len(r.Remove) != 1 {
		t.Errorf("RoundTo = %+v", r)
	}
	if FactorsFrom(nil) != nil {
		t.Error("no factors converts to nil")
	}
}

// TestCeremonyInit pins the ceremony initialization conversions, including
// each holder's role and the factor policy.
func TestCeremonyInit(t *testing.T) {
	opts := CeremonyInitFrom(&transport.CeremonyInitializeRequest{
		SOPin: "so", UserPin: "user", CredentialSealStrategy: "shamir", Threshold: 2, Policy: "any-enrolled",
		Officers: []transport.CeremonyOfficerRequest{{Username: "alice", Role: "so", CSRPEM: "a"}, {Username: "carol", Role: "custodian", CSRPEM: "c"}},
		Hostname: "db.example.com", SANs: []string{"db.example.com"},
	})
	if opts.SOPin != "so" || opts.UserPin != "user" || opts.CredentialSealStrategy != "shamir" || opts.Threshold != 2 ||
		opts.Policy != seal.FactorPolicyAnyEnrolled || opts.Hostname != "db.example.com" || len(opts.SANs) != 1 {
		t.Errorf("CeremonyInitFrom = %+v", opts)
	}
	if len(opts.Officers) != 2 || opts.Officers[0].Role != seal.RoleSecurityOfficer || opts.Officers[1].Role != seal.RoleCustodian || opts.Officers[1].CSRPEM != "c" {
		t.Errorf("holders = %+v", opts.Officers)
	}
	if empty := CeremonyInitFrom(&transport.CeremonyInitializeRequest{}); empty.Officers != nil {
		t.Errorf("no holders must stay nil so configured holders apply: %+v", empty.Officers)
	}

	resp := CeremonyInitTo(&service.CeremonyInitResult{
		SPKIPin: "pin", CACertPEM: []byte("ca"), TLSCertPEM: []byte("tls"), State: "initialized", ShareCount: 2,
		Holders: []service.CeremonyHolderCert{{Username: "carol", Role: seal.RoleCustodian, CertPEM: []byte("cert")}},
	})
	if resp.SPKIPin != "pin" || resp.CACertPEM != "ca" || resp.TLSCertPEM != "tls" || resp.State != "initialized" || resp.ShareCount != 2 {
		t.Errorf("CeremonyInitTo = %+v", resp)
	}
	if len(resp.Holders) != 1 || resp.Holders[0].Role != "custodian" || resp.Holders[0].CertPEM != "cert" {
		t.Errorf("holders = %+v", resp.Holders)
	}
}
