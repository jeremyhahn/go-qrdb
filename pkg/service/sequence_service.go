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

package service

import (
	"context"
	"encoding/binary"
)

// SequenceServicer allocates database-unique, monotonically increasing
// counters -- the primitive TableID and AUTO_INCREMENT columns are built on.
//
// Correctness bar: gaps are acceptable, duplicates are not. Every block
// boundary is established by a Raft-committed compare-and-swap over the
// counter's MVCC ModRevision, so a node that loses leadership (or a request
// that times out) mid-allocation either committed the new counter value or
// it did not; there is no third outcome where two callers both believe they
// own the same number. A retry that races another allocator and loses
// simply throws its candidate block away and computes a fresh one from the
// value the winner committed -- that block is never handed out, which is
// where the gaps come from.
type SequenceServicer interface {
	// Allocate reserves n consecutive counter values and returns the first
	// one; the caller owns [first, first+n). Sequences start at 1, so
	// Allocate on a fresh sequence with n == 1 returns 1.
	Allocate(ctx context.Context, database, name string, n uint64) (first uint64, err error)

	// Peek returns the sequence's current value (the last value allocated,
	// or 0 for a sequence that has never been allocated from) without
	// advancing it.
	Peek(ctx context.Context, database, name string) (uint64, error)

	// Advance raises the sequence's current value to atLeast if it is not
	// already there. It is a no-op, not an error, when the sequence is
	// already at or past atLeast -- this makes it safe to call
	// unconditionally when importing or replaying data that carries its own
	// IDs, to guarantee the sequence never subsequently hands out a value
	// that collides with one already in use.
	Advance(ctx context.Context, database, name string, atLeast uint64) error

	// Reset returns the sequence to its zero state, so the next Allocate
	// call starts a fresh sequence at 1. Reset does not affect rows already
	// written under previously allocated values; callers that reset a
	// sequence backing a live AUTO_INCREMENT column are responsible for the
	// consequences of any resulting collisions with existing data.
	Reset(ctx context.Context, database, name string) error
}

// sequenceKVService is the narrow read interface SequenceService needs: it
// must observe both a counter's current value and the MVCC ModRevision it
// was last written at, so an update can be proposed as a compare-and-swap
// against that exact revision. A key that has never been written reads back
// as KeyNotFoundError, which SequenceService treats as current == 0,
// modRevision == 0 -- the same "absent" encoding a Compare against
// ModRevision 0 uses, so allocating on a brand-new sequence and updating an
// existing one flow through the identical compare-and-swap code path.
type sequenceKVService interface {
	GetRevision(ctx context.Context, key string) (*KeyValue, error)
}

// SequenceService implements SequenceServicer backed by an 8-byte
// big-endian counter at tableSequenceKey(database, name), advanced through
// TransactionServicer compare-and-swap proposals over Raft consensus.
type SequenceService struct {
	kv  sequenceKVService
	txn TransactionServicer
}

// Compile-time interface check.
var _ SequenceServicer = (*SequenceService)(nil)

// NewSequenceService creates a new SequenceServicer backed by the given
// read interface (for observing counter value + ModRevision) and
// TransactionServicer (for proposing compare-and-swap updates).
func NewSequenceService(kv sequenceKVService, txn TransactionServicer) *SequenceService {
	return &SequenceService{kv: kv, txn: txn}
}

// Allocate reserves n consecutive counter values and returns the first one.
func (s *SequenceService) Allocate(ctx context.Context, database, name string, n uint64) (uint64, error) {
	if err := validateSequenceRef(database, name); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, &SequenceInvalidCountError{}
	}

	var first uint64
	_, err := s.casLoop(ctx, database, name, func(current uint64) (uint64, bool) {
		first = current + 1
		return current + n, true
	})
	if err != nil {
		return 0, err
	}
	return first, nil
}

