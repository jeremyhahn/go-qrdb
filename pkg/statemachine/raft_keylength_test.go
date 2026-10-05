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

// This file is package statemachine_test (external), not statemachine:
// sdk/go/testutil/raftharness already imports pkg/statemachine, so a file
// inside the statemachine package itself that also imported raftharness
// would be a real import cycle. The !integration constraint matches every
// other raftharness-based test in this tree (e.g.
// cmd/qrdbd/raft_follower_adder_test.go): a real multi-node raft cluster
// test is a (slower) unit test here, not part of the +integration suite.
package statemachine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// TestRaft_MaxLengthKeyAppliesAndSurvivesRestart is go-qrdb#592 F1's real
// multi-node raft acceptance: a 65,535-byte key (protocol.MaxClientKeyLength,
// the exact boundary where a 2-byte key-length field's "N + keyLen"
// arithmetic wrapped in 16-bit arithmetic while keyLen was still a uint16)
// goes through the full pipeline -- client encode, raft replication, Apply
// on every one of 3 replicas running sm_storage: file -- applies on all of
// them, and the shard stays healthy afterward. The key also survives a
// replica restarting and recovering the state from its own disk, proving
// the fix holds across the exact replay path a rolling restart exercises.
func TestRaft_MaxLengthKeyAppliesAndSurvivesRestart(t *testing.T) {
	c := raftharness.NewCluster(t, 3)
	const shardID = 91
	members := map[uint64]string{}
	for i, a := range c.Addrs {
		members[uint64(i+1)] = a
	}
	roots := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	start := func(i int) {
		t.Helper()
		cf, err := statemachine.ResolveTypedWithStorage("disk", "", statemachine.StorageConfig{Root: roots[i], Kind: "file"})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Hosts[i].StartShard(members, false, cf, quicraftconfig.Config{
			ShardID: shardID, ReplicaID: uint64(i + 1), MemberIdentities: c.Identities,
		}); err != nil {
			t.Fatalf("start replica %d: %v", i+1, err)
		}
	}
	for i := range c.Hosts {
		start(i)
	}
	c.WaitReadable(t, shardID, c.Hosts...)

	propose := func(cmd []byte) error {
		t.Helper()
		var lastErr error
		deadline := time.Now().Add(30 * time.Second)
		for attempt := 0; time.Now().Before(deadline); attempt++ {
			h := c.Hosts[attempt%len(c.Hosts)]
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := h.SyncPropose(ctx, shardID, cmd)
			cancel()
			if err == nil {
				return nil
			}
			lastErr = err
			time.Sleep(200 * time.Millisecond)
		}
		return lastErr
	}
	health := func(label string) {
		t.Helper()
		cmd, err := kv.EncodePut([]byte("health"), []byte(label))
		if err != nil {
			t.Fatal(err)
		}
		if err := propose(cmd); err != nil {
			t.Fatalf("shard unhealthy after %s: %v", label, err)
		}
	}

	maxKey := strings.Repeat("k", 65535)
	putCmd, err := kv.EncodePut([]byte(maxKey), []byte("v"))
	if err != nil {
		t.Fatalf("encode a 65535-byte key: %v", err)
	}
	if err := propose(putCmd); err != nil {
		t.Fatalf("propose of a 65535-byte key failed: %v", err)
	}
	health("after max-length put")

	// Every replica must agree.
	for i, h := range c.Hosts {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		got, err := h.SyncRead(ctx, shardID, []byte(maxKey))
		cancel()
		if err != nil {
			t.Fatalf("replica %d: SyncRead: %v", i+1, err)
		}
		gb, _ := got.([]byte)
		if string(gb) != "v" {
			t.Fatalf("replica %d: max-length key = %q, want %q", i+1, gb, "v")
		}
	}

	// Restart replica 3 from its own disk state and confirm it recovers
	// the max-length key through the file engine's own recovery path.
	if err := c.Hosts[2].StopShard(shardID); err != nil {
		t.Fatalf("stop replica 3: %v", err)
	}
	start(2)
	c.WaitReadable(t, shardID, c.Hosts[2])
	health("restart")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := c.Hosts[2].SyncRead(ctx, shardID, []byte(maxKey))
	if err != nil {
		t.Fatalf("restarted replica: SyncRead: %v", err)
	}
	if gb, _ := got.([]byte); string(gb) != "v" {
		t.Fatalf("restarted replica lost the max-length key: got %q", gb)
	}
}
