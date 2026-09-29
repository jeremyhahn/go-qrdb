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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func TestDatabaseService_UpdateSQL(t *testing.T) {
	svc := newTestDatabaseService(t)
	if _, err := svc.Create(testDBConfig("sql-db")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	info, err := svc.UpdateSQL("sql-db", &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic})
	if err != nil {
		t.Fatalf("UpdateSQL() error = %v", err)
	}
	if info.SQL == nil || info.SQL.Concurrency != database.ConcurrencyPessimistic {
		t.Fatalf("UpdateSQL().SQL = %+v, want pessimistic", info.SQL)
	}

	var invalid *database.InvalidConcurrencyError
	if _, err := svc.UpdateSQL("sql-db", &database.SQLConfig{Concurrency: "eager"}); !errors.As(err, &invalid) {
		t.Fatalf("UpdateSQL(eager) = %v, want *InvalidConcurrencyError", err)
	}
	var notFound *database.NotFoundError
	if _, err := svc.UpdateSQL("missing", nil); !errors.As(err, &notFound) {
		t.Fatalf("UpdateSQL(missing) = %v, want *NotFoundError", err)
	}
}

func TestDatabaseService_UpdateSQLForTenant(t *testing.T) {
	svc := newTestDatabaseService(t)
	acme := tenant.WithTenantID(context.Background(), "acme")
	if _, err := svc.CreateForTenant(acme, testDBConfig("acme-sql")); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}
	pess := &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic}

	info, err := svc.UpdateSQLForTenant(acme, "acme-sql", pess)
	if err != nil {
		t.Fatalf("UpdateSQLForTenant() error = %v", err)
	}
	if info.SQL.EffectiveConcurrency() != database.ConcurrencyPessimistic {
		t.Fatalf("SQL = %+v, want pessimistic", info.SQL)
	}

	// Another tenant's database is answered exactly as a missing one.
	var notFound *database.NotFoundError
	other := tenant.WithTenantID(context.Background(), "globex")
	if _, err := svc.UpdateSQLForTenant(other, "acme-sql", pess); !errors.As(err, &notFound) {
		t.Fatalf("cross-tenant = %v, want *database.NotFoundError", err)
	}

	// The system tenant may target any database.
	system := tenant.WithTenantID(context.Background(), tenantid.System)
	if _, err := svc.UpdateSQLForTenant(system, "acme-sql", nil); err != nil {
		t.Fatalf("system UpdateSQLForTenant() error = %v", err)
	}
}
