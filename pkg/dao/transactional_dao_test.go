//go:build codec_json

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewTransactional validates NewTransactional creation.
func TestNewTransactional(t *testing.T) {
	t.Run("ValidCreation", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		dao, err := NewTransactional[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)
		assert.NotNil(t, dao)
	})

	t.Run("NilStore", func(t *testing.T) {
		_, err := NewTransactional[*TestEntity](
			nil,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		assert.Error(t, err)
		var configErr InvalidConfigError
		assert.ErrorAs(t, err, &configErr)
	})

	t.Run("EmptyEntityType", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		_, err := NewTransactional[*TestEntity](
			store,
			"",
			func() *TestEntity { return &TestEntity{} },
		)
		assert.Error(t, err)
		var configErr InvalidConfigError
		assert.ErrorAs(t, err, &configErr)
	})

	t.Run("NilNewEntity", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		_, err := NewTransactional[*TestEntity](
			store,
			"users",
			nil,
		)
		assert.Error(t, err)
		var configErr InvalidConfigError
		assert.ErrorAs(t, err, &configErr)
	})

	t.Run("WithOptions", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		dao, err := NewTransactional[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithTimeout(10*time.Second),
			WithCodec(CodecJSON),
		)
		require.NoError(t, err)
		assert.NotNil(t, dao)
	})
}

