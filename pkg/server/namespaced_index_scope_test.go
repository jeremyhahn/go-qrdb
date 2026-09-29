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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// The namespace the wrapper writes is the namespace the engine reads back
// (go-qrdb#410)
// ---------------------------------------------------------------------------

// recCreateIndex records the name and config an index create reaches the
// inner servicer with.
type recCreateIndex struct {
	service.IndexServicer
	lastName string
	lastCfg  index.IndexConfig
}

func (m *recCreateIndex) CreateIndex(_ context.Context, name, _ string, cfg index.IndexConfig) error {
	m.lastName = name
	m.lastCfg = cfg
	return nil
}

// TestNamespacedIndex_NameCarriesTheKeyNamespace is the load-bearing
// agreement between two packages. namespacedIndexService prefixes an index
// NAME with tenant.ComposeNamespace, and the engine derives an index's key
// scope by reading that namespace back off the name
// (engine.IndexNamespace). If either side changed its formula, index
// maintenance would silently widen to the whole shard again -- which is the
// defect go-qrdb#410 reported -- or narrow to nothing.
//
// The composite prefix is also what namespacedKVService applies to every KEY,
// so asserting the round trip here asserts that an index's scope is exactly
// its own (tenant, database) keyspace.
func TestNamespacedIndex_NameCarriesTheKeyNamespace(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
		database string
	}{
		{name: "ordinary pair", tenantID: "acme", database: "sales"},
		{name: "bootstrap pair", tenantID: tenantid.System, database: tenant.DefaultDatabase},
		{name: "empty falls back to the bootstrap pair", tenantID: "", database: ""},
		{name: "dotted database name", tenantID: "acme", database: "sales.v2"},
		{name: "index name shape a collection produces", tenantID: "acme", database: "sales"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := tenant.ComposeNamespace(tt.tenantID, tt.database)
			rec := &recCreateIndex{}
			w := newNamespacedIndexService(rec, prefix)

			if err := w.CreateIndex(context.Background(), "sales.users.email", "email", index.IndexConfig{
				Name:          "sales.users.email",
				CaseSensitive: true,
			}); err != nil {
				t.Fatalf("CreateIndex: %v", err)
			}

			got := engine.IndexNamespace(rec.lastName)
			if got != prefix {
				t.Fatalf("engine.IndexNamespace(%q) = %q; want the wrapper's prefix %q",
					rec.lastName, got, prefix)
			}
		})
	}
}

// TestNamespacedIndex_ScopeAdmitsOwnKeysOnly observes the same agreement from
// the key side: the scope the engine composes for a namespaced index admits
// the keys namespacedKVService writes for that (tenant, database) pair and no
// other pair's.
func TestNamespacedIndex_ScopeAdmitsOwnKeysOnly(t *testing.T) {
	prefix := tenant.ComposeNamespace("acme", "sales")
	rec := &recCreateIndex{}
	w := newNamespacedIndexService(rec, prefix)
	if err := w.CreateIndex(context.Background(), "users.email", "email", index.IndexConfig{}); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	scope := engine.ComposeIndexScope(rec.lastName, rec.lastCfg.KeyPrefix)

	own := prefix + "__col:sales:users/1"
	if !engine.KeyInScope(scope, []byte(own)) {
		t.Errorf("scope %q excludes its own key %q", scope, own)
	}

	for _, foreign := range []string{
		tenant.ComposeNamespace("evil", "sales") + "__col:sales:users/1",
		tenant.ComposeNamespace("acme", "hr") + "__col:hr:people/1",
		"__col:sales:users/1",
	} {
		if engine.KeyInScope(scope, []byte(foreign)) {
			t.Errorf("scope %q admits foreign key %q", scope, foreign)
		}
	}
}

// TestNamespacedIndex_CreateIndexKeepsDeclaredKeyPrefix states that the
// wrapper does NOT touch a declared key prefix. The prefix is relative to the
// namespace and the engine composes the two, so prefixing it here would apply
// the namespace twice.
func TestNamespacedIndex_CreateIndexKeepsDeclaredKeyPrefix(t *testing.T) {
	prefix := tenant.ComposeNamespace("acme", "sales")
	rec := &recCreateIndex{}
	w := newNamespacedIndexService(rec, prefix)

	if err := w.CreateIndex(context.Background(), "sales.users.email", "email", index.IndexConfig{
		KeyPrefix: "__col:sales:users/",
	}); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	if rec.lastCfg.KeyPrefix != "__col:sales:users/" {
		t.Fatalf("declared KeyPrefix = %q; want it passed through unchanged", rec.lastCfg.KeyPrefix)
	}
	scope := string(engine.ComposeIndexScope(rec.lastName, rec.lastCfg.KeyPrefix))
	if want := prefix + "__col:sales:users/"; scope != want {
		t.Fatalf("composed scope = %q; want %q", scope, want)
	}
}
