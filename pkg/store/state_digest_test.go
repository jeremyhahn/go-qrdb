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

package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
)

// digestSM is the surface of the pebble and mvcc state machines these tests
// drive.
type digestSM interface {
	Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
	Lookup(ctx context.Context, query interface{}) (interface{}, error)
	PrepareSnapshot() (interface{}, error)
	SaveSnapshot(ctx context.Context, snapshotCtx interface{}, w io.Writer, stopper <-chan struct{}) error
	RecoverFromSnapshot(ctx context.Context, r io.Reader, stopper <-chan struct{}) error
	GetHash() (uint64, error)
	Close(ctx context.Context) error
}

// digestTarget opens one kind of Pebble-backed state machine and exposes its
// database so a test can write beneath the apply path.
type digestTarget struct {
	open func(t *testing.T) digestSM
	db   func(s digestSM) *pebble.DB
}

func digestTargets() map[string]digestTarget {
	return map[string]digestTarget{
		"pebble": {
			open: func(t *testing.T) digestSM { return newTestPebbleSM(t) },
			db:   func(s digestSM) *pebble.DB { return s.(*PebbleStateMachine).db },
		},
		"mvcc": {
			open: func(t *testing.T) digestSM { return newTestMVCCStateMachine(t) },
			db:   func(s digestSM) *pebble.DB { return s.(*MVCCStateMachine).db },
		},
	}
}

// digestEntries is a log of puts and deletes over printable and binary keys.
func digestEntries() []sm.Entry {
	var entries []sm.Entry
	for i := 1; i <= 40; i++ {
		idx := uint64(i)
		key := []byte(fmt.Sprintf("k/%02d", i%13))
		if i%2 == 0 {
			key = append([]byte{0x01}, key...)
		}
		if i%5 == 0 {
			entries = append(entries, mvccDeleteEntry(idx, key))
			continue
		}
		entries = append(entries, mvccPutEntry(idx, key, []byte(fmt.Sprintf("v%d", i)), 0))
	}
	return entries
}

// applyInBatches applies entries in batches of the given sizes, cycling.
func applyInBatches(t *testing.T, s digestSM, entries []sm.Entry, sizes ...int) {
	t.Helper()
	for off, i := 0, 0; off < len(entries); i++ {
		n := min(sizes[i%len(sizes)], len(entries)-off)
		if err := s.Update(context.Background(), entries[off:off+n], make([]sm.Result, n)); err != nil {
			t.Fatalf("Update: %v", err)
		}
		off += n
	}
}

func lookupStateDigest(t *testing.T, s digestSM) statedigest.Digest {
	t.Helper()
	got, err := s.Lookup(context.Background(), statedigest.Query{})
	if err != nil {
		t.Fatalf("Lookup(statedigest.Query): %v", err)
	}
	d, ok := got.(statedigest.Digest)
	if !ok {
		t.Fatalf("Lookup returned %T, want statedigest.Digest", got)
	}
	return d
}

func TestStateDigest_EmptyStateMachine(t *testing.T) {
	const emptyHex = "bf493fb5a08b61e017a6eb771e4ed6df171271bc3ab6a3a23cffa7346e8031ba"
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			d := lookupStateDigest(t, tgt.open(t))
			if d.AppliedIndex != 0 || d.Records != 0 || d.Hex() != emptyHex {
				t.Fatalf("empty digest = %d/%d/%s, want 0/0/%s", d.AppliedIndex, d.Records, d.Hex(), emptyHex)
			}
		})
	}
}

func TestStateDigest_ReportsTheAppliedIndex(t *testing.T) {
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			s := tgt.open(t)
			entries := digestEntries()
			applyInBatches(t, s, entries, 7)
			d := lookupStateDigest(t, s)
			if want := entries[len(entries)-1].Index; d.AppliedIndex != want {
				t.Fatalf("AppliedIndex = %d, want %d", d.AppliedIndex, want)
			}
			if d.Records == 0 {
				t.Fatal("Records = 0 after applying puts")
			}
		})
	}
}

