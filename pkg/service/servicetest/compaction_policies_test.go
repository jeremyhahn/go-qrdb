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

package servicetest

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The database service NewDatabaseService returns sets and reports a
// database's policy through the installed port.
func TestNewDatabaseService_InstallsThePort(t *testing.T) {
	mgr := database.NewManager(slog.New(slog.DiscardHandler), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	svc := NewDatabaseService(t, mgr)
	ctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)
	if _, err := svc.CreateForTenant(ctx, database.DatabaseConfig{Name: "events", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	info, err := svc.UpdateCompactionForTenant(ctx, "events", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 2})
	if err != nil || info.Compaction == nil || info.Compaction.RetainRevisions != 2 {
		t.Fatalf("UpdateCompactionForTenant = %+v, %v; want the policy", info, err)
	}
}

func TestCompactionPolicies_SetReadAndClear(t *testing.T) {
	p := NewCompactionPolicies()
	ctx := context.Background()
	if got, err := p.CompactionPolicy(ctx, "acme", "db"); got != nil || err != nil {
		t.Fatalf("unset policy = %+v, %v; want none", got, err)
	}
	if err := p.SetCompactionPolicy(ctx, "acme", "db", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}); err != nil {
		t.Fatalf("SetCompactionPolicy: %v", err)
	}
	got, err := p.CompactionPolicy(ctx, "acme", "db")
	if err != nil || got == nil || got.RetainRevisions != 3 {
		t.Fatalf("CompactionPolicy = %+v, %v; want the revision policy", got, err)
	}
	if other, _ := p.CompactionPolicy(ctx, "globex", "db"); other != nil {
		t.Fatalf("another tenant's database read acme's policy: %+v", other)
	}
	for _, off := range []*compaction.Policy{{Mode: compaction.Off}, nil} {
		if err := p.SetCompactionPolicy(ctx, "acme", "db", &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}); err != nil {
			t.Fatalf("SetCompactionPolicy: %v", err)
		}
		if err := p.SetCompactionPolicy(ctx, "acme", "db", off); err != nil {
			t.Fatalf("SetCompactionPolicy(off): %v", err)
		}
		if got, _ := p.CompactionPolicy(ctx, "acme", "db"); got != nil {
			t.Fatalf("policy after %v = %+v; want none", off, got)
		}
	}
}

func TestCompactionPolicies_RefusesAnInvalidPolicy(t *testing.T) {
	p := NewCompactionPolicies()
	err := p.SetCompactionPolicy(context.Background(), "acme", "db", &compaction.Policy{Mode: compaction.Revision})
	var invalid *compaction.InvalidPolicyError
	if !errors.As(err, &invalid) {
		t.Fatalf("SetCompactionPolicy = %v; want *compaction.InvalidPolicyError", err)
	}
}