// TestTransactionalDAOBasicOperations validates basic CRUD operations.
func TestTransactionalDAOBasicOperations(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("SaveAndGet", func(t *testing.T) {
		entity := &TestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "Alice", retrieved.Name)
		assert.Equal(t, "alice@example.com", retrieved.Email)
	})

	t.Run("Delete", func(t *testing.T) {
		entity := &TestEntity{ID: 2, Name: "Bob", Email: "bob@example.com"}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		err = dao.Delete(ctx, entity)
		require.NoError(t, err)

		_, err = dao.Get(ctx, 2)
		assert.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("Count", func(t *testing.T) {
		// Clear store by creating new DAO
		store := kvstoretest.NewGuardedMemoryStore()
		dao, err := NewTransactional[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		err = dao.Save(ctx, &TestEntity{ID: 10, Name: "User1"})
		require.NoError(t, err)
		err = dao.Save(ctx, &TestEntity{ID: 11, Name: "User2"})
		require.NoError(t, err)

		count, err := dao.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})

	t.Run("EntityType", func(t *testing.T) {
		assert.Equal(t, "users", dao.EntityType())
	})
}

// TestWithTx validates the WithTx helper function.
func TestWithTx(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("SuccessfulTransaction", func(t *testing.T) {
		err := WithTx(ctx, dao, func(tx DAOTransaction[*TestEntity]) error {
			return tx.Save(&TestEntity{ID: 100, Name: "TxUser"})
		})
		require.NoError(t, err)

		// Verify persisted
		retrieved, err := dao.Get(ctx, 100)
		require.NoError(t, err)
		assert.Equal(t, "TxUser", retrieved.Name)
	})

	t.Run("FailedTransaction", func(t *testing.T) {
		expectedErr := errors.New("intentional error")
		err := WithTx(ctx, dao, func(tx DAOTransaction[*TestEntity]) error {
			err := tx.Save(&TestEntity{ID: 101, Name: "FailUser"})
			if err != nil {
				return err
			}
			return expectedErr
		})
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)

		// Verify not persisted (rolled back)
		_, err = dao.Get(ctx, 101)
		assert.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("EmptyTransaction", func(t *testing.T) {
		err := WithTx(ctx, dao, func(_ DAOTransaction[*TestEntity]) error {
			return nil
		})
		require.NoError(t, err)
	})
}

// TestBatchSave validates the BatchSave helper function.
func TestBatchSave(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("SaveMultipleEntities", func(t *testing.T) {
		entities := []*TestEntity{
			{ID: 200, Name: "Batch1"},
			{ID: 201, Name: "Batch2"},
			{ID: 202, Name: "Batch3"},
		}

		err := BatchSave(ctx, dao, entities)
		require.NoError(t, err)

		// Verify all saved
		for _, e := range entities {
			retrieved, err := dao.Get(ctx, e.ID)
			require.NoError(t, err)
			assert.Equal(t, e.Name, retrieved.Name)
		}
	})

	t.Run("EmptyBatch", func(t *testing.T) {
		err := BatchSave(ctx, dao, []*TestEntity{})
		require.NoError(t, err)
	})

	t.Run("NilSlice", func(t *testing.T) {
		err := BatchSave[*TestEntity](ctx, dao, nil)
		require.NoError(t, err)
	})
}

// TestBatchDelete validates the BatchDelete helper function.
func TestBatchDelete(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("DeleteMultipleEntities", func(t *testing.T) {
		// Create entities
		entities := []*TestEntity{
			{ID: 300, Name: "Delete1"},
			{ID: 301, Name: "Delete2"},
			{ID: 302, Name: "Delete3"},
		}

		for _, e := range entities {
			err := dao.Save(ctx, e)
			require.NoError(t, err)
		}

		// Batch delete
		err := BatchDelete(ctx, dao, entities)
		require.NoError(t, err)

		// Verify all deleted
		for _, e := range entities {
			_, err := dao.Get(ctx, e.ID)
			assert.Error(t, err)
			assert.True(t, IsNotFound(err))
		}
	})

	t.Run("EmptyBatch", func(t *testing.T) {
		err := BatchDelete(ctx, dao, []*TestEntity{})
		require.NoError(t, err)
	})

	t.Run("NilSlice", func(t *testing.T) {
		err := BatchDelete[*TestEntity](ctx, dao, nil)
		require.NoError(t, err)
	})
}

// TestSaveWithOptimisticLock validates the SaveWithOptimisticLock helper.
func TestSaveWithOptimisticLock(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*VersionedTestEntity](
		store,
		"versioned",
		func() *VersionedTestEntity { return &VersionedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("SuccessfulOptimisticLock", func(t *testing.T) {
		// Create initial entity
		entity := &VersionedTestEntity{ID: 400, Name: "Original", Version: 1}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		// Update with correct version
		updated := &VersionedTestEntity{ID: 400, Name: "Updated", Version: 2}
		err = SaveWithOptimisticLock(ctx, dao, updated, 1)
		require.NoError(t, err)

		// Verify update
		retrieved, err := dao.Get(ctx, 400)
		require.NoError(t, err)
		assert.Equal(t, "Updated", retrieved.Name)
	})

	t.Run("FailedOptimisticLock", func(t *testing.T) {
		// Create initial entity
		entity := &VersionedTestEntity{ID: 401, Name: "Original", Version: 5}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		// Update with wrong version
		updated := &VersionedTestEntity{ID: 401, Name: "Updated", Version: 6}
		err = SaveWithOptimisticLock(ctx, dao, updated, 1) // Wrong version
		assert.Error(t, err)
		assert.True(t, IsConditionFailed(err))

		// Verify not updated
		retrieved, err := dao.Get(ctx, 401)
		require.NoError(t, err)
		assert.Equal(t, "Original", retrieved.Name)
	})
}

// TestVersionedEntity validates the VersionedEntity helper struct.
func TestVersionedEntity(t *testing.T) {
	v := &VersionedEntity{Version: 5}

	assert.Equal(t, int64(5), v.GetVersion())

	v.SetVersion(10)
	assert.Equal(t, int64(10), v.GetVersion())
}

// TestTimestampedEntity validates the TimestampedEntity helper struct.
func TestTimestampedEntity(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)

	ts := &TimestampedEntity{}
	ts.SetCreatedAt(now)
	ts.SetUpdatedAt(later)

	assert.Equal(t, now, ts.GetCreatedAt())
	assert.Equal(t, later, ts.GetUpdatedAt())
}

// TestTransactionalDAOPagination validates pagination operations.
func TestTransactionalDAOPagination(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Create test data
	for i := 1; i <= 30; i++ {
		entity := &TestEntity{
			ID:    uint64(i),
			Name:  "User",
			Email: "user@example.com",
		}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)
	}

	t.Run("FirstPage", func(t *testing.T) {
		result, err := dao.Page(ctx, PageQuery{Page: 1, PageSize: 10})
		require.NoError(t, err)
		assert.Len(t, result.Entities, 10)
		assert.Equal(t, 30, result.Total)
		assert.True(t, result.HasMore)
	})

	t.Run("LastPage", func(t *testing.T) {
		result, err := dao.Page(ctx, PageQuery{Page: 3, PageSize: 10})
		require.NoError(t, err)
		assert.Len(t, result.Entities, 10)
		assert.False(t, result.HasMore)
	})

	t.Run("ForEachPage", func(t *testing.T) {
		pageCount := 0
		entityCount := 0

		err := dao.ForEachPage(ctx, PageQuery{PageSize: 10}, func(result PageResult[*TestEntity]) error {
			pageCount++
			entityCount += len(result.Entities)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 3, pageCount)
		assert.Equal(t, 30, entityCount)
	})
}

// TestTransactionConditionOperators validates all condition operators.
func TestTransactionConditionOperators(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*VersionedTestEntity](
		store,
		"versioned",
		func() *VersionedTestEntity { return &VersionedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Create entity with version 5
	entity := &VersionedTestEntity{ID: 500, Name: "Test", Version: 5}
	err = dao.Save(ctx, entity)
	require.NoError(t, err)

	tests := []struct {
		name      string
		operator  ConditionOp
		value     int64
		shouldErr bool
	}{
		{"Equal_Match", OpEqual, 5, false},
		{"Equal_NoMatch", OpEqual, 3, true},
		{"NotEqual_Match", OpNotEqual, 3, false},
		{"NotEqual_NoMatch", OpNotEqual, 5, true},
		{"Greater_Match", OpGreater, 4, false},
		{"Greater_NoMatch", OpGreater, 5, true},
		{"Less_Match", OpLess, 6, false},
		{"Less_NoMatch", OpLess, 5, true},
		{"GreaterEqual_Match_Equal", OpGreaterEqual, 5, false},
		{"GreaterEqual_Match_Greater", OpGreaterEqual, 4, false},
		{"GreaterEqual_NoMatch", OpGreaterEqual, 6, true},
		{"LessEqual_Match_Equal", OpLessEqual, 5, false},
		{"LessEqual_Match_Less", OpLessEqual, 6, false},
		{"LessEqual_NoMatch", OpLessEqual, 4, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := dao.BeginTx(ctx)
			require.NoError(t, err)

			updated := &VersionedTestEntity{ID: 500, Name: "Updated", Version: 6}
			err = tx.SaveWithCondition(updated, Condition{
				Target:   ConditionVersion,
				Operator: tc.operator,
				Value:    tc.value,
			})
			require.NoError(t, err)

			err = tx.Commit(ctx)
			if tc.shouldErr {
				assert.Error(t, err)
				assert.True(t, IsConditionFailed(err))
			} else {
				assert.NoError(t, err)
				// Reset for next test
				entity.Name = "Test"
				entity.Version = 5
				err = dao.Save(ctx, entity)
				require.NoError(t, err)
			}
		})
	}
}

// TestTransactionErrorTypes validates error type checking functions.
func TestTransactionErrorTypes(t *testing.T) {
	t.Run("IsTransactionClosed", func(t *testing.T) {
		err := TransactionClosedError{Op: "Test"}
		assert.True(t, IsTransactionClosed(err))
		assert.True(t, IsTransactionClosed(&err))
		assert.False(t, IsTransactionClosed(errors.New("other error")))
		assert.False(t, IsTransactionClosed(nil))
	})

	t.Run("IsTransactionCommitError", func(t *testing.T) {
		err := TransactionCommitError{Cause: errors.New("cause")}
		assert.True(t, IsTransactionCommitError(err))
		assert.True(t, IsTransactionCommitError(&err))
		assert.False(t, IsTransactionCommitError(errors.New("other error")))
		assert.False(t, IsTransactionCommitError(nil))
	})

	t.Run("IsInvalidEntity", func(t *testing.T) {
		err := InvalidEntityError{Reason: "test"}
		assert.True(t, IsInvalidEntity(err))
		assert.True(t, IsInvalidEntity(&err))
		assert.False(t, IsInvalidEntity(errors.New("other error")))
		assert.False(t, IsInvalidEntity(nil))
	})

	t.Run("IsInvalidCondition", func(t *testing.T) {
		err := InvalidConditionError{Reason: "test"}
		assert.True(t, IsInvalidCondition(err))
		assert.True(t, IsInvalidCondition(&err))
		assert.False(t, IsInvalidCondition(errors.New("other error")))
		assert.False(t, IsInvalidCondition(nil))
	})

	t.Run("IsConditionFailed", func(t *testing.T) {
		err := ConditionFailedError{Reason: "test"}
		assert.True(t, IsConditionFailed(err))
		assert.True(t, IsConditionFailed(&err))
		assert.False(t, IsConditionFailed(errors.New("other error")))
		assert.False(t, IsConditionFailed(nil))
	})
}

// TestErrorMessages validates error message formatting.
func TestErrorMessages(t *testing.T) {
	t.Run("TransactionClosedError", func(t *testing.T) {
		err := TransactionClosedError{Op: "Save"}
		assert.Contains(t, err.Error(), "closed")
		assert.Contains(t, err.Error(), "Save")
	})

	t.Run("TransactionCommitError", func(t *testing.T) {
		cause := errors.New("underlying error")
		err := TransactionCommitError{Cause: cause}
		assert.Contains(t, err.Error(), "commit failed")
		assert.Contains(t, err.Error(), "underlying error")
		assert.Equal(t, cause, errors.Unwrap(err))
	})

	t.Run("InvalidEntityError", func(t *testing.T) {
		err := InvalidEntityError{Reason: "nil value"}
		assert.Contains(t, err.Error(), "invalid entity")
		assert.Contains(t, err.Error(), "nil value")
	})

	t.Run("InvalidConditionError", func(t *testing.T) {
		err := InvalidConditionError{Reason: "bad type"}
		assert.Contains(t, err.Error(), "invalid condition")
		assert.Contains(t, err.Error(), "bad type")
	})

	t.Run("ConditionFailedError", func(t *testing.T) {
		cond := Condition{Target: ConditionVersion, Operator: OpEqual, Value: int64(1)}
		err := ConditionFailedError{Condition: cond, Reason: "mismatch"}
		assert.Contains(t, err.Error(), "condition failed")
		assert.Contains(t, err.Error(), "version")
		assert.Contains(t, err.Error(), "mismatch")
	})
}

// TestTransactionWithTimestampedEntity validates condition evaluation with timestamps.
func TestTransactionWithTimestampedEntity(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*TimestampedTestEntity](
		store,
		"timestamped",
		func() *TimestampedTestEntity { return &TimestampedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("CreateConditionWithTimestamp", func(t *testing.T) {
		now := time.Now()
		entity := &TimestampedTestEntity{
			ID:        600,
			Name:      "Original",
			CreatedAt: now,
			UpdatedAt: now,
		}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		updated := &TimestampedTestEntity{
			ID:        600,
			Name:      "Updated",
			CreatedAt: now,
			UpdatedAt: time.Now(),
		}
		err = tx.SaveWithCondition(updated, Condition{
			Target:   ConditionCreate,
			Operator: OpEqual,
			Value:    now.UnixNano(),
		})
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 600)
		require.NoError(t, err)
		assert.Equal(t, "Updated", retrieved.Name)
	})

	t.Run("ModConditionWithTimestamp", func(t *testing.T) {
		now := time.Now()
		entity := &TimestampedTestEntity{
			ID:        601,
			Name:      "Original",
			CreatedAt: now,
			UpdatedAt: now,
		}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		updated := &TimestampedTestEntity{
			ID:        601,
			Name:      "Updated",
			CreatedAt: now,
			UpdatedAt: time.Now(),
		}
		err = tx.SaveWithCondition(updated, Condition{
			Target:   ConditionMod,
			Operator: OpGreaterEqual,
			Value:    now.UnixNano() - int64(time.Second),
		})
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 601)
		require.NoError(t, err)
		assert.Equal(t, "Updated", retrieved.Name)
	})
}

// TestTransactionValueCondition validates value-based conditions.
func TestTransactionValueCondition(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("ValueEqualCondition", func(t *testing.T) {
		entity := &TestEntity{ID: 700, Name: "Alice", Email: "alice@example.com"}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		// Get serialized value for comparison
		gDAO := dao.(*transactionalDAO[*TestEntity])
		data, err := gDAO.codec.Marshal(entity)
		require.NoError(t, err)

		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		updated := &TestEntity{ID: 700, Name: "Alice Updated", Email: "alice@example.com"}
		err = tx.SaveWithCondition(updated, Condition{
			Target:   ConditionValue,
			Operator: OpEqual,
			Value:    data,
		})
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 700)
		require.NoError(t, err)
		assert.Equal(t, "Alice Updated", retrieved.Name)
	})
}

// TestConcurrentTransactions validates concurrent transaction behavior.
func TestConcurrentTransactions(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("IndependentTransactions", func(t *testing.T) {
		// Two independent transactions should not interfere
		tx1, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		tx2, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		err = tx1.Save(&TestEntity{ID: 800, Name: "Tx1User"})
		require.NoError(t, err)

		err = tx2.Save(&TestEntity{ID: 801, Name: "Tx2User"})
		require.NoError(t, err)

		// Commit both
		err = tx1.Commit(ctx)
		require.NoError(t, err)

		err = tx2.Commit(ctx)
		require.NoError(t, err)

		// Verify both entities exist
		_, err = dao.Get(ctx, 800)
		require.NoError(t, err)

		_, err = dao.Get(ctx, 801)
		require.NoError(t, err)
	})
}

// TestTransactionCommit_ThirdOpFailureAppliesNothing proves Commit is
// all-or-nothing: when the third queued op is refused, the first two never
// reach the store (go-qrdb#1737).
func TestTransactionCommit_ThirdOpFailureAppliesNothing(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	dao, err := NewTransactional[*VersionedTestEntity](
		store, "versioned", func() *VersionedTestEntity { return &VersionedTestEntity{} },
	)
	require.NoError(t, err)
	ctx := context.Background()

	existing := &VersionedTestEntity{ID: 900, Name: "Original", Version: 5}
	require.NoError(t, dao.Save(ctx, existing))
	doomed := &VersionedTestEntity{ID: 903, Name: "Doomed", Version: 1}
	require.NoError(t, dao.Save(ctx, doomed))

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Save(&VersionedTestEntity{ID: 901, Name: "First", Version: 1}))
	require.NoError(t, tx.Delete(doomed))
	require.NoError(t, tx.SaveWithCondition(
		&VersionedTestEntity{ID: 900, Name: "Updated", Version: 6},
		Condition{Target: ConditionVersion, Operator: OpEqual, Value: int64(1)},
	))

	err = tx.Commit(ctx)
	require.Error(t, err)
	assert.True(t, IsConditionFailed(err))

	_, err = dao.Get(ctx, 901)
	assert.True(t, IsNotFound(err), "the first op must not have been applied")
	still, err := dao.Get(ctx, 903)
	require.NoError(t, err, "the second op (a delete) must not have been applied")
	assert.Equal(t, "Doomed", still.Name)
	unchanged, err := dao.Get(ctx, 900)
	require.NoError(t, err)
	assert.Equal(t, "Original", unchanged.Name)
}

