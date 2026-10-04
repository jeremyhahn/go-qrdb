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

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mvccLookupIndex runs an exact-match index query and returns the number of
// entity keys the engine answered with.
func mvccLookupIndex(t *testing.T, eng *MVCCEngine, indexName, probe string) int {
	t.Helper()
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: indexName,
		Value:     []byte(probe),
	})
	if err != nil {
		t.Fatalf("Lookup(%q, %q): %v", indexName, probe, err)
	}
	keys, ok := got.([][]byte)
	if !ok {
		t.Fatalf("Lookup returned %T; want [][]byte", got)
	}
	return len(keys)
}

// TestMVCCEngine_IndexCreate_CaseInsensitive covers the second engine, over
// BOTH index implementations it can pick between.
//
// MVCCEngine.handleIndexCreate used to decode the command with the positional
// DecodeIndexCreate, which cannot report case-insensitivity, and built its
// index with constructors that could not express it. So an
// `index:"case_insensitive"` definition became a case-SENSITIVE index on an
// MVCC-backed node and a lookup for "Alpha" over a stored "alpha" answered
// empty with a nil error -- the same defect go-qrdb#34 removes from KVEngine.
//
// Both branches are exercised because they are two separate implementations:
// index.MemoryIndex when no PebbleDB is configured, index.PebbleIndex when
// one is. Covering one would leave the other silently wrong.
func TestMVCCEngine_IndexCreate_CaseInsensitive(t *testing.T) {
	for _, backend := range []string{"memory-index", "pebble-index"} {
		t.Run(backend, func(t *testing.T) {
			var eng *MVCCEngine
			if backend == "memory-index" {
				e, stg := newMVCCEngine(t)
				t.Cleanup(func() { _ = stg.Close() })
				t.Cleanup(func() { _ = e.Close(context.Background()) })
				eng = e
			} else {
				e, cleanup := newMVCCEngineWithPebble(t)
				t.Cleanup(cleanup)
				eng = e
			}

			// Written BEFORE the index exists: reaches it via backfill.
			applyMVCCEntries(t, eng, []sm.Entry{
				mvccPutEntry(1, []byte("user:1"), []byte(`{"name":"Alice"}`), 0),
			})

			cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name",
				protocol.IndexCreateOptions{CaseInsensitive: true}))
			applyMVCCEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})

			for _, tc := range []struct {
				probe string
				want  int
			}{
				{"Alice", 1},
				{"alice", 1},
				{"ALICE", 1},
				{"bob", 0},
			} {
				if got := mvccLookupIndex(t, eng, "by_name", tc.probe); got != tc.want {
					t.Errorf("probe %q matched %d keys; want %d", tc.probe, got, tc.want)
				}
			}
		})
	}
}

// TestMVCCEngine_IndexCreate_DefaultIsCaseSensitive is the control and the
// polarity guard: index.IndexConfig names its field for the DEFAULT, so a
// config left at its zero value folds case. An ordinary index must not.
func TestMVCCEngine_IndexCreate_DefaultIsCaseSensitive(t *testing.T) {
	for _, backend := range []string{"memory-index", "pebble-index"} {
		t.Run(backend, func(t *testing.T) {
			var eng *MVCCEngine
			if backend == "memory-index" {
				e, stg := newMVCCEngine(t)
				t.Cleanup(func() { _ = stg.Close() })
				t.Cleanup(func() { _ = e.Close(context.Background()) })
				eng = e
			} else {
				e, cleanup := newMVCCEngineWithPebble(t)
				t.Cleanup(cleanup)
				eng = e
			}

			applyMVCCEntries(t, eng, []sm.Entry{
				mvccPutEntry(1, []byte("user:1"), []byte(`{"name":"Alice"}`), 0),
			})

			// The deprecated encoder is the pre-#34 wire shape, so this also
			// covers replay of an existing Raft log.
			cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
			applyMVCCEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})

			if got := mvccLookupIndex(t, eng, "by_name", "Alice"); got != 1 {
				t.Fatalf("exact probe matched %d keys; want 1", got)
			}
			if got := mvccLookupIndex(t, eng, "by_name", "alice"); got != 0 {
				t.Errorf("a differently-cased probe matched %d keys on a default index; "+
					"want 0 -- the default must stay case-SENSITIVE", got)
			}
		})
	}
}
