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
	"fmt"
	"slices"
	"sort"
	"testing"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

func TestIndexCreateValueType(t *testing.T) {
	if got := IndexCreateValueType(protocol.IndexCreateOptions{Numeric: true}); got != index.IndexValueTypeNumeric {
		t.Errorf("numeric flag: got %v, want IndexValueTypeNumeric", got)
	}
	if got := IndexCreateValueType(protocol.IndexCreateOptions{Unique: true}); got != index.IndexValueTypeString {
		t.Errorf("no numeric flag: got %v, want IndexValueTypeString", got)
	}
}

// valueTypeEngine is the slice of the engine surface the rebuild tests drive,
// so one sequence of assertions runs against every engine that applies
// OpIndexCreate.
type valueTypeEngine interface {
	Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
	Lookup(ctx context.Context, query interface{}) (interface{}, error)
}

type valueTypeEngineCase struct {
	eng valueTypeEngine
	put func(idx uint64, key, value string) sm.Entry
}

func valueTypeEngines(t *testing.T) map[string]valueTypeEngineCase {
	t.Helper()
	kvStg := memory.NewStorage()
	t.Cleanup(func() { _ = kvStg.Close() })

	mvccStg := memory.NewStorage()
	t.Cleanup(func() { _ = mvccStg.Close() })
	mvccEng, err := NewMVCCEngine(mvccStg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = mvccEng.Close(context.Background()) })

	return map[string]valueTypeEngineCase{
		"KVEngine": {eng: NewKVEngine(kvStg), put: kvPutEntry},
		"MVCCEngine": {eng: mvccEng, put: func(idx uint64, key, value string) sm.Entry {
			return mvccPutEntry(idx, []byte(key), []byte(value), 0)
		}},
	}
}

func applyIndexCreate(t *testing.T, eng valueTypeEngine, entry uint64, opts protocol.IndexCreateOptions) sm.Result {
	t.Helper()
	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_n", "n", opts))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: entry, Cmd: cmd}}, results); err != nil {
		t.Fatalf("index create: %v", err)
	}
	return results[0]
}

// scanFromTwo answers `n >= 2` against the index and returns the matched
// terms, sorted.
func scanFromTwo(t *testing.T, eng valueTypeEngine) []string {
	t.Helper()
	got, err := eng.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "by_n",
		Start:     []byte("2"),
	})
	if err != nil {
		t.Fatalf("index scan: %v", err)
	}
	terms := make([]string, 0)
	for term := range got.(map[string][][]byte) {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

// TestHandleIndexCreate_ValueTypeChangeRebuilds pins the one divergence a
// same-owner re-create does not leave to the original definition. A field
// indexed as a string and later declared numeric must end up with numeric
// terms: left at string terms, "10" sorts below "2" and `n >= 2` silently
// drops the record holding 10.
func TestHandleIndexCreate_ValueTypeChangeRebuilds(t *testing.T) {
	stringOrder := []string{"2"}
	numericOrder := []string{"10", "2"}

	for name, c := range valueTypeEngines(t) {
		t.Run(name, func(t *testing.T) {
			applyIndexCreate(t, c.eng, 1, protocol.IndexCreateOptions{})
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(2, "doc-2", `{"n":2}`),
				c.put(3, "doc-10", `{"n":10}`),
			}, make([]sm.Result, 2)); err != nil {
				t.Fatalf("put: %v", err)
			}

			// The control: string terms compare by bytes.
			if got := scanFromTwo(t, c.eng); !slices.Equal(got, stringOrder) {
				t.Fatalf("string index: n >= 2 matched %v, want %v", got, stringOrder)
			}

			applyIndexCreate(t, c.eng, 4, protocol.IndexCreateOptions{Numeric: true})
			if got := scanFromTwo(t, c.eng); !slices.Equal(got, numericOrder) {
				t.Fatalf("after a numeric re-create: n >= 2 matched %v, want %v -- "+
					"the index kept its string terms", got, numericOrder)
			}

			// The same definition again is the idempotent no-op it always was.
			applyIndexCreate(t, c.eng, 5, protocol.IndexCreateOptions{Numeric: true})
			if got := scanFromTwo(t, c.eng); !slices.Equal(got, numericOrder) {
				t.Fatalf("after a repeated numeric create: n >= 2 matched %v, want %v", got, numericOrder)
			}

			// The rule runs both ways.
			applyIndexCreate(t, c.eng, 6, protocol.IndexCreateOptions{})
			if got := scanFromTwo(t, c.eng); !slices.Equal(got, stringOrder) {
				t.Fatalf("after a string re-create: n >= 2 matched %v, want %v", got, stringOrder)
			}
		})
	}
}

