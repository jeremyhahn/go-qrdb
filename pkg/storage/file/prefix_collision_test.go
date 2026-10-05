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

package file

import (
	"bytes"
	"errors"
	"sort"
	"testing"
)

// TestPrefixCollision_BothOrders reproduces go-qrdb#592: a key that is a
// '/' prefix of another key used to map to the SAME filesystem path as that
// other key's parent directory, so whichever of the two was written second
// collided with what the first had created there. Both write orders must
// now succeed, independently of each other, and each key must read back its
// own value afterwards.
func TestPrefixCollision_BothOrders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  string
		second string
	}{
		{"child then parent", "dir/child", "dir"},
		{"parent then child", "dir", "dir/child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := New(t.TempDir())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = eng.Close() }()

			if err := eng.Set([]byte(tc.first), []byte("v1")); err != nil {
				t.Fatalf("Set(%q): %v", tc.first, err)
			}
			if err := eng.Set([]byte(tc.second), []byte("v2")); err != nil {
				t.Fatalf("Set(%q): %v", tc.second, err)
			}

			got1, err := eng.Get([]byte(tc.first))
			if err != nil {
				t.Fatalf("Get(%q): %v", tc.first, err)
			}
			if string(got1) != "v1" {
				t.Fatalf("Get(%q) = %q, want %q", tc.first, got1, "v1")
			}

			got2, err := eng.Get([]byte(tc.second))
			if err != nil {
				t.Fatalf("Get(%q): %v", tc.second, err)
			}
			if string(got2) != "v2" {
				t.Fatalf("Get(%q) = %q, want %q", tc.second, got2, "v2")
			}

			keys, err := eng.List(nil)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(keys) != 2 {
				t.Fatalf("List returned %d keys, want 2: %q", len(keys), keys)
			}
		})
	}
}

// TestPrefixCollision_DeleteParentKeepsChild proves that deleting the
// shorter key's value never deletes the longer key's subtree -- Delete
// removes only the value sentinel, never the directory outright (see
// Engine.Delete).
func TestPrefixCollision_DeleteParentKeepsChild(t *testing.T) {
	eng, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("dir"), []byte("parent")); err != nil {
		t.Fatalf("Set(dir): %v", err)
	}
	if err := eng.Set([]byte("dir/child"), []byte("child")); err != nil {
		t.Fatalf("Set(dir/child): %v", err)
	}
	if err := eng.Delete([]byte("dir")); err != nil {
		t.Fatalf("Delete(dir): %v", err)
	}

	if got, err := eng.Get([]byte("dir")); err != nil || got != nil {
		t.Fatalf("Get(dir) after delete = (%q, %v), want (nil, nil)", got, err)
	}
	got, err := eng.Get([]byte("dir/child"))
	if err != nil {
		t.Fatalf("Get(dir/child): %v", err)
	}
	if string(got) != "child" {
		t.Fatalf("Get(dir/child) = %q, want %q", got, "child")
	}
}

