// Copyright (c) 2026 Jeremy Hahn
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

package namespace

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// sharedInner returns one store holding keys a..d in namespace "t1", the same
// keys in namespace "t2", which sorts after it, and one key outside both.
func sharedInner(t *testing.T) *memory.MemoryStorage {
	t.Helper()
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })
	for _, ns := range []string{"t1/", "t2/"} {
		for _, k := range []string{"a", "b", "c", "d"} {
			if err := inner.Set([]byte(ns+k), []byte(k)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := inner.Set([]byte("zz"), []byte("outside")); err != nil {
		t.Fatal(err)
	}
	return inner
}

func innerKeys(t *testing.T, inner storage.Storage) []string {
	t.Helper()
	keys, err := inner.List(nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	return out
}

// TestNamespaceEngine_DeleteRange pins confinement: a bounded range is
// translated into the namespace, and an unbounded one ends at the namespace's
// own end, never reaching a namespace or key that sorts after it.
func TestNamespaceEngine_DeleteRange(t *testing.T) {
	cases := map[string]struct {
		end  []byte
		want []string
	}{
		"bounded":   {[]byte("c"), []string{"t1/a", "t1/c", "t1/d", "t2/a", "t2/b", "t2/c", "t2/d", "zz"}},
		"unbounded": {nil, []string{"t1/a", "t2/a", "t2/b", "t2/c", "t2/d", "zz"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			inner := sharedInner(t)
			if err := New(inner, []byte("t1")).DeleteRange([]byte("b"), tc.end); err != nil {
				t.Fatalf("DeleteRange: %v", err)
			}
			if got := innerKeys(t, inner); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("inner keys = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNamespacedBatch_DeleteRange(t *testing.T) {
	cases := map[string]struct {
		end  []byte
		want []string
	}{
		"bounded":   {[]byte("c"), []string{"t1/a", "t1/c", "t1/d", "t2/a", "t2/b", "t2/c", "t2/d", "zz"}},
		"unbounded": {nil, []string{"t1/a", "t2/a", "t2/b", "t2/c", "t2/d", "zz"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			inner := sharedInner(t)
			b := New(inner, []byte("t1")).Batch()
			defer func() { _ = b.Close() }()
			if err := b.DeleteRange([]byte("b"), tc.end); err != nil {
				t.Fatalf("DeleteRange: %v", err)
			}
			if err := b.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if got := innerKeys(t, inner); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("inner keys = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNamespaceEngine_DeleteRangeRefusals checks the range is validated in
// the caller's key space, before the prefix could make an invalid range look
// valid: an empty start would otherwise become the bare namespace prefix.
func TestNamespaceEngine_DeleteRangeRefusals(t *testing.T) {
	inner := sharedInner(t)
	ns := New(inner, []byte("t1"))
	b := ns.Batch()
	defer func() { _ = b.Close() }()

	for name, del := range map[string]func(start, end []byte) error{
		"engine": ns.DeleteRange,
		"batch":  b.DeleteRange,
	} {
		if err := del(nil, []byte("c")); !errors.Is(err, storage.ErrEmptyKey) {
			t.Errorf("%s DeleteRange(nil, c) = %v, want ErrEmptyKey", name, err)
		}
		if err := del([]byte("c"), []byte("b")); !errors.Is(err, storage.ErrInvalidRange) {
			t.Errorf("%s DeleteRange(c, b) = %v, want ErrInvalidRange", name, err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := innerKeys(t, inner); len(got) != 9 {
		t.Fatalf("a refused range delete removed keys: %v", got)
	}
}

func TestAppendRangeEnd(t *testing.T) {
	prefix := []byte("t1/")
	if got := appendRangeEnd(prefix, []byte("x")); string(got) != "t1/x" {
		t.Fatalf("appendRangeEnd(t1/, x) = %q, want t1/x", got)
	}
	if got := appendRangeEnd(prefix, nil); string(got) != "t10" {
		t.Fatalf("appendRangeEnd(t1/, nil) = %q, want the namespace end t10", got)
	}
	if string(prefix) != "t1/" {
		t.Fatalf("appendRangeEnd modified the prefix: %q", prefix)
	}
}
