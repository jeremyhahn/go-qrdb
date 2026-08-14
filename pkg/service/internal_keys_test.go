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

package service

import (
	"slices"
	"testing"
)

func TestIsInternalKey_MatchesEveryReservedPrefix(t *testing.T) {
	internal := []string{
		"__sys:",
		"__sys:roles/admin",
		"__sys:users/root",
		"__sys:tables/shop/orders",
		"__tbl:",
		"__tbl:shop:1/\x00\x00\x00\x00\x00\x00\x00\x01",
		"__idx:",
		"__idx:shop:1:uq_email/a@b",
		"__seq:",
		"__seq:shop:__autoinc:1",
		"ts:",
		"ts:cpu/1700000000",
		"idx:metric:",
		"idx:metric:cpu",
		"idx:tag:",
		"idx:tag:host=web1",
		"retention:",
		"retention:cpu",
	}
	for _, key := range internal {
		if !IsInternalKey(key) {
			t.Errorf("IsInternalKey(%q) = false, want true", key)
		}
	}
}

// TestIsInternalKey_AdmitsUserKeysThatMerelyResembleReservedOnes keeps the
// guard from over-reaching. It matches a prefix, not a substring and not a
// near-miss: "ts" without the colon and "idx:metrics" are ordinary user keys,
// and refusing them would deny legitimate restores.
func TestIsInternalKey_AdmitsUserKeysThatMerelyResembleReservedOnes(t *testing.T) {
	userKeys := []string{
		"",
		"app/a",
		"_sys:x",
		"__sy:foo",
		"__sysx:foo",
		"__tb:foo",
		"__tblx:foo",
		"__id:foo",
		"__se:foo",
		"ts",
		"tsx:foo",
		"t:foo",
		"idx:",
		"idx:other",
		"idx:metrics", // "idx:metric:" requires the trailing colon
		"retention",
		"my__sys:key",
		"prefixed/ts:cpu",
		"prefixed/__tbl:shop:1/x",
	}
	for _, key := range userKeys {
		if IsInternalKey(key) {
			t.Errorf("IsInternalKey(%q) = true, want false", key)
		}
	}
}

// TestInternalKeyPrefixes_TracksTheKeyLayout pins the guard to the key layout
// it guards. The relational prefixes are declared from the same constants
// pkg/service composes row, index, and sequence keys from, so the two cannot
// drift; this fails if a prefix is dropped from the set or a constant is
// changed without the set following.
func TestInternalKeyPrefixes_TracksTheKeyLayout(t *testing.T) {
	got := InternalKeyPrefixes()
	want := []string{
		InternalKeyPrefixSystem,
		tableDataPrefix,
		tableIndexPrefix,
		tableSequencePrefix,
		"ts:",
		"idx:metric:",
		"idx:tag:",
		"retention:",
	}
	for _, p := range want {
		if !slices.Contains(got, p) {
			t.Errorf("InternalKeyPrefixes() = %q, missing %q", got, p)
		}
	}
	if len(got) != len(want) {
		t.Errorf("InternalKeyPrefixes() has %d entries (%q), want %d -- a prefix was added without a decision about its skip-filter grant",
			len(got), got, len(want))
	}

	// The catalog lives under the system prefix, which is why the catalog was
	// never exposed and the row data was.
	if !IsInternalKey(tableCatalogPrefix) {
		t.Errorf("the table catalog prefix %q is not internal", tableCatalogPrefix)
	}
}

// TestInternalKeyPrefixes_ReturnsIndependentCopy proves a caller cannot widen
// the user keyspace by mutating the slice the guard hands out.
func TestInternalKeyPrefixes_ReturnsIndependentCopy(t *testing.T) {
	first := InternalKeyPrefixes()
	if len(first) == 0 {
		t.Fatal("InternalKeyPrefixes returned nothing")
	}
	first[0] = "clobbered"

	if !IsInternalKey("__sys:roles/admin") {
		t.Fatal("mutating the returned slice disabled the system-key guard")
	}
	if second := InternalKeyPrefixes(); second[0] == "clobbered" {
		t.Fatal("InternalKeyPrefixes shares its backing array with the caller")
	}
}

func TestInternalKeyPrefixes_ContainsTheSystemPrefix(t *testing.T) {
	if InternalKeyPrefixSystem != "__sys:" {
		t.Fatalf("InternalKeyPrefixSystem = %q, want __sys:", InternalKeyPrefixSystem)
	}
	if !slices.Contains(InternalKeyPrefixes(), InternalKeyPrefixSystem) {
		t.Fatal("the system prefix is missing from the internal prefix set")
	}
}

