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
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestNew validates the New function with valid and invalid parameters.
func TestNew(t *testing.T) {
	t.Run("ValidParameters", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"test_entities",
			func() *TestEntity { return &TestEntity{} },
		)

		require.NoError(t, err)
		require.NotNil(t, dao)
		assert.Equal(t, "test_entities", dao.EntityType())
	})

	t.Run("ValidParametersWithOptions", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"test_entities",
			func() *TestEntity { return &TestEntity{} },
			WithShard(5),
			WithCodec(CodecJSON),
			WithTimeout(10*time.Second),
		)

		require.NoError(t, err)
		require.NotNil(t, dao)
		assert.Equal(t, "test_entities", dao.EntityType())
	})

	t.Run("NilStore", func(t *testing.T) {
		dao, err := New[*TestEntity](
			nil,
			"test_entities",
			func() *TestEntity { return &TestEntity{} },
		)

		require.Error(t, err)
		assert.Nil(t, dao)

		var configErr InvalidConfigError
		require.True(t, errors.As(err, &configErr))
		assert.Equal(t, "store", configErr.Field)
		assert.Contains(t, configErr.Reason, "cannot be nil")
	})

	t.Run("EmptyEntityType", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"",
			func() *TestEntity { return &TestEntity{} },
		)

		require.Error(t, err)
		assert.Nil(t, dao)

		var configErr InvalidConfigError
		require.True(t, errors.As(err, &configErr))
		assert.Equal(t, "entityType", configErr.Field)
		assert.Contains(t, configErr.Reason, "cannot be empty")
	})

	t.Run("NilNewEntityFactory", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"test_entities",
			nil,
		)

		require.Error(t, err)
		assert.Nil(t, dao)

		var configErr InvalidConfigError
		require.True(t, errors.As(err, &configErr))
		assert.Equal(t, "newEntity", configErr.Field)
		assert.Contains(t, configErr.Reason, "cannot be nil")
	})

	t.Run("UnsupportedCodec", func(t *testing.T) {
		// Test with a codec type that will never exist, ensuring this test
		// always exercises the unsupported codec error path regardless of build tags
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"test_entities",
			func() *TestEntity { return &TestEntity{} },
			WithCodec(CodecType("nonexistent_test_codec")),
		)

		require.Error(t, err)
		assert.Nil(t, dao)

		var codecErr CodecCreationError
		require.True(t, errors.As(err, &codecErr))
		assert.Equal(t, "nonexistent_test_codec", codecErr.CodecType)
	})
}

// TestSave validates the Save function.
func TestSave(t *testing.T) {
	t.Run("SaveWithoutID", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		entity := &TestEntity{
			Name:  "Alice",
			Email: "alice@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// ID should be auto-generated
		assert.NotZero(t, entity.ID)
	})

	t.Run("SaveWithID", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		entity := &TestEntity{
			ID:    12345,
			Name:  "Bob",
			Email: "bob@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// ID should remain unchanged
		assert.Equal(t, uint64(12345), entity.ID)
	})

	t.Run("SaveAndRetrieve", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		original := &TestEntity{
			Name:  "Charlie",
			Email: "charlie@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, original)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, original.ID)
		require.NoError(t, err)

		assert.Equal(t, original.ID, retrieved.ID)
		assert.Equal(t, original.Name, retrieved.Name)
		assert.Equal(t, original.Email, retrieved.Email)
	})

	t.Run("SaveOverwrite", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		entity := &TestEntity{
			ID:    1000,
			Name:  "Original",
			Email: "original@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// Update the entity
		entity.Name = "Updated"
		entity.Email = "updated@example.com"
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// Retrieve and verify update
		retrieved, err := dao.Get(ctx, entity.ID)
		require.NoError(t, err)
		assert.Equal(t, "Updated", retrieved.Name)
		assert.Equal(t, "updated@example.com", retrieved.Email)
	})

	t.Run("SaveWithTimeout", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithTimeout(5*time.Second),
		)
		require.NoError(t, err)

		entity := &TestEntity{
			Name:  "Timeout Test",
			Email: "timeout@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.NoError(t, err)
	})
}

