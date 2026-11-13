// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials_test

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// tenantHomeShard is the Raft group the test's tenant home store lives in.
const tenantHomeShard = uint64(4201)

// replicatedCluster is three nodes that share one system shard and one
// tenant home group over Raft. Every node builds its credential stores the
// way a server does: the system store over the system shard's KV and
// transaction services (lifecycle.NewReplicatedSealBackend, the store
// lifecycle binds), and the tenant store over the home group's KV, index
// and transaction services (lifecycle.NewGuardedKVStoreAdapter, the store
// TenantHomes serves) derived with ForTenant beside a node-local barrier.
type replicatedCluster struct {
	system []*credentials.CredentialService
	tenant []*credentials.CredentialService
	// rotations counts the scheduled rotation values drawn per name,
	// across every node.
	rotations sync.Map
}

func (c *replicatedCluster) drawn(name string) int32 {
	n, ok := c.rotations.Load(name)
	if !ok {
		return 0
	}
	return n.(*atomic.Int32).Load()
}

func startReplicatedCluster(t *testing.T) *replicatedCluster {
	t.Helper()
	ctx := context.Background()
	raft := raftharness.NewCluster(t, 3)
	members := map[uint64]string{}
	for i, addr := range raft.Addrs {
		members[uint64(i+1)] = addr
	}
	createFn, err := statemachine.ResolveTyped("memory", "")
	require.NoError(t, err)
	for i, host := range raft.Hosts {
		require.NoError(t, host.StartShard(members, false, createFn, quicraftconfig.Config{
			ShardID: tenantHomeShard, ReplicaID: uint64(i + 1), MemberIdentities: raft.Identities,
		}))
	}
	raft.WaitReadable(t, tenantHomeShard, raft.Hosts...)

	c := &replicatedCluster{}
	source := credentials.WithRotationValueSource(func(_ context.Context, name string) (*secure.Buffer, error) {
		n, _ := c.rotations.LoadOrStore(name, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		return secure.NewBufferFromBytes([]byte(name + "-rotated"))
	})
	logger := slog.New(slog.DiscardHandler)
	for _, host := range raft.Hosts {
		adapter := raftbackend.New(host)
		backend := lifecycle.NewReplicatedSealBackend(
			service.NewKVService(adapter, shard.ShardKV),
			service.NewTransactionService(adapter, shard.ShardKV),
		)
		systemStore, ok := backend.(credentials.Store)
		require.True(t, ok, "the replicated seal backend is a credential store")
		svc, err := credentials.NewCredentialService(&credentials.Config{Strategy: credentials.StrategyBarrier}, nil, logger, source)
		require.NoError(t, err)
		require.NoError(t, svc.BindStore(ctx, systemStore))

		homeKV, err := lifecycle.NewGuardedKVStoreAdapter(
			service.NewKVService(adapter, tenantHomeShard),
			service.NewIndexService(adapter, tenantHomeShard),
			service.NewTransactionService(adapter, tenantHomeShard),
		)
		require.NoError(t, err)
		home, err := credentials.NewKVStore(homeKV)
		require.NoError(t, err)
		tenantSvc, err := svc.ForTenant(ctx, home, newNodeBarrier(t, logger))
		require.NoError(t, err)

		c.system = append(c.system, svc)
		c.tenant = append(c.tenant, tenantSvc)
	}
	return c
}

// newNodeBarrier returns an initialised node-local tenant barrier, the
// legacy store ForTenant migrates from; it holds nothing here.
func newNodeBarrier(t *testing.T, logger *slog.Logger) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("node-master-password"))
	require.NoError(t, err)
	barrier, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	require.NoError(t, err)
	require.NoError(t, barrier.Initialize(ctx, seal.MustCredentials(ctx, []byte("node-password-123"))))
	t.Cleanup(func() { _ = barrier.Close() })
	return barrier
}

func retrieved(t *testing.T, svc *credentials.CredentialService, name string, version uint64) string {
	t.Helper()
	ctx := context.Background()
	var buf *secure.Buffer
	var err error
	if version == 0 {
		buf, err = svc.RetrieveCredentialBuffer(ctx, name)
	} else {
		buf, err = svc.RetrieveCredentialVersionBuffer(ctx, name, version)
	}
	require.NoError(t, err)
	defer buf.Destroy()
	var got string
	require.NoError(t, buf.WithBytes(func(b []byte) error {
		got = string(b)
		return nil
	}))
	return got
}

