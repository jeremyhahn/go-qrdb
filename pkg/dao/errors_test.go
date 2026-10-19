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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// TestNotFoundError validates the NotFoundError error type.
func TestNotFoundError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		err := NotFoundError{
			EntityType: "User",
			ID:         12345,
		}

		expected := "dao: User with ID 12345 not found"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorMessageDifferentType", func(t *testing.T) {
		err := NotFoundError{
			EntityType: "Session",
			ID:         99999,
		}

		expected := "dao: Session with ID 99999 not found"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorMessageZeroID", func(t *testing.T) {
		err := NotFoundError{
			EntityType: "Product",
			ID:         0,
		}

		expected := "dao: Product with ID 0 not found"
		assert.Equal(t, expected, err.Error())
	})
}

// TestIsNotFound validates the IsNotFound helper function.
func TestIsNotFound(t *testing.T) {
	t.Run("DirectNotFoundError", func(t *testing.T) {
		err := NotFoundError{
			EntityType: "User",
			ID:         123,
		}

		assert.True(t, IsNotFound(err))
	})

	t.Run("PointerToNotFoundError", func(t *testing.T) {
		err := &NotFoundError{
			EntityType: "User",
			ID:         123,
		}

		assert.True(t, IsNotFound(err))
	})

	t.Run("WrappedNotFoundError", func(t *testing.T) {
		baseErr := NotFoundError{
			EntityType: "User",
			ID:         123,
		}
		wrappedErr := &dberrors.WrappedMessageError{Message: "operation failed", Cause: baseErr}

		assert.True(t, IsNotFound(wrappedErr))
	})

	t.Run("NotAnNotFoundError", func(t *testing.T) {
		err := errors.New("some other error")
		assert.False(t, IsNotFound(err))
	})

	t.Run("NilError", func(t *testing.T) {
		assert.False(t, IsNotFound(nil))
	})

	t.Run("DifferentErrorType", func(t *testing.T) {
		err := SerializationFailedError{
			EntityType: "User",
			Cause:      errors.New("json error"),
		}

		assert.False(t, IsNotFound(err))
	})
}

// TestSerializationFailedError validates the SerializationFailedError error type.
func TestSerializationFailedError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		causeErr := errors.New("json: unsupported type")
		err := SerializationFailedError{
			EntityType: "User",
			Cause:      causeErr,
		}

		expected := "dao: failed to serialize User: json: unsupported type"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorMessageDifferentType", func(t *testing.T) {
		causeErr := errors.New("yaml: marshal error")
		err := SerializationFailedError{
			EntityType: "Product",
			Cause:      causeErr,
		}

		expected := "dao: failed to serialize Product: yaml: marshal error"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("Unwrap", func(t *testing.T) {
		causeErr := errors.New("underlying error")
		err := SerializationFailedError{
			EntityType: "Session",
			Cause:      causeErr,
		}

		unwrapped := err.Unwrap()
		assert.Equal(t, causeErr, unwrapped)
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		causeErr := errors.New("codec error")
		err := SerializationFailedError{
			EntityType: "User",
			Cause:      causeErr,
		}

		var serErr SerializationFailedError
		assert.True(t, errors.As(err, &serErr))
		assert.Equal(t, "User", serErr.EntityType)
		assert.Equal(t, causeErr, serErr.Cause)
	})
}

// TestDeserializationFailedError validates the DeserializationFailedError error type.
func TestDeserializationFailedError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		causeErr := errors.New("json: invalid syntax")
		err := DeserializationFailedError{
			EntityType: "User",
			Cause:      causeErr,
		}

		expected := "dao: failed to deserialize User: json: invalid syntax"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorMessageDifferentType", func(t *testing.T) {
		causeErr := errors.New("msgpack: decode error")
		err := DeserializationFailedError{
			EntityType: "Order",
			Cause:      causeErr,
		}

		expected := "dao: failed to deserialize Order: msgpack: decode error"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("Unwrap", func(t *testing.T) {
		causeErr := errors.New("underlying error")
		err := DeserializationFailedError{
			EntityType: "Product",
			Cause:      causeErr,
		}

		unwrapped := err.Unwrap()
		assert.Equal(t, causeErr, unwrapped)
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		causeErr := errors.New("codec error")
		err := DeserializationFailedError{
			EntityType: "Session",
			Cause:      causeErr,
		}

		var deserErr DeserializationFailedError
		assert.True(t, errors.As(err, &deserErr))
		assert.Equal(t, "Session", deserErr.EntityType)
		assert.Equal(t, causeErr, deserErr.Cause)
	})
}

// TestInvalidEntityIDError validates the InvalidEntityIDError error type.
func TestInvalidEntityIDError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		err := InvalidEntityIDError{
			EntityType: "User",
			Reason:     "cannot be zero",
		}

		expected := "dao: invalid entity ID for User: cannot be zero"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorMessageDifferentReason", func(t *testing.T) {
		err := InvalidEntityIDError{
			EntityType: "Product",
			Reason:     "cannot delete entity with ID 0",
		}

		expected := "dao: invalid entity ID for Product: cannot delete entity with ID 0"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		err := InvalidEntityIDError{
			EntityType: "Order",
			Reason:     "ID must be positive",
		}

		var invalidIDErr InvalidEntityIDError
		assert.True(t, errors.As(err, &invalidIDErr))
		assert.Equal(t, "Order", invalidIDErr.EntityType)
		assert.Equal(t, "ID must be positive", invalidIDErr.Reason)
	})
}

