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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPageQuery validates the NewPageQuery function.
func TestNewPageQuery(t *testing.T) {
	t.Run("DefaultValues", func(t *testing.T) {
		query := NewPageQuery()

		assert.Equal(t, 1, query.Page)
		assert.Equal(t, 25, query.PageSize)
		assert.Equal(t, SortAscending, query.SortOrder)
	})

	t.Run("PageIs1", func(t *testing.T) {
		query := NewPageQuery()
		assert.Equal(t, 1, query.Page)
	})

	t.Run("PageSizeIs25", func(t *testing.T) {
		query := NewPageQuery()
		assert.Equal(t, 25, query.PageSize)
	})

	t.Run("SortOrderIsAscending", func(t *testing.T) {
		query := NewPageQuery()
		assert.Equal(t, SortAscending, query.SortOrder)
	})
}

// TestNewPageResult validates the NewPageResult function.
func TestNewPageResult(t *testing.T) {
	t.Run("EmptyResult", func(t *testing.T) {
		result := NewPageResult[*TestEntity]()

		require.NotNil(t, result.Entities)
		assert.Equal(t, 0, len(result.Entities))
		assert.Equal(t, 0, result.Page)
		assert.Equal(t, 0, result.PageSize)
		assert.False(t, result.HasMore)
	})

	t.Run("EntitiesNotNil", func(t *testing.T) {
		result := NewPageResult[*TestEntity]()
		assert.NotNil(t, result.Entities)
	})

	t.Run("EntitiesIsEmpty", func(t *testing.T) {
		result := NewPageResult[*TestEntity]()
		assert.Equal(t, 0, len(result.Entities))
	})
}

// TestNewPageResultFromQuery validates the NewPageResultFromQuery function.
func TestNewPageResultFromQuery(t *testing.T) {
	t.Run("FromDefaultQuery", func(t *testing.T) {
		query := NewPageQuery()
		result := NewPageResultFromQuery[*TestEntity](query)

		require.NotNil(t, result.Entities)
		assert.Equal(t, 0, len(result.Entities))
		assert.Equal(t, 1, result.Page)
		assert.Equal(t, 25, result.PageSize)
		assert.False(t, result.HasMore)
	})

	t.Run("FromCustomQuery", func(t *testing.T) {
		query := PageQuery{
			Page:      5,
			PageSize:  10,
			SortOrder: SortDescending,
		}
		result := NewPageResultFromQuery[*TestEntity](query)

		require.NotNil(t, result.Entities)
		assert.Equal(t, 0, len(result.Entities))
		assert.Equal(t, 5, result.Page)
		assert.Equal(t, 10, result.PageSize)
		assert.False(t, result.HasMore)
	})

	t.Run("PreallocatesCapacity", func(t *testing.T) {
		query := PageQuery{
			Page:     1,
			PageSize: 100,
		}
		result := NewPageResultFromQuery[*TestEntity](query)

		// Capacity should be preallocated to PageSize
		assert.GreaterOrEqual(t, cap(result.Entities), 100)
	})
}

// TestSortOrderConstants validates sort order constants.
func TestSortOrderConstants(t *testing.T) {
	t.Run("SortAscendingValue", func(t *testing.T) {
		assert.Equal(t, 0, SortAscending)
	})

	t.Run("SortDescendingValue", func(t *testing.T) {
		assert.Equal(t, 1, SortDescending)
	})

	t.Run("SortOrdersDifferent", func(t *testing.T) {
		assert.NotEqual(t, SortAscending, SortDescending)
	})
}

// TestPageQueryFields validates PageQuery struct fields.
func TestPageQueryFields(t *testing.T) {
	t.Run("AllFieldsSet", func(t *testing.T) {
		query := PageQuery{
			Page:      10,
			PageSize:  50,
			SortOrder: SortDescending,
		}

		assert.Equal(t, 10, query.Page)
		assert.Equal(t, 50, query.PageSize)
		assert.Equal(t, SortDescending, query.SortOrder)
	})

	t.Run("ZeroValues", func(t *testing.T) {
		var query PageQuery

		assert.Equal(t, 0, query.Page)
		assert.Equal(t, 0, query.PageSize)
		assert.Equal(t, 0, query.SortOrder)
	})
}

// TestPageResultFields validates PageResult struct fields.
func TestPageResultFields(t *testing.T) {
	t.Run("AllFieldsSet", func(t *testing.T) {
		entities := []*TestEntity{
			{ID: 1, Name: "User1"},
			{ID: 2, Name: "User2"},
		}

		result := PageResult[*TestEntity]{
			Entities: entities,
			Page:     2,
			PageSize: 10,
			HasMore:  true,
		}

		assert.Equal(t, 2, len(result.Entities))
		assert.Equal(t, 2, result.Page)
		assert.Equal(t, 10, result.PageSize)
		assert.True(t, result.HasMore)
	})
}
