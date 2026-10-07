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

package lifecycle

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// TestDatabaseUnsealGate_VetoesASealedRecordOrAnUnknownDatabase: the node
// unseal's database veto follows the database's own cluster seal record,
// named by its owner (the system tenant for an unowned database), and refuses
// a database this node cannot name an owner for.
func TestDatabaseUnsealGate_VetoesASealedRecordOrAnUnknownDatabase(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	for _, cfg := range []database.DatabaseConfig{
		{TenantID: "acme", Name: "orders", Backend: database.BackendMemory},
		{Name: "platform", Backend: database.BackendMemory},
	} {
		if _, err := mgr.Create(cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}
	sealer := clustersealtest.New(true)
	gate := databaseUnsealGate(mgr, sealer)

	if gate("orders") || gate("platform") {
		t.Fatal("a database with no sealed record was vetoed")
	}
	sealer.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier("acme", "orders"), Epoch: 1, Sealed: true})
	sealer.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier("system", "platform"), Epoch: 1, Sealed: true})
	if !gate("orders") {
		t.Fatal("a database whose record is sealed was not vetoed")
	}
	if !gate("platform") {
		t.Fatal("an unowned database's record, the system tenant's, was not consulted")
	}
	if !gate("ghost") {
		t.Fatal("a database this node cannot name an owner for was not vetoed")
	}
}
