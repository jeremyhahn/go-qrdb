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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantContext returns a context with the given tenant ID set.
func tenantContext(tenantID string) context.Context {
	return tenant.WithTenantID(context.Background(), tenantID)
}

func TestManager_CreateForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validMemoryConfig("tenant-a-db")
	db, err := mgr.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}
	if db.TenantID() != "tenant-a" {
		t.Errorf("TenantID() = %q, want %q", db.TenantID(), "tenant-a")
	}
	if db.Name() != "tenant-a-db" {
		t.Errorf("Name() = %q, want %q", db.Name(), "tenant-a-db")
	}
}

func TestManager_CreateForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := context.Background()
	cfg := validMemoryConfig("db1")
	_, err := mgr.CreateForTenant(ctx, cfg)
	if err == nil {
		t.Fatal("CreateForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_GetForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validMemoryConfig("get-tenant-db")
	if _, err := mgr.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	db, err := mgr.GetForTenant(ctx, "get-tenant-db")
	if err != nil {
		t.Fatalf("GetForTenant() error = %v", err)
	}
	if db.TenantID() != "tenant-a" {
		t.Errorf("TenantID() = %q, want %q", db.TenantID(), "tenant-a")
	}
}

func TestManager_GetForTenant_CrossTenantDenied(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	cfg := validMemoryConfig("cross-tenant-db")
	if _, err := mgr.CreateForTenant(ctxA, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	ctxB := tenantContext("tenant-b")
	_, err := mgr.GetForTenant(ctxB, "cross-tenant-db")
	if err == nil {
		t.Fatal("GetForTenant() cross-tenant = nil, want error")
	}
	var accessErr *TenantAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("error type = %T, want *TenantAccessDeniedError", err)
	}
	if accessErr.TenantID != "tenant-b" {
		t.Errorf("TenantID = %q, want %q", accessErr.TenantID, "tenant-b")
	}
}

func TestManager_GetForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	_, err := mgr.GetForTenant(context.Background(), "any-db")
	if err == nil {
		t.Fatal("GetForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_GetForTenant_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	_, err := mgr.GetForTenant(ctx, "nonexistent-db")
	if err == nil {
		t.Fatal("GetForTenant() for nonexistent db = nil, want error")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
}

func TestManager_ListForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	ctxB := tenantContext("tenant-b")

	if _, err := mgr.CreateForTenant(ctxA, validMemoryConfig("a-db1")); err != nil {
		t.Fatalf("CreateForTenant(a-db1) error = %v", err)
	}
	if _, err := mgr.CreateForTenant(ctxA, validMemoryConfig("a-db2")); err != nil {
		t.Fatalf("CreateForTenant(a-db2) error = %v", err)
	}
	if _, err := mgr.CreateForTenant(ctxB, validMemoryConfig("b-db1")); err != nil {
		t.Fatalf("CreateForTenant(b-db1) error = %v", err)
	}

	listA, err := mgr.ListForTenant(ctxA)
	if err != nil {
		t.Fatalf("ListForTenant(a) error = %v", err)
	}
	if len(listA) != 2 {
		t.Fatalf("ListForTenant(a) = %d entries, want 2", len(listA))
	}

	listB, err := mgr.ListForTenant(ctxB)
	if err != nil {
		t.Fatalf("ListForTenant(b) error = %v", err)
	}
	if len(listB) != 1 {
		t.Fatalf("ListForTenant(b) = %d entries, want 1", len(listB))
	}
	if listB[0].Name != "b-db1" {
		t.Errorf("ListForTenant(b)[0].Name = %q, want %q", listB[0].Name, "b-db1")
	}
}

func TestManager_ListForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	_, err := mgr.ListForTenant(context.Background())
	if err == nil {
		t.Fatal("ListForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_ListForTenant_Sorted(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-sort")
	for _, name := range []string{"zebra", "alpha", "mango"} {
		if _, err := mgr.CreateForTenant(ctx, validMemoryConfig(name)); err != nil {
			t.Fatalf("CreateForTenant(%q) error = %v", name, err)
		}
	}

	list, err := mgr.ListForTenant(ctx)
	if err != nil {
		t.Fatalf("ListForTenant() error = %v", err)
	}
	expected := []string{"alpha", "mango", "zebra"}
	for i, want := range expected {
		if list[i].Name != want {
			t.Errorf("list[%d].Name = %q, want %q", i, list[i].Name, want)
		}
	}
}

func TestManager_ListForTenant_Empty(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("empty-tenant")
	list, err := mgr.ListForTenant(ctx)
	if err != nil {
		t.Fatalf("ListForTenant() error = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListForTenant() for empty tenant = %d entries, want 0", len(list))
	}
}

func TestManager_DropForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validMemoryConfig("drop-tenant-db")
	if _, err := mgr.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	if err := mgr.DropForTenant(ctx, "drop-tenant-db"); err != nil {
		t.Fatalf("DropForTenant() error = %v", err)
	}

	_, err := mgr.Get("drop-tenant-db")
	if err == nil {
		t.Fatal("Get() after DropForTenant() = nil, want error")
	}
}

func TestManager_DropForTenant_CrossTenantDenied(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	cfg := validMemoryConfig("no-drop-db")
	if _, err := mgr.CreateForTenant(ctxA, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	ctxB := tenantContext("tenant-b")
	err := mgr.DropForTenant(ctxB, "no-drop-db")
	if err == nil {
		t.Fatal("DropForTenant() cross-tenant = nil, want error")
	}
	var accessErr *TenantAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("error type = %T, want *TenantAccessDeniedError", err)
	}
}

func TestManager_DropForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.DropForTenant(context.Background(), "any-db")
	if err == nil {
		t.Fatal("DropForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_DropForTenant_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	err := mgr.DropForTenant(ctx, "nonexistent-db")
	if err == nil {
		t.Fatal("DropForTenant() for nonexistent db = nil, want error")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
}

func TestManager_UpdateProtocolForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validMemoryConfig("proto-tenant-db")
	if _, err := mgr.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	protocol := ProtocolConfig{MySQL: true, PostgreSQL: true}
	if err := mgr.UpdateProtocolForTenant(ctx, "proto-tenant-db", protocol); err != nil {
		t.Fatalf("UpdateProtocolForTenant() error = %v", err)
	}

	db, err := mgr.Get("proto-tenant-db")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !db.ProtocolConfig().MySQL {
		t.Error("ProtocolConfig().MySQL = false, want true")
	}
}

func TestManager_UpdateProtocolForTenant_CrossTenantDenied(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	cfg := validMemoryConfig("proto-cross-db")
	if _, err := mgr.CreateForTenant(ctxA, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	ctxB := tenantContext("tenant-b")
	err := mgr.UpdateProtocolForTenant(ctxB, "proto-cross-db", ProtocolConfig{MySQL: true})
	if err == nil {
		t.Fatal("UpdateProtocolForTenant() cross-tenant = nil, want error")
	}
	var accessErr *TenantAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("error type = %T, want *TenantAccessDeniedError", err)
	}
}

func TestManager_UpdateEtcdForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validMemoryConfig("etcd-tenant-db")
	if _, err := mgr.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	etcd := &EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:2379"}
	if err := mgr.UpdateEtcdForTenant(ctx, "etcd-tenant-db", etcd); err != nil {
		t.Fatalf("UpdateEtcdForTenant() error = %v", err)
	}
}

func TestManager_UpdateEtcdForTenant_CrossTenantDenied(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	cfg := validMemoryConfig("etcd-cross-db")
	if _, err := mgr.CreateForTenant(ctxA, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	ctxB := tenantContext("tenant-b")
	err := mgr.UpdateEtcdForTenant(ctxB, "etcd-cross-db", &EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:2379"})
	if err == nil {
		t.Fatal("UpdateEtcdForTenant() cross-tenant = nil, want error")
	}
	var accessErr *TenantAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("error type = %T, want *TenantAccessDeniedError", err)
	}
}

func TestManager_UpdateRaftForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validMemoryConfig("raft-tenant-db")
	if _, err := mgr.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	raft := &DatabaseRaftConfig{ClusterMode: RaftNone}
	if err := mgr.UpdateRaftForTenant(ctx, "raft-tenant-db", raft); err != nil {
		t.Fatalf("UpdateRaftForTenant() error = %v", err)
	}
}

func TestManager_UpdateRaftForTenant_CrossTenantDenied(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	cfg := validMemoryConfig("raft-cross-db")
	if _, err := mgr.CreateForTenant(ctxA, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	ctxB := tenantContext("tenant-b")
	err := mgr.UpdateRaftForTenant(ctxB, "raft-cross-db", &DatabaseRaftConfig{ClusterMode: RaftNone})
	if err == nil {
		t.Fatal("UpdateRaftForTenant() cross-tenant = nil, want error")
	}
	var accessErr *TenantAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("error type = %T, want *TenantAccessDeniedError", err)
	}
}

// TestManager_UpdateBarrierForTenant verifies that enabling a barrier via
// UpdateBarrierForTenant on a database that was created WITHOUT one active
// is rejected: enabling after creation is a re-encryption migration this
// method does not perform (see Manager.UpdateBarrier).
func TestManager_UpdateBarrierForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validDedicatedMemoryConfig(t, "barrier-tenant-db")
	if _, err := mgr.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	barrier := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	err := mgr.UpdateBarrierForTenant(ctx, "barrier-tenant-db", barrier)
	if err == nil {
		t.Fatal("UpdateBarrierForTenant() enabling a never-active barrier = nil, want *BarrierMigrationNotSupportedError")
	}
	var migErr *BarrierMigrationNotSupportedError
	if !errors.As(err, &migErr) {
		t.Fatalf("error type = %T, want *BarrierMigrationNotSupportedError", err)
	}
}

