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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// VersionedTestEntity implements both Entity and Versioned interfaces.
type VersionedTestEntity struct {
	ID      uint64 `json:"id"`
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

func (e *VersionedTestEntity) EntityID() uint64      { return e.ID }
func (e *VersionedTestEntity) SetEntityID(id uint64) { e.ID = id }
func (e *VersionedTestEntity) GetVersion() int64     { return e.Version }
func (e *VersionedTestEntity) SetVersion(v int64)    { e.Version = v }

// TimestampedTestEntity implements both Entity and Timestamped interfaces.
type TimestampedTestEntity struct {
	ID        uint64    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (e *TimestampedTestEntity) EntityID() uint64         { return e.ID }
func (e *TimestampedTestEntity) SetEntityID(id uint64)    { e.ID = id }
func (e *TimestampedTestEntity) GetCreatedAt() time.Time  { return e.CreatedAt }
func (e *TimestampedTestEntity) GetUpdatedAt() time.Time  { return e.UpdatedAt }
func (e *TimestampedTestEntity) SetCreatedAt(t time.Time) { e.CreatedAt = t }
func (e *TimestampedTestEntity) SetUpdatedAt(t time.Time) { e.UpdatedAt = t }

// TestConditionTargetString validates ConditionTarget.String().
func TestConditionTargetString(t *testing.T) {
	tests := []struct {
		name     string
		target   ConditionTarget
		expected string
	}{
		{
			name:     "Version",
			target:   ConditionVersion,
			expected: "version",
		},
		{
			name:     "Create",
			target:   ConditionCreate,
			expected: "create",
		},
		{
			name:     "Mod",
			target:   ConditionMod,
			expected: "mod",
		},
		{
			name:     "Value",
			target:   ConditionValue,
			expected: "value",
		},
		{
			name:     "Unknown",
			target:   ConditionTarget(99),
			expected: "unknown(99)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.target.String())
		})
	}
}

// TestConditionOpString validates ConditionOp.String().
func TestConditionOpString(t *testing.T) {
	tests := []struct {
		name     string
		op       ConditionOp
		expected string
	}{
		{
			name:     "Equal",
			op:       OpEqual,
			expected: "==",
		},
		{
			name:     "NotEqual",
			op:       OpNotEqual,
			expected: "!=",
		},
		{
			name:     "Greater",
			op:       OpGreater,
			expected: ">",
		},
		{
			name:     "Less",
			op:       OpLess,
			expected: "<",
		},
		{
			name:     "GreaterEqual",
			op:       OpGreaterEqual,
			expected: ">=",
		},
		{
			name:     "LessEqual",
			op:       OpLessEqual,
			expected: "<=",
		},
		{
			name:     "Unknown",
			op:       ConditionOp(99),
			expected: "unknown(99)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.op.String())
		})
	}
}

// TestConditionString validates Condition.String().
func TestConditionString(t *testing.T) {
	cond := Condition{
		Target:   ConditionVersion,
		Operator: OpEqual,
		Value:    int64(5),
	}

	result := cond.String()
	assert.Contains(t, result, "version")
	assert.Contains(t, result, "==")
	assert.Contains(t, result, "5")
}

// TestConditionValidate validates Condition.Validate().
func TestConditionValidate(t *testing.T) {
	t.Run("ValidVersionCondition", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(1),
		}
		err := cond.Validate()
		assert.NoError(t, err)
	})

	t.Run("ValidCreateCondition", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionCreate,
			Operator: OpGreater,
			Value:    int64(0),
		}
		err := cond.Validate()
		assert.NoError(t, err)
	})

	t.Run("ValidModCondition", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionMod,
			Operator: OpLess,
			Value:    uint64(100),
		}
		err := cond.Validate()
		assert.NoError(t, err)
	})

	t.Run("ValidValueConditionString", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionValue,
			Operator: OpEqual,
			Value:    "test",
		}
		err := cond.Validate()
		assert.NoError(t, err)
	})

	t.Run("ValidValueConditionBytes", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionValue,
			Operator: OpNotEqual,
			Value:    []byte("test"),
		}
		err := cond.Validate()
		assert.NoError(t, err)
	})

	t.Run("ValidValueConditionNil", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionValue,
			Operator: OpEqual,
			Value:    nil,
		}
		err := cond.Validate()
		assert.NoError(t, err)
	})

	t.Run("InvalidVersionConditionWrongType", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    "not a number",
		}
		err := cond.Validate()
		assert.Error(t, err)
		assert.True(t, IsInvalidCondition(err))
	})

	t.Run("InvalidValueConditionWrongType", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionValue,
			Operator: OpEqual,
			Value:    12345, // int is not valid for value comparison
		}
		err := cond.Validate()
		assert.Error(t, err)
		assert.True(t, IsInvalidCondition(err))
	})

	t.Run("InvalidTarget", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionTarget(99),
			Operator: OpEqual,
			Value:    int64(1),
		}
		err := cond.Validate()
		assert.Error(t, err)
		assert.True(t, IsInvalidCondition(err))
	})

	t.Run("InvalidOperator", func(t *testing.T) {
		cond := Condition{
			Target:   ConditionVersion,
			Operator: ConditionOp(99),
			Value:    int64(1),
		}
		err := cond.Validate()
		assert.Error(t, err)
		assert.True(t, IsInvalidCondition(err))
	})
}

