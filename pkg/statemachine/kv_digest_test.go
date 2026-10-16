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

package statemachine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// digestLooker is the Lookup half of every adapter this package registers.
type digestLooker interface {
	Lookup(ctx context.Context, query interface{}) (interface{}, error)
}

// digestOp is one client write in the log the digest tests replay.
type digestOp struct {
	del   bool
	key   []byte
	value []byte
}

// digestLog is a fixed log of puts, overwrites and deletes over printable
// and binary keys. Keys never start with 0x00, the reserved prefix.
func digestLog() []digestOp {
	var ops []digestOp
	for i := 0; i < 60; i++ {
		var key []byte
		if i%3 == 0 {
			key = []byte{0x01, byte(i % 11), 0xff}
		} else {
			key = []byte(fmt.Sprintf("k/%03d", i%17))
		}
		if i%5 == 4 {
			ops = append(ops, digestOp{del: true, key: key})
			continue
		}
		ops = append(ops, digestOp{key: key, value: []byte(fmt.Sprintf("v%d", i))})
	}
	return ops
}

// referenceDigest is the digest of the log computed from a plain map, with
// no state machine involved: what every replica must report.
func referenceDigest(t *testing.T, ops []digestOp) statedigest.Digest {
	t.Helper()
	model := map[string][]byte{}
	for _, op := range ops {
		if op.del {
			delete(model, string(op.key))
		} else {
			model[string(op.key)] = op.value
		}
	}
	keys := make([]string, 0, len(model))
	for k := range model {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b := statedigest.NewBuilder()
	for _, k := range keys {
		if err := b.Add([]byte(k), model[k]); err != nil {
			t.Fatalf("reference Add: %v", err)
		}
	}
	return b.Finish(uint64(len(ops)))
}

// digestEntries encodes the log as Raft entries with indexes 1..n.
func digestEntries(t *testing.T, ops []digestOp) []sm.Entry {
	t.Helper()
	entries := make([]sm.Entry, len(ops))
	for i, op := range ops {
		var cmd []byte
		var err error
		if op.del {
			cmd, err = kv.EncodeDelete(op.key)
		} else {
			cmd, err = kv.EncodePut(op.key, op.value)
		}
		if err != nil {
			t.Fatalf("encode entry %d: %v", i, err)
		}
		entries[i] = sm.Entry{Index: uint64(i + 1), Cmd: cmd}
	}
	return entries
}

// replayInBatches applies entries through Update in batches of uneven size,
// so the applied-index stamp is exercised at batch ends that differ from
// entry ends.
func replayInBatches(t *testing.T, u updater, entries []sm.Entry) {
	t.Helper()
	ctx := context.Background()
	sizes := []int{1, 7, 3, 13}
	for i, n := 0, 0; i < len(entries); n++ {
		end := min(i+sizes[n%len(sizes)], len(entries))
		results := make([]sm.Result, end-i)
		if err := u.Update(ctx, entries[i:end], results); err != nil {
			t.Fatalf("Update [%d,%d): %v", i, end, err)
		}
		i = end
	}
}

// lookupDigest asks a state machine for its digest.
func lookupDigest(t *testing.T, l digestLooker) statedigest.Digest {
	t.Helper()
	res, err := l.Lookup(context.Background(), statedigest.Query{})
	if err != nil {
		t.Fatalf("Lookup(statedigest.Query): %v", err)
	}
	d, ok := res.(statedigest.Digest)
	if !ok {
		t.Fatalf("Lookup(statedigest.Query) = %T, want statedigest.Digest", res)
	}
	return d
}

// openDigestSM opens, and registers Close for, a state machine built by a
// registered CreateFunc.
func openDigestSM(t *testing.T, fn sm.CreateFunc) interface{} {
	t.Helper()
	ctx := context.Background()
	s := fn(1, 1)
	if o, ok := s.(opener); ok {
		if _, err := o.Open(ctx, t.TempDir(), nil); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	if c, ok := s.(closer); ok {
		t.Cleanup(func() { _ = c.Close(ctx) })
	}
	return s
}

// digestVariants is every registered KV state machine, and every
// raft.sm_storage backend under the two that compose one.
func digestVariants() []struct {
	name  string
	build func(t *testing.T) sm.CreateFunc
} {
	typed := func(smType string) func(t *testing.T) sm.CreateFunc {
		return func(t *testing.T) sm.CreateFunc {
			fn, err := ResolveTyped(smType, "")
			if err != nil {
				t.Fatal(err)
			}
			return fn
		}
	}
	stored := func(smType, kind string) func(t *testing.T) sm.CreateFunc {
		return func(t *testing.T) sm.CreateFunc {
			fn, err := ResolveTypedWithStorage(smType, "", StorageConfig{Root: t.TempDir(), Kind: kind})
			if err != nil {
				t.Fatal(err)
			}
			return fn
		}
	}
	return []struct {
		name  string
		build func(t *testing.T) sm.CreateFunc
	}{
		{"memory", typed("memory")},
		{"standard", typed("standard")},
		{"concurrent", typed("concurrent")},
		{"highperf", typed("highperf")},
		{"fast-over-memory", stored("fast", "memory")},
		{"fast-over-file", stored("fast", "file")},
		{"fast-over-pebble", stored("fast", "pebble")},
		{"disk-over-memory", stored("disk", "memory")},
		{"disk-over-file", stored("disk", "file")},
		{"disk-over-pebble", stored("disk", "pebble")},
	}
}

// TestStateDigest_BackendEquivalence: the same log replayed into every KV
// state machine, over every raft.sm_storage backend, reports the same
// applied index, record count and SHA-256 -- and that digest is the one a
// plain map of the same writes produces.
func TestStateDigest_BackendEquivalence(t *testing.T) {
	ops := digestLog()
	entries := digestEntries(t, ops)
	want := referenceDigest(t, ops)
	if want.Records == 0 {
		t.Fatal("the log leaves no records; the test would compare empty sets")
	}
	for _, v := range digestVariants() {
		t.Run(v.name, func(t *testing.T) {
			s := openDigestSM(t, v.build(t))
			u, ok := s.(updater)
			if !ok {
				t.Fatalf("%T has no Update", s)
			}
			replayInBatches(t, u, entries)
			got := lookupDigest(t, s.(digestLooker))
			if got != want {
				t.Fatalf("digest = {index %d, records %d, %s}, want {index %d, records %d, %s}",
					got.AppliedIndex, got.Records, got.Hex(), want.AppliedIndex, want.Records, want.Hex())
			}
		})
	}
}

// TestStateDigest_DivergenceInjection: two replicas of the same log agree;
// a write that reaches one replica's storage without going through Raft
// changes that replica's digest, and only that replica's, while both still
// report the same applied index -- the signature of divergence.
func TestStateDigest_DivergenceInjection(t *testing.T) {
	backends := map[string]func() Backend{
		"standard": func() Backend { return NewStandardBackend() },
		"disk":     func() Backend { return NewDiskBackend(memstore.NewStorage()) },
	}
	for name, nb := range backends {
		t.Run(name, func(t *testing.T) {
			entries := digestEntries(t, digestLog())
			a := NewKVStateMachine(nb())
			b := NewKVStateMachine(nb())
			t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
			for _, k := range []*KVStateMachine{a, b} {
				if err := k.Update(entries, make([]sm.Result, len(entries))); err != nil {
					t.Fatalf("Update: %v", err)
				}
			}
			before, err := a.stateDigest()
			if err != nil {
				t.Fatal(err)
			}
			if d, _ := b.stateDigest(); d != before {
				t.Fatal("replicas of the same log disagree before any injection")
			}

			if err := b.backend.Put([]byte("k/003"), []byte("tampered")); err != nil {
				t.Fatalf("inject: %v", err)
			}

			da, _ := a.stateDigest()
			db, _ := b.stateDigest()
			if da != before {
				t.Fatal("the untouched replica's digest changed")
			}
			if db.Sum == before.Sum {
				t.Fatal("the tampered replica's digest did not change")
			}
			if db.AppliedIndex != da.AppliedIndex {
				t.Fatalf("applied index %d vs %d: injection must not move it", db.AppliedIndex, da.AppliedIndex)
			}
		})
	}
}

func TestKVStateMachine_StateDigest_EmptyStateMachine(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			k := NewKVStateMachine(nb())
			t.Cleanup(func() { _ = k.Close() })
			d, err := k.stateDigest()
			if err != nil {
				t.Fatal(err)
			}
			if d != statedigest.NewBuilder().Finish(0) {
				t.Fatalf("empty digest = {%d, %d, %s}, want the header-only digest at index 0", d.AppliedIndex, d.Records, d.Hex())
			}
		})
	}
}

