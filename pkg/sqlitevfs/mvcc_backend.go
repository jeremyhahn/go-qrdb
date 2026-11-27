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
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
)

// MVCCBackend is a Backend over a local *mvcc.Store. It is the local-process
// commit path only: Commit writes one storage batch and advances the store's
// revision itself, so the store must have no other writer (no Raft apply
// loop) while the backend commits. Replicating a commit through a Raft batch
// proposal is a later slice.
//
// Every method returns ctx.Err() before it reads or writes when ctx is
// already done. A commit that has started is not interrupted: it is one
// local storage batch.
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
func (b *MVCCBackend) Revision(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return b.store.CurrentRevision(), nil
}

// GetAt returns key's value at rev. A key with no live value at rev (never
// written, or deleted at or before rev) is found=false, not an error.
func (b *MVCCBackend) GetAt(ctx context.Context, key []byte, rev int64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
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

// Keys returns every key with a live value at revision rev that starts with
// prefix, in key order. A non-positive rev is the empty store.
func (b *MVCCBackend) Keys(ctx context.Context, prefix []byte, rev int64) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rev <= 0 {
		return nil, nil
	}
	opts := []lock.RangeOption{lock.WithKeysOnly()}
	current := b.store.CurrentRevision()
	if rev < current {
		// A past revision scans the data entries, so a key deleted
		// since rev is still listed.
		opts = append(opts, lock.WithRevision(rev))
	}
	kvs, _, _, err := b.store.RangeFrom(b.store.Storage(), current, prefix, prefixEnd(prefix), opts...)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs: list keys at revision %d: %w", rev, err)
	}
	keys := make([][]byte, 0, len(kvs))
	for _, kv := range kvs {
		keys = append(keys, kv.Key)
	}
	return keys, nil
}

// prefixEnd returns the smallest key greater than every key starting with
// prefix, or nil (the end of the keyspace) when there is none.
func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xFF {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

// Commit writes every put and every delete into one storage batch at
// revision CurrentRevision()+1, commits it, and publishes the revision. Any
// error closes the batch uncommitted and leaves the revision unchanged. A
// commit with nothing to write returns the current revision. The read
// checks are evaluated first under the same lock (checkReadsLocked): a failed
// one is ErrSnapshotChanged and writes nothing.
func (b *MVCCBackend) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkReadsLocked(reads); err != nil {
		return 0, err
	}
	return b.commitLocked(puts, deletes)
}

// checkReadsLocked evaluates every read check against the newest committed
// revision; b.mu must be held, so no commit lands between the check and the
// batch that follows it. A moved key is ErrSnapshotChanged.
func (b *MVCCBackend) checkReadsLocked(reads []ReadCheck) error {
	if len(reads) == 0 {
		return nil
	}
	now := b.store.CurrentRevision()
	for _, r := range reads {
		var modRev, createRev int64
		found := false
		if now > 0 {
			kv, err := b.store.GetAtRevision(r.Key, now)
			if err != nil {
				var notFound *mvcc.KeyNotFoundError
				var noRevision *mvcc.RevisionNotFoundError
				if !errors.As(err, &notFound) && !errors.As(err, &noRevision) {
					return fmt.Errorf("sqlitevfs: read check %q at revision %d: %w", r.Key, now, err)
				}
			} else {
				found = true
				modRev = kv.ModRevision
				createRev = kv.CreateRevision
			}
		}
		if r.CreateRev != 0 {
			if !found || createRev != r.CreateRev {
				return fmt.Errorf("%w: %q no longer holds create revision %d", ErrSnapshotChanged, r.Key, r.CreateRev)
			}
			continue
		}
		if found != r.Found || (found && modRev > r.Rev) {
			return fmt.Errorf("%w: %q changed since revision %d", ErrSnapshotChanged, r.Key, r.Rev)
		}
	}
	return nil
}

// Publish lists the keys live under src at CurrentRevision(), puts each
// value under dst + (key without src), deletes the source key, and applies
// puts and deletes, all in one storage batch at one revision
// (commitLocked), in that order: an explicit put overwrites a moved value
// on the same key and an explicit delete removes either, as the engine's
// OpMVCCMovePrefix does. A live count other than expectedKeys is
// ErrPublishConflict and writes nothing. This is the local-process path
// only; the replicated path is ServiceBackend over the server's
// OpMVCCMovePrefix.
func (b *MVCCBackend) Publish(ctx context.Context, src, dst []byte, expectedKeys int64, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validatePublish(src, dst, expectedKeys); err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkReadsLocked(reads); err != nil {
		return 0, err
	}
	rev := b.store.CurrentRevision()
	keys, err := b.Keys(ctx, src, rev)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: publish list %q at revision %d: %w", src, rev, err)
	}
	if int64(len(keys)) != expectedKeys {
		return 0, fmt.Errorf("%w: %d keys live under %q at revision %d, expected %d",
			ErrPublishConflict, len(keys), src, rev, expectedKeys)
	}
	// The batch applies puts before deletes and a key at most once, so
	// the ordered writes are folded into their net effect first, in the
	// engine's order (ServiceBackend's OpMVCCMovePrefix): the moved values
	// and the source deletes, then the explicit puts, then the explicit
	// deletes. An explicit put therefore wins over a moved value on the
	// same key, and an explicit delete over both.
	final := make(map[string][]byte, len(puts)+2*len(keys))
	deleted := make(map[string]bool, len(deletes)+len(keys))
	set := func(k string, v []byte) { final[k] = v; delete(deleted, k) }
	del := func(k string) { delete(final, k); deleted[k] = true }
	for _, key := range keys {
		value, found, err := b.GetAt(ctx, key, rev)
		if err != nil {
			return 0, fmt.Errorf("sqlitevfs: publish read %q at revision %d: %w", key, rev, err)
		}
		if !found {
			return 0, fmt.Errorf("%w: %q has no live value at revision %d", ErrPublishConflict, key, rev)
		}
		dstKey := make([]byte, 0, len(dst)+len(key)-len(src))
		dstKey = append(append(dstKey, dst...), key[len(src):]...)
		set(string(dstKey), value)
		del(string(key))
	}
	for k, v := range puts {
		set(k, v)
	}
	for _, k := range deletes {
		del(string(k))
	}
	allDeletes := make([][]byte, 0, len(deleted))
	for k := range deleted {
		allDeletes = append(allDeletes, []byte(k))
	}
	sort.Slice(allDeletes, func(i, j int) bool { return bytes.Compare(allDeletes[i], allDeletes[j]) < 0 })
	return b.commitLocked(final, allDeletes)
}

// validatePublish refuses an empty src or dst, prefixes where one is a
// prefix of the other, and an expected key count below one.
func validatePublish(src, dst []byte, expectedKeys int64) error {
	switch {
	case len(src) == 0 || len(dst) == 0:
		return fmt.Errorf("%w: empty source or destination prefix", ErrInvalidPublish)
	case bytes.HasPrefix(src, dst) || bytes.HasPrefix(dst, src):
		return fmt.Errorf("%w: prefixes %q and %q overlap", ErrInvalidPublish, src, dst)
	case expectedKeys < 1:
		return fmt.Errorf("%w: expected key count %d", ErrInvalidPublish, expectedKeys)
	}
	return nil
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
