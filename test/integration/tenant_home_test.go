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

//go:build integration

package integration_test

// TestTenantHome_RecordsLiveOnlyInTheHomeStore proves, on a real node in each
// mode, that every isolated tenant keeps its identity records in its home
// store and none in the system shard, whatever its isolation policy:
//
//   - Standalone, a barrier and a volume tenant; in raft mode, a raft, a
//     barrier and a volume tenant. Raft mode is a one-member cluster, so its
//     home groups are real Raft groups on their own WALs, under a passphrase
//     seal: a home group's WAL is under the tenant's key, and a host whose
//     system barrier does not encrypt cannot give it one.
//   - Each tenant is onboarded with its first admin, so its CA is created in
//     its home store too.
//   - The volume and raft tenants are created and written under an
//     operator-access floor; the barrier tenant under a strict floor.
//   - Each tenant's user and role are read back through the node's store
//     resolver -- the home store -- and probed by exact key in the system
//     shard under every form it could hold a tenant's record in. A shared
//     tenant's user, written the same way, IS found by the same probe, so
//     the probe can fail.
//   - The node is closed and reopened on the same data directory: every
//     record is still in its home store and still absent from the system
//     shard.
//
// TestTenant_StrictHome_AuthenticatesOnEveryNode covers the same homes across
// three qrdbd processes: placement on every node, catch-up and full restart.

import (
	"context"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	qrdbnode "github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// homeCase is one tenant the suite creates and probes.
type homeCase struct {
	id        string
	isolation tenant.IsolationType
	// floor is the cluster floor the tenant is created and written under.
	floor tenant.ClusterIsolationPolicy
	// raftOnly marks a type only raft mode offers.
	raftOnly bool
}

// homeCases are written in order: the floor only tightens, so every
// operator-access tenant comes before the strict one.
var homeCases = []homeCase{
	{id: "ledger", isolation: tenant.IsolationRaft, floor: tenant.ClusterIsolationPolicyOperatorAccess, raftOnly: true},
	{id: "archive", isolation: tenant.IsolationVolume, floor: tenant.ClusterIsolationPolicyOperatorAccess},
	{id: "safe", isolation: tenant.IsolationBarrier, floor: tenant.ClusterIsolationPolicyStrict},
}

// homeRecords are the exact keys one tenant's user and role occupy.
type homeRecords struct {
	tenantID string
	keys     []string
}

// homeNodeMode boots a node in one mode on cfg.
type homeNodeMode struct {
	name string
	raft bool
	// passphrase initializes, then unseals, a passphrase-sealed node.
	passphrase string
	config     func(t *testing.T) *qrdbconfig.Config
	open       func(ctx context.Context, cfg *qrdbconfig.Config) (*qrdbnode.Node, error)
}

func TestTenantHome_RecordsLiveOnlyInTheHomeStore(t *testing.T) {
	modes := []homeNodeMode{
		{
			name:   "standalone",
			config: homeNodeConfig,
			open: func(ctx context.Context, cfg *qrdbconfig.Config) (*qrdbnode.Node, error) {
				return qrdbnode.NewStandaloneNode(ctx, cfg, nil, homeNodeOptions()...)
			},
		},
		{
			name:       "raft",
			raft:       true,
			passphrase: "tenant-home-integration-secret",
			config: func(t *testing.T) *qrdbconfig.Config {
				cfg := homeNodeConfig(t)
				// A home group's WAL is under the tenant's key, which only
				// a host whose system barrier encrypts can give it.
				cfg.Seal.Strategy = string(seal.StrategyPassphrase)
				addr := fmt.Sprintf("127.0.0.1:%d", allocatePorts(t, 1)[0])
				cfg.Raft.ReplicaID = 1
				cfg.Raft.RaftAddress = addr
				cfg.Raft.ListenAddress = addr
				return cfg
			},
			open: func(ctx context.Context, cfg *qrdbconfig.Config) (*qrdbnode.Node, error) {
				members := map[uint64]string{1: cfg.Raft.RaftAddress}
				return qrdbnode.NewClusteredNode(ctx, cfg, members, nil, homeNodeOptions()...)
			},
		},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := mode.config(t)
			n := openHomeNode(t, mode, cfg)
			svc := n.Services()
			pools, ok := n.BarrierRegistry().VolumeService()
			if !ok {
				t.Fatal("the node attached no volume catalog")
			}
			if _, err := pools.CreatePool(ctx, volume.CreatePoolRequest{Name: "default"}); err != nil {
				t.Fatalf("CreatePool: %v", err)
			}
			floors, ok := svc.Tenant.(interface {
				SetClusterIsolationPolicy(context.Context, tenant.ClusterIsolationPolicy) error
			})
			if !ok {
				t.Fatalf("tenant service %T cannot set the cluster floor", svc.Tenant)
			}

			// The control: a shared tenant's user is in the system shard,
			// found by the same exact-key probe the isolated tenants must fail.
			onboardHomeTenant(t, n, "shared", tenant.IsolationPrefix)
			shared := writeHomeRecords(t, n, "shared")
			if found := systemShardHolds(t, n, shared); len(found) == 0 {
				t.Fatalf("the probe found none of the shared tenant's records %v in the system shard; it cannot fail", shared.keys)
			}

			// The node starts with no floor; each case sets the one it runs
			// under, the weaker first, then tightens to strict.
			var written []homeRecords
			var floor tenant.ClusterIsolationPolicy
			for _, tc := range homeCases {
				if tc.raftOnly && !mode.raft {
					continue
				}
				if tc.floor != floor {
					if err := floors.SetClusterIsolationPolicy(ctx, tc.floor); err != nil {
						t.Fatalf("SetClusterIsolationPolicy(%s): %v", tc.floor, err)
					}
					floor = tc.floor
				}
				onboardHomeTenant(t, n, tc.id, tc.isolation)
				recs := writeHomeRecords(t, n, tc.id)
				assertHomeConfined(t, n, recs)
				written = append(written, recs)
			}

			// A restart reopens every home store on the same data directory.
			if err := n.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			n = openHomeNode(t, mode, cfg)
			for _, recs := range written {
				assertHomeConfined(t, n, recs)
			}
		})
	}
}

