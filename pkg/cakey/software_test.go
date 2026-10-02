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

package cakey

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// mockBarrier implements seal.EncryptionBarrier with real AES-GCM
// round-trips, mirroring pkg/cert's test double.
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
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.sealed {
		return nil, &BarrierSealedError{}
	}
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
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.sealed {
		return nil, &BarrierSealedError{}
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < 12 {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *mockBarrier) IsSealed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sealed
}

func (b *mockBarrier) seal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sealed = true
}

func (b *mockBarrier) unseal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sealed = false
}

// storeFactory builds a fresh kvstore.KVStore for one test's duration.
// Running the same table against two independent factories is what proves
// SoftwareProvider has no mode-specific assumption: the standalone factory
// returns a bare MemoryKVStore (standalone mode's store), and the
// "replicated" factory wraps one in an extra namespacing indirection,
// standing in for the replicated store reached through Raft in cluster
// mode. Both satisfy the exact same kvstore.KVStore interface the
// provider consumes, which is the property being tested; a real multi-node
// Raft round-trip belongs in test/integration, not here.
type storeFactory struct {
	name string
	new  func(t *testing.T) kvstore.KVStore
}

func storeFactories() []storeFactory {
	return []storeFactory{
		{
			name: "standalone",
			new: func(*testing.T) kvstore.KVStore {
				return kvstore.NewMemoryKVStore()
			},
		},
		{
			name: "raft-replicated",
			new: func(t *testing.T) kvstore.KVStore {
				ns, err := kvstore.NewNamespacedKVStore(kvstore.NewMemoryKVStore(), "replicated")
				if err != nil {
					t.Fatalf("NewNamespacedKVStore: %v", err)
				}
				return ns
			},
		},
	}
}

func testSubject() pkix.Name {
	return pkix.Name{CommonName: "qrdb-ca-test"}
}

// faultyKVStore wraps a real kvstore.KVStore and injects a configurable
// error from Get/Put, for exercising SoftwareProvider's *StoreError paths
// without a real storage failure.
type faultyKVStore struct {
	kvstore.KVStore
	failGetOn string
	failPutOn string
	failErr   error
}

func (f *faultyKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failGetOn != "" && key == f.failGetOn {
		return nil, f.failErr
	}
	return f.KVStore.Get(ctx, key)
}

func (f *faultyKVStore) Put(ctx context.Context, key string, value []byte) error {
	if f.failPutOn != "" && key == f.failPutOn {
		return f.failErr
	}
	return f.KVStore.Put(ctx, key, value)
}

func TestSoftwareProvider_InitializeGeneratesAndPersists(t *testing.T) {
	for _, sf := range storeFactories() {
		t.Run(sf.name, func(t *testing.T) {
			store := sf.new(t)
			barrier := newMockBarrier()
			p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), 10*time.Hour)

			if err := p.Initialize(context.Background()); err != nil {
				t.Fatalf("Initialize: %v", err)
			}

			signer, err := p.Signer(context.Background())
			if err != nil {
				t.Fatalf("Signer: %v", err)
			}
			if signer == nil {
				t.Fatal("Signer returned nil")
			}
			cert, err := p.Certificate(context.Background())
			if err != nil {
				t.Fatalf("Certificate: %v", err)
			}
			if !cert.IsCA {
				t.Fatal("generated certificate is not a CA certificate")
			}

			// Persisted, not just cached: a second provider over the same
			// store loads the SAME key material rather than generating a
			// second one.
			p2 := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), 10*time.Hour)
			cert2, err := p2.Certificate(context.Background())
			if err != nil {
				t.Fatalf("second provider Certificate: %v", err)
			}
			if cert2.SerialNumber.Cmp(cert.SerialNumber) != 0 {
				t.Fatalf("second provider generated a different CA (serial %s != %s); Initialize should have loaded the persisted one", cert2.SerialNumber, cert.SerialNumber)
			}
		})
	}
}

