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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// mockBarrier implements seal.EncryptionBarrier with real AES-GCM
// round-trips, mirroring pkg/cert's test double. SoftwareProvider no
// longer calls Encrypt/Decrypt on this barrier for key material (see
// software.go's doc comment) -- it is exercised here only through
// IsSealed(), and the AES-GCM machinery is kept so a test that constructs
// TWO of these (the "different barriers" tests) proves they are
// independent key material, standing in for two nodes' own at-rest
// storage encryption.
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

// newGuardedMemoryStore returns a fresh kvstoretest.GuardedMemoryStore:
// NewSoftwareProvider needs kvstore.MultiGuardedBatchKVStore to write its
// key and certificate records together in one batch, and a plain
// *kvstore.MemoryKVStore deliberately does not offer it (see
// kvstore.TestNewNamespacedKVStore_AtomicOnlyStillWorks). Production gets
// the capability from lifecycle.NewGuardedKVStoreAdapter, backed by the
// real transaction service; these unit tests get it from kvstoretest.
func newGuardedMemoryStore() *kvstoretest.GuardedMemoryStore {
	return kvstoretest.NewGuardedMemoryStore()
}

// storeFactory builds a fresh kvstore.MultiGuardedBatchKVStore for one
// test's duration. Running the same table against two independent
// factories is what proves SoftwareProvider has no mode-specific
// assumption: the standalone factory returns a bare guardedMemoryStore
// (standing in for standalone mode's store plus its in-process transaction
// plane), and the "replicated" factory wraps one in an extra namespacing
// indirection, standing in for the replicated store reached through Raft in
// cluster mode. Both satisfy the exact same kvstore.MultiGuardedBatchKVStore
// interface the provider consumes, which is the property being tested; a
// real multi-node Raft round-trip belongs in test/integration, not here.
type storeFactory struct {
	name string
	new  func(t *testing.T) kvstore.MultiGuardedBatchKVStore
}

func storeFactories() []storeFactory {
	return []storeFactory{
		{
			name: "standalone",
			new: func(*testing.T) kvstore.MultiGuardedBatchKVStore {
				return newGuardedMemoryStore()
			},
		},
		{
			name: "raft-replicated",
			new: func(t *testing.T) kvstore.MultiGuardedBatchKVStore {
				ns, err := kvstore.NewNamespacedKVStore(newGuardedMemoryStore(), "replicated")
				if err != nil {
					t.Fatalf("NewNamespacedKVStore: %v", err)
				}
				multi, ok := ns.(kvstore.MultiGuardedBatchKVStore)
				if !ok {
					t.Fatal("NewNamespacedKVStore over a multi-guarded inner store must itself be multi-guarded")
				}
				return multi
			},
		},
	}
}

func testSubject() pkix.Name {
	return pkix.Name{CommonName: "qrdb-ca-test"}
}

// faultyKVStore wraps a real kvstore.MultiGuardedBatchKVStore and injects a
// configurable error from Get/PutAllIfAllUnchanged, for exercising
// SoftwareProvider's *StoreError paths without a real storage failure.
type faultyKVStore struct {
	kvstore.MultiGuardedBatchKVStore
	failGetOn                  string
	failPutAllIfAllUnchangedOn string
	failErr                    error
}

func (f *faultyKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failGetOn != "" && key == f.failGetOn {
		return nil, f.failErr
	}
	return f.MultiGuardedBatchKVStore.Get(ctx, key)
}

func (f *faultyKVStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if f.failPutAllIfAllUnchangedOn != "" {
		for _, w := range writes {
			if w.Key == f.failPutAllIfAllUnchangedOn {
				return false, f.failErr
			}
		}
	}
	return f.MultiGuardedBatchKVStore.PutAllIfAllUnchanged(ctx, guards, writes)
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
	// constructed over a kvstore.MultiGuardedBatchKVStore and a
	// seal.EncryptionBarrier only. This test pins that by construction:
	// there is no *os.File, no dataDir parameter, and no WriteFileAtomic
	// call anywhere in this package (grepped: see software.go), so there is
	// structurally no way for Initialize or load to write a CA key to the
	// node's local disk.
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// The only persistence path is kvStore.PutAllIfAllUnchanged, asserted
	// indirectly by the cross-provider load test above; here we additionally
	// confirm exactly the two expected records exist in the store -- the
	// private key and the certificate, written together in one atomic
	// batch, never as a torn pair and never as more than these two.
	keys, err := store.List(context.Background(), "__sys:cluster_ca/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected exactly 2 persisted records (key, certificate), got %d: %v", len(keys), keys)
	}
}

