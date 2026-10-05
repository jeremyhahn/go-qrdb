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
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestClusteredNode_EmbeddedClientServesTheRaftOnlyFamilies: on a raft node
// the in-process client serves every raft-only family. The lock family was
// never wired into the embedded transport, so node.Client().Lock() answered a
// wiring-defect error in the one mode that serves locks.
func TestClusteredNode_EmbeddedClientServesTheRaftOnlyFamilies(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)
	// This test is about which service families the embedded transport
	// wires on a raft node, not about RBAC; disabled HERE rather than in
	// newRaftIsolationConfig itself, which cluster_formation_test.go's
	// auth-method-rebinding test also builds its config from and needs
	// auth ENABLED for.
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)
	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil, WithNoSyncWrites())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	awaitWritable(ctx, t, n)

	lock, err := n.Client().Lock().LockAcquire(ctx, &transport.LockAcquireRequest{
		Key: "embedded/lock", HolderID: "holder", TTL: 30,
	})
	require.NoError(t, err, "the lock family must be served in process on a raft node")
	require.Equal(t, "embedded/lock", lock.Key)

	lease, err := n.Client().Lease().LeaseGrant(ctx, 30)
	require.NoError(t, err, "the lease family must be served in process on a raft node")
	require.NotZero(t, lease.ID)
}

// awaitWritable waits for the single member to elect itself and accept a
// write, bounded so a node that never becomes writable fails the test.
func awaitWritable(ctx context.Context, t *testing.T, n *Node) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		err := n.Client().KV().Put(ctx, "embedded/ready", []byte("1"))
		if err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the clustered node never accepted a write: %v", err)
		case <-tick.C:
		}
	}
}
