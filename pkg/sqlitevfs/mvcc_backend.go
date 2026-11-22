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

package sqlitevfs

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
)

// MVCCBackend is a Backend over a local *mvcc.Store. It is the local-process
// commit path only: Commit writes one storage batch and advances the store's
// revision itself, so the store must have no other writer (no Raft apply
// loop) while the backend commits. Replicating a commit through a Raft batch
// proposal is a later slice.
type MVCCBackend struct {
	store *mvcc.Store
	// mu serializes Commit: each commit claims CurrentRevision()+1.
	mu sync.Mutex
}

// NewMVCCBackend returns a Backend over s. A nil store is refused with
// ErrNilBackend.
func NewMVCCBackend(s *mvcc.Store) (*MVCCBackend, error) {
	if s == nil {
		return nil, ErrNilBackend
	}
	return &MVCCBackend{store: s}, nil
}

// Revision returns the store's current revision.
func (b *MVCCBackend) Revision() int64 {
	return b.store.CurrentRevision()
}

// GetAt returns key's value at rev. A key with no live value at rev (never
// written, or deleted at or before rev) is found=false, not an error.
func (b *MVCCBackend) GetAt(key []byte, rev int64) ([]byte, bool, error) {
	if rev <= 0 {
		return nil, false, nil
	}
	kv, err := b.store.GetAtRevision(key, rev)
	if err != nil {
		var notFound *mvcc.KeyNotFoundError
		var noRevision *mvcc.RevisionNotFoundError
		if errors.As(err, &notFound) || errors.As(err, &noRevision) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return kv.Value, true, nil
}

// Commit writes every put and every delete into one storage batch at
// revision CurrentRevision()+1, commits it, and publishes the revision. Any
// error closes the batch uncommitted and leaves the revision unchanged. A
// commit with nothing to write returns the current revision.
func (b *MVCCBackend) Commit(puts map[string][]byte, deletes [][]byte) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.commitLocked(puts, deletes)
}

// Publish reads each source key of moves with GetAtRevision at
// CurrentRevision(), puts its value under the destination key, deletes the
// source key, and applies puts and deletes, all in one storage batch at one
// revision (commitLocked). A source key with no live value fails the
// publish with an error naming it and writes nothing. This is the
// local-process path only; a Raft publish proposal is a later slice.
func (b *MVCCBackend) Publish(moves map[string]string, puts map[string][]byte, deletes [][]byte) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rev := b.store.CurrentRevision()
	all := make(map[string][]byte, len(puts)+len(moves))
	for k, v := range puts {
		all[k] = v
	}
	allDeletes := make([][]byte, 0, len(deletes)+len(moves))
	allDeletes = append(allDeletes, deletes...)
	for dst, src := range moves {
		value, found, err := b.GetAt([]byte(src), rev)
		if err != nil {
			return 0, fmt.Errorf("sqlitevfs: publish read %q at revision %d: %w", src, rev, err)
		}
		if !found {
			return 0, fmt.Errorf("sqlitevfs: publish source %q has no live value at revision %d", src, rev)
		}
		all[dst] = value
		allDeletes = append(allDeletes, []byte(src))
	}
	return b.commitLocked(all, allDeletes)
}

// commitLocked is the batch body of Commit and Publish; b.mu must be held.
func (b *MVCCBackend) commitLocked(puts map[string][]byte, deletes [][]byte) (int64, error) {
	if len(puts) == 0 && len(deletes) == 0 {
		return b.store.CurrentRevision(), nil
	}
	rev := b.store.CurrentRevision() + 1
	batch := b.store.Storage().Batch()
	defer func() { _ = batch.Close() }()

	keys := make([]string, 0, len(puts))
	for k := range puts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := b.store.PutInBatch(batch, []byte(k), puts[k], 0, rev); err != nil {
			return 0, err
		}
	}
	for _, k := range deletes {
		if _, err := b.store.DeleteInBatch(batch, k, rev); err != nil {
			return 0, err
		}
	}
	if err := batch.Set(mvcc.RevisionKey(), mvcc.EncodeRevision(rev)); err != nil {
		return 0, fmt.Errorf("sqlitevfs: persist revision %d: %w", rev, err)
	}
	if err := batch.Commit(); err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit batch at revision %d: %w", rev, err)
	}
	b.store.SetRevision(rev)
	b.store.MarkCommitted(rev)
	return rev, nil
}
