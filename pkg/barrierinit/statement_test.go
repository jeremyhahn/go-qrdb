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

package barrierinit

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// wantReason asserts err is an *InitStatementError for reason.
func wantReason(t *testing.T, err error, reason Reason) {
	t.Helper()
	var se *InitStatementError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *InitStatementError(%q)", err, err, reason)
	}
	if se.Reason != reason {
		t.Fatalf("reason = %q, want %q (err %v)", se.Reason, reason, err)
	}
	if !strings.Contains(se.Error(), string(reason)) {
		t.Fatalf("Error() = %q does not name the reason", se.Error())
	}
}

func TestVerify_FirstAdminSignedEpochOneVerifies(t *testing.T) {
	admin := newKey(t)
	s := signed(t, admin, unsigned(1, "kms://acme/key"))
	if err := Verify(s, Anchors{FirstAdmin: thumbprint(t, admin.Public())}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	spki, _ := x509.MarshalPKIXPublicKey(admin.Public())
	if !bytes.Equal(s.SignerSPKI, spki) {
		t.Fatal("Sign did not record the signer's SPKI")
	}
}

func TestSignTenantInit_EncodesTheTenantsEpochOneStatement(t *testing.T) {
	admin := newKey(t)
	anchors := Anchors{FirstAdmin: thumbprint(t, admin.Public())}
	for strategy, want := range map[string]seal.StrategyID{
		"":     seal.StrategyPassphrase, // an unnamed strategy, as the node reads it
		"none": seal.StrategyNone,
	} {
		raw, err := SignTenantInit(admin, "acme", strategy, testNow)
		if err != nil {
			t.Fatalf("SignTenantInit(%q): %v", strategy, err)
		}
		s, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", strategy, err)
		}
		if s.Barrier != clusterseal.TenantBarrier("acme") || s.Epoch != 1 || s.StrategyID != want || s.KeyRef != "" {
			t.Fatalf("statement for %q = %+v, want acme's epoch-1 %q statement with no key reference", strategy, s, want)
		}
		if err := Verify(s, anchors); err != nil {
			t.Fatalf("Verify(%q): %v", strategy, err)
		}
	}
}

// A Shamir statement signs the split it completes, so one naming no roster
// or no check value is malformed: SignTenantInit has neither to give.
func TestSignTenantInit_AShamirStatementNeedsItsSplit(t *testing.T) {
	_, err := SignTenantInit(newKey(t), "acme", "shamir", testNow)
	wantReason(t, err, ReasonMalformed)
	for name, s := range map[string]InitStatement{
		"no roster":      {Roster: nil, UnsealKeyCheck: []byte("check")},
		"no check value": {Roster: []byte("roster"), UnsealKeyCheck: nil},
	} {
		t.Run(name, func(t *testing.T) {
			hash := sha256.Sum256(s.Roster)
			s.Barrier, s.Epoch, s.StrategyID, s.Threshold, s.RosterHash = clusterseal.TenantBarrier("acme"), 1, seal.StrategyShamir, 2, hash[:]
			_, err := Sign(newKey(t), s)
			wantReason(t, err, ReasonMalformed)
		})
	}
}

