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

// package firstboot_test, not firstboot: this file builds a real, guarded
// KV store over a raftharness cluster the same way cmd/qrdbd itself does
// (pkg/service's own KV/Index/Transaction service constructors), and
// pkg/service imports back into pkg/firstboot (ceremony_admin_rebind.go,
// ceremony_leader_gate.go) -- an internal "package firstboot" test file
// importing pkg/service would close that into an import cycle the
// production code never has. See export_test.go for the two unexported
// symbols this file reaches through it.
package firstboot_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// discardLogger returns a logger that discards everything it is given --
// duplicated from firstboot_test.go's own trivial helper rather than
// exported through export_test.go, since there is nothing package-internal
// about it.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRegisterBuiltInRoles_ConcurrentSeedersConverge is go-qrdb's regression
// test for the firstboot role race (reproduced at 61d7cb049,
// TestClusterEnableEncryption_TenantScope_RF3): a node failed first boot with
// "register role auditor failed: role: id already exists" even though
// another seeder was about to create — or had just created — the identical
// canonical role.
//
// It runs over a REAL go-quicraft cluster (raftharness), not a fake: the
// defect is a visibility-lag race between a create collision (reported
// authoritatively by the LEADER) and the verifying read's own StaleRead,
// which a deterministic fake cannot model — pkg/role's own
// interrupted_create_test.go fakes exactly the shape of corruption
// WithAtomicIdentity rules out, not the replication timing that still
// surfaces around it.
//
// Two kinds of concurrent seeder race the SAME built-in roles, in the SAME
// system tenant, over the SAME real shard:
//
//   - N goroutines, one per cluster host, each running registerBuiltInRoles
//     exactly as cmd/qrdbd's runFirstBoot does on every node at boot.
//   - One more goroutine running role.RoleServicer.ReconcileBuiltInRoles
//     directly, over a store built exactly as pkg/server/lifecycle's
//     roleSeederAdapter now builds it (WithIdentityIndex/WithAtomicIdentity)
//     — modeling CreateTenant's RoleSeeder racing first-boot's own loop,
//     the second root cause this bug's investigation found.
//
// Every one of them must succeed, and every host must read back the
// identical canonical definition for every built-in role afterward.
func TestRegisterBuiltInRoles_ConcurrentSeedersConverge(t *testing.T) {
	const hostCount = 3
	const shardID = uint64(9001)

	c := raftharness.NewCluster(t, hostCount)
	members := make(map[uint64]string, hostCount)
	for i := range hostCount {
		members[uint64(i+1)] = c.Addrs[i]
	}
	for i := range hostCount {
		if err := c.Hosts[i].StartShard(members, false, c.CreateFn(), quicraftconfig.Config{
			ShardID: shardID, ReplicaID: uint64(i + 1), MemberIdentities: c.Identities,
		}); err != nil {
			t.Fatalf("StartShard(host %d): %v", i, err)
		}
	}
	waitShardReadableWithin(t, c.Hosts, shardID, extraShardReadyBound)

	// One role.RoleServicer per host, wired exactly like cmd/qrdbd's
	// boot-time role store (bootRoleSvc in bootstrapUserRoleStores): a
	// guarded, transaction-backed KVStore with an identity index built over
	// that SAME store, and WithAtomicIdentity so CreateRole commits the
	// claim and every write as one guarded batch.
	newAtomicRoleStore := func(hostIdx int) role.RoleServicer {
		t.Helper()
		host := raftbackend.New(c.Hosts[hostIdx])
		guarded, err := lifecycle.NewGuardedKVStoreAdapter(
			service.NewKVService(host, shardID),
			service.NewIndexService(host, shardID),
			service.NewTransactionService(host, shardID),
		)
		if err != nil {
			t.Fatalf("NewGuardedKVStoreAdapter(host %d): %v", hostIdx, err)
		}
		idx, err := tenant.NewIdentityIndex(guarded)
		if err != nil {
			t.Fatalf("NewIdentityIndex(host %d): %v", hostIdx, err)
		}
		svc, err := role.NewStore(guarded, role.WithIdentityIndex(idx), role.WithAtomicIdentity())
		if err != nil {
			t.Fatalf("role.NewStore(host %d): %v", hostIdx, err)
		}
		return svc
	}

	roleSvcs := make([]role.RoleServicer, hostCount)
	for i := range hostCount {
		roleSvcs[i] = newAtomicRoleStore(i)
	}
	// The tenant-seeder racer: a SEPARATE store construction over the same
	// shard (host 0), mirroring roleSeederAdapter.ReconcileBuiltInRoles
	// exactly — a fresh KVStore/identity-index pair, not a shared instance,
	// since that is what CreateTenant builds fresh on every call.
	seederSvc := newAtomicRoleStore(0)

	var wg sync.WaitGroup
	errs := make([]error, hostCount+1)

	for i := range hostCount {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result := &firstboot.Result{}
			_, err := firstboot.RegisterBuiltInRoles(context.Background(), firstboot.Dependencies{
				Roles:  roleSvcs[i],
				Logger: discardLogger(),
			}, tenantid.System, result)
			errs[i] = err
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := seederSvc.ReconcileBuiltInRoles(context.Background(), tenantid.System)
		errs[hostCount] = err
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("seeder %d: %v", i, err)
		}
	}
	if t.Failed() {
		return
	}

	// Convergence: every host reads back the SAME canonical definition for
	// every built-in role (Owner included — SeparationOfDuties defaults to
	// false).
	want := role.BuiltInRoles(tenantid.System)
	want = append(want, role.OwnerRole(tenantid.System))
	for _, w := range want {
		for i := range hostCount {
			got, err := roleSvcs[i].GetRoleByName(context.Background(), tenantid.System, w.Name)
			if err != nil {
				t.Fatalf("host %d: GetRoleByName(%q): %v", i, w.Name, err)
			}
			if !firstboot.RoleMatchesSeed(got, w) {
				t.Errorf("host %d: role %q = %+v, want canonical %+v", i, w.Name, got, w)
			}
		}
	}
}

// extraShardReadyBound bounds waitShardReadableWithin's wait for THIS
// test's own second shard (every raftharness.NewCluster host already forms
// and waits out shard.ShardKV's own election under raftharness.Bound, 2
// minutes, before this test starts). Longer than raftharness.Bound: under
// `make coverage`'s instrumentation overhead and full-suite parallel load,
// a SECOND election on the same hosts was observed to miss that window
// (two observed timeouts, each resolving on a bare retry under lighter
// load) -- a genuine host-contention risk, not a defect in the election
// itself, so the fix is more patience, not a different algorithm.
const extraShardReadyBound = 5 * time.Minute

// waitShardReadableWithin is raftharness.Cluster.WaitReadable with a wait
// bound this test controls directly, rather than the package's own fixed
// raftharness.Bound -- see extraShardReadyBound.
func waitShardReadableWithin(t testing.TB, hosts []*quicraft.Host, shardID uint64, bound time.Duration) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for _, host := range hosts {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), bound)
			_, err := host.SyncGetShardMembership(ctx, shardID)
			cancel()
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("waitShardReadableWithin: shard %d membership never became readable: %v", shardID, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
