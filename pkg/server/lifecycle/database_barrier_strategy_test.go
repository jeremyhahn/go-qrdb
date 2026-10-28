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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// TestBuildServices_DatabaseBarrierResolvesStrategiesThroughTheRegistry pins the
// composition: a hardware strategy a composer registers on Infra.StrategyRegistry
// is a barrier mode the node's database barrier manager accepts, as it already
// was for a tenant barrier.
func TestBuildServices_DatabaseBarrierResolvesStrategiesThroughTheRegistry(t *testing.T) {
	device, err := sealtest.NewDeviceStrategy("fake-device")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	reg := seal.NewDefaultStrategyRegistry()
	if err := reg.Register("fake-device", func() (seal.SealingStrategy, error) { return device, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	barrier, registry, credSvc := newTestBarrier(t)
	dbs := databasetest.NewManager(nil, t.TempDir())

	node := buildSeamNode(t, lifecycle.Infra{
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		DatabaseManager:   dbs,
		StrategyRegistry:  reg,
	})
	if node.Database == nil {
		t.Fatal("database service not built")
	}
	bm := dbs.BarrierManager()
	if bm == nil {
		t.Fatal("BuildServices wired no database barrier manager")
	}
	if !bm.SupportsMode("fake-device") {
		t.Fatal("a registered strategy is not a database barrier mode: Infra.StrategyRegistry did not reach the manager")
	}
	if bm.SupportsMode("unregistered") {
		t.Fatal("an unregistered mode is accepted")
	}
}

func TestBuildServices_DatabaseBarrierWithoutARegistryKeepsTheBuiltInModes(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	dbs := databasetest.NewManager(nil, t.TempDir())

	buildSeamNode(t, lifecycle.Infra{
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		DatabaseManager:   dbs,
	})
	bm := dbs.BarrierManager()
	if bm == nil {
		t.Fatal("BuildServices wired no database barrier manager")
	}
	if !bm.SupportsMode(database.BarrierModePassphrase) || !bm.SupportsMode(database.BarrierModeShamir) {
		t.Fatal("a built-in mode is unsupported with no registry")
	}
	if bm.SupportsMode("fake-device") {
		t.Fatal("a registry-only mode is supported with no registry")
	}
}