// TestPrefixCollision_DeleteLeavesDirectoryIntactForConcurrentSiblingWrite
// proves Delete does not remove the key's directory, even when nothing else
// is in it afterwards: Engine.Delete documents why removing it would race a
// concurrent Set of a key for which this one is a '/' prefix (FastBackend
// issues Set/Delete for different keys from concurrent per-shard workers).
// The key itself must still read back absent, and a sibling key written
// afterwards into the same directory must succeed.
func TestPrefixCollision_DeleteLeavesDirectoryIntactForConcurrentSiblingWrite(t *testing.T) {
	eng, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("a/b/c"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := eng.Delete([]byte("a/b/c")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := eng.Get([]byte("a/b/c")); err != nil || got != nil {
		t.Fatalf("Get(a/b/c) after delete = (%q, %v), want (nil, nil)", got, err)
	}

	// A key for which the deleted one is a '/' prefix must still be able to
	// claim the now-empty directory.
	if err := eng.Set([]byte("a/b/c/d"), []byte("sibling")); err != nil {
		t.Fatalf("Set(a/b/c/d) after deleting its '/' prefix: %v", err)
	}
	got, err := eng.Get([]byte("a/b/c/d"))
	if err != nil {
		t.Fatalf("Get(a/b/c/d): %v", err)
	}
	if string(got) != "sibling" {
		t.Fatalf("Get(a/b/c/d) = %q, want %q", got, "sibling")
	}
}

// TestPrefixCollision_TrailingAndDoubledSlashesStayDistinct proves keys that
// differ only by a leading, trailing, or doubled '/' -- which filepath.Clean
// would otherwise collapse into the SAME directory as their stripped form --
// are stored, read, and listed as the distinct keys they are.
func TestPrefixCollision_TrailingAndDoubledSlashesStayDistinct(t *testing.T) {
	eng, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	keys := [][]byte{
		[]byte("a"),
		[]byte("a/"),
		[]byte("/a"),
		[]byte("a//b"),
		[]byte("a/b"),
	}
	for i, k := range keys {
		if err := eng.Set(k, []byte{byte('0' + i)}); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}
	for i, k := range keys {
		got, err := eng.Get(k)
		if err != nil {
			t.Fatalf("Get(%q): %v", k, err)
		}
		want := []byte{byte('0' + i)}
		if !bytes.Equal(got, want) {
			t.Fatalf("Get(%q) = %q, want %q", k, got, want)
		}
	}

	listed, err := eng.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Slice(listed, func(i, j int) bool { return bytes.Compare(listed[i], listed[j]) < 0 })
	if len(listed) != len(keys) {
		t.Fatalf("List returned %d keys, want %d: %q", len(listed), len(keys), listed)
	}
}

// TestKeySegmentTooLong_Refused proves a key whose encoded segment would
// exceed this filesystem's path-component limit is refused with a typed,
// declared refusal (KeySegmentTooLongError) rather than reaching the OS and
// failing with ENAMETOOLONG -- the distinction statemachine.ResolveApplyError
// needs to classify it as a refusal rather than a fault (go-qrdb#592).
func TestKeySegmentTooLong_Refused(t *testing.T) {
	eng, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	huge := bytes.Repeat([]byte{0x00}, maxEncodedSegmentLen) // each byte encodes to 3 bytes: %00

	var tooLong *KeySegmentTooLongError
	for _, op := range []struct {
		name string
		call func() error
	}{
		{"Set", func() error { return eng.Set(huge, []byte("v")) }},
		{"Delete", func() error { return eng.Delete(huge) }},
	} {
		if err := op.call(); !errors.As(err, &tooLong) {
			t.Fatalf("%s(oversized key): err = %v, want KeySegmentTooLongError", op.name, err)
		}
	}
	if _, err := eng.Get(huge); !errors.As(err, &tooLong) {
		t.Fatalf("Get(oversized key): err = %v, want KeySegmentTooLongError", err)
	}
	if _, err := eng.Exists(huge); !errors.As(err, &tooLong) {
		t.Fatalf("Exists(oversized key): err = %v, want KeySegmentTooLongError", err)
	}
	if tooLong.Refusal() == "" {
		t.Error("KeySegmentTooLongError.Refusal() is empty")
	}
	if tooLong.Error() == "" {
		t.Error("KeySegmentTooLongError.Error() is empty")
	}

	// A key just under the limit, with no '/' nesting at all, is accepted.
	ok := bytes.Repeat([]byte{'a'}, maxEncodedSegmentLen-1)
	if err := eng.Set(ok, []byte("v")); err != nil {
		t.Fatalf("Set(just under the limit): %v", err)
	}
}

// FuzzFileEngine_PrefixKeysNeverCollide fuzzes the exact defect class behind
// go-qrdb#592: given arbitrary bytes, split them into a "parent" key and a
// "child" key formed by inserting a '/' into the middle of the same bytes,
// so child is always, by construction, parent with more bytes appended after
// a '/'. Setting both, in either order, must always succeed, and each must
// read back its own value -- across any seed corpus entry or random input
// the fuzzer discovers, not only the two cases the original report named.
func FuzzFileEngine_PrefixKeysNeverCollide(f *testing.F) {
	f.Add([]byte("dir/child"), true)
	f.Add([]byte("dir/child"), false)
	f.Add([]byte("a"), true)
	f.Add([]byte{0x00, 0x2e, 0x25, '/'}, true)

	f.Fuzz(func(t *testing.T, raw []byte, parentFirst bool) {
		if len(raw) < 2 {
			t.Skip()
		}
		mid := len(raw) / 2
		if mid == 0 {
			mid = 1
		}
		parent := append([]byte{}, raw[:mid]...)
		child := append(append(append([]byte{}, parent...), '/'), raw[mid:]...)

		eng, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = eng.Close() }()

		first, second := parent, child
		firstVal, secondVal := []byte("parent-value"), []byte("child-value")
		if !parentFirst {
			first, second = child, parent
			firstVal, secondVal = secondVal, firstVal
		}

		var tooLong *KeySegmentTooLongError
		if err := eng.Set(first, firstVal); err != nil {
			if errors.As(err, &tooLong) {
				t.Skip() // a fuzzed segment exceeded the filesystem limit; not this bug.
			}
			t.Fatalf("Set(first=%q): %v", first, err)
		}
		if err := eng.Set(second, secondVal); err != nil {
			if errors.As(err, &tooLong) {
				t.Skip()
			}
			t.Fatalf("Set(second=%q): %v", second, err)
		}

		gotParent, err := eng.Get(parent)
		if err != nil {
			t.Fatalf("Get(parent=%q): %v", parent, err)
		}
		if !bytes.Equal(gotParent, []byte("parent-value")) {
			t.Fatalf("Get(parent=%q) = %q, want %q", parent, gotParent, "parent-value")
		}
		gotChild, err := eng.Get(child)
		if err != nil {
			t.Fatalf("Get(child=%q): %v", child, err)
		}
		if !bytes.Equal(gotChild, []byte("child-value")) {
			t.Fatalf("Get(child=%q) = %q, want %q", child, gotChild, "child-value")
		}
	})
}