// TestGet validates the Get function.
func TestGet(t *testing.T) {
	t.Run("GetExistingEntity", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		entity := &TestEntity{
			ID:    999,
			Name:  "Dave",
			Email: "dave@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 999)
		require.NoError(t, err)
		assert.Equal(t, entity.ID, retrieved.ID)
		assert.Equal(t, entity.Name, retrieved.Name)
		assert.Equal(t, entity.Email, retrieved.Email)
	})

	t.Run("GetNonExistentEntity", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		retrieved, err := dao.Get(ctx, 99999)
		require.Error(t, err)

		var notFoundErr NotFoundError
		require.True(t, errors.As(err, &notFoundErr))
		assert.Equal(t, "users", notFoundErr.EntityType)
		assert.Equal(t, uint64(99999), notFoundErr.ID)
		assert.Nil(t, retrieved)
	})

	t.Run("GetWithTimeout", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithTimeout(5*time.Second),
		)
		require.NoError(t, err)

		entity := &TestEntity{
			ID:    777,
			Name:  "Timeout Test",
			Email: "timeout@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 777)
		require.NoError(t, err)
		assert.Equal(t, entity.ID, retrieved.ID)
	})
}

// TestDelete validates the Delete function.
func TestDelete(t *testing.T) {
	t.Run("DeleteValidEntity", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		entity := &TestEntity{
			ID:    888,
			Name:  "Eve",
			Email: "eve@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// Verify it exists
		_, err = dao.Get(ctx, 888)
		require.NoError(t, err)

		// Delete it
		err = dao.Delete(ctx, entity)
		require.NoError(t, err)

		// Verify it's gone
		_, err = dao.Get(ctx, 888)
		require.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("DeleteZeroID", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		entity := &TestEntity{
			ID:    0,
			Name:  "Invalid",
			Email: "invalid@example.com",
		}

		ctx := context.Background()
		err = dao.Delete(ctx, entity)
		require.Error(t, err)

		var invalidIDErr InvalidEntityIDError
		require.True(t, errors.As(err, &invalidIDErr))
		assert.Equal(t, "users", invalidIDErr.EntityType)
		assert.Contains(t, invalidIDErr.Reason, "cannot delete entity with ID 0")
	})

	t.Run("DeleteNonExistentEntity", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		entity := &TestEntity{
			ID:    99999,
			Name:  "DoesNotExist",
			Email: "nonexistent@example.com",
		}

		ctx := context.Background()
		err = dao.Delete(ctx, entity)
		// Delete is idempotent - no error
		require.NoError(t, err)
	})
}

// TestCount validates the Count function.
func TestCount(t *testing.T) {
	t.Run("CountEmpty", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		count, err := dao.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("CountWithData", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 5 entities
		for i := 1; i <= 5; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		count, err := dao.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 5, count)
	})

	t.Run("CountAfterDelete", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 3 entities
		entities := make([]*TestEntity, 3)
		for i := 0; i < 3; i++ {
			entities[i] = &TestEntity{
				ID:    uint64(i + 1),
				Name:  "User" + string(rune('0'+i+1)),
				Email: "user" + string(rune('0'+i+1)) + "@example.com",
			}
			err = dao.Save(ctx, entities[i])
			require.NoError(t, err)
		}

		count, err := dao.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 3, count)

		// Delete one
		err = dao.Delete(ctx, entities[1])
		require.NoError(t, err)

		count, err = dao.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})
}