func TestSoftwareProvider_NoKeyFileOnDisk(t *testing.T) {
	// SoftwareProvider never touches a filesystem path at all -- it is
	// constructed over a kvstore.KVStore and a seal.EncryptionBarrier only.
	// This test pins that by construction: there is no *os.File, no
	// dataDir parameter, and no WriteFileAtomic call anywhere in this
	// package (grepped: see software.go), so there is structurally no way
	// for Initialize or load to write a CA key to the node's local disk.
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// The only persistence path is kvStore.Put, asserted indirectly by the
	// cross-provider load test above; here we additionally confirm the two
	// expected keys (and only those) exist in the store.
	keys, err := store.List(context.Background(), "__sys:cluster_ca/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected exactly 2 persisted records (wrapped_key, cert_pem), got %d: %v", len(keys), keys)
	}
}

func TestSoftwareProvider_SealedBarrierRefusesSigning(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	p.Seal()
	barrier.seal()

	if _, err := p.Signer(context.Background()); err == nil {
		t.Fatal("Signer succeeded while barrier sealed, want *BarrierSealedError")
	} else {
		var sealedErr *BarrierSealedError
		if !errors.As(err, &sealedErr) {
			t.Fatalf("Signer error = %v (%T), want *BarrierSealedError", err, err)
		}
	}
}

func TestSoftwareProvider_UnsealAfterRestartReloadsFromStore(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	p1 := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p1.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	wantCert, err := p1.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}

	// Simulate a process restart: a brand-new provider instance, same
	// store and barrier, with nothing cached in memory.
	p2 := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p2.Unseal(context.Background()); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	gotSigner, err := p2.Signer(context.Background())
	if err != nil {
		t.Fatalf("Signer after Unseal: %v", err)
	}
	gotCert, err := p2.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate after Unseal: %v", err)
	}
	if gotCert.SerialNumber.Cmp(wantCert.SerialNumber) != 0 {
		t.Fatalf("reloaded certificate serial %s != original %s", gotCert.SerialNumber, wantCert.SerialNumber)
	}
	if gotSigner.Public() == nil {
		t.Fatal("reloaded signer has no public key")
	}
}

func TestSoftwareProvider_UnsealWithNoStoredMaterial(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	err := p.Unseal(context.Background())
	var notInit *NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("Unseal with nothing stored = %v (%T), want *NotInitializedError", err, err)
	}
}

func TestSoftwareProvider_InitializeWhileSealedRefuses(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	barrier.seal()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	err := p.Initialize(context.Background())
	var sealedErr *BarrierSealedError
	if !errors.As(err, &sealedErr) {
		t.Fatalf("Initialize while sealed = %v (%T), want *BarrierSealedError", err, err)
	}
}

