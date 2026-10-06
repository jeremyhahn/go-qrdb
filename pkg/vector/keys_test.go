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

package vector

import (
	"bytes"
	"sort"
	"strings"
	"testing"
)

func TestNamespaceKey_CarriesTheReservedPrefix(t *testing.T) {
	key := NamespaceKey("docs")
	if !strings.HasPrefix(string(key), KeyPrefix) {
		t.Fatalf("NamespaceKey(%q) = %q, want prefix %q", "docs", key, KeyPrefix)
	}
	if !bytes.HasPrefix(key, NamespacePrefix()) {
		t.Fatalf("NamespaceKey(%q) = %q does not have prefix %q", "docs", key, NamespacePrefix())
	}
}

func TestNamespaceKey_DistinctNamespacesProduceDistinctKeys(t *testing.T) {
	if bytes.Equal(NamespaceKey("a"), NamespaceKey("b")) {
		t.Fatal("NamespaceKey(\"a\") == NamespaceKey(\"b\")")
	}
}

func TestVectorKey_OrdersByIDNumerically(t *testing.T) {
	// Big-endian encoding is what makes a lexicographic key scan visit IDs in
	// numeric order; this pins that property rather than merely the encoding.
	ids := []uint64{1, 2, 10, 256, 1 << 40}
	keys := make([][]byte, len(ids))
	for i, id := range ids {
		keys[i] = VectorKey("ns", id)
	}
	sorted := make([][]byte, len(keys))
	copy(sorted, keys)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	for i := range keys {
		if !bytes.Equal(keys[i], sorted[i]) {
			t.Fatalf("VectorKey IDs not in lexicographic order: %v", ids)
		}
	}
}

func TestVectorKey_DistinctNamespacesDoNotCollide(t *testing.T) {
	// A key must not let one namespace's vector collide with, or be mistaken
	// for, another's -- including when one name is a prefix of the other.
	if bytes.Equal(VectorKey("a", 1), VectorKey("ab", 1)) {
		t.Fatal("VectorKey(\"a\", 1) == VectorKey(\"ab\", 1)")
	}
	if !bytes.HasPrefix(VectorKey("ns", 5), VectorPrefix("ns")) {
		t.Fatal("VectorKey does not carry VectorPrefix")
	}
	if bytes.HasPrefix(VectorKey("ns2", 5), VectorPrefix("ns")) {
		t.Fatal("VectorPrefix(\"ns\") matches a key in namespace \"ns2\"")
	}
}

func TestMetadataKey_FieldValueDelimitersSeparateDistinctPairs(t *testing.T) {
	// "a","bc" must not collide with "ab","c": the \x00 separators are what
	// keep the two pairs apart despite both concatenating to the same bytes.
	k1 := MetadataKey("ns", "a", "bc", 1)
	k2 := MetadataKey("ns", "ab", "c", 1)
	if bytes.Equal(k1, k2) {
		t.Fatal("MetadataKey(\"a\",\"bc\") == MetadataKey(\"ab\",\"c\")")
	}
}

func TestMetadataKey_EmptyValueUsesSentinelNotEmptyHex(t *testing.T) {
	// hex.EncodeToString("") is "" too, which would leave an empty path
	// segment between two '/' separators -- the "//" go-objstore's local
	// provider refuses (hexSegment's doc comment, B4). hexSegment substitutes
	// "-", a byte outside hex's own alphabet, so it can never be mistaken for
	// a real hex-encoded empty-string VALUE and can never collide with one.
	key := MetadataKey("ns", "field", "", 1)
	if !bytes.Contains(key, []byte("/-/")) {
		t.Fatalf("MetadataKey(namespace, field, \"\", id) = %q, want the \"-\" sentinel segment between slashes", key)
	}
	if bytes.Contains(key, []byte("//")) {
		t.Fatalf("MetadataKey(namespace, field, \"\", id) = %q, contains \"//\"", key)
	}

	// Field and value share hexSegment; cover the sentinel for an empty
	// FIELD too, and confirm the two empty segments remain distinguishable
	// from each other's position rather than colliding.
	emptyField := MetadataKey("ns", "", "value", 1)
	emptyBoth := MetadataKey("ns", "", "", 1)
	if bytes.Equal(key, emptyField) || bytes.Equal(key, emptyBoth) || bytes.Equal(emptyField, emptyBoth) {
		t.Fatal("MetadataKey with different empty-string positions produced colliding keys")
	}
}

func TestMetadataKey_PrefixesNestCorrectly(t *testing.T) {
	key := MetadataKey("ns", "field", "value", 42)
	if !bytes.HasPrefix(key, MetadataFieldValuePrefix("ns", "field", "value")) {
		t.Fatal("MetadataKey does not carry MetadataFieldValuePrefix")
	}
	if !bytes.HasPrefix(key, MetadataPrefix("ns")) {
		t.Fatal("MetadataKey does not carry MetadataPrefix")
	}
	if bytes.HasPrefix(key, MetadataFieldValuePrefix("ns", "field", "other")) {
		t.Fatal("MetadataKey matches a different value's prefix")
	}
}

func TestMutationLogKey_OrdersBySequenceNumerically(t *testing.T) {
	seqs := []uint64{0, 1, 255, 256, 1 << 32}
	keys := make([][]byte, len(seqs))
	for i, seq := range seqs {
		keys[i] = MutationLogKey("ns", seq)
	}
	sorted := make([][]byte, len(keys))
	copy(sorted, keys)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	for i := range keys {
		if !bytes.Equal(keys[i], sorted[i]) {
			t.Fatalf("MutationLogKey sequences not in lexicographic order: %v", seqs)
		}
	}
}

func TestMutationLogKey_CarriesMutationLogPrefix(t *testing.T) {
	if !bytes.HasPrefix(MutationLogKey("ns", 3), MutationLogPrefix("ns")) {
		t.Fatal("MutationLogKey does not carry MutationLogPrefix")
	}
	if bytes.HasPrefix(MutationLogKey("other", 3), MutationLogPrefix("ns")) {
		t.Fatal("MutationLogPrefix(\"ns\") matches a different namespace's key")
	}
}

func TestKeyFamilies_NeverCollideWithEachOther(t *testing.T) {
	// The four key families (namespace, vector, metadata, mutation-log) must
	// never produce an identical key for the same namespace: an engine
	// deleting "every key under this prefix" for one family must never
	// silently also match another's.
	ns := "docs"
	keys := [][]byte{
		NamespaceKey(ns),
		VectorKey(ns, 1),
		MetadataKey(ns, "f", "v", 1),
		MutationLogKey(ns, 1),
	}
	for i := range keys {
		for j := range keys {
			if i == j {
				continue
			}
			if bytes.Equal(keys[i], keys[j]) {
				t.Fatalf("key family %d collides with family %d: %q", i, j, keys[i])
			}
		}
	}
}
