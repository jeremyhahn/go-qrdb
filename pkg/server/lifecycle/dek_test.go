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
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// keyringRecordKey is where the keyring record lands inside a tenant's
// resolved store (pkg/dek's keyringStoreKey).
const keyringRecordKey = "__sys:dek/keyring"

// TestBuildServices_DEKRidesThePrefixTenantsNamespace runs the seal-DISABLED
// branch: the keyring is served there too, and a prefix tenant's record lives
// in that tenant's own namespace of the replicated shared store. With no
// barrier the record is stored without at-rest encryption, which is the
// documented "none" behaviour.
func TestBuildServices_DEKRidesThePrefixTenantsNamespace(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	if node.DEK == nil {
		t.Fatal("DEK is nil on a node with a StoreResolver")
	}

	ctx := context.Background()
	if _, err := node.Tenant.CreateTenant(ctx, "acme", "acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	acme := tenant.WithTenantID(ctx, "acme")
	sealed, err := node.DEK.Encrypt(acme, "", []byte("s3cret"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := node.KV.Get(ctx, tenant.IdentityNamespace("acme")+keyringRecordKey)
	if err != nil || len(raw) == 0 {
		t.Fatalf("keyring record not in acme's namespace: %v", err)
	}
	opened, err := node.DEK.Decrypt(acme, "", sealed.Ciphertext, []byte("aad"))
	if err != nil || !bytes.Equal(opened.Plaintext, []byte("s3cret")) {
		t.Fatalf("Decrypt = %v, %v", opened, err)
	}
	if _, err := node.DEK.Status(tenant.WithTenantID(ctx, "globex"), "acme"); !errors.Is(err, dek.ErrCrossTenant) {
		t.Fatalf("cross-tenant: %v, want ErrCrossTenant", err)
	}
}

// TestBuildServices_DEKIsWrappedByTheTenantsBarrier runs the seal-ENABLED
// branch with a barrier-isolated tenant: the keyring record sits in the
// replicated shared store encrypted under the tenant's own barrier, and a
// sealed tenant barrier makes the keyring unreachable until it is unsealed —
// after which the earlier ciphertext still decrypts.
func TestBuildServices_DEKIsWrappedByTheTenantsBarrier(t *testing.T) {
	node, registry := buildRoutedIAMNode(t)
	if node.DEK == nil {
		t.Fatal("DEK is nil on a seal-enabled node")
	}
	ctx := context.Background()
	const id = "vault-tenant"
	if _, err := node.Tenant.CreateTenant(ctx, id, "Vault Tenant", tenant.IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := registry.InitializeTenant(ctx, id, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	caller := tenant.WithTenantID(ctx, id)
	sealed, err := node.DEK.Encrypt(caller, "", []byte("s3cret"), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	raw, err := node.KV.Get(ctx, seal.TenantSharedNamespace(id)+keyringRecordKey)
	if err != nil || len(raw) == 0 {
		t.Fatalf("keyring record not in the tenant's shared range: %v", err)
	}
	if bytes.Contains(raw, []byte(`"versions"`)) {
		t.Fatal("the keyring record is stored in the clear; it must be encrypted under the tenant barrier")
	}

	if err := registry.SealTenant(id); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if _, err := node.DEK.Decrypt(caller, "", sealed.Ciphertext, nil); !errors.Is(err, dek.ErrKeyringUnavailable) {
		t.Fatalf("sealed tenant: err = %v, want ErrKeyringUnavailable", err)
	}
	if err := registry.UnsealTenant(ctx, id, seal.Credentials{}); err != nil {
		t.Fatalf("UnsealTenant: %v", err)
	}
	opened, err := node.DEK.Decrypt(caller, "", sealed.Ciphertext, nil)
	if err != nil || !bytes.Equal(opened.Plaintext, []byte("s3cret")) {
		t.Fatalf("Decrypt after unseal = %v, %v", opened, err)
	}

	// Deleting the tenant leaves no keyring behind: the record is reclaimed
	// with the tenant's shared range, not merely made unreadable.
	if err := node.Tenant.DeleteTenant(ctx, id); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if raw, err := node.KV.Get(ctx, seal.TenantSharedNamespace(id)+keyringRecordKey); err == nil && len(raw) > 0 {
		t.Fatal("the deleted tenant's keyring record is still in the shared store")
	}
	if _, err := node.DEK.Decrypt(caller, "", sealed.Ciphertext, nil); err == nil {
		t.Fatal("a deleted tenant's ciphertext still decrypts")
	}
}
