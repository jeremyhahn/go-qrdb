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

package errors

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParameterNotFoundError_Error(t *testing.T) {
	t.Run("includes path in message", func(t *testing.T) {
		err := &ParameterNotFoundError{Path: "/app/config/db-host"}
		assert.Contains(t, err.Error(), "/app/config/db-host")
		assert.Contains(t, err.Error(), "parameter not found")
	})

	t.Run("empty path", func(t *testing.T) {
		err := &ParameterNotFoundError{Path: ""}
		assert.Contains(t, err.Error(), "parameter not found")
	})
}

func TestParameterExistsError_Error(t *testing.T) {
	t.Run("includes path in message", func(t *testing.T) {
		err := &ParameterExistsError{Path: "/app/config/db-host"}
		assert.Contains(t, err.Error(), "/app/config/db-host")
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("empty path", func(t *testing.T) {
		err := &ParameterExistsError{Path: ""}
		assert.Contains(t, err.Error(), "already exists")
	})
}

func TestInvalidPathError_Error(t *testing.T) {
	t.Run("includes path and reason", func(t *testing.T) {
		err := &InvalidPathError{Path: "bad//path", Reason: "double slash not allowed"}
		msg := err.Error()
		assert.Contains(t, msg, "bad//path")
		assert.Contains(t, msg, "double slash not allowed")
	})

	t.Run("empty fields", func(t *testing.T) {
		err := &InvalidPathError{Path: "", Reason: ""}
		assert.Contains(t, err.Error(), "invalid parameter path")
	})
}

func TestParameterExpiredError_Error(t *testing.T) {
	t.Run("includes path and expiry time", func(t *testing.T) {
		expiry := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
		err := &ParameterExpiredError{Path: "/app/secret", ExpiredAt: expiry}
		msg := err.Error()
		assert.Contains(t, msg, "/app/secret")
		assert.Contains(t, msg, "expired")
		assert.Contains(t, msg, "2025-01-15")
	})

	t.Run("zero time", func(t *testing.T) {
		err := &ParameterExpiredError{Path: "/test", ExpiredAt: time.Time{}}
		assert.Contains(t, err.Error(), "expired")
	})
}

func TestParameterEncryptionError_Error(t *testing.T) {
	t.Run("includes op and underlying error", func(t *testing.T) {
		err := &ParameterEncryptionError{
			Op:  "parameterstore.encrypt",
			Err: errors.New("bad key material"),
		}
		msg := err.Error()
		assert.Contains(t, msg, "parameterstore.encrypt")
		assert.Contains(t, msg, "encryption failed")
		assert.Contains(t, msg, "bad key material")
	})

	t.Run("unwrap returns underlying error", func(t *testing.T) {
		inner := errors.New("inner error")
		err := &ParameterEncryptionError{Op: "test", Err: inner}
		assert.Equal(t, inner, err.Unwrap())
	})
}

func TestParameterValueTooLargeError_Error(t *testing.T) {
	t.Run("includes path size and max", func(t *testing.T) {
		err := &ParameterValueTooLargeError{Path: "/big/param", Size: 65536, MaxSize: 4096}
		msg := err.Error()
		assert.Contains(t, msg, "/big/param")
		assert.Contains(t, msg, "65536")
		assert.Contains(t, msg, "4096")
	})
}

func TestParameterPathTooLongError_Error(t *testing.T) {
	t.Run("includes length and max", func(t *testing.T) {
		err := &ParameterPathTooLongError{Path: "/very/long/path", Length: 2048, MaxLength: 512}
		msg := err.Error()
		assert.Contains(t, msg, "2048")
		assert.Contains(t, msg, "512")
	})
}

func TestParameterTagLimitError_Error(t *testing.T) {
	t.Run("includes path count and max", func(t *testing.T) {
		err := &ParameterTagLimitError{Path: "/tagged/param", Count: 55, MaxCount: 50}
		msg := err.Error()
		assert.Contains(t, msg, "/tagged/param")
		assert.Contains(t, msg, "55")
		assert.Contains(t, msg, "50")
	})
}

func TestNewParameterNotFoundError(t *testing.T) {
	t.Run("creates DragonError with correct code", func(t *testing.T) {
		err := NewParameterNotFoundError("/app/missing")
		require.NotNil(t, err)
		assert.Equal(t, ErrNotFound, err.Code)
		assert.Equal(t, "parameterstore", err.Op)

		var pnf *ParameterNotFoundError
		require.True(t, errors.As(err.Err, &pnf))
		assert.Equal(t, "/app/missing", pnf.Path)
	})

	t.Run("empty path still creates error", func(t *testing.T) {
		err := NewParameterNotFoundError("")
		require.NotNil(t, err)
		assert.Equal(t, ErrNotFound, err.Code)
	})
}

func TestNewParameterExistsError(t *testing.T) {
	t.Run("creates DragonError with correct code", func(t *testing.T) {
		err := NewParameterExistsError("/app/existing")
		require.NotNil(t, err)
		assert.Equal(t, ErrAlreadyExists, err.Code)
		assert.Equal(t, "parameterstore", err.Op)

		var pe *ParameterExistsError
		require.True(t, errors.As(err.Err, &pe))
		assert.Equal(t, "/app/existing", pe.Path)
	})

	t.Run("empty path still creates error", func(t *testing.T) {
		err := NewParameterExistsError("")
		require.NotNil(t, err)
		assert.Equal(t, ErrAlreadyExists, err.Code)
	})
}

func TestNewInvalidPathError(t *testing.T) {
	t.Run("creates DragonError with correct code", func(t *testing.T) {
		err := NewInvalidPathError("bad//path", "double slash")
		require.NotNil(t, err)
		assert.Equal(t, ErrInvalidInput, err.Code)
		assert.Equal(t, "parameterstore", err.Op)

		var ip *InvalidPathError
		require.True(t, errors.As(err.Err, &ip))
		assert.Equal(t, "bad//path", ip.Path)
		assert.Equal(t, "double slash", ip.Reason)
	})

	t.Run("empty fields still creates error", func(t *testing.T) {
		err := NewInvalidPathError("", "")
		require.NotNil(t, err)
		assert.Equal(t, ErrInvalidInput, err.Code)
	})
}

func TestNewParameterExpiredError(t *testing.T) {
	t.Run("creates DragonError with correct code", func(t *testing.T) {
		expiry := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
		err := NewParameterExpiredError("/app/secret", expiry)
		require.NotNil(t, err)
		assert.Equal(t, ErrNotFound, err.Code)
		assert.Equal(t, "parameterstore", err.Op)

		var pe *ParameterExpiredError
		require.True(t, errors.As(err.Err, &pe))
		assert.Equal(t, "/app/secret", pe.Path)
		assert.Equal(t, expiry, pe.ExpiredAt)
	})

	t.Run("zero time still creates error", func(t *testing.T) {
		err := NewParameterExpiredError("/test", time.Time{})
		require.NotNil(t, err)
		assert.Equal(t, ErrNotFound, err.Code)
	})
}

func TestNewParameterEncryptionError(t *testing.T) {
	t.Run("creates DragonError with encryption code", func(t *testing.T) {
		inner := errors.New("AES key too short")
		err := NewParameterEncryptionError("encrypt", inner)
		require.NotNil(t, err)
		assert.Equal(t, ErrEncryption, err.Code)
		assert.Equal(t, "encrypt", err.Op)

		var pe *ParameterEncryptionError
		require.True(t, errors.As(err.Err, &pe))
		assert.Equal(t, inner, pe.Err)
	})

	t.Run("nil inner error", func(t *testing.T) {
		err := NewParameterEncryptionError("op", nil)
		require.NotNil(t, err)
		assert.Equal(t, ErrEncryption, err.Code)
	})
}

func TestNewParameterDecryptionError(t *testing.T) {
	t.Run("creates DragonError with decryption code", func(t *testing.T) {
		inner := errors.New("cipher: message authentication failed")
		err := NewParameterDecryptionError("decrypt", inner)
		require.NotNil(t, err)
		assert.Equal(t, ErrDecryption, err.Code)
		assert.Equal(t, "decrypt", err.Op)

		var pe *ParameterEncryptionError
		require.True(t, errors.As(err.Err, &pe))
	})

	t.Run("nil inner error", func(t *testing.T) {
		err := NewParameterDecryptionError("op", nil)
		require.NotNil(t, err)
		assert.Equal(t, ErrDecryption, err.Code)
	})
}

func TestNewParameterValueTooLargeError(t *testing.T) {
	t.Run("creates DragonError with correct fields", func(t *testing.T) {
		err := NewParameterValueTooLargeError("/big/val", 65536, 4096)
		require.NotNil(t, err)
		assert.Equal(t, ErrInvalidInput, err.Code)
		assert.Equal(t, "parameterstore", err.Op)

		var pvtl *ParameterValueTooLargeError
		require.True(t, errors.As(err.Err, &pvtl))
		assert.Equal(t, "/big/val", pvtl.Path)
		assert.Equal(t, 65536, pvtl.Size)
		assert.Equal(t, 4096, pvtl.MaxSize)
	})
}

func TestNewParameterPathTooLongError(t *testing.T) {
	t.Run("creates DragonError with correct fields", func(t *testing.T) {
		err := NewParameterPathTooLongError("/very/long", 2048, 512)
		require.NotNil(t, err)
		assert.Equal(t, ErrInvalidInput, err.Code)
		assert.Equal(t, "parameterstore", err.Op)

		var pptl *ParameterPathTooLongError
		require.True(t, errors.As(err.Err, &pptl))
		assert.Equal(t, 2048, pptl.Length)
		assert.Equal(t, 512, pptl.MaxLength)
	})
}

func TestNewParameterTagLimitError(t *testing.T) {
	t.Run("creates DragonError with correct fields", func(t *testing.T) {
		err := NewParameterTagLimitError("/tagged", 55, 50)
		require.NotNil(t, err)
		assert.Equal(t, ErrInvalidInput, err.Code)
		assert.Equal(t, "parameterstore", err.Op)

		var ptl *ParameterTagLimitError
		require.True(t, errors.As(err.Err, &ptl))
		assert.Equal(t, "/tagged", ptl.Path)
		assert.Equal(t, 55, ptl.Count)
		assert.Equal(t, 50, ptl.MaxCount)
	})
}

func TestIsParameterNotFound(t *testing.T) {
	t.Run("true for ParameterNotFoundError", func(t *testing.T) {
		err := NewParameterNotFoundError("/test")
		assert.True(t, IsParameterNotFound(err))
	})

	t.Run("false for different error", func(t *testing.T) {
		err := NewParameterExistsError("/test")
		assert.False(t, IsParameterNotFound(err))
	})

	t.Run("false for nil", func(t *testing.T) {
		assert.False(t, IsParameterNotFound(nil))
	})
}

func TestIsParameterExists(t *testing.T) {
	t.Run("true for ParameterExistsError", func(t *testing.T) {
		err := NewParameterExistsError("/test")
		assert.True(t, IsParameterExists(err))
	})

	t.Run("false for different error", func(t *testing.T) {
		err := NewParameterNotFoundError("/test")
		assert.False(t, IsParameterExists(err))
	})

	t.Run("false for nil", func(t *testing.T) {
		assert.False(t, IsParameterExists(nil))
	})
}

func TestIsParameterExpired(t *testing.T) {
	t.Run("true for ParameterExpiredError", func(t *testing.T) {
		err := NewParameterExpiredError("/test", time.Now())
		assert.True(t, IsParameterExpired(err))
	})

	t.Run("false for different error", func(t *testing.T) {
		err := NewParameterNotFoundError("/test")
		assert.False(t, IsParameterExpired(err))
	})

	t.Run("false for nil", func(t *testing.T) {
		assert.False(t, IsParameterExpired(nil))
	})
}

func TestIsInvalidPath(t *testing.T) {
	t.Run("true for InvalidPathError", func(t *testing.T) {
		err := NewInvalidPathError("bad", "reason")
		assert.True(t, IsInvalidPath(err))
	})

	t.Run("false for different error", func(t *testing.T) {
		err := NewParameterNotFoundError("/test")
		assert.False(t, IsInvalidPath(err))
	})

	t.Run("false for nil", func(t *testing.T) {
		assert.False(t, IsInvalidPath(nil))
	})
}

func TestIsParameterEncryptionErrorFunc(t *testing.T) {
	t.Run("true for ParameterEncryptionError", func(t *testing.T) {
		err := NewParameterEncryptionError("encrypt", errors.New("fail"))
		assert.True(t, IsParameterEncryptionError(err))
	})

	t.Run("true for decryption error (uses same type)", func(t *testing.T) {
		err := NewParameterDecryptionError("decrypt", errors.New("fail"))
		assert.True(t, IsParameterEncryptionError(err))
	})

	t.Run("false for different error", func(t *testing.T) {
		err := NewParameterNotFoundError("/test")
		assert.False(t, IsParameterEncryptionError(err))
	})

	t.Run("false for nil", func(t *testing.T) {
		assert.False(t, IsParameterEncryptionError(nil))
	})
}
