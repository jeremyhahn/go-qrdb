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

package protocol

import "strconv"

// MaxClientKeyLength is the global maximum length, in bytes, of any key a
// client may name on the KV data plane (owner ruling, go-qrdb#592). It is
// checked in TWO places that must agree:
//
//   - Here, in every encoder that frames a key in a wider-than-necessary
//     field: EncodeOp and EncodeCompare frame a key in a four-byte length
//     field (their value and range-end fields need the room; the key does
//     not), so without this check a key far longer than any other opcode
//     permits would encode successfully and only be refused once replicated
//     — one write ahead of every other verb's pre-propose rejection.
//     EncodeCompareAndSwap, EncodeCompareAndDelete and go-quicraft's
//     kv.EncodePut/EncodePutIfAbsent/EncodeDelete already enforce the
//     identical bound as a consequence of their two-byte key frame
//     (`checkUint16Len`, `kv.MaxKeyLength`), not as a copy of this policy, so
//     they are not touched here.
//   - `pkg/statemachine/native_txn.go`'s `validateClientKey`, which refuses
//     it again at Apply, identically on every replica regardless of that
//     replica's `raft.sm_storage` — the same backend-independence the empty
//     and reserved-key refusals already have. A key's validity must never
//     depend on which storage backend happens to be configured, and a
//     pre-propose check alone cannot promise that: a caller that reaches a
//     state machine some other way (a raw log entry, an older or
//     non-conforming client) still has this backstop.
//
// 65,535 is not a storage limit of any particular backend — the file engine's
// hashed, content-addressed layout has no key-length limit of its own at
// all. It is a deliberate operational ceiling: a key is an identifier, and
// this is already far beyond any legitimate one.
const MaxClientKeyLength = 65535

// KeyTooLongError is returned when a key exceeds MaxClientKeyLength. It is a
// refusal of the request on its merits — every replica, and every encoder on
// every transport, refuses the identical key identically — never a storage
// fault, so it satisfies RefusalError.
type KeyTooLongError struct {
	// Length is the offending key's length in bytes.
	Length int
}

// Error returns a human-readable description of the refused key.
func (e *KeyTooLongError) Error() string {
	return "protocol: key length " + strconv.Itoa(e.Length) +
		" exceeds the maximum of " + strconv.Itoa(MaxClientKeyLength) + " bytes"
}

// Refusal declares this a refusal of the request, not a fault. See
// RefusalError.
func (e *KeyTooLongError) Refusal() string {
	return "key too long"
}

// CheckKeyLen refuses a key longer than MaxClientKeyLength, so an encoder
// whose key field is wider than that bound still holds every key to the one
// global policy rather than only to its own frame width.
func CheckKeyLen(key []byte) error {
	if len(key) > MaxClientKeyLength {
		return &KeyTooLongError{Length: len(key)}
	}
	return nil
}