// stagedRebuildEntries applies, over a string index by_n, one Update holding
// a put of doc-x followed by a numeric re-create of by_n, and returns how
// many stored by_n entries name doc-x.
func stagedRebuildEntries(t *testing.T, rescope bool) int {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })

	applyIndexCreate(t, eng, 1, protocol.IndexCreateOptions{})
	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_n", "n", protocol.IndexCreateOptions{Numeric: true, Rescope: rescope}))
	if err := eng.Update(context.Background(), []sm.Entry{
		mvccPutEntry(2, []byte("doc-x"), []byte(`{"n":7}`), 0),
		{Index: 3, Cmd: cmd},
	}, make([]sm.Result, 2)); err != nil {
		t.Fatalf("put + re-create: %v", err)
	}

	prefix := []byte("idx:by_n:")
	it := stg.NewIterator(&storage.IteratorOptions{LowerBound: prefix, UpperBound: []byte("idx:by_n;")})
	defer func() { _ = it.Close() }()
	n := 0
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		if bytes.Contains(it.Key(), []byte("doc-x")) || bytes.Contains(it.Value(), []byte("doc-x")) {
			n++
		}
	}
	return n
}

func TestHandleIndexCreate_UnflaggedRebuildAfterStagedWriteKeepsLegacyResult(t *testing.T) {
	// A create without Rescope clears committed storage only, so the string
	// term the put staged in the same batch commits beside the numeric one.
	if got := stagedRebuildEntries(t, false); got != 2 {
		t.Fatalf("by_n entries for doc-x = %d, want 2 (staged string term + numeric term)", got)
	}
}

func TestHandleIndexCreate_RescopeRebuildAfterStagedWriteClearsStaged(t *testing.T) {
	if got := stagedRebuildEntries(t, true); got != 1 {
		t.Fatalf("by_n entries for doc-x = %d, want 1 (numeric term only)", got)
	}
}

// TestHandleIndexCreate_ValueTypeChangeKeepsOwnership checks the rebuild is
// reached only after the ownership check: a create by another owner is
// refused whatever value type it asks for, and the index is left as it was.
func TestHandleIndexCreate_ValueTypeChangeKeepsOwnership(t *testing.T) {
	for name, c := range valueTypeEngines(t) {
		t.Run(name, func(t *testing.T) {
			applyIndexCreate(t, c.eng, 1, protocol.IndexCreateOptions{Owner: "collection:a"})
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(2, "doc-2", `{"n":2}`),
				c.put(3, "doc-10", `{"n":10}`),
			}, make([]sm.Result, 2)); err != nil {
				t.Fatalf("put: %v", err)
			}

			res := applyIndexCreate(t, c.eng, 4, protocol.IndexCreateOptions{Owner: "collection:b", Numeric: true})
			if _, _, ok := protocol.DecodeIndexOwnershipError(res.Data); !ok {
				t.Fatalf("a create by another owner was not refused: result %+v", res)
			}
			if got := scanFromTwo(t, c.eng); !slices.Equal(got, []string{"2"}) {
				t.Fatalf("a refused create changed the index: n >= 2 matched %v, want [2]", got)
			}
		})
	}
}

