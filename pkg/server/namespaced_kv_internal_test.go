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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package server

import (
	"context"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
)

// ---------------------------------------------------------------------------
// The internal-key guard inside a namespaced view
//
// The inner service sees "acme/__tbl:shop:1/row", which is an ordinary user
// key by every test the inner guard applies -- so the guard has to run here,
// on the key in the form the caller wrote it. These tests seed the inner
// store with exactly that shape, which is what a tenant holding relational
// tables actually looks like on disk.
// ---------------------------------------------------------------------------

// seededNamespacedKV returns a namespaced view over an inner store holding one
// ordinary user key plus one key in each reserved namespace, all under the
// "acme/" namespace prefix.
func seededNamespacedKV(t *testing.T) *namespacedKVService {
	t.Helper()

	inner := newMockKVService()
	ctx := context.Background()
	seed := map[string]string{
		"acme/app/a":                  "user data",
		"acme/__sys:roles/admin":      "role",
		"acme/__tbl:shop:1/row":       "classified",
		"acme/__idx:shop:1:uq_e/a@b":  "pk",
		"acme/__seq:shop:__autoinc:1": "counter",
		"acme/ts:cpu/1":               "point",
	}
	for k, v := range seed {
		if err := inner.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("seed %q: %v", k, err)
		}
	}
	return newNamespacedKVService(inner, "acme")
}

func TestNamespacedKV_EnumerationHidesEveryReservedNamespace(t *testing.T) {
	svc := seededNamespacedKV(t)
	ctx := context.Background()
	want := []string{"app/a"}

	listers := map[string]func() ([]string, error){
		"List":      func() ([]string, error) { return svc.List(ctx, "") },
		"ListStale": func() ([]string, error) { return svc.ListStale(ctx, "") },
		"ListWithConsistency": func() ([]string, error) {
			return svc.ListWithConsistency(ctx, "", consistency.ReadOptions{})
		},
	}
	for name, list := range listers {
		got, err := list()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s(\"\") = %q, want %q -- a reserved namespace is visible to the tenant's KV surface", name, got, want)
		}
	}

	scanners := map[string]func() (map[string][]byte, error){
		"Scan":      func() (map[string][]byte, error) { return svc.Scan(ctx, "") },
		"ScanStale": func() (map[string][]byte, error) { return svc.ScanStale(ctx, "") },
		"ScanWithConsistency": func() (map[string][]byte, error) {
			return svc.ScanWithConsistency(ctx, "", consistency.ReadOptions{})
		},
	}
	for name, scan := range scanners {
		got, err := scan()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != 1 {
			t.Errorf("%s(\"\") returned %d pairs, want 1: %v", name, len(got), got)
		}
		if string(got["app/a"]) != "user data" {
			t.Errorf("%s(\"\") lost the user key: %v", name, got)
		}
		if _, leaked := got["__tbl:shop:1/row"]; leaked {
			t.Errorf("%s(\"\") disclosed a relational row", name)
		}
	}
}

// TestNamespacedKV_ServesAReservedNamespaceToItsOwner is the counter-test:
// the relational layer and the RBAC stores reach their records through this
// same namespaced view, so an enumeration rooted inside a namespace whose
// owner enumerates it must be served in full.
func TestNamespacedKV_ServesAReservedNamespaceToItsOwner(t *testing.T) {
	svc := seededNamespacedKV(t)
	ctx := context.Background()

	rows, err := svc.Scan(ctx, "__tbl:shop:1/")
	if err != nil {
		t.Fatalf("Scan rows: %v", err)
	}
	if string(rows["__tbl:shop:1/row"]) != "classified" {
		t.Fatalf("the table's own row scan returned %v -- the guard is hiding the relational layer from its own data", rows)
	}

	entries, err := svc.List(ctx, "__idx:shop:1:uq_e/")
	if err != nil {
		t.Fatalf("List index entries: %v", err)
	}
	if !slices.Equal(entries, []string{"__idx:shop:1:uq_e/a@b"}) {
		t.Fatalf("the index's own listing returned %q, want its single entry", entries)
	}

	roles, err := svc.List(ctx, "__sys:roles/")
	if err != nil {
		t.Fatalf("List roles: %v", err)
	}
	if !slices.Equal(roles, []string{"__sys:roles/admin"}) {
		t.Fatalf("the RBAC store's own listing returned %q, want its single record", roles)
	}
}

// TestNamespacedKV_HidesNamespacesWithNoEnumeratingOwner pins the withheld
// half of the grant: nothing reads sequences or time-series points through KV
// enumeration, so naming their prefix outright still discloses nothing.
func TestNamespacedKV_HidesNamespacesWithNoEnumeratingOwner(t *testing.T) {
	svc := seededNamespacedKV(t)
	ctx := context.Background()

	for _, prefix := range []string{"__seq:", "ts:"} {
		keys, err := svc.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List(%q): %v", prefix, err)
		}
		if len(keys) != 0 {
			t.Errorf("List(%q) = %q, want nothing", prefix, keys)
		}
	}
}