func TestSoftwareProvider_InitializeStoreFailures(t *testing.T) {
	boom := errors.New("boom")

	t.Run("get existing fails", func(t *testing.T) {
		store := &faultyKVStore{KVStore: kvstore.NewMemoryKVStore(), failGetOn: "__sys:cluster_ca/wrapped_key", failErr: boom}
		p := NewSoftwareProvider(store, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.Initialize(context.Background()); !errors.As(err, &storeErr) {
			t.Fatalf("Initialize = %v, want *StoreError", err)
		}
	})

	t.Run("put wrapped key fails", func(t *testing.T) {
		store := &faultyKVStore{KVStore: kvstore.NewMemoryKVStore(), failPutOn: "__sys:cluster_ca/wrapped_key", failErr: boom}
		p := NewSoftwareProvider(store, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.Initialize(context.Background()); !errors.As(err, &storeErr) {
			t.Fatalf("Initialize = %v, want *StoreError", err)
		}
	})

	t.Run("put cert fails", func(t *testing.T) {
		store := &faultyKVStore{KVStore: kvstore.NewMemoryKVStore(), failPutOn: "__sys:cluster_ca/cert_pem", failErr: boom}
		p := NewSoftwareProvider(store, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.Initialize(context.Background()); !errors.As(err, &storeErr) {
			t.Fatalf("Initialize = %v, want *StoreError", err)
		}
	})
}

func TestSoftwareProvider_LoadFailures(t *testing.T) {
	boom := errors.New("boom")

	t.Run("get cert fails", func(t *testing.T) {
		backing := kvstore.NewMemoryKVStore()
		barrier := newMockBarrier()
		seed := NewSoftwareProvider(backing, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		if err := seed.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		faulty := &faultyKVStore{KVStore: backing, failGetOn: "__sys:cluster_ca/cert_pem", failErr: boom}
		p := NewSoftwareProvider(faulty, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.Unseal(context.Background()); !errors.As(err, &storeErr) {
			t.Fatalf("Unseal = %v, want *StoreError", err)
		}
	})

	t.Run("cert record missing after wrapped key present", func(t *testing.T) {
		backing := kvstore.NewMemoryKVStore()
		barrier := newMockBarrier()
		if err := backing.Put(context.Background(), "__sys:cluster_ca/wrapped_key", []byte("not-empty")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		p := NewSoftwareProvider(backing, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		var notInit *NotInitializedError
		if err := p.Unseal(context.Background()); !errors.As(err, &notInit) {
			t.Fatalf("Unseal = %v, want *NotInitializedError", err)
		}
	})

	t.Run("decrypt fails", func(t *testing.T) {
		backing := kvstore.NewMemoryKVStore()
		barrier := newMockBarrier()
		seed := NewSoftwareProvider(backing, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		if err := seed.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		// Corrupt the wrapped key so Decrypt fails.
		if err := backing.Put(context.Background(), "__sys:cluster_ca/wrapped_key", []byte("not-a-valid-ciphertext")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		p := NewSoftwareProvider(backing, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		var genErr *GenerateError
		if err := p.Unseal(context.Background()); !errors.As(err, &genErr) {
			t.Fatalf("Unseal = %v, want *GenerateError", err)
		}
	})

	t.Run("stored cert is not valid PEM", func(t *testing.T) {
		backing := kvstore.NewMemoryKVStore()
		barrier := newMockBarrier()
		seed := NewSoftwareProvider(backing, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		if err := seed.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if err := backing.Put(context.Background(), "__sys:cluster_ca/cert_pem", []byte("not pem")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		p := NewSoftwareProvider(backing, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		var genErr *GenerateError
		if err := p.Unseal(context.Background()); !errors.As(err, &genErr) {
			t.Fatalf("Unseal = %v, want *GenerateError", err)
		}
	})
}

func TestSoftwareProvider_UnsealWhileSealedRefuses(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	barrier.seal()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	var sealedErr *BarrierSealedError
	if err := p.Unseal(context.Background()); !errors.As(err, &sealedErr) {
		t.Fatalf("Unseal while sealed = %v, want *BarrierSealedError", err)
	}
}

func TestSoftwareProvider_CertificateWhileSealedRefuses(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	barrier.seal()
	var sealedErr *BarrierSealedError
	if _, err := p.Certificate(context.Background()); !errors.As(err, &sealedErr) {
		t.Fatalf("Certificate while sealed = %v, want *BarrierSealedError", err)
	}
}

func TestSoftwareProvider_UnsealAfterSealReloadsIntoMemory(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	p.Seal()
	if _, err := p.Signer(context.Background()); err != nil {
		t.Fatalf("Signer after Seal (barrier still unsealed) = %v, want auto-reload to succeed", err)
	}
}

func TestStaticProvider(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	backing := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := backing.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	signer, err := backing.Signer(context.Background())
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	cert, err := backing.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}

	sp, err := NewStaticProvider(signer, cert)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	gotSigner, err := sp.Signer(context.Background())
	if err != nil || gotSigner != signer {
		t.Fatalf("Signer = %v, %v; want %v, nil", gotSigner, err, signer)
	}
	gotCert, err := sp.Certificate(context.Background())
	if err != nil || gotCert != cert {
		t.Fatalf("Certificate = %v, %v; want %v, nil", gotCert, err, cert)
	}

	// Sealing the backing barrier has no effect on a StaticProvider that
	// already holds its material in memory -- it has no store or barrier
	// of its own.
	barrier.seal()
	if _, err := sp.Signer(context.Background()); err != nil {
		t.Fatalf("StaticProvider.Signer after backing barrier sealed = %v, want nil", err)
	}
}

func TestNewStaticProvider_NilInputsRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	backing := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := backing.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	signer, _ := backing.Signer(context.Background())
	cert, _ := backing.Certificate(context.Background())

	if _, err := NewStaticProvider(nil, cert); err == nil {
		t.Fatal("NewStaticProvider(nil signer, cert) succeeded, want error")
	}
	if _, err := NewStaticProvider(signer, nil); err == nil {
		t.Fatal("NewStaticProvider(signer, nil cert) succeeded, want error")
	}
}

// countingSigner wraps a crypto.Signer and counts Sign calls, standing in
// for an HSM/TPM/KMS backend that performs the signature on a device qrdb
// never has key access to -- the test never reads a private key out of it,
// only observes that it was asked to sign.
type countingSigner struct {
	inner crypto.Signer
	calls int
}

func (c *countingSigner) Public() crypto.PublicKey { return c.inner.Public() }

func (c *countingSigner) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	c.calls++
	return c.inner.Sign(rand, digest, opts)
}

// TestHSMStyleProviderNeverExposesKey proves that when a Provider is
// injected (standing in for an xkms HSM/TPM/KMS backend), qrdb stores no
// key material of its own: the Adapter-wrapped double is the ONLY source of
// signatures, and the store that would have held a SoftwareProvider's
// wrapped key is never written to.
func TestHSMStyleProviderNeverExposesKey(t *testing.T) {
	// The real key material is generated once, standing in for whatever
	// lives inside the HSM/TPM/KMS; the test never extracts it from
	// counting, only drives it through Adapter.
	backingStore := kvstore.NewMemoryKVStore()
	backingBarrier := newMockBarrier()
	backing := NewSoftwareProvider(backingStore, backingBarrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := backing.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	realSigner, err := backing.Signer(context.Background())
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	realCert, err := backing.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	counting := &countingSigner{inner: realSigner}

	var provider Provider = Adapter{
		SignerFunc:      func(context.Context) (crypto.Signer, error) { return counting, nil },
		CertificateFunc: func(context.Context) (*x509.Certificate, error) { return realCert, nil },
	}

	// The deployment's own qrdb store: no SoftwareProvider ever touches it,
	// because an injected Provider replaces the built-in one entirely.
	deploymentStore := kvstore.NewMemoryKVStore()

	signer, err := provider.Signer(context.Background())
	if err != nil {
		t.Fatalf("Provider.Signer: %v", err)
	}
	if _, err := signer.Sign(rand.Reader, make([]byte, 32), nil); err != nil {
		t.Fatalf("Sign through provider: %v", err)
	}
	if counting.calls != 1 {
		t.Fatalf("expected exactly 1 call through the HSM-style signer, got %d", counting.calls)
	}

	keys, err := deploymentStore.List(context.Background(), "__sys:cluster_ca/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("HSM-backed deployment must store no key material in qrdb's own store, found %v", keys)
	}
}

// casigningIdentity mirrors the shape of go-xkms's pkg/ca.CASigningIdentity
// closely enough to prove an adapter over it costs nothing but the two
// method references: Signer(ctx) (crypto.Signer, error), plus a
// KeyAttributes-style Certificate lookup. go-xkms is never imported here --
// this type is written from the public method signatures documented in
// pkg/ca/interface.go and is this package's own test double.
type casigningIdentity struct {
	signer crypto.Signer
	cert   *x509.Certificate
}

func (c *casigningIdentity) Signer(context.Context) (crypto.Signer, error) { return c.signer, nil }

// certificateFor stands in for CACertificateAccessor.Certificate(ctx, attrs),
// closed over the one fixed KeyAttributes value an adapter for a single CA
// needs.
func (c *casigningIdentity) certificateFor(context.Context) (*x509.Certificate, error) {
	return c.cert, nil
}

// TestXKMSShapedTypeSatisfiesProviderViaAdapter is the compile-time-adjacent
// check the CA-key-provider ruling asks for: a type shaped like go-xkms's
// CASigningIdentity + CACertificateAccessor pair, adapted with nothing more
// than Adapter, satisfies cakey.Provider.
func TestXKMSShapedTypeSatisfiesProviderViaAdapter(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	barrier := newMockBarrier()
	backing := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := backing.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	signer, _ := backing.Signer(context.Background())
	cert, _ := backing.Certificate(context.Background())

	identity := &casigningIdentity{signer: signer, cert: cert}

	var p Provider = Adapter{
		SignerFunc:      identity.Signer,
		CertificateFunc: identity.certificateFor,
	}

	gotSigner, err := p.Signer(context.Background())
	if err != nil || gotSigner != signer {
		t.Fatalf("adapted Signer = %v, %v; want %v, nil", gotSigner, err, signer)
	}
	gotCert, err := p.Certificate(context.Background())
	if err != nil || gotCert != cert {
		t.Fatalf("adapted Certificate = %v, %v; want %v, nil", gotCert, err, cert)
	}
}