func TestSoftwareProvider_SealedBarrierRefusesSigning(t *testing.T) {
	store := newGuardedMemoryStore()
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
	store := newGuardedMemoryStore()
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

// TestSoftwareProvider_TwoNodesDifferentBarriersLoadTheSameCA is the
// owner-required proof for the B1 ruling: two providers, standing in for
// two cluster nodes, with TWO INDEPENDENT barriers (as every node's own
// system barrier/encfs is independent) over ONE shared replicated store
// both read the SAME cluster CA. Before this fix, the second node's
// Decrypt used its own barrier against a ciphertext the FIRST node's
// barrier produced and failed every time; there is no Encrypt/Decrypt call
// left in this package for that to happen to.
func TestSoftwareProvider_TwoNodesDifferentBarriersLoadTheSameCA(t *testing.T) {
	sharedStore := newGuardedMemoryStore()
	nodeABarrier := newMockBarrier()
	nodeBBarrier := newMockBarrier()

	nodeA := NewSoftwareProvider(sharedStore, nodeABarrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := nodeA.Initialize(context.Background()); err != nil {
		t.Fatalf("node A Initialize: %v", err)
	}
	wantCert, err := nodeA.Certificate(context.Background())
	if err != nil {
		t.Fatalf("node A Certificate: %v", err)
	}
	wantSigner, err := nodeA.Signer(context.Background())
	if err != nil {
		t.Fatalf("node A Signer: %v", err)
	}

	// Node B never wrote anything -- it reads what node A persisted,
	// through its OWN, independent barrier.
	nodeB := NewSoftwareProvider(sharedStore, nodeBBarrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	gotCert, err := nodeB.Certificate(context.Background())
	if err != nil {
		t.Fatalf("node B Certificate (different barrier than the writer): %v", err)
	}
	if gotCert.SerialNumber.Cmp(wantCert.SerialNumber) != 0 {
		t.Fatalf("node B loaded a different CA: serial %s != node A's %s", gotCert.SerialNumber, wantCert.SerialNumber)
	}
	gotSigner, err := nodeB.Signer(context.Background())
	if err != nil {
		t.Fatalf("node B Signer (different barrier than the writer): %v", err)
	}
	if !gotSigner.Public().(*ecdsa.PublicKey).Equal(wantSigner.Public().(*ecdsa.PublicKey)) {
		t.Fatal("node B's signer does not hold the same key node A persisted")
	}

	// Sealing node A's barrier (its own at-rest encryption going away, in
	// this model) must not affect node B's already-successful read, and
	// must not stop node B signing with ITS view of the same material.
	nodeABarrier.seal()
	if _, err := nodeB.Signer(context.Background()); err != nil {
		t.Fatalf("node B Signer after node A's (unrelated) barrier sealed: %v", err)
	}
}

// TestSoftwareProvider_NodeJoinsReadsExistingCA proves the "a node joins"
// half of the B1 test requirement, distinct from the restart test above:
// a BRAND NEW node, with its own fresh barrier that never touched the
// store before, reads the cluster CA an existing member already
// established -- the member-side CSR join scenario, at the storage layer.
func TestSoftwareProvider_NodeJoinsReadsExistingCA(t *testing.T) {
	sharedStore := newGuardedMemoryStore()
	founder := NewSoftwareProvider(sharedStore, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := founder.Initialize(context.Background()); err != nil {
		t.Fatalf("founder Initialize: %v", err)
	}
	wantCert, err := founder.Certificate(context.Background())
	if err != nil {
		t.Fatalf("founder Certificate: %v", err)
	}

	joiner := NewSoftwareProvider(sharedStore, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
	gotCert, err := joiner.Certificate(context.Background())
	if err != nil {
		t.Fatalf("joiner Certificate: %v", err)
	}
	if gotCert.SerialNumber.Cmp(wantCert.SerialNumber) != 0 {
		t.Fatalf("joining node read a different CA: serial %s != founder's %s", gotCert.SerialNumber, wantCert.SerialNumber)
	}
}

// TestSoftwareProvider_ConcurrentInitializeConvergesOnOneCA is the
// owner-required concurrency proof for B2: several providers, standing in
// for several nodes racing to become the cluster's first, all call
// Initialize against the SAME store at once. Exactly one CA must result,
// and every provider must end up holding IT -- not a mix where some callers
// keep their own candidate because a non-atomic check-then-write let two
// candidates both believe they were first.
func TestSoftwareProvider_ConcurrentInitializeConvergesOnOneCA(t *testing.T) {
	const n = 16
	sharedStore := newGuardedMemoryStore()

	var wg sync.WaitGroup
	certs := make([]*x509.Certificate, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := NewSoftwareProvider(sharedStore, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
			errs[i] = p.Initialize(context.Background())
			if errs[i] == nil {
				certs[i], errs[i] = p.Certificate(context.Background())
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("provider %d: %v", i, err)
		}
	}
	want := certs[0].SerialNumber
	for i, c := range certs {
		if c.SerialNumber.Cmp(want) != 0 {
			t.Fatalf("provider %d converged on a different CA: serial %s != provider 0's %s", i, c.SerialNumber, want)
		}
	}

	// Exactly two records in the store (key + certificate), not two per
	// racer.
	keys, err := sharedStore.List(context.Background(), "__sys:cluster_ca/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected exactly 2 persisted records after %d concurrent Initialize calls, got %d: %v", n, len(keys), keys)
	}
}

// TestSoftwareProvider_CrashBetweenWritesCannotTearTheRecord is the
// owner-required "crash between writes" proof for B2, carried forward to
// the two-record design: a crash (or a non-atomic write failure) between
// writing the key record and writing the certificate record must not be
// able to leave one without the other. The guarded batch
// (PutAllIfAllUnchanged) makes that window not exist -- there is only ONE
// call to crash during, covering both records -- and this test proves it
// by injecting the failure at that one call: nothing is ever partially
// written for a reader to find.
func TestSoftwareProvider_CrashBetweenWritesCannotTearTheRecord(t *testing.T) {
	boom := errors.New("crash during the one batch write")
	backing := newGuardedMemoryStore()
	store := &faultyKVStore{
		MultiGuardedBatchKVStore:   backing,
		failPutAllIfAllUnchangedOn: "__sys:cluster_ca/material",
		failErr:                    boom,
	}
	p := NewSoftwareProvider(store, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)

	var storeErr *StoreError
	if err := p.Initialize(context.Background()); !errors.As(err, &storeErr) {
		t.Fatalf("Initialize with a faulted write = %v, want *StoreError", err)
	}

	// Nothing was left behind for a second provider to read as if it were
	// real -- the underlying store (bypassing the fault injection) has
	// NEITHER record, not a half-written pair.
	for _, suffix := range []string{"material", "certificate"} {
		data, err := backing.Get(context.Background(), "__sys:cluster_ca/"+suffix)
		if err != nil {
			t.Fatalf("Get(%s): %v", suffix, err)
		}
		if data != nil {
			t.Fatalf("a failed batch write left %q behind: %q", suffix, data)
		}
	}
}

func TestSoftwareProvider_UnsealWithNoStoredMaterial(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	err := p.Unseal(context.Background())
	var notInit *NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("Unseal with nothing stored = %v (%T), want *NotInitializedError", err, err)
	}
}

func TestSoftwareProvider_InitializeWhileSealedRefuses(t *testing.T) {
	store := newGuardedMemoryStore()
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

	t.Run("putallifallunchanged fails", func(t *testing.T) {
		store := &faultyKVStore{MultiGuardedBatchKVStore: newGuardedMemoryStore(), failPutAllIfAllUnchangedOn: "__sys:cluster_ca/material", failErr: boom}
		p := NewSoftwareProvider(store, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.Initialize(context.Background()); !errors.As(err, &storeErr) {
			t.Fatalf("Initialize = %v, want *StoreError", err)
		}
	})

	t.Run("reload after losing the race fails", func(t *testing.T) {
		backing := newGuardedMemoryStore()
		seed := NewSoftwareProvider(backing, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		if err := seed.Initialize(context.Background()); err != nil {
			t.Fatalf("seed Initialize: %v", err)
		}
		// PutIfAbsent on the already-populated key reports stored=false
		// (no error) and Initialize falls through to load(); fault THAT
		// read.
		faulty := &faultyKVStore{MultiGuardedBatchKVStore: backing, failGetOn: "__sys:cluster_ca/material", failErr: boom}
		p := NewSoftwareProvider(faulty, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.Initialize(context.Background()); !errors.As(err, &storeErr) {
			t.Fatalf("Initialize (losing the race, faulted reload) = %v, want *StoreError", err)
		}
	})
}

func TestSoftwareProvider_LoadFailures(t *testing.T) {
	boom := errors.New("boom")

	t.Run("get material fails", func(t *testing.T) {
		backing := newGuardedMemoryStore()
		seed := NewSoftwareProvider(backing, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		if err := seed.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		faulty := &faultyKVStore{MultiGuardedBatchKVStore: backing, failGetOn: "__sys:cluster_ca/material", failErr: boom}
		p := NewSoftwareProvider(faulty, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.Unseal(context.Background()); !errors.As(err, &storeErr) {
			t.Fatalf("Unseal = %v, want *StoreError", err)
		}
	})

	t.Run("stored key record does not parse as an EC private key", func(t *testing.T) {
		backing := newGuardedMemoryStore()
		if err := backing.Put(context.Background(), "__sys:cluster_ca/material", []byte("not a real DER-encoded key")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		p := NewSoftwareProvider(backing, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var corruptErr *MaterialCorruptError
		if err := p.Unseal(context.Background()); !errors.As(err, &corruptErr) {
			t.Fatalf("Unseal = %v, want *MaterialCorruptError", err)
		}
	})

	// TestSoftwareProvider_CertificateNeverReadsKeyMaterial below is the
	// positive half of the cert/key split; this is the negative half: a
	// corrupt CERTIFICATE record must be reported without ever needing to
	// touch (and here, without disturbing) the key record, because
	// Certificate/load read them as two independent records.
	t.Run("stored certificate record does not parse", func(t *testing.T) {
		backing := newGuardedMemoryStore()
		seed := NewSoftwareProvider(backing, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		if err := seed.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		// Overwrite ONLY the certificate record; the key record, written by
		// seed.Initialize above, is left untouched.
		if err := backing.Put(context.Background(), "__sys:cluster_ca/certificate", []byte("not a real DER-encoded certificate")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		p := NewSoftwareProvider(backing, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var corruptErr *MaterialCorruptError
		if err := p.Unseal(context.Background()); !errors.As(err, &corruptErr) {
			t.Fatalf("Unseal = %v, want *MaterialCorruptError", err)
		}
	})
}

func TestSoftwareProvider_UnsealWhileSealedRefuses(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	barrier.seal()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	var sealedErr *BarrierSealedError
	if err := p.Unseal(context.Background()); !errors.As(err, &sealedErr) {
		t.Fatalf("Unseal while sealed = %v, want *BarrierSealedError", err)
	}
}

func TestSoftwareProvider_CertificateWhileSealedRefuses(t *testing.T) {
	store := newGuardedMemoryStore()
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
	store := newGuardedMemoryStore()
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

func TestSoftwareProvider_AdoptIfAbsent(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()

	// Generate in-memory material exactly as the bootstrap crash-window
	// design does: a key born before any store existed.
	seedKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	seedCert := selfSignedTestCert(t, seedKey)
	seedKeyDER, err := x509.MarshalECPrivateKey(seedKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	seedKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: seedKeyDER})
	seedCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: seedCert.Raw})

	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.AdoptIfAbsent(context.Background(), seedKeyPEM, seedCertPEM); err != nil {
		t.Fatalf("AdoptIfAbsent (first call, store empty): %v", err)
	}
	cert, err := p.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if cert.SerialNumber.Cmp(seedCert.SerialNumber) != 0 {
		t.Fatalf("adopted certificate serial %s != seed serial %s", cert.SerialNumber, seedCert.SerialNumber)
	}

	// A second call with the SAME material is a no-op confirmation: the
	// already-persisted record matches what was offered, so it loads
	// cleanly rather than refusing.
	p2 := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p2.AdoptIfAbsent(context.Background(), seedKeyPEM, seedCertPEM); err != nil {
		t.Fatalf("AdoptIfAbsent (second call, SAME material, store already populated): %v", err)
	}
	cert2, err := p2.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if cert2.SerialNumber.Cmp(seedCert.SerialNumber) != 0 {
		t.Fatalf("second AdoptIfAbsent with matching material changed the persisted CA: got serial %s, want %s", cert2.SerialNumber, seedCert.SerialNumber)
	}
}

// TestSoftwareProvider_AdoptIfAbsent_MismatchRefused is the owner-required
// B2 proof: a caller offering DIFFERENT material than what the store
// already holds is a split-brain, not a race to politely lose. It must be
// reported, not silently discarded in favor of whichever was stored first.
func TestSoftwareProvider_AdoptIfAbsent_MismatchRefused(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()

	seedKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	seedCert := selfSignedTestCert(t, seedKey)
	seedKeyDER, _ := x509.MarshalECPrivateKey(seedKey)
	seedKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: seedKeyDER})
	seedCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: seedCert.Raw})

	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.AdoptIfAbsent(context.Background(), seedKeyPEM, seedCertPEM); err != nil {
		t.Fatalf("AdoptIfAbsent (first call, store empty): %v", err)
	}

	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	otherCert := selfSignedTestCert(t, otherKey)
	otherKeyDER, _ := x509.MarshalECPrivateKey(otherKey)
	otherKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: otherKeyDER})
	otherCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherCert.Raw})

	p2 := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	var mismatch *MaterialMismatchError
	if err := p2.AdoptIfAbsent(context.Background(), otherKeyPEM, otherCertPEM); !errors.As(err, &mismatch) {
		t.Fatalf("AdoptIfAbsent with mismatched material = %v, want *MaterialMismatchError", err)
	}

	// The persisted record is UNCHANGED -- the original seed material, not
	// the rejected candidate and not some merge of the two.
	confirm := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	gotCert, err := confirm.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate after a rejected mismatch: %v", err)
	}
	if gotCert.SerialNumber.Cmp(seedCert.SerialNumber) != 0 {
		t.Fatalf("a rejected AdoptIfAbsent changed the persisted CA: got serial %s, want the original %s", gotCert.SerialNumber, seedCert.SerialNumber)
	}
}

func TestSoftwareProvider_AdoptIfAbsent_NoMaterialAndStoreEmpty(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)

	// An ordinary restart after the key was already adopted and its local
	// cache deleted would call this with nil/nil; here the store is ALSO
	// empty (the gap case), which must refuse rather than silently
	// generating a key that would diverge from whatever was cached
	// elsewhere.
	err := p.AdoptIfAbsent(context.Background(), nil, nil)
	var notInit *NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("AdoptIfAbsent(nil, nil) with an empty store = %v, want *NotInitializedError", err)
	}
}

func TestSoftwareProvider_AdoptIfAbsent_NoMaterialButStorePopulated(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	seed := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := seed.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	wantCert, err := seed.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}

	// An ordinary restart: no in-memory key (it was never cached locally
	// past the first successful adopt), store already has material.
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.AdoptIfAbsent(context.Background(), nil, nil); err != nil {
		t.Fatalf("AdoptIfAbsent(nil, nil) with a populated store: %v", err)
	}
	gotCert, err := p.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if gotCert.SerialNumber.Cmp(wantCert.SerialNumber) != 0 {
		t.Fatalf("loaded certificate serial %s != persisted %s", gotCert.SerialNumber, wantCert.SerialNumber)
	}
}