// changeAfterReadStore is a guarded memory store that, the first time
// changeKey is read, hands back what it read and then writes changeTo in
// its place: a concurrent writer landing between a SaveWithCondition's
// evaluation and the batch.
type changeAfterReadStore struct {
	*kvstoretest.GuardedMemoryStore
	changeKey string
	changeTo  []byte
	changed   bool
}

func (s *changeAfterReadStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.GuardedMemoryStore.Get(ctx, key)
	if err == nil && key == s.changeKey && !s.changed {
		s.changed = true
		if putErr := s.GuardedMemoryStore.Put(ctx, key, s.changeTo); putErr != nil {
			return nil, putErr
		}
	}
	return data, err
}

// TestTransactionCommit_ConditionGuardRefusesConcurrentChange proves a
// SaveWithCondition is guarded on the bytes its condition was evaluated
// against: a record that changes after evaluation refuses the whole batch.
func TestTransactionCommit_ConditionGuardRefusesConcurrentChange(t *testing.T) {
	store := &changeAfterReadStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	dao, err := NewTransactional[*VersionedTestEntity](
		store, "versioned", func() *VersionedTestEntity { return &VersionedTestEntity{} },
	)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, dao.Save(ctx, &VersionedTestEntity{ID: 910, Name: "Original", Version: 5}))
	gDAO := dao.(*transactionalDAO[*VersionedTestEntity])
	concurrent, err := gDAO.codec.Marshal(&VersionedTestEntity{ID: 910, Name: "Concurrent", Version: 6})
	require.NoError(t, err)
	store.changeKey = gDAO.makeKey(910)
	store.changeTo = concurrent

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Save(&VersionedTestEntity{ID: 911, Name: "Sibling", Version: 1}))
	require.NoError(t, tx.SaveWithCondition(
		&VersionedTestEntity{ID: 910, Name: "Mine", Version: 6},
		Condition{Target: ConditionVersion, Operator: OpEqual, Value: int64(5)},
	))

	err = tx.Commit(ctx)
	require.Error(t, err)
	assert.True(t, IsTransactionCommitError(err))
	assert.ErrorIs(t, err, kvstore.ErrNotApplied)
	assert.True(t, store.changed, "the concurrent write must have happened after evaluation")

	_, err = dao.Get(ctx, 911)
	assert.True(t, IsNotFound(err), "the sibling save must not have been applied")
	got, err := dao.Get(ctx, 910)
	require.NoError(t, err)
	assert.Equal(t, "Concurrent", got.Name, "the concurrent writer's record must survive")
}

