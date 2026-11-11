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

package delegation

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

func validGrant() Grant {
	return Grant{Tenant: "acme", Principal: "xkms", Scope: []string{"kv:read", "kv:write"}}
}

// issue signs a client certificate carrying exts with a fresh CA, returning
// the parsed leaf and its CA.
func issue(t *testing.T, life time.Duration, exts ...pkix.Extension) (*x509.Certificate, *x509.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "acme login CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber:    big.NewInt(2),
		Subject:         pkix.Name{CommonName: "xkms", OrganizationalUnit: []string{"acme"}},
		NotBefore:       now,
		NotAfter:        now.Add(life),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: exts,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	return leaf, ca
}

func verify(leaf, ca *x509.Certificate) error {
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}

func TestGrant_RoundTripsThroughACertificate(t *testing.T) {
	ext, err := validGrant().Extension()
	if err != nil {
		t.Fatal(err)
	}
	if !ext.Critical || !ext.Id.Equal(OID) {
		t.Fatalf("extension = %+v, want critical %v", ext, OID)
	}
	leaf, _ := issue(t, time.Hour, ext)
	g, found, err := FromCertificate(leaf)
	if err != nil || !found {
		t.Fatalf("FromCertificate = %v, %v", found, err)
	}
	want := validGrant()
	if g.Tenant != want.Tenant || g.Principal != want.Principal || strings.Join(g.Scope, ",") != strings.Join(want.Scope, ",") {
		t.Fatalf("grant = %+v, want %+v", g, want)
	}
}

func TestFromCertificate_NoExtensionIsNotFound(t *testing.T) {
	leaf, _ := issue(t, time.Hour)
	if _, found, err := FromCertificate(leaf); found || err != nil {
		t.Fatalf("FromCertificate = %v, %v; want not found", found, err)
	}
	if _, found, err := FromCertificate(nil); found || err != nil {
		t.Fatalf("FromCertificate(nil) = %v, %v; want not found", found, err)
	}
}

func TestFromCertificate_RefusesANonCriticalExtension(t *testing.T) {
	ext, err := validGrant().Extension()
	if err != nil {
		t.Fatal(err)
	}
	ext.Critical = false
	leaf, _ := issue(t, time.Hour, ext)
	_, found, err := FromCertificate(leaf)
	var extErr *ExtensionError
	if !found || !errors.As(err, &extErr) {
		t.Fatalf("FromCertificate = %v, %v; want found with *ExtensionError", found, err)
	}
}

func TestFromCertificate_RefusesMalformedValues(t *testing.T) {
	build := func(f func(b *cryptobyte.Builder)) []byte {
		var b cryptobyte.Builder
		f(&b)
		der, err := b.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		return der
	}
	good, err := validGrant().Extension()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":    nil,
		"trailing": append(append([]byte(nil), good.Value...), 0),
		"version 2": build(func(b *cryptobyte.Builder) {
			b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
				b.AddASN1Int64(2)
				addUTF8(b, "acme")
				addUTF8(b, "xkms")
				b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) { addUTF8(b, "kv:read") })
			})
		}),
		"printable string": build(func(b *cryptobyte.Builder) {
			b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
				b.AddASN1Int64(Version)
				b.AddASN1(cbasn1.PrintableString, func(b *cryptobyte.Builder) { b.AddBytes([]byte("acme")) })
				addUTF8(b, "xkms")
				b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) { addUTF8(b, "kv:read") })
			})
		}),
		"empty scope": build(func(b *cryptobyte.Builder) {
			b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
				b.AddASN1Int64(Version)
				addUTF8(b, "acme")
				addUTF8(b, "xkms")
				b.AddASN1(cbasn1.SEQUENCE, func(*cryptobyte.Builder) {})
			})
		}),
		"scope not a sequence": build(func(b *cryptobyte.Builder) {
			b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
				b.AddASN1Int64(Version)
				addUTF8(b, "acme")
				addUTF8(b, "xkms")
				addUTF8(b, "kv:read")
			})
		}),
	}
	for name, value := range cases {
		leaf := &x509.Certificate{Extensions: []pkix.Extension{{Id: OID, Critical: true, Value: value}}}
		_, found, err := FromCertificate(leaf)
		var extErr *ExtensionError
		if !found || !errors.As(err, &extErr) {
			t.Errorf("%s: FromCertificate = %v, %v; want found with *ExtensionError", name, found, err)
		}
	}
}

