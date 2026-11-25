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

package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_StandalonePebbleRefusesCredentialReadsWhenSealed pins
// that on a standalone Pebble node, whose credential store is the node's
// own KV rather than the seal barrier, sealing the system barrier stops
// the credential verbs from returning a credential's current or retained
// value: sealing closes neither the Pebble database nor an ordinary read
// of it, so the bound store must keep the barrier's own sealed check.
func TestBuildServices_StandalonePebbleRefusesCredentialReadsWhenSealed(t *testing.T) {
	ctx := context.Background()
	barrier, registry, credSvc := newTestBarrier(t)

	dataDir := t.TempDir()
	// Encrypted Pebble with an enabled Encryptor in Infra: the composition
	// that puts the credential records on the data plane behind the gate.
	enc := atrest.New(barrier)
	stg, _, err := lifecycle.NewLocalStorage(database.BackendPebble, dataDir, enc, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatalf("NewLocalStorage(pebble): %v", err)
	}
	host := backend.NewLocalBackend(engine.NewKVEngine(stg))
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           dataDir,
		Backend:           database.BackendPebble,
		Encryptor:         enc,
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices on a pebble data plane: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(ctx) })

	if err := credSvc.StoreCredential(ctx, "db-password", []byte("v1-secret")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := credSvc.RotateCredential(ctx, "db-password", credentials.RotateOptions{NewValue: []byte("v2-secret"), GracePeriod: time.Hour}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	// The records went to the data plane, not the seal barrier: otherwise the
	// assertions below would test the barrier's own seal, not the gate.
	barrierKeys, err := barrier.List(ctx, "")
	if err != nil {
		t.Fatalf("barrier.List: %v", err)
	}
	for _, k := range barrierKeys {
		if strings.Contains(k, "db-password") {
			t.Fatalf("credential record %q is in the seal barrier, want it on the data plane", k)
		}
	}

	// Unsealed, both the current value and the retained first version read.
	cur, err := node.Credential.CredentialRetrieveBuffer(ctx, "db-password")
	if err != nil {
		t.Fatalf("CredentialRetrieveBuffer while unsealed: %v", err)
	}
	cur.Destroy()
	old, err := node.Credential.CredentialRetrieveVersionBuffer(ctx, "db-password", 1)
	if err != nil {
		t.Fatalf("CredentialRetrieveVersionBuffer while unsealed: %v", err)
	}
	old.Destroy()

	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if buf, err := node.Credential.CredentialRetrieveBuffer(ctx, "db-password"); !errors.Is(err, seal.ErrSealed) {
		if buf != nil {
			buf.Destroy()
		}
		t.Fatalf("CredentialRetrieveBuffer after sealing: err = %v, want seal.ErrSealed", err)
	}
	if buf, err := node.Credential.CredentialRetrieveVersionBuffer(ctx, "db-password", 1); !errors.Is(err, seal.ErrSealed) {
		if buf != nil {
			buf.Destroy()
		}
		t.Fatalf("CredentialRetrieveVersionBuffer after sealing: err = %v, want seal.ErrSealed", err)
	}
}

// TestTenantBarrier_SealedRefusesMoveAndMigration: a sealed tenant barrier
// is never read as an empty store. Its migration into the tenant's home
// store refuses the derived service, and a store move out of it refuses the
// next credential verb, both with seal.ErrSealed, and nothing is copied.
func TestTenantBarrier_SealedRefusesMoveAndMigration(t *testing.T) {
	ctx := context.Background()
	_, _, parent := newTestBarrier(t)
	tenant, _, tenantSvc := newTestBarrier(t)
	if err := tenantSvc.StoreCredential(ctx, "tenant-db", []byte("v1-secret")); err != nil {
		t.Fatalf("StoreCredential on the tenant barrier: %v", err)
	}
	if err := tenant.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	home, err := credentials.NewKVStore(kvstoretest.NewGuardedMemoryStore())
	if err != nil {
		t.Fatalf("NewKVStore: %v", err)
	}
	if svc, err := parent.ForTenant(ctx, home, tenant); !errors.Is(err, seal.ErrSealed) || svc != nil {
		t.Fatalf("ForTenant on a sealed tenant barrier: svc = %v, err = %v, want nil and seal.ErrSealed", svc, err)
	}

	_, _, mover := newTestBarrier(t)
	dest := seal.NewMemoryBackend()
	if err := mover.BindStoreFrom(ctx, dest, tenant); err != nil {
		t.Fatalf("BindStoreFrom: a failed move must not fail the bind: %v", err)
	}
	if buf, err := mover.RetrieveCredentialBuffer(ctx, "tenant-db"); !errors.Is(err, seal.ErrSealed) {
		if buf != nil {
			buf.Destroy()
		}
		t.Fatalf("RetrieveCredentialBuffer with a move pending from a sealed barrier: err = %v, want seal.ErrSealed", err)
	}
	keys, err := dest.List(ctx, "")
	if err != nil {
		t.Fatalf("dest.List: %v", err)
	}
	for _, k := range keys {
		if strings.Contains(k, "tenant-db") {
			t.Fatalf("record %q was copied out of a sealed barrier", k)
		}
	}
}
