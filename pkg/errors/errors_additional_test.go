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

package errors

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapf(t *testing.T) {
	t.Run("wrapf with standard error", func(t *testing.T) {
		baseErr := errors.New("base error")
		err := Wrapf(baseErr, ErrStorage, "storage.Get", "failed to get key %s", "test-key")

		require.Error(t, err)

		var de *QRDBError
		require.ErrorAs(t, err, &de)
		assert.Equal(t, ErrStorage, de.Code)
		assert.Equal(t, "storage.Get", de.Op)
		assert.Contains(t, err.Error(), "failed to get key test-key")
		assert.Contains(t, err.Error(), "base error")
	})

	t.Run("wrapf with nil error", func(t *testing.T) {
		err := Wrapf(nil, ErrStorage, "storage.Get", "some message")
		assert.Nil(t, err)
	})

	t.Run("wrapf preserves retryable", func(t *testing.T) {
		baseErr := errors.New("timeout")
		err := Wrapf(baseErr, ErrTimeout, "api.call", "request timed out")

		require.Error(t, err)
		assert.True(t, IsRetryable(err))
	})
}

func TestQRDBError_WithRetry(t *testing.T) {
	t.Run("set retry to true", func(t *testing.T) {
		err := &QRDBError{Code: ErrNotFound}
		assert.False(t, err.IsRetryable())

		err = err.WithRetry(true)
		assert.True(t, err.IsRetryable())
	})

	t.Run("set retry to false", func(t *testing.T) {
		err := &QRDBError{Code: ErrTimeout, Retry: true}
		assert.True(t, err.IsRetryable())

		err = err.WithRetry(false)
		// Still retryable because code is retryable
		assert.True(t, err.IsRetryable())
	})

	t.Run("explicit retry takes precedence", func(t *testing.T) {
		err := &QRDBError{Code: ErrNotFound, Retry: false}
		assert.False(t, err.IsRetryable())

		err.Retry = true
		assert.True(t, err.IsRetryable())
	})
}

func TestQRDBError_GetContext_Nil(t *testing.T) {
	err := &QRDBError{Code: ErrNotFound, Context: nil}
	val, ok := err.GetContext("key")
	assert.Nil(t, val)
	assert.False(t, ok)
}

func TestAs(t *testing.T) {
	t.Run("successful As", func(t *testing.T) {
		err := New(ErrNotFound, "test", "not found")
		var de *QRDBError
		ok := As(err, &de)
		assert.True(t, ok)
		assert.Equal(t, ErrNotFound, de.Code)
	})

	t.Run("As with non-QRDBError", func(t *testing.T) {
		err := errors.New("standard error")
		var de *QRDBError
		ok := As(err, &de)
		assert.False(t, ok)
	})

	t.Run("As with nil", func(t *testing.T) {
		var de *QRDBError
		ok := As(nil, &de)
		assert.False(t, ok)
	})
}

func TestGetContext(t *testing.T) {
	t.Run("get context from QRDBError", func(t *testing.T) {
		err := New(ErrNotFound, "test", "not found")
		_ = err.WithContext("key", "value")
		_ = err.WithContext("number", 42)

		val, ok := GetContext(err, "key")
		assert.True(t, ok)
		assert.Equal(t, "value", val)

		val, ok = GetContext(err, "number")
		assert.True(t, ok)
		assert.Equal(t, 42, val)
	})

	t.Run("get context from non-QRDBError", func(t *testing.T) {
		err := errors.New("standard error")
		val, ok := GetContext(err, "key")
		assert.False(t, ok)
		assert.Nil(t, val)
	})

	t.Run("get context from nil error", func(t *testing.T) {
		val, ok := GetContext(nil, "key")
		assert.False(t, ok)
		assert.Nil(t, val)
	})

	t.Run("get non-existent context key", func(t *testing.T) {
		err := New(ErrNotFound, "test", "not found")
		val, ok := GetContext(err, "nonexistent")
		assert.False(t, ok)
		assert.Nil(t, val)
	})
}

func TestInternalf(t *testing.T) {
	err := Internalf("process", "unexpected state: %s", "invalid")
	require.Error(t, err)

	var de *QRDBError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, ErrInternal, de.Code)
	assert.Equal(t, "process", de.Op)
	assert.Contains(t, err.Error(), "unexpected state: invalid")
}

func TestUnavailablef(t *testing.T) {
	err := Unavailablef("cluster", "no leader available in %d nodes", 3)
	require.Error(t, err)

	var de *QRDBError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, ErrUnavailable, de.Code)
	assert.Equal(t, "cluster", de.Op)
	assert.Contains(t, err.Error(), "no leader available in 3 nodes")
	assert.True(t, IsRetryable(err))
}

