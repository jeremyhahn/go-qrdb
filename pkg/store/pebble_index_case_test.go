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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// createdIndexMatches applies an index-create command to a PebbleStateMachine,
// writes one entry under a mixed-case term, and reports how many keys a probe
// answers with.
func createdIndexMatches(t *testing.T, opts protocol.IndexCreateOptions, stored, probe string) int {
	t.Helper()
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_team", "team", opts))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	idx, ok := s.indexes["by_team"]
	if !ok {
		t.Fatal("index was not created")
	}
	if err := idx.Put([]byte("member/1"), []byte(stored)); err != nil {
		t.Fatalf("index Put: %v", err)
	}

	keys, err := idx.Get([]byte(probe))
	if err != nil {
		t.Fatalf("index Get(%q): %v", probe, err)
	}
	return len(keys)
}

// TestPebbleStateMachine_IndexCreate_CaseInsensitive covers go-qrdb#34 on the
// third index-create handler in the tree. pebbleHandleIndexCreate decoded with
// the positional DecodeIndexCreate, which cannot report case-insensitivity, and
// built its index with a constructor that could not express it -- so an
// index:"case_insensitive" definition became a case-SENSITIVE index here too,
// and a lookup for "Alpha" over a stored "alpha" answered empty with a nil
// error.
func TestPebbleStateMachine_IndexCreate_CaseInsensitive(t *testing.T) {
	for _, probe := range []string{"Alpha", "alpha", "ALPHA"} {
		if got := createdIndexMatches(t,
			protocol.IndexCreateOptions{CaseInsensitive: true}, "Alpha", probe); got != 1 {
			t.Errorf("probe %q matched %d keys; want 1", probe, got)
		}
	}
	if got := createdIndexMatches(t,
		protocol.IndexCreateOptions{CaseInsensitive: true}, "Alpha", "beta"); got != 0 {
		t.Errorf("probe %q matched %d keys; want 0", "beta", got)
	}
}

// TestPebbleStateMachine_IndexCreate_DefaultIsCaseSensitive is the control and
// the polarity guard. index.IndexConfig names CaseSensitive for the DEFAULT,
// so a config left at its zero value folds case: this handler must set it, or
// every ordinary index it builds becomes case-insensitive.
func TestPebbleStateMachine_IndexCreate_DefaultIsCaseSensitive(t *testing.T) {
	if got := createdIndexMatches(t, protocol.IndexCreateOptions{}, "Alpha", "Alpha"); got != 1 {
		t.Fatalf("exact probe matched %d keys; want 1", got)
	}
	if got := createdIndexMatches(t, protocol.IndexCreateOptions{}, "Alpha", "alpha"); got != 0 {
		t.Errorf("a differently-cased probe matched %d keys on a default index; "+
			"want 0 -- the default must stay case-SENSITIVE", got)
	}
}
