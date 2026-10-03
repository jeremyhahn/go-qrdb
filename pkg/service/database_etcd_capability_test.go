//go:build !qrdb_etcd

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

package service

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// TestDatabaseService_Create_EtcdNotCompiled_Refused: on a binary without
// qrdb_etcd, a create request asking for the etcd wire protocol is refused
// with the typed *CapabilityUnsupportedError before the database is created
// at all -- Get afterward finds nothing, not a database silently created
// with etcd quietly left off.
func TestDatabaseService_Create_EtcdNotCompiled_Refused(t *testing.T) {
	svc := newTestDatabaseService(t)

	cfg := testDBConfig("etcd-create-db")
	cfg.Protocol.Etcd = &database.EtcdConfig{Enabled: true, ListenAddr: "localhost:2379"}

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("Create() with etcd on a !qrdb_etcd build returned nil, want *service.CapabilityUnsupportedError")
	}
	var capErr *CapabilityUnsupportedError
	if !errors.As(err, &capErr) {
		t.Fatalf("Create() error type = %T, want *service.CapabilityUnsupportedError", err)
	}

	if _, gerr := svc.Get("etcd-create-db"); gerr == nil {
		t.Fatal("Get() found a database the refused Create() must not have persisted")
	} else {
		var notFound *database.NotFoundError
		if !errors.As(gerr, &notFound) {
			t.Fatalf("Get() error type = %T, want *database.NotFoundError", gerr)
		}
	}
}

// TestDatabaseService_CreateForTenant_EtcdNotCompiled_Refused is the
// tenant-scoped counterpart of TestDatabaseService_Create_EtcdNotCompiled_Refused,
// covering the IsolationVolume/IsolationRaft path that calls the manager
// directly rather than going through CreateWithCredentials.
func TestDatabaseService_CreateForTenant_EtcdNotCompiled_Refused(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := systemRoleCtx("admin")

	cfg := testDBConfig("etcd-tenant-create-db")
	cfg.Protocol.Etcd = &database.EtcdConfig{Enabled: true, ListenAddr: "localhost:2379"}

	_, err := svc.CreateForTenant(ctx, cfg)
	if err == nil {
		t.Fatal("CreateForTenant() with etcd on a !qrdb_etcd build returned nil, want *service.CapabilityUnsupportedError")
	}
	var capErr *CapabilityUnsupportedError
	if !errors.As(err, &capErr) {
		t.Fatalf("CreateForTenant() error type = %T, want *service.CapabilityUnsupportedError", err)
	}

	if _, gerr := svc.Get("etcd-tenant-create-db"); gerr == nil {
		t.Fatal("Get() found a database the refused CreateForTenant() must not have persisted")
	}
}

// TestDatabaseService_UpdateEtcd_NotCompiled_Refused: an existing database's
// etcd configuration cannot be turned on through UpdateEtcd on a binary
// without qrdb_etcd -- the typed refusal fires and the database's protocol
// configuration is left exactly as it was, not half-updated.
func TestDatabaseService_UpdateEtcd_NotCompiled_Refused(t *testing.T) {
	svc := newTestDatabaseService(t)

	if _, err := svc.Create(testDBConfig("etcd-update-db")); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	_, err := svc.UpdateEtcd("etcd-update-db", &database.EtcdConfig{Enabled: true, ListenAddr: "localhost:2379"})
	if err == nil {
		t.Fatal("UpdateEtcd() on a !qrdb_etcd build returned nil, want *service.CapabilityUnsupportedError")
	}
	var capErr *CapabilityUnsupportedError
	if !errors.As(err, &capErr) {
		t.Fatalf("UpdateEtcd() error type = %T, want *service.CapabilityUnsupportedError", err)
	}

	info, gerr := svc.Get("etcd-update-db")
	if gerr != nil {
		t.Fatalf("Get() returned unexpected error: %v", gerr)
	}
	if info.Protocol.Etcd != nil && info.Protocol.Etcd.Enabled {
		t.Fatalf("UpdateEtcd() refusal left etcd enabled: %+v", info.Protocol.Etcd)
	}
}

// TestDatabaseService_UpdateEtcdForTenant_NotCompiled_Refused is the
// tenant-scoped counterpart of TestDatabaseService_UpdateEtcd_NotCompiled_Refused.
func TestDatabaseService_UpdateEtcdForTenant_NotCompiled_Refused(t *testing.T) {
	svc := newTestDatabaseService(t)
	ctx := systemRoleCtx("admin")

	if _, err := svc.Create(testDBConfig("etcd-update-tenant-db")); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	_, err := svc.UpdateEtcdForTenant(ctx, "etcd-update-tenant-db",
		&database.EtcdConfig{Enabled: true, ListenAddr: "localhost:2379"})
	if err == nil {
		t.Fatal("UpdateEtcdForTenant() on a !qrdb_etcd build returned nil, want *service.CapabilityUnsupportedError")
	}
	var capErr *CapabilityUnsupportedError
	if !errors.As(err, &capErr) {
		t.Fatalf("UpdateEtcdForTenant() error type = %T, want *service.CapabilityUnsupportedError", err)
	}

	info, gerr := svc.Get("etcd-update-tenant-db")
	if gerr != nil {
		t.Fatalf("Get() returned unexpected error: %v", gerr)
	}
	if info.Protocol.Etcd != nil && info.Protocol.Etcd.Enabled {
		t.Fatalf("UpdateEtcdForTenant() refusal left etcd enabled: %+v", info.Protocol.Etcd)
	}
}

// TestDatabaseService_UpdateEtcd_Disable_NotRefused: disabling etcd (or
// leaving it at its zero value) is never refused, even on a build without
// qrdb_etcd -- there is nothing to serve either way, and a database created
// before this binary lost the tag must still be able to turn an already-off
// protocol further off.
func TestDatabaseService_UpdateEtcd_Disable_NotRefused(t *testing.T) {
	svc := newTestDatabaseService(t)

	if _, err := svc.Create(testDBConfig("etcd-disable-db")); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	if _, err := svc.UpdateEtcd("etcd-disable-db", &database.EtcdConfig{Enabled: false}); err != nil {
		t.Fatalf("UpdateEtcd(disabled) returned unexpected error: %v", err)
	}

	var capErr *CapabilityUnsupportedError
	if _, err := svc.Create(testDBConfig("etcd-none-db")); errors.As(err, &capErr) {
		t.Fatalf("Create() with no etcd block was refused as a capability error: %v", err)
	}
}
