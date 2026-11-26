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
	"errors"
	"testing"
)

// liveKeys is the live keys of app.db under prefix at the backend's newest
// revision.
func liveKeys(t *testing.T, b *MVCCBackend, prefix []byte) [][]byte {
	t.Helper()
	keys, err := b.Keys(t.Context(), prefix, revOf(t, b))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// A removal of a database with more pages than one request can carry
// deletes them in requests under the transport cap and leaves no key of
// the database.
func TestRemovalChunksDeletesUnderTheCap(t *testing.T) {
	b := newTestBackend(t)
	if _, err := b.Commit(t.Context(), nil, map[string][]byte{"other/1": []byte("x")}, nil); err != nil {
		t.Fatal(err)
	}
	absent := revOf(t, b)
	rec := &requestRecorder{MVCCBackend: b}
	s := seedTruncatable(t, rec)
	*rec = requestRecorder{MVCCBackend: b}

	if _, err := s.RestoreToRevision(t.Context(), absent); err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if rec.maxEst >= truncCap || rec.maxJSON >= truncCap {
		t.Fatalf("a request of %d bytes (JSON %d) reached the cap %d", rec.maxEst, rec.maxJSON, truncCap)
	}
	if rec.commits < 3 {
		t.Fatalf("%d commits: the removal's deletes were not split", rec.commits)
	}
	if keys := liveKeys(t, b, []byte(keyRoot+"app.db/")); len(keys) != 0 {
		t.Fatalf("%d keys of app.db left after its removal", len(keys))
	}
}

// failPublish refuses every publish, so a staged commit fails after its
// staging commits and cleans its stage keys up.
type failPublish struct {
	*requestRecorder
}

var errPublishRefused = errors.New("publish refused")

func (f failPublish) Publish(context.Context, []byte, []byte, int64, []ReadCheck, map[string][]byte, [][]byte) (int64, error) {
	f.publishes++
	return 0, errPublishRefused
}

// A staged commit that fails at publish deletes more stage keys than one
// request can carry in requests under the transport cap, and leaves none.
func TestFailedStageCleanupChunksDeletesUnderTheCap(t *testing.T) {
	b := newTestBackend(t)
	rec := &requestRecorder{MVCCBackend: b}
	s := seedTruncatable(t, failPublish{rec})
	*rec = requestRecorder{MVCCBackend: b}
	s.stageThreshold = 0

	txn := beginReserved(t, s)
	fills := make([]byte, truncPages)
	for i := range fills {
		fills[i] = 0x7f
	}
	writePages(t, txn, fills...)
	if _, err := txn.Commit(t.Context()); !errors.Is(err, errPublishRefused) {
		t.Fatalf("Commit = %v, want the refused publish", err)
	}
	if rec.publishes != 1 {
		t.Fatalf("%d publishes, want 1", rec.publishes)
	}
	if rec.maxEst >= truncCap || rec.maxJSON >= truncCap {
		t.Fatalf("a request of %d bytes (JSON %d) reached the cap %d", rec.maxEst, rec.maxJSON, truncCap)
	}
	root, err := StageRoot("app.db")
	if err != nil {
		t.Fatal(err)
	}
	if keys := liveKeys(t, b, root); len(keys) != 0 {
		t.Fatalf("%d stage keys left after the failed commit", len(keys))
	}
}
