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
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// ---------------------------------------------------------------------------
// Apply-batch determinism
//
// A Raft state machine must produce identical state from an identical log
// regardless of how the apply loop groups committed entries into Update()
// calls: the grouping is decided per-node by local timing (how many entries
// happened to be ready when the apply goroutine woke), so any dependence on it
// makes replicas diverge from the same log.
//
// The tests below apply the SAME entries under EVERY grouping of interest and
// require byte-identical state and identical per-entry sm.Result values.
// ---------------------------------------------------------------------------

// applyGrouping describes one way of partitioning a log into Update() calls.
type applyGrouping struct {
	name  string
	sizes []int
}

// standardGroupings are the partitions of a four-entry log that a real apply
// loop can produce: one call, two calls split in the middle, one call per
// entry, and an uneven split. Every one of them must yield the same state.
func standardGroupings() []applyGrouping {
	return []applyGrouping{
		{name: "1234", sizes: []int{4}},
		{name: "12|34", sizes: []int{2, 2}},
		{name: "1|2|3|4", sizes: []int{1, 1, 1, 1}},
		{name: "1|23|4", sizes: []int{1, 2, 1}},
	}
}

// groupingsFor returns the four standard groupings scaled to a log of n
// entries: whole, halves, singletons, and an uneven 1/n-2/1 split.
func groupingsFor(n int) []applyGrouping {
	if n < 4 {
		panic("groupingsFor: need at least 4 entries")
	}
	half := n / 2
	singles := make([]int, n)
	for i := range singles {
		singles[i] = 1
	}
	return []applyGrouping{
		{name: "whole", sizes: []int{n}},
		{name: "halves", sizes: []int{half, n - half}},
		{name: "singletons", sizes: singles},
		{name: "uneven", sizes: []int{1, n - 2, 1}},
	}
}

// applyUnderGrouping opens a fresh state machine, applies entries in the given
// grouping, and returns the per-entry results together with a full dump of the
// resulting storage.
func applyUnderGrouping(t *testing.T, entries []sm.Entry, g applyGrouping) ([]sm.Result, string) {
	t.Helper()

	s := newTestMVCCStateMachine(t)
	results := make([]sm.Result, len(entries))

	off := 0
	for _, size := range g.sizes {
		if off+size > len(entries) {
			t.Fatalf("grouping %q overruns the %d-entry log", g.name, len(entries))
		}
		if err := s.Update(context.Background(), entries[off:off+size], results[off:off+size]); err != nil {
			t.Fatalf("grouping %q: Update(entries[%d:%d]): %v", g.name, off, off+size, err)
		}
		off += size
	}
	if off != len(entries) {
		t.Fatalf("grouping %q covers %d of %d entries", g.name, off, len(entries))
	}

	return results, dumpState(t, s)
}

// dumpState renders every key-value pair the state machine holds as a stable,
// diffable string. It is deliberately a full dump rather than a hash so a
// divergence names the key that differs.
func dumpState(t *testing.T, s *MVCCStateMachine) string {
	t.Helper()

	iter, err := s.db.NewIter(nil)
	if err != nil {
		t.Fatalf("dumpState: NewIter: %v", err)
	}
	defer func() { _ = iter.Close() }()

	var b strings.Builder
	for iter.First(); iter.Valid(); iter.Next() {
		v, verr := iter.ValueAndErr()
		if verr != nil {
			t.Fatalf("dumpState: value: %v", verr)
		}
		fmt.Fprintf(&b, "%s = %s\n", hex.EncodeToString(iter.Key()), hex.EncodeToString(v))
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("dumpState: iterate: %v", err)
	}
	return b.String()
}

// formatResults renders per-entry results for comparison across groupings.
func formatResults(results []sm.Result) string {
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "[%d] value=%d data=%s\n", i, r.Value, hex.EncodeToString(r.Data))
	}
	return b.String()
}

// requireGroupingInvariance applies entries under every grouping and requires
// that all of them agree on both the per-entry results and the final state.
func requireGroupingInvariance(t *testing.T, entries []sm.Entry, groupings []applyGrouping) {
	t.Helper()

	type outcome struct {
		results string
		state   string
	}
	outcomes := make(map[string]outcome, len(groupings))
	for _, g := range groupings {
		results, state := applyUnderGrouping(t, entries, g)
		outcomes[g.name] = outcome{results: formatResults(results), state: state}
	}

	base := groupings[0].name
	want := outcomes[base]
	for _, g := range groupings[1:] {
		got := outcomes[g.name]
		if got.results != want.results {
			t.Errorf("apply-batch grouping changed the per-entry results.\n"+
				"grouping %q:\n%s\ngrouping %q:\n%s",
				base, want.results, g.name, got.results)
		}
		if got.state != want.state {
			t.Errorf("apply-batch grouping changed the committed state.\n%s",
				diffDumps(base, want.state, g.name, got.state))
		}
	}
}