// homeNodeConfig is a node with every API listener off, a seal of the
// default strategy none, and one volume pool for the volume and raft tenants'
// data roots.
func homeNodeConfig(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Path: t.TempDir()}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	// The suite calls the node's services in process; it is about where a
	// record lives, not who may write it.
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)
	return cfg
}

// homeNodeOptions entitles tenants to every isolation type.
func homeNodeOptions() []qrdbnode.NodeOption {
	return []qrdbnode.NodeOption{
		qrdbnode.WithTenantIsolation(),
		qrdbnode.WithNoSyncWrites(),
		qrdbnode.WithDefaultTenantEntitlements(&quota.Entitlements{
			AllowedIsolationModes: []string{
				quota.IsolationModePrefix, quota.IsolationModeBarrier,
				quota.IsolationModeVolume, quota.IsolationModeRaft,
			},
			AllowDedicatedRaft:        true,
			AllowDedicatedWAL:         true,
			AllowVolumeStorage:        true,
			AllowSystemManagedBarrier: true,
			AllowedVolumePools:        []string{"default"},
		}),
	}
}

// openHomeNode opens a node of mode on cfg and closes it when the test ends,
// unless the test closed it first.
func openHomeNode(t *testing.T, mode homeNodeMode, cfg *qrdbconfig.Config) *qrdbnode.Node {
	t.Helper()
	n, err := mode.open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open %s node: %v", mode.name, err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if n.IsSealed() && mode.passphrase != "" {
		if err := n.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte(mode.passphrase))); err != nil {
			t.Fatalf("unseal %s node: %v", mode.name, err)
		}
	}
	if n.IsSealed() {
		t.Fatalf("%s node is sealed", mode.name)
	}
	return n
}

// onboardHomeTenant creates tenantID with its first admin through the
// onboarding service every transport's tenant create delegates to, which is
// what provisions an isolated tenant's home. A barrier is system-managed.
func onboardHomeTenant(t *testing.T, n *qrdbnode.Node, tenantID string, isolation tenant.IsolationType) {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR(tenantID + "-admin")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	if _, err := n.Services().TenantOnboarding.OnboardTenant(context.Background(), &service.OnboardTenantRequest{
		ID:                tenantID,
		Name:              tenantID,
		IsolationType:     isolation,
		BarrierAutoUnseal: true,
		AdminUsername:     "admin",
		AdminCSRPEM:       csrPEM,
	}); err != nil {
		t.Fatalf("OnboardTenant(%s, %s): %v", tenantID, isolation, err)
	}
}

// writeHomeRecords creates a role and a user bound to it in tenantID, through
// the node's own IAM services, and returns the keys they occupy.
func writeHomeRecords(t *testing.T, n *qrdbnode.Node, tenantID string) homeRecords {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	svc := n.Services()
	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name: "reader", TenantID: tenantID, Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole(%s): %v", tenantID, err)
	}
	u, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "alice", Email: "alice@db.example.com", TenantID: tenantID, RoleIDs: []string{r.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", tenantID, err)
	}
	return homeRecords{tenantID: tenantID, keys: []string{
		user.PrimaryRecordKey(u.ID),
		"__sys:user_idx/username/" + tenantID + "/alice",
		"__sys:roles/id/" + r.ID,
		"__sys:role_idx/name/" + tenantID + "/reader",
	}}
}

// systemShardHolds returns every form of recs' keys the system shard holds:
// as is, in the tenant's prefix keyspace, or in the tenant's or the system
// tenant's identity namespace. Each probe is
// one exact-key read.
func systemShardHolds(t *testing.T, n *qrdbnode.Node, recs homeRecords) []string {
	t.Helper()
	kv := n.Services().KV
	var found []string
	for _, key := range recs.keys {
		for _, form := range []string{
			key,
			recs.tenantID + "/" + key,
			tenant.IdentityNamespace(recs.tenantID) + key,
			tenant.IdentityNamespace("system") + key,
		} {
			v, err := kv.Get(context.Background(), form)
			if err == nil && v != nil {
				found = append(found, form)
			}
		}
	}
	return found
}

// assertHomeConfined fails unless each of recs' keys is in the tenant's home
// store and no form of it is in the system shard.
func assertHomeConfined(t *testing.T, n *qrdbnode.Node, recs homeRecords) {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), recs.tenantID)
	home, err := n.Services().StoreResolver.Resolve(ctx, recs.tenantID)
	if err != nil {
		t.Fatalf("%s: resolve home store: %v", recs.tenantID, err)
	}
	for _, key := range recs.keys {
		v, err := home.Get(ctx, key)
		if err != nil || v == nil {
			t.Errorf("%s: %q is not in the home store: %v", recs.tenantID, key, err)
		}
	}
	if found := systemShardHolds(t, n, recs); len(found) > 0 {
		t.Errorf("%s: the system shard holds %q", recs.tenantID, found)
	}
}
