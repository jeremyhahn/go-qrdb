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
// The key hierarchy here is the REAL dek.Keyring over the real
// StoreResolver, not a recorder. Only the production path can answer "can the
// tenant's ciphertext still be decrypted afterwards?", which is the property
// the defect destroyed.

package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// tenantCrypto is a tenant's DEK keyring plus a ciphertext written under it,
// which is what a "can the tenant's data still be read?" assertion has to be
// made against.
type tenantCrypto struct {
	keyring    *dek.Keyring
	ciphertext []byte
	plaintext  []byte
}

// newTenantCrypto builds the production keyring over a StoreResolver for svc
// and seals a payload for tenantID under it.
func newTenantCrypto(t *testing.T, ctx context.Context, svc *TenantService, reg *seal.BarrierRegistry, tenantID string) *tenantCrypto {
	t.Helper()
	resolver, err := NewStoreResolver(kvstore.NewMemoryKVStore(), reg, svc)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	keyring, err := dek.NewKeyring(resolver)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	plaintext := []byte("the tenant's data")
	sealed, err := keyring.Encrypt(ctx, tenantID, plaintext, nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return &tenantCrypto{keyring: keyring, plaintext: plaintext, ciphertext: sealed.Ciphertext}
}

// decrypts reports whether tenantID's keyring can still read the payload
// written under it.
func (c *tenantCrypto) decrypts(ctx context.Context, t *testing.T, tenantID string) bool {
	t.Helper()
	opened, err := c.keyring.Decrypt(ctx, tenantID, c.ciphertext, nil)
	if err != nil {
		return false
	}
	return string(opened.Plaintext) == string(c.plaintext)
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
	crypto := newTenantCrypto(t, ctx, svc, reg, "acme")

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
		t.Error("a delete that never made the retirement durable left the tenant's keyring unusable")
	}

	// And the error told the truth: nothing changed.
	backend.failPut = false
	if _, gErr := svc.GetTenant(ctx, "acme"); gErr != nil {
		t.Errorf("the tenant must still be there: %v", gErr)
	}
}

// TestDeleteTenant_HappyPathStillFullyRetires is the other half of the
// reordering: fencing first must not weaken a delete that succeeds. The
// keyring is unusable, the id is retired, the metadata is gone and the
// keyspace is reclaimed -- all of it, in one call.
func TestDeleteTenant_HappyPathStillFullyRetires(t *testing.T) {
	ctx := context.Background()
	svc, reg := newTestService(t)
	reclaim := &recordingReclaimer{}
	svc.SetKeyspaceReclaimer(reclaim)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	crypto := newTenantCrypto(t, ctx, svc, reg, "acme")

	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	if crypto.decrypts(ctx, t, "acme") {
		t.Error("a successful delete must leave the tenant's keyring unusable")
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
// the reordering deliberately allows -- a retired id whose keyspace is still
// there -- and asserts it is resumable rather than wedged.
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
	crypto := newTenantCrypto(t, ctx, svc, reg, "acme")

	var te *TeardownError
	if err := svc.DeleteTenant(ctx, "acme"); !errors.As(err, &te) {
		t.Fatalf("DeleteTenant = %v, want a TeardownError", err)
	}

	// The fence held; only the reclamation is outstanding, and the residue
	// it leaves is reachable by nobody: the keyring no longer resolves.
	if _, tombstoned, _ := svc.deletedAt(ctx, "acme"); !tombstoned {
		t.Error("the id must be retired after a failure past the fence")
	}
	if crypto.decrypts(ctx, t, "acme") {
		t.Error("a retired tenant's keyring must be unusable even before the reclamation finishes")
	}

	// Re-issuing finishes it.
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
		svc.SetKeyspaceReclaimer(&recordingReclaimer{err: errStoreFault})
		if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant: %v", err)
		}

		err := svc.DeleteTenant(ctx, "acme")
		if err == nil {
			t.Fatal("DeleteTenant must fail")
		}
		msg := err.Error()
		for _, want := range []string{"acme", "retired", "no longer resolvable", "re-issue DeleteTenant", "keyspace reclaim"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message %q does not carry %q; an operator who reads it as "+
					"\"nothing changed\" leaves a half-torn-down tenant behind", msg, want)
			}
		}
	})
}