// diffDumps renders the keys on which two state dumps disagree.
func diffDumps(nameA, a, nameB, b string) string {
	parse := func(dump string) map[string]string {
		m := make(map[string]string)
		for _, line := range strings.Split(dump, "\n") {
			k, v, ok := strings.Cut(line, " = ")
			if !ok {
				continue
			}
			m[k] = v
		}
		return m
	}
	ma, mb := parse(a), parse(b)

	keys := make([]string, 0, len(ma)+len(mb))
	seen := make(map[string]struct{}, len(ma)+len(mb))
	for k := range ma {
		keys = append(keys, k)
		seen[k] = struct{}{}
	}
	for k := range mb {
		if _, ok := seen[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var out strings.Builder
	for _, k := range keys {
		va, oka := ma[k]
		vb, okb := mb[k]
		if oka && okb && va == vb {
			continue
		}
		fmt.Fprintf(&out, "  key %s: %s=%q %s=%q\n", k, nameA, va, nameB, vb)
	}
	return out.String()
}

// txnEntry builds a transaction entry with a single mod-revision precondition
// on key: on success it writes successValue to key, on failure it writes a
// marker so the outcome is observable in the dumped state either way.
func txnEntry(index uint64, key []byte, wantModRev int64, successValue, failureKey []byte) sm.Entry {
	compare := protocol.EncodeCompare(key,
		byte(service.CompareModRevision), byte(service.CompareEqual),
		nil, 0, 0, wantModRev)
	success := protocol.EncodeOp(protocol.OpMVCCPut, key, successValue, nil, 0)
	failure := protocol.EncodeOp(protocol.OpMVCCPut, failureKey, []byte("lost"), nil, 0)
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeTransaction([][]byte{compare}, [][]byte{success}, [][]byte{failure}),
	}
}

// mvccPutIfAbsentEntry builds an sm.Entry with an MVCC conditional put.
func mvccPutIfAbsentEntry(index uint64, key, value []byte) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   protocol.EncodeMVCCPutIfAbsent(key, value, 0),
	}
}

// baseKVPutEntry builds an sm.Entry with a base-KV Put command.
func baseKVPutEntry(t *testing.T, index uint64, key, value []byte) sm.Entry {
	t.Helper()
	cmd, err := kv.EncodePut(key, value)
	if err != nil {
		t.Fatalf("kv.EncodePut: %v", err)
	}
	return sm.Entry{Index: index, Cmd: cmd}
}

// baseKVPutIfAbsentEntry builds an sm.Entry with a base-KV conditional put.
func baseKVPutIfAbsentEntry(t *testing.T, index uint64, key, value []byte) sm.Entry {
	t.Helper()
	cmd, err := kv.EncodePutIfAbsent(key, value)
	if err != nil {
		t.Fatalf("kv.EncodePutIfAbsent: %v", err)
	}
	return sm.Entry{Index: index, Cmd: cmd}
}

// TestMVCCStateMachine_ApplyBatchGroupingIsDeterministic pins the core
// invariant: the same log produces the same state under every apply-batch
// grouping. The log mixes plain puts, deletes, conditional puts, and
// compare-and-swap transactions whose preconditions reference keys written by
// earlier entries in the SAME log.
func TestMVCCStateMachine_ApplyBatchGroupingIsDeterministic(t *testing.T) {
	key := []byte("account")

	entries := []sm.Entry{
		mvccPutEntry(1, key, []byte("v0"), 0),
		// Both transactions preconditioned on the revision entry 1 produced.
		// Exactly one may win; the second must observe the first and fail over
		// to its failure branch.
		txnEntry(2, key, 1, []byte("w1"), []byte("failed-2")),
		txnEntry(3, key, 1, []byte("w2"), []byte("failed-3")),
		mvccPutIfAbsentEntry(4, []byte("once"), []byte("first")),
		mvccPutIfAbsentEntry(5, []byte("once"), []byte("second")),
		mvccDeleteEntry(6, key),
		mvccPutIfAbsentEntry(7, key, []byte("reborn")),
	}

	requireGroupingInvariance(t, entries, groupingsFor(len(entries)))
}