func TestManager_UpdateBarrierForTenant_CrossTenantDenied(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	cfg := validMemoryConfig("barrier-cross-db")
	if _, err := mgr.CreateForTenant(ctxA, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	ctxB := tenantContext("tenant-b")
	err := mgr.UpdateBarrierForTenant(ctxB, "barrier-cross-db", &DatabaseBarrierConfig{Enabled: true})
	if err == nil {
		t.Fatal("UpdateBarrierForTenant() cross-tenant = nil, want error")
	}
	var accessErr *TenantAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("error type = %T, want *TenantAccessDeniedError", err)
	}
}

func TestManager_UpdateConnectionForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validMemoryConfig("conn-tenant-db")
	if _, err := mgr.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	conn := &DatabaseConnectionConfig{Transport: "grpc", Address: "127.0.0.1:9090"}
	if err := mgr.UpdateConnectionForTenant(ctx, "conn-tenant-db", conn); err != nil {
		t.Fatalf("UpdateConnectionForTenant() error = %v", err)
	}
}

func TestManager_UpdateConnectionForTenant_CrossTenantDenied(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	cfg := validMemoryConfig("conn-cross-db")
	if _, err := mgr.CreateForTenant(ctxA, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	ctxB := tenantContext("tenant-b")
	conn := &DatabaseConnectionConfig{Transport: "grpc", Address: "127.0.0.1:9090"}
	err := mgr.UpdateConnectionForTenant(ctxB, "conn-cross-db", conn)
	if err == nil {
		t.Fatal("UpdateConnectionForTenant() cross-tenant = nil, want error")
	}
	var accessErr *TenantAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("error type = %T, want *TenantAccessDeniedError", err)
	}
}