// applyEmailIndexCreate applies a unique create of the by_email index over
// the email field with the given options.
func applyEmailIndexCreate(t *testing.T, eng valueTypeEngine, entry uint64, opts protocol.IndexCreateOptions) sm.Result {
	t.Helper()
	opts.Unique = true
	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", opts))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: entry, Cmd: cmd}}, results); err != nil {
		t.Fatalf("index create: %v", err)
	}
	return results[0]
}

// emailLookup returns the keys the by_email index holds for value.
func emailLookup(t *testing.T, eng valueTypeEngine, value string) []string {
	t.Helper()
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{IndexName: "by_email", Value: []byte(value)})
	if err != nil {
		t.Fatalf("index lookup %q: %v", value, err)
	}
	keys := make([]string, 0)
	if got != nil {
		for _, k := range got.([][]byte) {
			keys = append(keys, string(k))
		}
	}
	sort.Strings(keys)
	return keys
}

// TestHandleIndexCreate_KeyPrefixChangeRebuilds pins that a same-owner
// re-create declaring a different key scope rebuilds the index over the new
// scope instead of keeping the old one: an index an earlier client created
// database-wide stops holding other entities' keys once it is re-created
// scoped to member/ (Codex round 2 P1 2 on #1706).
func TestHandleIndexCreate_KeyPrefixChangeRebuilds(t *testing.T) {
	for name, c := range valueTypeEngines(t) {
		t.Run(name, func(t *testing.T) {
			applyEmailIndexCreate(t, c.eng, 1, protocol.IndexCreateOptions{})
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(2, "member/1", `{"email":"a@x"}`),
				c.put(3, "txmember/1", `{"email":"b@x"}`),
			}, make([]sm.Result, 2)); err != nil {
				t.Fatalf("put: %v", err)
			}
			if got := emailLookup(t, c.eng, "b@x"); !slices.Equal(got, []string{"txmember/1"}) {
				t.Fatalf("unscoped index: b@x matched %v, want [txmember/1]", got)
			}

			assertScoped := func(step string) {
				t.Helper()
				if got := emailLookup(t, c.eng, "b@x"); len(got) != 0 {
					t.Fatalf("%s: b@x matched %v, want none (txmember/ is outside the scope)", step, got)
				}
				if got := emailLookup(t, c.eng, "a@x"); !slices.Equal(got, []string{"member/1"}) {
					t.Fatalf("%s: a@x matched %v, want [member/1]", step, got)
				}
			}

			applyEmailIndexCreate(t, c.eng, 4, protocol.IndexCreateOptions{KeyPrefix: "member/", Rescope: true})
			assertScoped("scoped re-create")

			results := make([]sm.Result, 1)
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(5, "member/2", `{"email":"b@x"}`),
			}, results); err != nil {
				t.Fatalf("put member/2: %v", err)
			}
			if msg, ok := protocol.DecodeAppError(results[0].Data); ok {
				t.Fatalf("put member/2 refused: %s (txmember/1 must not hold b@x)", msg)
			}
			if got := emailLookup(t, c.eng, "b@x"); !slices.Equal(got, []string{"member/2"}) {
				t.Fatalf("after put: b@x matched %v, want [member/2]", got)
			}

			// The same scoped create again is a no-op.
			applyEmailIndexCreate(t, c.eng, 6, protocol.IndexCreateOptions{KeyPrefix: "member/", Rescope: true})
			if got := emailLookup(t, c.eng, "b@x"); !slices.Equal(got, []string{"member/2"}) {
				t.Fatalf("repeated create: b@x matched %v, want [member/2]", got)
			}
			if got := emailLookup(t, c.eng, "a@x"); !slices.Equal(got, []string{"member/1"}) {
				t.Fatalf("repeated create: a@x matched %v, want [member/1]", got)
			}

			// A txmember write under the scoped index is not indexed.
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(7, "txmember/2", `{"email":"c@x"}`),
			}, make([]sm.Result, 1)); err != nil {
				t.Fatalf("put txmember/2: %v", err)
			}
			if got := emailLookup(t, c.eng, "c@x"); len(got) != 0 {
				t.Fatalf("scoped index: c@x matched %v, want none", got)
			}

			// Back to database-wide: txmember keys are indexed again once
			// the wider scope holds no duplicate value.
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(8, "txmember/1", `{"email":"d@x"}`),
			}, make([]sm.Result, 1)); err != nil {
				t.Fatalf("put txmember/1: %v", err)
			}
			applyEmailIndexCreate(t, c.eng, 9, protocol.IndexCreateOptions{Rescope: true})
			if got := emailLookup(t, c.eng, "d@x"); !slices.Equal(got, []string{"txmember/1"}) {
				t.Fatalf("unscoped re-create: d@x matched %v, want [txmember/1]", got)
			}
			if got := emailLookup(t, c.eng, "c@x"); !slices.Equal(got, []string{"txmember/2"}) {
				t.Fatalf("unscoped re-create: c@x matched %v, want [txmember/2]", got)
			}
			if got := emailLookup(t, c.eng, "a@x"); !slices.Equal(got, []string{"member/1"}) {
				t.Fatalf("unscoped re-create: a@x matched %v, want [member/1]", got)
			}
		})
	}
}

