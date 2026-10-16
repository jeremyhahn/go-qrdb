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

// stateDigest answers a statedigest.Query for the mvcc state machine, exactly
// as PebbleStateMachine.stateDigest does.
func (s *MVCCStateMachine) stateDigest() (statedigest.Digest, error) {
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

	marker := []byte(metaKeyLastApplied)
	b := statedigest.NewBuilder()
	var applied uint64
	for iter.First(); iter.Valid(); iter.Next() {
		k := iter.Key()
		if bytes.Equal(k, marker) {
			v, err := iter.ValueAndErr()
			if err != nil {
				return statedigest.Digest{}, &ReadError{Key: metaKeyLastApplied, Err: err}
			}
			applied = decodeLastApplied(v)
			continue
		}
		if isNodeLocalKey(k) {
			continue
		}
		v, err := iter.ValueAndErr()
		if err != nil {
			return statedigest.Digest{}, &ReadError{Err: err}
		}
		if err := b.Add(k, v); err != nil {
			return statedigest.Digest{}, err
		}
	}
	if err := iter.Error(); err != nil {
		return statedigest.Digest{}, &ReadError{Err: err}
	}
	return b.Finish(applied), nil
}

// decodeLastApplied decodes a persisted last-applied index. A value of the
// wrong length reads as zero, the rule Open has always applied to it.
func decodeLastApplied(v []byte) uint64 {
	if len(v) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(v)
}