func TestKVStateMachine_StateDigest_ViaLookup(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())
	t.Cleanup(func() { _ = k.Close() })
	entries := digestEntries(t, digestLog()[:4])
	if err := k.Update(entries, make([]sm.Result, len(entries))); err != nil {
		t.Fatal(err)
	}
	res, err := k.Lookup(statedigest.Query{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	d, ok := res.(statedigest.Digest)
	if !ok || d.AppliedIndex != 4 {
		t.Fatalf("Lookup = %#v, want a digest at index 4", res)
	}
}

func TestKVStateMachine_StateDigest_InvalidMarker(t *testing.T) {
	b := NewStandardBackend()
	k := NewKVStateMachine(b)
	t.Cleanup(func() { _ = k.Close() })
	if err := b.Put([]byte(diskMetaKeyLastApplied), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	var ide *IncompleteDataError
	if _, err := k.stateDigest(); !errors.As(err, &ide) {
		t.Fatalf("err = %v, want *IncompleteDataError", err)
	}
	if _, err := k.GetHash(); !errors.As(err, &ide) {
		t.Fatalf("GetHash err = %v, want *IncompleteDataError", err)
	}
}

// recordsErrBackend is a Backend whose Records fails.
type recordsErrBackend struct {
	Backend
	err error
}

var _ Backend = (*recordsErrBackend)(nil)

func (b *recordsErrBackend) Records(RecordVisitor) error { return b.err }

func TestKVStateMachine_StateDigest_RecordsError(t *testing.T) {
	want := &StorageError{Op: "records", Inner: errors.New("disk gone")}
	k := NewKVStateMachine(&recordsErrBackend{Backend: NewStandardBackend(), err: want})
	t.Cleanup(func() { _ = k.Close() })
	if _, err := k.stateDigest(); !errors.Is(err, want) {
		t.Fatalf("stateDigest err = %v, want %v", err, want)
	}
	if _, err := k.GetHash(); !errors.Is(err, want) {
		t.Fatalf("GetHash err = %v, want %v", err, want)
	}
}

// TestKVStateMachine_StateDigest_WaitsForBatch: the digest takes the batch
// lock exclusively, so it never reads between an apply batch's writes and
// that batch's applied-index stamp.
func TestKVStateMachine_StateDigest_WaitsForBatch(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())
	t.Cleanup(func() { _ = k.Close() })
	k.batch.RLock() // an Update in flight
	done := make(chan struct{})
	go func() {
		_, _ = k.stateDigest()
		close(done)
	}()
	select {
	case <-done:
		k.batch.RUnlock()
		t.Fatal("the digest read while an apply batch held the batch lock")
	case <-time.After(50 * time.Millisecond):
	}
	k.batch.RUnlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the digest did not complete after the batch finished")
	}
}

