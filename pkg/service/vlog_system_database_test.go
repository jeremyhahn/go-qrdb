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

//go:build qrdb_vlog

package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestLogService_BarrierTenantsSystemDatabaseLogsStayBehindItsBarrier pins
// that only system/audit goes to the system shard: any other log of the
// system database (each transport's default database) is the tenant's own
// data, written to and read from its barrier store, and refused while the
// barrier is sealed. Listing the system database shows both.
func TestLogService_BarrierTenantsSystemDatabaseLogsStayBehindItsBarrier(t *testing.T) {
	f := newStandaloneBarrierFixture(t)
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"vault": {ID: "vault", IsolationType: tenant.IsolationBarrier},
	}}
	auditResolver := NewSystemShardAuditResolver(barrierHomeGroup(t), 1, lookup)
	if err := NewReplicatedLogger(auditResolver).Emit(f.ctx, fixedAuditEvent("vault")); err != nil {
		t.Fatalf("Emit = %v, want the event on the system shard", err)
	}
	svc, err := NewLogService(LogServiceConfig{
		Resolver:      &standaloneBarrierResolver{f: f},
		AuditResolver: auditResolver,
		Databases:     noDatabases(t),
	})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	route := svc.(*logService).route
	releases := LogRef{Database: vlog.AuditDatabase, Log: "releases"}
	if _, err := svc.CreateLog(f.ctx, &CreateLogRequest{LogRef: releases, Origin: "example.test/vault/releases", SigningKeyType: vlog.SignatureEd25519}); err != nil {
		t.Fatalf("CreateLog(system/releases): %v", err)
	}
	if _, err := svc.Append(f.ctx, &AppendRequest{LogRef: releases, Entries: [][]byte{[]byte("v1")}}); err != nil {
		t.Fatalf("Append(system/releases): %v", err)
	}
	if route.barriers["vault"] == nil {
		t.Fatal("system/releases was not served by the tenant's barrier store")
	}
	listed, err := svc.ListLogs(f.ctx, &ListLogsRequest{Database: vlog.AuditDatabase})
	if err != nil {
		t.Fatalf("ListLogs(system): %v", err)
	}
	names := map[string]bool{}
	for _, id := range listed.Logs {
		names[id.Log] = true
	}
	if len(listed.Logs) != 2 || !names[vlog.AuditLog] || !names["releases"] {
		t.Fatalf("ListLogs(system) = %+v, want the audit log and releases", listed.Logs)
	}
	if err := f.reg.SealTenant("vault"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if _, err := svc.Head(f.ctx, &HeadRequest{LogRef: releases}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("Head(system/releases) while sealed = %v, want tenant.ErrTenantSealed", err)
	}
	if _, err := svc.Append(f.ctx, &AppendRequest{LogRef: releases, Entries: [][]byte{[]byte("v2")}}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("Append(system/releases) while sealed = %v, want tenant.ErrTenantSealed", err)
	}
	audit := LogRef{Database: vlog.AuditDatabase, Log: vlog.AuditLog}
	if head, err := svc.Head(f.ctx, &HeadRequest{LogRef: audit}); err != nil || head.Head.Size != 1 {
		t.Fatalf("Head(system/audit) while sealed = %+v, %v; want the 1 audited event", head, err)
	}
	ctx := context.Background()
	if err := f.reg.UnsealTenant(ctx, "vault", seal.MustCredentials(ctx, []byte(barrierTenantPass))); err != nil {
		t.Fatalf("UnsealTenant: %v", err)
	}
	if head, err := svc.Head(f.ctx, &HeadRequest{LogRef: releases}); err != nil || head.Head.Size != 1 {
		t.Fatalf("Head(system/releases) after unseal = %+v, %v; want 1 entry", head, err)
	}
}

// TestLogService_BarrierSystemDatabaseSignsAndRotates pins that a barrier
// database whose writes need no drop fence (the default database name, or
// the system tenant) still signs and rotates the keys of its logs.
func TestLogService_BarrierSystemDatabaseSignsAndRotates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tenantID string
		ctx      context.Context
	}{
		{name: "system tenant", tenantID: "", ctx: context.Background()},
		{name: "tenant", tenantID: "acme", ctx: acmeCtx()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			g := newFenceGroup(t)
			mgr := newBarrierDatabaseManager(t, dir, database.WithKeyspaceReclaimer(markerReclaimer{mu: &g.mu, store: g.store}))
			svc, _ := newParentGroupLogService(t, g, mgr, nil)
			bg := context.Background()
			db, err := mgr.CreateWithCredentials(bg, database.DatabaseConfig{
				TenantID: tc.tenantID, Name: tenant.DefaultDatabase, Backend: database.BackendPebble,
				DataDir: filepath.Join(dir, "default"),
				Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase},
			}, seal.MustCredentials(bg, []byte(barrierDatabasePass)))
			if err != nil {
				t.Fatalf("create the barrier %s database: %v", tenant.DefaultDatabase, err)
			}
			if !db.BarrierEnabled() || db.Incarnation() == "" {
				t.Fatalf("barrier enabled %v, incarnation %q; want a barrier database with an incarnation", db.BarrierEnabled(), db.Incarnation())
			}
			ref := LogRef{Database: tenant.DefaultDatabase, Log: "releases"}
			const origin = "example.test/system/releases"
			created, err := svc.CreateLog(tc.ctx, &CreateLogRequest{LogRef: ref, Origin: origin, SigningKeyType: vlog.SignatureEd25519})
			if err != nil {
				t.Fatalf("CreateLog: %v", err)
			}
			if _, err := svc.Append(tc.ctx, &AppendRequest{LogRef: ref, Entries: [][]byte{[]byte("system-entry-0001")}}); err != nil {
				t.Fatalf("Append: %v", err)
			}
			signed, err := svc.SignHead(tc.ctx, &SignHeadRequest{LogRef: ref})
			if err != nil {
				t.Fatalf("SignHead: %v", err)
			}
			pub, err := created.Key.Public()
			if err != nil {
				t.Fatalf("created key: %v", err)
			}
			if err := vlog.VerifyHead(pub, origin, signed.Head); err != nil {
				t.Fatalf("VerifyHead under the first key = %v", err)
			}
			rotated, err := svc.RotateKey(tc.ctx, &RotateKeyRequest{LogRef: ref, SigningKeyType: vlog.SignatureEd25519})
			if err != nil {
				t.Fatalf("RotateKey: %v", err)
			}
			if _, err := svc.Append(tc.ctx, &AppendRequest{LogRef: ref, Entries: [][]byte{[]byte("system-entry-0002")}}); err != nil {
				t.Fatalf("Append after RotateKey: %v", err)
			}
			signed, err = svc.SignHead(tc.ctx, &SignHeadRequest{LogRef: ref})
			if err != nil {
				t.Fatalf("SignHead after RotateKey: %v", err)
			}
			pub, err = rotated.Key.Public()
			if err != nil {
				t.Fatalf("rotated key: %v", err)
			}
			if err := vlog.VerifyHead(pub, origin, signed.Head); err != nil || signed.Head.Size != 2 {
				t.Fatalf("VerifyHead(size %d) under the rotated key = %v; want a verified head of size 2", signed.Head.Size, err)
			}
		})
	}
}
