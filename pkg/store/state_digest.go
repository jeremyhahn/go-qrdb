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

package store

import (
	"bytes"
	"encoding/binary"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// stateDigest answers a statedigest.Query for the pebble state machine. The
// read lock Update excludes is held only while the snapshot is opened; the
// scan runs against the snapshot, so apply is not held up by it.
func (s *PebbleStateMachine) stateDigest() (statedigest.Digest, error) {
	s.mu.RLock()
	if s.db == nil {
		s.mu.RUnlock()
		return statedigest.Digest{}, &NotOpenError{}
	}
	snap := s.db.NewSnapshot()
	s.mu.RUnlock()
	defer func() { _ = snap.Close() }()
	return pebbleStateDigest(snap)
}

// pebbleStateDigest is the state digest of one Pebble snapshot: every record
// SaveSnapshot would carry, with the applied index read from the same
// snapshot. Update commits the applied index in the same batch as the entry's
// writes, so the snapshot holds both or neither.
func pebbleStateDigest(snap *pebble.Snapshot) (statedigest.Digest, error) {
	iter, err := snap.NewIter(nil)
	if err != nil {
		return statedigest.Digest{}, &ReadError{Err: err}
	}
	defer func() { _ = iter.Close() }()

	b := statedigest.NewBuilder()
	var applied uint64
	value := iter.ValueAndErr
	for iter.First(); iter.Valid(); iter.Next() {
		if err := addDigestRecord(b, &applied, iter.Key(), value); err != nil {
			return statedigest.Digest{}, err
		}
	}
	if err := iter.Error(); err != nil {
		return statedigest.Digest{}, &ReadError{Err: err}
	}
	return b.Finish(applied), nil
}

// readerStateDigest is the state digest of r, exactly as pebbleStateDigest
// computes it for a Pebble snapshot. The mvcc state machine reads it from its
// published readView, so a batch Update has committed but not yet published
// is in neither its applied index nor its records.
func readerStateDigest(r storage.Reader) (statedigest.Digest, error) {
	iter := r.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	b := statedigest.NewBuilder()
	var applied uint64
	// A value read that fails reports through Error, never by a panic or a
	// nil value hashed as though it were the record's contents.
	value := func() ([]byte, error) {
		v := iter.Value()
		if err := iter.Error(); err != nil {
			return nil, err
		}
		return v, nil
	}
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		if err := addDigestRecord(b, &applied, iter.Key(), value); err != nil {
			return statedigest.Digest{}, err
		}
	}
	if err := iter.Error(); err != nil {
		return statedigest.Digest{}, &ReadError{Err: err}
	}
	return b.Finish(applied), nil
}

// addDigestRecord adds the record at key k to b, or, for the applied-index
// marker, decodes it into applied instead. A node-local key is skipped.
// value reads the record's value and is called only for a record the digest
// uses.
func addDigestRecord(b *statedigest.Builder, applied *uint64, k []byte, value func() ([]byte, error)) error {
	if bytes.Equal(k, []byte(metaKeyLastApplied)) {
		v, err := value()
		if err != nil {
			return &ReadError{Key: metaKeyLastApplied, Err: err}
		}
		*applied = decodeLastApplied(v)
		return nil
	}
	if isNodeLocalKey(k) {
		return nil
	}
	v, err := value()
	if err != nil {
		return &ReadError{Err: err}
	}
	return b.Add(k, v)
}

// decodeLastApplied decodes a persisted last-applied index. A value of the
// wrong length reads as zero, the rule Open has always applied to it.
func decodeLastApplied(v []byte) uint64 {
	if len(v) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(v)
}