func TestGrant_ValidateRefusesEachMalformedField(t *testing.T) {
	long := strings.Repeat("a", MaxPrincipalLen+1)
	many := make([]string, MaxScope+1)
	for i := range many {
		many[i] = "r:a" + strings.Repeat("x", i)
	}
	cases := map[string]Grant{
		"empty tenant":      {Principal: "xkms", Scope: []string{"kv:read"}},
		"tenant with space": {Tenant: "a cme", Principal: "xkms", Scope: []string{"kv:read"}},
		"empty principal":   {Tenant: "acme", Scope: []string{"kv:read"}},
		"long principal":    {Tenant: "acme", Principal: long, Scope: []string{"kv:read"}},
		"upper principal":   {Tenant: "acme", Principal: "XKMS", Scope: []string{"kv:read"}},
		"dot-led principal": {Tenant: "acme", Principal: ".xkms", Scope: []string{"kv:read"}},
		"no scope":          {Tenant: "acme", Principal: "xkms"},
		"too many":          {Tenant: "acme", Principal: "xkms", Scope: many},
		"no colon":          {Tenant: "acme", Principal: "xkms", Scope: []string{"kvread"}},
		"two colons":        {Tenant: "acme", Principal: "xkms", Scope: []string{"kv:read:x"}},
		"empty action":      {Tenant: "acme", Principal: "xkms", Scope: []string{"kv:"}},
		"empty resource":    {Tenant: "acme", Principal: "xkms", Scope: []string{":read"}},
		"duplicate":         {Tenant: "acme", Principal: "xkms", Scope: []string{"kv:read", "kv:read"}},
		"control character": {Tenant: "acme", Principal: "xkms", Scope: []string{"kv:re\x00ad"}},
	}
	for name, g := range cases {
		var gErr *GrantError
		if err := g.Validate(); !errors.As(err, &gErr) {
			t.Errorf("%s: Validate = %v, want *GrantError", name, err)
		}
		if _, err := g.Extension(); err == nil {
			t.Errorf("%s: Extension encoded an invalid grant", name)
		}
	}
	if err := validGrant().Validate(); err != nil {
		t.Fatalf("valid grant refused: %v", err)
	}
}

func TestValidatePrincipal(t *testing.T) {
	for _, ok := range []string{"xkms", "a", "svc.v2_x-y", "0abc"} {
		if err := ValidatePrincipal(ok); err != nil {
			t.Errorf("ValidatePrincipal(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "_x", "X", "x y", strings.Repeat("a", MaxPrincipalLen+1)} {
		if err := ValidatePrincipal(bad); err == nil {
			t.Errorf("ValidatePrincipal(%q) accepted", bad)
		}
	}
}

// TestVerifiable_IsWhatMakesTheCriticalExtensionVerify proves both halves of
// the criticality contract: crypto/x509 alone refuses a delegation, and only
// the handled copy verifies.
func TestVerifiable_IsWhatMakesTheCriticalExtensionVerify(t *testing.T) {
	ext, err := validGrant().Extension()
	if err != nil {
		t.Fatal(err)
	}
	leaf, ca := issue(t, time.Hour, ext)
	var unhandled x509.UnhandledCriticalExtension
	if err := verify(leaf, ca); !errors.As(err, &unhandled) {
		t.Fatalf("an unaware verifier accepted a delegation: %v", err)
	}
	handled := Verifiable(leaf)
	if handled == leaf {
		t.Fatal("Verifiable returned the certificate itself for a valid delegation")
	}
	if err := verify(handled, ca); err != nil {
		t.Fatalf("the handled copy does not verify: %v", err)
	}
	if len(leaf.UnhandledCriticalExtensions) != 1 {
		t.Fatal("Verifiable modified the original certificate")
	}
}

func TestVerifiable_LeavesEveryOtherCertificateAlone(t *testing.T) {
	plain, _ := issue(t, time.Hour)
	if Verifiable(plain) != plain {
		t.Fatal("Verifiable copied a certificate with no delegation")
	}
	if Verifiable(nil) != nil {
		t.Fatal("Verifiable(nil) is not nil")
	}
	// A malformed critical delegation stays unhandled, so crypto/x509 keeps
	// refusing it.
	bad, ca := issue(t, time.Hour, pkix.Extension{Id: OID, Critical: true, Value: []byte{0x30, 0x00}})
	if Verifiable(bad) != bad {
		t.Fatal("Verifiable handled a malformed delegation")
	}
	if err := verify(Verifiable(bad), ca); err == nil {
		t.Fatal("a malformed delegation verified")
	}
}

func TestCheckLifetime(t *testing.T) {
	ok, _ := issue(t, MaxLifetime)
	if err := CheckLifetime(ok); err != nil {
		t.Fatalf("CheckLifetime(MaxLifetime) = %v", err)
	}
	long, _ := issue(t, MaxLifetime+time.Second)
	var lErr *LifetimeError
	if err := CheckLifetime(long); !errors.As(err, &lErr) {
		t.Fatalf("CheckLifetime(MaxLifetime+1s) = %v, want *LifetimeError", err)
	}
	if !strings.Contains(lErr.Error(), MaxLifetime.String()) {
		t.Fatalf("error %q does not name the bound", lErr.Error())
	}
}

// TestOID_IsPinned fails if the extension identifier moves: certificates
// already issued carry it, and go-trusted-platform and go-xkms share its arc.
func TestOID_IsPinned(t *testing.T) {
	if got, want := OID.String(), "1.3.6.1.4.1.29377.101.24"; got != want {
		t.Fatalf("OID = %s, want %s", got, want)
	}
}
