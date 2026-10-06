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

package audit

import "testing"

func TestIsTableKey(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
		want bool
	}{
		{"a head record", []byte(TablePrefix + "h/acme"), true},
		{"an event record", []byte(TablePrefix + "e/acme/1"), true},
		{"the lease table's prefix is a different table", []byte("\x00qrdb.lease/l/1"), false},
		{"an ordinary user key", []byte("acme/widget"), false},
		{"empty", []byte{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTableKey(tt.key); got != tt.want {
				t.Fatalf("IsTableKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestEventSeq(t *testing.T) {
	key := eventKey("acme", 42)
	seq, ok := eventSeq(key, "acme")
	if !ok || seq != 42 {
		t.Fatalf("eventSeq(%q, acme) = (%d, %v), want (42, true)", key, seq, ok)
	}

	if _, ok := eventSeq(key, "widgets"); ok {
		t.Fatal("eventSeq matched a key under a different tenant's prefix")
	}
	if _, ok := eventSeq([]byte("too-short"), "acme"); ok {
		t.Fatal("eventSeq matched a key with no tenant prefix at all")
	}
	if _, ok := eventSeq(append(eventRangeStart("acme", 0), 'x'), "acme"); ok {
		t.Fatal("eventSeq matched a key with a sequence of the wrong width")
	}
}

func TestDecodeHead(t *testing.T) {
	var digest [32]byte
	digest[0] = 0xAB
	encoded := encodeHead(7, 3, digest)

	seq, lo, got, ok := decodeHead(encoded)
	if !ok || seq != 7 || lo != 3 || got != digest {
		t.Fatalf("decodeHead round trip = (%d, %d, %x, %v), want (7, 3, %x, true)", seq, lo, got, ok, digest)
	}

	if _, _, _, ok := decodeHead([]byte("too-short")); ok {
		t.Fatal("decodeHead accepted a record of the wrong width")
	}
	if _, _, _, ok := decodeHead(nil); ok {
		t.Fatal("decodeHead accepted a nil record")
	}
}
