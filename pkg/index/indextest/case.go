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

package indextest

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// CaseFolding pins the case-sensitivity contract of IndexConfig.CaseSensitive
// on an index built by newIndex.
func CaseFolding(t *testing.T, newIndex Factory) {
	t.Helper()
	folding := index.IndexConfig{Name: "by_name", CaseSensitive: false}

	// Folding on only some of the value-touching paths is worse than not
	// folding at all, so Get, Delete and the re-Get after Delete are all
	// asserted: an index that folds on Put but not on Delete would accumulate
	// entries no caller could ever remove, and one that folds on Put but not
	// Get would answer empty with a nil error over a term it holds.
	t.Run("PutGetDelete", func(t *testing.T) {
		idx := newIndex(t, folding)
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

	// The bound half of the folding contract. A bound and the terms it is
	// compared against must live in the same domain: an upper bound of "M"
	// against folded terms excludes every one of them, since 'm' > 'M' in
	// byte order. That returns an empty result with a nil error over an index
	// holding matches.
	t.Run("ScanFoldsBounds", func(t *testing.T) {
		idx := newIndex(t, folding)
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

	// The control for every assertion above: an index configured
	// case-sensitive matches only the exact term.
	t.Run("CaseSensitive", func(t *testing.T) {
		idx := newIndex(t, index.IndexConfig{Name: "by_name", CaseSensitive: true})
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

// IsCaseSensitive asserts that idx, which a constructor without an
// IndexConfig built, matches only the exact term.
//
// IndexConfig names its field for the DEFAULT, so a config left at its zero
// value folds case. A constructor that takes no config must therefore pass
// CaseSensitive:true explicitly, or every existing caller silently changes
// behaviour.
func IsCaseSensitive(t *testing.T, idx index.Index) {
	t.Helper()
	if err := idx.Put([]byte("user:1"), []byte("Alice")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if keys, err := idx.Get([]byte("alice")); err != nil {
		t.Fatalf("Get: %v", err)
	} else if len(keys) != 0 {
		t.Errorf("an index built without a config folded case (%d matches); "+
			"every existing caller would change behaviour", len(keys))
	}
}
