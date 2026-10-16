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

package statemachine

import (
	"bytes"
	"errors"
	"testing"
)

func collectRecords(t *testing.T, b Backend) (keys, values []string) {
	t.Helper()
	err := b.Records(func(key, value []byte) error {
		keys = append(keys, string(key))
		values = append(values, string(value))
		return nil
	})
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	return keys, values
}

func TestBackendRecords_AscendingOrderEveryBackend(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			b := nb()
			t.Cleanup(func() { _ = b.Close() })
			for _, k := range []string{"z", "\x01\xff", "a/b", "a", "m"} {
				if err := b.Put([]byte(k), []byte("v-"+k)); err != nil {
					t.Fatal(err)
				}
			}
			keys, values := collectRecords(t, b)
			want := []string{"\x01\xff", "a", "a/b", "m", "z"}
			if len(keys) != len(want) {
				t.Fatalf("keys = %q, want %q", keys, want)
			}
			for i := range want {
				if keys[i] != want[i] || values[i] != "v-"+want[i] {
					t.Fatalf("record %d = %q=%q, want %q=%q", i, keys[i], values[i], want[i], "v-"+want[i])
				}
			}
		})
	}
}

func TestBackendRecords_DeletedKeysAbsentEveryBackend(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			b := nb()
			t.Cleanup(func() { _ = b.Close() })
			_ = b.Put([]byte("keep"), []byte("1"))
			_ = b.Put([]byte("gone"), []byte("2"))
			if err := b.Delete([]byte("gone")); err != nil {
				t.Fatal(err)
			}
			keys, _ := collectRecords(t, b)
			if len(keys) != 1 || keys[0] != "keep" {
				t.Fatalf("keys = %q, want [keep]", keys)
			}
		})
	}
}

func TestBackendRecords_StopsAtVisitorErrorEveryBackend(t *testing.T) {
	stop := errors.New("stop")
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			b := nb()
			t.Cleanup(func() { _ = b.Close() })
			_ = b.Put([]byte("a"), []byte("1"))
			_ = b.Put([]byte("b"), []byte("2"))
			calls := 0
			err := b.Records(func(_, _ []byte) error {
				calls++
				return stop
			})
			if !errors.Is(err, stop) || calls != 1 {
				t.Fatalf("err = %v after %d calls, want stop after 1", err, calls)
			}
		})
	}
}

func TestBackendGetHash_MatchesRecordsEveryBackend(t *testing.T) {
	// GetHash is xxhash over Records; two backends holding the same records
	// agree, whatever their layout.
	var hashes []uint64
	for name, nb := range leaseBackends() {
		b := nb()
		for _, k := range []string{"b", "a", "c"} {
			_ = b.Put([]byte(k), []byte(k+k))
		}
		h, err := b.GetHash()
		_ = b.Close()
		if err != nil {
			t.Fatalf("%s: GetHash: %v", name, err)
		}
		hashes = append(hashes, h)
	}
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Fatalf("backends disagree on GetHash: %x", hashes)
		}
	}
}

func TestVisitSorted_OrdersAndStops(t *testing.T) {
	recs := []kvRecord{{key: "b", value: []byte("2")}, {key: "a", value: []byte("1")}, {key: "c"}}
	var got []string
	stop := errors.New("stop")
	err := visitSorted(recs, func(key, _ []byte) error {
		got = append(got, string(key))
		if bytes.Equal(key, []byte("b")) {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want stop", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("visited %q, want [a b]", got)
	}
}

func TestVisitSorted_Empty(t *testing.T) {
	if err := visitSorted(nil, func(_, _ []byte) error {
		t.Fatal("visitor called on an empty set")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestXXHashRecords_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	h, err := xxhashRecords(func(RecordVisitor) error { return want })
	if !errors.Is(err, want) || h != 0 {
		t.Fatalf("(%x, %v), want (0, boom)", h, err)
	}
}

func TestXXHashRecords_SeparatesKeyFromValueOrder(t *testing.T) {
	one := func(fn RecordVisitor) error { return fn([]byte("a"), []byte("b")) }
	two := func(fn RecordVisitor) error { return fn([]byte("b"), []byte("a")) }
	h1, _ := xxhashRecords(one)
	h2, _ := xxhashRecords(two)
	if h1 == h2 {
		t.Fatal("swapping key and value did not change the hash")
	}
}