// TestCodecCreationError validates the CodecCreationError error type.
func TestCodecCreationError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		causeErr := errors.New("codec not supported")
		err := CodecCreationError{
			CodecType: "cbor",
			Cause:     causeErr,
		}

		expected := "dao: failed to create codec cbor: codec not supported"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorMessageDifferentCodec", func(t *testing.T) {
		causeErr := errors.New("requires proto.Message")
		err := CodecCreationError{
			CodecType: "protobuf",
			Cause:     causeErr,
		}

		expected := "dao: failed to create codec protobuf: requires proto.Message"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("Unwrap", func(t *testing.T) {
		causeErr := errors.New("underlying error")
		err := CodecCreationError{
			CodecType: "custom",
			Cause:     causeErr,
		}

		unwrapped := err.Unwrap()
		assert.Equal(t, causeErr, unwrapped)
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		causeErr := errors.New("not available")
		err := CodecCreationError{
			CodecType: "cbor",
			Cause:     causeErr,
		}

		var codecErr CodecCreationError
		assert.True(t, errors.As(err, &codecErr))
		assert.Equal(t, "cbor", codecErr.CodecType)
		assert.Equal(t, causeErr, codecErr.Cause)
	})
}

// TestInvalidConfigError validates the InvalidConfigError error type.
func TestInvalidConfigError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		err := InvalidConfigError{
			Field:  "store",
			Reason: "cannot be nil",
		}

		expected := "dao: invalid config store: cannot be nil"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorMessageDifferentField", func(t *testing.T) {
		err := InvalidConfigError{
			Field:  "entityType",
			Reason: "cannot be empty",
		}

		expected := "dao: invalid config entityType: cannot be empty"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		err := InvalidConfigError{
			Field:  "newEntity",
			Reason: "factory function required",
		}

		var configErr InvalidConfigError
		assert.True(t, errors.As(err, &configErr))
		assert.Equal(t, "newEntity", configErr.Field)
		assert.Equal(t, "factory function required", configErr.Reason)
	})
}

// TestErrStopIteration validates the ErrStopIteration sentinel error.
func TestErrStopIteration(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		expected := "dao: stop iteration"
		assert.Equal(t, expected, ErrStopIteration.Error())
	})

	t.Run("ErrorComparison", func(t *testing.T) {
		err := ErrStopIteration
		assert.True(t, errors.Is(err, ErrStopIteration))
	})

	t.Run("WrappedComparison", func(t *testing.T) {
		wrapped := &dberrors.WrappedMessageError{Message: "wrapped", Cause: ErrStopIteration}
		assert.True(t, errors.Is(wrapped, ErrStopIteration))
	})
}

// TestErrorWrapping validates error unwrapping for all error types.
func TestErrorWrapping(t *testing.T) {
	t.Run("SerializationFailedWrapping", func(t *testing.T) {
		baseErr := errors.New("json error")
		serErr := SerializationFailedError{
			EntityType: "User",
			Cause:      baseErr,
		}

		assert.True(t, errors.Is(serErr.Unwrap(), baseErr))
	})

	t.Run("DeserializationFailedWrapping", func(t *testing.T) {
		baseErr := errors.New("decode error")
		deserErr := DeserializationFailedError{
			EntityType: "Product",
			Cause:      baseErr,
		}

		assert.True(t, errors.Is(deserErr.Unwrap(), baseErr))
	})

	t.Run("CodecCreationWrapping", func(t *testing.T) {
		baseErr := errors.New("not supported")
		codecErr := CodecCreationError{
			CodecType: "custom",
			Cause:     baseErr,
		}

		assert.True(t, errors.Is(codecErr.Unwrap(), baseErr))
	})

	t.Run("MultiLevelWrapping", func(t *testing.T) {
		baseErr := errors.New("root cause")
		serErr := SerializationFailedError{
			EntityType: "User",
			Cause:      baseErr,
		}
		wrapped := &dberrors.WrappedMessageError{Message: "operation failed", Cause: serErr}

		var serErrUnwrapped SerializationFailedError
		require.True(t, errors.As(wrapped, &serErrUnwrapped))
		assert.Equal(t, baseErr, serErrUnwrapped.Cause)
	})
}

