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
	"slices"
	"sort"
	"testing"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
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
