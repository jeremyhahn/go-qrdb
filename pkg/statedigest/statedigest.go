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

// Package statedigest defines the replica state digest: a SHA-256 over a
// canonical encoding of a state machine's replicated records, reported beside
// the applied index the records were read at.
//
// The encoding (version 1) is the 20-byte header "qrdb-state-digest-v1" and a
// 0x00 byte, followed by every record in strictly ascending bytewise key
// order as
//
//	u64 BE key length || key || u64 BE value length || value
//
// The applied index and the record count are not hashed; they are reported
// beside the sum. Two replicas holding the same records produce the same sum
// whatever storage backend holds them, so comparing sums taken at the same
// applied index compares the replicas' state.
//
// The package imports only the standard library so that every state machine
// package can answer a [Query] without an import cycle.
package statedigest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strconv"
)

// Header is the domain-separation prefix hashed before the first record. It
// names the encoding version, so a future encoding cannot collide with this
// one.
const Header = "qrdb-state-digest-v1"

// Query is the lookup a state machine answers with a [Digest]. It carries no
// fields: the digest always covers the whole replicated record set at the
// state machine's current applied index.
type Query struct{}

// Digest is a state machine's replicated record set reduced to a SHA-256,
// with the applied index the records were read at.
type Digest struct {
	// AppliedIndex is the Raft log index of the last entry applied to the
	// records that were hashed.
	AppliedIndex uint64
	// Records is the number of records hashed.
	Records uint64
	// Sum is the SHA-256 over the canonical encoding.
	Sum [sha256.Size]byte
}

// Hex returns Sum as 64 lowercase hexadecimal characters.
func (d Digest) Hex() string {
	return hex.EncodeToString(d.Sum[:])
}

// KeyOrderError reports a record added out of strictly ascending key order.
// It carries only the record's position, never the key or value, so it is
// safe to return across a tenant barrier.
type KeyOrderError struct {
	// Position is the zero-based position of the offending record.
	Position uint64
}

// Error implements error.
func (e *KeyOrderError) Error() string {
	return "statedigest: record " + strconv.FormatUint(e.Position, 10) +
		" is not in strictly ascending key order"
}

// Builder accumulates records into a [Digest]. The zero value is not usable;
// call [NewBuilder].
type Builder struct {
	h       hash.Hash
	last    []byte
	records uint64
	lenBuf  [8]byte
}

// NewBuilder returns a Builder with the header already hashed.
func NewBuilder() *Builder {
	b := &Builder{h: sha256.New()}
	_, _ = b.h.Write([]byte(Header))
	_, _ = b.h.Write([]byte{0})
	return b
}

// Add hashes one record. Keys must arrive in strictly ascending bytewise
// order; a key equal to or below the previous one returns a *KeyOrderError
// and leaves the builder unchanged.
func (b *Builder) Add(key, value []byte) error {
	if b.records > 0 && bytes.Compare(key, b.last) <= 0 {
		return &KeyOrderError{Position: b.records}
	}
	b.writeField(key)
	b.writeField(value)
	b.last = append(b.last[:0], key...)
	b.records++
	return nil
}

// Finish returns the digest of the records added so far, labelled with
// appliedIndex. The builder must not be used afterwards.
func (b *Builder) Finish(appliedIndex uint64) Digest {
	d := Digest{AppliedIndex: appliedIndex, Records: b.records}
	b.h.Sum(d.Sum[:0])
	return d
}

// writeField hashes a length-prefixed byte string.
func (b *Builder) writeField(p []byte) {
	binary.BigEndian.PutUint64(b.lenBuf[:], uint64(len(p)))
	_, _ = b.h.Write(b.lenBuf[:])
	_, _ = b.h.Write(p)
}