// TestReplicatedCluster runs the credential verbs through one node of a
// three-node cluster and reads them through the others, on the system
// store and on a tenant's home store.
func TestReplicatedCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a three-node Raft cluster")
	}
	c := startReplicatedCluster(t)
	ctx := context.Background()
	policy := credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	// The visibility subtest's policy is not due inside the RotateDue
	// subtest's window, so that window holds one due credential.
	later := credentials.RotationPolicy{Enabled: true, Interval: 30 * 24 * time.Hour, RetainVersions: 2}
	for _, store := range []struct {
		name  string
		nodes []*credentials.CredentialService
	}{
		{name: "system", nodes: c.system},
		{name: "tenant", nodes: c.tenant},
	} {
		nodes := store.nodes
		t.Run(store.name+"/policy and value visible on peers", func(t *testing.T) {
			name := store.name + "-visible"
			require.NoError(t, nodes[0].StoreCredential(ctx, name, []byte("v1")))
			require.NoError(t, nodes[0].SetRotationPolicy(ctx, name, later))
			for i, peer := range nodes[1:] {
				names, err := peer.ListCredentials(ctx)
				require.NoError(t, err)
				assert.Contains(t, names, name, "peer %d lists the credential", i+1)
				assert.Equal(t, "v1", retrieved(t, peer, name, 0), "peer %d reads the value", i+1)
				got, err := peer.RotationPolicy(ctx, name)
				require.NoError(t, err)
				assert.Equal(t, later, got, "peer %d reads the policy", i+1)
			}
		})

		t.Run(store.name+"/rotation and deletion seen through peers", func(t *testing.T) {
			name := store.name + "-rotated"
			require.NoError(t, nodes[0].StoreCredential(ctx, name, []byte("v1")))
			_, err := nodes[1].RotateCredential(ctx, name, credentials.RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
			require.NoError(t, err)
			for _, i := range []int{0, 2} {
				assert.Equal(t, "v2", retrieved(t, nodes[i], name, 0), "node %d reads the rotated value", i)
				assert.Equal(t, "v1", retrieved(t, nodes[i], name, 1), "node %d reads the grace version", i)
			}

			require.NoError(t, nodes[2].DeleteCredential(ctx, name))
			for _, i := range []int{0, 1} {
				_, err := nodes[i].RetrieveCredentialBuffer(ctx, name)
				require.ErrorIs(t, err, credentials.ErrCredentialNotFound, "node %d", i)
				_, err = nodes[i].RetrieveCredentialVersionBuffer(ctx, name, 1)
				require.Error(t, err, "node %d reads no version of a deleted credential", i)
				names, err := nodes[i].ListCredentials(ctx)
				require.NoError(t, err)
				assert.NotContains(t, names, name, "node %d", i)
			}
		})

		t.Run(store.name+"/concurrent RotateDue: one claim wins", func(t *testing.T) {
			name := store.name + "-due"
			require.NoError(t, nodes[0].StoreCredential(ctx, name, []byte("v1")))
			require.NoError(t, nodes[0].SetRotationPolicy(ctx, name, policy))

			due := time.Now().Add(2 * time.Hour)
			var rotated atomic.Int32
			var wg sync.WaitGroup
			for _, node := range nodes {
				wg.Go(func() {
					n, err := node.RotateDue(ctx, due)
					assert.NoError(t, err)
					rotated.Add(int32(n))
				})
			}
			wg.Wait()
			// The earlier subtests' credentials are not due by then, so
			// this name is the only rotation the three nodes can claim.
			assert.Equal(t, int32(1), rotated.Load(), "exactly one node rotates")
			assert.Equal(t, int32(1), c.drawn(name), "one rotation value is drawn")
			for i, node := range nodes {
				assert.Equal(t, name+"-rotated", retrieved(t, node, name, 0), "node %d reads the scheduled value", i)
			}
		})
	}
}
