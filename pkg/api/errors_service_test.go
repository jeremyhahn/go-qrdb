//go:build !integration

package api

import (
	"errors"
	"testing"
	"time"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestServiceClosedError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ServiceClosedError{Service: "kms"}
		if got := e.Error(); got != "kms is closed" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewServiceClosedError("kms.get", "kms")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestServiceSimpleErrors(t *testing.T) {
	tests := []struct {
		name     string
		errFn    func() error
		newFn    func() *dberrors.QRDBError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{"CrossTenantAccess", func() error { return &CrossTenantAccessError{} }, func() *dberrors.QRDBError { return NewCrossTenantAccessError("secrets.get") }, "cross-tenant access denied", dberrors.ErrForbidden},
		{"ChecksumMismatch", func() error { return &ChecksumMismatchError{} }, func() *dberrors.QRDBError { return NewChecksumMismatchError("put") }, "client checksum does not match computed checksum", dberrors.ErrInvalidInput},
		{"AlreadyArchived", func() error { return &AlreadyArchivedError{} }, func() *dberrors.QRDBError { return NewAlreadyArchivedError("archive") }, "object is already archived", dberrors.ErrInvalidInput},
		{"NotArchived", func() error { return &NotArchivedError{} }, func() *dberrors.QRDBError { return NewNotArchivedError("unarchive") }, "object is not archived", dberrors.ErrInvalidInput},
		{"NilRequest", func() error { return &NilRequestError{} }, func() *dberrors.QRDBError { return NewNilRequestError("svc.put") }, "request is nil", dberrors.ErrInvalidInput},
		{"DestinationExists", func() error { return &DestinationExistsError{} }, func() *dberrors.QRDBError { return NewDestinationExistsError("copy") }, "destination key already exists", dberrors.ErrAlreadyExists},
		{"InvalidEmailFormat", func() error { return &InvalidEmailFormatError{} }, func() *dberrors.QRDBError { return NewInvalidEmailFormatError("user.create") }, "invalid email format", dberrors.ErrInvalidInput},
		{"EntityTypeEmpty", func() error { return &EntityTypeEmptyError{} }, func() *dberrors.QRDBError { return NewEntityTypeEmptyError("register") }, "entity type cannot be empty", dberrors.ErrInvalidInput},
		{"DefaultTTLExceedsMax", func() error { return &DefaultTTLExceedsMaxError{} }, func() *dberrors.QRDBError { return NewDefaultTTLExceedsMaxError("validate") }, "default TTL cannot exceed max TTL", dberrors.ErrInvalidInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}

func TestServiceFieldErrors(t *testing.T) {
	tests := []struct {
		name    string
		errFn   func() error
		newFn   func() *dberrors.QRDBError
		wantMsg string
	}{
		{"AEADLimitExceeded", func() error { return &AEADLimitExceededError{KeyType: "DEK", ID: "k1"} }, func() *dberrors.QRDBError { return NewAEADLimitExceededError("svc", "DEK", "k1") }, "AEAD safety limit exceeded for DEK k1: key rotation required"},
		{"ObjectNotAvailable", func() error { return &ObjectNotAvailableError{Status: "deleted"} }, func() *dberrors.QRDBError { return NewObjectNotAvailableError("svc", "deleted") }, "object not available (status: deleted)"},
		{"InvalidPath", func() error { return &InvalidPathError{Message: "path contains //"} }, func() *dberrors.QRDBError { return NewInvalidPathError("svc", "path contains //") }, "path contains //"},
		{"WhitespaceField", func() error { return &WhitespaceFieldError{Field: "name"} }, func() *dberrors.QRDBError { return NewWhitespaceFieldError("svc", "name") }, "name cannot be empty or whitespace"},
		{"NotConfigured", func() error { return &NotConfiguredError{Resource: "backup"} }, func() *dberrors.QRDBError { return NewNotConfiguredError("svc", "backup") }, "no backup configured"},
		{"NoTargetSpecified", func() error { return &NoTargetSpecifiedError{Target: "backend"} }, func() *dberrors.QRDBError { return NewNoTargetSpecifiedError("svc", "backend") }, "no backend specified"},
		{"AlreadyClosed", func() error { return &AlreadyClosedError{Component: "registry"} }, func() *dberrors.QRDBError { return NewAlreadyClosedError("svc", "registry") }, "registry already closed"},
		{"MigrationTargetNotFound", func() error { return &MigrationTargetNotFoundError{Target: "s3"} }, func() *dberrors.QRDBError { return NewMigrationTargetNotFoundError("migrate", "s3") }, "migration target not found: s3"},
		{"GCOperationNotFound", func() error { return &GCOperationNotFoundError{OperationID: "gc-1"} }, func() *dberrors.QRDBError { return NewGCOperationNotFoundError("gc", "gc-1") }, "GC operation not found: gc-1"},
		{"SourceLocationNotFound", func() error { return &SourceLocationNotFoundError{Location: "us-east"} }, func() *dberrors.QRDBError { return NewSourceLocationNotFoundError("catalog", "us-east") }, "source location not found in catalog: us-east"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			de := tt.newFn()
			if de.Err.Error() != tt.wantMsg {
				t.Errorf("constructor msg = %q, want %q", de.Err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestIndexedItemError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &IndexedItemError{Index: 3, Message: "key"}
		want := "key at index 3 cannot be empty"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIndexedItemError("batch.put", "value", 5)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestOverflowError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &OverflowError{Field: "count", Value: 99999, Max: "int16"}
		want := "count 99999 exceeds maximum int16"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewOverflowError("svc", "backends", 500, "int8")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestMaximumValueError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &MaximumValueError{Field: "TTL", Maximum: "24h"}
		want := "TTL cannot exceed 24h"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewMaximumValueError("validate", "size", "1GB")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestDurationErrors(t *testing.T) {
	t.Run("MinDurationValue", func(t *testing.T) {
		e := &MinDurationValueError{Field: "interval", Minimum: 5 * time.Second}
		want := "interval must be at least 5s"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewMinDurationValueError("validate", "interval", 5*time.Second)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("MaxDurationValue", func(t *testing.T) {
		e := &MaxDurationValueError{Field: "timeout", Maximum: time.Hour}
		want := "timeout cannot exceed 1h0m0s"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewMaxDurationValueError("validate", "timeout", time.Hour)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("TTLExceedsMax", func(t *testing.T) {
		e := &TTLExceedsMaxError{Requested: 48 * time.Hour, Maximum: 24 * time.Hour}
		want := "requested TTL exceeds max TTL: 48h0m0s > 24h0m0s"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewTTLExceedsMaxError("validate", 48*time.Hour, 24*time.Hour)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestKeyParseError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &KeyParseError{Message: "invalid format", Key: "bad/key"}
		want := "invalid format: bad/key"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewKeyParseError("storage.parse", "missing separator", "abc")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestReservedEntityPrefixError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ReservedEntityPrefixError{Prefix: "__sys__", EntityType: "custom"}
		want := "entity type cannot use reserved prefix '__sys__': custom"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewReservedEntityPrefixError("register", "_qrdb", "test")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestSeparatorPrefixError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &SeparatorPrefixError{Separator: "/", EntityType: "/bad"}
		want := "entity type cannot start with separator '/': /bad"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewSeparatorPrefixError("register", ".", ".bad")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestContainsObjectsError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ContainsObjectsError{Count: 42, Message: "delete first"}
		want := "backend contains 42 objects; delete first"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewContainsObjectsError("backend.remove", 10, "purge first")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestBackendCountOverflowError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &BackendCountOverflowError{Count: 999999}
		want := "backend count 999999 exceeds maximum int32"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewBackendCountOverflowError("svc", 999999)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestServiceCauseErrors(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name     string
		errFn    func() error
		newFn    func() *dberrors.QRDBError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{
			"BackendUnavailable",
			func() error { return &BackendUnavailableError{Cause: cause} },
			func() *dberrors.QRDBError { return NewBackendUnavailableError("svc", cause) },
			"backend not available: underlying",
			dberrors.ErrUnavailable,
		},
		{
			"GeneratorConfig",
			func() error { return &GeneratorConfigError{Cause: cause} },
			func() *dberrors.QRDBError { return NewGeneratorConfigError("svc", cause) },
			"invalid generator config: underlying",
			dberrors.ErrInvalidInput,
		},
		{
			"NotFoundWithCause",
			func() error { return &NotFoundWithCauseError{Resource: "secret", Cause: cause} },
			func() *dberrors.QRDBError { return NewNotFoundWithCauseError("svc", "secret", cause) },
			"secret not found: underlying",
			dberrors.ErrNotFound,
		},
		{
			"NoAvailableBackend",
			func() error { return &NoAvailableBackendError{Cause: cause} },
			func() *dberrors.QRDBError { return NewNoAvailableBackendError("svc", cause) },
			"no available backend for object: underlying",
			dberrors.ErrUnavailable,
		},
		{
			"SourceBackendUnavailable",
			func() error { return &SourceBackendUnavailableError{Cause: cause} },
			func() *dberrors.QRDBError { return NewSourceBackendUnavailableError("repair", cause) },
			"source backend unavailable: underlying",
			dberrors.ErrStorage,
		},
		{
			"GeneratorNotFound",
			func() error { return &GeneratorNotFoundError{Cause: cause} },
			func() *dberrors.QRDBError { return NewGeneratorNotFoundError("cred", cause) },
			"generator not found: underlying",
			dberrors.ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			type unwrapper interface{ Unwrap() error }
			if uw, ok := e.(unwrapper); ok {
				if uw.Unwrap() != cause {
					t.Error("Unwrap failed")
				}
			}
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}

func TestNotFoundByNameError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &NotFoundByNameError{Resource: "backend", Name: "primary"}
		want := "backend not found: primary"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewNotFoundByNameError("svc", "generator", "uuid")
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}
