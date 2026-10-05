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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// OrderEntity is a second entity type for cross-DAO UnitOfWork tests.
type OrderEntity struct {
	ID     uint64  `json:"id"`
	UserID uint64  `json:"user_id"`
	Amount float64 `json:"amount"`
}

func (o *OrderEntity) EntityID() uint64      { return o.ID }
func (o *OrderEntity) SetEntityID(id uint64) { o.ID = id }

// --- UnitOfWork Tests ---

func TestUnitOfWork_CrossDAOSave(t *testing.T) {
	t.Run("CommitsSavesAcrossMultipleDAOs", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		ctx := context.Background()

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		orderDAO, err := New[*OrderEntity](store, "orders", func() *OrderEntity { return &OrderEntity{} })
		require.NoError(t, err)

		user := &TestEntity{ID: 1, Name: "Alice", Email: "alice@test.com"}
		order := &OrderEntity{ID: 100, UserID: 1, Amount: 99.99}

		uow := NewUnitOfWork(store)

		err = RegisterSave(uow, userDAO, user)
		require.NoError(t, err)

		err = RegisterSave(uow, orderDAO, order)
		require.NoError(t, err)

		assert.Equal(t, 2, uow.Len())
		assert.False(t, uow.IsClosed())

		err = uow.Commit(ctx)
		require.NoError(t, err)

		assert.True(t, uow.IsClosed())

		// Verify both entities are persisted
		retrievedUser, err := userDAO.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "Alice", retrievedUser.Name)
		assert.Equal(t, "alice@test.com", retrievedUser.Email)

		retrievedOrder, err := orderDAO.Get(ctx, 100)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), retrievedOrder.UserID)
		assert.InDelta(t, 99.99, retrievedOrder.Amount, 0.001)
	})
}

func TestUnitOfWork_CrossDAOSaveAndDelete(t *testing.T) {
	t.Run("CommitsMixedSaveAndDeleteOperations", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		ctx := context.Background()

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		orderDAO, err := New[*OrderEntity](store, "orders", func() *OrderEntity { return &OrderEntity{} })
		require.NoError(t, err)

		// Pre-populate a user that will be deleted
		existingUser := &TestEntity{ID: 50, Name: "Bob", Email: "bob@test.com"}
		err = userDAO.Save(ctx, existingUser)
		require.NoError(t, err)

		// Create new order and delete existing user in one UoW
		newOrder := &OrderEntity{ID: 200, UserID: 50, Amount: 42.00}

		uow := NewUnitOfWork(store)

		err = RegisterSave(uow, orderDAO, newOrder)
		require.NoError(t, err)

		err = RegisterDelete(uow, userDAO, existingUser)
		require.NoError(t, err)

		assert.Equal(t, 2, uow.Len())

		err = uow.Commit(ctx)
		require.NoError(t, err)

		// Verify order was saved
		retrievedOrder, err := orderDAO.Get(ctx, 200)
		require.NoError(t, err)
		assert.InDelta(t, 42.00, retrievedOrder.Amount, 0.001)

		// Verify user was deleted
		_, err = userDAO.Get(ctx, 50)
		require.Error(t, err)
		assert.True(t, IsNotFound(err))
	})
}

func TestUnitOfWork_Rollback(t *testing.T) {
	t.Run("RollbackDiscardsAllOperations", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		orderDAO, err := New[*OrderEntity](store, "orders", func() *OrderEntity { return &OrderEntity{} })
		require.NoError(t, err)

		user := &TestEntity{ID: 1, Name: "Alice", Email: "alice@test.com"}
		order := &OrderEntity{ID: 100, UserID: 1, Amount: 99.99}

		uow := NewUnitOfWork(store)

		err = RegisterSave(uow, userDAO, user)
		require.NoError(t, err)
		err = RegisterSave(uow, orderDAO, order)
		require.NoError(t, err)

		// Rollback instead of commit
		uow.Rollback()

		assert.True(t, uow.IsClosed())

		// Verify nothing was persisted
		_, err = userDAO.Get(ctx, 1)
		require.Error(t, err)
		assert.True(t, IsNotFound(err))

		_, err = orderDAO.Get(ctx, 100)
		require.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("RollbackIsIdempotent", func(t *testing.T) {
		store := newMemoryKVStore()
		uow := NewUnitOfWork(store)

		uow.Rollback()
		uow.Rollback() // Should not panic
		assert.True(t, uow.IsClosed())
	})

	t.Run("RollbackAfterCommitIsNoOp", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		uow := NewUnitOfWork(store)
		err := uow.Commit(ctx)
		require.NoError(t, err)

		uow.Rollback() // Should not panic
		assert.True(t, uow.IsClosed())
	})
}