// TestToInt64 validates the toInt64 helper function.
func TestToInt64(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected int64
	}{
		{"int", int(42), 42},
		{"int32", int32(42), 42},
		{"int64", int64(42), 42},
		{"uint", uint(42), 42},
		{"uint32", uint32(42), 42},
		{"uint64", uint64(42), 42},
		{"string", "not a number", 0},
		{"nil", nil, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := toInt64(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestBytesEqual validates the bytesEqual helper function.
func TestBytesEqual(t *testing.T) {
	t.Run("EqualSlices", func(t *testing.T) {
		a := []byte("hello")
		b := []byte("hello")
		assert.True(t, bytesEqual(a, b))
	})

	t.Run("UnequalSlices", func(t *testing.T) {
		a := []byte("hello")
		b := []byte("world")
		assert.False(t, bytesEqual(a, b))
	})

	t.Run("DifferentLengths", func(t *testing.T) {
		a := []byte("hello")
		b := []byte("hi")
		assert.False(t, bytesEqual(a, b))
	})

	t.Run("BothEmpty", func(t *testing.T) {
		a := []byte{}
		b := []byte{}
		assert.True(t, bytesEqual(a, b))
	})

	t.Run("OneEmpty", func(t *testing.T) {
		a := []byte("hello")
		b := []byte{}
		assert.False(t, bytesEqual(a, b))
	})
}

// TestBytesCompare validates the bytesCompare helper function.
func TestBytesCompare(t *testing.T) {
	t.Run("FirstLess", func(t *testing.T) {
		a := []byte("abc")
		b := []byte("def")
		assert.Equal(t, -1, bytesCompare(a, b))
	})

	t.Run("FirstGreater", func(t *testing.T) {
		a := []byte("def")
		b := []byte("abc")
		assert.Equal(t, 1, bytesCompare(a, b))
	})

	t.Run("Equal", func(t *testing.T) {
		a := []byte("abc")
		b := []byte("abc")
		assert.Equal(t, 0, bytesCompare(a, b))
	})

	t.Run("ShorterIsLess", func(t *testing.T) {
		a := []byte("ab")
		b := []byte("abc")
		assert.Equal(t, -1, bytesCompare(a, b))
	})

	t.Run("LongerIsGreater", func(t *testing.T) {
		a := []byte("abc")
		b := []byte("ab")
		assert.Equal(t, 1, bytesCompare(a, b))
	})
}

// TestTransactionSaveAndGet validates Save and Get operations in a transaction.
func TestTransactionSaveAndGet(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("SaveAndGetInTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		entity := &TestEntity{ID: 1, Name: "Alice", Email: "alice@example.com"}
		err = tx.Save(entity)
		require.NoError(t, err)

		// Read-your-own-writes
		retrieved, err := tx.Get(1)
		require.NoError(t, err)
		assert.Equal(t, "Alice", retrieved.Name)

		err = tx.Commit(ctx)
		require.NoError(t, err)
	})

	t.Run("SaveWithAutoGeneratedID", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		entity := &TestEntity{Name: "Bob", Email: "bob@example.com"}
		err = tx.Save(entity)
		require.NoError(t, err)

		assert.NotZero(t, entity.ID)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		// Verify persisted
		retrieved, err := dao.Get(ctx, entity.ID)
		require.NoError(t, err)
		assert.Equal(t, "Bob", retrieved.Name)
	})
}

// TestTransactionDelete validates Delete operations in a transaction.
func TestTransactionDelete(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// First create an entity
	entity := &TestEntity{ID: 100, Name: "ToDelete", Email: "delete@example.com"}
	err = dao.Save(ctx, entity)
	require.NoError(t, err)

	t.Run("DeleteInTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		err = tx.Delete(entity)
		require.NoError(t, err)

		// Read-your-own-deletes
		_, err = tx.Get(100)
		assert.Error(t, err)
		assert.True(t, IsNotFound(err))

		err = tx.Commit(ctx)
		require.NoError(t, err)

		// Verify deleted
		_, err = dao.Get(ctx, 100)
		assert.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("DeleteEntityWithZeroID", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		entity := &TestEntity{ID: 0, Name: "NoID"}
		err = tx.Delete(entity)
		assert.Error(t, err)
		var idErr InvalidEntityIDError
		assert.ErrorAs(t, err, &idErr)
	})
}

// TestTransactionRollback validates Rollback operations.
func TestTransactionRollback(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("RollbackDiscardsChanges", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		entity := &TestEntity{ID: 200, Name: "WillBeRolledBack"}
		err = tx.Save(entity)
		require.NoError(t, err)

		err = tx.Rollback()
		require.NoError(t, err)

		// Entity should not exist
		_, err = dao.Get(ctx, 200)
		assert.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("RollbackIsIdempotent", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		err = tx.Rollback()
		require.NoError(t, err)

		err = tx.Rollback()
		require.NoError(t, err)
	})

	t.Run("RollbackAfterCommitIsNoop", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		entity := &TestEntity{ID: 201, Name: "Committed"}
		err = tx.Save(entity)
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		err = tx.Rollback()
		require.NoError(t, err)

		// Entity should exist
		retrieved, err := dao.Get(ctx, 201)
		require.NoError(t, err)
		assert.Equal(t, "Committed", retrieved.Name)
	})
}

