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

package clusterca

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// newGuardedMemoryStore returns a fresh kvstoretest.GuardedMemoryStore:
// NewProvider requires kvstore.MultiGuardedBatchKVStore
// (cakey.NewSoftwareProvider writes the key and certificate records
// together in one atomic batch), and a plain *kvstore.MemoryKVStore
// deliberately does not offer it. Production gets the capability from
// lifecycle.NewGuardedKVStoreAdapter; these unit tests get it from
// kvstoretest.
func newGuardedMemoryStore() *kvstoretest.GuardedMemoryStore {
	return kvstoretest.NewGuardedMemoryStore()
}

// mockBarrier mirrors pkg/cakey's test double: real AES-GCM round-trips.
type mockBarrier struct {
	mu     sync.RWMutex
	sealed bool
	key    []byte
}

func newMockBarrier() *mockBarrier {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		panic("test key generation: " + err.Error())
	}
	return &mockBarrier{key: key}
}

func (b *mockBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	// #nosec G407 -- NewGCMWithRandomNonce samples its own nonce; nil is required here.
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

func (b *mockBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *mockBarrier) IsSealed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sealed
}

// newRealProvider returns an initialized, in-memory-backed *cakey.SoftwareProvider
// for Key A.
func newRealProvider(t *testing.T) *cakey.SoftwareProvider {
	t.Helper()
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	p := NewProvider(store, barrier)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return p
}

// decodeCert PEM-decodes and parses a single certificate, failing the test
// on any error.
func decodeCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("decodeCert: no PEM block found")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("decodeCert: ParseCertificate: %v", err)
	}
	return cert
}

func TestSubject(t *testing.T) {
	s := Subject()
	if s.CommonName != "qrdb-ca" {
		t.Fatalf("Subject().CommonName = %q, want qrdb-ca", s.CommonName)
	}
}

