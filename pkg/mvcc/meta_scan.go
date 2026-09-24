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

package mvcc

import (
	"bytes"
	"encoding/binary"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// Range and prefix scans over the metadata namespace
// ---------------------------------------------------------------------------
//
// A METADATA KEY SORTS BY LENGTH FIRST (EncodeMetaKey: prefix byte, uint64
// length, user key), so the user keys of one prefix are not one contiguous
// run of the namespace. They are one contiguous run PER KEY LENGTH: within a
// single length the keys sort by their bytes. A scan that walked the whole
// namespace and filtered paid for every key in the store to find a handful,
// which is what made a 3,648-record listing cost seconds on a store of
// hundreds of thousands of keys (go-quicbaas, 2026-09-23).
//
// ForEachMetaKeyInRange seeks instead. For each key length present it seeks
// to the first key of that length at or above start, reads the length's run
// until it leaves [start, end), and seeks on to the next length. The cost is
// one seek per distinct key length plus the matching keys, not the store.

// ForEachMetaKeyInRange calls visit, in (length, key) order, for every user
// key whose metadata record the iterator can reach and that lies in
// [start, end). A nil end is unbounded above, as in IsInRange. visit returns
// false to stop. The iterator must be bounded to the metadata namespace
// (MetaLowerBound, MetaUpperBound); records that do not decode are skipped,
// as the full scan skipped them.
func ForEachMetaKeyInRange(iter storage.Iterator, start, end []byte, visit func(userKey []byte) bool) error {
	// Seek's answer, not Valid alone, says whether anything is at or beyond
	// the target: an iterator that finds nothing may still be positioned on
	// the key it held before.
	keyLen := 0
	for iter.Seek(metaSeekKey(keyLen, lowerBoundAtLen(start, keyLen))) && iter.Valid() {
		landed, ok := metaKeyLen(iter.Key())
		if !ok || landed < keyLen {
			// Outside the namespace, or a key the seek could not have
			// landed on: nothing at or beyond the target remains.
			break
		}
		if landed > keyLen {
			// No key of this length is in range; the next length present
			// is where the iterator landed.
			keyLen = landed
			continue
		}
		for ; iter.Valid(); iter.Next() {
			encoded := iter.Key()
			if length, ok := metaKeyLen(encoded); !ok || length != keyLen {
				break
			}
			userKey, err := DecodeMetaKey(encoded)
			if err != nil {
				continue
			}
			if bytes.Compare(userKey, start) < 0 {
				// Only a key equal to start[:keyLen] can sort here; it is
				// shorter than start and so below it.
				continue
			}
			if end != nil && bytes.Compare(userKey, end) >= 0 {
				// The rest of this length's run sorts higher still.
				break
			}
			if !visit(userKey) {
				return iter.Error()
			}
		}
		keyLen++
	}
	return iter.Error()
}

// ForEachMetaKeyWithPrefix calls visit for every user key that starts with
// prefix, in (length, key) order. An empty prefix visits every key.
func ForEachMetaKeyWithPrefix(iter storage.Iterator, prefix []byte, visit func(userKey []byte) bool) error {
	return ForEachMetaKeyInRange(iter, prefix, prefixEnd(prefix), visit)
}

// metaSeekKey is the encoded metadata key a seek for keys of length keyLen at
// or above lower targets: the namespace byte, the length, then lower.
func metaSeekKey(keyLen int, lower []byte) []byte {
	buf := make([]byte, 1+8+len(lower))
	buf[0] = prefixMeta
	binary.BigEndian.PutUint64(buf[1:9], uint64(keyLen)) //nolint:gosec // G115: keyLen is a non-negative key length
	copy(buf[9:], lower)
	return buf
}

// metaKeyLen reports the user-key length an encoded metadata key declares.
func metaKeyLen(encoded []byte) (int, bool) {
	if len(encoded) < 9 || encoded[0] != prefixMeta {
		return 0, false
	}
	return int(binary.BigEndian.Uint64(encoded[1:9])), true //nolint:gosec // G115: bounded by the encoded key's own length
}

// lowerBoundAtLen is the smallest byte string of length keyLen or shorter
// that no in-range key of length keyLen sorts below: start itself when it
// fits, otherwise start's first keyLen bytes.
func lowerBoundAtLen(start []byte, keyLen int) []byte {
	if len(start) <= keyLen {
		return start
	}
	return start[:keyLen]
}

// prefixEnd is the smallest key greater than every key starting with prefix,
// or nil when there is none (an empty prefix, or one made only of 0xFF).
func prefixEnd(prefix []byte) []byte {
	end := append([]byte{}, prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xFF {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}
