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

//go:build !integration && codec_json && raft

package node

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestClusteredNode_PublishesItsMemberRecord: a clustered embedded node
// publishes its lease-backed member record, as every raft-mode qrdbd does,
// without opting into dynamic membership. Replica forwarding resolves its
// targets from these records. Close withdraws it.
//
// This deliberately runs with newRaftIsolationConfig's default auth ON (the
// production default): featureClient, the node's own in-process client for
// this machinery, is node-internal and was never meant to need a caller
// identity (node.go's own doc comment on the field), so this proves the
// FIX -- excluding it from the embedded-authorization widening
// (sdk/go/node/node.go, the featureClientOpts snapshot) -- not a test that
// works only because RBAC happened to be off.
func TestClusteredNode_PublishesItsMemberRecord(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)
	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil, WithNoSyncWrites())
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = n.Close()
		}
	})

	require.Eventually(t, func() bool { return n.memberAdv.Load() != nil }, 30*time.Second, 50*time.Millisecond,
		"the clustered node never published its member record")

	members, err := n.featureClient.Load().NewMembershipStore().ListMembers(ctx)
	require.NoError(t, err)
	found := false
	for _, m := range members {
		if m.NodeID == cfg.Raft.ReplicaID && m.Address == cfg.Raft.RaftAddress {
			found = true
		}
	}
	require.True(t, found, "no member record for replica %d at %s in %+v", cfg.Raft.ReplicaID, cfg.Raft.RaftAddress, members)

	require.NoError(t, n.Close())
	closed = true
	require.Nil(t, n.memberAdv.Load(), "Close left the member record published")
}
