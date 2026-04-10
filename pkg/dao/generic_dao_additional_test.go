//go:build codec_json

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// errorKVStore is a mock KVStore that returns errors for testing.
type errorKVStore struct {
	putErr    error
	getErr    error
	deleteErr error
	scanErr   error
	listErr   error
	scanData  map[string][]byte
}

func (e *errorKVStore) Put(_ context.Context, _ string, _ []byte) error {
	return e.putErr
}

func (e *errorKVStore) Get(_ context.Context, _ string) ([]byte, error) {
	if e.getErr != nil {
		return nil, e.getErr
	}
	return nil, &dberrors.DragonError{Code: dberrors.ErrNotFound, Op: "kv.get"}
}

func (e *errorKVStore) Delete(_ context.Context, _ string) error {
	return e.deleteErr
}

func (e *errorKVStore) Scan(_ context.Context, _ string, fn func(key string, value []byte) error) error {
	if e.scanErr != nil {
		return e.scanErr
	}
	for k, v := range e.scanData {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (e *errorKVStore) List(_ context.Context, _ string) ([]string, error) {
	return nil, e.listErr
}

func (e *errorKVStore) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (e *errorKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (e *errorKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (e *errorKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (e *errorKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (e *errorKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

var _ kvstore.KVStore = (*errorKVStore)(nil)

// TestSaveWithMarshalError validates Save error handling when marshaling fails.
func TestSaveWithMarshalError(t *testing.T) {
	t.Run("SaveReturnsSerializationError", func(t *testing.T) {
		store := &errorKVStore{putErr: errors.New("put error")}
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		entity := &TestEntity{ID: 1, Name: "Test", Email: "test@test.com"}
		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "put error")
	})
}

// TestGetWithGenericError validates Get error handling for non-NotFound errors.
func TestGetWithGenericError(t *testing.T) {
	t.Run("GetReturnsGenericError", func(t *testing.T) {
		genericErr := errors.New("generic storage error")
		store := &errorKVStore{getErr: genericErr}
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		_, err = dao.Get(ctx, 123)
		require.Error(t, err)
		assert.Equal(t, genericErr, err)
	})
}

// TestCountWithScanError validates Count error handling.
func TestCountWithScanError(t *testing.T) {
	t.Run("CountReturnsErrorOnScanFailure", func(t *testing.T) {
		scanErr := errors.New("scan failed")
		store := &errorKVStore{scanErr: scanErr}
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		_, err = dao.Count(ctx)
		require.Error(t, err)
		assert.Equal(t, scanErr, err)
	})
}

// TestPageWithScanError validates Page error handling.
func TestPageWithScanError(t *testing.T) {
	t.Run("PageReturnsErrorOnScanFailure", func(t *testing.T) {
		scanErr := errors.New("scan failed")
		store := &errorKVStore{scanErr: scanErr}
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		_, err = dao.Page(ctx, NewPageQuery())
		require.Error(t, err)
		assert.Equal(t, scanErr, err)
	})
}

// TestForEachPageWithError validates ForEachPage error handling.
func TestForEachPageWithError(t *testing.T) {
	t.Run("ForEachPageReturnsPageError", func(t *testing.T) {
		scanErr := errors.New("scan failed")
		store := &errorKVStore{scanErr: scanErr}
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		err = dao.ForEachPage(ctx, NewPageQuery(), func(result PageResult[*TestEntity]) error {
			return nil
		})
		require.Error(t, err)
		assert.Equal(t, scanErr, err)
	})
}

// TestIsNotFoundErrorVariants tests the isNotFoundError helper with various inputs.
func TestIsNotFoundErrorVariants(t *testing.T) {
	t.Run("NilError", func(t *testing.T) {
		assert.False(t, isNotFoundError(nil))
	})

	t.Run("DragonErrorNotFound", func(t *testing.T) {
		err := &dberrors.DragonError{Code: dberrors.ErrNotFound, Op: "test"}
		assert.True(t, isNotFoundError(err))
	})

	t.Run("DragonErrorOther", func(t *testing.T) {
		err := &dberrors.DragonError{Code: dberrors.ErrInternal, Op: "test"}
		assert.False(t, isNotFoundError(err))
	})

	t.Run("GenericError", func(t *testing.T) {
		err := errors.New("some error")
		assert.False(t, isNotFoundError(err))
	})
}

// TestPageWithMalformedData validates Page behavior with corrupt data.
func TestPageWithMalformedData(t *testing.T) {
	t.Run("SkipsMalformedEntities", func(t *testing.T) {
		// Create a store with malformed data
		store := newMemoryKVStore()
		ctx := context.Background()

		// Store valid data
		validEntity := &TestEntity{ID: 1, Name: "Valid", Email: "valid@test.com"}
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)
		err = dao.Save(ctx, validEntity)
		require.NoError(t, err)

		// Manually insert malformed data
		err = store.Put(ctx, "users/999", []byte("not valid json{"))
		require.NoError(t, err)

		// Page should skip the malformed entry and return valid ones
		result, err := dao.Page(ctx, NewPageQuery())
		require.NoError(t, err)
		// The valid entity should still be found
		assert.GreaterOrEqual(t, len(result.Entities), 1)
	})
}

// TestNewDAOWithNilStore validates NewDAO error when store is nil.
func TestNewDAOWithNilStore(t *testing.T) {
	_, err := New[*TestEntity](
		nil,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.Error(t, err)
	var configErr InvalidConfigError
	assert.True(t, errors.As(err, &configErr))
}

// TestNewDAOWithEmptyEntityType validates NewDAO error when entityType is empty.
func TestNewDAOWithEmptyEntityType(t *testing.T) {
	store := newMemoryKVStore()
	_, err := New[*TestEntity](
		store,
		"",
		func() *TestEntity { return &TestEntity{} },
	)
	require.Error(t, err)
	var configErr InvalidConfigError
	assert.True(t, errors.As(err, &configErr))
}

// TestNewDAOWithNilNewEntity validates NewDAO error when newEntity is nil.
func TestNewDAOWithNilNewEntity(t *testing.T) {
	store := newMemoryKVStore()
	_, err := New[*TestEntity](
		store,
		"users",
		nil,
	)
	require.Error(t, err)
	var configErr InvalidConfigError
	assert.True(t, errors.As(err, &configErr))
}
