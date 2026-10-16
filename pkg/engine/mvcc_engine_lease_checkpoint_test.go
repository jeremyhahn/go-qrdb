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

package engine

import (
	"bytes"
	"context"
	"hash/fnv"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// checkpointEngine is an MVCC engine with a lessor on a fixed clock, over a
// storage that outlives it so a test can restart the engine on its state.
type checkpointEngine struct {
	eng *MVCCEngine
	lr  *lease.Lessor
	stg storage.Storage
}

// openCheckpointEngine opens an MVCC engine over stg with a fresh lessor
// restored from stg's lease table, as a replica does at start.
func openCheckpointEngine(t *testing.T, stg storage.Storage) *checkpointEngine {
	t.Helper()
	lr := lease.NewLessor(lease.WithClock(clocktest.New()))
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	return &checkpointEngine{eng: eng, lr: lr, stg: stg}
}

func (c *checkpointEngine) apply(t *testing.T, entries []sm.Entry) {
	t.Helper()
	for _, e := range entries {
		applyLeaseCmd(t, c.eng, e.Index, e.Cmd)
	}
}

// restart closes the engine and opens a new one on the same storage.
func (c *checkpointEngine) restart(t *testing.T) *checkpointEngine {
	t.Helper()
	if err := c.eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return openCheckpointEngine(t, c.stg)
}

func (c *checkpointEngine) close(t *testing.T) {
	t.Helper()
	if err := c.eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = c.stg.Close()
}

func (c *checkpointEngine) remaining(t *testing.T, id int64) int64 {
	t.Helper()
	l := c.lr.Lookup(id)
	if l == nil {
		t.Fatalf("lease %d not found", id)
	}
	return l.RemainingTTL
}

// storageImage returns every key and value in stg, in key order: the state a
// snapshot of the engine carries.
func storageImage(t *testing.T, stg storage.Storage) [][2][]byte {
	t.Helper()
	snap, err := stg.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()
	it := snap.NewIterator(&storage.IteratorOptions{})
	defer func() { _ = it.Close() }()
	var image [][2][]byte
	for it.SeekToFirst(); it.Valid(); it.Next() {
		image = append(image, [2][]byte{bytes.Clone(it.Key()), bytes.Clone(it.Value())})
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return image
}

// stateHash hashes stg's every key and value: two replicas with equal hashes
// hold the same replicated state.
func stateHash(t *testing.T, stg storage.Storage) uint64 {
	t.Helper()
	h := fnv.New64a()
	for _, kv := range storageImage(t, stg) {
		for _, b := range kv {
			var n [4]byte
			n[0], n[1], n[2], n[3] = byte(len(b)>>24), byte(len(b)>>16), byte(len(b)>>8), byte(len(b))
			_, _ = h.Write(n[:])
			_, _ = h.Write(b)
		}
	}
	return h.Sum64()
}

// imageHas reports whether any key in image starts with prefix.
func imageHas(image [][2][]byte, prefix string) bool {
	for _, kv := range image {
		if bytes.HasPrefix(kv[0], []byte(prefix)) {
			return true
		}
	}
	return false
}

// recoverFrom returns a fresh storage holding leader's state, as a replica
// that recovers from the leader's snapshot does.
func recoverFrom(t *testing.T, leader storage.Storage) storage.Storage {
	t.Helper()
	stg := memory.NewStorage()
	for _, kv := range storageImage(t, leader) {
		if err := stg.Set(kv[0], kv[1]); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	return stg
}

func checkpointEntries(t *testing.T, first uint64, cmds ...func() ([]byte, error)) []sm.Entry {
	t.Helper()
	entries := make([]sm.Entry, len(cmds))
	for i, encode := range cmds {
		cmd, err := encode()
		if err != nil {
			t.Fatalf("encode entry %d: %v", first+uint64(i), err)
		}
		entries[i] = sm.Entry{Index: first + uint64(i), Cmd: cmd}
	}
	return entries
}

// supersededCheckpointLog is a group's log in which a keep-alive applies
// between the leader sampling a checkpoint and the checkpoint applying: the
// grant at 1, the first checkpoint at 2 (it records rfrom), the keep-alive at
// 3 and, at 4, a checkpoint sampled at index 2 that still carries the TTL from
// before the renewal.
func supersededCheckpointLog(t *testing.T) []sm.Entry {
	t.Helper()
	return checkpointEntries(t, 1,
		func() ([]byte, error) { return protocol.EncodeLeaseGrant(600) },
		func() ([]byte, error) {
			return protocol.EncodeLeaseCheckpoint(0, 1, []lease.Checkpoint{{ID: 1, RemainingTTL: 600}})
		},
		func() ([]byte, error) { return protocol.EncodeLeaseKeepAlive(1) },
		func() ([]byte, error) {
			return protocol.EncodeLeaseCheckpoint(0, 2, []lease.Checkpoint{{ID: 1, RemainingTTL: 100}})
		},
	)
}

// TestMVCCEngine_ARenewalSupersedesAnEarlierCheckpoint: a checkpoint sampled
// before a renewal applied is rejected, and the renewal's TTL stands on the
// replica that applied it, after a restart, on a replica recovered from a
// snapshot taken between the two, and on one that replays the log entry by
// entry.
func TestMVCCEngine_ARenewalSupersedesAnEarlierCheckpoint(t *testing.T) {
	log := supersededCheckpointLog(t)

	t.Run("restart", func(t *testing.T) {
		n := openCheckpointEngine(t, memory.NewStorage())
		n.apply(t, log)
		if got := n.remaining(t, 1); got != 600 {
			t.Fatalf("remaining after the superseded checkpoint = %d, want 600", got)
		}
		n = n.restart(t)
		defer n.close(t)
		if got := n.remaining(t, 1); got != 600 {
			t.Fatalf("restarted remaining = %d, want the renewal's 600", got)
		}
	})

	t.Run("snapshot recovery", func(t *testing.T) {
		leader := openCheckpointEngine(t, memory.NewStorage())
		defer leader.close(t)
		leader.apply(t, log[:3])
		recovered := openCheckpointEngine(t, recoverFrom(t, leader.stg))
		recovered.apply(t, log[3:])
		recovered = recovered.restart(t)
		defer recovered.close(t)
		if got := recovered.remaining(t, 1); got != 600 {
			t.Fatalf("snapshot-recovered remaining = %d, want the renewal's 600", got)
		}
	})

	t.Run("replay", func(t *testing.T) {
		replayed := openCheckpointEngine(t, memory.NewStorage())
		for _, e := range log {
			replayed.apply(t, []sm.Entry{e})
			replayed = replayed.restart(t)
		}
		defer replayed.close(t)
		if got := replayed.remaining(t, 1); got != 600 {
			t.Fatalf("replayed remaining = %d, want the renewal's 600", got)
		}
	})
}

// TestMVCCEngine_AnUnsupersededCheckpointApplies: a checkpoint sampled after
// the lease's last renewal applies, and a restarted replica counts the lease
// from it.
func TestMVCCEngine_AnUnsupersededCheckpointApplies(t *testing.T) {
	log := supersededCheckpointLog(t)
	unsuperseded := append(log[:2:2], checkpointEntries(t, 3, func() ([]byte, error) {
		return protocol.EncodeLeaseCheckpoint(0, 2, []lease.Checkpoint{{ID: 1, RemainingTTL: 100}})
	})...)

	n := openCheckpointEngine(t, memory.NewStorage())
	n.apply(t, unsuperseded)
	n = n.restart(t)
	defer n.close(t)
	if got := n.remaining(t, 1); got != 100 {
		t.Fatalf("restarted remaining = %d, want the checkpointed 100", got)
	}
}

// TestMVCCEngine_WithoutACheckpointRenewalsAreNotRecorded: a group that never
// applied a checkpoint records neither a renewal nor the rfrom marker, so its
// snapshot carries the records it carried before checkpoints existed.
func TestMVCCEngine_WithoutACheckpointRenewalsAreNotRecorded(t *testing.T) {
	log := supersededCheckpointLog(t)

	n := openCheckpointEngine(t, memory.NewStorage())
	n.apply(t, log[:1])
	n.apply(t, checkpointEntries(t, 2, func() ([]byte, error) { return protocol.EncodeLeaseKeepAlive(1) }))
	image := storageImage(t, n.stg)

	// The control: a group that did apply a checkpoint carries both records,
	// so their absence above is a finding.
	control := openCheckpointEngine(t, memory.NewStorage())
	defer control.close(t)
	control.apply(t, log[:3])
	controlImage := storageImage(t, control.stg)

	for _, prefix := range []string{lease.TablePrefix + "r/", lease.TablePrefix + "rfrom"} {
		if imageHas(image, prefix) {
			t.Fatalf("snapshot carries %q with no checkpoint applied", prefix)
		}
		if !imageHas(controlImage, prefix) {
			t.Fatalf("control snapshot lacks %q: the absence check cannot fail", prefix)
		}
	}

	n = n.restart(t)
	defer n.close(t)
	if got := n.remaining(t, 1); got != 600 {
		t.Fatalf("restarted remaining = %d, want a full 600", got)
	}
}

// TestMVCCEngine_CheckpointGateOffLeavesTheStateHashUnchanged: with the
// lease-checkpoint gate off no checkpoint is proposed, so a keep-alive leaves
// the replicated state exactly as it was. The control shows a keep-alive after
// a checkpoint does change it, so the comparison can fail.
func TestMVCCEngine_CheckpointGateOffLeavesTheStateHashUnchanged(t *testing.T) {
	log := supersededCheckpointLog(t)

	n := openCheckpointEngine(t, memory.NewStorage())
	defer n.close(t)
	n.apply(t, log[:1])
	granted := stateHash(t, n.stg)
	n.apply(t, checkpointEntries(t, 2,
		func() ([]byte, error) { return protocol.EncodeLeaseKeepAlive(1) },
		func() ([]byte, error) { return protocol.EncodeLeaseKeepAlive(1) },
	))
	if got := stateHash(t, n.stg); got != granted {
		t.Fatal("a keep-alive with no checkpoint applied changed the replicated state")
	}

	control := openCheckpointEngine(t, memory.NewStorage())
	defer control.close(t)
	control.apply(t, log[:2])
	marked := stateHash(t, control.stg)
	control.apply(t, log[2:3])
	if stateHash(t, control.stg) == marked {
		t.Fatal("a keep-alive after the first checkpoint recorded no renewal")
	}
}
