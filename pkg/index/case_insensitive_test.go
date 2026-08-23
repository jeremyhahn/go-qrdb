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

package index

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// caseInsensitiveIndexes builds one case-insensitive index of each
// implementation MVCCEngine.handleIndexCreate can choose between, so every
// assertion below runs against both.
func caseInsensitiveIndexes(t *testing.T, caseSensitive bool) map[string]Index {
	t.Helper()
	cfg := IndexConfig{Name: "by_name", CaseSensitive: caseSensitive}

	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pebbleIdx, err := NewPebbleIndexWithConfig(db, "by_name", cfg)
	if err != nil {
		t.Fatalf("NewPebbleIndexWithConfig: %v", err)
	}

	return map[string]Index{
		"MemoryIndex": NewMemoryIndexWithConfig("by_name", cfg),
		"PebbleIndex": pebbleIdx,
	}
}

// TestCaseInsensitiveIndex_PutGetDelete covers go-qrdb#34 at the index layer.
//
// Folding on only some of the value-touching paths is worse than not folding
// at all, so Get, Delete and the re-Get after Delete are all asserted: an
// index that folds on Put but not on Delete would accumulate entries no
// caller could ever remove, and one that folds on Put but not Get would
// answer empty with a nil error over a term it holds.
func TestCaseInsensitiveIndex_PutGetDelete(t *testing.T) {
	for name, idx := range caseInsensitiveIndexes(t, false) {
		t.Run(name, func(t *testing.T) {
			if err := idx.Put([]byte("user:1"), []byte("Alice")); err != nil {
				t.Fatalf("Put: %v", err)
			}

			for _, probe := range []string{"Alice", "alice", "ALICE", "aLiCe"} {
				keys, err := idx.Get([]byte(probe))
				if err != nil {
					t.Fatalf("Get(%q): %v", probe, err)
				}
				if len(keys) != 1 {
					t.Errorf("Get(%q) = %d keys; want 1", probe, len(keys))
				}
			}

			if keys, err := idx.Get([]byte("bob")); err != nil {
				t.Fatalf("Get(bob): %v", err)
			} else if len(keys) != 0 {
				t.Errorf("Get(bob) = %d keys; want 0", len(keys))
			}

			// Delete with a differently-cased term must remove the entry the
			// differently-cased Put created.
			if err := idx.Delete([]byte("user:1"), []byte("ALICE")); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if keys, err := idx.Get([]byte("alice")); err != nil {
				t.Fatalf("Get after Delete: %v", err)
			} else if len(keys) != 0 {
				t.Errorf("Get after a differently-cased Delete = %d keys; want 0 -- "+
					"an entry no caller can remove", len(keys))
			}
		})
	}
}

// TestCaseInsensitiveIndex_ScanFoldsBounds guards the bound half of the
// folding contract. A bound and the terms it is compared against must live in
// the same domain: an upper bound of "M" against folded terms excludes every
// one of them, since 'm' > 'M' in byte order. That returns an empty result
// with a nil error over an index holding matches.
func TestCaseInsensitiveIndex_ScanFoldsBounds(t *testing.T) {
	for name, idx := range caseInsensitiveIndexes(t, false) {
		t.Run(name, func(t *testing.T) {
			for _, e := range []struct{ key, value string }{
				{"user:1", "Alice"},
				{"user:2", "Bob"},
				{"user:3", "Zoe"},
			} {
				if err := idx.Put([]byte(e.key), []byte(e.value)); err != nil {
					t.Fatalf("Put(%s): %v", e.key, err)
				}
			}

			// Upper-cased bounds over folded terms. [A, M) must select alice
			// and bob and exclude zoe.
			got, err := idx.Scan([]byte("A"), []byte("M"))
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("Scan([A,M)) returned %d terms (%v); want 2 -- "+
					"an unfolded bound excludes every folded term", len(got), got)
			}
			for _, want := range []string{"alice", "bob"} {
				if _, ok := got[want]; !ok {
					t.Errorf("Scan([A,M)) missing %q; got %v", want, got)
				}
			}
			if _, ok := got["zoe"]; ok {
				t.Errorf("Scan([A,M)) included zoe; the upper bound is not exclusive")
			}
		})
	}
}

// TestCaseSensitiveIndex_IsTheDefault is the control for every assertion
// above, and the polarity guard for the constructors.
//
// IndexConfig names its field for the DEFAULT, so a config left at its zero
// value folds case. NewMemoryIndex and NewPebbleIndex must therefore pass
// CaseSensitive:true explicitly, or every existing caller silently changes
// behaviour.
func TestCaseSensitiveIndex_IsTheDefault(t *testing.T) {
	for name, idx := range caseInsensitiveIndexes(t, true) {
		t.Run(name+"/WithConfig", func(t *testing.T) {
			if err := idx.Put([]byte("user:1"), []byte("Alice")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if keys, err := idx.Get([]byte("alice")); err != nil {
				t.Fatalf("Get: %v", err)
			} else if len(keys) != 0 {
				t.Errorf("a folded probe matched %d keys on a case-sensitive index; want 0", len(keys))
			}
			if keys, err := idx.Get([]byte("Alice")); err != nil {
				t.Fatalf("Get: %v", err)
			} else if len(keys) != 1 {
				t.Errorf("exact probe matched %d keys; want 1", len(keys))
			}
		})
	}

	// The legacy constructors must land on the same side of the invariant.
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	pebbleIdx, err := NewPebbleIndex(db, "by_name", false, IndexValueTypeString)
	if err != nil {
		t.Fatalf("NewPebbleIndex: %v", err)
	}

	for name, idx := range map[string]Index{
		"MemoryIndex": NewMemoryIndex("by_name", false),
		"PebbleIndex": pebbleIdx,
	} {
		t.Run(name+"/legacy-constructor", func(t *testing.T) {
			if err := idx.Put([]byte("user:1"), []byte("Alice")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if keys, err := idx.Get([]byte("alice")); err != nil {
				t.Fatalf("Get: %v", err)
			} else if len(keys) != 0 {
				t.Errorf("%s built by the legacy constructor folded case (%d matches); "+
					"every existing caller would change behaviour", name, len(keys))
			}
		})
	}
}

// TestNewPebbleIndexWithConfig_NumericIgnoresCaseSensitivity pins the one
// interaction between the two encodings. A numeric index parses its values as
// numbers and encodes them to a fixed-width form in which case has no
// meaning; folding the decimal text first would be a no-op at best and would
// corrupt the sign-flip encoding at worst.
func TestNewPebbleIndexWithConfig_NumericIgnoresCaseSensitivity(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	idx, err := NewPebbleIndexWithConfig(db, "by_age", IndexConfig{
		Name:      "by_age",
		ValueType: IndexValueTypeNumeric,
		// Deliberately the folding polarity.
		CaseSensitive: false,
	})
	if err != nil {
		t.Fatalf("NewPebbleIndexWithConfig: %v", err)
	}
	if idx.caseInsensitive {
		t.Error("a numeric index was built case-insensitive; folding has no meaning " +
			"over the fixed-width encoding and would corrupt the sign flip")
	}

	if err := idx.Put([]byte("user:1"), []byte("-42")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	keys, err := idx.Get([]byte("-42"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("Get(-42) = %d keys; want 1", len(keys))
	}
}
