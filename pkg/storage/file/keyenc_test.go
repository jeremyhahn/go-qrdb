// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package file

import (
	"bytes"
	"testing"
)

// TestKeyPathCodecRoundTrip verifies encodeKeyToPath/decodePathToKey are an
// exact bijection for text, structured, and binary keys — including the exact
// key shape that previously failed with "invalid argument".
func TestKeyPathCodecRoundTrip(t *testing.T) {
	cases := [][]byte{
		[]byte("simple"),
		[]byte("__sys:users/id/alice"),
		// The exact failing key: __sys:users/id/ + big-endian uint64 0x1001.
		append([]byte("__sys:users/id/"), 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x01),
		// All bytes 0x00..0xff in a binary segment.
		func() []byte {
			b := []byte("bin/")
			for i := 0; i < 256; i++ {
				b = append(b, byte(i))
			}
			return b
		}(),
		[]byte("a/./b"),   // "." segment must not become a real "." dir
		[]byte("a/../b"),  // ".." segment must not escape the root
		[]byte("pct/%/x"), // literal '%' must round-trip
		{0x2e},            // a bare "." key
		{0x2e, 0x2e},      // a bare ".." key
	}
	for _, key := range cases {
		enc := encodeKeyToPath(key)
		if got := decodePathToKey(enc); !bytes.Equal(got, key) {
			t.Errorf("round-trip mismatch\n key=%q\n enc=%q\n got=%q", key, enc, got)
		}
		// No path segment may be "." or ".." (would break the filesystem layout).
		for _, seg := range bytes.Split([]byte(enc), []byte("/")) {
			if string(seg) == "." || string(seg) == ".." {
				t.Errorf("encoded path %q contains reserved segment %q", enc, seg)
			}
			for _, c := range seg {
				if c == 0x00 {
					t.Errorf("encoded path %q contains NUL byte", enc)
				}
			}
		}
	}
}

// TestEngineBinaryKey stores and retrieves the previously-failing binary key
// through the real engine (Set/Get/Has/iterate/Delete), proving the file
// backend now honours the []byte-key contract like memory/raft.
func TestEngineBinaryKey(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = e.Close() }()

	key := append([]byte("__sys:users/id/"), 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x01)
	val := []byte("admin-user-record")

	if err := e.Set(key, val); err != nil {
		t.Fatalf("Set binary key: %v", err)
	}
	got, err := e.Get(key)
	if err != nil {
		t.Fatalf("Get binary key: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("Get returned %q, want %q", got, val)
	}

	// The iterator must reconstruct the exact binary key.
	it := newIterator(e, nil)
	defer func() { _ = it.Close() }()
	found := false
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		if bytes.Equal(it.Key(), key) {
			found = true
			if !bytes.Equal(it.Value(), val) {
				t.Fatalf("iterator value %q, want %q", it.Value(), val)
			}
		}
	}
	if !found {
		t.Fatalf("iterator did not reconstruct the binary key")
	}

	if err := e.Delete(key); err != nil {
		t.Fatalf("Delete binary key: %v", err)
	}
	if got, _ := e.Get(key); got != nil {
		t.Fatalf("key still present after Delete")
	}
}
