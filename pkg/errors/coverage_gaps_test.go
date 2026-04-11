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

func TestNewSystemKeyspaceViolationError(t *testing.T) {
	t.Run("creates error with correct code and fields", func(t *testing.T) {
		err := NewSystemKeyspaceViolationError("kv.Put", "__system/config")
		require.NotNil(t, err)
		assert.Equal(t, ErrSystemKeyspaceViolation, err.Code)
		assert.Equal(t, "kv.Put", err.Op)
		assert.Contains(t, err.Error(), "__system/config")
		assert.Contains(t, err.Error(), "system keyspace")

		val, ok := err.Context["key"]
		assert.True(t, ok)
		assert.Equal(t, "__system/config", val)
	})

	t.Run("empty key still creates error", func(t *testing.T) {
		err := NewSystemKeyspaceViolationError("op", "")
		require.NotNil(t, err)
		assert.Equal(t, ErrSystemKeyspaceViolation, err.Code)
	})
}

func TestNewCrossTenantAccessError(t *testing.T) {
	t.Run("creates error with correct code and context", func(t *testing.T) {
		err := NewCrossTenantAccessError("kv.Get", "tenant-A", "tenant-B")
		require.NotNil(t, err)
		assert.Equal(t, ErrCrossTenantAccess, err.Code)
		assert.Equal(t, "kv.Get", err.Op)
		assert.Contains(t, err.Error(), "tenant-A")
		assert.Contains(t, err.Error(), "tenant-B")
		assert.Contains(t, err.Error(), "cross-tenant access denied")

		val, ok := err.Context["requesting_tenant"]
		assert.True(t, ok)
		assert.Equal(t, "tenant-A", val)

		val, ok = err.Context["target_tenant"]
		assert.True(t, ok)
		assert.Equal(t, "tenant-B", val)
	})

	t.Run("empty tenants still creates error", func(t *testing.T) {
		err := NewCrossTenantAccessError("op", "", "")
		require.NotNil(t, err)
		assert.Equal(t, ErrCrossTenantAccess, err.Code)
	})
}

func TestNewMissingTenantContextError(t *testing.T) {
	t.Run("creates error with correct code", func(t *testing.T) {
		err := NewMissingTenantContextError("kv.List")
		require.NotNil(t, err)
		assert.Equal(t, ErrMissingTenantContext, err.Code)
		assert.Equal(t, "kv.List", err.Op)
		assert.Contains(t, err.Error(), "tenant context is required")
	})

	t.Run("empty op still creates error", func(t *testing.T) {
		err := NewMissingTenantContextError("")
		require.NotNil(t, err)
		assert.Equal(t, ErrMissingTenantContext, err.Code)
	})
}

func TestIsSystemKeyspaceViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "system keyspace violation error",
			err:  NewSystemKeyspaceViolationError("op", "key"),
			want: true,
		},
		{
			name: "qrdb error with correct code",
			err:  &QRDBError{Code: ErrSystemKeyspaceViolation},
			want: true,
		},
		{
			name: "different error code",
			err:  &QRDBError{Code: ErrNotFound},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("some error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSystemKeyspaceViolation(tt.err))
		})
	}
}

func TestIsCrossTenantAccess(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "cross tenant access error",
			err:  NewCrossTenantAccessError("op", "a", "b"),
			want: true,
		},
		{
			name: "qrdb error with correct code",
			err:  &QRDBError{Code: ErrCrossTenantAccess},
			want: true,
		},
		{
			name: "different error code",
			err:  &QRDBError{Code: ErrNotFound},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("some error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsCrossTenantAccess(tt.err))
		})
	}
}

func TestIsMissingTenantContext(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "missing tenant context error",
			err:  NewMissingTenantContextError("op"),
			want: true,
		},
		{
			name: "qrdb error with correct code",
			err:  &QRDBError{Code: ErrMissingTenantContext},
			want: true,
		},
		{
			name: "different error code",
			err:  &QRDBError{Code: ErrNotFound},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("some error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsMissingTenantContext(tt.err))
		})
	}
}

func TestNotImplementedHelper(t *testing.T) {
	t.Run("creates error with correct code", func(t *testing.T) {
		err := NotImplemented("feature.X", "not yet available")
		require.Error(t, err)

		var de *QRDBError
		require.ErrorAs(t, err, &de)
		assert.Equal(t, ErrNotImplemented, de.Code)
		assert.Equal(t, "feature.X", de.Op)
		assert.Contains(t, err.Error(), "not yet available")
	})

	t.Run("is not retryable", func(t *testing.T) {
		err := NotImplemented("op", "reason")
		assert.False(t, IsRetryable(err))
	})
}

func TestErrorCode_String_RemainingCodes(t *testing.T) {
	// Test codes that were not covered in existing tests
	codes := []struct {
		code ErrorCode
		want string
	}{
		{ErrQuorumLost, "quorum_lost"},
		{ErrSplitBrain, "split_brain"},
		{ErrRateLimited, "rate_limited"},
		{ErrNotImplemented, "not_implemented"},
		{ErrNotAllowed, "not_allowed"},
		{ErrSystemKeyspaceViolation, "system_keyspace_violation"},
		{ErrCrossTenantAccess, "cross_tenant_access"},
		{ErrMissingTenantContext, "missing_tenant_context"},
		{ErrMissingJustification, "missing_justification"},
		{ErrMissingUserContext, "missing_user_context"},
	}

	for _, tc := range codes {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.code.String())
		})
	}
}

func TestErrorCode_HTTPStatus_RemainingCodes(t *testing.T) {
	// Test codes that were not covered in existing tests
	codes := []struct {
		code   ErrorCode
		status int
	}{
		{ErrNotAllowed, 403},
		{ErrSystemKeyspaceViolation, 403},
		{ErrCrossTenantAccess, 403},
		{ErrMissingTenantContext, 400},
		{ErrMissingJustification, 400},
		{ErrMissingUserContext, 400},
		{ErrQuorumLost, 503},
		{ErrNotImplemented, 501},
		{ErrRateLimited, 429},
		{ErrSplitBrain, 500},
	}

	for _, tc := range codes {
		t.Run(tc.code.String(), func(t *testing.T) {
			assert.Equal(t, tc.status, tc.code.HTTPStatus())
		})
	}
}

func TestErrorCode_IsRetryable_RateLimited(t *testing.T) {
	assert.True(t, ErrRateLimited.IsRetryable())
}
