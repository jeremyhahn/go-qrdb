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
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func newCompactionManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	for _, cfg := range []DatabaseConfig{
		{Name: "acme-db", TenantID: "acme", Engine: EngineKV, Backend: BackendMemory},
		{Name: "beta-db", TenantID: "beta", Engine: EngineKV, Backend: BackendMemory},
		{Name: "sys-db", Engine: EngineKV, Backend: BackendMemory},
	} {
		if _, err := m.Create(cfg); err != nil {
			t.Fatalf("Create(%s): %v", cfg.Name, err)
		}
	}
	return m
}

// The policy is persisted with the database, survives a reopen, and "off"
// is stored as no policy at all.
func TestManager_UpdateCompaction_PersistsAndClears(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	if _, err := m.Create(DatabaseConfig{Name: "app", Engine: EngineKV, Backend: BackendMemory}); err != nil {
		t.Fatal(err)
	}
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 7}
	if err := m.UpdateCompaction("app", policy); err != nil {
		t.Fatalf("UpdateCompaction: %v", err)
	}
	if err := m.UpdateCompaction("app", &compaction.Policy{Mode: "nope"}); err == nil {
		t.Fatal("an invalid policy was accepted")
	}
	if err := m.UpdateCompaction("missing", policy); err == nil {
		t.Fatal("a missing database took a policy")
	}
	_ = m.Close()

	reopened := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	t.Cleanup(func() { _ = reopened.Close() })
	db, err := reopened.Open("app")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := db.CompactionConfig(); got == nil || *got != *policy {
		t.Fatalf("policy after reopen = %+v, want %+v", got, policy)
	}
	if got := db.Info().Compaction; got == nil || *got != *policy {
		t.Fatalf("Info().Compaction = %+v", got)
	}
	if err := reopened.UpdateCompaction("app", &compaction.Policy{Mode: compaction.Off}); err != nil {
		t.Fatal(err)
	}
	if db.CompactionConfig() != nil {
		t.Fatal("an off policy was stored rather than cleared")
	}
}

// Every tenant, the system tenant included, sets policies on its own
// databases only; another tenant's database answers as missing.
func TestManager_UpdateCompactionForTenant_OwnDatabasesOnly(t *testing.T) {
	m := newCompactionManager(t)
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}
	tests := []struct {
		caller, db string
		allowed    bool
	}{
		{"acme", "acme-db", true},
		{"acme", "beta-db", false},
		{"acme", "sys-db", false},
		{tenant.SystemTenantID, "sys-db", true},
		{tenant.SystemTenantID, "acme-db", false},
		{"", "beta-db", false},
	}
	for _, tt := range tests {
		err := m.UpdateCompactionForTenant(tenant.WithTenantID(context.Background(), tt.caller), tt.db, policy)
		if tt.allowed {
			if err != nil {
				t.Errorf("%q on %s: %v", tt.caller, tt.db, err)
			}
			continue
		}
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			t.Errorf("%q on %s = %v, want NotFoundError", tt.caller, tt.db, err)
		}
	}
	if beta, _ := m.Get("beta-db"); beta.CompactionConfig() != nil {
		t.Fatal("a refused caller set beta's policy")
	}
	var nf *NotFoundError
	if err := m.UpdateCompactionForTenant(tenant.WithTenantID(context.Background(), "acme"), "nope", policy); !errors.As(err, &nf) {
		t.Fatalf("missing database = %v", err)
	}
}

// The owner of a database being dropped is told so; anyone else is told the
// database does not exist.
func TestManager_UpdateCompactionForTenant_DroppingDatabase(t *testing.T) {
	m := newCompactionManager(t)
	db, _ := m.Get("acme-db")
	db.SetDropping(true)
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}

	var dropping *DroppingError
	if err := m.UpdateCompactionForTenant(tenant.WithTenantID(context.Background(), "acme"), "acme-db", policy); !errors.As(err, &dropping) {
		t.Fatalf("owner on a dropping database = %v", err)
	}
	var nf *NotFoundError
	if err := m.UpdateCompactionForTenant(tenant.WithTenantID(context.Background(), "beta"), "acme-db", policy); !errors.As(err, &nf) {
		t.Fatalf("stranger on a dropping database = %v, want NotFoundError", err)
	}
}

func TestDatabaseConfig_ValidatesItsCompactionPolicy(t *testing.T) {
	cfg := DatabaseConfig{Name: "app", Engine: EngineKV, Backend: BackendMemory,
		Compaction: &compaction.Policy{Mode: compaction.Period, RetainPeriod: "0s"}}
	var invalid *compaction.InvalidPolicyError
	if err := cfg.Validate(); !errors.As(err, &invalid) {
		t.Fatalf("Validate = %v", err)
	}
}
