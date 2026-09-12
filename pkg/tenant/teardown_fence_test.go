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

// This file covers go-qrdb#483: DeleteTenant shredded the DEK before the
// retirement was durable, so a failed tombstone write left a live, listed,
// resolvable tenant whose every byte was permanently undecryptable -- under an
// error that read as "the delete did not happen".
//
// The assertions here are about the IRREVERSIBLE step and the sentence the
// operator reads, which is why they are separated from tombstone_test.go: that
// file asks what the tombstone means, this one asks what survives a failure.
//
// The DEK shredder here is the REAL dek.TenantDEKManager over a real barrier,
// not a recorder. A double records that DeleteTenantDEK was called; only the
// production key hierarchy can answer "does the tenant's ciphertext still
// decrypt afterwards?", which is the property the defect destroyed.

package tenant

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// tenantCrypto is a tenant's DEK plus a ciphertext written under it, which is
// what a "did the shred run?" assertion has to be made against.
type tenantCrypto struct {
	manager    dek.TenantDEKManager
	ciphertext []byte
	plaintext  []byte
}

// newTenantCrypto builds a real tenant DEK manager over a real barrier,
// issues tenantID a DEK and seals a payload under it.
func newTenantCrypto(t *testing.T, ctx context.Context, reg *seal.BarrierRegistry, tenantID string) *tenantCrypto {
	t.Helper()

	// newTestBarrierRegistry leaves the system barrier sealed; the key
	// hierarchy the DEK manager wraps with starts one level below it.
	system := reg.SystemBarrier()
	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("system-pw")}); err != nil &&
		!errors.Is(err, seal.ErrAlreadyInitialized) {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	cluster, err := dek.NewClusterDEKManager(system, testLogger())
	if err != nil {
		t.Fatalf("NewClusterDEKManager: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Close() })
	if err := cluster.Initialize(ctx, dek.AlgorithmAES256GCM, dek.AESKeySize); err != nil {
		t.Fatalf("ClusterDEKManager.Initialize: %v", err)
	}

	mgr, err := dek.NewTenantDEKManager(cluster, seal.NewMemoryBackend(), testLogger(), nil)
	if err != nil {
		t.Fatalf("NewTenantDEKManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	key, err := mgr.GetOrCreateTenantDEK(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetOrCreateTenantDEK: %v", err)
	}

	plaintext := []byte("the tenant's data")
	return &tenantCrypto{manager: mgr, plaintext: plaintext, ciphertext: sealWith(t, key, plaintext)}
}

// decrypts reports whether tenantID's DEK is still able to read the payload
// written under it. A shredded DEK makes this false, permanently.
func (c *tenantCrypto) decrypts(ctx context.Context, t *testing.T, tenantID string) bool {
	t.Helper()
	key, err := c.manager.GetTenantDEK(ctx, tenantID)
	if err != nil {
		return false
	}
	got, err := openWith(key, c.ciphertext)
	if err != nil {
		return false
	}
	return string(got) == string(c.plaintext)
}

// sealWith encrypts plaintext under key with AES-256-GCM, nonce prefixed.
//
// The nonce is the AEAD's, not this helper's. NewGCMWithRandomNonce generates
// it and prefixes it to the ciphertext itself -- the same framing this helper
// used to hand-roll, and the only one GODEBUG=fips140=only admits, because
// FIPS 140-3 forbids caller-chosen GCM IVs.
func sealWith(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	return gcmFor(t, key).Seal(nil, nil, plaintext, nil)
}

// openWith is sealWith's inverse. The nonce travels inside the ciphertext, so
// there is no prefix to split off and no length guard to get wrong.
func openWith(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func gcmFor(t *testing.T, key []byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatalf("cipher.NewGCMWithRandomNonce: %v", err)
	}
	return gcm
}

// TestDeleteTenant_FailedTombstoneLeavesTheDEKAbleToDecrypt is go-qrdb#483's
// failing-first test, measured against the real key hierarchy.
//
// Before the reordering this failed on its central assertion: the shred ran
// first, so by the time the tombstone write failed the tenant's ciphertext was
// unreadable forever -- while GetTenant still resolved the tenant and the
// error said only "put-tombstone failed".
func TestDeleteTenant_FailedTombstoneLeavesTheDEKAbleToDecrypt(t *testing.T) {
	ctx := context.Background()
	backend := &failingBackend{inner: seal.NewMemoryBackend()}
	svc, reg := newTestServiceWithStore(t, backend)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	crypto := newTenantCrypto(t, ctx, reg, "acme")
	svc.SetDEKShredder(crypto.manager)

	if !crypto.decrypts(ctx, t, "acme") {
		t.Fatal("fixture: the tenant's data must decrypt before the delete")
	}

	backend.failPut = true
	err := svc.DeleteTenant(ctx, "acme")
	var se *StoreError
	if !errors.As(err, &se) || se.Op != "put-tombstone" {
		t.Fatalf("DeleteTenant = %v, want a StoreError{Op: put-tombstone}", err)
	}

	if !crypto.decrypts(ctx, t, "acme") {
		t.Error("the DEK was shredded by a delete that never made the retirement durable; " +
			"the tenant's data is now permanently undecryptable")
	}

	// And the error told the truth: nothing changed.
	backend.failPut = false
	if _, gErr := svc.GetTenant(ctx, "acme"); gErr != nil {
		t.Errorf("the tenant must still be there: %v", gErr)
	}
}

// TestDeleteTenant_HappyPathStillFullyRetires is the other half of the
// reordering: fencing first must not weaken a delete that succeeds. The DEK is
// destroyed, the id is retired, the metadata is gone and the keyspace is
// reclaimed -- all of it, in one call.
func TestDeleteTenant_HappyPathStillFullyRetires(t *testing.T) {
	ctx := context.Background()
	svc, reg := newTestService(t)
	reclaim := &recordingReclaimer{}
	svc.SetKeyspaceReclaimer(reclaim)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	crypto := newTenantCrypto(t, ctx, reg, "acme")
	svc.SetDEKShredder(crypto.manager)

	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	if crypto.decrypts(ctx, t, "acme") {
		t.Error("a successful delete must have destroyed the DEK")
	}
	if _, err := svc.GetTenant(ctx, "acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("GetTenant after delete = %v, want ErrTenantNotFound", err)
	}
	if _, tombstoned, _ := svc.deletedAt(ctx, "acme"); !tombstoned {
		t.Error("a successful delete must retire the id")
	}
	var deleted *TenantDeletedError
	if _, err := svc.CreateTenant(ctx, "acme", "Acme Two", IsolationPrefix); !errors.As(err, &deleted) {
		t.Errorf("the retired id must be refused, got %v", err)
	}
	if len(reclaim.calls) != 1 {
		t.Errorf("reclaim calls = %v, want exactly one", reclaim.calls)
	}
}

// TestDeleteTenant_ResumeAfterAMidTeardownFailureCompletes drives the state
// the reordering deliberately allows -- a retired id whose DEK is still there
// -- and asserts it is resumable rather than wedged.
//
// This is the trade fencing first accepts, and the reason it is acceptable is
// asserted here rather than argued: the residue is reachable by nobody and one
// re-issued call removes it.
func TestDeleteTenant_ResumeAfterAMidTeardownFailureCompletes(t *testing.T) {
	ctx := context.Background()
	svc, reg := newTestService(t)
	reclaim := &recordingReclaimer{err: errStoreFault}
	svc.SetKeyspaceReclaimer(reclaim)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	crypto := newTenantCrypto(t, ctx, reg, "acme")
	svc.SetDEKShredder(crypto.manager)

	var te *TeardownError
	if err := svc.DeleteTenant(ctx, "acme"); !errors.As(err, &te) {
		t.Fatalf("DeleteTenant = %v, want a TeardownError", err)
	}

	// The fence held and the shred ran; only the reclamation is outstanding.
	if _, tombstoned, _ := svc.deletedAt(ctx, "acme"); !tombstoned {
		t.Error("the id must be retired after a failure past the fence")
	}
	if crypto.decrypts(ctx, t, "acme") {
		t.Error("the shred runs before the reclamation, so it must already have happened")
	}

	// Re-issuing finishes it. The shred re-runs over a key that is already
	// gone, which is the idempotence DEKShredder requires.
	reclaim.err = nil
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("re-issued DeleteTenant: %v", err)
	}
	if len(reclaim.calls) != 2 {
		t.Errorf("reclaim calls = %v, want the resume to have run it again", reclaim.calls)
	}
}

// TestDeleteTenant_ErrorsSayWhatIsAlreadyIrreversible is the assertion that
// matters to a human and that neither ordering test covers.
//
// go-qrdb#483's sharp edge was not the ordering by itself, it was that
// "put-tombstone failed" reads as "nothing changed" and, under the old
// ordering, was false about exactly the part that could not be undone. So each
// message is asserted on which side of the fence it comes from.
func TestDeleteTenant_ErrorsSayWhatIsAlreadyIrreversible(t *testing.T) {
	ctx := context.Background()

	t.Run("before the fence the message claims nothing", func(t *testing.T) {
		backend := &failingBackend{inner: seal.NewMemoryBackend()}
		svc, _ := newTestServiceWithStore(t, backend)
		if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant: %v", err)
		}
		backend.failPut = true

		err := svc.DeleteTenant(ctx, "acme")
		if err == nil {
			t.Fatal("DeleteTenant must fail")
		}
		var te *TeardownError
		if errors.As(err, &te) {
			t.Errorf("a fence failure must not be reported as a teardown past the fence: %v", err)
		}
		if !strings.Contains(err.Error(), "put-tombstone") {
			t.Errorf("message %q does not name the step that failed", err.Error())
		}
	})

	t.Run("past the fence the message names the retirement and the remedy", func(t *testing.T) {
		svc, _ := newTestService(t)
		svc.SetDEKShredder(&shredRecorder{err: errStoreFault})
		if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant: %v", err)
		}

		err := svc.DeleteTenant(ctx, "acme")
		if err == nil {
			t.Fatal("DeleteTenant must fail")
		}
		msg := err.Error()
		for _, want := range []string{"acme", "retired", "no longer resolvable", "re-issue DeleteTenant", "dek shred"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message %q does not carry %q; an operator who reads it as "+
					"\"nothing changed\" leaves a half-torn-down tenant behind", msg, want)
			}
		}
	})
}

// TestDeleteTenantDEK_IsIdempotent pins the contract the resume path rests on,
// against the real manager rather than a double. go-qrdb#483 asked whether it
// holds; it does, because the storage delete treats not-found as success.
func TestDeleteTenantDEK_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	_, reg := newTestService(t)
	crypto := newTenantCrypto(t, ctx, reg, "acme")

	if err := crypto.manager.DeleteTenantDEK(ctx, "acme"); err != nil {
		t.Fatalf("first DeleteTenantDEK: %v", err)
	}
	if err := crypto.manager.DeleteTenantDEK(ctx, "acme"); err != nil {
		t.Fatalf("second DeleteTenantDEK must be success, got %v", err)
	}
	if err := crypto.manager.DeleteTenantDEK(ctx, "never-existed"); err != nil {
		t.Fatalf("shredding a tenant that never had a DEK must be success, got %v", err)
	}
}
