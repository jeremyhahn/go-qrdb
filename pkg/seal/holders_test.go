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

package seal

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func validRoster(t *testing.T) *HolderRoster {
	t.Helper()
	r, err := newCaptureDistributor("a", "b", "c").PrepareRoster(context.Background(), DomainUnseal, 2, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHolderRoster_ValidateAcceptsAWellFormedRoster(t *testing.T) {
	if err := validRoster(t).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHolderRoster_ValidateRefusesInconsistency(t *testing.T) {
	cases := map[string]func(r *HolderRoster){
		"nil version":         func(r *HolderRoster) { r.Version = 0 },
		"no distribution":     func(r *HolderRoster) { r.Distribution = "" },
		"unknown domain":      func(r *HolderRoster) { r.Domain = "x" },
		"unknown policy":      func(r *HolderRoster) { r.Policy = "sometimes" },
		"threshold one":       func(r *HolderRoster) { r.Threshold = 1 },
		"threshold over":      func(r *HolderRoster) { r.Threshold = 4 },
		"total mismatch":      func(r *HolderRoster) { r.Total = 4 },
		"empty username":      func(r *HolderRoster) { r.Holders[0].Username = "" },
		"duplicate username":  func(r *HolderRoster) { r.Holders[1].Username = r.Holders[0].Username },
		"index zero":          func(r *HolderRoster) { r.Holders[0].ShareIndex = 0 },
		"index over total":    func(r *HolderRoster) { r.Holders[0].ShareIndex = 9 },
		"duplicate index":     func(r *HolderRoster) { r.Holders[1].ShareIndex = r.Holders[0].ShareIndex },
		"no certificate":      func(r *HolderRoster) { r.Holders[0].CertThumbprint = "" },
		"shared certificate":  func(r *HolderRoster) { r.Holders[1].CertThumbprint = r.Holders[0].CertThumbprint },
		"no role":             func(r *HolderRoster) { r.Holders[0].Role = "" },
		"no factors":          func(r *HolderRoster) { r.Holders[0].Factors = nil },
		"undeclared designee": func(r *HolderRoster) { r.Holders[0].Designated = "piv" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := validRoster(t)
			mutate(r)
			var bad *InvalidRosterError
			if err := r.Validate(); !errors.As(err, &bad) {
				t.Fatalf("Validate = %v, want InvalidRosterError", err)
			}
		})
	}
	var bad *InvalidRosterError
	if err := (*HolderRoster)(nil).Validate(); !errors.As(err, &bad) {
		t.Fatalf("nil roster Validate = %v", err)
	}
}

// TestHolderRoster_AdmittedFactorEnforcesThePolicy is the factor-policy
// contract: designated admits exactly the designated factor, any-enrolled
// admits every enrolled one, and neither admits a factor never enrolled.
func TestHolderRoster_AdmittedFactorEnforcesThePolicy(t *testing.T) {
	r := validRoster(t)
	var notAdmitted *FactorNotAdmittedError

	if f, err := r.AdmittedFactor("a", "mtls"); err != nil || f.Name != "mtls" {
		t.Fatalf("designated factor refused: %v", err)
	}
	if f, err := r.AdmittedFactor("a", ""); err != nil || f.Name != "mtls" {
		t.Fatalf("empty factor did not select the designated one: %v", err)
	}
	if _, err := r.AdmittedFactor("a", "fido2"); !errors.As(err, &notAdmitted) {
		t.Fatalf("designated policy admitted a second factor: %v", err)
	}

	r.Policy = FactorPolicyAnyEnrolled
	if f, err := r.AdmittedFactor("a", "fido2"); err != nil || f.Name != "fido2" {
		t.Fatalf("any-enrolled refused an enrolled factor: %v", err)
	}
	if _, err := r.AdmittedFactor("a", "piv"); !errors.As(err, &notAdmitted) {
		t.Fatalf("any-enrolled admitted a factor never enrolled: %v", err)
	}
	var missing *HolderNotInRosterError
	if _, err := r.AdmittedFactor("zed", "mtls"); !errors.As(err, &missing) {
		t.Fatalf("unknown holder = %v", err)
	}
}

func TestFactorPolicy_DefaultsToDesignated(t *testing.T) {
	if FactorPolicy("").OrDefault() != FactorPolicyDesignated {
		t.Fatal("empty policy does not default to designated")
	}
	if !FactorPolicy("").Valid() || !FactorPolicyAnyEnrolled.Valid() || FactorPolicy("x").Valid() {
		t.Fatal("policy validity is wrong")
	}
	if NewHolderRoster(DomainUnseal, "", 2).Policy != FactorPolicyDesignated {
		t.Fatal("NewHolderRoster does not default the policy")
	}
}

func TestQuorumOp_ValidInItsDomainsOnly(t *testing.T) {
	valid := map[QuorumOp][]QuorumDomain{
		OpUnseal:    {DomainUnseal},
		OpSeal:      {DomainUnseal},
		OpRootToken: {DomainUnseal},
		OpRekey:     {DomainUnseal, DomainRecovery},
		OpRecover:   {DomainRecovery},
	}
	for op, domains := range valid {
		for _, d := range []QuorumDomain{DomainUnseal, DomainRecovery, "other"} {
			want := false
			for _, ok := range domains {
				want = want || ok == d
			}
			if op.ValidIn(d) != want {
				t.Errorf("%s in %s = %v, want %v", op, d, op.ValidIn(d), want)
			}
		}
	}
	if QuorumOp("x").ValidIn(DomainUnseal) {
		t.Fatal("unknown op valid")
	}
	if !RoleSecurityOfficer.Valid() || !RoleCustodian.Valid() || HolderRole("admin").Valid() {
		t.Fatal("role validity is wrong")
	}
}

// TestRosterEncoding_DigestIsOverTheStoredBytes pins that the digest is
// taken over the exact bytes stored, so decode(encode(r)) reproduces it, and
// that a changed roster changes it.
func TestRosterEncoding_DigestIsOverTheStoredBytes(t *testing.T) {
	r := validRoster(t)
	raw, digest, err := encodeRoster(r)
	if err != nil {
		t.Fatal(err)
	}
	back, digest2, err := decodeRoster(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(digest, digest2) || back.Total != r.Total {
		t.Fatal("decode did not reproduce the stored roster and digest")
	}
	r.Holders[0].CertThumbprint = "different"
	_, digest3, err := encodeRoster(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(digest, digest3) {
		t.Fatal("a changed roster kept its digest")
	}

	if _, _, err := decodeRoster(nil); !errors.Is(err, ErrHolderRosterMissing) {
		t.Fatalf("decode(nil) = %v", err)
	}
	var bad *InvalidRosterError
	if _, _, err := decodeRoster([]byte("{")); !errors.As(err, &bad) {
		t.Fatalf("decode(garbage) = %v", err)
	}
	if _, _, err := decodeRoster([]byte(`{"version":1}`)); !errors.As(err, &bad) {
		t.Fatalf("decode(invalid) = %v", err)
	}
	if _, _, err := encodeRoster(&HolderRoster{}); !errors.As(err, &bad) {
		t.Fatalf("encode(invalid) = %v", err)
	}
}

func TestHolderErrors_Messages(t *testing.T) {
	for _, err := range []error{
		&InvalidRosterError{Reason: "r"},
		&HolderNotInRosterError{Holder: "h"},
		&FactorNotAdmittedError{Holder: "h", Factor: "f", Policy: FactorPolicyDesignated},
		&HolderRevokedError{Holder: "h"},
		&HolderAlreadySubmittedError{Holder: "h"},
		&ShareIndexMismatchError{Holder: "h", Want: 2},
		&HolderAuthenticationError{Holder: "h", Err: errTestProof},
		&QuorumOpInvalidError{Domain: "d", Op: "o"},
		&RevocationBreaksQuorumError{Threshold: 3, Remaining: 2},
		&HolderCountMismatchError{Want: 3, Enrolled: 2},
	} {
		if err.Error() == "" {
			t.Fatalf("%T has an empty message", err)
		}
	}
	if !errors.Is(&HolderAlreadySubmittedError{}, ErrShamirDuplicateShare) {
		t.Fatal("HolderAlreadySubmittedError does not match ErrShamirDuplicateShare")
	}
	if !errors.Is(&RevocationBreaksQuorumError{}, ErrShamirThresholdInvalid) {
		t.Fatal("RevocationBreaksQuorumError does not match ErrShamirThresholdInvalid")
	}
	// Opaque across the transport boundary: one fixed message, and no
	// unwrap to the verifier's reason, which stays in Err for the log.
	opaque := &HolderAuthenticationError{Holder: "alice", Err: errTestProof}
	if errors.Is(opaque, errTestProof) {
		t.Fatal("HolderAuthenticationError unwraps to the verifier's reason")
	}
	if opaque.Error() != (&HolderAuthenticationError{Holder: "mallory", Err: errors.New("other")}).Error() {
		t.Fatal("HolderAuthenticationError's message varies with the holder or reason")
	}
}
