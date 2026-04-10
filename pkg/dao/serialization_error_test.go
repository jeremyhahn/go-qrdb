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
)

// failingCodec is a codec that always fails for testing.
type failingCodec[E any] struct{}

func (c *failingCodec[E]) Marshal(_ E) ([]byte, error) {
	return nil, errors.New("marshal failed")
}

func (c *failingCodec[E]) Unmarshal(_ []byte, _ *E) error {
	return errors.New("unmarshal failed")
}

func (c *failingCodec[E]) Type() CodecType {
	return CodecJSON
}

// TestSaveSerializationError validates the serialization error path.
func TestSaveSerializationError(t *testing.T) {
	t.Run("SaveReturnsSerializationFailedError", func(t *testing.T) {
		store := newMemoryKVStore()
		// Create DAO normally first
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		// Access internal and replace codec with failing one
		gDao := dao.(*genericDAO[*TestEntity])
		gDao.codec = &failingCodec[*TestEntity]{}

		entity := &TestEntity{ID: 1, Name: "Test", Email: "test@test.com"}
		ctx := context.Background()
		err = gDao.Save(ctx, entity)
		require.Error(t, err)

		var serErr SerializationFailedError
		require.True(t, errors.As(err, &serErr))
		assert.Equal(t, "users", serErr.EntityType)
		assert.Contains(t, serErr.Error(), "failed to serialize")
	})
}

// TestGetDeserializationError validates the deserialization error path.
func TestGetDeserializationError(t *testing.T) {
	t.Run("GetReturnsDeserializationFailedError", func(t *testing.T) {
		// Create a store with corrupt data
		store := newMemoryKVStore()
		ctx := context.Background()
		// Store invalid JSON data directly
		err := store.Put(ctx, "users/123", []byte("not valid json"))
		require.NoError(t, err)

		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		// Try to get entity with invalid data
		_, err = dao.Get(ctx, 123)
		require.Error(t, err)

		var deserErr DeserializationFailedError
		require.True(t, errors.As(err, &deserErr))
		assert.Equal(t, "users", deserErr.EntityType)
		assert.Contains(t, deserErr.Error(), "failed to deserialize")
	})
}

// TestSerializationFailedErrorMethods validates SerializationFailedError methods.
func TestSerializationFailedErrorMethods(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		cause := errors.New("test cause")
		err := SerializationFailedError{EntityType: "users", Cause: cause}
		msg := err.Error()
		assert.Contains(t, msg, "users")
		assert.Contains(t, msg, "test cause")
	})

	t.Run("Unwrap", func(t *testing.T) {
		cause := errors.New("test cause")
		err := SerializationFailedError{EntityType: "users", Cause: cause}
		assert.Equal(t, cause, err.Unwrap())
	})
}

// TestDeserializationFailedErrorMethods validates DeserializationFailedError methods.
func TestDeserializationFailedErrorMethods(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		cause := errors.New("test cause")
		err := DeserializationFailedError{EntityType: "users", Cause: cause}
		msg := err.Error()
		assert.Contains(t, msg, "users")
		assert.Contains(t, msg, "test cause")
	})

	t.Run("Unwrap", func(t *testing.T) {
		cause := errors.New("test cause")
		err := DeserializationFailedError{EntityType: "users", Cause: cause}
		assert.Equal(t, cause, err.Unwrap())
	})
}

// TestCodecCreationErrorMethods validates CodecCreationError methods.
func TestCodecCreationErrorMethods(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		cause := errors.New("test cause")
		err := CodecCreationError{CodecType: "json", Cause: cause}
		msg := err.Error()
		assert.Contains(t, msg, "json")
		assert.Contains(t, msg, "test cause")
	})

	t.Run("Unwrap", func(t *testing.T) {
		cause := errors.New("test cause")
		err := CodecCreationError{CodecType: "json", Cause: cause}
		assert.Equal(t, cause, err.Unwrap())
	})
}

// TestInvalidEntityIDErrorMethods validates InvalidEntityIDError methods.
func TestInvalidEntityIDErrorMethods(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		err := InvalidEntityIDError{EntityType: "users", Reason: "cannot be zero"}
		msg := err.Error()
		assert.Contains(t, msg, "users")
		assert.Contains(t, msg, "cannot be zero")
	})
}

// TestInvalidConfigErrorMethods validates InvalidConfigError methods.
func TestInvalidConfigErrorMethods(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		err := InvalidConfigError{Field: "timeout", Reason: "must be positive"}
		msg := err.Error()
		assert.Contains(t, msg, "timeout")
		assert.Contains(t, msg, "must be positive")
	})
}

// TestNotFoundErrorMethods validates NotFoundError methods.
func TestNotFoundErrorMethods(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		err := NotFoundError{EntityType: "users", ID: 123}
		msg := err.Error()
		assert.Contains(t, msg, "users")
		assert.Contains(t, msg, "123")
		assert.Contains(t, msg, "not found")
	})
}