func TestHandleIndexCreate_RescopeOverDuplicatesIsRefused(t *testing.T) {
	for name, c := range valueTypeEngines(t) {
		t.Run(name, func(t *testing.T) {
			applyEmailIndexCreate(t, c.eng, 1, protocol.IndexCreateOptions{KeyPrefix: "member/"})
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(2, "member/1", `{"email":"a@x"}`),
				c.put(3, "txmember/1", `{"email":"a@x"}`),
			}, make([]sm.Result, 2)); err != nil {
				t.Fatalf("put: %v", err)
			}

			res := applyEmailIndexCreate(t, c.eng, 4, protocol.IndexCreateOptions{Rescope: true})
			if _, kind, ok := protocol.DecodeAppErrorKind(res.Data); !ok || kind != protocol.AppErrorConflict {
				t.Fatalf("rescope over duplicates: result %q, want an AppErrorConflict", res.Data)
			}
			if got := emailLookup(t, c.eng, "a@x"); !slices.Equal(got, []string{"member/1"}) {
				t.Fatalf("after refused rescope: a@x matched %v, want [member/1]", got)
			}

			results := make([]sm.Result, 1)
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(5, "member/2", `{"email":"a@x"}`),
			}, results); err != nil {
				t.Fatalf("put member/2: %v", err)
			}
			if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
				t.Fatalf("put member/2 accepted; want a unique conflict against member/1")
			}

			// A wider scope without a duplicate value rescopes and indexes both keys.
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(6, "txmember/1", `{"email":"b@x"}`),
			}, make([]sm.Result, 1)); err != nil {
				t.Fatalf("put txmember/1: %v", err)
			}
			res = applyEmailIndexCreate(t, c.eng, 7, protocol.IndexCreateOptions{Rescope: true})
			if msg, ok := protocol.DecodeAppError(res.Data); ok {
				t.Fatalf("rescope without duplicates refused: %s", msg)
			}
			if got := emailLookup(t, c.eng, "a@x"); !slices.Equal(got, []string{"member/1"}) {
				t.Fatalf("after rescope: a@x matched %v, want [member/1]", got)
			}
			if got := emailLookup(t, c.eng, "b@x"); !slices.Equal(got, []string{"txmember/1"}) {
				t.Fatalf("after rescope: b@x matched %v, want [txmember/1]", got)
			}
		})
	}
}

