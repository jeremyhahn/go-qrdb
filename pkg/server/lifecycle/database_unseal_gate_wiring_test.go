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

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_WiresTheDatabaseUnsealGate pins the composition: a node
// built with a cluster sealer gives its database barrier manager the record
// gate, so the node unseal leaves sealed a database whose own cluster seal
// record is sealed, and only that one.
func TestBuildServices_WiresTheDatabaseUnsealGate(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()
	barrier, registry, credSvc := newTestBarrier(t)
	dbs := databasetest.NewManager(nil, t.TempDir())
	sealer := clustersealtest.New(true)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		DatabaseManager:   dbs,
		ClusterSealer:     sealer,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	for _, name := range []string{"orders", "ledger"} {
		if _, err := dbs.Create(database.DatabaseConfig{
			TenantID: "acme", Name: name, Backend: database.BackendMemory,
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	sealer.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier("acme", "orders"), Epoch: 1, Sealed: true})

	bm := dbs.BarrierManager()
	if bm == nil {
		t.Fatal("BuildServices wired no database barrier manager")
	}
	if !bm.UnsealRefused("orders", "acme") {
		t.Fatal("the database barrier manager does not veto a database whose record is sealed: the gate is not wired")
	}
	if bm.UnsealRefused("ledger", "acme") {
		t.Fatal("the database barrier manager vetoes a database whose record is clear")
	}
	if bm.UnsealRefused("orders", "beta") {
		t.Fatal("the database barrier manager vetoes another tenant's database of the same name")
	}
}