// TestPage validates the Page function.
func TestPage(t *testing.T) {
	t.Run("PageEmpty", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		query := PageQuery{Page: 1, PageSize: 10}
		result, err := dao.Page(ctx, query)

		require.NoError(t, err)
		assert.Equal(t, 0, len(result.Entities))
		assert.Equal(t, 1, result.Page)
		assert.Equal(t, 10, result.PageSize)
		assert.False(t, result.HasMore)
	})

	t.Run("PageFirstPage", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 10 entities
		for i := 1; i <= 10; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		query := PageQuery{Page: 1, PageSize: 5}
		result, err := dao.Page(ctx, query)

		require.NoError(t, err)
		assert.Equal(t, 5, len(result.Entities))
		assert.Equal(t, 1, result.Page)
		assert.Equal(t, 5, result.PageSize)
		assert.True(t, result.HasMore)
	})

	t.Run("PageSecondPage", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 10 entities
		for i := 1; i <= 10; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		query := PageQuery{Page: 2, PageSize: 5}
		result, err := dao.Page(ctx, query)

		require.NoError(t, err)
		assert.Equal(t, 5, len(result.Entities))
		assert.Equal(t, 2, result.Page)
		assert.Equal(t, 5, result.PageSize)
		assert.False(t, result.HasMore)
	})

	t.Run("PageLastPagePartial", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 7 entities
		for i := 1; i <= 7; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		query := PageQuery{Page: 2, PageSize: 5}
		result, err := dao.Page(ctx, query)

		require.NoError(t, err)
		assert.Equal(t, 2, len(result.Entities))
		assert.Equal(t, 2, result.Page)
		assert.Equal(t, 5, result.PageSize)
		assert.False(t, result.HasMore)
	})

	t.Run("PageInvalidPageNumber", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Page 0 should be normalized to 1
		query := PageQuery{Page: 0, PageSize: 10}
		result, err := dao.Page(ctx, query)

		require.NoError(t, err)
		// Result reflects the normalized values
		assert.Equal(t, 1, result.Page)
	})

	t.Run("PageInvalidPageSize", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// PageSize 0 should be normalized to default (25)
		query := PageQuery{Page: 1, PageSize: 0}
		result, err := dao.Page(ctx, query)

		require.NoError(t, err)
		// Result reflects the normalized values
		assert.Equal(t, 25, result.PageSize)
	})

	t.Run("PageExactMultiple", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add exactly 10 entities (2 pages of 5)
		for i := 1; i <= 10; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		// First page
		query := PageQuery{Page: 1, PageSize: 5}
		result, err := dao.Page(ctx, query)
		require.NoError(t, err)
		assert.Equal(t, 5, len(result.Entities))
		assert.True(t, result.HasMore)

		// Last page (should not have HasMore)
		query = PageQuery{Page: 2, PageSize: 5}
		result, err = dao.Page(ctx, query)
		require.NoError(t, err)
		assert.Equal(t, 5, len(result.Entities))
		assert.False(t, result.HasMore)
	})
}

// TestPage_SortFieldGlobalOrder pins that PageQuery.SortField sorts the whole
// entity set before paging, not just the storage-key-ordered slice that
// landed in one page. Entity keys are "{entityType}/{id}" with a decimal,
// unpadded id, so storage-key order and Name order are deliberately made to
// disagree here -- a page-local sort of the wrong candidates would still
// produce a "sorted-looking" page while silently skipping and repeating
// entities across the page boundary.
func TestPage_SortFieldGlobalOrder(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	names := []string{"Mia", "Alice", "Zara", "Kevin", "Omar", "Beth", "Yara", "Carl", "Nina", "Drew"}
	for i, name := range names {
		require.NoError(t, dao.Save(ctx, &TestEntity{
			ID:    uint64(i + 1),
			Name:  name,
			Email: name + "@example.com",
		}))
	}

	wantOrder := append([]string(nil), names...)
	sort.Strings(wantOrder)

	var gotOrder []string
	seen := make(map[uint64]bool)
	for page := 1; page <= 3; page++ {
		result, err := dao.Page(ctx, PageQuery{Page: page, PageSize: 4, SortField: "Name"})
		require.NoError(t, err)
		for _, e := range result.Entities {
			require.False(t, seen[e.ID], "entity %d returned more than once across pages", e.ID)
			seen[e.ID] = true
			gotOrder = append(gotOrder, e.Name)
		}
		if !result.HasMore {
			break
		}
	}

	assert.Equal(t, wantOrder, gotOrder, "paging through every page with SortField set must yield the entities in global sorted order")
	assert.Len(t, gotOrder, len(names), "every entity must be returned exactly once across all pages")
}