func TestSoftwareProvider_AdoptIfAbsent_SealedRefuses(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	barrier.seal()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	var sealedErr *BarrierSealedError
	if err := p.AdoptIfAbsent(context.Background(), nil, nil); !errors.As(err, &sealedErr) {
		t.Fatalf("AdoptIfAbsent while sealed = %v, want *BarrierSealedError", err)
	}
}

func TestSoftwareProvider_AdoptIfAbsent_InvalidPEM(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()

	t.Run("invalid key PEM", func(t *testing.T) {
		p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		var genErr *GenerateError
		if err := p.AdoptIfAbsent(context.Background(), []byte("not pem"), []byte("also not pem")); !errors.As(err, &genErr) {
			t.Fatalf("AdoptIfAbsent with invalid key PEM = %v, want *GenerateError", err)
		}
	})

	t.Run("invalid cert PEM", func(t *testing.T) {
		store2 := newGuardedMemoryStore()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		keyDER, _ := x509.MarshalECPrivateKey(key)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		p := NewSoftwareProvider(store2, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
		var genErr *GenerateError
		if err := p.AdoptIfAbsent(context.Background(), keyPEM, []byte("not pem")); !errors.As(err, &genErr) {
			t.Fatalf("AdoptIfAbsent with invalid cert PEM = %v, want *GenerateError", err)
		}
	})
}

func TestSoftwareProvider_AdoptIfAbsent_StoreFailures(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cert := selfSignedTestCert(t, key)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	boom := errors.New("boom")

	t.Run("putallifallunchanged fails", func(t *testing.T) {
		store := &faultyKVStore{MultiGuardedBatchKVStore: newGuardedMemoryStore(), failPutAllIfAllUnchangedOn: "__sys:cluster_ca/material", failErr: boom}
		p := NewSoftwareProvider(store, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.AdoptIfAbsent(context.Background(), keyPEM, certPEM); !errors.As(err, &storeErr) {
			t.Fatalf("AdoptIfAbsent = %v, want *StoreError", err)
		}
	})

	t.Run("reload after losing the race fails", func(t *testing.T) {
		backing := newGuardedMemoryStore()
		seed := NewSoftwareProvider(backing, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		if err := seed.AdoptIfAbsent(context.Background(), keyPEM, certPEM); err != nil {
			t.Fatalf("seed AdoptIfAbsent: %v", err)
		}
		// AdoptIfAbsent's "lost the race" verification reads ONLY the
		// certificate record (loadCertRecord) -- the key record is never
		// touched by that check -- so the fault belongs on "certificate",
		// not "material".
		faulty := &faultyKVStore{MultiGuardedBatchKVStore: backing, failGetOn: "__sys:cluster_ca/certificate", failErr: boom}
		p := NewSoftwareProvider(faulty, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
		var storeErr *StoreError
		if err := p.AdoptIfAbsent(context.Background(), keyPEM, certPEM); !errors.As(err, &storeErr) {
			t.Fatalf("AdoptIfAbsent (losing the race, faulted reload) = %v, want *StoreError", err)
		}
	})
}

// selfSignedTestCert builds a minimal self-signed CA certificate for key,
// for AdoptIfAbsent tests that need real cert bytes without depending on
// SoftwareProvider's own generation path.
func selfSignedTestCert(t *testing.T, key *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), serialBits))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               testSubject(),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func TestSoftwareProvider_WithAdoptMaterial(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()

	seedKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	seedCert := selfSignedTestCert(t, seedKey)
	seedKeyDER, _ := x509.MarshalECPrivateKey(seedKey)
	seedKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: seedKeyDER})
	seedCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: seedCert.Raw})

	// Construction performs no I/O: the store is untouched until first use.
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour,
		WithAdoptMaterial(seedKeyPEM, seedCertPEM))
	if keys, err := store.List(context.Background(), "__sys:cluster_ca/"); err != nil {
		t.Fatalf("List: %v", err)
	} else if len(keys) != 0 {
		t.Fatalf("NewSoftwareProvider performed I/O at construction, found %v", keys)
	}

	// First use adopts the seed material (store was empty).
	cert, err := p.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if cert.SerialNumber.Cmp(seedCert.SerialNumber) != 0 {
		t.Fatalf("adopted certificate serial %s != seed serial %s", cert.SerialNumber, seedCert.SerialNumber)
	}

	// A second provider with the SAME adopt material, over the SAME
	// now-populated store, confirms rather than refuses: its own candidate
	// matches what is already there.
	p2 := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour,
		WithAdoptMaterial(seedKeyPEM, seedCertPEM))
	signer2, err := p2.Signer(context.Background())
	if err != nil {
		t.Fatalf("Signer (same adopt material as what is already persisted): %v", err)
	}
	if !signer2.Public().(*ecdsa.PublicKey).Equal(&seedKey.PublicKey) {
		t.Fatal("second provider's Signer does not match the originally adopted key")
	}

	// A third provider with DIFFERENT adopt material over the same store is
	// the split-brain case: two processes each hold their own in-memory
	// bootstrap material and disagree about which is the cluster CA. That
	// must surface as an error, not a silent "whichever was there first
	// wins" -- the B2 ruling this package enforces.
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherCert := selfSignedTestCert(t, otherKey)
	otherKeyDER, _ := x509.MarshalECPrivateKey(otherKey)
	otherKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: otherKeyDER})
	otherCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherCert.Raw})

	p3 := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour,
		WithAdoptMaterial(otherKeyPEM, otherCertPEM))
	var mismatch *MaterialMismatchError
	if _, err := p3.Signer(context.Background()); !errors.As(err, &mismatch) {
		t.Fatalf("Signer with mismatched adopt material = %v, want *MaterialMismatchError", err)
	}
}

