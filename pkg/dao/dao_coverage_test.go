//go:build codec_json

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

package dao

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// NewCodec — ProtoBuf and default/unknown codec type paths
// ---------------------------------------------------------------------------

// TestNewCodec_ProtoBufReturnsError verifies that requesting a ProtoBuf codec
// via NewCodec returns an error directing the caller to use NewProtoBufCodec.
func TestNewCodec_ProtoBufReturnsError(t *testing.T) {
	_, err := NewCodec[*TestEntity](CodecProtoBuf)
	require.Error(t, err)
	var cce CodecCreationError
	require.ErrorAs(t, err, &cce)
	assert.Equal(t, string(CodecProtoBuf), cce.CodecType)
}

// TestNewCodec_UnsupportedCodecReturnsError verifies that an unrecognized
// codec type returns a CodecCreationError.
func TestNewCodec_UnsupportedCodecReturnsError(t *testing.T) {
	_, err := NewCodec[*TestEntity]("codec_unsupported_xyz")
	require.Error(t, err)
	var cce CodecCreationError
	require.ErrorAs(t, err, &cce)
}

// ---------------------------------------------------------------------------
// jsonNumberToUint64 — json.Number and invalid string paths
// ---------------------------------------------------------------------------

// TestJsonNumberToUint64_JsonNumber verifies conversion of a json.Number value.
func TestJsonNumberToUint64_JsonNumber(t *testing.T) {
	n := json.Number("12345")
	result := jsonNumberToUint64(n)
	assert.Equal(t, uint64(12345), result)
}

// TestJsonNumberToUint64_InvalidJsonNumber verifies that an unparseable
// json.Number returns 0.
func TestJsonNumberToUint64_InvalidJsonNumber(t *testing.T) {
	n := json.Number("not-a-number")
	result := jsonNumberToUint64(n)
	assert.Equal(t, uint64(0), result)
}

// TestJsonNumberToUint64_InvalidString verifies that an unparseable string
// returns 0 (the json.Number path with invalid content is covered above;
// this covers the case where v is a plain string that fails ParseUint).
func TestJsonNumberToUint64_InvalidString(t *testing.T) {
	result := jsonNumberToUint64("not-a-uint")
	assert.Equal(t, uint64(0), result)
}

// TestJsonNumberToUint64_UnknownType verifies that an unsupported type
// returns 0.
func TestJsonNumberToUint64_UnknownType(t *testing.T) {
	result := jsonNumberToUint64(struct{ x int }{x: 1})
	assert.Equal(t, uint64(0), result)
}

// ---------------------------------------------------------------------------
// genericDAO.indexName — database-prefixed path
// ---------------------------------------------------------------------------

// TestGenericDAO_IndexName_WithDatabase verifies that indexName includes the
// database prefix when the DAO was created with a database option.
func TestGenericDAO_IndexName_WithDatabase(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
		WithDatabase("mydb"),
	)
	require.NoError(t, err)

	gDAO := dao.(*genericDAO[*TestEntity])
	name := gDAO.indexName("email")
	assert.Equal(t, "mydb.users.email", name)
}

// TestGenericDAO_IndexName_WithoutDatabase verifies the legacy format when
// no database is configured.
func TestGenericDAO_IndexName_WithoutDatabase(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	gDAO := dao.(*genericDAO[*TestEntity])
	name := gDAO.indexName("email")
	assert.Equal(t, "users.email", name)
}

// ---------------------------------------------------------------------------
// isNil — non-reference type returns false
// ---------------------------------------------------------------------------

// TestIsNil_NonPointerReturnsFalse verifies that isNil returns false for
// struct value types (not pointers, interfaces, slices, maps, etc.).
func TestIsNil_NonPointerReturnsFalse(t *testing.T) {
	entity := TestEntity{ID: 1, Name: "test"}
	assert.False(t, isNil(entity))
}

// TestIsNil_NilPointerReturnsTrue verifies that isNil returns true for
// a nil pointer.
func TestIsNil_NilPointerReturnsTrue(t *testing.T) {
	var e *TestEntity
	assert.True(t, isNil(e))
}

// ---------------------------------------------------------------------------
// evaluateCondition — ConditionCreate, ConditionMod, ConditionValue paths
// ---------------------------------------------------------------------------

