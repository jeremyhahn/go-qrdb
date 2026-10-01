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
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// isolationLookup answers GetTenant from a fixed isolation map; an unknown
// tenant is tenant.NotFoundError-shaped, as the tenant service answers.
type isolationLookup map[string]tenant.IsolationType

func (l isolationLookup) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	iso, ok := l[id]
	if !ok {
		return nil, &TenantNotFoundError{TenantID: id}
	}
	return &tenant.TenantInfo{ID: id, IsolationType: iso}, nil
}

// A barrier-enabled database of a volume- or raft-isolated tenant is served
// from the tenant's own store, never the per-database store its barrier
// wraps. Manager.Open cannot see tenant isolation, so it warned only for
// backend: raft records; such a record written before creation refused it
// opened silently. The boot audit warns for it, and only for it.
func TestWarnBarriersOffDataPath_WarnsForIsolatedTenantBarriers(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	barrier := &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	dbs := []database.DatabaseInfo{
		{TenantID: "vol", Name: "vault", Barrier: barrier},
		{TenantID: "raftt", Name: "ledger", Barrier: barrier},
		{TenantID: "prefix", Name: "ondisk", Barrier: barrier},
		{TenantID: "vol", Name: "plain"},
		{TenantID: "", Name: "sysdb", Barrier: barrier},
		{TenantID: "ghost", Name: "orphan", Barrier: barrier},
	}
	tenants := isolationLookup{
		"vol":    tenant.IsolationVolume,
		"raftt":  tenant.IsolationRaft,
		"prefix": tenant.IsolationPrefix,
	}

	WarnBarriersOffDataPath(context.Background(), dbs, tenants, logger)

	out := logs.String()
	for _, want := range []string{"name=vault", "name=ledger"} {
		if !strings.Contains(out, want) {
			t.Errorf("no warning for %s in %q", want, out)
		}
	}
	for _, not := range []string{"name=ondisk", "name=plain", "name=sysdb", "name=orphan"} {
		if strings.Contains(out, not) {
			t.Errorf("warned for %s: %q", not, out)
		}
	}
	if got := strings.Count(out, database.BarrierOffDataPathWarning); got != 2 {
		t.Fatalf("%d warnings, want 2: %q", got, out)
	}
}

// Without a tenant lookup nothing can be classified, and nothing is logged.
func TestWarnBarriersOffDataPath_NoLookup(t *testing.T) {
	var logs bytes.Buffer
	WarnBarriersOffDataPath(context.Background(),
		[]database.DatabaseInfo{{TenantID: "vol", Name: "vault", Barrier: &database.DatabaseBarrierConfig{Enabled: true}}},
		nil, slog.New(slog.NewTextHandler(&logs, nil)))
	if logs.Len() != 0 {
		t.Fatalf("logged %q without a tenant lookup", logs.String())
	}
}