// A scoped re-create without the rescope flag keeps the existing index and
// its original scope, as the merge base does: an older proposer or a
// historical log replays the same on every replica.
func TestHandleIndexCreate_KeyPrefixChangeWithoutRescopeKeepsTheIndex(t *testing.T) {
	for name, c := range valueTypeEngines(t) {
		t.Run(name, func(t *testing.T) {
			applyEmailIndexCreate(t, c.eng, 1, protocol.IndexCreateOptions{})
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(2, "txmember/1", `{"email":"b@x"}`),
			}, make([]sm.Result, 1)); err != nil {
				t.Fatalf("put: %v", err)
			}

			applyEmailIndexCreate(t, c.eng, 3, protocol.IndexCreateOptions{KeyPrefix: "member/"})
			if got := emailLookup(t, c.eng, "b@x"); !slices.Equal(got, []string{"txmember/1"}) {
				t.Fatalf("b@x matched %v, want [txmember/1] (the unscoped index is kept)", got)
			}
		})
	}
}

// A scoped re-create that follows a write in the same apply batch must leave
// the index as two separate applies would: the entry the write staged in the
// batch is outside the new scope, so the rebuild must drop it too.
func TestHandleIndexCreate_KeyPrefixChangeInOneBatchMatchesSeparateApplies(t *testing.T) {
	scoped := protocol.IndexCreateOptions{KeyPrefix: "member/", Rescope: true, Unique: true}
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", scoped))

	for _, grouped := range []bool{true, false} {
		t.Run(fmt.Sprintf("grouped=%v", grouped), func(t *testing.T) {
			c := valueTypeEngines(t)["MVCCEngine"]
			applyEmailIndexCreate(t, c.eng, 1, protocol.IndexCreateOptions{})

			put := c.put(2, "txmember/1", `{"email":"b@x"}`)
			create := sm.Entry{Index: 3, Cmd: createCmd}
			batches := [][]sm.Entry{{put}, {create}}
			if grouped {
				batches = [][]sm.Entry{{put, create}}
			}
			for _, entries := range batches {
				if err := c.eng.Update(context.Background(), entries, make([]sm.Result, len(entries))); err != nil {
					t.Fatalf("update: %v", err)
				}
			}

			if got := emailLookup(t, c.eng, "b@x"); len(got) != 0 {
				t.Fatalf("b@x matched %v, want none (txmember/ is outside the scope)", got)
			}
			results := make([]sm.Result, 1)
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(4, "member/2", `{"email":"b@x"}`),
			}, results); err != nil {
				t.Fatalf("put member/2: %v", err)
			}
			if msg, ok := protocol.DecodeAppError(results[0].Data); ok {
				t.Fatalf("put member/2 refused: %s (txmember/1 must not hold b@x)", msg)
			}
			if got := emailLookup(t, c.eng, "b@x"); !slices.Equal(got, []string{"member/2"}) {
				t.Fatalf("after put: b@x matched %v, want [member/2]", got)
			}
		})
	}
}

// TestHandleIndexCreate_KeyPrefixChangeKeepsOwnership pins that a create by
// a different owner declaring a new key scope is refused and leaves the
// index's scope unchanged.
func TestHandleIndexCreate_KeyPrefixChangeKeepsOwnership(t *testing.T) {
	for name, c := range valueTypeEngines(t) {
		t.Run(name, func(t *testing.T) {
			applyEmailIndexCreate(t, c.eng, 1, protocol.IndexCreateOptions{Owner: "collection:a"})
			if err := c.eng.Update(context.Background(), []sm.Entry{
				c.put(2, "member/1", `{"email":"a@x"}`),
				c.put(3, "txmember/1", `{"email":"b@x"}`),
			}, make([]sm.Result, 2)); err != nil {
				t.Fatalf("put: %v", err)
			}

			res := applyEmailIndexCreate(t, c.eng, 4, protocol.IndexCreateOptions{Owner: "collection:b", KeyPrefix: "member/", Rescope: true})
			if _, _, ok := protocol.DecodeIndexOwnershipError(res.Data); !ok {
				t.Fatalf("a create by another owner was not refused: result %+v", res)
			}
			if got := emailLookup(t, c.eng, "b@x"); !slices.Equal(got, []string{"txmember/1"}) {
				t.Fatalf("a refused create changed the index: b@x matched %v, want [txmember/1]", got)
			}
		})
	}
}