// TestTransactionClosedOperations validates operations on closed transactions.
func TestTransactionClosedOperations(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("SaveOnClosedTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		require.NoError(t, err)

		err = tx.Save(&TestEntity{ID: 1, Name: "Test"})
		assert.Error(t, err)
		assert.True(t, IsTransactionClosed(err))
	})

	t.Run("DeleteOnClosedTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		require.NoError(t, err)

		err = tx.Delete(&TestEntity{ID: 1, Name: "Test"})
		assert.Error(t, err)
		assert.True(t, IsTransactionClosed(err))
	})

	t.Run("GetOnClosedTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		require.NoError(t, err)

		_, err = tx.Get(1)
		assert.Error(t, err)
		assert.True(t, IsTransactionClosed(err))
	})

	t.Run("SaveWithConditionOnClosedTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		require.NoError(t, err)

		err = tx.SaveWithCondition(&TestEntity{ID: 1}, Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(1),
		})
		assert.Error(t, err)
		assert.True(t, IsTransactionClosed(err))
	})

	t.Run("CommitOnClosedTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		require.NoError(t, err)

		err = tx.Commit(ctx)
		assert.Error(t, err)
		assert.True(t, IsTransactionClosed(err))
	})

	t.Run("IsClosed", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		assert.False(t, tx.IsClosed())

		err = tx.Commit(ctx)
		require.NoError(t, err)

		assert.True(t, tx.IsClosed())
	})
}

// TestTransactionNilEntity validates handling of nil entities.
func TestTransactionNilEntity(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	t.Run("SaveNilEntity", func(t *testing.T) {
		err := tx.Save(nil)
		assert.Error(t, err)
		assert.True(t, IsInvalidEntity(err))
	})

	t.Run("DeleteNilEntity", func(t *testing.T) {
		err := tx.Delete(nil)
		assert.Error(t, err)
		assert.True(t, IsInvalidEntity(err))
	})

	t.Run("SaveWithConditionNilEntity", func(t *testing.T) {
		err := tx.SaveWithCondition(nil, Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(0),
		})
		assert.Error(t, err)
		assert.True(t, IsInvalidEntity(err))
	})
}

// TestIsNil pins that a non-pointer entity value is never treated as nil.
func TestIsNil(t *testing.T) {
	assert.False(t, isNil(TestEntity{ID: 1, Name: "test"}))
}

