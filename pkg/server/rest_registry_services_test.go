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

// Tests for the relational service accessors on the REST registry.
//
// Each is built fresh per call from the (tenant, db)-scoped servicers that
// resolve returns, because resolve is what applies the per-tenant barrier,
// quota and namespace isolation. An accessor that skipped resolve -- or that
// returned a service assembled from unscoped servicers -- would read and write
// another tenant's keyspace, which is the failure this layer exists to prevent.

package server

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newAccessorRegistry builds a REST registry whose default database resolves
// to the given service set.
func newAccessorRegistry(t *testing.T, svcs *DatabaseServices) *restServiceRegistry {
	t.Helper()
	dbReg := NewDatabaseServiceRegistry("default")
	dbReg.Register("default", svcs)
	return NewRESTServiceRegistry(dbReg, nil, tenant.SystemTenantID).(*restServiceRegistry)
}

// TestRelationalAccessorsRefuseAnIncompleteServiceSet covers the guard on each
// accessor.
//
// The relational surface needs KV, MVCC and Transaction together: the table
// catalog lives in KV, rows in MVCC, and every DDL/DML mutation is committed
// as one transaction. A partial set must be refused rather than half-built --
// a table service without a transaction would apply DDL non-atomically.
func TestRelationalAccessorsRefuseAnIncompleteServiceSet(t *testing.T) {
	tests := []struct {
		name string
		svcs *DatabaseServices
	}{
		{"nothing wired", &DatabaseServices{}},
		{"kv only", &DatabaseServices{KV: newMockKVService()}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAccessorRegistry(t, tc.svcs)

			if _, err := r.TableService(tenant.SystemTenantID, "default"); err == nil {
				t.Error("TableService must refuse an incomplete service set")
			} else {
				var want *service.CapabilityUnsupportedError
				if !errors.As(err, &want) {
					t.Errorf("TableService error = %v (%T), want *service.CapabilityUnsupportedError", err, err)
				} else if !strings.HasPrefix(want.Message, "table: ") {
					t.Errorf("message %q must name the refused service table", want.Message)
				}
			}

			if _, err := r.SequenceService(tenant.SystemTenantID, "default"); err == nil {
				t.Error("SequenceService must refuse an incomplete service set")
			} else {
				var want *service.CapabilityUnsupportedError
				if !errors.As(err, &want) {
					t.Errorf("SequenceService error = %v (%T), want *service.CapabilityUnsupportedError", err, err)
				} else if !strings.HasPrefix(want.Message, "sequence: ") {
					t.Errorf("message %q must name the refused service sequence", want.Message)
				}
			}

			if _, err := r.PrivilegeService(tenant.SystemTenantID, "default"); err == nil {
				t.Error("PrivilegeService must refuse an incomplete service set")
			} else {
				var want *service.CapabilityUnsupportedError
				if !errors.As(err, &want) {
					t.Errorf("PrivilegeService error = %v (%T), want *service.CapabilityUnsupportedError", err, err)
				} else if !strings.HasPrefix(want.Message, "privilege: ") {
					t.Errorf("message %q must name the refused service privilege", want.Message)
				}
			}
		})
	}
}

// TestRelationalAccessorsPropagateAResolveFailure covers the branch where the
// (tenant, db) pair itself does not resolve. The accessor must surface that
// rather than fall back to any default set -- falling back is how a request
// for a database that does not exist ends up served from another one.
func TestRelationalAccessorsPropagateAResolveFailure(t *testing.T) {
	r := newAccessorRegistry(t, &DatabaseServices{
		KV:          newMockKVService(),
		MVCC:        &stubMVCCService{},
		Transaction: &stubTransactionService{},
	})

	const missingDB = "no-such-database"

	if _, err := r.TableService(tenant.SystemTenantID, missingDB); err == nil {
		t.Error("TableService must fail for an unresolvable database")
	}
	if _, err := r.SequenceService(tenant.SystemTenantID, missingDB); err == nil {
		t.Error("SequenceService must fail for an unresolvable database")
	}
	if _, err := r.PrivilegeService(tenant.SystemTenantID, missingDB); err == nil {
		t.Error("PrivilegeService must fail for an unresolvable database")
	}
}

// TestRelationalAccessorsBuildFromACompleteSet covers the success path of all
// three, including that each call returns a usable service rather than a nil
// interface with a nil error.
func TestRelationalAccessorsBuildFromACompleteSet(t *testing.T) {
	r := newAccessorRegistry(t, &DatabaseServices{
		KV:          newMockKVService(),
		MVCC:        &stubMVCCService{},
		Transaction: &stubTransactionService{},
	})

	table, err := r.TableService(tenant.SystemTenantID, "default")
	if err != nil {
		t.Fatalf("TableService: %v", err)
	}
	if table == nil {
		t.Error("TableService returned a nil service with a nil error")
	}

	seq, err := r.SequenceService(tenant.SystemTenantID, "default")
	if err != nil {
		t.Fatalf("SequenceService: %v", err)
	}
	if seq == nil {
		t.Error("SequenceService returned a nil service with a nil error")
	}

	priv, err := r.PrivilegeService(tenant.SystemTenantID, "default")
	if err != nil {
		t.Fatalf("PrivilegeService: %v", err)
	}
	if priv == nil {
		t.Error("PrivilegeService returned a nil service with a nil error")
	}
}

// TestRelationalAccessorsAreBuiltPerCall pins the documented decision not to
// cache. Caching would hold servicers resolved under one tenant's barrier and
// quota wrapping and hand them to the next caller, so a fresh build per call
// is the isolation guarantee rather than an inefficiency.
func TestRelationalAccessorsAreBuiltPerCall(t *testing.T) {
	r := newAccessorRegistry(t, &DatabaseServices{
		KV:          newMockKVService(),
		MVCC:        &stubMVCCService{},
		Transaction: &stubTransactionService{},
	})

	first, err := r.TableService(tenant.SystemTenantID, "default")
	if err != nil {
		t.Fatalf("first TableService: %v", err)
	}
	second, err := r.TableService(tenant.SystemTenantID, "default")
	if err != nil {
		t.Fatalf("second TableService: %v", err)
	}
	if first == second {
		t.Error("TableService returned a cached instance; it must be built per call")
	}
}

// TestWithDatabaseManagerStoresTheManager covers the unexported option. It is
// what routes a database with its own dedicated barrier store away from the
// owning tenant's shared services, so a dropped manager silently sends
// dedicated-store databases back to the shared keyspace.
func TestWithDatabaseManagerStoresTheManager(t *testing.T) {
	r := &restServiceRegistry{}

	withDatabaseManager(nil)(r)
	if r.dbManager != nil {
		t.Error("withDatabaseManager(nil) must leave the manager unset")
	}

	mgr := &database.Manager{}
	withDatabaseManager(mgr)(r)
	if r.dbManager != mgr {
		t.Error("withDatabaseManager did not store the supplied manager")
	}
}
