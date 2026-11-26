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
	"context"
	"fmt"
	"sort"
)

// commitStaged commits an oversized transaction (Q2 A). It picks a fresh
// commit id, writes the dirty pages in page-number order under StageKey in
// Backend.Commit calls of at most the Store's stage chunk size each (puts
// only), then sends one Backend.Publish in the prefix form: every key under
// StagePrefix(db, commitID) moves to PagePrefix(db) + the same 4-byte page
// number (neither prefix is a prefix of the other), with the staged page
// count as the expected count, and the meta record metaValue and the
// hole deletes apply at the same one revision, which it returns, with as
// many of trim (pages past the new count, stale stage keys) as fit under
// the transport cap; trimAfter deletes the rest.
// A live staged count other than the pages it wrote is ErrPublishConflict
// and publishes nothing. The publish carries the commit's read set (reads,
// see Txn.readSet), so an optimistic staged commit whose reads moved
// conflicts at publish with ErrSnapshotChanged, and its stage keys are swept
// like any other publish failure.
//
// Staged keys are never read as pages: readers see the old pages until the
// publish revision and the new ones from it. On a staging or publish
// failure commitStaged deletes every stage key it wrote in one best-effort
// Backend.Commit and returns the original error wrapped with the db name.
func (t *Txn) commitStaged(ctx context.Context, reads []ReadCheck, metaValue []byte, deletes, trim [][]byte) (int64, error) {
	s := t.store
	commitID, err := newCommitID()
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit %q: %w", s.db, err)
	}
	pgnos := make([]uint32, 0, len(t.dirty))
	for pgno := range t.dirty {
		pgnos = append(pgnos, pgno)
	}
	sort.Slice(pgnos, func(i, j int) bool { return pgnos[i] < pgnos[j] })
	puts := map[string][]byte{string(s.metaKey): metaValue}
	stagePrefix, err := StagePrefix(s.db, commitID)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: staged commit %q: %w", s.db, err)
	}
	pagePrefix, err := PagePrefix(s.db)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: staged commit %q: %w", s.db, err)
	}
	// The publish carries the read checks, meta and in-range deletes in
	// one request; staging moves only page bytes, so a publish that would
	// reach the cap is refused before anything is staged.
	publishBytes := encodedRequestBytes(reads, puts, deletes, stagePrefix, pagePrefix)
	if err := s.boundAtomic(publishBytes, reads); err != nil {
		return 0, err
	}

	var staged [][]byte
	fail := func(err error) (int64, error) {
		if len(staged) > 0 {
			// The cleanup ignores the caller's cancellation
			// (context.WithoutCancel), so a cancelled ctx cannot leave
			// staged keys behind, and is chunked under the transport cap.
			// No publish names this commit id, so nothing guards it.
			s.deleteChunked(context.WithoutCancel(ctx), nil, staged)
		}
		return 0, fmt.Errorf("sqlitevfs: staged commit %q: %w", s.db, err)
	}

	chunk := make(map[string][]byte)
	var chunkKeys [][]byte
	chunkBytes := 0
	// chunkRequest is the encoded request size of the chunk so far
	// (encodedRequestBytes, built up entry by entry).
	chunkRequest := requestEnvelopeFraming
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		if _, err := s.backend.Commit(ctx, nil, chunk, nil); err != nil {
			return err
		}
		staged = append(staged, chunkKeys...)
		chunk = make(map[string][]byte)
		chunkKeys = nil
		chunkBytes = 0
		chunkRequest = requestEnvelopeFraming
		if t.held {
			// Renews the lease each chunk and refuses the publish once it was lost.
			return s.writer.Check(ctx, s.lockKey, t.token)
		}
		return nil
	}
	for _, pgno := range pgnos {
		page := t.dirty[pgno]
		stageKey, err := StageKey(s.db, commitID, pgno)
		if err != nil {
			return fail(err)
		}
		// Flush before the page bytes pass the chunk size or the encoded
		// request would reach the transport cap.
		entry := encodedEntryBytes(len(stageKey), len(page))
		if chunkBytes > 0 && (chunkBytes+len(page) > s.stageChunkBytes || chunkRequest+entry >= s.requestBytes) {
			if err := flush(); err != nil {
				return fail(err)
			}
		}
		chunk[string(stageKey)] = page
		chunkKeys = append(chunkKeys, stageKey)
		chunkBytes += len(page)
		chunkRequest += entry
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	head, rest := s.splitDeletes(publishBytes, trim)
	rev, err := s.backend.Publish(ctx, stagePrefix, pagePrefix, int64(len(pgnos)), reads, puts, append(deletes, head...))
	if err != nil {
		return fail(err)
	}
	s.trimAfter(ctx, rev, rest)
	return rev, nil
}