// TestMVCCStateMachine_ConcurrentCASHasExactlyOneWinner reproduces the
// concurrent compare-and-swap case directly: N transactions committed in one
// Raft log, all preconditioned on the SAME revision of the same key. Exactly
// one may report success no matter how the apply loop groups them.
func TestMVCCStateMachine_ConcurrentCASHasExactlyOneWinner(t *testing.T) {
	const contenders = 16
	key := []byte("cas")

	entries := []sm.Entry{mvccPutEntry(1, key, []byte("v0"), 0)}
	for i := 0; i < contenders; i++ {
		entries = append(entries, txnEntry(uint64(2+i), key, 1,
			[]byte(fmt.Sprintf("winner-%d", i)),
			[]byte(fmt.Sprintf("failed-%d", i))))
	}

	for _, g := range groupingsFor(len(entries)) {
		t.Run(g.name, func(t *testing.T) {
			results, _ := applyUnderGrouping(t, entries, g)

			winners := 0
			for i := 1; i < len(results); i++ {
				if len(results[i].Data) == 1 && results[i].Data[0] == 1 {
					winners++
				}
			}
			if winners != 1 {
				t.Fatalf("expected exactly 1 of %d compare-and-swap transactions to succeed, got %d",
					contenders, winners)
			}
		})
	}
}

// TestMVCCStateMachine_PutIfAbsentHasExactlyOneStore requires that of N
// conditional puts to the same key in one log, exactly one stores — under
// every apply-batch grouping.
func TestMVCCStateMachine_PutIfAbsentHasExactlyOneStore(t *testing.T) {
	const contenders = 8
	key := []byte("create-once")

	var entries []sm.Entry
	for i := 0; i < contenders; i++ {
		entries = append(entries, mvccPutIfAbsentEntry(uint64(1+i), key,
			[]byte(fmt.Sprintf("v%d", i))))
	}

	for _, g := range groupingsFor(len(entries)) {
		t.Run(g.name, func(t *testing.T) {
			results, _ := applyUnderGrouping(t, entries, g)

			stored := 0
			for _, r := range results {
				if r.Value == kv.ResultStored {
					stored++
				}
			}
			if stored != 1 {
				t.Fatalf("expected exactly 1 of %d conditional puts to store, got %d",
					contenders, stored)
			}
		})
	}
}

// TestMVCCStateMachine_BaseKVPutIfAbsentHasExactlyOneStore is the base-KV
// opcode's half of the conditional-put invariant. It is the primitive
// service.TableService.CreateTable rides on, so a second winner here is a
// duplicate CREATE TABLE.
func TestMVCCStateMachine_BaseKVPutIfAbsentHasExactlyOneStore(t *testing.T) {
	const contenders = 8
	key := []byte("table/meta/users")

	var entries []sm.Entry
	for i := 0; i < contenders; i++ {
		entries = append(entries, baseKVPutIfAbsentEntry(t, uint64(1+i), key,
			[]byte(fmt.Sprintf("schema-%d", i))))
	}

	for _, g := range groupingsFor(len(entries)) {
		t.Run(g.name, func(t *testing.T) {
			results, _ := applyUnderGrouping(t, entries, g)

			stored := 0
			for _, r := range results {
				if r.Value == kv.ResultStored {
					stored++
				}
			}
			if stored != 1 {
				t.Fatalf("expected exactly 1 of %d base-KV conditional puts to store, got %d",
					contenders, stored)
			}
		})
	}
}

// TestMVCCStateMachine_UniqueIndexAcrossApplyBatchEntries requires that a
// unique secondary index rejects a duplicate written by a LATER entry in the
// same apply batch, exactly as it would if the entries were applied one call
// at a time.
func TestMVCCStateMachine_UniqueIndexAcrossApplyBatchEntries(t *testing.T) {
	entries := []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeIndexCreate("uniq_email", "email", true, false, false)},
		mvccPutEntry(2, []byte("user/a"), []byte(`{"email":"dup@example.com"}`), 0),
		mvccPutEntry(3, []byte("user/b"), []byte(`{"email":"dup@example.com"}`), 0),
		mvccPutEntry(4, []byte("user/c"), []byte(`{"email":"other@example.com"}`), 0),
	}

	for _, g := range standardGroupings() {
		t.Run(g.name, func(t *testing.T) {
			results, _ := applyUnderGrouping(t, entries, g)

			if _, isErr := protocol.DecodeAppError(results[2].Data); !isErr {
				t.Fatalf("entry 3 wrote a duplicate unique value: result=%+v", results[2])
			}
			if _, isErr := protocol.DecodeAppError(results[3].Data); isErr {
				t.Fatalf("entry 4 was rejected but holds a distinct unique value: result=%+v", results[3])
			}
		})
	}
}

