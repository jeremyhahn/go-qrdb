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

func TestNotImplemented(t *testing.T) {
	err := NotImplemented("feature.X", "not yet available")
	require.Error(t, err)

	var de *QRDBError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, ErrNotImplemented, de.Code)
	assert.Equal(t, "feature.X", de.Op)
	assert.Contains(t, err.Error(), "not yet available")
	assert.False(t, IsRetryable(err))
}

func TestCircuitOpen(t *testing.T) {
	err := CircuitOpen("downstream.Call", "payment-service")
	require.Error(t, err)

	var de *QRDBError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, ErrCircuitOpen, de.Code)
	assert.Equal(t, "downstream.Call", de.Op)
}

func TestIsCircuitOpen(t *testing.T) {
	assert.True(t, IsCircuitOpen(CircuitOpen("op", "my-service")))
	assert.False(t, IsCircuitOpen(nil))
	assert.False(t, IsCircuitOpen(New(ErrTimeout, "op", "timed out")))
	assert.False(t, IsCircuitOpen(errors.New("plain error")))
}

func TestWrapf(t *testing.T) {
	t.Run("wraps with a formatted message", func(t *testing.T) {
		err := Wrapf(errors.New("base error"), ErrStorage, "storage.Get", "failed to get key %s", "test-key")
		require.Error(t, err)

		var de *QRDBError
		require.ErrorAs(t, err, &de)
		assert.Equal(t, ErrStorage, de.Code)
		assert.Equal(t, "storage.Get", de.Op)
		assert.Contains(t, err.Error(), "failed to get key test-key")
		assert.Contains(t, err.Error(), "base error")
	})

	t.Run("nil error stays nil", func(t *testing.T) {
		assert.Nil(t, Wrapf(nil, ErrStorage, "storage.Get", "some message"))
	})

	t.Run("retryable code makes the result retryable", func(t *testing.T) {
		err := Wrapf(errors.New("timeout"), ErrTimeout, "api.call", "request timed out")
		assert.True(t, IsRetryable(err))
	})
}

func TestAs(t *testing.T) {
	var de *QRDBError
	assert.True(t, As(New(ErrNotFound, "test", "not found"), &de))
	assert.Equal(t, ErrNotFound, de.Code)

	var other *QRDBError
	assert.False(t, As(errors.New("standard error"), &other))
	assert.False(t, As(nil, &other))
}

func TestGetContext(t *testing.T) {
	err := New(ErrNotFound, "test", "not found")
	_ = err.WithContext("key", "value")
	_ = err.WithContext("number", 42)

	val, ok := GetContext(err, "key")
	assert.True(t, ok)
	assert.Equal(t, "value", val)

	val, ok = GetContext(err, "number")
	assert.True(t, ok)
	assert.Equal(t, 42, val)

	for name, e := range map[string]error{
		"non-QRDBError": errors.New("standard error"),
		"nil error":     nil,
		"missing key":   New(ErrNotFound, "test", "not found"),
	} {
		val, ok := GetContext(e, "key")
		assert.False(t, ok, name)
		assert.Nil(t, val, name)
	}
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