func TestUnitOfWork_CommitOnClosedUoW(t *testing.T) {
	t.Run("CommitAfterCommitReturnsError", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		ctx := context.Background()

		uow := NewUnitOfWork(store)

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		user := &TestEntity{ID: 1, Name: "Alice", Email: "alice@test.com"}
		err = RegisterSave(uow, userDAO, user)
		require.NoError(t, err)

		err = uow.Commit(ctx)
		require.NoError(t, err)

		// Second commit should fail
		err = uow.Commit(ctx)
		require.Error(t, err)
		assert.True(t, IsUnitOfWorkClosed(err))
	})

	t.Run("CommitAfterRollbackReturnsError", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		uow := NewUnitOfWork(store)
		uow.Rollback()

		err := uow.Commit(ctx)
		require.Error(t, err)
		assert.True(t, IsUnitOfWorkClosed(err))
	})
}

func TestUnitOfWork_EmptyCommit(t *testing.T) {
	t.Run("EmptyUnitOfWorkCommitSucceeds", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		uow := NewUnitOfWork(store)
		assert.Equal(t, 0, uow.Len())

		err := uow.Commit(ctx)
		require.NoError(t, err)
		assert.True(t, uow.IsClosed())
	})
}

func TestUnitOfWork_RegisterOnClosedUoW(t *testing.T) {
	t.Run("RegisterSaveOnClosedUoWReturnsError", func(t *testing.T) {
		store := newMemoryKVStore()

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		uow := NewUnitOfWork(store)
		uow.Rollback()

		user := &TestEntity{ID: 1, Name: "Alice", Email: "alice@test.com"}
		err = RegisterSave(uow, userDAO, user)
		require.Error(t, err)
		assert.True(t, IsUnitOfWorkClosed(err))
	})

	t.Run("RegisterDeleteOnClosedUoWReturnsError", func(t *testing.T) {
		store := newMemoryKVStore()

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		uow := NewUnitOfWork(store)
		uow.Rollback()

		user := &TestEntity{ID: 1, Name: "Alice", Email: "alice@test.com"}
		err = RegisterDelete(uow, userDAO, user)
		require.Error(t, err)
		assert.True(t, IsUnitOfWorkClosed(err))
	})
}

func TestUnitOfWork_RegisterDeleteWithZeroID(t *testing.T) {
	t.Run("ReturnsInvalidEntityIDError", func(t *testing.T) {
		store := newMemoryKVStore()

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		uow := NewUnitOfWork(store)

		user := &TestEntity{ID: 0, Name: "NoID"}
		err = RegisterDelete(uow, userDAO, user)
		require.Error(t, err)

		var idErr InvalidEntityIDError
		assert.True(t, errors.As(err, &idErr))
	})
}

func TestUnitOfWork_AutoIDGeneration(t *testing.T) {
	t.Run("AutoGeneratesIDForNewEntities", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		ctx := context.Background()

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		user := &TestEntity{Name: "NoID", Email: "noid@test.com"}
		assert.Equal(t, uint64(0), user.EntityID())

		uow := NewUnitOfWork(store)
		err = RegisterSave(uow, userDAO, user)
		require.NoError(t, err)

		// ID should be set after RegisterSave
		assert.NotEqual(t, uint64(0), user.EntityID())

		err = uow.Commit(ctx)
		require.NoError(t, err)

		// Verify entity is retrievable by auto-generated ID
		retrieved, err := userDAO.Get(ctx, user.EntityID())
		require.NoError(t, err)
		assert.Equal(t, "NoID", retrieved.Name)
	})
}

func TestUnitOfWork_WithTimeout(t *testing.T) {
	t.Run("CustomTimeoutIsApplied", func(t *testing.T) {
		store := newMemoryKVStore()

		uow := NewUnitOfWork(store, WithUnitOfWorkTimeout(5*time.Second))
		assert.Equal(t, 5*time.Second, uow.timeout)
	})

	t.Run("DefaultTimeoutIsUsed", func(t *testing.T) {
		store := newMemoryKVStore()

		uow := NewUnitOfWork(store)
		assert.Equal(t, defaultUnitOfWorkTimeout, uow.timeout)
	})
}