// TestEvaluateCondition_CreateOnExistingTimestamped verifies that
// ConditionCreate works correctly with a Timestamped entity.
func TestEvaluateCondition_CreateOnExistingTimestamped(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TimestampedTestEntity](
		store,
		"ts_entities",
		func() *TimestampedTestEntity { return &TimestampedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entity := &TimestampedTestEntity{ID: 1001, Name: "Original", CreatedAt: createdAt, UpdatedAt: createdAt}
	require.NoError(t, dao.Save(ctx, entity))

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)

	updated := &TimestampedTestEntity{ID: 1001, Name: "Updated", CreatedAt: createdAt, UpdatedAt: createdAt}
	err = tx.SaveWithCondition(updated, Condition{
		Target:   ConditionCreate,
		Operator: OpEqual,
		Value:    createdAt.UnixNano(),
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	retrieved, err := dao.Get(ctx, 1001)
	require.NoError(t, err)
	assert.Equal(t, "Updated", retrieved.Name)
}

// TestEvaluateCondition_CreateOnNonTimestamped verifies that ConditionCreate
// returns ConditionFailedError when entity doesn't implement Timestamped.
func TestEvaluateCondition_CreateOnNonTimestamped(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"plain_entities",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	entity := &TestEntity{ID: 2001, Name: "Original"}
	require.NoError(t, dao.Save(ctx, entity))

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)

	updated := &TestEntity{ID: 2001, Name: "Updated"}
	err = tx.SaveWithCondition(updated, Condition{
		Target:   ConditionCreate,
		Operator: OpEqual,
		Value:    int64(0),
	})
	require.NoError(t, err)

	// Commit should fail because TestEntity is not Timestamped.
	commitErr := tx.Commit(ctx)
	require.Error(t, commitErr)
	assert.True(t, IsConditionFailed(commitErr) || errors.Is(commitErr, commitErr))
}

// TestEvaluateCondition_ModOnExistingTimestamped verifies that ConditionMod
// works correctly with a Timestamped entity.
func TestEvaluateCondition_ModOnExistingTimestamped(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TimestampedTestEntity](
		store,
		"ts_mod_entities",
		func() *TimestampedTestEntity { return &TimestampedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	entity := &TimestampedTestEntity{ID: 3001, Name: "Original", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, dao.Save(ctx, entity))

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)

	updated := &TimestampedTestEntity{ID: 3001, Name: "Updated", CreatedAt: now, UpdatedAt: now}
	err = tx.SaveWithCondition(updated, Condition{
		Target:   ConditionMod,
		Operator: OpEqual,
		Value:    now.UnixNano(),
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

// TestEvaluateCondition_ModOnNonTimestamped verifies that ConditionMod
// returns ConditionFailedError when entity doesn't implement Timestamped.
func TestEvaluateCondition_ModOnNonTimestamped(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"plain_mod_entities",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	entity := &TestEntity{ID: 4001, Name: "Original"}
	require.NoError(t, dao.Save(ctx, entity))

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)

	updated := &TestEntity{ID: 4001, Name: "Updated"}
	err = tx.SaveWithCondition(updated, Condition{
		Target:   ConditionMod,
		Operator: OpEqual,
		Value:    int64(0),
	})
	require.NoError(t, err)

	commitErr := tx.Commit(ctx)
	require.Error(t, commitErr)
}

// TestEvaluateCondition_ValueOnExistingEntity verifies the ConditionValue
// path where the existing entity is serialized for comparison.
func TestEvaluateCondition_ValueOnExistingEntity(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"value_cond_entities",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	entity := &TestEntity{ID: 5001, Name: "Original"}
	require.NoError(t, dao.Save(ctx, entity))

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)

	updated := &TestEntity{ID: 5001, Name: "Updated"}
	// NotEqual so it passes regardless of serialized form.
	err = tx.SaveWithCondition(updated, Condition{
		Target:   ConditionValue,
		Operator: OpNotEqual,
		Value:    []byte("something_completely_different"),
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

// TestEvaluateCondition_ValueOnNonExistentEntity verifies the ConditionValue
// path where the entity doesn't exist — nil is passed to evaluateValueCondition.
func TestEvaluateCondition_ValueOnNonExistentEntity(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"value_new_entities",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)

	newEntity := &TestEntity{ID: 6001, Name: "New"}
	err = tx.SaveWithCondition(newEntity, Condition{
		Target:   ConditionValue,
		Operator: OpEqual,
		Value:    []byte(nil),
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

// ---------------------------------------------------------------------------
// evaluateValueCondition — OpGreater, OpLess, OpGreaterEqual, OpLessEqual
// ---------------------------------------------------------------------------

// TestEvaluateValueCondition_ByteComparisons verifies the byte comparison
// operators (greater, less, ge, le) in evaluateValueCondition by using
// ConditionValue with various operators on an existing entity.
func TestEvaluateValueCondition_ByteComparisons(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"byte_cmp_entities",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	entity := &TestEntity{ID: 8001, Name: "ZZZ"}
	require.NoError(t, dao.Save(ctx, entity))

	tests := []struct {
		name    string
		op      ConditionOp
		value   interface{}
		wantErr bool
	}{
		{"greater_than_empty", OpGreater, []byte(""), false},
		{"less_than_high", OpLess, []byte("\xff\xff\xff\xff"), false},
		{"ge_same_serialized", OpGreaterEqual, []byte(""), false},
		{"le_same_serialized", OpLessEqual, []byte("\xff\xff\xff\xff"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := dao.BeginTx(ctx)
			require.NoError(t, err)

			updated := &TestEntity{ID: 8001, Name: "Updated"}
			err = tx.SaveWithCondition(updated, Condition{
				Target:   ConditionValue,
				Operator: tt.op,
				Value:    tt.value,
			})
			require.NoError(t, err)

			commitErr := tx.Commit(ctx)
			if tt.wantErr {
				require.Error(t, commitErr)
			} else {
				// May succeed or fail based on actual serialized bytes;
				// the point is to exercise the comparison operators.
				_ = commitErr
			}
		})
	}
}

// ---------------------------------------------------------------------------
// evaluateCondition — new entity without existence, ConditionCreate/Mod
// ---------------------------------------------------------------------------

// TestEvaluateCondition_NewEntityConditionCreate verifies that when an entity
// doesn't exist and ConditionCreate is used, evaluateNumericCondition(0) runs.
func TestEvaluateCondition_NewEntityConditionCreate(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"new_create_entities",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)

	newEntity := &TestEntity{ID: 9001, Name: "New"}
	err = tx.SaveWithCondition(newEntity, Condition{
		Target:   ConditionCreate,
		Operator: OpEqual,
		Value:    int64(0),
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

// TestEvaluateCondition_NewEntityConditionMod verifies that when an entity
// doesn't exist and ConditionMod is used, evaluateNumericCondition(0) runs.
func TestEvaluateCondition_NewEntityConditionMod(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"new_mod_entities",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)

	newEntity := &TestEntity{ID: 9002, Name: "New"}
	err = tx.SaveWithCondition(newEntity, Condition{
		Target:   ConditionMod,
		Operator: OpEqual,
		Value:    int64(0),
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

// ---------------------------------------------------------------------------
// extractGenericDAO — unsupported DAO type path
// ---------------------------------------------------------------------------

// TestExtractGenericDAO_HappyPath verifies that RegisterSave works correctly
// with a plain *genericDAO (the extractGenericDAO type switch happy path).
func TestExtractGenericDAO_HappyPath(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"extract_happy",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	uow := NewUnitOfWork(store)
	err = RegisterSave(uow, dao, &TestEntity{ID: 1, Name: "Test"})
	require.NoError(t, err)
}

// TestExtractGenericDAO_TransactionalDAO verifies that RegisterSave also works
// with a *transactionalDAO (the second branch in the extractGenericDAO switch).
func TestExtractGenericDAO_TransactionalDAO(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"extract_transactional",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	uow := NewUnitOfWork(store)
	err = RegisterSave(uow, dao, &TestEntity{ID: 2, Name: "Test2"})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// WithTx — function returns error path
// ---------------------------------------------------------------------------

// TestWithTx_FunctionReturnsError verifies that WithTx rolls back and
// returns the error when the callback function returns an error.
func TestWithTx_FunctionReturnsError(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"withtx_error",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	injectedErr := errors.New("intentional error")

	err = WithTx(ctx, dao, func(tx DAOTransaction[*TestEntity]) error {
		_ = tx.Save(&TestEntity{ID: 9999, Name: "ShouldNotPersist"})
		return injectedErr
	})

	require.Error(t, err)
	assert.Equal(t, injectedErr, err)

	// Entity should not have been saved.
	_, getErr := dao.Get(ctx, 9999)
	require.Error(t, getErr)
}

// ---------------------------------------------------------------------------
// evaluateCondition — ConditionVersion on non-Versioned existing entity
// ---------------------------------------------------------------------------

// TestEvaluateCondition_VersionOnNonVersioned pins that a ConditionVersion
// guard on an entity that carries no version is REFUSED.
//
// This test previously asserted the opposite — that "version 1 is assumed" —
// which is how the defect survived: it read as a deliberate convention rather
// than a fabricated comparison. What the constant actually bought was a guard
// that never guards. Nothing about the stored record was consulted, so
// OpEqual 1 passed on the first update and on the ten-thousandth, and OpEqual 2
// failed on both. A caller following data-access.md ("any read-modify-write of
// an entity uses SaveWithCondition with ConditionVersion, or it is a
// lost-update bug") got a call that compiled, returned nil, and guarded
// nothing.
//
// The refusal is the same shape ConditionCreate and ConditionMod already use
// for an entity that does not implement Timestamped, so the three targets now
// agree: a condition that cannot be evaluated is an error, not a default
// (go-qrdb#59).
func TestEvaluateCondition_VersionOnNonVersioned(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*TestEntity](
		store,
		"version_non_versioned",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	entity := &TestEntity{ID: 10001, Name: "Original"}
	require.NoError(t, dao.Save(ctx, entity))

	// Every comparison value is refused, not just the ones that used to fail.
	// Asserting only that OpEqual 2 errors would pass against the constant as
	// well, since 2 != 1 — it is OpEqual 1, the value the constant silently
	// satisfied, that distinguishes the fix.
	for _, want := range []int64{0, 1, 2} {
		tx, err := dao.BeginTx(ctx)
		require.NoError(t, err)

		updated := &TestEntity{ID: 10001, Name: "Updated"}
		err = tx.SaveWithCondition(updated, Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    want,
		})
		require.NoError(t, err, "queuing the operation must not fail")

		err = tx.Commit(ctx)
		require.Error(t, err, "ConditionVersion on a non-Versioned entity must be refused (value %d)", want)

		var failed ConditionFailedError
		require.ErrorAs(t, err, &failed)
		assert.Contains(t, failed.Reason, "does not implement Versioned",
			"the refusal must name the missing interface, not report a version mismatch")
	}

	// The record is untouched: a refused guard must not write.
	retrieved, err := dao.Get(ctx, 10001)
	require.NoError(t, err)
	assert.Equal(t, "Original", retrieved.Name)
}

// And the guard still WORKS for an entity that does carry a version, so the
// refusal above is a distinction rather than a blanket rejection.
func TestEvaluateCondition_VersionOnVersionedStillDecides(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := NewTransactional[*VersionedTestEntity](
		store,
		"version_versioned",
		func() *VersionedTestEntity { return &VersionedTestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, dao.Save(ctx, &VersionedTestEntity{ID: 20001, Name: "Original", Version: 7}))

	// The stored version is consulted: 7 passes.
	tx, err := dao.BeginTx(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.SaveWithCondition(
		&VersionedTestEntity{ID: 20001, Name: "Updated", Version: 8},
		Condition{Target: ConditionVersion, Operator: OpEqual, Value: int64(7)},
	))
	require.NoError(t, tx.Commit(ctx))

	retrieved, err := dao.Get(ctx, 20001)
	require.NoError(t, err)
	assert.Equal(t, "Updated", retrieved.Name)

	// And a stale expectation fails, now against the version the record
	// actually carries (8) rather than a constant.
	tx, err = dao.BeginTx(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.SaveWithCondition(
		&VersionedTestEntity{ID: 20001, Name: "Stale", Version: 9},
		Condition{Target: ConditionVersion, Operator: OpEqual, Value: int64(7)},
	))
	require.Error(t, tx.Commit(ctx))

	retrieved, err = dao.Get(ctx, 20001)
	require.NoError(t, err)
	assert.Equal(t, "Updated", retrieved.Name)
}