func TestStateDigest_IndependentOfApplyBatching(t *testing.T) {
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			a, b := tgt.open(t), tgt.open(t)
			applyInBatches(t, a, digestEntries(), 1)
			applyInBatches(t, b, digestEntries(), 7, 3, 13)
			da, db := lookupStateDigest(t, a), lookupStateDigest(t, b)
			if da != db {
				t.Fatalf("replicas disagree: %d/%d/%s vs %d/%d/%s",
					da.AppliedIndex, da.Records, da.Hex(), db.AppliedIndex, db.Records, db.Hex())
			}
		})
	}
}

func TestStateDigest_DivergenceInjection(t *testing.T) {
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			healthy, damaged := tgt.open(t), tgt.open(t)
			applyInBatches(t, healthy, digestEntries(), 5)
			applyInBatches(t, damaged, digestEntries(), 5)

			// A write that bypassed the log: one replica's state now differs
			// while its applied index does not.
			if err := tgt.db(damaged).Set([]byte("k/rogue"), []byte("x"), pebble.Sync); err != nil {
				t.Fatalf("inject: %v", err)
			}
			h, d := lookupStateDigest(t, healthy), lookupStateDigest(t, damaged)
			if h.AppliedIndex != d.AppliedIndex {
				t.Fatalf("applied index moved: %d vs %d", h.AppliedIndex, d.AppliedIndex)
			}
			if h.Sum == d.Sum {
				t.Fatal("a diverged replica reported the same digest")
			}
			if d.Records != h.Records+1 {
				t.Fatalf("Records = %d, want %d", d.Records, h.Records+1)
			}
		})
	}
}

func TestStateDigest_SurvivesSnapshotRoundTrip(t *testing.T) {
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			src, dst := tgt.open(t), tgt.open(t)
			applyInBatches(t, src, digestEntries(), 4)

			var buf bytes.Buffer
			snap, err := src.PrepareSnapshot()
			if err != nil {
				t.Fatalf("PrepareSnapshot: %v", err)
			}
			if err := src.SaveSnapshot(context.Background(), snap, &buf, nil); err != nil {
				t.Fatalf("SaveSnapshot: %v", err)
			}
			if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
				t.Fatalf("RecoverFromSnapshot: %v", err)
			}
			// The applied index is node-local and does not travel; the
			// records do, so the sums agree.
			if s, d := lookupStateDigest(t, src), lookupStateDigest(t, dst); s.Sum != d.Sum || s.Records != d.Records {
				t.Fatalf("recovered replica disagrees: %s/%d vs %s/%d", s.Hex(), s.Records, d.Hex(), d.Records)
			}
		})
	}
}

func TestStateDigest_PinnedToItsSnapshot(t *testing.T) {
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			s := tgt.open(t)
			entries := digestEntries()
			applyInBatches(t, s, entries[:20], 20)
			before := lookupStateDigest(t, s)

			snap := tgt.db(s).NewSnapshot()
			defer func() { _ = snap.Close() }()
			applyInBatches(t, s, entries[20:], 20)

			pinned, err := pebbleStateDigest(snap)
			if err != nil {
				t.Fatalf("pebbleStateDigest: %v", err)
			}
			if pinned != before {
				t.Fatalf("digest of the earlier snapshot moved with later applies: %d/%s vs %d/%s",
					pinned.AppliedIndex, pinned.Hex(), before.AppliedIndex, before.Hex())
			}
			if after := lookupStateDigest(t, s); after.AppliedIndex != entries[len(entries)-1].Index {
				t.Fatalf("live AppliedIndex = %d, want %d", after.AppliedIndex, entries[len(entries)-1].Index)
			}
		})
	}
}