// TestUnitOfWork_CommitRefusesOverANonGuardedStoreEvenMidSequence proves
// Commit refuses a multi-op batch outright -- *AtomicBatchUnsupportedError,
// nothing written -- the moment the store does not implement
// kvstore.MultiGuardedBatchKVStore. Before go-qrdb#589's coordinator ruling
// this scenario (a save followed by another op) exercised a
// sequential-apply-and-compensate fallback instead; that path is removed
// entirely, so there is nothing left to compensate -- see
// unit_of_work_atomic_test.go for the atomic-store failure mode (a
// mid-batch unique-index collision) that replaces it.
func TestUnitOfWork_CommitRefusesOverANonGuardedStoreEvenMidSequence(t *testing.T) {
	store := newMemoryKVStore()
	ctx := context.Background()

	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
	require.NoError(t, err)
	orderDAO, err := New[*OrderEntity](store, "orders", func() *OrderEntity { return &OrderEntity{} })
	require.NoError(t, err)

	existingUser := &TestEntity{ID: 1, Name: "Alice", Email: "alice@test.com"}
	require.NoError(t, userDAO.Save(ctx, existingUser))

	uow := NewUnitOfWork(store)
	require.NoError(t, RegisterDelete(uow, userDAO, existingUser))
	require.NoError(t, RegisterSave(uow, orderDAO, &OrderEntity{ID: 100, UserID: 1, Amount: 50.00}))

	err = uow.Commit(ctx)
	var unsupported *AtomicBatchUnsupportedError
	require.ErrorAs(t, err, &unsupported)

	// Neither queued operation reached the store.
	restored, getErr := store.Get(ctx, "users/1")
	require.NoError(t, getErr)
	require.NotNil(t, restored, "a refused Commit must not have deleted the user")
	_, getErr = store.Get(ctx, "orders/100")
	require.Error(t, getErr, "a refused Commit must not have saved the order")
}

func TestUnitOfWork_TransactionalDAOExtraction(t *testing.T) {
	t.Run("WorksWithTransactionalDAO", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		ctx := context.Background()

		txDAO, err := NewTransactional[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		uow := NewUnitOfWork(store)

		user := &TestEntity{ID: 1, Name: "FromTxDAO", Email: "tx@test.com"}
		err = RegisterSave(uow, txDAO, user)
		require.NoError(t, err)

		err = uow.Commit(ctx)
		require.NoError(t, err)

		retrieved, err := txDAO.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "FromTxDAO", retrieved.Name)
	})
}

func TestUnitOfWork_MultipleSavesAndDeletes(t *testing.T) {
	t.Run("HandlesManyOperations", func(t *testing.T) {
		store := kvstoretest.NewGuardedMemoryStore()
		ctx := context.Background()

		userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		// Pre-populate users to delete
		for i := uint64(1); i <= 5; i++ {
			user := &TestEntity{ID: i, Name: "Delete", Email: "delete@test.com"}
			err = userDAO.Save(ctx, user)
			require.NoError(t, err)
		}

		uow := NewUnitOfWork(store)

		// Delete 5 existing users
		for i := uint64(1); i <= 5; i++ {
			err = RegisterDelete(uow, userDAO, &TestEntity{ID: i})
			require.NoError(t, err)
		}

		// Save 5 new users
		for i := uint64(10); i <= 14; i++ {
			user := &TestEntity{ID: i, Name: "New", Email: "new@test.com"}
			err = RegisterSave(uow, userDAO, user)
			require.NoError(t, err)
		}

		assert.Equal(t, 10, uow.Len())

		err = uow.Commit(ctx)
		require.NoError(t, err)

		// Verify old users are gone
		for i := uint64(1); i <= 5; i++ {
			_, err = userDAO.Get(ctx, i)
			require.Error(t, err)
			assert.True(t, IsNotFound(err))
		}

		// Verify new users exist
		for i := uint64(10); i <= 14; i++ {
			retrieved, err := userDAO.Get(ctx, i)
			require.NoError(t, err)
			assert.Equal(t, "New", retrieved.Name)
		}
	})
}

// --- SaveWithCondition on GenericDAO Tests ---

