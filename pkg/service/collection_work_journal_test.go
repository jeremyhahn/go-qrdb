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
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingJournal records each Record call, and what the collection's
// metadata held at that moment, so a test can check the entry is written
// BEFORE the transition it announces.
type recordingJournal struct {
	mu      sync.Mutex
	kv      *memoryKVSvc
	calls   []string
	stateAt []string
	err     error
}

var _ CollectionWorkJournal = (*recordingJournal)(nil)

func (r *recordingJournal) Record(ctx context.Context, database, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, database+"/"+name)
	state := "absent"
	if raw, err := r.kv.Get(ctx, collectionMetadataKey(database, name)); err == nil && raw != nil {
		info, _, lerr := (&CollectionService{kvSvc: r.kv}).loadCollectionRecord(ctx, database, name)
		if lerr == nil {
			state = info.State
		}
	}
	r.stateAt = append(r.stateAt, state)
	return r.err
}

// TestWorkJournal_RecordedBeforeEveryTransition: a create is recorded before
// its CREATING record exists, and a drop before its DROPPING mark, so a crash
// right after either transition always leaves the sweeper an entry. A drop
// re-issued on a collection already DROPPING records nothing new.
func TestWorkJournal_RecordedBeforeEveryTransition(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	j := &recordingJournal{kv: kv}
	svc := NewCollectionService(kv).WithWorkJournal(j)

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
	require.NoError(t, err)
	markStored(t, kv, "db", "c", CollectionStateActive)
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "c"}))

	assert.Equal(t, []string{"db/c", "db/c"}, j.calls)
	assert.Equal(t, []string{"absent", CollectionStateActive}, j.stateAt, "each entry precedes its transition")

	_, err = svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "d"})
	require.NoError(t, err)
	markStored(t, kv, "db", "d", CollectionStateDropping)
	j.calls = nil
	require.NoError(t, svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "d"}))
	assert.Empty(t, j.calls, "resuming a drop already recorded records nothing")
}

// TestWorkJournal_FailureFailsTheTransition: a transition the sweeper could
// not find is one nobody finishes, so a journal that cannot be written fails
// the create or drop before anything changes.
func TestWorkJournal_FailureFailsTheTransition(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	j := &recordingJournal{kv: kv, err: errors.New("journal unavailable")}
	svc := NewCollectionService(kv).WithWorkJournal(j)

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
	assert.Equal(t, "journal", metadataOp(t, err))
	_, gerr := svc.GetCollectionInfo(ctx, "db", "c")
	var notFound *CollectionNotFoundError
	assert.ErrorAs(t, gerr, &notFound, "nothing is created")

	j.err = nil
	_, err = svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c"})
	require.NoError(t, err)
	j.err = errors.New("journal unavailable")
	err = svc.DropCollection(ctx, &DropCollectionRequest{Database: "db", Name: "c"})
	assert.Equal(t, "journal", metadataOp(t, err))
	info, gerr := svc.GetCollectionInfo(ctx, "db", "c")
	require.NoError(t, gerr)
	assert.Equal(t, CollectionStateActive, info.State, "the collection is not marked")
}
