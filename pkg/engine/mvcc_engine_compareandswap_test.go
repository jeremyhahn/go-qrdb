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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// mvccCompareAndSwapEntry builds a guarded-swap entry.
func mvccCompareAndSwapEntry(idx uint64, key, expected, next string) sm.Entry {
	cmd, err := protocol.EncodeCompareAndSwap([]byte(key), []byte(expected), []byte(next))
	if err != nil {
		panic(err) // test values are small; an error here is a test bug
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

// The guarded swap completes the set OpMVCCPutIfAbsent and OpCompareAndDelete
// began, and it is held to the same contract as both: the decision is made
// against replicated state inside the state machine, and a refused guard
// changes nothing at all.
func TestMVCCEngine_CompareAndSwap_Contract(t *testing.T) {
	t.Run("swaps on a match", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "lease", "gen-1")})
		results := applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndSwapEntry(2, "lease", "gen-1", "gen-2")})

		if results[0].Value != protocol.ResultSwapped {
			t.Fatalf("Value = %d, want ResultSwapped", results[0].Value)
		}
		got, err := eng.Store().Get([]byte("lease"))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got.Value) != "gen-2" {
			t.Fatalf("value = %q, want %q", got.Value, "gen-2")
		}
	})

	t.Run("refuses on a mismatch and keeps the current value", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "lease", "gen-2")})
		results := applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndSwapEntry(2, "lease", "gen-1", "gen-9")})

		if results[0].Value != protocol.ResultNotSwapped {
			t.Fatalf("Value = %d, want ResultNotSwapped — a writer holding a stale "+
				"expectation overwrote the current holder", results[0].Value)
		}
		got, err := eng.Store().Get([]byte("lease"))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got.Value) != "gen-2" {
			t.Fatalf("value = %q, want the holder's %q", got.Value, "gen-2")
		}
	})

	t.Run("does NOT create an absent key", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		// The distinction from PutIfAbsent. A swap moves a record between two
		// states, and a record that does not exist has no first state — so a
		// caller cannot accidentally use this to create.
		results := applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndSwapEntry(1, "never", "", "value")})
		if results[0].Value != protocol.ResultNotSwapped {
			t.Fatalf("Value = %d, want ResultNotSwapped", results[0].Value)
		}
		if _, err := eng.Store().Get([]byte("never")); err == nil {
			t.Fatal("a guarded swap CREATED an absent key; that is PutIfAbsent's job")
		}
	})

	t.Run("sees a write packed earlier in the SAME batch", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		// If the guard read past the batch it would decide on a stale value,
		// and replicas that grouped entries differently would diverge — the
		// one outcome a replicated state machine may never produce.
		results := applyMVCCEntries(t, eng, []sm.Entry{
			baseKVPutIfAbsentEntry(1, "lease", "gen-1"),
			mvccCompareAndSwapEntry(2, "lease", "gen-1", "gen-2"),
		})
		if results[1].Value != protocol.ResultSwapped {
			t.Fatalf("Value = %d, want ResultSwapped — the guard did not see the "+
				"write packed ahead of it in the same batch", results[1].Value)
		}
	})

	t.Run("allocates a fresh revision, and a refused swap allocates none", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "lease", "gen-1")})

		before := eng.Store().CurrentRevision()
		applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndSwapEntry(2, "lease", "gen-1", "gen-2")})
		after := eng.Store().CurrentRevision()
		if after <= before {
			t.Fatalf("revision %d -> %d: a guarded swap must allocate a fresh revision "+
				"like any other write", before, after)
		}

		applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndSwapEntry(3, "lease", "stale", "gen-3")})
		if refused := eng.Store().CurrentRevision(); refused != after {
			t.Fatalf("revision %d -> %d: a refused guard must change nothing at all",
				after, refused)
		}
	})
}

// A guarded swap REPLACES the secondary index entries: the old value's come
// out as the new value's go in.
//
// Retracting without adding leaves the record invisible to index queries;
// adding without retracting leaves the key answering to a value it no longer
// holds. Both are silent, and both survive every test that only reads the
// primary key.
func TestMVCCEngine_CompareAndSwap_ReplacesIndexes(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
	if err := eng.Update(context.Background(),
		[]sm.Entry{{Index: 1, Cmd: idxCmd}}, make([]sm.Result, 1)); err != nil {
		t.Fatalf("Update (index create): %v", err)
	}

	const before = `{"name":"alice"}`
	const after = `{"name":"bob"}`
	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(2, "user:1", before)})

	results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccCompareAndSwapEntry(3, "user:1", before, after),
	})
	if results[0].Value != protocol.ResultSwapped {
		t.Fatalf("Value = %d, want ResultSwapped", results[0].Value)
	}

	query := func(value string) int {
		t.Helper()
		got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
			IndexName: "by_name", Value: []byte(value),
		})
		if err != nil {
			t.Fatalf("Lookup(%q): %v", value, err)
		}
		return len(got.([][]byte))
	}

	if n := query("alice"); n != 0 {
		t.Errorf("the OLD index value still returns %d id(s); the swap did not retract it", n)
	}
	if n := query("bob"); n != 1 {
		t.Errorf("the NEW index value returns %d id(s), want 1; the swap did not index it", n)
	}
}