func TestSignTenantShamirInit_EncodesTheSplit(t *testing.T) {
	admin := newKey(t)
	raw, err := SignTenantShamirInit(admin, "acme", 2, []byte("roster"), []byte("check"), testNow)
	if err != nil {
		t.Fatalf("SignTenantShamirInit: %v", err)
	}
	s, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	hash := sha256.Sum256([]byte("roster"))
	if s.StrategyID != seal.StrategyShamir || s.Epoch != 1 || s.Threshold != 2 ||
		!bytes.Equal(s.RosterHash, hash[:]) || string(s.Roster) != "roster" || string(s.UnsealKeyCheck) != "check" {
		t.Fatalf("statement = %+v, want acme's epoch-1 shamir statement for the split", s)
	}
	if err := Verify(s, Anchors{FirstAdmin: thumbprint(t, admin.Public())}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if raw, err := SignTenantShamirInit(nil, "acme", 2, []byte("roster"), []byte("check"), testNow); raw != nil || err != nil {
		t.Fatalf("with no signer = %q, %v, want no statement", raw, err)
	}
}

func TestSignTenantInit_NilSignerSignsNothing(t *testing.T) {
	raw, err := SignTenantInit(nil, "acme", "none", testNow)
	if err != nil || raw != nil {
		t.Fatalf("SignTenantInit(nil) = %v, %v; want nil, nil", raw, err)
	}
}

func TestSignTenantInit_RefusesAnUnencodableStatement(t *testing.T) {
	_, err := SignTenantInit(newKey(t), "acme", strings.Repeat("s", MaxFieldLength+1), testNow)
	wantReason(t, err, ReasonMalformed)
}

// The red test of PN-2: a statement signed by any key but the first admin's is
// refused, so a statement the platform or one compromised node writes fails on
// every honest node.
func TestVerify_SignedByAnotherKeyIsRefused(t *testing.T) {
	admin, other := newKey(t), newKey(t)
	s := signed(t, other, unsigned(1, ""))
	wantReason(t, Verify(s, Anchors{FirstAdmin: thumbprint(t, admin.Public())}), ReasonWrongSigner)
}

func TestVerify_SPKISwappedToTheAdminsFailsTheSignature(t *testing.T) {
	admin, other := newKey(t), newKey(t)
	s := signed(t, other, unsigned(1, ""))
	s.SignerSPKI, _ = x509.MarshalPKIXPublicKey(admin.Public())
	wantReason(t, Verify(s, Anchors{FirstAdmin: thumbprint(t, admin.Public())}), ReasonBadSignature)
}

func TestVerify_EveryFieldIsCovered(t *testing.T) {
	admin := newKey(t)
	anchors := Anchors{FirstAdmin: thumbprint(t, admin.Public())}
	tamper := map[string]func(*InitStatement){
		"barrier":   func(s *InitStatement) { s.Barrier = clusterseal.TenantBarrier("globex") },
		"issued at": func(s *InitStatement) { s.IssuedAt-- },
		"strategy":  func(s *InitStatement) { s.StrategyID = "system-barrier" },
		"key ref":   func(s *InitStatement) { s.KeyRef = "kms://platform/key" },
		"threshold": func(s *InitStatement) { s.Threshold = 3 },
		"roster":    func(s *InitStatement) { s.RosterHash = []byte("roster") },
		"salt":      func(s *InitStatement) { s.VerifierSalt = []byte("another-salt-xx") },
		"check":     func(s *InitStatement) { s.UnsealKeyCheck = []byte("check") },
		"signature": func(s *InitStatement) { s.Signature[len(s.Signature)-1] ^= 1 },
	}
	for name, mutate := range tamper {
		t.Run(name, func(t *testing.T) {
			s := signed(t, admin, unsigned(1, "kms://acme/key"))
			mutate(&s)
			wantReason(t, Verify(s, anchors), ReasonBadSignature)
		})
	}
}

func TestVerify_LaterEpochChainsToTheOwnersCA(t *testing.T) {
	ca, admin := newCA(t), newKey(t)
	s := unsigned(2, "")
	s.SignerCertificate = ca.issue(t, admin.Public())
	s = signed(t, admin, s)
	if err := Verify(s, Anchors{CA: ca.pool, At: testNow}); err != nil {
		t.Fatalf("Verify(epoch 2): %v", err)
	}
	// The first admin's thumbprint is not an anchor for a later epoch.
	wantReason(t, Verify(s, Anchors{FirstAdmin: thumbprint(t, admin.Public()), At: testNow}), ReasonNoAnchor)
}

func TestVerify_LaterEpochRefusals(t *testing.T) {
	ca, rogue, admin, other := newCA(t), newCA(t), newKey(t), newKey(t)
	anchors := Anchors{CA: ca.pool, At: testNow}

	t.Run("certificate from another CA", func(t *testing.T) {
		s := unsigned(2, "")
		s.SignerCertificate = rogue.issue(t, admin.Public())
		wantReason(t, Verify(signed(t, admin, s), anchors), ReasonUntrustedCertificate)
	})
	t.Run("certificate for another key", func(t *testing.T) {
		s := unsigned(2, "")
		s.SignerCertificate = ca.issue(t, other.Public())
		wantReason(t, Verify(signed(t, admin, s), anchors), ReasonWrongSigner)
	})
	t.Run("no certificate", func(t *testing.T) {
		wantReason(t, Verify(signed(t, admin, unsigned(2, "")), anchors), ReasonUntrustedCertificate)
	})
	t.Run("certificate expired at issue", func(t *testing.T) {
		s := unsigned(2, "")
		s.SignerCertificate = ca.issue(t, admin.Public())
		s.IssuedAt = testNow.Add(2 * time.Hour).Unix()
		late := Anchors{CA: ca.pool, At: testNow.AddDate(1, 0, 0)}
		wantReason(t, Verify(signed(t, admin, s), late), ReasonNotValidAtIssue)
	})
	t.Run("no CA anchor", func(t *testing.T) {
		s := unsigned(2, "")
		s.SignerCertificate = ca.issue(t, admin.Public())
		wantReason(t, Verify(signed(t, admin, s), Anchors{}), ReasonNoAnchor)
	})
}

func TestSignPlatformInit_VerifiesAgainstTheClusterCAOnly(t *testing.T) {
	cluster, owner, node := newCA(t), newCA(t), newKey(t)
	raw, err := SignPlatformInit(node, cluster.issue(t, node.Public()), "acme", testNow)
	if err != nil {
		t.Fatalf("SignPlatformInit: %v", err)
	}
	s, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Barrier != clusterseal.TenantBarrier("acme") || s.Epoch != 1 || s.StrategyID != seal.StrategySystemBarrier {
		t.Fatalf("statement = %+v, want acme's epoch-1 system-barrier statement", s)
	}
	if err := Verify(s, Anchors{Platform: cluster.pool, At: testNow}); err != nil {
		t.Fatalf("Verify(platform anchor): %v", err)
	}

	t.Run("owner keeps custody", func(t *testing.T) {
		wantReason(t, Verify(s, Anchors{FirstAdmin: thumbprint(t, node.Public()), CA: cluster.pool, At: testNow}), ReasonNoAnchor)
	})
	t.Run("certificate from the owner's CA", func(t *testing.T) {
		other, err := SignPlatformInit(node, owner.issue(t, node.Public()), "acme", testNow)
		if err != nil {
			t.Fatalf("SignPlatformInit: %v", err)
		}
		st, err := Parse(other)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		anchors := Anchors{CA: owner.pool, Platform: cluster.pool, At: testNow}
		wantReason(t, Verify(st, anchors), ReasonUntrustedCertificate)
	})
	t.Run("certificate for another key", func(t *testing.T) {
		other, err := SignPlatformInit(node, cluster.issue(t, newKey(t).Public()), "acme", testNow)
		if err != nil {
			t.Fatalf("SignPlatformInit: %v", err)
		}
		st, err := Parse(other)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		wantReason(t, Verify(st, Anchors{Platform: cluster.pool, At: testNow}), ReasonWrongSigner)
	})
}

// platformStatement is acme's platform statement, signed by a node under a
// cluster certificate valid from an hour before testNow to an hour after,
// issued at issuedAt.
func platformStatement(t *testing.T, cluster *testCA, issuedAt time.Time) InitStatement {
	t.Helper()
	node := newKey(t)
	raw, err := SignPlatformInit(node, cluster.issue(t, node.Public()), "acme", issuedAt)
	if err != nil {
		t.Fatalf("SignPlatformInit: %v", err)
	}
	s, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

// A statement signed while its certificate was valid is verified at its issue
// time: it still verifies once the certificate has expired, so a replica that
// joins later can still initialize, and the tenant still unseals.
func TestVerify_StatementOutlivesItsCertificate(t *testing.T) {
	cluster, owner, admin := newCA(t), newCA(t), newKey(t)
	expired := testNow.AddDate(1, 0, 0)
	if err := Verify(platformStatement(t, cluster, testNow), Anchors{Platform: cluster.pool, At: expired}); err != nil {
		t.Fatalf("Verify(platform statement, certificate expired since) = %v, want nil", err)
	}
	s := unsigned(2, "")
	s.SignerCertificate = owner.issue(t, admin.Public())
	if err := Verify(signed(t, admin, s), Anchors{CA: owner.pool, At: expired}); err != nil {
		t.Fatalf("Verify(epoch 2, certificate expired since) = %v, want nil", err)
	}
}

// An issue time outside the certificate's validity window is refused, however
// the verification time falls: a certificate vouches only for what was signed
// while it was valid.
func TestVerify_IssueTimeOutsideTheCertificateIsRefused(t *testing.T) {
	cluster := newCA(t)
	anchors := Anchors{Platform: cluster.pool, At: testNow.AddDate(1, 0, 0)}
	cases := map[string]time.Time{
		"before not-before": testNow.Add(-2 * time.Hour),
		"after not-after":   testNow.Add(2 * time.Hour),
	}
	for name, issuedAt := range cases {
		t.Run(name, func(t *testing.T) {
			wantReason(t, Verify(platformStatement(t, cluster, issuedAt), anchors), ReasonNotValidAtIssue)
		})
	}
}

// A statement issued after the time it is verified at is refused: there is no
// allowance for clock skew. Issued at exactly that time, it verifies.
func TestVerify_IssuedAfterTheVerificationTimeIsRefused(t *testing.T) {
	cluster, admin := newCA(t), newKey(t)
	platform := platformStatement(t, cluster, testNow)
	firstAdmin := signed(t, admin, unsigned(1, ""))
	early := testNow.Add(-time.Second)
	wantReason(t, Verify(platform, Anchors{Platform: cluster.pool, At: early}), ReasonIssuedInFuture)
	wantReason(t, Verify(firstAdmin, Anchors{FirstAdmin: thumbprint(t, admin.Public()), At: early}), ReasonIssuedInFuture)
	if err := Verify(platform, Anchors{Platform: cluster.pool, At: testNow}); err != nil {
		t.Fatalf("Verify(issued at the verification time) = %v, want nil", err)
	}
}

// The issue time is signed: moving it, even inside the certificate's validity
// window, breaks the signature.
func TestVerify_TamperedIssueTimeBreaksTheSignature(t *testing.T) {
	cluster := newCA(t)
	anchors := Anchors{Platform: cluster.pool, At: testNow.AddDate(1, 0, 0)}
	for name, shift := range map[string]int64{"earlier": -60, "later": 60} {
		t.Run(name, func(t *testing.T) {
			s := platformStatement(t, cluster, testNow)
			s.IssuedAt += shift
			wantReason(t, Verify(s, anchors), ReasonBadSignature)
		})
	}
}

func TestSignPlatformInit_Refusals(t *testing.T) {
	cluster, node := newCA(t), newKey(t)
	t.Run("no certificate", func(t *testing.T) {
		_, err := SignPlatformInit(node, nil, "acme", testNow)
		wantReason(t, err, ReasonUntrustedCertificate)
	})
	t.Run("nil signer", func(t *testing.T) {
		if _, err := SignPlatformInit(nil, cluster.issue(t, node.Public()), "acme", testNow); err == nil {
			t.Fatal("SignPlatformInit(nil signer) = nil error, want refusal")
		}
	})
	t.Run("invalid tenant", func(t *testing.T) {
		_, err := SignPlatformInit(node, cluster.issue(t, node.Public()), "", testNow)
		wantReason(t, err, ReasonMalformed)
	})
}

// A later epoch of a platform-custody barrier verifies under a certificate
// from either the owner's CA or the cluster CA, and under neither once the
// platform anchor is withdrawn.
func TestVerify_LaterEpochUnderPlatformCustody(t *testing.T) {
	cluster, owner, node := newCA(t), newCA(t), newKey(t)
	s := unsigned(2, "")
	s.SignerCertificate = cluster.issue(t, node.Public())
	s = signed(t, node, s)
	if err := Verify(s, Anchors{CA: owner.pool, Platform: cluster.pool, At: testNow}); err != nil {
		t.Fatalf("Verify(cluster-certified, platform anchor): %v", err)
	}
	wantReason(t, Verify(s, Anchors{CA: owner.pool, At: testNow}), ReasonUntrustedCertificate)

	o := unsigned(2, "")
	o.SignerCertificate = owner.issue(t, node.Public())
	o = signed(t, node, o)
	if err := Verify(o, Anchors{CA: owner.pool, Platform: cluster.pool, At: testNow}); err != nil {
		t.Fatalf("Verify(owner-certified, platform anchor): %v", err)
	}
}

func TestVerify_EpochOneNeedsTheFirstAdmin(t *testing.T) {
	ca, admin := newCA(t), newKey(t)
	s := unsigned(1, "")
	s.SignerCertificate = ca.issue(t, admin.Public())
	wantReason(t, Verify(signed(t, admin, s), Anchors{CA: ca.pool, At: testNow}), ReasonNoAnchor)
}

func TestVerify_Malformed(t *testing.T) {
	admin := newKey(t)
	anchors := Anchors{FirstAdmin: thumbprint(t, admin.Public())}
	cases := map[string]func(*InitStatement){
		"invalid barrier":    func(s *InitStatement) { s.Barrier = "tenant/" },
		"zero epoch":         func(s *InitStatement) { s.Epoch = 0 },
		"no issue time":      func(s *InitStatement) { s.IssuedAt = 0 },
		"negative issue":     func(s *InitStatement) { s.IssuedAt = -1 },
		"no strategy":        func(s *InitStatement) { s.StrategyID = "" },
		"negative threshold": func(s *InitStatement) { s.Threshold = -1 },
		"oversized key ref":  func(s *InitStatement) { s.KeyRef = strings.Repeat("k", MaxFieldLength+1) },
		"no signature":       func(s *InitStatement) { s.Signature = nil },
		"no signer":          func(s *InitStatement) { s.SignerSPKI = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := signed(t, admin, unsigned(1, ""))
			mutate(&s)
			wantReason(t, Verify(s, anchors), ReasonMalformed)
		})
	}
	t.Run("unparsable signer key", func(t *testing.T) {
		s := signed(t, admin, unsigned(1, ""))
		s.SignerSPKI = []byte("not a key")
		wantReason(t, Verify(s, anchors), ReasonMalformedKey)
	})
}

func TestSign_Refusals(t *testing.T) {
	var unsupported *mtls.UnsupportedSignerError
	if _, err := Sign(nil, unsigned(1, "")); !errors.As(err, &unsupported) {
		t.Fatalf("Sign(nil) = %v, want *mtls.UnsupportedSignerError", err)
	}
	if _, err := Sign(newKey(t), unsigned(0, "")); err == nil {
		t.Fatal("Sign signed a malformed statement")
	}
}

// The encoding is domain-separated and length-prefixed: moving bytes from one
// field into its neighbour changes the message, and the message opens with
// the statement's own context.
func TestMessage_LengthPrefixedAndDomainSeparated(t *testing.T) {
	a, b := unsigned(1, "ab"), unsigned(1, "a")
	a.VerifierSalt, b.VerifierSalt = []byte("c"), []byte("bc")
	if bytes.Equal(a.Message(), b.Message()) {
		t.Fatal("two statements differing only in a field boundary encode alike")
	}
	msg := a.Message()
	n := binary.BigEndian.Uint32(msg)
	if string(msg[4:4+n]) != messageContext {
		t.Fatalf("message opens with %q, want the context %q", msg[4:4+n], messageContext)
	}
	signedOnce := a
	signedOnce.Signature = []byte("sig")
	if !bytes.Equal(signedOnce.Message(), a.Message()) {
		t.Fatal("the signature must not be part of the signed message")
	}
}

func TestVerify_TheRosterMustBeTheOneItsHashCommitsTo(t *testing.T) {
	admin := newKey(t)
	anchors := Anchors{FirstAdmin: thumbprint(t, admin.Public())}
	withRoster := func(roster []byte) InitStatement {
		s := unsigned(1, "")
		s.StrategyID = seal.StrategyShamir
		s.Threshold = 2
		s.Roster = roster
		sum := sha256.Sum256(roster)
		s.RosterHash = sum[:]
		s.UnsealKeyCheck = bytes.Repeat([]byte{7}, seal.UnsealKeyCheckSize)
		return s
	}
	roster := []byte(`{"version":1,"domain":"unseal"}`)
	if err := Verify(signed(t, admin, withRoster(roster)), anchors); err != nil {
		t.Fatalf("a statement carrying its committed roster: %v", err)
	}
	raw, err := signed(t, admin, withRoster(roster)).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Parse(raw); err != nil || !bytes.Equal(got.Roster, roster) {
		t.Fatalf("the roster does not round-trip: %v", err)
	}

	other := withRoster(roster)
	other.Roster = []byte(`{"version":1,"domain":"recovery"}`)
	if _, err := Sign(admin, other); err == nil {
		t.Fatal("signed a roster its hash does not commit to")
	}
	other = signed(t, admin, withRoster(roster))
	other.Roster = []byte(`{"version":1,"domain":"recovery"}`)
	wantReason(t, Verify(other, anchors), ReasonMalformed)

	huge := signed(t, admin, withRoster(roster))
	huge.Roster = bytes.Repeat([]byte{'x'}, MaxRosterLength+1)
	sum := sha256.Sum256(huge.Roster)
	huge.RosterHash = sum[:]
	wantReason(t, Verify(huge, anchors), ReasonMalformed)

	// Covering RosterHash covers Roster: a consistent swap breaks the signature.
	swapped := signed(t, admin, withRoster(roster))
	swapped.Roster = []byte(`{"version":1,"domain":"unseal","total":3}`)
	sum = sha256.Sum256(swapped.Roster)
	swapped.RosterHash = sum[:]
	wantReason(t, Verify(swapped, anchors), ReasonBadSignature)
}
