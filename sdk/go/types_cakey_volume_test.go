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

package sdk_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// This file is in the EXTERNAL test package and imports only sdk/go, so it
// implements the CA-key and volume seams the way an embedder does: anything
// missing from the re-export fails to compile here.

// Compile-time checks that the error aliases are the provider's error types.
var (
	_ error = &sdk.CAKeyBarrierSealedError{}
	_ error = &sdk.CAKeyNotInitializedError{}
	_ error = &sdk.CAKeySignerRevokedError{}
	_ error = &sdk.CAKeyNilProviderError{}
	_ error = &sdk.VolumeInvalidSpecError{}
	_ error = &sdk.VolumeOpenError{}
	_ error = &sdk.VolumeUsageError{}
	_ error = &sdk.VolumeClosedError{}

	_ sdk.CAKeyProvider        = sdk.CAKeyAdapter{}
	_ sdk.CAKeyProvider        = (*sdk.CAKeyStaticProvider)(nil)
	_ sdk.CAKeyProviderFactory = (*tenantCAKeys)(nil)
	_ sdk.VolumeProvider       = dirProvider{}
	_ sdk.VolumeLockable       = (*dirVolume)(nil)
	_ sdk.VolumeKeyProvider    = fixedVolumeKeys{}
)

// selfSignedCA returns a fresh P-256 key and a CA certificate for it.
func selfSignedCA(t testing.TB, cn string) (*ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Unix(0, 0).Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

// tenantCAKeys is a CAKeyProviderFactory holding one key per tenant, the
// shape an embedder's HSM-backed factory has.
type tenantCAKeys struct {
	byTenant map[string]sdk.CAKeyProvider
}

func (f *tenantCAKeys) ProviderForTenant(tenantID string) (sdk.CAKeyProvider, error) {
	p, ok := f.byTenant[tenantID]
	if !ok {
		return nil, &sdk.CAKeyNotInitializedError{}
	}
	return p, nil
}

func (f *tenantCAKeys) TenantIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(f.byTenant))
	for id := range f.byTenant {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func TestSDK_CAKeyAdapterSignsWithTheEmbeddersKey(t *testing.T) {
	key, cert := selfSignedCA(t, "adapter")
	p := sdk.CAKeyAdapter{
		SignerFunc:      func(context.Context) (crypto.Signer, error) { return key, nil },
		CertificateFunc: func(context.Context) (*x509.Certificate, error) { return cert, nil },
	}
	s, err := p.Signer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	digest := make([]byte, 32)
	sig, err := s.Sign(rand.Reader, digest, crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Certificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(got.PublicKey.(*ecdsa.PublicKey), digest, sig) {
		t.Fatal("the adapter's signature does not verify under its certificate")
	}
}

func TestSDK_CAKeyAdapterPassesTheProvidersErrorThrough(t *testing.T) {
	p := sdk.CAKeyAdapter{
		SignerFunc: func(context.Context) (crypto.Signer, error) { return nil, &sdk.CAKeyBarrierSealedError{} },
	}
	_, err := p.Signer(context.Background())
	var sealed *sdk.CAKeyBarrierSealedError
	if !errors.As(err, &sealed) {
		t.Fatalf("Signer error = %v, want *CAKeyBarrierSealedError", err)
	}
}

func TestSDK_NewCAKeyStaticProviderServesItsMaterial(t *testing.T) {
	key, cert := selfSignedCA(t, "static")
	p, err := sdk.NewCAKeyStaticProvider(key, cert)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Certificate(context.Background())
	if err != nil || !got.Equal(cert) {
		t.Fatalf("Certificate() = %v, %v; want the supplied certificate", got, err)
	}
}

func TestSDK_NewCAKeyStaticProviderRefusesNilMaterial(t *testing.T) {
	_, cert := selfSignedCA(t, "static")
	if _, err := sdk.NewCAKeyStaticProvider(nil, cert); err == nil {
		t.Fatal("a nil signer was accepted")
	}
	if _, err := sdk.NewCAKeyStaticProvider(nil, nil); err == nil {
		t.Fatal("nil material was accepted")
	}
}

func TestSDK_CAKeyProviderFactoryResolvesOneKeyPerTenant(t *testing.T) {
	ka, ca := selfSignedCA(t, "tenant-a")
	kb, cb := selfSignedCA(t, "tenant-b")
	pa, _ := sdk.NewCAKeyStaticProvider(ka, ca)
	pb, _ := sdk.NewCAKeyStaticProvider(kb, cb)
	var f sdk.CAKeyProviderFactory = &tenantCAKeys{byTenant: map[string]sdk.CAKeyProvider{"a": pa, "b": pb}}

	ids, err := f.TenantIDs(context.Background())
	if err != nil || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("TenantIDs() = %v, %v", ids, err)
	}
	got, err := f.ProviderForTenant("b")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := got.Certificate(context.Background()); !c.Equal(cb) {
		t.Fatal("tenant b resolved to another tenant's CA")
	}
	var notInit *sdk.CAKeyNotInitializedError
	if _, err := f.ProviderForTenant("c"); !errors.As(err, &notInit) {
		t.Fatalf("an unknown tenant returned %v, want *CAKeyNotInitializedError", err)
	}
}

// dirProvider is a VolumeProvider over an existing directory.
type dirProvider struct{}

func (dirProvider) Name() string { return "example-dir" }

func (dirProvider) Open(_ context.Context, spec sdk.VolumeSpec) (sdk.Volume, error) {
	if spec.Path == "" {
		return nil, &sdk.VolumeInvalidSpecError{}
	}
	return &dirVolume{root: spec.Path, locked: true}, nil
}

// dirVolume is a lockable Volume.
type dirVolume struct {
	root   string
	locked bool
	closed bool
}

func (v *dirVolume) Root() string { return v.root }

func (v *dirVolume) Usage(context.Context) (sdk.VolumeUsage, error) {
	if v.closed {
		return sdk.VolumeUsage{}, &sdk.VolumeClosedError{}
	}
	return sdk.VolumeUsage{SizeBytes: 1 << 30, FreeBytes: 1 << 29}, nil
}

func (v *dirVolume) Health(context.Context) sdk.VolumeHealth {
	if v.locked {
		return sdk.VolumeHealth{State: sdk.VolumeStateLocked}
	}
	return sdk.VolumeHealth{State: sdk.VolumeStateReady}
}

func (v *dirVolume) Close(context.Context) error { v.closed = true; return nil }

func (v *dirVolume) Unlock(_ context.Context, _ sdk.VolumeKey) error { v.locked = false; return nil }

func (v *dirVolume) Lock(context.Context) error { v.locked = true; return nil }

// fixedVolumeKeys is a VolumeKeyProvider returning one key, the shape of an
// embedder's KMS-backed provider.
type fixedVolumeKeys struct{ key sdk.VolumeKey }

func (k fixedVolumeKeys) VolumeKey(context.Context, string, string, string) (sdk.VolumeKey, error) {
	return k.key, nil
}

func TestSDK_VolumeProviderUnlocksWithTheProvidersKey(t *testing.T) {
	ctx := context.Background()
	vol, err := dirProvider{}.Open(ctx, sdk.VolumeSpec{Pool: "fast", NodeID: "n1", Path: "/srv/fast"})
	if err != nil {
		t.Fatal(err)
	}
	if got := vol.Health(ctx).State; got != sdk.VolumeStateLocked {
		t.Fatalf("a fresh volume is %v, want locked", got)
	}
	var keys sdk.VolumeKeyProvider = fixedVolumeKeys{key: sdk.VolumeKey{1, 2, 3}}
	key, err := keys.VolumeKey(ctx, "n1", "fast", "fast")
	if err != nil {
		t.Fatal(err)
	}
	lockable, ok := vol.(sdk.VolumeLockable)
	if !ok {
		t.Fatal("the volume does not offer VolumeLockable")
	}
	if err := lockable.Unlock(ctx, key); err != nil {
		t.Fatal(err)
	}
	key.Wipe()
	if key != (sdk.VolumeKey{}) {
		t.Fatal("Wipe left key bytes behind")
	}
	if got := vol.Health(ctx).State; got != sdk.VolumeStateReady {
		t.Fatalf("an unlocked volume is %v, want ready", got)
	}
}

func TestSDK_VolumeProviderRefusalsAreTyped(t *testing.T) {
	ctx := context.Background()
	var invalid *sdk.VolumeInvalidSpecError
	if _, err := (dirProvider{}).Open(ctx, sdk.VolumeSpec{Pool: "fast"}); !errors.As(err, &invalid) {
		t.Fatalf("an empty path returned %v, want *VolumeInvalidSpecError", err)
	}
	vol, _ := dirProvider{}.Open(ctx, sdk.VolumeSpec{Path: "/srv/fast"})
	_ = vol.Close(ctx)
	var closed *sdk.VolumeClosedError
	if _, err := vol.Usage(ctx); !errors.As(err, &closed) {
		t.Fatalf("Usage after Close returned %v, want *VolumeClosedError", err)
	}
}

// ExampleCAKeyAdapter wires an embedder's own CA signing key into the seam
// node.WithClusterCAKeyProvider takes, without a wrapper type.
func ExampleCAKeyAdapter() {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cluster CA"}, IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	cert, _ := x509.ParseCertificate(der)

	var p sdk.CAKeyProvider = sdk.CAKeyAdapter{
		SignerFunc:      func(context.Context) (crypto.Signer, error) { return key, nil }, // an HSM-backed signer in production
		CertificateFunc: func(context.Context) (*x509.Certificate, error) { return cert, nil },
	}
	got, _ := p.Certificate(context.Background())
	fmt.Println(got.Subject.CommonName)
	// Output: cluster CA
}

// ExampleVolumeKeyProvider supplies a lockable volume's key from the
// embedder's own key custody, the seam node.WithVolumeKeyProvider takes.
func ExampleVolumeKeyProvider() {
	var keys sdk.VolumeKeyProvider = fixedVolumeKeys{key: sdk.VolumeKey{0x42}}
	key, _ := keys.VolumeKey(context.Background(), "node-1", "fast", "fast")
	defer key.Wipe()
	fmt.Println(len(key))
	// Output: 32
}