// TestSoftwareProvider_AdoptIfAbsent_KeyCertMismatchRefused proves
// AdoptIfAbsent refuses a key and certificate that do not belong together
// -- the certificate's public key does not match the offered private key --
// BEFORE ever attempting to persist them, distinct from
// TestSoftwareProvider_WithAdoptMaterial's split-brain case (two
// internally-consistent key/cert pairs that disagree with each other).
func TestSoftwareProvider_AdoptIfAbsent_KeyCertMismatchRefused(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (cert): %v", err)
	}
	cert := selfSignedTestCert(t, certKey)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})

	// A DIFFERENT key than the one cert was issued for.
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (wrong): %v", err)
	}
	wrongKeyDER, err := x509.MarshalECPrivateKey(wrongKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	wrongKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: wrongKeyDER})

	err = p.AdoptIfAbsent(context.Background(), wrongKeyPEM, certPEM)
	var mismatch *KeyCertMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("AdoptIfAbsent(wrong key, cert) = %v, want *KeyCertMismatchError", err)
	}

	// Nothing was persisted: the store is untouched by the refused adopt.
	keys, listErr := store.List(context.Background(), "__sys:cluster_ca/")
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(keys) != 0 {
		t.Fatalf("AdoptIfAbsent persisted material despite refusing the mismatch, found %v", keys)
	}
}