// ---------------------------------------------------------------------------
// The guarded writes carry the namespace prefix
//
// CompareAndDelete and CompareAndSwap had no test. The property that matters
// is not that they work, but that the PREFIX is applied before the guard is
// evaluated -- the same property PutIfAbsent's doc comment spells out.
//
// A guarded write that forgot the prefix would reach across the namespace
// boundary: tenant "acme" comparing against a key it does not own, and
// deleting or overwriting it when the value happened to match. It would also
// pass a test that only checked the boolean, because the operation would
// genuinely succeed -- on the wrong key. These tests therefore assert the
// effect on the INNER store, keyed by the fully-qualified name, and assert
// that another tenant's identically-named key is untouched.
// ---------------------------------------------------------------------------

func TestNamespacedKV_CompareAndDelete_IsScopedToTheNamespace(t *testing.T) {
	ctx := context.Background()
	inner := newMockKVService()
	if err := inner.Put(ctx, "acme/shared", []byte("v")); err != nil {
		t.Fatalf("seed acme: %v", err)
	}
	if err := inner.Put(ctx, "globex/shared", []byte("v")); err != nil {
		t.Fatalf("seed globex: %v", err)
	}
	kv := newNamespacedKVService(inner, "acme")

	ok, err := kv.CompareAndDelete(ctx, "shared", []byte("v"))
	if err != nil {
		t.Fatalf("CompareAndDelete: %v", err)
	}
	if !ok {
		t.Fatal("CompareAndDelete declined a matching value in its own namespace")
	}
	if _, err := inner.Get(ctx, "acme/shared"); err == nil {
		t.Error("the caller's own key survived the delete: the prefix was not applied")
	}
	if _, err := inner.Get(ctx, "globex/shared"); err != nil {
		t.Errorf("another tenant's identically-named key was deleted: %v", err)
	}
}

func TestNamespacedKV_CompareAndSwap_IsScopedToTheNamespace(t *testing.T) {
	ctx := context.Background()
	inner := newMockKVService()
	if err := inner.Put(ctx, "acme/shared", []byte("v")); err != nil {
		t.Fatalf("seed acme: %v", err)
	}
	if err := inner.Put(ctx, "globex/shared", []byte("v")); err != nil {
		t.Fatalf("seed globex: %v", err)
	}
	kv := newNamespacedKVService(inner, "acme")

	ok, err := kv.CompareAndSwap(ctx, "shared", []byte("v"), []byte("next"))
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if !ok {
		t.Fatal("CompareAndSwap declined a matching value in its own namespace")
	}
	got, err := inner.Get(ctx, "acme/shared")
	if err != nil {
		t.Fatalf("Get acme/shared: %v", err)
	}
	if string(got) != "next" {
		t.Errorf("acme/shared = %q, want %q: the prefix was not applied", got, "next")
	}
	other, err := inner.Get(ctx, "globex/shared")
	if err != nil {
		t.Fatalf("Get globex/shared: %v", err)
	}
	if string(other) != "v" {
		t.Errorf("another tenant's identically-named key was overwritten: %q", other)
	}
}

// TestNamespacedKV_GuardedWrites_DeclineAcrossTheBoundary is the negative
// direction: a caller that supplies the value ANOTHER tenant's key holds must
// still be declined, because the key it names does not exist in its own
// namespace. A prefix-less implementation would succeed here.
func TestNamespacedKV_GuardedWrites_DeclineAcrossTheBoundary(t *testing.T) {
	ctx := context.Background()
	inner := newMockKVService()
	if err := inner.Put(ctx, "globex/only-theirs", []byte("theirs")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	kv := newNamespacedKVService(inner, "acme")

	if ok, err := kv.CompareAndDelete(ctx, "only-theirs", []byte("theirs")); err != nil || ok {
		t.Errorf("CompareAndDelete reached across the namespace boundary: ok=%v err=%v", ok, err)
	}
	if ok, err := kv.CompareAndSwap(ctx, "only-theirs", []byte("theirs"), []byte("mine")); err != nil || ok {
		t.Errorf("CompareAndSwap reached across the namespace boundary: ok=%v err=%v", ok, err)
	}
	got, err := inner.Get(ctx, "globex/only-theirs")
	if err != nil {
		t.Fatalf("the other tenant's key was destroyed: %v", err)
	}
	if string(got) != "theirs" {
		t.Errorf("the other tenant's value changed to %q", got)
	}
}
