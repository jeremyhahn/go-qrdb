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

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestClusteredCreateFunc_AppliesVerifiableLog pins that a replica an SDK
// clustered node builds applies protocol.OpVerifiableLog, as a daemon replica
// does. The node advertises featuregate.VerifiableLog, so once the gate
// activates the opcode is proposed to every replica; a replica built without
// a log registry would answer it with a capability refusal while its peers
// grow the log. Both clustered boot paths take their state machines from
// clusteredCreateFunc, so this covers each of them.
func TestClusteredCreateFunc_AppliesVerifiableLog(t *testing.T) {
	require.Contains(t, featuregate.Supported(), featuregate.VerifiableLog,
		"the premise: this binary advertises the verifiable log")

	id := vlog.LogID{TenantID: "acme", Database: "certs", Log: "issued"}
	desc := vlog.Descriptor{Origin: "db.example.com/acme/certs", Hash: vlog.HashSHA256, VerifierKeys: []string{"key-1"}}
	create, err := protocol.EncodeVerifiableLogCreate(id, desc)
	require.NoError(t, err)
	appendOne, err := protocol.EncodeVerifiableLogAppend(id, [][]byte{[]byte("a")})
	require.NoError(t, err)

	for _, smType := range []string{"", "pebble", "mvcc"} {
		t.Run("sm_type="+smType, func(t *testing.T) {
			cfg := qrdbconfig.DefaultConfig()
			cfg.Raft.SMType = smType
			fn, err := clusteredCreateFunc(cfg, nil, nil, nil, nil, nil)
			require.NoError(t, err)

			machine, ok := fn(1, 1).(sm.DiskStateMachine)
			require.True(t, ok, "the clustered MVCC store is a disk state machine")
			_, err = machine.Open(context.Background(), t.TempDir(), make(chan struct{}))
			require.NoError(t, err)
			t.Cleanup(func() { _ = machine.Close(context.Background()) })

			entries := []sm.Entry{{Index: 1, Cmd: create}, {Index: 2, Cmd: appendOne}}
			results := make([]sm.Result, len(entries))
			require.NoError(t, machine.Update(context.Background(), entries, results))
			for i, res := range results {
				msg, refused := protocol.DecodeAppError(res.Data)
				require.False(t, refused, "entry %d refused: %s", i+1, msg)
			}

			got, err := machine.Lookup(context.Background(), vlog.HeadRequest{ID: id})
			require.NoError(t, err)
			head, ok := got.(*vlog.Head)
			require.True(t, ok, "Lookup(HeadRequest) = %T, want *vlog.Head", got)
			require.Equal(t, uint64(1), head.Size)
		})
	}
}

// TestClusteredCreateFunc_LogRegistryPerReplica pins that two state machines
// a clustered node builds -- two Raft groups on one node -- keep separate
// logs: a log created in one is unknown to the other.
func TestClusteredCreateFunc_LogRegistryPerReplica(t *testing.T) {
	id := vlog.LogID{TenantID: "acme", Database: "certs", Log: "issued"}
	create, err := protocol.EncodeVerifiableLogCreate(id, vlog.Descriptor{Origin: "db.example.com/acme/certs", Hash: vlog.HashSHA256, VerifierKeys: []string{"key-1"}})
	require.NoError(t, err)

	fn, err := clusteredCreateFunc(qrdbconfig.DefaultConfig(), nil, nil, nil, nil, nil)
	require.NoError(t, err)
	open := func(shardID uint64) sm.DiskStateMachine {
		machine := fn(shardID, 1).(sm.DiskStateMachine)
		_, err := machine.Open(context.Background(), t.TempDir(), make(chan struct{}))
		require.NoError(t, err)
		t.Cleanup(func() { _ = machine.Close(context.Background()) })
		return machine
	}
	first, second := open(1), open(2)

	results := make([]sm.Result, 1)
	require.NoError(t, first.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: create}}, results))
	_, refused := protocol.DecodeAppError(results[0].Data)
	require.False(t, refused)

	_, err = first.Lookup(context.Background(), vlog.HeadRequest{ID: id})
	require.NoError(t, err, "the group that created the log serves its head")
	_, err = second.Lookup(context.Background(), vlog.HeadRequest{ID: id})
	require.Error(t, err, "another group on the node must not see the log")
}
