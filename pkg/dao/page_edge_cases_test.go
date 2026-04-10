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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// countThenErrorKVStore is a store that counts successfully but errors on scan.
type countThenErrorKVStore struct {
	countCalls int
	scanErr    error
}

func (s *countThenErrorKVStore) Put(_ context.Context, _ string, _ []byte) error {
	return nil
}

func (s *countThenErrorKVStore) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (s *countThenErrorKVStore) Delete(_ context.Context, _ string) error {
	return nil
}

func (s *countThenErrorKVStore) Scan(_ context.Context, _ string, fn func(key string, value []byte) error) error {
	s.countCalls++
	// First call is for Count, return empty
	if s.countCalls == 1 {
		return nil
	}
	// Second call is for actual paging, return error
	return s.scanErr
}

func (s *countThenErrorKVStore) List(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (s *countThenErrorKVStore) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (s *countThenErrorKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (s *countThenErrorKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (s *countThenErrorKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (s *countThenErrorKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (s *countThenErrorKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

var _ kvstore.KVStore = (*countThenErrorKVStore)(nil)

// TestPageScanErrorAfterCount validates Page error handling when scan fails after count.
func TestPageScanErrorAfterCount(t *testing.T) {
	t.Run("PageReturnsErrorOnSecondScan", func(t *testing.T) {
		scanErr := errors.New("scan error after count")
		store := &countThenErrorKVStore{scanErr: scanErr}

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

// TestForEachPageInvalidPageSize validates ForEachPage with invalid page size.
func TestForEachPageInvalidPageSize(t *testing.T) {
	t.Run("NormalizesInvalidPageSize", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add some data
		entity := &TestEntity{ID: 1, Name: "Test", Email: "test@test.com"}
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// Use invalid page size (0)
		query := PageQuery{Page: 1, PageSize: 0}
		callCount := 0
		err = dao.ForEachPage(ctx, query, func(result PageResult[*TestEntity]) error {
			callCount++
			// Should have normalized PageSize to 25
			assert.Equal(t, 25, result.PageSize)
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, callCount)
	})
}

// TestPageBeyondData validates Page behavior when page is beyond available data.
func TestPageBeyondData(t *testing.T) {
	t.Run("PageBeyondDataReturnsEmpty", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add some data
		for i := 1; i <= 5; i++ {
			entity := &TestEntity{ID: uint64(i), Name: "Test", Email: "test@test.com"}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		// Request page far beyond data
		query := PageQuery{Page: 100, PageSize: 5}
		result, err := dao.Page(ctx, query)

		require.NoError(t, err)
		assert.Empty(t, result.Entities)
		assert.Equal(t, 100, result.Page)
		assert.Equal(t, 5, result.Total)
		assert.False(t, result.HasMore)
	})
}

// TestPageNegativeValues validates Page behavior with negative values.
func TestPageNegativeValues(t *testing.T) {
	t.Run("NegativePageNormalizesToOne", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		query := PageQuery{Page: -5, PageSize: -10}
		result, err := dao.Page(ctx, query)

		require.NoError(t, err)
		// Should be normalized
		assert.Equal(t, 1, result.Page)
		assert.Equal(t, 25, result.PageSize)
	})
}

// TestForEachPageNegativeValues validates ForEachPage behavior with negative values.
func TestForEachPageNegativeValues(t *testing.T) {
	t.Run("NegativeValuesNormalizedToDefaults", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		query := PageQuery{Page: -5, PageSize: -10}
		called := false
		err = dao.ForEachPage(ctx, query, func(result PageResult[*TestEntity]) error {
			called = true
			assert.Equal(t, 1, result.Page)
			assert.Equal(t, 25, result.PageSize)
			return nil
		})

		require.NoError(t, err)
		assert.True(t, called)
	})
}
