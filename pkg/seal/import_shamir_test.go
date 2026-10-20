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

package seal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
)

// shamirBlobOf exports the first node's persisted Shamir root-key blob: what
// database creation or scaling would deliver to a replica.
func shamirBlobOf(t *testing.T, first *holderFixture) []byte {
	t.Helper()
	blob, err := first.b.ExportSealedBlob(context.Background())
	if err != nil {
		t.Fatalf("ExportSealedBlob: %v", err)
	}
	if got := PersistedStrategyFrom(context.Background(), first.b.base, first.b.config.RootKeyPath); got != StrategyShamir {
		t.Fatalf("the fixture's blob is a %q key, want shamir", got)
	}
	return blob
}

// sealedBlobUnder returns a structurally valid sealed blob naming strategy.
func sealedBlobUnder(t *testing.T, strategy StrategyID) []byte {
	t.Helper()
	var key SealedRootKey
	if err := json.Unmarshal(buildValidSealedBlob(t), &key); err != nil {
		t.Fatal(err)
	}
	key.Strategy = strategy
	blob, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

// armedNodeOver wraps a registry's armed tenant barrier so the holders'
// quorum can be submitted to it.
func armedNodeOver(t *testing.T, b *StorageBarrier, roster []byte) *armedNode {
	t.Helper()
	sum := sha256.Sum256(roster)
	n := &armedNode{t: t, base: b.base, b: b, verifier: newTestVerifier(), digest: sum[:]}
	b.SetHolderVerifier(n.verifier)
	return n
}

func rootKeyPersisted(t *testing.T, b *StorageBarrier) bool {
	t.Helper()
	ok, err := b.base.Exists(context.Background(), b.config.RootKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestImportSealedBlob_RefusesAShamirBlobAndTheQuorumStillKeysTheNode(t *testing.T) {
	ctx := context.Background()
	first := newHolderFixture(t, 2, 3)
	n := newArmedNode(t, first)

	if err := n.b.ImportSealedBlob(ctx, shamirBlobOf(t, first)); !errors.Is(err, ErrImportSealedBlobShamir) {
		t.Fatalf("ImportSealedBlob(shamir) = %v, want ErrImportSealedBlobShamir", err)
	}
	if n.keyed() || !n.b.ShamirArmed() {
		t.Fatalf("after a refused import: keyed=%v armed=%v, want an armed node with no key", n.keyed(), n.b.ShamirArmed())
	}

	n.submit("h2", first.dist.share(DomainUnseal, "h2"))
	if o, err := n.submit("h3", first.dist.share(DomainUnseal, "h3")); err != nil || !o.Complete || n.b.IsSealed() {
		t.Fatalf("first quorum after a refused import = %+v, %v", o, err)
	}
	if bytes.Equal(rootKeyOf(t, n.b), rootKeyOf(t, first.b)) {
		t.Fatal("the node took the first node's root key")
	}
}

func TestBarrierRegistry_ImportTenant_ShamirBlobLeavesTheReplicaToArmShamir(t *testing.T) {
	ctx := context.Background()
	first := newHolderFixture(t, 2, 3)
	blob := shamirBlobOf(t, first)
	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)

	t.Run("armed", func(t *testing.T) {
		if err := reg.ArmTenantShamir(ctx, "armed", first.params.Roster, first.params.UnsealKeyCheck); err != nil {
			t.Fatalf("ArmTenantShamir: %v", err)
		}
		if _, err := reg.ImportTenant(ctx, "armed", blob); !errors.Is(err, ErrImportSealedBlobShamir) {
			t.Fatalf("ImportTenant(shamir) = %v, want ErrImportSealedBlobShamir", err)
		}
		tb, err := reg.GetTenant("armed")
		if err != nil {
			t.Fatalf("a refused import dropped the armed barrier: %v", err)
		}
		if rootKeyPersisted(t, tb.Barrier()) || !tb.Barrier().ShamirArmed() {
			t.Fatal("a refused import left the armed barrier keyed or disarmed")
		}
		n := armedNodeOver(t, tb.Barrier(), first.params.Roster)
		n.submit("h1", first.dist.share(DomainUnseal, "h1"))
		if o, err := n.submit("h2", first.dist.share(DomainUnseal, "h2")); err != nil || !o.Complete || tb.IsSealed() {
			t.Fatalf("first quorum = %+v, %v", o, err)
		}
		if !rootKeyPersisted(t, tb.Barrier()) {
			t.Fatal("the first quorum did not key the replica")
		}
	})

	t.Run("not yet armed", func(t *testing.T) {
		reserveTestVolumeTenant(t, reg, "unarmed")
		if _, err := reg.ImportTenant(ctx, "unarmed", blob); !errors.Is(err, ErrImportSealedBlobShamir) {
			t.Fatalf("ImportTenant(shamir) = %v, want ErrImportSealedBlobShamir", err)
		}
		if _, err := reg.GetTenant("unarmed"); err == nil {
			t.Fatal("a refused import left a barrier registered")
		}
		if err := reg.ArmTenantShamir(ctx, "unarmed", first.params.Roster, first.params.UnsealKeyCheck); err != nil {
			t.Fatalf("ArmTenantShamir after a refused import: %v", err)
		}
		tb, err := reg.GetTenant("unarmed")
		if err != nil || !tb.Barrier().ShamirArmed() || rootKeyPersisted(t, tb.Barrier()) {
			t.Fatalf("after arming: %v", err)
		}
	})
}

func TestBarrierRegistry_ImportTenant_NonShamirBlobsStillImport(t *testing.T) {
	ctx := context.Background()
	reg, _, _ := newPooledRegistry(t)
	initSystemBarrierForImport(t, reg)

	for _, strategy := range []StrategyID{StrategyNone, StrategyPassphrase, "pkcs11"} {
		t.Run(string(strategy), func(t *testing.T) {
			tenantID := "import-" + string(strategy)
			reserveTestVolumeTenant(t, reg, tenantID)
			tb, err := reg.ImportTenant(ctx, tenantID, sealedBlobUnder(t, strategy))
			if err != nil {
				t.Fatalf("ImportTenant(%s): %v", strategy, err)
			}
			if got := tb.Barrier().PersistedStrategy(ctx); got != strategy {
				t.Fatalf("persisted strategy = %q, want %q", got, strategy)
			}
			if !tb.IsSealed() {
				t.Fatal("an imported barrier must stay sealed")
			}
		})
	}
}
