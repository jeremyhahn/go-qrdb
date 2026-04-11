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
)

func TestQRDBError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *QRDBError
		want string
	}{
		{
			name: "with operation and underlying error",
			err: &QRDBError{
				Code: ErrNotFound,
				Op:   "storage.Get",
				Err:  errors.New("key not found"),
			},
			want: "storage.Get: not_found: key not found",
		},
		{
			name: "with operation only",
			err: &QRDBError{
				Code: ErrTimeout,
				Op:   "raft.Propose",
			},
			want: "raft.Propose: timeout",
		},
		{
			name: "with underlying error only",
			err: &QRDBError{
				Code: ErrInternal,
				Err:  errors.New("unexpected error"),
			},
			want: "internal: unexpected error",
		},
		{
			name: "code only",
			err: &QRDBError{
				Code: ErrConflict,
			},
			want: "conflict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("QRDBError.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQRDBError_Is(t *testing.T) {
	baseErr := errors.New("base error")
	qrdbErr := &QRDBError{
		Code: ErrNotFound,
		Op:   "test",
		Err:  baseErr,
	}

	tests := []struct {
		name   string
		err    *QRDBError
		target error
		want   bool
	}{
		{
			name:   "matches same code",
			err:    qrdbErr,
			target: &QRDBError{Code: ErrNotFound},
			want:   true,
		},
		{
			name:   "does not match different code",
			err:    qrdbErr,
			target: &QRDBError{Code: ErrTimeout},
			want:   false,
		},
		{
			name:   "matches underlying error",
			err:    qrdbErr,
			target: baseErr,
			want:   true,
		},
		{
			name:   "nil target",
			err:    qrdbErr,
			target: nil,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Is(tt.target); got != tt.want {
				t.Errorf("QRDBError.Is() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQRDBError_Unwrap(t *testing.T) {
	baseErr := errors.New("base error")
	qrdbErr := &QRDBError{
		Code: ErrInternal,
		Err:  baseErr,
	}

	if got := qrdbErr.Unwrap(); !errors.Is(got, baseErr) {
		t.Errorf("QRDBError.Unwrap() = %v, want %v", got, baseErr)
	}
}

func TestQRDBError_WithContext(t *testing.T) {
	err := &QRDBError{Code: ErrNotFound}
	err = err.WithContext("key", "test-key")
	err = err.WithContext("table", "users")

	if val, ok := err.GetContext("key"); !ok || val != "test-key" {
		t.Errorf("GetContext(key) = %v, %v; want test-key, true", val, ok)
	}

	if val, ok := err.GetContext("table"); !ok || val != "users" {
		t.Errorf("GetContext(table) = %v, %v; want users, true", val, ok)
	}

	if _, ok := err.GetContext("nonexistent"); ok {
		t.Error("GetContext(nonexistent) should return false")
	}
}

func TestQRDBError_IsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  *QRDBError
		want bool
	}{
		{
			name: "timeout is retryable",
			err:  &QRDBError{Code: ErrTimeout},
			want: true,
		},
		{
			name: "unavailable is retryable",
			err:  &QRDBError{Code: ErrUnavailable},
			want: true,
		},
		{
			name: "not found is not retryable",
			err:  &QRDBError{Code: ErrNotFound},
			want: false,
		},
		{
			name: "explicit retry flag",
			err:  &QRDBError{Code: ErrNotFound, Retry: true},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.IsRetryable(); got != tt.want {
				t.Errorf("QRDBError.IsRetryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	err := New(ErrNotFound, "storage.Get", "key not found")

	if err.Code != ErrNotFound {
		t.Errorf("New().Code = %v, want %v", err.Code, ErrNotFound)
	}

	if err.Op != "storage.Get" {
		t.Errorf("New().Op = %v, want %v", err.Op, "storage.Get")
	}

	if err.Error() != "storage.Get: not_found: key not found" {
		t.Errorf("New().Error() = %v, want %v", err.Error(), "storage.Get: not_found: key not found")
	}
}

func TestNewf(t *testing.T) {
	err := Newf(ErrInvalidInput, "validate", "field %s is required", "username")

	if err.Code != ErrInvalidInput {
		t.Errorf("Newf().Code = %v, want %v", err.Code, ErrInvalidInput)
	}

	wantMsg := "validate: invalid_input: field username is required"
	if err.Error() != wantMsg {
		t.Errorf("Newf().Error() = %v, want %v", err.Error(), wantMsg)
	}
}

func TestWrap(t *testing.T) {
	baseErr := errors.New("base error")

	tests := []struct {
		name    string
		err     error
		code    ErrorCode
		op      string
		wantNil bool
	}{
		{
			name:    "wrap nil returns nil",
			err:     nil,
			code:    ErrInternal,
			op:      "test",
			wantNil: true,
		},
		{
			name:    "wrap standard error",
			err:     baseErr,
			code:    ErrStorage,
			op:      "storage.Get",
			wantNil: false,
		},
		{
			name:    "wrap QRDBError",
			err:     &QRDBError{Code: ErrNotFound, Op: "inner"},
			code:    ErrStorage,
			op:      "outer",
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Wrap(tt.err, tt.code, tt.op)

			if tt.wantNil {
				if got != nil {
					t.Errorf("Wrap() = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Error("Wrap() = nil, want error")
				return
			}

			var de *QRDBError
			if !errors.As(got, &de) {
				t.Error("Wrap() did not return QRDBError")
				return
			}

			if de.Op != tt.op {
				t.Errorf("Wrap().Op = %v, want %v", de.Op, tt.op)
			}
		})
	}
}

func TestIs(t *testing.T) {
	notFoundErr := New(ErrNotFound, "test", "not found")
	timeoutErr := New(ErrTimeout, "test", "timeout")

	tests := []struct {
		name string
		err  error
		code ErrorCode
		want bool
	}{
		{
			name: "matches code",
			err:  notFoundErr,
			code: ErrNotFound,
			want: true,
		},
		{
			name: "does not match code",
			err:  notFoundErr,
			code: ErrTimeout,
			want: false,
		},
		{
			name: "wrapped error preserves original code",
			err:  Wrap(notFoundErr, ErrStorage, "outer"),
			code: ErrNotFound, // Wrapping preserves original error code
			want: true,
		},
		{
			name: "nil error",
			err:  nil,
			code: ErrNotFound,
			want: false,
		},
		{
			name: "standard error",
			err:  errors.New("standard"),
			code: ErrNotFound,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Is(tt.err, tt.code); got != tt.want {
				t.Errorf("Is() = %v, want %v", got, tt.want)
			}
		})
	}

	// Test specific timeout error
	if !Is(timeoutErr, ErrTimeout) {
		t.Error("Is() should return true for timeout error")
	}
}

func TestCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{
			name: "QRDBError",
			err:  New(ErrNotFound, "test", "not found"),
			want: ErrNotFound,
		},
		{
			name: "standard error",
			err:  errors.New("standard"),
			want: ErrUnknown,
		},
		{
			name: "nil error",
			err:  nil,
			want: ErrUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Code(tt.err); got != tt.want {
				t.Errorf("Code() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOperation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "QRDBError with op",
			err:  New(ErrNotFound, "storage.Get", "not found"),
			want: "storage.Get",
		},
		{
			name: "standard error",
			err:  errors.New("standard"),
			want: "",
		},
		{
			name: "nil error",
			err:  nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Operation(tt.err); got != tt.want {
				t.Errorf("Operation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "retryable error",
			err:  New(ErrTimeout, "test", "timeout"),
			want: true,
		},
		{
			name: "non-retryable error",
			err:  New(ErrNotFound, "test", "not found"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "standard error",
			err:  errors.New("standard"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.want {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCommonConstructors(t *testing.T) {
	t.Run("NotFound", func(t *testing.T) {
		err := NotFound("storage.Get", "key")
		if !Is(err, ErrNotFound) {
			t.Error("NotFound() should create ErrNotFound")
		}
	})

	t.Run("AlreadyExists", func(t *testing.T) {
		err := AlreadyExists("storage.Set", "key")
		if !Is(err, ErrAlreadyExists) {
			t.Error("AlreadyExists() should create ErrAlreadyExists")
		}
	})

	t.Run("InvalidInput", func(t *testing.T) {
		err := InvalidInput("validate", "missing field")
		if !Is(err, ErrInvalidInput) {
			t.Error("InvalidInput() should create ErrInvalidInput")
		}
	})

	t.Run("InvalidInputf", func(t *testing.T) {
		err := InvalidInputf("validate", "field %s is invalid", "username")
		if !Is(err, ErrInvalidInput) {
			t.Error("InvalidInputf() should create ErrInvalidInput")
		}
	})

	t.Run("Timeout", func(t *testing.T) {
		err := Timeout("raft.Propose", "5s")
		if !Is(err, ErrTimeout) {
			t.Error("Timeout() should create ErrTimeout")
		}
		if !IsRetryable(err) {
			t.Error("Timeout() should be retryable")
		}
	})

	t.Run("Internal", func(t *testing.T) {
		err := Internal("process", "unexpected state")
		if !Is(err, ErrInternal) {
			t.Error("Internal() should create ErrInternal")
		}
	})

	t.Run("Unavailable", func(t *testing.T) {
		err := Unavailable("cluster", "no leader")
		if !Is(err, ErrUnavailable) {
			t.Error("Unavailable() should create ErrUnavailable")
		}
		if !IsRetryable(err) {
			t.Error("Unavailable() should be retryable")
		}
	})
}

func TestErrorCode_String(t *testing.T) {
	tests := []struct {
		code ErrorCode
		want string
	}{
		{ErrNotFound, "not_found"},
		{ErrTimeout, "timeout"},
		{ErrConflict, "conflict"},
		{ErrInternal, "internal"},
		{ErrorCode(9999), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.code.String(); got != tt.want {
				t.Errorf("ErrorCode.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrorCode_HTTPStatus(t *testing.T) {
	tests := []struct {
		code ErrorCode
		want int
	}{
		{ErrNotFound, 404},
		{ErrConflict, 409},
		{ErrTimeout, 408},
		{ErrInvalidInput, 400},
		{ErrUnauthorized, 401},
		{ErrForbidden, 403},
		{ErrUnavailable, 503},
		{ErrInternal, 500},
	}

	for _, tt := range tests {
		t.Run(tt.code.String(), func(t *testing.T) {
			if got := tt.code.HTTPStatus(); got != tt.want {
				t.Errorf("ErrorCode.HTTPStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrorCode_IsRetryable(t *testing.T) {
	retryable := []ErrorCode{
		ErrTimeout,
		ErrUnavailable,
		ErrLeaderNotAvailable,
		ErrClusterNotReady,
		ErrDeadlineExceeded,
		ErrResourceExhausted,
		ErrAborted,
	}

	for _, code := range retryable {
		t.Run(code.String(), func(t *testing.T) {
			if !code.IsRetryable() {
				t.Errorf("%s should be retryable", code.String())
			}
		})
	}

	nonRetryable := []ErrorCode{
		ErrNotFound,
		ErrConflict,
		ErrInvalidInput,
		ErrUnauthorized,
		ErrForbidden,
	}

	for _, code := range nonRetryable {
		t.Run(code.String(), func(t *testing.T) {
			if code.IsRetryable() {
				t.Errorf("%s should not be retryable", code.String())
			}
		})
	}
}

func TestQuotaExceededError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *QuotaExceededError
		contains []string
	}{
		{
			name: "with tenant ID",
			err: &QuotaExceededError{
				Resource:  "vectors",
				Namespace: "test-ns",
				TenantID:  "tenant-123",
				Limit:     1000,
				Current:   900,
				Requested: 200,
			},
			contains: []string{"tenant-123", "test-ns", "vectors", "1000", "900", "200"},
		},
		{
			name: "without tenant ID",
			err: &QuotaExceededError{
				Resource:  "storage_bytes",
				Namespace: "prod-ns",
				TenantID:  "",
				Limit:     1000000,
				Current:   950000,
				Requested: 100000,
			},
			contains: []string{"prod-ns", "storage_bytes"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errStr := tt.err.Error()
			for _, s := range tt.contains {
				if !contains(errStr, s) {
					t.Errorf("Error() = %q, want to contain %q", errStr, s)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestNewQuotaExceededError(t *testing.T) {
	err := NewQuotaExceededError("vectors", "test-ns", "tenant-abc", 100, 95, 10)

	if err.Code != ErrResourceExhausted {
		t.Errorf("Code = %v, want %v", err.Code, ErrResourceExhausted)
	}

	if err.Op != "quota.check" {
		t.Errorf("Op = %q, want %q", err.Op, "quota.check")
	}

	var qe *QuotaExceededError
	if !errors.As(err.Err, &qe) {
		t.Error("Err should be *QuotaExceededError")
	}

	if qe.Resource != "vectors" {
		t.Errorf("Resource = %q, want %q", qe.Resource, "vectors")
	}
	if qe.Namespace != "test-ns" {
		t.Errorf("Namespace = %q, want %q", qe.Namespace, "test-ns")
	}
	if qe.TenantID != "tenant-abc" {
		t.Errorf("TenantID = %q, want %q", qe.TenantID, "tenant-abc")
	}
	if qe.Limit != 100 {
		t.Errorf("Limit = %d, want %d", qe.Limit, 100)
	}
	if qe.Current != 95 {
		t.Errorf("Current = %d, want %d", qe.Current, 95)
	}
	if qe.Requested != 10 {
		t.Errorf("Requested = %d, want %d", qe.Requested, 10)
	}

	// Check context values
	if err.Context["resource"] != "vectors" {
		t.Errorf("Context[resource] = %v, want %q", err.Context["resource"], "vectors")
	}
}

func TestIsQuotaExceeded(t *testing.T) {
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
			name: "quota exceeded error directly",
			err:  &QuotaExceededError{Resource: "vectors"},
			want: true,
		},
		{
			name: "qrdb error with resource exhausted code",
			err:  NewQuotaExceededError("storage", "ns", "tenant", 100, 90, 20),
			want: true,
		},
		{
			name: "different error",
			err:  &QRDBError{Code: ErrNotFound, Op: "test"},
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
			if got := IsQuotaExceeded(tt.err); got != tt.want {
				t.Errorf("IsQuotaExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsResourceExhausted(t *testing.T) {
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
			name: "resource exhausted error",
			err:  &QRDBError{Code: ErrResourceExhausted},
			want: true,
		},
		{
			name: "different error code",
			err:  &QRDBError{Code: ErrNotFound},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsResourceExhausted(tt.err); got != tt.want {
				t.Errorf("IsResourceExhausted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsMissingJustification(t *testing.T) {
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
			name: "missing justification error",
			err:  NewMissingJustificationError("SwitchTenantContext"),
			want: true,
		},
		{
			name: "qrdb error with missing justification code",
			err:  &QRDBError{Code: ErrMissingJustification},
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
			if got := IsMissingJustification(tt.err); got != tt.want {
				t.Errorf("IsMissingJustification() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsMissingUserContext(t *testing.T) {
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
			name: "missing user context error",
			err:  NewMissingUserContextError("SwitchTenantContext"),
			want: true,
		},
		{
			name: "qrdb error with missing user context code",
			err:  &QRDBError{Code: ErrMissingUserContext},
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
			if got := IsMissingUserContext(tt.err); got != tt.want {
				t.Errorf("IsMissingUserContext() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewMissingJustificationError(t *testing.T) {
	err := NewMissingJustificationError("SwitchTenantContext")

	if err.Code != ErrMissingJustification {
		t.Errorf("Code = %v, want %v", err.Code, ErrMissingJustification)
	}
	if err.Op != "SwitchTenantContext" {
		t.Errorf("Op = %v, want %v", err.Op, "SwitchTenantContext")
	}
	if err.Err == nil {
		t.Error("Err should not be nil")
	}

	// Test error message format
	msg := err.Error()
	if msg == "" {
		t.Error("Error() should return non-empty message")
	}
}

func TestNewMissingUserContextError(t *testing.T) {
	err := NewMissingUserContextError("AuthHandler")

	if err.Code != ErrMissingUserContext {
		t.Errorf("Code = %v, want %v", err.Code, ErrMissingUserContext)
	}
	if err.Op != "AuthHandler" {
		t.Errorf("Op = %v, want %v", err.Op, "AuthHandler")
	}
	if err.Err == nil {
		t.Error("Err should not be nil")
	}

	// Test error message format
	msg := err.Error()
	if msg == "" {
		t.Error("Error() should return non-empty message")
	}
}
