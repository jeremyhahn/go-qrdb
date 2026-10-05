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

import "strings"

// The file backend maps each key to a directory under the engine directory,
// holding a sentinel file (valueFileName) that carries the value (see
// file.go). Keys are arbitrary []byte per the storage.Storage contract — the
// same contract the memory and raft backends honour — so they may contain
// bytes that are not valid in a filesystem path (NUL, control bytes, high
// bytes), that collide with path semantics ("." / ".." segments), or that
// collide with ANOTHER key's path once '/' is read as a directory separator
// (a key "dir" and a key "dir/child" both name something under "dir").
// Storing a key verbatim as a single file (the historical behaviour)
// produced "invalid argument" on open for the first class — e.g. a uint64
// identity key like `__sys:users/id/\x00\x00\x00\x00\x00\x00\x10\x01` — and a
// rename failure for the second: "dir" and "dir/child" both resolve under
// "dir", and whichever is written second collides with what the first
// created there (go-qrdb#592).
//
// encodeKeyToPath / decodePathToKey are an exact bijection between key bytes
// and a '/'-delimited relative path:
//
//   - '/' bytes delimit directory levels, so the on-disk layout mirrors the
//     key hierarchy and the store stays browsable/debuggable.
//   - Filesystem-safe printable ASCII passes through verbatim, so structured
//     text keys (`__sys:users/id/...`) remain human-readable; only the binary
//     bytes of a segment are escaped (`%00%00%00%00%00%00%10%01`).
//   - Every other byte, plus '%' (the escape marker) and a leading '.' of any
//     segment (so no segment is "." or ".."), is percent-encoded as %XX.
//   - A '/' byte is escaped, rather than read as a directory separator, when
//     treating it as one would produce an EMPTY segment: a leading '/' (the
//     first byte of the key), a trailing '/' (the last byte of the key), or a
//     second consecutive '/'. filepath.Clean collapses an empty segment into
//     its neighbour — "a/" and "a" would name the identical directory — which
//     is exactly the kind of collision this codec exists to rule out. Escaping
//     it removes the empty segment from the path syntax entirely, so Clean has
//     nothing to collapse.
//
// Because every segment this codec ever emits is non-empty and never "." or
// "..", filepath.Join(root, encodeKeyToPath(key)) never invokes any of
// filepath.Clean's normalisation rules — it is exactly root + "/" + the
// encoded string. That, together with encode being decode's left inverse
// (proven by TestKeyPathCodecRoundTrip over arbitrary byte strings), is what
// makes the mapping from distinct keys to distinct directories total: two
// different keys can never name the same path, so file.go's "one directory
// per key, holding both this key's value and its children's subdirectories"
// layout (go-qrdb#592) never collides regardless of which keys share a '/'
// prefix relationship or in which order they are written.
//
// The mapping is deterministic and order-independent of the on-disk layout:
// the iterator sorts by the decoded key bytes, not by directory order.

const hexUpper = "0123456789ABCDEF"

// isSafeKeyByte reports whether c may appear verbatim in a path segment.
// Safe = printable ASCII excluding '%' (the escape marker) and '/' (the
// segment separator, handled explicitly by the codec).
func isSafeKeyByte(c byte) bool {
	return c > 0x20 && c < 0x7f && c != '%' && c != '/'
}

// encodeKeyToPath renders an arbitrary key as a filesystem-safe, '/'-delimited
// relative path. It is the inverse of decodePathToKey.
func encodeKeyToPath(key []byte) string {
	var b strings.Builder
	b.Grow(len(key) + 8)
	segStart := true
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c == '/' && i != 0 && i != len(key)-1 && key[i+1] != '/':
			// A real separator: it is not the first or last byte of the key,
			// and it is not immediately followed by another '/'. Every one of
			// those three cases would otherwise produce an empty path segment
			// — see the package doc.
			b.WriteByte('/')
			segStart = true
			continue
		case isSafeKeyByte(c) && (!segStart || c != '.'):
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&0x0f])
		}
		segStart = false
	}
	return b.String()
}

// decodePathToKey reverses encodeKeyToPath. rel must use '/' separators (the
// caller converts OS separators via filepath.ToSlash). Any '%XX' triplet is
// decoded to its byte; a stray '%' with fewer than two following hex digits is
// treated verbatim (defensive — encodeKeyToPath never emits one).
func decodePathToKey(rel string) []byte {
	out := make([]byte, 0, len(rel))
	for i := 0; i < len(rel); i++ {
		c := rel[i]
		if c == '%' && i+2 < len(rel) {
			hi, ok1 := unhex(rel[i+1])
			lo, ok2 := unhex(rel[i+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// unhex parses a single hex digit.
func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}