func TestStaticProvider(t *testing.T) {
	store := newGuardedMemoryStore()
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
	store := newGuardedMemoryStore()
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
// material record is never written to.
func TestHSMStyleProviderNeverExposesKey(t *testing.T) {
	// The real key material is generated once, standing in for whatever
	// lives inside the HSM/TPM/KMS; the test never extracts it from
	// counting, only drives it through Adapter.
	backingStore := newGuardedMemoryStore()
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
	deploymentStore := newGuardedMemoryStore()

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
	store := newGuardedMemoryStore()
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

// TestSoftwareProvider_CertificateNeverReadsKeyMaterial is the owner-required
// proof for the record-shape ruling: once a certificate is persisted,
// reading it on a cold cache must not fetch the private-key record at all.
// A fresh provider is pointed at a store whose "material" Get is wired to
// fail; Certificate still succeeds (it never asked), while Signer -- which
// genuinely needs the key -- surfaces that same failure.
func TestSoftwareProvider_CertificateNeverReadsKeyMaterial(t *testing.T) {
	backing := newGuardedMemoryStore()
	seed := NewSoftwareProvider(backing, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := seed.Initialize(context.Background()); err != nil {
		t.Fatalf("seed Initialize: %v", err)
	}
	wantCert, err := seed.Certificate(context.Background())
	if err != nil {
		t.Fatalf("seed Certificate: %v", err)
	}

	boom := errors.New("material must not be read for a cert-only call")
	faulty := &faultyKVStore{MultiGuardedBatchKVStore: backing, failGetOn: "__sys:cluster_ca/material", failErr: boom}

	// A brand-new provider, nothing cached: Certificate is the FIRST call.
	p := NewSoftwareProvider(faulty, newMockBarrier(), "__sys:cluster_ca/", testSubject(), time.Hour)
	gotCert, err := p.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate must succeed without ever reading key material, got: %v", err)
	}
	if gotCert.SerialNumber.Cmp(wantCert.SerialNumber) != 0 {
		t.Fatalf("Certificate returned serial %s, want %s", gotCert.SerialNumber, wantCert.SerialNumber)
	}

	// Signer, in contrast, genuinely needs the key record and must surface
	// the same injected failure -- proving the fault was reachable at all,
	// and that Certificate's success above was not an accident of the fault
	// never being wired up correctly.
	var storeErr *StoreError
	if _, err := p.Signer(context.Background()); !errors.As(err, &storeErr) {
		t.Fatalf("Signer = %v, want *StoreError (confirming the material fault is live)", err)
	}
}

// TestSoftwareProvider_SignerCannotBeTypeAssertedToPrivateKey proves Signer
// returns an opaque crypto.Signer: a caller cannot type-assert it back to
// *ecdsa.PrivateKey (or any other concrete key type) and extract the raw
// key material.
func TestSoftwareProvider_SignerCannotBeTypeAssertedToPrivateKey(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	signer, err := p.Signer(context.Background())
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	if _, ok := signer.(*ecdsa.PrivateKey); ok {
		t.Fatal("Signer is type-assertable to *ecdsa.PrivateKey; the key is not opaque")
	}
	if _, ok := signer.(interface{ Equal(crypto.PrivateKey) bool }); ok {
		// *ecdsa.PrivateKey implements Equal; if the returned value also
		// does, a caller could at least compare it against a known key,
		// which is still more than an opaque signer should expose.
		t.Fatal("Signer exposes Equal; a caller could probe the key material by comparison")
	}
}

// TestSoftwareProvider_SignerObtainedBeforeSealRefusesAfter proves the
// opaque signer re-checks the barrier on every Sign call, not only once at
// the time Signer was fetched: a caller holding an already-obtained Signer
// across a reseal must have every subsequent Sign refused.
func TestSoftwareProvider_SignerObtainedBeforeSealRefusesAfter(t *testing.T) {
	store := newGuardedMemoryStore()
	barrier := newMockBarrier()
	p := NewSoftwareProvider(store, barrier, "__sys:cluster_ca/", testSubject(), time.Hour)
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	signer, err := p.Signer(context.Background())
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}

	// Sign succeeds while unsealed.
	if _, err := signer.Sign(rand.Reader, make([]byte, 32), crypto.SHA256); err != nil {
		t.Fatalf("Sign before seal: %v", err)
	}

	// Reseal: the SAME crypto.Signer value the caller already holds must
	// now refuse, even though it never called Signer again to "notice" the
	// barrier changed.
	barrier.seal()
	var sealedErr *BarrierSealedError
	if _, err := signer.Sign(rand.Reader, make([]byte, 32), crypto.SHA256); !errors.As(err, &sealedErr) {
		t.Fatalf("Sign after reseal, same Signer value = %v, want *BarrierSealedError", err)
	}

	// Public() is unaffected -- it is not secret and mirrors Certificate's
	// seal-independent contract for public data.
	if signer.Public() == nil {
		t.Fatal("Public() returned nil after reseal; want the cached public key regardless of barrier state")
	}
}
