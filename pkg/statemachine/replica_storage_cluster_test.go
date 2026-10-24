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

package statemachine_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// composerStorage is a composer's replica storage factory: it opens memory
// storage for each replica and records the identity and directory it was
// handed, or fails every call with err.
type composerStorage struct {
	mu    sync.Mutex
	dirs  map[uint64]string // replica ID -> directory
	shard map[uint64]uint64 // replica ID -> shard ID
	err   error
}

func newComposerStorage() *composerStorage {
	return &composerStorage{dirs: map[uint64]string{}, shard: map[uint64]uint64{}}
}

func (c *composerStorage) open(shardID, replicaID uint64, dir string) (storage.Storage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	c.dirs[replicaID] = dir
	c.shard[replicaID] = shardID
	return memstore.NewStorage(), nil
}

func TestReplicaStorageFactory_ThreeReplicaClusterOpensEveryReplica(t *testing.T) {
	for _, smType := range []string{"disk", "fast"} {
		t.Run(smType, func(t *testing.T) {
			c := raftharness.NewCluster(t, 3)
			const shardID = 92
			members := map[uint64]string{}
			for i, a := range c.Addrs {
				members[uint64(i+1)] = a
			}
			cs := newComposerStorage()
			roots := []string{t.TempDir(), t.TempDir(), t.TempDir()}
			for i, h := range c.Hosts {
				cf, err := statemachine.ResolveTypedWithStorage(smType, "", statemachine.StorageConfig{Root: roots[i], Replica: cs.open})
				if err != nil {
					t.Fatal(err)
				}
				if err := h.StartShard(members, false, cf, quicraftconfig.Config{
					ShardID: shardID, ReplicaID: uint64(i + 1), MemberIdentities: c.Identities,
				}); err != nil {
					t.Fatalf("start replica %d: %v", i+1, err)
				}
			}
			c.WaitReadable(t, shardID, c.Hosts...)

			cs.mu.Lock()
			for i := range c.Hosts {
				r := uint64(i + 1)
				want := filepath.Join(roots[i], "sm", "shard-92-replica-"+strconv.Itoa(i+1))
				if cs.shard[r] != shardID || cs.dirs[r] != want {
					t.Errorf("replica %d opened as shard %d in %q, want shard %d in %q", r, cs.shard[r], cs.dirs[r], shardID, want)
				}
			}
			cs.mu.Unlock()

			// The composer's storage carries the replicated state.
			cmd, err := kv.EncodePut([]byte("k"), []byte("v"))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(30 * time.Second)
			for attempt := 0; ; attempt++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, err = c.Hosts[attempt%len(c.Hosts)].SyncPropose(ctx, shardID, cmd)
				cancel()
				if err == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if err != nil {
				t.Fatalf("propose: %v", err)
			}
			for i, h := range c.Hosts {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				got, err := h.SyncRead(ctx, shardID, []byte("k"))
				cancel()
				if gb, _ := got.([]byte); err != nil || string(gb) != "v" {
					t.Fatalf("replica %d: read %q, %v; want %q", i+1, gb, err, "v")
				}
			}
		})
	}
}

func TestReplicaStorageFactory_FailureFailsTheReplicaOpen(t *testing.T) {
	for _, smType := range []string{"disk", "fast"} {
		t.Run(smType, func(t *testing.T) {
			c := raftharness.NewCluster(t, 1)
			cause := errors.New("composer storage unavailable")
			cs := newComposerStorage()
			cs.err = cause
			cf, err := statemachine.ResolveTypedWithStorage(smType, "", statemachine.StorageConfig{Root: t.TempDir(), Replica: cs.open})
			if err != nil {
				t.Fatal(err)
			}
			err = c.Hosts[0].StartShard(map[uint64]string{1: c.Addrs[0]}, false, cf, quicraftconfig.Config{
				ShardID: 93, ReplicaID: 1, MemberIdentities: c.Identities,
			})
			var openErr *statemachine.ReplicaStorageOpenError
			if !errors.As(err, &openErr) || openErr.ShardID != 93 || openErr.ReplicaID != 1 || !errors.Is(err, cause) {
				t.Fatalf("StartShard err = %v, want the typed replica storage failure", err)
			}
		})
	}
}