// TestSkipInternalKeyFilter_GrantsOnlyTheEnumeratingNamespaces asserts the
// escape hatch is exactly as wide as the subsystems that need it. The
// relational row and index namespaces are read back through this very
// enumeration surface, so they must be granted it -- without the grant every
// table scan returns nothing. The sequence and time-series namespaces are
// reached by exact key or through their own engine, so they are not.
func TestSkipInternalKeyFilter_GrantsOnlyTheEnumeratingNamespaces(t *testing.T) {
	granted := []string{
		InternalKeyPrefixSystem,
		"__sys:roles/",
		tableDataPrefix,
		"__tbl:shop:1/",
		tableIndexPrefix,
		"__idx:shop:1:uq_email/",
	}
	for _, prefix := range granted {
		if !SkipInternalKeyFilter(prefix) {
			t.Errorf("SkipInternalKeyFilter(%q) = false, want true -- the owning subsystem cannot read its own records", prefix)
		}
	}

	withheld := []string{
		tableSequencePrefix,
		"__seq:shop:__autoinc:1",
		"ts:",
		"ts:cpu",
		"idx:metric:",
		"idx:tag:",
		"retention:",
	}
	for _, prefix := range withheld {
		if SkipInternalKeyFilter(prefix) {
			t.Errorf("SkipInternalKeyFilter(%q) = true, want false -- this namespace has no enumerating owner and must stay hidden", prefix)
		}
	}
}

// TestSkipInternalKeyFilter_RefusesPrefixesThatMerelySpanTheNamespace is the
// test that keeps a one-character sweep from becoming an enumeration of the
// whole internal keyspace: "_" covers both "__sys:" and "__tbl:" but lies
// inside neither, so it is an ordinary user prefix and stays filtered.
func TestSkipInternalKeyFilter_RefusesPrefixesThatMerelySpanTheNamespace(t *testing.T) {
	spanning := []string{"", "_", "__", "__s", "__t", "t", "i", "r", "app/"}
	for _, prefix := range spanning {
		if SkipInternalKeyFilter(prefix) {
			t.Errorf("SkipInternalKeyFilter(%q) = true, want false", prefix)
		}
	}
}

func TestFilterInternalKeys_DropsEveryReservedNamespace(t *testing.T) {
	keys := []string{
		"app/a",
		"__sys:roles/admin",
		"__tbl:shop:1/row",
		"__idx:shop:1:ix/entry",
		"__seq:shop:__autoinc:1",
		"ts:cpu/1",
		"app/b",
	}
	got := FilterInternalKeys(keys, "")
	want := []string{"app/a", "app/b"}
	if !slices.Equal(got, want) {
		t.Fatalf("FilterInternalKeys(_, \"\") = %q, want %q", got, want)
	}
}

func TestFilterInternalKeys_PassesTheOwningNamespaceThrough(t *testing.T) {
	keys := []string{"__tbl:shop:1/a", "__tbl:shop:1/b"}
	got := FilterInternalKeys(keys, "__tbl:shop:1/")
	if !slices.Equal(got, keys) {
		t.Fatalf("FilterInternalKeys over the owning prefix = %q, want %q", got, keys)
	}
}

func TestFilterInternalMap_DropsEveryReservedNamespace(t *testing.T) {
	data := map[string][]byte{
		"app/a":                  []byte("1"),
		"__sys:roles/admin":      []byte("2"),
		"__tbl:shop:1/row":       []byte("3"),
		"__idx:shop:1:ix/entry":  []byte("4"),
		"__seq:shop:__autoinc:1": []byte("5"),
		"retention:cpu":          []byte("6"),
	}
	got := FilterInternalMap(data, "")
	if len(got) != 1 {
		t.Fatalf("FilterInternalMap(_, \"\") = %v, want only the user key", got)
	}
	if _, ok := got["app/a"]; !ok {
		t.Fatalf("FilterInternalMap dropped the user key: %v", got)
	}
}

func TestFilterInternalMap_PassesTheOwningNamespaceThrough(t *testing.T) {
	data := map[string][]byte{"__idx:shop:1:ix/entry": []byte("pk")}
	got := FilterInternalMap(data, "__idx:shop:1:ix/")
	if len(got) != 1 {
		t.Fatalf("FilterInternalMap over the owning prefix = %v, want the entry back", got)
	}
}
