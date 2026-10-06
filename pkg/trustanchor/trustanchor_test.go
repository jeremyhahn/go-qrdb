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

package trustanchor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// testCA is a CA certificate and its key.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var serial int64 = 100

func nextSerial() *big.Int { serial++; return big.NewInt(serial) }

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// newRoot returns a self-signed CA certificate.
func newRoot(t *testing.T, cn string) *testCA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          nextSerial(),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: c, key: key}
}

// issue signs a certificate with ca.
func (ca *testCA) issue(t *testing.T, tmpl *x509.Certificate) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key := newKey(t)
	tmpl.SerialNumber = nextSerial()
	if tmpl.NotBefore.IsZero() {
		tmpl.NotBefore = time.Now().Add(-time.Hour)
		tmpl.NotAfter = time.Now().Add(12 * time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

func (ca *testCA) intermediate(t *testing.T, cn string) *testCA {
	t.Helper()
	c, k := ca.issue(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: cn}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign,
	})
	return &testCA{cert: c, key: k}
}

func (ca *testCA) clientLeaf(t *testing.T, cn, ou string) *x509.Certificate {
	t.Helper()
	c, _ := ca.issue(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: cn, OrganizationalUnit: []string{ou}},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	return c
}

func TestParseAnchors_RefusesWhatCannotAnchor(t *testing.T) {
	root := newRoot(t, "root")
	leaf := root.clientLeaf(t, "alice", "acme")
	noSign, _ := root.issue(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: "nosign"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature,
	})
	expired, _ := root.issue(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: "old"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour),
	})
	cases := map[string][]byte{
		"a leaf":                 EncodePEM(leaf),
		"no cert-sign usage":     EncodePEM(noSign),
		"outside validity":       EncodePEM(expired),
		"good then bad":          append(EncodePEM(root.cert), EncodePEM(leaf)...),
		"not PEM":                []byte("not a certificate"),
		"a PEM block, not x.509": []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"),
	}
	for name, bundle := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAnchors(bundle, time.Now())
			var invalid *InvalidAnchorError
			var empty *EmptyBundleError
			if !errors.As(err, &invalid) && !errors.As(err, &empty) {
				t.Fatalf("ParseAnchors = %v, want a refusal", err)
			}
			if err.Error() == "" {
				t.Fatal("empty error text")
			}
		})
	}
	got, err := ParseAnchors(append(EncodePEM(root.cert), EncodePEM(root.cert)...), time.Now())
	if err != nil || len(got) != 1 {
		t.Fatalf("a duplicated anchor = %d, %v; want it collapsed to 1", len(got), err)
	}
}

func TestKVRegistry_AddListRemove(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	a, b := newRoot(t, "a"), newRoot(t, "b")

	added, err := r.Add(ctx, "acme", append(EncodePEM(a.cert), EncodePEM(b.cert)...))
	if err != nil || len(added) != 2 {
		t.Fatalf("Add = %v, %v", added, err)
	}
	again, err := r.Add(ctx, "acme", EncodePEM(a.cert))
	if err != nil || len(again) != 1 || !again[0].AddedAt.Equal(added[0].AddedAt) && !again[0].AddedAt.Equal(added[1].AddedAt) {
		t.Fatalf("re-adding a registered anchor = %v, %v; want it reported as already registered", again, err)
	}
	list, err := r.List(ctx, "acme")
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if other, _ := r.List(ctx, "globex"); len(other) != 0 {
		t.Fatalf("another tenant sees %d anchors, want none", len(other))
	}
	bundle, err := r.CABundle(ctx, "acme")
	if err != nil || !bytes.Contains(bundle, EncodePEM(a.cert)) || !bytes.Contains(bundle, EncodePEM(b.cert)) {
		t.Fatalf("CABundle = %q, %v", bundle, err)
	}
	if err := r.Remove(ctx, "acme", Fingerprint(a.cert)); err != nil {
		t.Fatal(err)
	}
	var nf *AnchorNotFoundError
	if err := r.Remove(ctx, "acme", Fingerprint(a.cert)); !errors.As(err, &nf) || nf.Error() == "" {
		t.Fatalf("removing it again = %v, want *AnchorNotFoundError", err)
	}
	if err := r.Remove(ctx, "globex", Fingerprint(b.cert)); !errors.As(err, &nf) {
		t.Fatalf("another tenant removing acme's anchor = %v, want *AnchorNotFoundError", err)
	}
	if empty, _ := r.CABundle(ctx, "globex"); empty != nil {
		t.Fatalf("CABundle of a tenant with none = %q, want nil", empty)
	}
}

func TestKVRegistry_RefusesBadTenantsAndTooMany(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newRoot(t, "r")
	var bad *InvalidTenantError
	if _, err := r.Add(ctx, "acme/evil", EncodePEM(root.cert)); !errors.As(err, &bad) || bad.Error() == "" || bad.Unwrap() == nil {
		t.Fatalf("a nesting tenant id = %v, want *InvalidTenantError", err)
	}
	var bundle []byte
	for range MaxAnchorsPerTenant + 1 {
		bundle = append(bundle, EncodePEM(newRoot(t, "x").cert)...)
	}
	var tooMany *TooManyAnchorsError
	if _, err := r.Add(ctx, "acme", bundle); !errors.As(err, &tooMany) || tooMany.Error() == "" {
		t.Fatalf("Add past the limit = %v, want *TooManyAnchorsError", err)
	}
	if list, _ := r.List(ctx, "acme"); len(list) != 0 {
		t.Fatalf("a refused Add registered %d anchors", len(list))
	}
}