// TestTransactionCommit_NonGuardedStoreRefused proves Commit, BatchSave and
// WithTx refuse a store without kvstore.MultiGuardedBatchKVStore with
// *AtomicBatchUnsupportedError and write nothing: there is no sequential
// fallback.
func TestTransactionCommit_NonGuardedStoreRefused(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store, "users", func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)
	ctx := context.Background()

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Save(&TestEntity{ID: 920, Name: "A"}))
	require.NoError(t, tx.Save(&TestEntity{ID: 921, Name: "B"}))
	var unsupported *AtomicBatchUnsupportedError
	require.ErrorAs(t, tx.Commit(ctx), &unsupported)

	err = BatchSave(ctx, dao, []*TestEntity{{ID: 922, Name: "C"}, {ID: 923, Name: "D"}})
	require.ErrorAs(t, err, &unsupported)

	err = WithTx(ctx, dao, func(tx DAOTransaction[*TestEntity]) error {
		return tx.Save(&TestEntity{ID: 924, Name: "E"})
	})
	require.ErrorAs(t, err, &unsupported)

	for _, id := range []uint64{920, 921, 922, 923, 924} {
		_, getErr := dao.Get(ctx, id)
		assert.True(t, IsNotFound(getErr), "id %d must not have been written", id)
	}
}
