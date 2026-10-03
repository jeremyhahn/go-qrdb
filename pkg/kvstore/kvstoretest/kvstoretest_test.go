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

package kvstoretest

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func TestGuardedMemoryStore_PutAllIfAllUnchanged_AppliesWhenAllAbsent(t *testing.T) {
	s := NewGuardedMemoryStore()
	ctx := context.Background()

	applied, err := s.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "a"}, {Key: "b"}},
		[]kvstore.KV{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}},
	)
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged: %v", err)
	}
	if !applied {
		t.Fatal("expected the batch to apply: both guards were absent")
	}

	va, err := s.Get(ctx, "a")
	if err != nil || string(va) != "1" {
		t.Fatalf("Get(a) = %q, %v; want %q, nil", va, err, "1")
	}
	vb, err := s.Get(ctx, "b")
	if err != nil || string(vb) != "2" {
		t.Fatalf("Get(b) = %q, %v; want %q, nil", vb, err, "2")
	}
}

func TestGuardedMemoryStore_PutAllIfAllUnchanged_RefusesWhenAnyGuardFails(t *testing.T) {
	s := NewGuardedMemoryStore()
	ctx := context.Background()

	if err := s.Put(ctx, "a", []byte("already here")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	applied, err := s.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "a"}, {Key: "b"}}, // "a" must be absent, but it is not
		[]kvstore.KV{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}},
	)
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged: %v", err)
	}
	if applied {
		t.Fatal("expected the batch to refuse: guard on \"a\" does not hold")
	}

	// Neither write landed -- all-or-nothing, not partial.
	if v, _ := s.Get(ctx, "b"); v != nil {
		t.Fatalf("Get(b) = %q, want nil (batch must not have applied)", v)
	}
	va, err := s.Get(ctx, "a")
	if err != nil || string(va) != "already here" {
		t.Fatalf("Get(a) = %q, %v; want the original value untouched", va, err)
	}
}

func TestGuardedMemoryStore_PutAllIfUnchanged_SingleGuard(t *testing.T) {
	s := NewGuardedMemoryStore()
	ctx := context.Background()

	applied, err := s.PutAllIfUnchanged(ctx, "guard", nil, []kvstore.KV{{Key: "p", Value: []byte("primary")}})
	if err != nil {
		t.Fatalf("PutAllIfUnchanged: %v", err)
	}
	if !applied {
		t.Fatal("expected the batch to apply: guard key was absent")
	}
	v, err := s.Get(ctx, "p")
	if err != nil || string(v) != "primary" {
		t.Fatalf("Get(p) = %q, %v; want %q, nil", v, err, "primary")
	}

	// A second call with the same absence guard now refuses: "guard" is
	// still absent (PutAllIfUnchanged's writes never touched it), but
	// prove the present-value guard path separately.
	applied2, err := s.PutAllIfUnchanged(ctx, "p", []byte("primary"), []kvstore.KV{{Key: "p", Value: []byte("updated")}})
	if err != nil {
		t.Fatalf("PutAllIfUnchanged (present-value guard): %v", err)
	}
	if !applied2 {
		t.Fatal("expected the batch to apply: guard value matched")
	}
	v2, err := s.Get(ctx, "p")
	if err != nil || string(v2) != "updated" {
		t.Fatalf("Get(p) after guarded update = %q, %v; want %q, nil", v2, err, "updated")
	}
}

func TestGuardedMemoryStore_IsAlsoAPlainKVStore(t *testing.T) {
	var s kvstore.KVStore = NewGuardedMemoryStore()
	ctx := context.Background()
	if err := s.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	v, err := s.Get(ctx, "k")
	if err != nil || string(v) != "v" {
		t.Fatalf("Get = %q, %v; want %q, nil", v, err, "v")
	}
}