// Peek returns the sequence's current value without advancing it.
func (s *SequenceService) Peek(ctx context.Context, database, name string) (uint64, error) {
	if err := validateSequenceRef(database, name); err != nil {
		return 0, err
	}
	current, _, err := s.readCounter(ctx, tableSequenceKey(database, name))
	if err != nil {
		return 0, err
	}
	return current, nil
}

// Advance raises the sequence's current value to atLeast if it is not
// already there.
func (s *SequenceService) Advance(ctx context.Context, database, name string, atLeast uint64) error {
	if err := validateSequenceRef(database, name); err != nil {
		return err
	}
	_, err := s.casLoop(ctx, database, name, func(current uint64) (uint64, bool) {
		if current >= atLeast {
			return current, false
		}
		return atLeast, true
	})
	return err
}

// Reset returns the sequence to its zero state.
func (s *SequenceService) Reset(ctx context.Context, database, name string) error {
	if err := validateSequenceRef(database, name); err != nil {
		return err
	}
	_, err := s.casLoop(ctx, database, name, func(current uint64) (uint64, bool) {
		if current == 0 {
			return current, false
		}
		return 0, true
	})
	return err
}

// casLoop reads the counter's current value, applies compute to decide the
// next value, and -- when compute reports a change -- proposes a Raft
// compare-and-swap over the counter's observed ModRevision. It retries with
// bounded jittered backoff (see catalogRetryConfig) on a losing CAS race,
// re-reading a fresh current/modRevision on every attempt, and gives up
// with SequenceConflictError after the retry budget is exhausted. When
// compute reports no change (already at the target value, as with a no-op
// Advance or Reset), casLoop returns the unchanged current value without
// proposing anything.
func (s *SequenceService) casLoop(ctx context.Context, database, name string, compute func(current uint64) (next uint64, changed bool)) (uint64, error) {
	key := tableSequenceKey(database, name)

	var result uint64
	err := doWithCatalogRetry(ctx, func(ctx context.Context) error {
		current, modRevision, err := s.readCounter(ctx, key)
		if err != nil {
			return err
		}

		next, changed := compute(current)
		if !changed {
			result = current
			return nil
		}

		compares := []Compare{{
			Key:         []byte(key),
			Target:      CompareModRevision,
			Result:      CompareEqual,
			ModRevision: modRevision,
		}}
		ops := []Op{{Type: OpPut, Key: []byte(key), Value: encodeSequenceCounter(next)}}

		resp, err := s.txn.Execute(ctx, compares, ops, nil)
		if err != nil {
			return err
		}
		if !resp.Succeeded {
			return &TransactionConflictError{Key: key}
		}

		result = next
		return nil
	})
	if err != nil {
		if isTransactionConflict(err) {
			return 0, &SequenceConflictError{Database: database, Name: name}
		}
		return 0, err
	}

	return result, nil
}

// readCounter reads the counter at key, returning (0, 0, nil) when the key
// has never been written -- the "absent" encoding described on
// sequenceKVService.
func (s *SequenceService) readCounter(ctx context.Context, key string) (current uint64, modRevision int64, err error) {
	kv, err := s.kv.GetRevision(ctx, key)
	if err == nil {
		return decodeSequenceCounter(kv.Value), kv.ModRevision, nil
	}
	if isKeyNotFound(err) {
		return 0, 0, nil
	}
	return 0, 0, err
}

// validateSequenceRef validates the database and sequence name are non-empty.
func validateSequenceRef(database, name string) error {
	if database == "" {
		return &SequenceEmptyDatabaseError{}
	}
	if name == "" {
		return &SequenceEmptyNameError{}
	}
	return nil
}

// encodeSequenceCounter encodes v as an 8-byte big-endian counter value.
func encodeSequenceCounter(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// decodeSequenceCounter decodes an 8-byte big-endian counter value. A value
// shorter than 8 bytes (including empty) decodes as 0 rather than erroring,
// so a counter key that exists but has not yet been written by this code
// path degrades to "absent" instead of failing closed.
func decodeSequenceCounter(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
