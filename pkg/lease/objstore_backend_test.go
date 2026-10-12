// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lease

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// openObjstoreTable opens objstore's local provider rooted at dir. Opening
// the same dir again is a reopen of the lease table written there.
func openObjstoreTable(t *testing.T, dir string) storage.Storage {
	t.Helper()
	st, err := objstore.New("local", map[string]string{"path": dir})
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// restoredFrom builds a fresh Lessor from the lease table in st.
func restoredFrom(t *testing.T, st storage.Storage) *Lessor {
	t.Helper()
	lr := NewLessor()
	t.Cleanup(lr.Close)
	if err := lr.Restore(st); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return lr
}

func TestObjstoreBackend_LeaseGrantBindRevokeRestore(t *testing.T) {
	dir := t.TempDir()
	st := openObjstoreTable(t, dir)

	lr := NewLessor()
	t.Cleanup(lr.Close)
	a, err := lr.Grant(st, 30)
	if err != nil {
		t.Fatalf("Grant A: %v", err)
	}
	b, err := lr.Grant(st, 40)
	if err != nil {
		t.Fatalf("Grant B: %v", err)
	}
	c, err := lr.Grant(st, 50)
	if err != nil {
		t.Fatalf("Grant C: %v", err)
	}
	// "\x00bin" checks a binary key binds and restores like a text one.
	for _, bind := range []struct {
		key string
		id  int64
	}{{"a1", a.ID}, {"\x00bin", a.ID}, {"b1", b.ID}, {"c1", c.ID}} {
		if err := lr.Bind(st, []byte(bind.key), bind.id); err != nil {
			t.Fatalf("Bind(%q, %d): %v", bind.key, bind.id, err)
		}
	}
	revoked, err := lr.Revoke(st, c.ID)
	if err != nil {
		t.Fatalf("Revoke C: %v", err)
	}
	if len(revoked) != 1 || string(revoked[0]) != "c1" {
		t.Fatalf("Revoke C returned %q, want [c1]", revoked)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st = openObjstoreTable(t, dir)
	restored := restoredFrom(t, st)
	if restored.Count() != 2 {
		t.Fatalf("restored Count = %d, want 2 (the revoked lease must stay gone)", restored.Count())
	}
	for _, want := range []struct {
		id   int64
		ttl  int64
		keys []string
	}{{a.ID, 30, []string{"a1", "\x00bin"}}, {b.ID, 40, []string{"b1"}}} {
		got := restored.Lookup(want.id)
		if got == nil {
			t.Fatalf("lease %d not restored", want.id)
		}
		if got.GrantedTTL != want.ttl {
			t.Errorf("lease %d GrantedTTL = %d, want %d", want.id, got.GrantedTTL, want.ttl)
		}
		for _, k := range want.keys {
			if owner := restored.LeaseOf([]byte(k)); owner != want.id {
				t.Errorf("%q restored bound to %d, want %d", k, owner, want.id)
			}
		}
	}
	if restored.Has(c.ID) || restored.LeaseOf([]byte("c1")) != NoLease {
		t.Fatal("revoked lease C or its binding came back after reopen")
	}

	// The ID sequence survived the reopen: no ID is reused.
	d, err := restored.Grant(st, 10)
	if err != nil {
		t.Fatalf("Grant after reopen: %v", err)
	}
	if d.ID != c.ID+1 {
		t.Fatalf("Grant after reopen ID = %d, want %d", d.ID, c.ID+1)
	}

	// A revoke after the reopen is itself durable.
	keys, err := restored.Revoke(st, a.ID)
	if err != nil {
		t.Fatalf("Revoke A after reopen: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("Revoke A after reopen returned %q, want a1 and the binary key", keys)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again := restoredFrom(t, openObjstoreTable(t, dir))
	if again.Has(a.ID) || !again.Has(b.ID) || !again.Has(d.ID) {
		t.Fatalf("second reopen: has A=%t B=%t D=%t, want false true true",
			again.Has(a.ID), again.Has(b.ID), again.Has(d.ID))
	}
	if again.LeaseOf([]byte("\x00bin")) != NoLease || again.LeaseOf([]byte("b1")) != b.ID {
		t.Fatal("second reopen: bindings do not match the revoke of A")
	}
}
