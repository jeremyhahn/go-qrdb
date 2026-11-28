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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// floorReader is the read side of the tenant service's cluster isolation
// floor (pkg/tenant/isolation_policy.go).
type floorReader interface {
	ClusterIsolationPolicy(ctx context.Context) (tenant.ClusterIsolationPolicy, error)
}

// nodeFloor reads the cluster isolation floor a booted node's tenant
// service holds.
func nodeFloor(t *testing.T, n *Node) tenant.ClusterIsolationPolicy {
	t.Helper()
	svc := n.Services()
	require.NotNil(t, svc, "a booted node exposes its service graph")
	reader, ok := svc.Tenant.(floorReader)
	require.True(t, ok, "the node's tenant service (%T) holds a cluster isolation floor", svc.Tenant)
	got, err := reader.ClusterIsolationPolicy(context.Background())
	require.NoError(t, err)
	return got
}

// withFIPS makes the node's FIPS 140-3 probe report on for one test.
func withFIPS(t *testing.T) {
	t.Helper()
	prev := fipsEnabled
	fipsEnabled = func() bool { return true }
	t.Cleanup(func() { fipsEnabled = prev })
}

// newFloorStandaloneConfig is a standalone in-memory node with no listeners.
func newFloorStandaloneConfig(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	disabled := false
	cfg.Auth.Enabled = &disabled
	return cfg
}

// TestStandaloneNode_AppliesConfiguredIsolationFloor proves a standalone SDK
// node applies tenant.isolation_policy as cmd/qrdbd does: the configured
// floor is the tenant service's floor once the node is built.
func TestStandaloneNode_AppliesConfiguredIsolationFloor(t *testing.T) {
	cfg := newFloorStandaloneConfig(t)
	cfg.Tenant.IsolationPolicy = string(tenant.ClusterIsolationPolicyStrict)

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.Equal(t, tenant.ClusterIsolationPolicyStrict, nodeFloor(t, n))
}

// TestClusteredNode_AppliesConfiguredIsolationFloor proves the clustered
// boot path applies the configured floor too.
func TestClusteredNode_AppliesConfiguredIsolationFloor(t *testing.T) {
	cfg := newRaftIsolationConfig(t)
	cfg.Tenant.IsolationPolicy = string(tenant.ClusterIsolationPolicyStrict)

	n, err := NewClusteredNode(context.Background(), cfg, raftMembers(), nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(raftEntitlements()),
		WithNoSyncWrites(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.Equal(t, tenant.ClusterIsolationPolicyStrict, nodeFloor(t, n))
}

// TestStandaloneNode_FIPSForcesStrictFloor proves a FIPS 140-3 node
// tightens the floor to strict even when the configuration names a weaker
// one, as cmd/qrdbd's FIPS floor does.
func TestStandaloneNode_FIPSForcesStrictFloor(t *testing.T) {
	withFIPS(t)
	cfg := newFloorStandaloneConfig(t)

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.Equal(t, tenant.ClusterIsolationPolicyStrict, nodeFloor(t, n))
}

// TestClusteredNode_FIPSForcesStrictFloor proves the clustered boot path
// applies the FIPS strict floor.
func TestClusteredNode_FIPSForcesStrictFloor(t *testing.T) {
	withFIPS(t)
	cfg := newRaftIsolationConfig(t)

	n, err := NewClusteredNode(context.Background(), cfg, raftMembers(), nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(raftEntitlements()),
		WithNoSyncWrites(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.Equal(t, tenant.ClusterIsolationPolicyStrict, nodeFloor(t, n))
}

// weakFloorTenant is a tenant service whose cluster floor stays
// operator-access whatever is written: a peer that already moved the floor,
// or a tightening the node-key log probe refused.
type weakFloorTenant struct {
	tenant.TenantServicer
}

func (weakFloorTenant) SetClusterIsolationPolicy(context.Context, tenant.ClusterIsolationPolicy) error {
	return errors.New("tightening refused")
}

func (weakFloorTenant) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

// TestApplyIsolationFloor_FIPSRefusesNonStrictFloor proves a FIPS node whose
// floor does not read strict back, or whose tenant service holds no floor,
// refuses to boot with a typed error, and that a non-FIPS node is not
// refused by the same floor.
func TestApplyIsolationFloor_FIPSRefusesNonStrictFloor(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	n := &Node{}

	// The process's own mode decides the first expectation: a leg run with
	// GODEBUG=fips140=on/only is in FIPS mode before withFIPS forces it.
	err := n.applyIsolationFloor(context.Background(),
		&lifecycle.Node{Tenant: weakFloorTenant{}}, cfg)
	if fipsEnabled() {
		require.ErrorIs(t, err, lifecycle.ErrFIPSFloorNotStrict,
			"in FIPS mode a weaker stored floor is a boot refusal")
	} else {
		require.NoError(t, err, "outside FIPS a weaker stored floor is not a boot refusal")
	}

	withFIPS(t)
	for name, svc := range map[string]*lifecycle.Node{
		"floor reads operator-access": {Tenant: weakFloorTenant{}},
		"no tenant service":           {},
	} {
		t.Run(name, func(t *testing.T) {
			err := n.applyIsolationFloor(context.Background(), svc, cfg)
			var floorErr *IsolationFloorError
			require.ErrorAs(t, err, &floorErr)
			require.ErrorIs(t, err, lifecycle.ErrFIPSFloorNotStrict)
		})
	}
}
