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

package vlog

import "testing"

func TestIsTableKey(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
		want bool
	}{
		{"a head record", []byte(TablePrefix + "h/acme"), true},
		{"an event record", []byte(TablePrefix + "e/acme/1"), true},
		{"a named log's head", logHeadKey(LogID{TenantID: "acme", Database: "db", Log: "l"}), true},
		{"a named log's tree node", nodeKey(LogID{TenantID: "acme", Database: "db", Log: "l"}, 3, 9), true},
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
	key := EventKey("acme", 42)
	seq, ok := EventSeq(key, "acme")
	if !ok || seq != 42 {
		t.Fatalf("EventSeq(%q, acme) = (%d, %v), want (42, true)", key, seq, ok)
	}

	if _, ok := EventSeq(key, "widgets"); ok {
		t.Fatal("eventSeq matched a key under a different tenant's prefix")
	}
	if _, ok := EventSeq([]byte("too-short"), "acme"); ok {
		t.Fatal("eventSeq matched a key with no tenant prefix at all")
	}
	if _, ok := EventSeq(append(EventRangeStart("acme", 0), 'x'), "acme"); ok {
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

func TestParseLogPrefix(t *testing.T) {
	id := LogID{TenantID: "acme", Database: "db", Log: "l"}
	got, prefix, ok := parseLogPrefix(entryKey(id, 7))
	if !ok || got != id || string(prefix) != string(logPrefix(id)) {
		t.Fatalf("parseLogPrefix(entry key) = %v, %q, %v, want %v and its prefix", got, prefix, ok, id)
	}
	for name, k := range map[string]string{
		"audit table key":   TablePrefix + "h/acme",
		"two segments":      LogTablePrefix + "acme/db",
		"empty log segment": LogTablePrefix + "acme/db//h",
		"bare prefix":       LogTablePrefix,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := parseLogPrefix([]byte(k)); ok {
				t.Fatalf("parseLogPrefix(%q) accepted it", k)
			}
		})
	}
}

func TestDecodeLogHead(t *testing.T) {
	h := make([]byte, hashSize)
	for name, tc := range map[string]struct {
		b    []byte
		want bool
	}{
		"empty log":            {b: encodeLogHead(0, 0, nil), want: true},
		"size 5, two hashes":   {b: encodeLogHead(5, 2, [][]byte{h, h}), want: true},
		"short fixed part":     {b: make([]byte, logHeadFixedSize-1)},
		"too few hashes":       {b: encodeLogHead(5, 0, [][]byte{h})},
		"too many hashes":      {b: encodeLogHead(4, 0, [][]byte{h, h})},
		"watermark past size":  {b: encodeLogHead(4, 5, [][]byte{h})},
		"truncated final hash": {b: encodeLogHead(1, 0, [][]byte{h})[:logHeadFixedSize+hashSize-1]},
	} {
		t.Run(name, func(t *testing.T) {
			size, lo, hashes, ok := decodeLogHead(tc.b)
			if ok != tc.want {
				t.Fatalf("decodeLogHead ok = %v, want %v", ok, tc.want)
			}
			if ok && string(encodeLogHead(size, lo, hashes)) != string(tc.b) {
				t.Fatal("decode then encode did not round-trip")
			}
		})
	}
}

func TestLogID_String(t *testing.T) {
	if got := (LogID{TenantID: "acme", Database: "db", Log: "l"}).String(); got != "acme/db/l" {
		t.Fatalf("String() = %q, want acme/db/l", got)
	}
	if got := (LogID{Database: "db", Log: "l"}).String(); got != "/db/l" {
		t.Fatalf("String() of the default tenant = %q, want /db/l", got)
	}
}
