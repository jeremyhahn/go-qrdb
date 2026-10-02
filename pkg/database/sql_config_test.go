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
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestSQLConfig_Validate(t *testing.T) {
	cases := map[Concurrency]bool{
		"":                     true,
		ConcurrencyOptimistic:  true,
		ConcurrencyPessimistic: true,
		"PESSIMISTIC":          false,
		"locking":              false,
	}
	for mode, ok := range cases {
		err := (&SQLConfig{Concurrency: mode}).Validate()
		if ok && err != nil {
			t.Errorf("Validate(%q) = %v, want nil", mode, err)
		}
		if !ok {
			var invalid *InvalidConcurrencyError
			if !errors.As(err, &invalid) {
				t.Errorf("Validate(%q) = %v, want *InvalidConcurrencyError", mode, err)
				continue
			}
			if !strings.Contains(invalid.Error(), string(mode)) {
				t.Errorf("message %q does not name the mode", invalid.Error())
			}
		}
	}
}

func TestSQLConfig_EffectiveConcurrency(t *testing.T) {
	var nilCfg *SQLConfig
	if got := nilCfg.EffectiveConcurrency(); got != ConcurrencyOptimistic {
		t.Errorf("nil = %q, want optimistic", got)
	}
	if got := (&SQLConfig{}).EffectiveConcurrency(); got != ConcurrencyOptimistic {
		t.Errorf("empty = %q, want optimistic", got)
	}
	if got := (&SQLConfig{Concurrency: ConcurrencyPessimistic}).EffectiveConcurrency(); got != ConcurrencyPessimistic {
		t.Errorf("pessimistic = %q", got)
	}
}

func TestDatabaseConfig_Validate_RefusesBadConcurrency(t *testing.T) {
	cfg := validMemoryConfig("bad-sql")
	cfg.SQL = &SQLConfig{Concurrency: "eager"}
	var invalid *InvalidConcurrencyError
	if err := cfg.Validate(); !errors.As(err, &invalid) {
		t.Fatalf("Validate() = %v, want *InvalidConcurrencyError", err)
	}
}

func TestManager_Create_PersistsSQLConfig(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)

	cfg := validMemoryConfig("sql-create")
	cfg.SQL = &SQLConfig{Concurrency: ConcurrencyPessimistic}
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got := db.SQLConfig().EffectiveConcurrency(); got != ConcurrencyPessimistic {
		t.Fatalf("SQLConfig() = %q, want pessimistic", got)
	}
	if db.Info().SQL == nil || db.Info().SQL.Concurrency != ConcurrencyPessimistic {
		t.Fatalf("Info().SQL = %+v, want pessimistic", db.Info().SQL)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()
	if err := mgr2.OpenAll(); err != nil {
		t.Fatalf("OpenAll() error = %v", err)
	}
	reopened, err := mgr2.Get("sql-create")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := reopened.SQLConfig().EffectiveConcurrency(); got != ConcurrencyPessimistic {
		t.Fatalf("after reopen SQLConfig() = %q, want pessimistic", got)
	}
}

func TestManager_UpdateSQL_PersistsAcrossReopen(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	if _, err := mgr.Create(validMemoryConfig("sql-update")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := mgr.UpdateSQL("sql-update", &SQLConfig{Concurrency: ConcurrencyPessimistic}); err != nil {
		t.Fatalf("UpdateSQL() error = %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()
	if err := mgr2.OpenAll(); err != nil {
		t.Fatalf("OpenAll() error = %v", err)
	}
	db, err := mgr2.Get("sql-update")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := db.SQLConfig().EffectiveConcurrency(); got != ConcurrencyPessimistic {
		t.Fatalf("SQLConfig() = %q, want pessimistic", got)
	}

	if err := mgr2.UpdateSQL("sql-update", nil); err != nil {
		t.Fatalf("UpdateSQL(nil) error = %v", err)
	}
	if got := db.SQLConfig().EffectiveConcurrency(); got != ConcurrencyOptimistic {
		t.Fatalf("after reset SQLConfig() = %q, want optimistic", got)
	}
}

func TestManager_UpdateSQL_Refusals(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	var notFound *NotFoundError
	if err := mgr.UpdateSQL("missing", &SQLConfig{Concurrency: ConcurrencyPessimistic}); !errors.As(err, &notFound) {
		t.Fatalf("UpdateSQL(missing) = %v, want *NotFoundError", err)
	}

	if _, err := mgr.Create(validMemoryConfig("sql-bad")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var invalid *InvalidConcurrencyError
	if err := mgr.UpdateSQL("sql-bad", &SQLConfig{Concurrency: "eager"}); !errors.As(err, &invalid) {
		t.Fatalf("UpdateSQL(eager) = %v, want *InvalidConcurrencyError", err)
	}
}

func TestManager_UpdateSQLForTenant(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA := tenantContext("tenant-a")
	if _, err := mgr.CreateForTenant(ctxA, validMemoryConfig("sql-tenant-db")); err != nil {
		t.Fatalf("CreateForTenant() error = %v", err)
	}
	pess := &SQLConfig{Concurrency: ConcurrencyPessimistic}
	if err := mgr.UpdateSQLForTenant(ctxA, "sql-tenant-db", pess); err != nil {
		t.Fatalf("UpdateSQLForTenant() error = %v", err)
	}

	// Another tenant's database is answered exactly as a missing one.
	var notFound *NotFoundError
	if err := mgr.UpdateSQLForTenant(tenantContext("tenant-b"), "sql-tenant-db", pess); !errors.As(err, &notFound) {
		t.Fatalf("cross-tenant = %v, want *NotFoundError", err)
	}
	if err := mgr.UpdateSQLForTenant(ctxA, "missing", pess); !errors.As(err, &notFound) {
		t.Fatalf("missing = %v, want *NotFoundError", err)
	}
	// No tenant is the system tenant, confined like any other: tenant-a's
	// database is missing to it.
	if err := mgr.UpdateSQLForTenant(context.Background(), "sql-tenant-db", pess); !errors.As(err, &notFound) {
		t.Fatalf("no tenant = %v, want *NotFoundError", err)
	}
}
