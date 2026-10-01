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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestDatabaseService_OtherTenantsDatabase_IndistinguishableFromMissing pins
// SEC-8(b): database names are still global, so a tenant addressing another
// tenant's database by name used to get a cross-tenant access denial where a
// name nobody holds got not-found -- an oracle telling it which names other
// tenants use. Every tenant-scoped operation must now answer another tenant's
// database exactly as it answers a missing one.
func TestDatabaseService_OtherTenantsDatabase_IndistinguishableFromMissing(t *testing.T) {
	svc, mgr := newTestDatabaseServiceWithBarrier(t)
	cfg := testDBConfig("acme-ledger")
	cfg.TenantID = "acme"
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	intruder := tenant.WithTenantID(context.Background(), "globex")

	ops := map[string]func(name string) error{
		"GetForTenant":  func(n string) error { _, err := svc.GetForTenant(intruder, n); return err },
		"DropForTenant": func(n string) error { return svc.DropForTenant(intruder, n) },
		"UpdateProtocolForTenant": func(n string) error {
			_, err := svc.UpdateProtocolForTenant(intruder, n, database.ProtocolConfig{})
			return err
		},
		"UpdateEtcdForTenant": func(n string) error { _, err := svc.UpdateEtcdForTenant(intruder, n, nil); return err },
		"UpdateRaftForTenant": func(n string) error { _, err := svc.UpdateRaftForTenant(intruder, n, nil); return err },
		"UpdateBarrierForTenant": func(n string) error {
			_, err := svc.UpdateBarrierForTenant(intruder, n, nil)
			return err
		},
		"UpdateConnectionForTenant": func(n string) error {
			_, err := svc.UpdateConnectionForTenant(intruder, n, nil)
			return err
		},
		"SealDatabase":          func(n string) error { return svc.SealDatabase(intruder, n) },
		"UnsealDatabase":        func(n string) error { return svc.UnsealDatabase(intruder, n, seal.Credentials{}) },
		"DatabaseBarrierStatus": func(n string) error { _, err := svc.DatabaseBarrierStatus(intruder, n); return err },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			owned := op("acme-ledger")
			missing := op("nobody-has-this")
			var nf *database.NotFoundError
			if !errors.As(owned, &nf) {
				t.Fatalf("%s on another tenant's database = %v (%T), want *database.NotFoundError", name, owned, owned)
			}
			if !errors.As(missing, &nf) {
				t.Fatalf("%s on a missing database = %v (%T), want *database.NotFoundError", name, missing, missing)
			}
			if strings.ReplaceAll(owned.Error(), "acme-ledger", "X") != strings.ReplaceAll(missing.Error(), "nobody-has-this", "X") {
				t.Errorf("answers differ: %q vs %q", owned, missing)
			}
		})
	}

	if _, err := mgr.Get("acme-ledger"); err != nil {
		t.Fatalf("the owner's database was affected: %v", err)
	}
}