// TestTransactionSaveWithCondition validates conditional save operations.
func TestTransactionSaveWithCondition(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*VersionedTestEntity](
		store,
		"versioned",
		func() *VersionedTestEntity { return &VersionedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("SaveWithVersionConditionMatch", func(t *testing.T) {
		// Create initial entity
		entity := &VersionedTestEntity{ID: 300, Name: "Original", Version: 1}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		updated := &VersionedTestEntity{ID: 300, Name: "Updated", Version: 2}
		err = tx.SaveWithCondition(updated, Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(1),
		})
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		// Verify update
		retrieved, err := dao.Get(ctx, 300)
		require.NoError(t, err)
		assert.Equal(t, "Updated", retrieved.Name)
	})

	t.Run("SaveWithVersionConditionMismatch", func(t *testing.T) {
		// Create initial entity
		entity := &VersionedTestEntity{ID: 301, Name: "Original", Version: 5}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		updated := &VersionedTestEntity{ID: 301, Name: "Updated", Version: 6}
		err = tx.SaveWithCondition(updated, Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(1), // Wrong version
		})
		require.NoError(t, err)

		err = tx.Commit(ctx)
		assert.Error(t, err)
		assert.True(t, IsConditionFailed(err))

		// Verify not updated
		retrieved, err := dao.Get(ctx, 301)
		require.NoError(t, err)
		assert.Equal(t, "Original", retrieved.Name)
	})

	t.Run("SaveWithInvalidCondition", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		entity := &VersionedTestEntity{ID: 302, Name: "Test"}
		err = tx.SaveWithCondition(entity, Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    "not a number", // Invalid
		})
		assert.Error(t, err)
		assert.True(t, IsInvalidCondition(err))
	})

	t.Run("SaveNewEntityWithVersionZero", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		entity := &VersionedTestEntity{ID: 303, Name: "New", Version: 1}
		err = tx.SaveWithCondition(entity, Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    int64(0), // Expect version 0 for new entity
		})
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 303)
		require.NoError(t, err)
		assert.Equal(t, "New", retrieved.Name)
	})
}

func TestTransactionConditionEvaluation(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"cond_eval",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	commit := func(t *testing.T, entity *TestEntity, cond Condition) error {
		t.Helper()
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		require.NoError(t, tx.SaveWithCondition(entity, cond), "queuing the operation must not fail")
		return tx.Commit(ctx)
	}
	requireRefused := func(t *testing.T, err error, reason string) {
		t.Helper()
		var failed ConditionFailedError
		require.ErrorAs(t, err, &failed)
		assert.Contains(t, failed.Reason, reason)
	}
	requireName := func(t *testing.T, id uint64, want string) {
		t.Helper()
		retrieved, err := dao.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, want, retrieved.Name)
	}

	t.Run("CreateAndModOnNonTimestampedRefused", func(t *testing.T) {
		require.NoError(t, dao.Save(ctx, &TestEntity{ID: 2001, Name: "Original"}))
		for _, target := range []ConditionTarget{ConditionCreate, ConditionMod} {
			err := commit(t, &TestEntity{ID: 2001, Name: "Updated"},
				Condition{Target: target, Operator: OpEqual, Value: int64(0)})
			requireRefused(t, err, "does not implement Timestamped")
		}
		requireName(t, 2001, "Original")
	})

	t.Run("VersionOnNonVersionedRefused", func(t *testing.T) {
		require.NoError(t, dao.Save(ctx, &TestEntity{ID: 10001, Name: "Original"}))
		// Every comparison value is refused, not just the ones that used to
		// fail: OpEqual 1 is the value a substituted constant silently
		// satisfied, so it is the case that distinguishes the refusal.
		for _, want := range []int64{0, 1, 2} {
			err := commit(t, &TestEntity{ID: 10001, Name: "Updated"},
				Condition{Target: ConditionVersion, Operator: OpEqual, Value: want})
			requireRefused(t, err, "does not implement Versioned")
		}
		requireName(t, 10001, "Original")
	})

	t.Run("CreateAndModOnNewEntityCompareAsZero", func(t *testing.T) {
		for i, target := range []ConditionTarget{ConditionCreate, ConditionMod} {
			passID, failID := uint64(9001+2*i), uint64(9002+2*i)
			require.NoError(t, commit(t, &TestEntity{ID: passID, Name: "New"},
				Condition{Target: target, Operator: OpEqual, Value: int64(0)}))
			requireName(t, passID, "New")

			err := commit(t, &TestEntity{ID: failID, Name: "New"},
				Condition{Target: target, Operator: OpEqual, Value: int64(1)})
			requireRefused(t, err, "")
			_, err = dao.Get(ctx, failID)
			assert.True(t, IsNotFound(err), "a refused guard must not write")
		}
	})

	t.Run("ValueOnNewEntityComparesAgainstNil", func(t *testing.T) {
		require.NoError(t, commit(t, &TestEntity{ID: 6001, Name: "New"},
			Condition{Target: ConditionValue, Operator: OpEqual, Value: []byte(nil)}))
		requireName(t, 6001, "New")

		err := commit(t, &TestEntity{ID: 6002, Name: "New"},
			Condition{Target: ConditionValue, Operator: OpNotEqual, Value: nil})
		requireRefused(t, err, "value does not satisfy condition")
	})

	t.Run("ValueOperatorsCompareStoredBytes", func(t *testing.T) {
		// The stored value is a JSON object, so its bytes sort above the
		// empty string and below 0xff.
		require.NoError(t, dao.Save(ctx, &TestEntity{ID: 8001, Name: "Original"}))
		tests := []struct {
			name  string
			op    ConditionOp
			value []byte
			pass  bool
		}{
			{"NotEqualOther", OpNotEqual, []byte("something_completely_different"), true},
			{"GreaterThanEmpty", OpGreater, []byte(""), true},
			{"LessThanHigh", OpLess, []byte("\xff"), true},
			{"GreaterEqualHigh", OpGreaterEqual, []byte("\xff"), false},
			{"LessEqualEmpty", OpLessEqual, []byte(""), false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := commit(t, &TestEntity{ID: 8001, Name: tt.name},
					Condition{Target: ConditionValue, Operator: tt.op, Value: tt.value})
				if tt.pass {
					require.NoError(t, err)
					requireName(t, 8001, tt.name)
					return
				}
				requireRefused(t, err, "value does not satisfy condition")
				retrieved, getErr := dao.Get(ctx, 8001)
				require.NoError(t, getErr)
				assert.NotEqual(t, tt.name, retrieved.Name, "a refused guard must not write")
			})
		}
	})
}

