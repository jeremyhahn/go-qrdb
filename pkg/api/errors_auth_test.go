//go:build !integration

package api

import (
	"errors"
	"fmt"
	"testing"
	"time"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestUserNotFoundError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UserNotFoundError{UserID: "user-123"}
		if got := e.Error(); got != "user not found: user-123" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUserNotFoundError("auth.get", "abc")
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestRoleNotFoundError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &RoleNotFoundError{RoleName: "admin"}
		if got := e.Error(); got != "role not found: admin" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewRoleNotFoundError("iam.get", "viewer")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUserAlreadyHasRoleError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UserAlreadyHasRoleError{RoleName: "admin"}
		if got := e.Error(); got != "user already has role: admin" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUserAlreadyHasRoleError("iam.assign", "viewer")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUserDoesNotHaveRoleError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UserDoesNotHaveRoleError{RoleName: "editor"}
		if got := e.Error(); got != "user does not have role: editor" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUserDoesNotHaveRoleError("iam.revoke", "admin")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestAuthCauseErrors(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name     string
		newFn    func() *dberrors.DragonError
		errFn    func() error
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{
			"NonceGenerationError",
			func() *dberrors.DragonError { return NewNonceGenerationError("auth", cause) },
			func() error { return &NonceGenerationError{Cause: cause} },
			"failed to generate nonce: underlying",
			dberrors.ErrInternal,
		},
		{
			"TokenSigningError",
			func() *dberrors.DragonError { return NewTokenSigningError("auth", "access", cause) },
			func() error { return &TokenSigningError{TokenType: "access", Cause: cause} },
			"failed to sign access token: underlying",
			dberrors.ErrInternal,
		},
		{
			"AdminUserDeleteError",
			func() *dberrors.DragonError { return NewAdminUserDeleteError("bootstrap", cause) },
			func() error { return &AdminUserDeleteError{Cause: cause} },
			"failed to delete existing admin user: underlying",
			dberrors.ErrInternal,
		},
		{
			"AdminUserCreateError",
			func() *dberrors.DragonError { return NewAdminUserCreateError("bootstrap", cause) },
			func() error { return &AdminUserCreateError{Cause: cause} },
			"failed to create admin user: underlying",
			dberrors.ErrInternal,
		},
		{
			"BootstrapMarkerError",
			func() *dberrors.DragonError { return NewBootstrapMarkerError("bootstrap", cause) },
			func() error { return &BootstrapMarkerError{Cause: cause} },
			"failed to mark bootstrap complete: underlying",
			dberrors.ErrInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			// Test Unwrap
			type unwrapper interface{ Unwrap() error }
			if uw, ok := e.(unwrapper); ok {
				if uw.Unwrap() != cause {
					t.Error("Unwrap did not return cause")
				}
			}
			// Test constructor
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}

func TestAuthSimpleErrors(t *testing.T) {
	tests := []struct {
		name     string
		newFn    func() *dberrors.DragonError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{"AdminUserNotFound", func() *dberrors.DragonError { return NewAdminUserNotFoundError("bootstrap") }, "admin user not found", dberrors.ErrNotFound},
		{"PasswordAuthDeprecated", func() *dberrors.DragonError { return NewPasswordAuthDeprecatedError("login") }, "password-based authentication is disabled; use IAM (WebAuthn, OIDC, OAuth) instead", dberrors.ErrUnimplemented},
		{"NilLogger", func() *dberrors.DragonError { return NewNilLoggerError("auth.new") }, "logger cannot be nil", dberrors.ErrInvalidInput},
		{"NilStorage", func() *dberrors.DragonError { return NewNilStorageError("auth.new") }, "storage cannot be nil for persistent auth", dberrors.ErrInvalidInput},
		{"StorageClosedAuth", func() *dberrors.DragonError { return NewStorageClosedAuthError("auth.get") }, "storage closed", dberrors.ErrUnavailable},
		{"KeyNotFound", func() *dberrors.DragonError { return NewKeyNotFoundError("auth.get") }, "key not found", dberrors.ErrNotFound},
		{"SnapshotNotSupported", func() *dberrors.DragonError { return NewSnapshotNotSupportedError("auth.snapshot") }, "snapshots not implemented in mock", dberrors.ErrUnimplemented},
		{"BatchFinalized", func() *dberrors.DragonError { return NewBatchFinalizedError("auth.batch") }, "batch already finalized", dberrors.ErrInvalidInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
			if de.Err.Error() != tt.wantMsg {
				t.Errorf("msg = %q, want %q", de.Err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestUnknownAuthManagerTypeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnknownAuthManagerTypeError{TypeName: "custom"}
		if got := e.Error(); got != "unknown auth manager type: custom" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownAuthManagerTypeError("auth", "foo")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestAdminUserExistsError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &AdminUserExistsError{Username: "admin"}
		want := fmt.Sprintf("Admin user '%s' already exists", "admin")
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		e := NewAdminUserExistsError("admin")
		if e.Username != "admin" {
			t.Errorf("username = %q", e.Username)
		}
	})
}

func TestPasswordTooShortError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &PasswordTooShortError{MinLength: 12, GotLength: 5}
		want := "password must be at least 12 characters, got 5"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewPasswordTooShortError("auth.register", 8, 3)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestPasswordHashError(t *testing.T) {
	cause := errors.New("bcrypt cost too high")
	t.Run("Error", func(t *testing.T) {
		e := &PasswordHashError{Cause: cause}
		want := "failed to hash password: bcrypt cost too high"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &PasswordHashError{Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap did not return cause")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewPasswordHashError("auth.hash", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestAccountLockedError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &AccountLockedError{Username: "jdoe", RetryAfter: 5 * time.Minute}
		want := "account locked for user jdoe: too many failed login attempts, retry after 5m0s"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewAccountLockedError("auth.login", "alice", 10*time.Minute)
		if de.Code != dberrors.ErrRateLimited {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIsAccountLocked(t *testing.T) {
	t.Run("True", func(t *testing.T) {
		err := &AccountLockedError{Username: "bob", RetryAfter: time.Minute}
		if !IsAccountLocked(err) {
			t.Error("expected IsAccountLocked to return true")
		}
	})
	t.Run("WrappedTrue", func(t *testing.T) {
		inner := &AccountLockedError{Username: "bob", RetryAfter: time.Minute}
		wrapped := fmt.Errorf("login failed: %w", inner)
		if !IsAccountLocked(wrapped) {
			t.Error("expected IsAccountLocked to return true for wrapped error")
		}
	})
	t.Run("FalseNil", func(t *testing.T) {
		if IsAccountLocked(nil) {
			t.Error("expected IsAccountLocked to return false for nil")
		}
	})
	t.Run("FalseOther", func(t *testing.T) {
		if IsAccountLocked(errors.New("other error")) {
			t.Error("expected IsAccountLocked to return false for non-AccountLockedError")
		}
	})
}
