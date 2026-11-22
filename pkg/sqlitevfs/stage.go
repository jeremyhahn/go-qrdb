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
// truncated-page deletes apply at the same one revision, which it returns.
// A live staged count other than the pages it wrote is ErrPublishConflict
// and publishes nothing.
//
// Staged keys are never read as pages: readers see the old pages until the
// publish revision and the new ones from it. On a staging or publish
// failure commitStaged deletes every stage key it wrote in one best-effort
// Backend.Commit and returns the original error wrapped with the db name.
func (t *Txn) commitStaged(metaValue []byte, deletes [][]byte) (int64, error) {
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

	var staged [][]byte
	fail := func(err error) (int64, error) {
		if len(staged) > 0 {
			_, _ = s.backend.Commit(nil, staged)
		}
		return 0, fmt.Errorf("sqlitevfs: staged commit %q: %w", s.db, err)
	}

	chunk := make(map[string][]byte)
	var chunkKeys [][]byte
	chunkBytes := 0
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		if _, err := s.backend.Commit(chunk, nil); err != nil {
			return err
		}
		staged = append(staged, chunkKeys...)
		chunk = make(map[string][]byte)
		chunkKeys = nil
		chunkBytes = 0
		return nil
	}
	for _, pgno := range pgnos {
		page := t.dirty[pgno]
		if chunkBytes > 0 && chunkBytes+len(page) > s.stageChunkBytes {
			if err := flush(); err != nil {
				return fail(err)
			}
		}
		stageKey, err := StageKey(s.db, commitID, pgno)
		if err != nil {
			return fail(err)
		}
		chunk[string(stageKey)] = page
		chunkKeys = append(chunkKeys, stageKey)
		chunkBytes += len(page)
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	puts := map[string][]byte{string(s.metaKey): metaValue}
	stagePrefix, err := StagePrefix(s.db, commitID)
	if err != nil {
		return fail(err)
	}
	pagePrefix, err := PagePrefix(s.db)
	if err != nil {
		return fail(err)
	}
	rev, err := s.backend.Publish(stagePrefix, pagePrefix, int64(len(pgnos)), puts, deletes)
	if err != nil {
		return fail(err)
	}
	return rev, nil
}