func TestManager_CreateForTenant_TenantIDInInfo(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-info")
	cfg := validMemoryConfig("info-tenant-db")
	db, err := mgr.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	info := db.Info()
	if info.TenantID != "tenant-info" {
		t.Errorf("Info().TenantID = %q, want %q", info.TenantID, "tenant-info")
	}
}

func TestManager_UpdateProtocolForTenant_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	err := mgr.UpdateProtocolForTenant(ctx, "nonexistent-db", ProtocolConfig{MySQL: true})
	if err == nil {
		t.Fatal("UpdateProtocolForTenant() for nonexistent db = nil, want error")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
}

func TestManager_UpdateProtocolForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateProtocolForTenant(context.Background(), "any-db", ProtocolConfig{})
	if err == nil {
		t.Fatal("UpdateProtocolForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_UpdateEtcdForTenant_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	err := mgr.UpdateEtcdForTenant(ctx, "nonexistent-db", &EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:2379"})
	if err == nil {
		t.Fatal("UpdateEtcdForTenant() for nonexistent db = nil, want error")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
}

func TestManager_UpdateEtcdForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateEtcdForTenant(context.Background(), "any-db", nil)
	if err == nil {
		t.Fatal("UpdateEtcdForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_UpdateRaftForTenant_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	err := mgr.UpdateRaftForTenant(ctx, "nonexistent-db", &DatabaseRaftConfig{ClusterMode: RaftNone})
	if err == nil {
		t.Fatal("UpdateRaftForTenant() for nonexistent db = nil, want error")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
}

func TestManager_UpdateRaftForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateRaftForTenant(context.Background(), "any-db", nil)
	if err == nil {
		t.Fatal("UpdateRaftForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_UpdateBarrierForTenant_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	err := mgr.UpdateBarrierForTenant(ctx, "nonexistent-db", &DatabaseBarrierConfig{Enabled: true})
	if err == nil {
		t.Fatal("UpdateBarrierForTenant() for nonexistent db = nil, want error")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
}

func TestManager_UpdateBarrierForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateBarrierForTenant(context.Background(), "any-db", nil)
	if err == nil {
		t.Fatal("UpdateBarrierForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_UpdateConnectionForTenant_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	conn := &DatabaseConnectionConfig{Transport: "grpc", Address: "127.0.0.1:9090"}
	err := mgr.UpdateConnectionForTenant(ctx, "nonexistent-db", conn)
	if err == nil {
		t.Fatal("UpdateConnectionForTenant() for nonexistent db = nil, want error")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("error type = %T, want *NotFoundError", err)
	}
}

func TestManager_UpdateConnectionForTenant_NoTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateConnectionForTenant(context.Background(), "any-db", nil)
	if err == nil {
		t.Fatal("UpdateConnectionForTenant() with no tenant = nil, want error")
	}
	var tidErr *TenantIDRequiredError
	if !errors.As(err, &tidErr) {
		t.Fatalf("error type = %T, want *TenantIDRequiredError", err)
	}
}

func TestManager_ListForTenant_TenantIDInInfo(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-list-info")
	cfg := validMemoryConfig("list-info-db")
	if _, err := mgr.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}

	list, err := mgr.ListForTenant(ctx)
	if err != nil {
		t.Fatalf("ListForTenant() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListForTenant() = %d entries, want 1", len(list))
	}
	if list[0].TenantID != "tenant-list-info" {
		t.Errorf("list[0].TenantID = %q, want %q", list[0].TenantID, "tenant-list-info")
	}
}