// TestMVCCStateMachine_IndexBackfillSeesSameBatchWrites requires that an index
// created by an entry backfills from the writes of earlier entries in the same
// apply batch. Otherwise the index a node builds depends on how its apply loop
// happened to group the log.
func TestMVCCStateMachine_IndexBackfillSeesSameBatchWrites(t *testing.T) {
	entries := []sm.Entry{
		mvccPutEntry(1, []byte("user/a"), []byte(`{"email":"a@example.com"}`), 0),
		mvccPutEntry(2, []byte("user/b"), []byte(`{"email":"b@example.com"}`), 0),
		{Index: 3, Cmd: protocol.EncodeIndexCreate("email_idx", "email", false, false, false)},
		mvccPutEntry(4, []byte("user/c"), []byte(`{"email":"c@example.com"}`), 0),
	}

	for _, g := range standardGroupings() {
		t.Run(g.name, func(t *testing.T) {
			s := newTestMVCCStateMachine(t)
			results := make([]sm.Result, len(entries))
			off := 0
			for _, size := range g.sizes {
				if err := s.Update(context.Background(), entries[off:off+size], results[off:off+size]); err != nil {
					t.Fatalf("Update: %v", err)
				}
				off += size
			}

			for _, want := range []string{"a@example.com", "b@example.com", "c@example.com"} {
				got, err := s.Lookup(context.Background(), service.IndexQueryRequest{
					IndexName: "email_idx",
					Value:     []byte(want),
				})
				if err != nil {
					t.Fatalf("index lookup %q: %v", want, err)
				}
				keys, ok := got.([][]byte)
				if !ok || len(keys) != 1 {
					t.Fatalf("index %q: expected exactly one entity key, got %#v", want, got)
				}
			}
		})
	}
}

// TestMVCCStateMachine_CompactWithinApplyBatchIsDeterministic requires that a
// compaction entry sees the writes of earlier entries in the same apply batch,
// so the compacted state does not depend on the grouping.
func TestMVCCStateMachine_CompactWithinApplyBatchIsDeterministic(t *testing.T) {
	key := []byte("hot")

	entries := []sm.Entry{
		mvccPutEntry(1, key, []byte("v1"), 0),
		mvccPutEntry(2, key, []byte("v2"), 0),
		mvccPutEntry(3, key, []byte("v3"), 0),
		mvccCompactEntry(4, 2),
		mvccPutEntry(5, key, []byte("v4"), 0),
	}

	requireGroupingInvariance(t, entries, groupingsFor(len(entries)))
}

// TestMVCCStateMachine_TransactionValueCompareSeesSameBatchWrites requires
// that a value precondition observes a write made by an earlier entry in the
// same apply batch.
func TestMVCCStateMachine_TransactionValueCompareSeesSameBatchWrites(t *testing.T) {
	key := []byte("flag")

	valueCompare := func(index uint64, want []byte) sm.Entry {
		compare := protocol.EncodeCompare(key,
			byte(service.CompareValue), byte(service.CompareEqual),
			want, 0, 0, 0)
		success := protocol.EncodeOp(protocol.OpMVCCPut, []byte("saw-"+string(want)), []byte("1"), nil, 0)
		return sm.Entry{
			Index: index,
			Cmd:   protocol.EncodeTransaction([][]byte{compare}, [][]byte{success}, nil),
		}
	}

	entries := []sm.Entry{
		mvccPutEntry(1, key, []byte("old"), 0),
		valueCompare(2, []byte("old")),
		mvccPutEntry(3, key, []byte("new"), 0),
		valueCompare(4, []byte("new")),
	}

	requireGroupingInvariance(t, entries, standardGroupings())
}

// ---------------------------------------------------------------------------
// PebbleStateMachine
// ---------------------------------------------------------------------------