func TestVerifyClientCertificate_ChainsToTheTenantsAnchorsOnly(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	acmeRoot, globexRoot := newRoot(t, "acme-root"), newRoot(t, "globex-root")
	issuing := acmeRoot.intermediate(t, "acme-issuing")
	leaf := issuing.clientLeaf(t, "alice", "acme")
	if _, err := r.Add(ctx, "acme", EncodePEM(acmeRoot.cert)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(ctx, "globex", EncodePEM(globexRoot.cert)); err != nil {
		t.Fatal(err)
	}

	if err := VerifyClientCertificate(ctx, r, "acme", leaf, []*x509.Certificate{issuing.cert}, time.Now()); err != nil {
		t.Fatalf("a leaf under acme's anchor, via its intermediate: %v", err)
	}
	var untrusted *CertificateNotTrustedError
	if err := VerifyClientCertificate(ctx, r, "globex", leaf, []*x509.Certificate{issuing.cert}, time.Now()); !errors.As(err, &untrusted) || untrusted.Error() == "" || untrusted.Unwrap() == nil {
		t.Fatalf("acme's leaf against globex's anchors = %v, want *CertificateNotTrustedError", err)
	}
	if err := VerifyClientCertificate(ctx, r, "acme", leaf, nil, time.Now()); !errors.As(err, &untrusted) {
		t.Fatalf("without its intermediate = %v, want *CertificateNotTrustedError", err)
	}
	var none *NoAnchorsError
	if err := VerifyClientCertificate(ctx, r, "initech", leaf, nil, time.Now()); !errors.As(err, &none) || none.Error() == "" {
		t.Fatalf("a tenant with no anchors = %v, want *NoAnchorsError", err)
	}
	if err := VerifyClientCertificateAgainst("acme", []*x509.Certificate{acmeRoot.cert}, nil, nil, time.Now()); !errors.As(err, &untrusted) {
		t.Fatalf("a nil leaf = %v, want *CertificateNotTrustedError", err)
	}
}

func TestChainContainsAnchor_IsPerTenant(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	acmeRoot := newRoot(t, "acme-root")
	issuing := acmeRoot.intermediate(t, "acme-issuing")
	leaf := issuing.clientLeaf(t, "alice", "acme")
	if _, err := r.Add(ctx, "acme", EncodePEM(acmeRoot.cert)); err != nil {
		t.Fatal(err)
	}
	chain := []*x509.Certificate{leaf, issuing.cert, acmeRoot.cert}

	if ok, err := ChainContainsAnchor(ctx, r, "acme", chain); err != nil || !ok {
		t.Fatalf("acme's chain for acme = %v, %v; want true", ok, err)
	}
	if ok, err := ChainContainsAnchor(ctx, r, "globex", chain); err != nil || ok {
		t.Fatalf("acme's chain for globex = %v, %v; want false", ok, err)
	}
	if ok, _ := ChainContainsAnchor(ctx, r, "acme", chain[:1]); ok {
		t.Fatal("a chain of just the leaf matched")
	}
	if ok, _ := ChainContainsAnchor(ctx, nil, "acme", chain); ok {
		t.Fatal("a nil source matched")
	}
	// A leaf that IS an anchor's bytes does not vouch for itself.
	self := []*x509.Certificate{acmeRoot.cert}
	if ok, _ := ChainContainsAnchor(ctx, r, "acme", self); ok {
		t.Fatal("the anchor as a leaf matched itself")
	}
}

func TestObserve_FiresOnSuccessfulChangesOnly(t *testing.T) {
	ctx := context.Background()
	fired := 0
	r := Observe(NewKVRegistry(kvstore.NewMemoryKVStore()), func() { fired++ })
	root := newRoot(t, "r")
	if _, err := r.Add(ctx, "acme", EncodePEM(root.cert)); err != nil || fired != 1 {
		t.Fatalf("Add: %v, fired %d", err, fired)
	}
	if _, err := r.Add(ctx, "acme", []byte("junk")); err == nil || fired != 1 {
		t.Fatalf("a failed Add fired: %v, fired %d", err, fired)
	}
	if err := r.Remove(ctx, "acme", "nope"); err == nil || fired != 1 {
		t.Fatalf("a failed Remove fired: %v, fired %d", err, fired)
	}
	if err := r.Remove(ctx, "acme", Fingerprint(root.cert)); err != nil || fired != 2 {
		t.Fatalf("Remove: %v, fired %d", err, fired)
	}
	if Observe(nil, func() {}) != nil {
		t.Fatal("observing nil returned non-nil")
	}
	plain := NewKVRegistry(kvstore.NewMemoryKVStore())
	if Observe(plain, nil) != Registry(plain) {
		t.Fatal("observing with no callback wrapped the registry")
	}
}

func TestErrors_Describe(t *testing.T) {
	cause := errors.New("cause")
	for _, err := range []error{
		&ExternallyManagedError{},
		&StoreError{Op: "put", Err: cause},
		&EmptyBundleError{},
	} {
		if err.Error() == "" {
			t.Fatalf("%T has empty text", err)
		}
	}
	if !errors.Is(&StoreError{Op: "x", Err: cause}, cause) {
		t.Fatal("StoreError does not unwrap")
	}
}