func TestStateDigest_AppliedIndexMarkerIsNotHashed(t *testing.T) {
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			s := tgt.open(t)
			applyInBatches(t, s, digestEntries(), 9)
			before := lookupStateDigest(t, s)

			var idx [8]byte
			binary.LittleEndian.PutUint64(idx[:], before.AppliedIndex+100)
			if err := tgt.db(s).Set([]byte(metaKeyLastApplied), idx[:], pebble.Sync); err != nil {
				t.Fatalf("set marker: %v", err)
			}
			after := lookupStateDigest(t, s)
			if after.AppliedIndex != before.AppliedIndex+100 {
				t.Fatalf("AppliedIndex = %d, want %d", after.AppliedIndex, before.AppliedIndex+100)
			}
			if after.Sum != before.Sum || after.Records != before.Records {
				t.Fatal("the applied-index marker entered the digest")
			}
		})
	}
}

func TestStateDigest_ImportStagingIsHashed(t *testing.T) {
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			s := tgt.open(t)
			before := lookupStateDigest(t, s)
			if err := tgt.db(s).Set(importstage.ChunkKey("sess", 1), []byte("chunk"), pebble.Sync); err != nil {
				t.Fatalf("stage: %v", err)
			}
			after := lookupStateDigest(t, s)
			if after.Sum == before.Sum || after.Records != 1 {
				t.Fatalf("a staged chunk was left out of the digest (%d records)", after.Records)
			}
		})
	}
}

func TestStateDigest_NotOpen(t *testing.T) {
	for name, s := range map[string]digestSM{
		"pebble": NewPebbleStateMachine(&pebble.Options{}),
		"mvcc":   NewMVCCStateMachine(nil),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Lookup(context.Background(), statedigest.Query{})
			var notOpen *NotOpenError
			if !errors.As(err, &notOpen) {
				t.Fatalf("err = %v, want *NotOpenError", err)
			}
		})
	}
}

func TestSnapshot_CarriesImportStaging(t *testing.T) {
	// Import staging is replicated, so a replica recovered from a snapshot
	// holds the chunks staged before it.
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			src, dst := tgt.open(t), tgt.open(t)
			key := importstage.ChunkKey("sess", 7)
			if err := tgt.db(src).Set(key, []byte("chunk"), pebble.Sync); err != nil {
				t.Fatalf("stage: %v", err)
			}
			var buf bytes.Buffer
			snap, err := src.PrepareSnapshot()
			if err != nil {
				t.Fatalf("PrepareSnapshot: %v", err)
			}
			if err := src.SaveSnapshot(context.Background(), snap, &buf, nil); err != nil {
				t.Fatalf("SaveSnapshot: %v", err)
			}
			if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
				t.Fatalf("RecoverFromSnapshot: %v", err)
			}
			v, closer, err := tgt.db(dst).Get(key)
			if err != nil {
				t.Fatalf("staged chunk missing after recovery: %v", err)
			}
			defer func() { _ = closer.Close() }()
			if string(v) != "chunk" {
				t.Fatalf("staged chunk = %q, want chunk", v)
			}
		})
	}
}

func TestGetHash_CoversImportStaging(t *testing.T) {
	for name, tgt := range digestTargets() {
		t.Run(name, func(t *testing.T) {
			s := tgt.open(t)
			before, err := s.GetHash()
			if err != nil {
				t.Fatalf("GetHash: %v", err)
			}
			if err := tgt.db(s).Set(importstage.MetaKey("sess"), []byte("meta"), pebble.Sync); err != nil {
				t.Fatalf("stage: %v", err)
			}
			after, err := s.GetHash()
			if err != nil {
				t.Fatalf("GetHash: %v", err)
			}
			if after == before {
				t.Fatal("GetHash did not change when a session record was staged")
			}
		})
	}
}

func TestDecodeLastApplied(t *testing.T) {
	var v [8]byte
	binary.LittleEndian.PutUint64(v[:], 0x0102030405060708)
	if got := decodeLastApplied(v[:]); got != 0x0102030405060708 {
		t.Fatalf("decodeLastApplied = %#x", got)
	}
}

func TestDecodeLastApplied_WrongLengthIsZero(t *testing.T) {
	for _, v := range [][]byte{nil, {1, 2, 3}, make([]byte, 9)} {
		if got := decodeLastApplied(v); got != 0 {
			t.Fatalf("decodeLastApplied(%x) = %d, want 0", v, got)
		}
	}
}