func TestKVStateMachine_Update_StampsLastEntryOfBatch(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			b := nb()
			k := NewKVStateMachine(b)
			t.Cleanup(func() { _ = k.Close() })
			entries := digestEntries(t, digestLog()[:5])
			if err := k.Update(entries, make([]sm.Result, len(entries))); err != nil {
				t.Fatal(err)
			}
			got, err := b.Get([]byte(diskMetaKeyLastApplied))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(encodeAppliedIndex(5)) {
				t.Fatalf("marker = %x, want index 5", got)
			}
		})
	}
}

func TestKVStateMachine_Update_EmptyBatchLeavesNoMarker(t *testing.T) {
	b := NewStandardBackend()
	k := NewKVStateMachine(b)
	t.Cleanup(func() { _ = k.Close() })
	if err := k.Update(nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Get([]byte(diskMetaKeyLastApplied)); got != nil {
		t.Fatalf("an empty batch stamped %x", got)
	}
}

func TestKVStateMachine_GetHash_IgnoresAppliedIndex(t *testing.T) {
	ops := digestLog()[:3]
	a := NewKVStateMachine(NewStandardBackend())
	b := NewKVStateMachine(NewStandardBackend())
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	entries := digestEntries(t, ops)
	if err := a.Update(entries, make([]sm.Result, len(entries))); err != nil {
		t.Fatal(err)
	}
	// The same records reached at a later index.
	shifted := digestEntries(t, ops)
	for i := range shifted {
		shifted[i].Index += 100
	}
	if err := b.Update(shifted, make([]sm.Result, len(shifted))); err != nil {
		t.Fatal(err)
	}
	ha, _ := a.GetHash()
	hb, _ := b.GetHash()
	if ha != hb {
		t.Fatal("GetHash depends on the applied index")
	}
}
