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

package cluster

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// testLeafPEM returns a self-signed certificate PEM over a fresh P-256 key.
// Every call yields a different keypair, hence a different identity.
func testLeafPEM(t *testing.T) ([]byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "node"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), leaf
}

func TestMemberIdentityFromCertificate(t *testing.T) {
	_, leaf := testLeafPEM(t)
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	want := "spki:" + hex.EncodeToString(sum[:])
	if got := MemberIdentityFromCertificate(leaf); got != want {
		t.Fatalf("MemberIdentityFromCertificate = %q, want %q", got, want)
	}
	if err := ValidateMemberIdentity(want); err != nil {
		t.Fatalf("derived identity fails validation: %v", err)
	}
	if got := MemberIdentityFromCertificate(nil); got != "" {
		t.Fatalf("MemberIdentityFromCertificate(nil) = %q, want empty", got)
	}
}

func TestMemberIdentityFromPEM(t *testing.T) {
	certPEM, leaf := testLeafPEM(t)
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1, 2, 3}})

	tests := []struct {
		name    string
		input   []byte
		want    string
		wantErr bool
	}{
		{name: "leaf", input: certPEM, want: MemberIdentityFromCertificate(leaf)},
		{name: "non-certificate block first", input: append(append([]byte{}, keyBlock...), certPEM...), want: MemberIdentityFromCertificate(leaf)},
		{name: "empty", input: nil, wantErr: true},
		{name: "no certificate", input: keyBlock, wantErr: true},
		{name: "garbage certificate", input: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("nope")}), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MemberIdentityFromPEM(tc.input)
			if tc.wantErr {
				var certErr *MemberIdentityCertificateError
				if !errors.As(err, &certErr) {
					t.Fatalf("err = %v, want *MemberIdentityCertificateError", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("MemberIdentityFromPEM = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestValidateMemberIdentity(t *testing.T) {
	valid := "spki:" + strings.Repeat("ab", 32)
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{name: "valid", input: valid, ok: true},
		{name: "missing scheme", input: strings.Repeat("ab", 32)},
		{name: "wrong scheme", input: "sha256:" + strings.Repeat("ab", 32)},
		{name: "short digest", input: "spki:" + strings.Repeat("ab", 31)},
		{name: "long digest", input: valid + "00"},
		{name: "upper-case hex", input: "spki:" + strings.Repeat("AB", 32)},
		{name: "non-hex", input: "spki:" + strings.Repeat("zz", 32)},
		{name: "empty", input: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMemberIdentity(tc.input)
			if tc.ok {
				if err != nil {
					t.Fatalf("ValidateMemberIdentity(%q) = %v, want nil", tc.input, err)
				}
				return
			}
			var formatErr *MemberIdentityFormatError
			if !errors.As(err, &formatErr) || formatErr.Identity != tc.input {
				t.Fatalf("ValidateMemberIdentity(%q) = %v, want *MemberIdentityFormatError", tc.input, err)
			}
		})
	}
}

func TestParseMemberIdentities(t *testing.T) {
	a := "spki:" + strings.Repeat("0a", 32)
	b := "spki:" + strings.Repeat("0b", 32)
	tests := []struct {
		name      string
		specs     []string
		want      map[uint64]string
		parseErr  bool
		formatErr bool
	}{
		{name: "none", specs: nil, want: map[uint64]string{}},
		{name: "two", specs: []string{"1=" + a, "2=" + b}, want: map[uint64]string{1: a, 2: b}},
		{name: "whitespace and blanks", specs: []string{" 1 = " + a + " ", "", "  "}, want: map[uint64]string{1: a}},
		{name: "missing equals", specs: []string{"1" + a}, parseErr: true},
		{name: "non-numeric id", specs: []string{"x=" + a}, parseErr: true},
		{name: "zero id", specs: []string{"0=" + a}, parseErr: true},
		{name: "duplicate id", specs: []string{"1=" + a, "1=" + b}, parseErr: true},
		{name: "bad identity", specs: []string{"1=spki:nothex"}, formatErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMemberIdentities(tc.specs)
			switch {
			case tc.parseErr:
				var pe *MemberIdentityParseError
				if !errors.As(err, &pe) {
					t.Fatalf("err = %v, want *MemberIdentityParseError", err)
				}
				return
			case tc.formatErr:
				var fe *MemberIdentityFormatError
				if !errors.As(err, &fe) {
					t.Fatalf("err = %v, want *MemberIdentityFormatError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMemberIdentities: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for id, identity := range tc.want {
				if got[id] != identity {
					t.Fatalf("got[%d] = %q, want %q", id, got[id], identity)
				}
			}
		})
	}
}

func TestResolveMemberIdentities(t *testing.T) {
	selfPEM, selfLeaf := testLeafPEM(t)
	self := MemberIdentityFromCertificate(selfLeaf)
	peer := "spki:" + strings.Repeat("0c", 32)
	other := "spki:" + strings.Repeat("0d", 32)

	t.Run("derives the local entry", func(t *testing.T) {
		got, err := ResolveMemberIdentities([]string{"2=" + peer}, 1, selfPEM)
		if err != nil {
			t.Fatalf("ResolveMemberIdentities: %v", err)
		}
		if got[1] != self || got[2] != peer || len(got) != 2 {
			t.Fatalf("got %v, want {1:%s 2:%s}", got, self, peer)
		}
	})
	t.Run("accepts a matching configured local entry", func(t *testing.T) {
		got, err := ResolveMemberIdentities([]string{"1=" + self, "2=" + peer}, 1, selfPEM)
		if err != nil || got[1] != self {
			t.Fatalf("got %v, %v; want local entry %s", got, err, self)
		}
	})
	t.Run("refuses a mismatched configured local entry", func(t *testing.T) {
		_, err := ResolveMemberIdentities([]string{"1=" + other}, 1, selfPEM)
		var mismatch *MemberIdentityMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("err = %v, want *MemberIdentityMismatchError", err)
		}
		if mismatch.ReplicaID != 1 || mismatch.Configured != other || mismatch.Derived != self {
			t.Fatalf("mismatch = %+v", mismatch)
		}
	})
	t.Run("no certificate leaves the configured map", func(t *testing.T) {
		got, err := ResolveMemberIdentities([]string{"2=" + peer}, 1, nil)
		if err != nil || len(got) != 1 || got[2] != peer {
			t.Fatalf("got %v, %v; want {2:%s}", got, err, peer)
		}
	})
	t.Run("propagates a parse error", func(t *testing.T) {
		_, err := ResolveMemberIdentities([]string{"bad"}, 1, selfPEM)
		var pe *MemberIdentityParseError
		if !errors.As(err, &pe) {
			t.Fatalf("err = %v, want *MemberIdentityParseError", err)
		}
	})
	t.Run("propagates an unreadable certificate", func(t *testing.T) {
		_, err := ResolveMemberIdentities(nil, 1, []byte("not pem"))
		var ce *MemberIdentityCertificateError
		if !errors.As(err, &ce) {
			t.Fatalf("err = %v, want *MemberIdentityCertificateError", err)
		}
	})
}

func TestMemberIdentityErrors(t *testing.T) {
	cause := errors.New("asn1: syntax error")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "parse", err: &MemberIdentityParseError{Input: "x"}, want: `cluster: invalid member identity "x", expected a unique replicaID=spki:<sha256-hex> with a non-zero replica ID`},
		{name: "format", err: &MemberIdentityFormatError{Identity: "y"}, want: `cluster: member identity "y" is not of the form spki:<64 lower-case hex digits>`},
		{name: "certificate without cause", err: &MemberIdentityCertificateError{Reason: "no CERTIFICATE block"}, want: "cluster: cannot derive member identity: no CERTIFICATE block"},
		{name: "certificate with cause", err: &MemberIdentityCertificateError{Reason: "parse certificate", Err: cause}, want: "cluster: cannot derive member identity: parse certificate: asn1: syntax error"},
		{name: "mismatch", err: &MemberIdentityMismatchError{ReplicaID: 3, Configured: "spki:a", Derived: "spki:b"}, want: "cluster: member identity for local replica 3 is configured as spki:a but its certificate presents spki:b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
	if !errors.Is(&MemberIdentityCertificateError{Reason: "r", Err: cause}, cause) {
		t.Fatal("MemberIdentityCertificateError does not unwrap to its cause")
	}
}
