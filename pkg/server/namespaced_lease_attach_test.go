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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// PutWithLeaseAndMode attaches like PutWithLease; the mode is not modelled.
func (l *sharedLessor) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, id int64, _ writemode.Mode) (*service.KeyValue, error) {
	return l.PutWithLease(ctx, key, value, id)
}

// leaseRegistry resolves (tenant, database) the way the server does -- the
// composite namespace over one shared lessor that serves both the lease
// verbs and the MVCC writes -- for two tenants that each own a database
// named "app".
func leaseRegistry(t *testing.T, lessor *sharedLessor) *restServiceRegistry {
	t.Helper()
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	for _, owner := range []string{"acme", "globex"} {
		if _, err := mgr.Create(database.DatabaseConfig{TenantID: owner, Name: owner + "app", Backend: database.BackendMemory}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	dbReg := NewDatabaseServiceRegistry("default")
	dbReg.Register("default", &DatabaseServices{Name: "default", MVCC: lessor, Lease: lessor})
	reg, ok := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
		WithDatabaseCatalog(newManagerCatalog(mgr)),
		withDatabaseManager(mgr),
	).(*restServiceRegistry)
	if !ok {
		t.Fatal("NewRESTServiceRegistry did not return *restServiceRegistry")
	}
	return reg
}

// A write may attach only a lease its own namespace granted. Lease IDs are
// global, so another tenant could attach its key to a lease it does not
// control -- learning the ID is live and tying its key to the owner's revoke
// and expiry -- through every transport's MVCC write and the etcd Put that
// rides on it.
func TestNamespacedMVCC_AttachesOnlyOwnedLeases(t *testing.T) {
	lessor := newSharedLessor()
	reg := leaseRegistry(t, lessor)
	ctx := context.Background()

	acmeLeases, err := reg.LeaseService("acme", "acmeapp")
	if err != nil {
		t.Fatalf("LeaseService(acme): %v", err)
	}
	granted, err := acmeLeases.Grant(ctx, 30)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	globexMVCC, err := reg.MVCCService("globex", "globexapp")
	if err != nil {
		t.Fatalf("MVCCService(globex): %v", err)
	}

	var notFound *service.LeaseNotFoundError
	if _, err := globexMVCC.PutWithLease(ctx, "k", []byte("v"), granted.ID); !errors.As(err, &notFound) {
		t.Errorf("another tenant's PutWithLease = %v, want *service.LeaseNotFoundError", err)
	}
	if _, err := globexMVCC.PutWithLeaseAndMode(ctx, "k", []byte("v"), granted.ID, writemode.Mode(0)); !errors.As(err, &notFound) {
		t.Errorf("another tenant's PutWithLeaseAndMode = %v, want *service.LeaseNotFoundError", err)
	}
	if _, attached := lessor.kv["globex/globexapp/k"]; attached {
		t.Fatal("another tenant's key was attached to the lease")
	}

	// The owner attaches, and no lease (0) needs no ownership.
	acmeMVCC, err := reg.MVCCService("acme", "acmeapp")
	if err != nil {
		t.Fatalf("MVCCService(acme): %v", err)
	}
	if _, err := acmeMVCC.PutWithLease(ctx, "k", []byte("v"), granted.ID); err != nil {
		t.Fatalf("owner PutWithLease: %v", err)
	}
	if lessor.kv["acme/acmeapp/k"] != granted.ID {
		t.Fatalf("the owner's key is not attached: %v", lessor.kv)
	}
	if _, err := acmeMVCC.PutWithLeaseAndMode(ctx, "k2", []byte("v"), granted.ID, writemode.Mode(0)); err != nil {
		t.Fatalf("owner PutWithLeaseAndMode: %v", err)
	}
}

// Without a lease view no lease can be attached; no lease still writes.
func TestNamespacedMVCC_NoLeaseViewRefusesALease(t *testing.T) {
	lessor := newSharedLessor()
	w := newNamespacedMVCCService(lessor, "acme/app/", nil)
	var notFound *service.LeaseNotFoundError
	if _, err := w.PutWithLease(context.Background(), "k", nil, 7); !errors.As(err, &notFound) {
		t.Fatalf("PutWithLease with no lease view = %v, want *service.LeaseNotFoundError", err)
	}
}
