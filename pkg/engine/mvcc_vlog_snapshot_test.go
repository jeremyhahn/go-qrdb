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
	"context"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// interleavingStorage commits an update in the middle of a log read, without
// a second goroutine: armed with mid, the next iterator opened on it or on any
// snapshot of it runs mid first. A log read opens its first iterator after it
// has read the log's head row and before it reads the rows that head
// describes, which is exactly where a concurrent commit would land.
type interleavingStorage struct {
	storage.Storage
	mid func()
}

// fire runs the armed update once. It disarms first, so the update's own
// iterators do not run it again.
func (s *interleavingStorage) fire() {
	if mid := s.mid; mid != nil {
		s.mid = nil
		mid()
	}
}

func (s *interleavingStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	s.fire()
	return s.Storage.NewIterator(opts)
}

func (s *interleavingStorage) Snapshot() (storage.Snapshot, error) {
	snap, err := s.Storage.Snapshot()
	if err != nil {
		return nil, err
	}
	return &interleavingSnapshot{Snapshot: snap, fire: s.fire}, nil
}

// interleavingSnapshot is a snapshot of an interleavingStorage: the update
// still commits mid-read, to the storage, and the snapshot must not see it.
type interleavingSnapshot struct {
	storage.Snapshot
	fire func()
}

func (s *interleavingSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	s.fire()
	return s.Snapshot.NewIterator(opts)
}

// newInterleavingEngine builds an engine with a log registry over an
// interleavingStorage holding a created log of four entries.
func newInterleavingEngine(t *testing.T) (*MVCCEngine, *interleavingStorage) {
	t.Helper()
	stg := &interleavingStorage{Storage: memory.NewStorage()}
	t.Cleanup(func() { _ = stg.Close() })
	eng, err := NewMVCCEngine(stg, WithMVCCLogs(vlog.NewChain()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	for i, r := range applyMVCCEntries(t, eng, []sm.Entry{vlogCreateEntry(1), vlogAppendEntry(2, "a", "b", "c", "d")}) {
		if msg, ok := protocol.DecodeAppError(r.Data); ok {
			t.Fatalf("setup entry %d refused: %s", i, msg)
		}
	}
	return eng, stg
}

// TestMVCCEngine_VerifiableLog_ReadHoldsOneCommit pins review 5201 [P2]: a
// log read is answered from one commit even when an update commits between
// its storage reads. Over live storage, Get read the head's low-watermark
// from before an ExpireBodies and then met the bodies after it, failing with
// a CorruptTableError; ReadHead combined the root from before a Checkpoint
// with the checkpoint after it.
func TestMVCCEngine_VerifiableLog_ReadHoldsOneCommit(t *testing.T) {
	ctx := context.Background()

	t.Run("get across expire bodies", func(t *testing.T) {
		eng, stg := newInterleavingEngine(t)
		req := vlog.GetRequest{ID: engineLog, From: 0, Limit: 10}
		before, err := eng.Lookup(ctx, req)
		if err != nil {
			t.Fatalf("Lookup before: %v", err)
		}

		stg.mid = func() {
			applyMVCCEntries(t, eng, []sm.Entry{{Index: 3, Cmd: mustEncode(protocol.EncodeVerifiableLogExpireBodies(engineLog, 2))}})
		}
		got, err := eng.Lookup(ctx, req)
		if err != nil {
			t.Fatalf("Lookup with an expiry committed mid-read: %v", err)
		}
		if !reflect.DeepEqual(got, before) {
			t.Fatalf("Lookup = %#v, want the pre-expiry entries %#v", got, before)
		}
		if stg.mid != nil {
			t.Fatal("the expiry never committed: the read opened no iterator")
		}
		if head := lookupHead(t, eng); head.BodiesFrom != 2 {
			t.Fatalf("after the read, bodies from = %d, want 2", head.BodiesFrom)
		}
	})

	t.Run("head across checkpoint", func(t *testing.T) {
		eng, stg := newInterleavingEngine(t)
		before := lookupHead(t, eng)

		// The checkpoint names the tree after one more append; a peer that
		// applied the same entries supplies its root.
		peer, _ := newMVCCEngine(t, WithMVCCLogs(vlog.NewChain()))
		applyMVCCEntries(t, peer, []sm.Entry{vlogCreateEntry(1), vlogAppendEntry(2, "a", "b", "c", "d"), vlogAppendEntry(3, "e")})
		next := lookupHead(t, peer)

		stg.mid = func() {
			for i, r := range applyMVCCEntries(t, eng, []sm.Entry{
				vlogAppendEntry(3, "e"),
				{Index: 4, Cmd: mustEncode(protocol.EncodeVerifiableLogCheckpoint(engineLog, next.Size, next.Root, []byte("signed-5")))},
			}) {
				if msg, ok := protocol.DecodeAppError(r.Data); ok {
					t.Errorf("mid-read entry %d refused: %s", i, msg)
				}
			}
		}
		got := lookupHead(t, eng)
		if !reflect.DeepEqual(got, before) {
			t.Fatalf("head = %+v, want the pre-checkpoint head %+v", got, before)
		}
		if stg.mid != nil {
			t.Fatal("the checkpoint never committed: the read opened no iterator")
		}
		if after := lookupHead(t, eng); after.Size != next.Size || after.CheckpointSize != next.Size {
			t.Fatalf("after the read, head = %+v, want size and checkpoint %d", after, next.Size)
		}
	})
}