func TestGenericDAO_SaveWithCondition(t *testing.T) {
	t.Run("SaveWithVersionConditionSucceeds", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		// VersionedTestEntity, not TestEntity: this subtest is named for a
		// version condition SUCCEEDING, and it used to get that from the
		// fabricated version-1 constant rather than from a stored version, so
		// it passed against an entity type that has no version at all
		// (go-qrdb#59).
		dao, err := New[*VersionedTestEntity](store, "users",
			func() *VersionedTestEntity { return &VersionedTestEntity{} })
		require.NoError(t, err)

		// Save initial entity
		user := &VersionedTestEntity{ID: 1, Name: "Alice", Version: 1}
		err = dao.Save(ctx, user)
		require.NoError(t, err)

		updated := &VersionedTestEntity{ID: 1, Name: "Alice Updated", Version: 2}
		cond := Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(1),
		}
		err = dao.SaveWithCondition(ctx, updated, cond)
		require.NoError(t, err)

		// Verify update
		retrieved, err := dao.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "Alice Updated", retrieved.Name)
		assert.Equal(t, int64(2), retrieved.Version)
	})

	t.Run("SaveWithConditionFailsWhenConditionNotMet", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		// Also VersionedTestEntity, so the refusal comes from the STORED
		// version disagreeing with the expectation. Against a non-Versioned
		// entity this subtest passes either way — before the fix because 999
		// != the constant 1, after it because the condition is inevaluable —
		// and neither outcome says anything about version comparison
		// (go-qrdb#59).
		dao, err := New[*VersionedTestEntity](store, "users",
			func() *VersionedTestEntity { return &VersionedTestEntity{} })
		require.NoError(t, err)

		// Save initial entity
		user := &VersionedTestEntity{ID: 1, Name: "Alice", Version: 1}
		err = dao.Save(ctx, user)
		require.NoError(t, err)

		// Try to save with wrong version
		updated := &VersionedTestEntity{ID: 1, Name: "Should Not Save", Version: 1000}
		cond := Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(999),
		}
		err = dao.SaveWithCondition(ctx, updated, cond)
		require.Error(t, err)
		assert.True(t, IsConditionFailed(err))

		// Verify original was NOT overwritten
		retrieved, err := dao.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "Alice", retrieved.Name)
	})

	t.Run("SaveWithConditionAutoGeneratesID", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		dao, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		user := &TestEntity{Name: "NewUser", Email: "new@test.com"}
		cond := Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(0),
		}
		err = dao.SaveWithCondition(ctx, user, cond)
		require.NoError(t, err)

		assert.NotEqual(t, uint64(0), user.EntityID())

		retrieved, err := dao.Get(ctx, user.EntityID())
		require.NoError(t, err)
		assert.Equal(t, "NewUser", retrieved.Name)
	})

	t.Run("SaveWithInvalidConditionReturnsError", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		dao, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		user := &TestEntity{ID: 1, Name: "Test", Email: "test@test.com"}
		cond := Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    "not-a-number", // Invalid: version requires numeric
		}
		err = dao.SaveWithCondition(ctx, user, cond)
		require.Error(t, err)
		assert.True(t, IsInvalidCondition(err))
	})

	t.Run("SaveWithValueConditionSucceeds", func(t *testing.T) {
		store := newMemoryKVStore()
		ctx := context.Background()

		dao, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)

		// For a new entity with value condition checking against nil
		user := &TestEntity{ID: 1, Name: "Alice", Email: "alice@test.com"}
		cond := Condition{
			Target:   ConditionValue,
			Operator: OpEqual,
			Value:    nil, // Expect no existing value (new entity)
		}
		err = dao.SaveWithCondition(ctx, user, cond)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "Alice", retrieved.Name)
	})
}

// --- Error type tests ---

func TestUnitOfWorkClosedError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		err := UnitOfWorkClosedError{Op: "Commit"}
		assert.Contains(t, err.Error(), "Commit")
		assert.Contains(t, err.Error(), "closed")
	})

	t.Run("IsUnitOfWorkClosed", func(t *testing.T) {
		err := UnitOfWorkClosedError{Op: "RegisterSave"}
		assert.True(t, IsUnitOfWorkClosed(err))
		assert.False(t, IsUnitOfWorkClosed(errors.New("other error")))
	})
}

func TestUnitOfWorkCommitError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		cause := errors.New("put failed")
		err := UnitOfWorkCommitError{OpIndex: 2, Key: "users/1", Cause: cause}
		assert.Contains(t, err.Error(), "operation 2")
		assert.Contains(t, err.Error(), "users/1")
		assert.Contains(t, err.Error(), "put failed")
	})

	t.Run("Unwrap", func(t *testing.T) {
		cause := errors.New("underlying error")
		err := UnitOfWorkCommitError{OpIndex: 0, Key: "k", Cause: cause}
		assert.Equal(t, cause, errors.Unwrap(err))
	})

	t.Run("IsUnitOfWorkCommitError", func(t *testing.T) {
		err := UnitOfWorkCommitError{OpIndex: 0, Key: "k", Cause: errors.New("e")}
		assert.True(t, IsUnitOfWorkCommitError(err))
		assert.False(t, IsUnitOfWorkCommitError(errors.New("other")))
	})
}