// newTestPebbleSM opens a PebbleStateMachine on a temporary directory.
func newTestPebbleSM(t *testing.T) *PebbleStateMachine {
	t.Helper()
	s := NewPebbleStateMachine(&pebble.Options{})
	ctx := context.Background()
	if _, err := s.Open(ctx, t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("open pebble sm: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	return s
}

// applyPebbleUnderGrouping applies entries to a fresh PebbleStateMachine in
// the given grouping and returns the results and a full state dump.
func applyPebbleUnderGrouping(t *testing.T, entries []sm.Entry, g applyGrouping) ([]sm.Result, string) {
	t.Helper()

	s := newTestPebbleSM(t)
	results := make([]sm.Result, len(entries))

	off := 0
	for _, size := range g.sizes {
		if err := s.Update(context.Background(), entries[off:off+size], results[off:off+size]); err != nil {
			t.Fatalf("grouping %q: Update: %v", g.name, err)
		}
		off += size
	}

	iter, err := s.db.NewIter(nil)
	if err != nil {
		t.Fatalf("NewIter: %v", err)
	}
	defer func() { _ = iter.Close() }()

	var b strings.Builder
	for iter.First(); iter.Valid(); iter.Next() {
		v, verr := iter.ValueAndErr()
		if verr != nil {
			t.Fatalf("value: %v", verr)
		}
		fmt.Fprintf(&b, "%s = %s\n", hex.EncodeToString(iter.Key()), hex.EncodeToString(v))
	}
	return results, b.String()
}

// TestPebbleStateMachine_ApplyBatchGroupingIsDeterministic pins the same
// invariant for the non-MVCC state machine, whose conditional puts and value
// preconditions read through the very same apply batch.
func TestPebbleStateMachine_ApplyBatchGroupingIsDeterministic(t *testing.T) {
	key := []byte("flag")

	valueCompare := func(index uint64, want, then []byte) sm.Entry {
		compare := protocol.EncodeCompare(key,
			byte(service.CompareValue), byte(service.CompareEqual),
			want, 0, 0, 0)
		success := protocol.EncodeOp(protocol.OpMVCCPut, then, []byte("1"), nil, 0)
		return sm.Entry{
			Index: index,
			Cmd:   protocol.EncodeTransaction([][]byte{compare}, [][]byte{success}, nil),
		}
	}

	entries := []sm.Entry{
		baseKVPutEntry(t, 1, key, []byte("old")),
		valueCompare(2, []byte("old"), []byte("saw-old")),
		baseKVPutEntry(t, 3, key, []byte("new")),
		valueCompare(4, []byte("new"), []byte("saw-new")),
		baseKVPutIfAbsentEntry(t, 5, []byte("once"), []byte("first")),
		baseKVPutIfAbsentEntry(t, 6, []byte("once"), []byte("second")),
	}

	groupings := groupingsFor(len(entries))
	baseResults, baseState := applyPebbleUnderGrouping(t, entries, groupings[0])
	for _, g := range groupings[1:] {
		results, state := applyPebbleUnderGrouping(t, entries, g)
		if formatResults(results) != formatResults(baseResults) {
			t.Errorf("pebble apply-batch grouping changed the per-entry results.\n%q:\n%s\n%q:\n%s",
				groupings[0].name, formatResults(baseResults), g.name, formatResults(results))
		}
		if state != baseState {
			t.Errorf("pebble apply-batch grouping changed the committed state.\n%s",
				diffDumps(groupings[0].name, baseState, g.name, state))
		}
	}
}

// TestPebbleStateMachine_PutIfAbsentHasExactlyOneStore requires exactly one
// winner among N conditional puts to the same key in one apply batch.
func TestPebbleStateMachine_PutIfAbsentHasExactlyOneStore(t *testing.T) {
	const contenders = 8
	key := []byte("create-once")

	var entries []sm.Entry
	for i := 0; i < contenders; i++ {
		entries = append(entries, baseKVPutIfAbsentEntry(t, uint64(1+i), key,
			[]byte(fmt.Sprintf("v%d", i))))
	}

	for _, g := range groupingsFor(len(entries)) {
		t.Run(g.name, func(t *testing.T) {
			results, _ := applyPebbleUnderGrouping(t, entries, g)
			stored := 0
			for _, r := range results {
				if r.Value == kv.ResultStored {
					stored++
				}
			}
			if stored != 1 {
				t.Fatalf("expected exactly 1 of %d conditional puts to store, got %d", contenders, stored)
			}
		})
	}
}

// TestPebbleStateMachine_MVCCPutIfAbsentHasExactlyOneStore covers the MVCC
// opcode on the non-MVCC state machine.
func TestPebbleStateMachine_MVCCPutIfAbsentHasExactlyOneStore(t *testing.T) {
	const contenders = 8
	key := []byte("create-once-mvcc")

	var entries []sm.Entry
	for i := 0; i < contenders; i++ {
		entries = append(entries, mvccPutIfAbsentEntry(uint64(1+i), key, []byte(fmt.Sprintf("v%d", i))))
	}

	for _, g := range groupingsFor(len(entries)) {
		t.Run(g.name, func(t *testing.T) {
			results, _ := applyPebbleUnderGrouping(t, entries, g)
			stored := 0
			for _, r := range results {
				if r.Value == kv.ResultStored {
					stored++
				}
			}
			if stored != 1 {
				t.Fatalf("expected exactly 1 of %d conditional puts to store, got %d", contenders, stored)
			}
		})
	}
}