func TestErrorCode_String_AllCodes(t *testing.T) {
	codes := []struct {
		code ErrorCode
		want string
	}{
		{ErrUnknown, "unknown"},
		{ErrNotFound, "not_found"},
		{ErrConflict, "conflict"},
		{ErrTimeout, "timeout"},
		{ErrInternal, "internal"},
		{ErrInvalidInput, "invalid_input"},
		{ErrUnauthorized, "unauthorized"},
		{ErrForbidden, "forbidden"},
		{ErrUnavailable, "unavailable"},
		{ErrStorage, "storage"},
		{ErrRaft, "raft"},
		{ErrEncryption, "encryption"},
		{ErrDecryption, "decryption"},
		{ErrValidation, "validation"},
		{ErrConfiguration, "configuration"},
		{ErrCanceled, "canceled"},
		{ErrDeadlineExceeded, "deadline_exceeded"},
		{ErrAlreadyExists, "already_exists"},
		{ErrResourceExhausted, "resource_exhausted"},
		{ErrPreconditionFailed, "precondition_failed"},
		{ErrAborted, "aborted"},
		{ErrOutOfRange, "out_of_range"},
		{ErrUnimplemented, "unimplemented"},
		{ErrDataLoss, "data_loss"},
		{ErrSnapshot, "snapshot"},
		{ErrReplication, "replication"},
		{ErrLeaderNotAvailable, "leader_not_available"},
		{ErrClusterNotReady, "cluster_not_ready"},
	}

	for _, tc := range codes {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.code.String())
		})
	}
}

func TestErrorCode_HTTPStatus_AllCodes(t *testing.T) {
	codes := []struct {
		code   ErrorCode
		status int
	}{
		{ErrNotFound, 404},
		{ErrConflict, 409},
		{ErrAlreadyExists, 409},
		{ErrTimeout, 408},
		{ErrDeadlineExceeded, 408},
		{ErrInvalidInput, 400},
		{ErrValidation, 400},
		{ErrOutOfRange, 400},
		{ErrUnauthorized, 401},
		{ErrForbidden, 403},
		{ErrUnavailable, 503},
		{ErrLeaderNotAvailable, 503},
		{ErrClusterNotReady, 503},
		{ErrUnimplemented, 501},
		{ErrResourceExhausted, 429},
		{ErrPreconditionFailed, 412},
		{ErrInternal, 500},
		{ErrUnknown, 500},
		{ErrStorage, 503},
		{ErrRaft, 503},
		{ErrEncryption, 500},
		{ErrDecryption, 500},
		{ErrConfiguration, 500},
		{ErrCanceled, 500},
		{ErrAborted, 500},
		{ErrDataLoss, 500},
		{ErrSnapshot, 500},
		{ErrReplication, 500},
	}

	for _, tc := range codes {
		t.Run(tc.code.String(), func(t *testing.T) {
			assert.Equal(t, tc.status, tc.code.HTTPStatus())
		})
	}
}

func TestWrap_PreservesContext(t *testing.T) {
	originalErr := New(ErrNotFound, "inner", "not found")
	_ = originalErr.WithContext("key", "value")
	_ = originalErr.WithContext("id", 123)

	wrappedErr := Wrap(originalErr, ErrStorage, "outer")
	require.Error(t, wrappedErr)

	var de *QRDBError
	require.ErrorAs(t, wrappedErr, &de)
	assert.Equal(t, "outer", de.Op)

	// Context should be preserved
	val, ok := GetContext(wrappedErr, "key")
	assert.True(t, ok)
	assert.Equal(t, "value", val)

	val, ok = GetContext(wrappedErr, "id")
	assert.True(t, ok)
	assert.Equal(t, 123, val)
}

// Retryability follows the CALLER's code, not the inner error's override
// (go-qrdb#187). This test asserted the opposite -- that an explicit
// WithRetry(true) on the wrapped error survives the wrap -- which is the same
// inheritance that let retry.Do hand back a cancellation marked retryable.
//
// The constraint the #187 ruling attached is absolute: a cancellation must not
// inherit Retry=true. Honouring an inner override would reintroduce it by
// another route, since any error whose author called WithRetry(true) would
// carry it through every reclassification above it.
func TestWrap_RetryFollowsTheCallersCode(t *testing.T) {
	originalErr := New(ErrNotFound, "inner", "not found")
	_ = originalErr.WithRetry(true)

	// ErrStorage is not retryable, so the wrapper is not either -- despite the
	// inner error's explicit override.
	wrappedErr := Wrap(originalErr, ErrStorage, "outer")
	require.Error(t, wrappedErr)
	assert.False(t, IsRetryable(wrappedErr),
		"the inner error's WithRetry(true) governed the wrapper's retryability")

	// The override is not destroyed, only no longer authoritative for the
	// outer frame: it is still on the inner error, reachable through the chain.
	var qe *QRDBError
	require.True(t, As(wrappedErr, &qe))
	var inner *QRDBError
	require.True(t, errors.As(qe.Unwrap(), &inner))
	assert.True(t, inner.Retry, "the inner error's own override must survive")

	// And the derivation runs the other way too: a retryable code makes the
	// wrapper retryable even though the inner error is not.
	notRetryable := New(ErrNotFound, "inner", "not found")
	require.False(t, notRetryable.IsRetryable())
	assert.True(t, IsRetryable(Wrap(notRetryable, ErrUnavailable, "outer")))
}

func TestQRDBError_IsWithNilUnderlying(t *testing.T) {
	err := &QRDBError{Code: ErrNotFound, Err: nil}
	standardErr := errors.New("standard")

	assert.False(t, err.Is(standardErr))
	assert.True(t, err.Is(&QRDBError{Code: ErrNotFound}))
}