// TestForEachPage validates the ForEachPage function.
func TestForEachPage(t *testing.T) {
	t.Run("ForEachPageEmpty", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		query := PageQuery{Page: 1, PageSize: 5}

		callCount := 0
		err = dao.ForEachPage(ctx, query, func(result PageResult[*TestEntity]) error {
			callCount++
			return nil
		})

		require.NoError(t, err)
		// Callback is now always called once, even for empty results
		assert.Equal(t, 1, callCount)
	})

	t.Run("ForEachPageAllPages", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 12 entities (3 pages of 5)
		for i := 1; i <= 12; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		query := PageQuery{Page: 1, PageSize: 5}
		callCount := 0
		totalEntities := 0

		err = dao.ForEachPage(ctx, query, func(result PageResult[*TestEntity]) error {
			callCount++
			totalEntities += len(result.Entities)
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 3, callCount)
		assert.Equal(t, 12, totalEntities)
	})

	t.Run("ForEachPageEarlyStop", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 15 entities
		for i := 1; i <= 15; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		query := PageQuery{Page: 1, PageSize: 5}
		callCount := 0

		err = dao.ForEachPage(ctx, query, func(result PageResult[*TestEntity]) error {
			callCount++
			if callCount == 2 {
				return ErrStopIteration
			}
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 2, callCount)
	})

	t.Run("ForEachPageWithError", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 10 entities
		for i := 1; i <= 10; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		query := PageQuery{Page: 1, PageSize: 5}
		expectedErr := errors.New("callback error")

		err = dao.ForEachPage(ctx, query, func(result PageResult[*TestEntity]) error {
			return expectedErr
		})

		require.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})

	t.Run("ForEachPageInvalidPageNumber", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Add 5 entities
		for i := 1; i <= 5; i++ {
			entity := &TestEntity{
				ID:    uint64(i),
				Name:  "User" + string(rune('0'+i)),
				Email: "user" + string(rune('0'+i)) + "@example.com",
			}
			err = dao.Save(ctx, entity)
			require.NoError(t, err)
		}

		// Start at page 0 (should normalize to 1)
		query := PageQuery{Page: 0, PageSize: 5}
		callCount := 0

		err = dao.ForEachPage(ctx, query, func(result PageResult[*TestEntity]) error {
			callCount++
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, callCount)
	})
}

// TestEntityType validates the EntityType function.
func TestEntityType(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"my_custom_type",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	assert.Equal(t, "my_custom_type", dao.EntityType())
}

// TestConcurrentOperations validates thread-safety of DAO operations.
func TestConcurrentOperations(t *testing.T) {
	t.Run("ConcurrentSaves", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		done := make(chan bool)

		// Save 10 entities concurrently
		for i := 1; i <= 10; i++ {
			go func(id int) {
				entity := &TestEntity{
					ID:    uint64(id),
					Name:  "User" + string(rune('0'+id)),
					Email: "user" + string(rune('0'+id)) + "@example.com",
				}
				err := dao.Save(ctx, entity)
				require.NoError(t, err)
				done <- true
			}(i)
		}

		// Wait for all to complete
		for i := 1; i <= 10; i++ {
			<-done
		}

		count, err := dao.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 10, count)
	})
}

// TestContextWithDeadline verifies that operations respect context deadlines.
func TestContextWithDeadline(t *testing.T) {
	t.Run("SaveWithDeadline", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithTimeout(30*time.Second),
		)
		require.NoError(t, err)

		// Create context with short deadline
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		entity := &TestEntity{Name: "Alice", Email: "alice@example.com"}
		err = dao.Save(ctx, entity)
		require.NoError(t, err)
		assert.NotZero(t, entity.EntityID())
	})

	t.Run("GetWithDeadline", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Save an entity first
		entity := &TestEntity{ID: 123, Name: "Bob", Email: "bob@example.com"}
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// Get with deadline context
		retrieved, err := dao.Get(ctx, 123)
		require.NoError(t, err)
		assert.Equal(t, "Bob", retrieved.Name)
	})

	t.Run("CountWithDeadline", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
		defer cancel()

		count, err := dao.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("DeleteWithDeadline", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Save first
		entity := &TestEntity{ID: 456, Name: "Carol", Email: "carol@example.com"}
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// Delete with deadline context
		err = dao.Delete(ctx, entity)
		require.NoError(t, err)

		// Verify deleted
		_, err = dao.Get(ctx, 456)
		assert.True(t, IsNotFound(err))
	})

	t.Run("PageWithDeadline", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		result, err := dao.Page(ctx, NewPageQuery())
		require.NoError(t, err)
		assert.Empty(t, result.Entities)
	})
}

