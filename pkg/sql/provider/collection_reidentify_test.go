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

package provider

import (
	"context"
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// reidentifyHarness is UPDATE ... SET _id = 2 WHERE _id = 1 over a mock whose
// release of document 1 answers with releaseErr and whose withdrawal of the
// new copy answers with withdrawErr.
func reidentifyHarness(releaseErr, withdrawErr error) (*CollectionTable, *[]uint64) {
	var deleted []uint64
	svc := &mockCollectionService{
		getFn: func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"name": "Old"}, Revision: "r1"}, nil
		},
		insertFn: func(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return &service.InsertDocumentResponse{ID: 2, Revision: "claimed"}, nil
		},
		deleteFn: func(_ context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
			if req.ID == 1 {
				return nil, releaseErr
			}
			deleted = append(deleted, req.ID)
			if withdrawErr != nil {
				return nil, withdrawErr
			}
			return &service.DeleteDocumentResponse{Deleted: true}, nil
		},
	}
	return NewCollectionTable(svc, "mydb", "users", map[string]string{"name": "VARCHAR"}, nil), &deleted
}

// TestCollectionTable_Reidentify_UnknownReleaseLeavesBothDocuments is B-6: a
// release of the old identity whose outcome is UNKNOWN -- a storage error,
// not a refusal -- may have removed the old document, so withdrawing the new
// copy could leave the document nowhere. Both are left and the statement
// reports the error as the storage failure it is, not as a retryable 1213.
func TestCollectionTable_Reidentify_UnknownReleaseLeavesBothDocuments(t *testing.T) {
	storage := &service.CollectionMetadataError{Op: "delete-document", Err: errors.New("disk")}
	tbl, deleted := reidentifyHarness(storage, nil)
	ctx := newTestContext()

	err := tbl.Updater(ctx).Update(ctx, sqle.Row{uint64(1), "Old"}, sqle.Row{uint64(2), "New"})
	require.Error(t, err)
	assert.ErrorIs(t, err, storage)
	assert.ErrorIs(t, err, service.ErrOutcomeUnknown)
	var unknown *DocumentMoveOutcomeUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, uint64(1), unknown.OldID)
	assert.Equal(t, uint64(2), unknown.NewID)
	assert.Contains(t, unknown.Error(), "both ids are left")
	assert.False(t, sqle.ErrLockDeadlock.Is(err), "an unknown outcome is not a lost race")
	assert.Empty(t, *deleted, "the new copy must not be withdrawn on an unknown outcome")
}

// TestCollectionTable_Reidentify_LostRaceWithdraws: a release refused because
// the old document changed or is gone withdraws the copy; a withdrawal that
// fails is joined into the statement's error rather than dropped.
func TestCollectionTable_Reidentify_LostRaceWithdraws(t *testing.T) {
	for name, release := range map[string]error{
		"changed": &service.CollectionDocumentConflictError{ID: 1},
		"gone":    &service.CollectionDocumentNotFoundError{ID: 1},
	} {
		t.Run(name, func(t *testing.T) {
			tbl, deleted := reidentifyHarness(release, nil)
			ctx := newTestContext()
			err := tbl.Updater(ctx).Update(ctx, sqle.Row{uint64(1), "Old"}, sqle.Row{uint64(2), "New"})
			require.Error(t, err)
			assert.True(t, sqle.ErrLockDeadlock.Is(err), "a lost race is reported as 1213 for the client to retry: %v", err)
			assert.NotErrorIs(t, err, service.ErrOutcomeUnknown)
			assert.Equal(t, []uint64{2}, *deleted, "the copy is withdrawn")
		})
	}

	withdraw := errors.New("withdraw failed")
	tbl, _ := reidentifyHarness(&service.CollectionDocumentConflictError{ID: 1}, withdraw)
	ctx := newTestContext()
	err := tbl.Updater(ctx).Update(ctx, sqle.Row{uint64(1), "Old"}, sqle.Row{uint64(2), "New"})
	require.Error(t, err)
	assert.ErrorIs(t, err, withdraw, "a failed withdrawal is reported, not swallowed")
}