func TestNewProvider_IssueNodeLeaf(t *testing.T) {
	p := newRealProvider(t)

	certPEM, keyPEM, err := IssueNodeLeaf(context.Background(), p, "spiffe://qrdb.cluster/node/127.0.0.1:9000")
	if err != nil {
		t.Fatalf("IssueNodeLeaf: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatal("IssueNodeLeaf returned empty PEM")
	}

	cert := decodeCert(t, certPEM)
	caCert, err := p.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if err := cert.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("issued leaf does not chain to the cluster CA: %v", err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://qrdb.cluster/node/127.0.0.1:9000" {
		t.Fatalf("leaf URIs = %v, want the SPIFFE node URI", cert.URIs)
	}
}

func TestIssueLeafForKey(t *testing.T) {
	p := newRealProvider(t)

	// Simulate a joining node's own, never-shared key pair.
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	certPEM, err := IssueLeafForKey(context.Background(), p, &nodeKey.PublicKey, "spiffe://qrdb.cluster/node/10.0.0.2:9000")
	if err != nil {
		t.Fatalf("IssueLeafForKey: %v", err)
	}
	cert := decodeCert(t, certPEM)
	caCert, err := p.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if err := cert.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("issued leaf does not chain to the cluster CA: %v", err)
	}
	gotPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !gotPub.Equal(&nodeKey.PublicKey) {
		t.Fatal("issued leaf does not carry the caller-supplied public key")
	}

	// B6: the leaf must carry ONLY the bound identity's URI SAN, never the
	// SIGNING member's own hostnames/interface IPs (clustertls.LocalSANs).
	// A join leaf with the signer's addresses on it would let a peer that
	// trusts "any SAN this CA ever signed" treat the joiner as reachable at
	// addresses that are actually the admitted member that happened to
	// process its join request.
	if len(cert.DNSNames) != 0 {
		t.Fatalf("join leaf carries DNS SANs %v, want none (identity-only)", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 0 {
		t.Fatalf("join leaf carries IP SANs %v, want none (identity-only)", cert.IPAddresses)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://qrdb.cluster/node/10.0.0.2:9000" {
		t.Fatalf("join leaf URIs = %v, want exactly the bound SPIFFE identity", cert.URIs)
	}
}

func TestIssueLeafForKey_NilProvider(t *testing.T) {
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, err := IssueLeafForKey(context.Background(), nil, &nodeKey.PublicKey)
	var nilErr *NilProviderError
	if !errors.As(err, &nilErr) {
		t.Fatalf("IssueLeafForKey(nil provider) = %v, want *NilProviderError", err)
	}
}

func TestIssueNodeLeaf_NilProvider(t *testing.T) {
	_, _, err := IssueNodeLeaf(context.Background(), nil)
	var nilErr *NilProviderError
	if !errors.As(err, &nilErr) {
		t.Fatalf("IssueNodeLeaf(nil provider) = %v, want *NilProviderError", err)
	}
}

// certOnlyProvider is a cakey.Provider double that returns a fixed
// certificate and a configurable signer error, for exercising clusterca's
// error-wrapping paths.
type certOnlyProvider struct {
	cert      *x509.Certificate
	signerErr error
	certErr   error
}

func (f *certOnlyProvider) Signer(context.Context) (crypto.Signer, error) {
	if f.signerErr != nil {
		return nil, f.signerErr
	}
	return nil, nil
}

func (f *certOnlyProvider) Certificate(context.Context) (*x509.Certificate, error) {
	if f.certErr != nil {
		return nil, f.certErr
	}
	return f.cert, nil
}

func TestMaterial_ProviderErrors(t *testing.T) {
	boom := errors.New("boom")

	t.Run("certificate fails", func(t *testing.T) {
		_, _, err := IssueNodeLeaf(context.Background(), &certOnlyProvider{certErr: boom})
		var provErr *ProviderError
		if !errors.As(err, &provErr) || provErr.Op != "certificate" {
			t.Fatalf("IssueNodeLeaf with failing Certificate = %v, want *ProviderError{Op: certificate}", err)
		}
	})

	t.Run("signer fails", func(t *testing.T) {
		p := newRealProvider(t)
		caCert, err := p.Certificate(context.Background())
		if err != nil {
			t.Fatalf("Certificate: %v", err)
		}
		_, _, err = IssueNodeLeaf(context.Background(), &certOnlyProvider{cert: caCert, signerErr: boom})
		var provErr *ProviderError
		if !errors.As(err, &provErr) || provErr.Op != "signer" {
			t.Fatalf("IssueNodeLeaf with failing Signer = %v, want *ProviderError{Op: signer}", err)
		}
	})
}

func TestCACertPEM(t *testing.T) {
	p := newRealProvider(t)
	out, err := CACertPEM(context.Background(), p)
	if err != nil {
		t.Fatalf("CACertPEM: %v", err)
	}
	decodeCert(t, out)
}

func TestIssueNodeLeaf_InvalidNodeURI(t *testing.T) {
	p := newRealProvider(t)
	_, _, err := IssueNodeLeaf(context.Background(), p, "://not-a-valid-uri")
	if err == nil {
		t.Fatal("IssueNodeLeaf with an invalid node URI succeeded, want an error")
	}
}

func TestIssueLeafForKey_InvalidNodeURI(t *testing.T) {
	p := newRealProvider(t)
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_, err = IssueLeafForKey(context.Background(), p, &nodeKey.PublicKey, "://not-a-valid-uri")
	if err == nil {
		t.Fatal("IssueLeafForKey with an invalid node URI succeeded, want an error")
	}
}

func TestCACertPEM_ProviderError(t *testing.T) {
	boom := errors.New("boom")
	_, err := CACertPEM(context.Background(), &certOnlyProvider{certErr: boom})
	var provErr *ProviderError
	if !errors.As(err, &provErr) || provErr.Op != "certificate" {
		t.Fatalf("CACertPEM with failing Certificate = %v, want *ProviderError{Op: certificate}", err)
	}
}

func TestCACertPEM_NilProvider(t *testing.T) {
	_, err := CACertPEM(context.Background(), nil)
	var nilErr *NilProviderError
	if !errors.As(err, &nilErr) {
		t.Fatalf("CACertPEM(nil) = %v, want *NilProviderError", err)
	}
}
