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
	"crypto/x509"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

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
		"":       seal.StrategyPassphrase, // an unnamed strategy, as the node reads it
		"none":   seal.StrategyNone,
		"shamir": seal.StrategyShamir,
	} {
		raw, err := SignTenantInit(admin, "acme", strategy)
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

func TestSignTenantInit_NilSignerSignsNothing(t *testing.T) {
	raw, err := SignTenantInit(nil, "acme", "none")
	if err != nil || raw != nil {
		t.Fatalf("SignTenantInit(nil) = %v, %v; want nil, nil", raw, err)
	}
}

func TestSignTenantInit_RefusesAnUnencodableStatement(t *testing.T) {
	_, err := SignTenantInit(newKey(t), "acme", strings.Repeat("s", MaxFieldLength+1))
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
	t.Run("expired certificate", func(t *testing.T) {
		s := unsigned(2, "")
		s.SignerCertificate = ca.issue(t, admin.Public())
		late := Anchors{CA: ca.pool, At: testNow.AddDate(1, 0, 0)}
		wantReason(t, Verify(signed(t, admin, s), late), ReasonUntrustedCertificate)
	})
	t.Run("no CA anchor", func(t *testing.T) {
		s := unsigned(2, "")
		s.SignerCertificate = ca.issue(t, admin.Public())
		wantReason(t, Verify(signed(t, admin, s), Anchors{}), ReasonNoAnchor)
	})
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
