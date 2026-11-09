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

package objstore

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math"
)

// Object naming. A storage key is arbitrary bytes, but a provider object name
// is a UTF-8 path of at most objcommon.MaxKeyLength bytes, and a filesystem
// provider additionally limits each path component to 255 bytes. Every key is
// therefore stored under one of two names:
//
//   - direct: directPrefix + the key's lowercase hex, cut into segments of at
//     most segmentLen characters joined by '/', with leafTerminator appended
//     to the last. Hex preserves byte order, and the hex of a byte prefix is a
//     string prefix of the hex of every key it prefixes, so List narrows on
//     the provider. The terminator keeps a leaf from ever sharing a name with
//     the directory a longer key's segments need.
//   - hashed: hashedPrefix + the lowercase hex SHA-256 of the key, used when
//     the direct name would exceed objcommon.MaxKeyLength, less the length of
//     the engine's object prefix (keys longer than about 500 bytes). The object body is a record carrying the key itself,
//     uvarint(len(key)) || key || value, and a read verifies the stored key.
//
// A name under either prefix that is not the canonical name of some key is
// not a record this engine wrote (a provider's in-flight temporary file, for
// one) and is skipped by every listing.
const (
	directPrefix   = "k/"
	hashedPrefix   = "h/"
	leafTerminator = '_'
	segmentLen     = 128

	// formatMarkerName is the object New checks before opening a provider:
	// see FormatMarkerMissingError and FormatVersionMismatchError. It lies
	// outside both key prefixes, so no key can name it.
	formatMarkerName = "qrdb-objstore-format"

	// formatVersion is this build's object layout. Bump it, and
	// FormatVersionMismatchError refuses every store written by the previous
	// one. Version 1 is the unmarked layout that named each object with its
	// key verbatim.
	formatVersion = "2"
)

// hashedNameLen is the length of every hashed object name.
const hashedNameLen = len(hashedPrefix) + 2*sha256.Size

// directNameLen returns the length of the direct name of an n-byte key.
func directNameLen(n int) int {
	hexLen := 2 * n
	segments := (hexLen + segmentLen - 1) / segmentLen
	return len(directPrefix) + hexLen + (segments - 1) + 1
}

// objectName returns the provider object name key is stored under, and
// whether that name is the hashed form whose body is a key-carrying record.
// limit is the longest name the engine may write: objcommon.MaxKeyLength less
// the length of its object prefix (see WithObjectPrefix).
func objectName(key []byte, limit int) (string, bool) {
	if directNameLen(len(key)) > limit {
		sum := sha256.Sum256(key)
		return hashedPrefix + hex.EncodeToString(sum[:]), true
	}
	name := appendSegmentedHex([]byte(directPrefix), key)
	return string(append(name, leafTerminator)), false
}

// directListPrefix returns the provider prefix every direct name of a key
// beginning with prefix starts with, and false when no direct name can: a
// prefix whose own encoding already exceeds the provider's name limit only
// prefixes keys that are stored hashed. limit is objectName's.
func directListPrefix(prefix []byte, limit int) (string, bool) {
	p := appendSegmentedHex([]byte(directPrefix), prefix)
	if len(p) > limit {
		return "", false
	}
	return string(p), true
}

// appendSegmentedHex appends the lowercase hex of b to dst, with a '/' after
// every segmentLen characters that are followed by more.
func appendSegmentedHex(dst, b []byte) []byte {
	h := hex.AppendEncode(nil, b)
	for i := 0; i < len(h); i += segmentLen {
		if i > 0 {
			dst = append(dst, '/')
		}
		dst = append(dst, h[i:min(i+segmentLen, len(h))]...)
	}
	return dst
}

// decodeDirectName returns the key a direct object name encodes, and false
// for any name that is not exactly the direct name objectName gives a key.
func decodeDirectName(name string, limit int) ([]byte, bool) {
	if len(name) <= len(directPrefix)+1 || name[:len(directPrefix)] != directPrefix ||
		name[len(name)-1] != leafTerminator {
		return nil, false
	}
	body := name[len(directPrefix) : len(name)-1]
	h := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		if body[i] != '/' {
			h = append(h, body[i])
		}
	}
	if len(h)%2 != 0 {
		return nil, false
	}
	key := make([]byte, hex.DecodedLen(len(h)))
	if _, err := hex.Decode(key, h); err != nil {
		return nil, false
	}
	// Re-encoding rejects every non-canonical spelling: uppercase hex,
	// misplaced separators, or a key long enough to be stored hashed.
	if canonical, hashed := objectName(key, limit); hashed || canonical != name {
		return nil, false
	}
	return key, true
}

// isHashedName reports whether name is shaped like a hashed object name.
func isHashedName(name string) bool {
	if len(name) != hashedNameLen || name[:len(hashedPrefix)] != hashedPrefix {
		return false
	}
	for i := len(hashedPrefix); i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hashedRecord returns the body stored under a hashed name: the key, framed
// by its uvarint length, followed by the value.
func hashedRecord(key, value []byte) io.Reader {
	header := binary.AppendUvarint(make([]byte, 0, binary.MaxVarintLen64+len(key)), uint64(len(key)))
	return io.MultiReader(bytes.NewReader(append(header, key...)), bytes.NewReader(value))
}

// readHashedRecord parses the record stored under the hashed name, returning
// the key it carries and, when withValue is set, its value. The key buffer
// grows only as bytes actually arrive, so a corrupt length cannot force a
// large allocation, and the key must hash to name.
func readHashedRecord(name string, r io.Reader, withValue bool, limit int) ([]byte, []byte, error) {
	src := &sourceErrReader{r: r}
	br := bufio.NewReader(src)
	n, err := binary.ReadUvarint(br)
	if src.err != nil {
		return nil, nil, src.err
	}
	if err != nil || n > math.MaxInt64 {
		return nil, nil, &RecordCorruptError{Name: name, Reason: "unreadable key length"}
	}
	key, err := io.ReadAll(io.LimitReader(br, int64(n)))
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(key)) != n {
		return nil, nil, &RecordCorruptError{Name: name, Reason: "record shorter than its key length"}
	}
	// The name is the stored key's hash, so a record that does not hash to
	// the name it was read from is misplaced or corrupt, never a usable key.
	if canonical, hashed := objectName(key, limit); !hashed || canonical != name {
		return nil, nil, &KeyMismatchError{Name: name}
	}
	if !withValue {
		return key, nil, nil
	}
	value, err := io.ReadAll(br)
	if err != nil {
		return nil, nil, err
	}
	return key, value, nil
}

// sourceErrReader records the first error its source returns other than
// io.EOF, so a provider read fault is reported as itself rather than as the
// malformed record a short read would otherwise look like.
type sourceErrReader struct {
	r   io.Reader
	err error
}

func (s *sourceErrReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && err != io.EOF && s.err == nil {
		s.err = err
	}
	return n, err
}