// TestErrorTypeDistinction validates that different error types are distinguishable.
func TestErrorTypeDistinction(t *testing.T) {
	t.Run("DistinguishErrors", func(t *testing.T) {
		notFoundErr := NotFoundError{EntityType: "User", ID: 1}
		serErr := SerializationFailedError{EntityType: "User", Cause: errors.New("err")}
		deserErr := DeserializationFailedError{EntityType: "User", Cause: errors.New("err")}
		invalidIDErr := InvalidEntityIDError{EntityType: "User", Reason: "zero"}
		codecErr := CodecCreationError{CodecType: "json", Cause: errors.New("err")}
		configErr := InvalidConfigError{Field: "store", Reason: "nil"}

		allErrors := []error{notFoundErr, serErr, deserErr, invalidIDErr, codecErr, configErr}

		// Ensure all errors are unique types
		for i, err1 := range allErrors {
			for j, err2 := range allErrors {
				if i != j {
					assert.NotEqual(t, fmt.Sprintf("%T", err1), fmt.Sprintf("%T", err2))
				}
			}
		}
	})
}

// TestErrorMessageFormatting validates consistent error message formatting.
func TestErrorMessageFormatting(t *testing.T) {
	t.Run("AllErrorsStartWithDao", func(t *testing.T) {
		allErrors := []error{
			NotFoundError{EntityType: "User", ID: 1},
			SerializationFailedError{EntityType: "User", Cause: errors.New("err")},
			DeserializationFailedError{EntityType: "User", Cause: errors.New("err")},
			InvalidEntityIDError{EntityType: "User", Reason: "zero"},
			CodecCreationError{CodecType: "json", Cause: errors.New("err")},
			InvalidConfigError{Field: "store", Reason: "nil"},
			ErrStopIteration,
		}

		for _, err := range allErrors {
			assert.Contains(t, err.Error(), "dao:")
		}
	})

	t.Run("ErrorsContainRelevantInfo", func(t *testing.T) {
		notFoundErr := NotFoundError{EntityType: "User", ID: 12345}
		assert.Contains(t, notFoundErr.Error(), "User")
		assert.Contains(t, notFoundErr.Error(), "12345")

		serErr := SerializationFailedError{EntityType: "Product", Cause: errors.New("test")}
		assert.Contains(t, serErr.Error(), "Product")
		assert.Contains(t, serErr.Error(), "test")

		configErr := InvalidConfigError{Field: "timeout", Reason: "negative"}
		assert.Contains(t, configErr.Error(), "timeout")
		assert.Contains(t, configErr.Error(), "negative")
	})
}

func TestAtomicWriteUnsupportedErrorUnwrap(t *testing.T) {
	t.Run("WrapsRefusal", func(t *testing.T) {
		cause := errors.New("proposal refused")
		err := &AtomicWriteUnsupportedError{EntityType: "users", Err: cause}

		assert.Same(t, cause, errors.Unwrap(err))
		assert.ErrorIs(t, err, cause)
	})

	t.Run("NilWhenStoreHasNoAtomicWrite", func(t *testing.T) {
		err := &AtomicWriteUnsupportedError{EntityType: "users"}

		assert.Nil(t, err.Unwrap())
	})
}

func TestAtomicBatchUnsupportedError(t *testing.T) {
	t.Run("ErrorMessageWithoutCause", func(t *testing.T) {
		err := &AtomicBatchUnsupportedError{}

		expected := "dao: unit of work: underlying store does not support atomic multi-key batches"
		assert.Equal(t, expected, err.Error())
		assert.Nil(t, err.Unwrap())
	})

	t.Run("ErrorMessageWithCause", func(t *testing.T) {
		cause := errors.New("guarded batch refused")
		err := &AtomicBatchUnsupportedError{Err: cause}

		expected := "dao: unit of work: underlying store does not support atomic multi-key batches: guarded batch refused"
		assert.Equal(t, expected, err.Error())
		assert.Same(t, cause, errors.Unwrap(err))
		assert.ErrorIs(t, err, cause)
	})
}

func TestIDCollisionError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		err := &IDCollisionError{EntityType: "users", ID: 42}

		expected := "dao: users: ID 42 is already in use by a different entity (hash-derived ID collision)"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		var err error = &IDCollisionError{EntityType: "orders", ID: 7}

		var collision *IDCollisionError
		assert.True(t, errors.As(err, &collision))
		assert.Equal(t, uint64(7), collision.ID)
	})
}

func TestSaveContentionExhaustedError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		err := &SaveContentionExhaustedError{EntityType: "users", ID: 42, Attempts: 5}

		expected := "dao: users: ID 42: update contended by concurrent writers across 5 attempts"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		var err error = &SaveContentionExhaustedError{EntityType: "orders", ID: 9, Attempts: 3}

		var contention *SaveContentionExhaustedError
		assert.True(t, errors.As(err, &contention))
		assert.Equal(t, 3, contention.Attempts)
	})
}

func TestUnsupportedIDGeneratorError(t *testing.T) {
	t.Run("ErrorMessage", func(t *testing.T) {
		err := &UnsupportedIDGeneratorError{EntityType: "users"}

		expected := "dao: users: configured IDGenerator does not support lookup by natural key"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ErrorsAs", func(t *testing.T) {
		var err error = &UnsupportedIDGeneratorError{EntityType: "orders"}

		var unsupported *UnsupportedIDGeneratorError
		assert.True(t, errors.As(err, &unsupported))
		assert.Equal(t, "orders", unsupported.EntityType)
	})
}
