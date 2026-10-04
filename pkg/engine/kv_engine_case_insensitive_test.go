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
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// lookupIndex runs an exact-match index query against the engine and returns
// the number of entity keys it answered with.
func lookupIndex(t *testing.T, eng *KVEngine, indexName, probe string) int {
	t.Helper()
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
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

// TestKVEngine_IndexCreate_CaseInsensitive_MatchesDifferentlyCasedProbe is the
// verification go-qrdb#34 asks for, at the engine layer: a case-insensitive
// definition must answer a differently-cased probe with a NON-EMPTY result.
//
// Before the flag reached the engine, handleIndexCreate hardcoded
// CaseSensitive:true, so this lookup came back empty with a nil error -- the
// silent-empty class the index family of issues exists to eliminate.
//
// Both index population paths are covered: the record written BEFORE creation
// arrives through BackfillIndex, the one written AFTER through the ordinary
// write-maintenance path. Covering only one would leave a folding gap that
// answers some probes and not others.
func TestKVEngine_IndexCreate_CaseInsensitive_MatchesDifferentlyCasedProbe(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Written BEFORE the index exists: reaches the index via backfill.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "user:1", `{"name":"Alice"}`),
	})

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name",
		protocol.IndexCreateOptions{CaseInsensitive: true}))
	applyKVEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})

	// Written AFTER the index exists: reaches the index via write maintenance.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(3, "user:2", `{"name":"BOB"}`),
	})

	for _, tc := range []struct {
		probe string
		want  int
	}{
		{"Alice", 1}, // as stored, backfilled record
		{"alice", 1}, // folded probe, backfilled record
		{"ALICE", 1},
		{"BOB", 1}, // as stored, write-maintained record
		{"bob", 1}, // folded probe, write-maintained record
		{"carol", 0},
	} {
		if got := lookupIndex(t, eng, "by_name", tc.probe); got != tc.want {
			t.Errorf("probe %q matched %d keys; want %d", tc.probe, got, tc.want)
		}
	}
}

// TestKVEngine_IndexCreate_DefaultIsCaseSensitive is the control, and the
// guard on the polarity of the new flag.
//
// index.IndexConfig names its field for the DEFAULT, so its zero value folds
// case: an engine that derived CaseSensitive from an unset option, or a wire
// flag whose absence meant "insensitive", would turn every ordinary index
// case-insensitive without anything else in the suite noticing.
func TestKVEngine_IndexCreate_DefaultIsCaseSensitive(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "user:1", `{"name":"Alice"}`),
	})

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name",
		protocol.IndexCreateOptions{}))
	applyKVEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})

	if got := lookupIndex(t, eng, "by_name", "Alice"); got != 1 {
		t.Fatalf("exact probe matched %d keys; want 1", got)
	}
	if got := lookupIndex(t, eng, "by_name", "alice"); got != 0 {
		t.Errorf("a differently-cased probe matched %d keys on a default index; "+
			"want 0 -- the default must stay case-SENSITIVE", got)
	}
}

// TestKVEngine_IndexCreate_LegacyCommandIsCaseSensitive replays a command in
// the shape written before IndexFlagCaseInsensitive existed and proves the
// engine still builds a case-sensitive index from it. This is the Raft-log
// compatibility guarantee, asserted through the engine rather than only
// through the decoder.
func TestKVEngine_IndexCreate_LegacyCommandIsCaseSensitive(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "user:1", `{"name":"Alice"}`),
	})

	// The deprecated encoder is exactly the pre-#34 wire shape.
	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
	applyKVEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})

	if got := lookupIndex(t, eng, "by_name", "alice"); got != 0 {
		t.Errorf("a replayed pre-#34 command produced a case-INSENSITIVE index "+
			"(%d matches for a folded probe); every existing log would change meaning", got)
	}
	if got := lookupIndex(t, eng, "by_name", "Alice"); got != 1 {
		t.Errorf("exact probe matched %d keys; want 1", got)
	}
}

// TestKVEngine_IndexCreate_UniqueAndCaseInsensitiveCompose checks the two
// flags do not collide on the wire: a unique case-insensitive index must
// enforce uniqueness over the FOLDED term, which is the combination an
// `index:"unique,case_insensitive"` tag produces.
func TestKVEngine_IndexCreate_UniqueAndCaseInsensitiveCompose(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email",
		protocol.IndexCreateOptions{Unique: true, CaseInsensitive: true}))
	applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: cmd}})

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(2, "user:1", `{"email":"Alice@Example.com"}`),
	})

	if got := lookupIndex(t, eng, "by_email", "alice@example.com"); got != 1 {
		t.Errorf("folded probe on a unique case-insensitive index matched %d keys; want 1", got)
	}
}
