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

//go:build !integration

package node

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestClusteredNode_FailedBootLeavesNoDatabaseKey: a clustered boot that fails
// after its database manager is built -- here, cancelled once the tenant
// infrastructure exists, with a passphrase database's barrier unsealed in
// memory -- leaves no database key behind: the failure path closes the
// database manager, and closing it wipes every database barrier.
func TestClusteredNode_FailedBootLeavesNoDatabaseKey(t *testing.T) {
	ctx := context.Background()
	cfg := sealClustered(newRaftIsolationConfig(t))
	creds := func() seal.Credentials { return seal.MustCredentials([]byte("failed-boot-db-keys")) }

	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil,
		WithTenantIsolation(), WithDefaultTenantEntitlements(volumeEntitlements()))
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var bm *database.DatabaseBarrierManager
	prevHook := tenantInfraBuiltHook
	tenantInfraBuiltHook = func() {
		bm = n.dbManager.BarrierManager()
		if bm == nil {
			t.Error("the boot built no database barrier manager")
			cancel()
			return
		}
		if _, err := n.dbManager.CreateWithCredentials(ctx, database.DatabaseConfig{
			TenantID: "acme", Name: "secure", Engine: database.EngineKV, Backend: database.BackendMemory,
			DataDir: t.TempDir(),
			Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase},
		}, seal.MustCredentials([]byte("db-pass-123456"))); err != nil {
			t.Errorf("create the database mid-boot: %v", err)
		}
		if st, err := bm.BarrierStatus("secure"); err != nil || st.Sealed {
			t.Errorf("precondition: the database barrier is not unsealed mid-boot (%+v, %v)", st, err)
		}
		cancel()
	}
	err = n.Unseal(cancelCtx, creds())
	tenantInfraBuiltHook = prevHook
	if err == nil {
		t.Fatal("the cancelled boot succeeded")
	}
	if bm == nil {
		t.Fatal("the hook never ran")
	}
	enc := bm.GetBarrier("secure")
	st, serr := bm.BarrierStatus("secure")
	if serr != nil || !st.Sealed || enc == nil || !enc.IsSealed() {
		t.Fatalf("the database key outlived the failed boot (status %+v, %v)", st, serr)
	}
}