// TestFactoryPatternDirectly tests factory functions that can be tested in isolation.
func TestFactoryPatternDirectly(t *testing.T) {
	t.Run("NewFactoryCreation", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)
		require.NotNil(t, factory)
	})

	t.Run("FactoryKVStore", func(t *testing.T) {
		store := newMemoryKVStore()
		provider := func(shardID uint64) kvstore.KVStore {
			return store
		}
		factory := NewFactory(provider)
		result := factory.KVStore(1)
		require.NotNil(t, result)
	})

	t.Run("NewDAOViaFactory", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		dao, err := NewDAO[*TestEntity](factory, "users", func() *TestEntity { return &TestEntity{} })
		require.NoError(t, err)
		require.NotNil(t, dao)
		assert.Equal(t, "users", dao.EntityType())
	})

	t.Run("TypedFactoryCreate", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		typedFactory := NewTypedFactory[*TestEntity](
			factory,
			"sessions",
			func() *TestEntity { return &TestEntity{} },
			WithCodec(CodecJSON),
		)
		require.NotNil(t, typedFactory)

		dao, err := typedFactory.Create()
		require.NoError(t, err)
		require.NotNil(t, dao)
		assert.Equal(t, "sessions", dao.EntityType())
	})

	t.Run("TypedFactoryWithOverrides", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		typedFactory := NewTypedFactory[*TestEntity](
			factory,
			"events",
			func() *TestEntity { return &TestEntity{} },
			WithShard(1),
		)

		dao, err := typedFactory.Create(WithCodec(CodecJSON))
		require.NoError(t, err)
		require.NotNil(t, dao)
		assert.Equal(t, "events", dao.EntityType())
	})
}

// ---------------------------------------------------------------------------
// Index query tests — DAO backed by a plain KVStore (no-op index methods)
// ---------------------------------------------------------------------------

// TestGenericDAO_QueryByIndex_PlainStore verifies that QueryByIndex
// on a plain store with no-op index returns empty results without error.
func TestGenericDAO_QueryByIndex_PlainStore(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	results, err := dao.QueryByIndex(ctx, "Email", "alice@example.com")

	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestGenericDAO_QueryOneByIndex_PlainStore verifies that
// QueryOneByIndex on a plain store returns NotFoundError.
func TestGenericDAO_QueryOneByIndex_PlainStore(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"sessions",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := dao.QueryOneByIndex(ctx, "Name", "bob")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, IsNotFound(err))
}

// TestGenericDAO_ScanByIndex_PlainStore verifies that ScanByIndex
// on a plain store returns empty results without error.
func TestGenericDAO_ScanByIndex_PlainStore(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"products",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	results, err := dao.ScanByIndex(ctx, "Name", "a", "z")

	require.NoError(t, err)
	assert.Empty(t, results)
}

// ---------------------------------------------------------------------------
// PageByIndex tests
// ---------------------------------------------------------------------------

// TestGenericDAO_PageByIndex_PlainStore_EmptyResult verifies PageByIndex
// on a plain store returns an empty result.
func TestGenericDAO_PageByIndex_PlainStore_EmptyResult(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := dao.PageByIndex(ctx, "Email", "test@example.com", PageQuery{Page: 1, PageSize: 10})

	require.NoError(t, err)
	assert.Empty(t, result.Entities)
	assert.Equal(t, 0, result.Total)
	assert.False(t, result.HasMore)
}

// ---------------------------------------------------------------------------
// CountByIndex tests
// ---------------------------------------------------------------------------

// TestGenericDAO_CountByIndex_PlainStore_Zero verifies CountByIndex
// on a plain store returns 0.
func TestGenericDAO_CountByIndex_PlainStore_Zero(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	count, err := dao.CountByIndex(ctx, "Email", "test@example.com")

	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// ---------------------------------------------------------------------------
// SortField tests
// ---------------------------------------------------------------------------

// TestGenericDAO_Page_SortField_Empty verifies that Page with empty
// SortField uses default storage key order (backward compat).
func TestGenericDAO_Page_SortField_Empty(t *testing.T) {
	store := newMemoryKVStore()
	dao, err := New[*TestEntity](
		store,
		"users",
		func() *TestEntity { return &TestEntity{} },
	)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, dao.Save(ctx, &TestEntity{ID: 1, Name: "Zara", Email: "z@x.com"}))
	require.NoError(t, dao.Save(ctx, &TestEntity{ID: 2, Name: "Alice", Email: "a@x.com"}))

	result, err := dao.Page(ctx, PageQuery{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, result.Entities, 2)
}