// TestMultiEntityTransaction validates multi-entity atomic commits.
func TestMultiEntityTransaction(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("AtomicMultiSave", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		err = tx.Save(&TestEntity{ID: 400, Name: "User1"})
		require.NoError(t, err)
		err = tx.Save(&TestEntity{ID: 401, Name: "User2"})
		require.NoError(t, err)
		err = tx.Save(&TestEntity{ID: 402, Name: "User3"})
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		// Verify all saved
		for id := uint64(400); id <= 402; id++ {
			_, err := dao.Get(ctx, id)
			require.NoError(t, err)
		}
	})

	t.Run("MixedSaveAndDelete", func(t *testing.T) {
		// Create entity to delete
		toDelete := &TestEntity{ID: 500, Name: "ToDelete"}
		err := dao.Save(ctx, toDelete)
		require.NoError(t, err)

		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		err = tx.Save(&TestEntity{ID: 501, Name: "NewUser"})
		require.NoError(t, err)
		err = tx.Delete(toDelete)
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)

		// Verify new entity exists
		_, err = dao.Get(ctx, 501)
		require.NoError(t, err)

		// Verify deleted entity is gone
		_, err = dao.Get(ctx, 500)
		assert.Error(t, err)
		assert.True(t, IsNotFound(err))
	})
}

// TestTransactionReadYourOwnWrites validates read-your-own-writes semantics.
func TestTransactionReadYourOwnWrites(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("ReadAfterSave", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		entity := &TestEntity{ID: 600, Name: "FirstSave"}
		err = tx.Save(entity)
		require.NoError(t, err)

		// Read should return cached value
		retrieved, err := tx.Get(600)
		require.NoError(t, err)
		assert.Equal(t, "FirstSave", retrieved.Name)

		// Update
		entity.Name = "SecondSave"
		err = tx.Save(entity)
		require.NoError(t, err)

		// Read should return updated cached value
		retrieved, err = tx.Get(600)
		require.NoError(t, err)
		assert.Equal(t, "SecondSave", retrieved.Name)
	})

	t.Run("ReadAfterDelete", func(t *testing.T) {
		// Pre-create entity
		entity := &TestEntity{ID: 601, Name: "ToDelete"}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		err = tx.Delete(entity)
		require.NoError(t, err)

		// Read should return not found
		_, err = tx.Get(601)
		assert.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("ReadFallsBackToStorage", func(t *testing.T) {
		// Pre-create entity
		entity := &TestEntity{ID: 602, Name: "InStorage"}
		err := dao.Save(ctx, entity)
		require.NoError(t, err)

		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()

		// Read without prior save should return from storage
		retrieved, err := tx.Get(602)
		require.NoError(t, err)
		assert.Equal(t, "InStorage", retrieved.Name)
	})
}

// TestEmptyTransaction validates behavior of empty transactions.
func TestEmptyTransaction(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("CommitEmptyTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		err = tx.Commit(ctx)
		require.NoError(t, err)
	})

	t.Run("RollbackEmptyTransaction", func(t *testing.T) {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		err = tx.Rollback()
		require.NoError(t, err)
	})
}
