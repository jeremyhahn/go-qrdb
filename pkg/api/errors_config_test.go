//go:build !integration

package api

import (
	"errors"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestConfigSectionValidationError(t *testing.T) {
	cause := errors.New("port required")
	t.Run("Error", func(t *testing.T) {
		e := &ConfigSectionValidationError{Section: "server", Cause: cause}
		want := "server config: port required"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewConfigSectionValidationError("validate", "raft", cause)
		if de.Code != dberrors.ErrValidation {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestConfigValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		errFn    func() error
		newFn    func() *dberrors.QRDBError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{
			"PositiveValueRequired",
			func() error { return &PositiveValueRequiredError{Field: "port"} },
			func() *dberrors.QRDBError { return NewPositiveValueRequiredError("validate", "port") },
			"port must be positive",
			dberrors.ErrValidation,
		},
		{
			"NonNegativeValueRequired",
			func() error { return &NonNegativeValueRequiredError{Field: "timeout"} },
			func() *dberrors.QRDBError { return NewNonNegativeValueRequiredError("validate", "timeout") },
			"timeout cannot be negative",
			dberrors.ErrValidation,
		},
		{
			"FloatRange",
			func() error { return &FloatRangeError{Field: "ratio", Min: 0.0, Max: 1.0} },
			func() *dberrors.QRDBError { return NewFloatRangeError("validate", "ratio", 0.0, 1.0) },
			"ratio must be between 0 and 1",
			dberrors.ErrValidation,
		},
		{
			"ValueExceedsLimit",
			func() error {
				return &ValueExceedsLimitError{Field: "batch", Value: 200, LimitOf: "max", LimitVal: 100}
			},
			func() *dberrors.QRDBError {
				return NewValueExceedsLimitError("validate", "batch", 200, "max", 100)
			},
			"batch (200) cannot exceed max (100)",
			dberrors.ErrValidation,
		},
		{
			"EmptyWhenEnabled",
			func() error { return &EmptyWhenEnabledError{Field: "cert", Feature: "TLS"} },
			func() *dberrors.QRDBError { return NewEmptyWhenEnabledError("validate", "cert", "TLS") },
			"cert cannot be empty when TLS is enabled",
			dberrors.ErrValidation,
		},
		{
			"ProviderRequirement",
			func() error { return &ProviderRequirementError{Provider: "aws", Requirement: "region"} },
			func() *dberrors.QRDBError { return NewProviderRequirementError("validate", "aws", "region") },
			"aws provider requires region",
			dberrors.ErrValidation,
		},
		{
			"DimensionMismatch",
			func() error {
				return &DimensionMismatchError{Source: "model", SourceDim: 768, Target: "index", TargetDim: 512}
			},
			func() *dberrors.QRDBError {
				return NewDimensionMismatchError("validate", "model", 768, "index", 512)
			},
			"dimension mismatch: model=768, index=512",
			dberrors.ErrValidation,
		},
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

func TestUnsupportedBackendError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnsupportedBackendError{Backend: "redis", Supported: []string{"pebble", "memory"}}
		want := "unsupported storage backend: redis (supported: pebble, memory)"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnsupportedBackendError("config", "sqlite", []string{"pebble"})
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestStorageKeyNotFoundError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &StorageKeyNotFoundError{Key: "my/key"}
		if got := e.Error(); got != "key not found: my/key" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewStorageKeyNotFoundError("config.get", "test")
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestArchiveNotSupportedError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ArchiveNotSupportedError{Backend: "memory"}
		if got := e.Error(); got != "archive not supported for memory storage" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewArchiveNotSupportedError("service", "memory")
		if de.Code != dberrors.ErrUnimplemented {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestBackupErrors(t *testing.T) {
	t.Run("BackupNotEnabled", func(t *testing.T) {
		e := &BackupNotEnabledError{}
		if got := e.Error(); got != "backup is not enabled" {
			t.Errorf("got %q", got)
		}
		de := NewBackupNotEnabledError("backup")
		if de.Code != dberrors.ErrPreconditionFailed {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("BackupBackendRequired", func(t *testing.T) {
		e := &BackupBackendRequiredError{}
		if got := e.Error(); got != "backup storage backend is required when backup is enabled" {
			t.Errorf("got %q", got)
		}
		de := NewBackupBackendRequiredError("config")
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestURLErrors(t *testing.T) {
	t.Run("URLSchemeError", func(t *testing.T) {
		e := &URLSchemeError{Field: "endpoint", AllowedSchemes: []string{"http", "https"}}
		want := "endpoint must use http or https scheme"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewURLSchemeError("validate", "url", []string{"grpc"})
		if de.Code != dberrors.ErrValidation {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("URLMissingHostError", func(t *testing.T) {
		e := &URLMissingHostError{Field: "endpoint"}
		if got := e.Error(); got != "endpoint must include a host" {
			t.Errorf("got %q", got)
		}
		de := NewURLMissingHostError("validate", "url")
		if de.Code != dberrors.ErrValidation {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIntRangeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &IntRangeError{Field: "port", Value: 99999, Min: 1, Max: 65535}
		want := "port must be between 1 and 65535 (got 99999)"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIntRangeError("validate", "workers", 0, 1, 100)
		if de.Code != dberrors.ErrValidation {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestPositiveFloatRequiredError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &PositiveFloatRequiredError{Field: "rate", Value: -1.5}
		want := "rate must be positive (got -1.500000)"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewPositiveFloatRequiredError("validate", "ratio", -0.5)
		if de.Code != dberrors.ErrValidation {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestPositiveIntRequiredError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &PositiveIntRequiredError{Field: "count", Value: -3}
		want := "count must be positive (got -3)"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewPositiveIntRequiredError("validate", "size", 0)
		if de.Code != dberrors.ErrValidation {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEntityConfigValidationError(t *testing.T) {
	cause := errors.New("bad config")
	t.Run("Error", func(t *testing.T) {
		e := &EntityConfigValidationError{Entity: "secrets", Cause: cause}
		want := "invalid config for entity secrets: bad config"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEntityConfigValidationError("validate", "kms", cause)
		if de.Code != dberrors.ErrValidation {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestNilConfigError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &NilConfigError{}
		if got := e.Error(); got != "configuration is nil" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewNilConfigError("server")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestPathAndDirectoryErrors(t *testing.T) {
	cause := errors.New("perm denied")

	t.Run("PathOperationError", func(t *testing.T) {
		e := &PathOperationError{Action: "create dir", Cause: cause}
		want := "failed to create dir: perm denied"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
		de := NewPathOperationError("config", "mkdir", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("NotADirectoryError", func(t *testing.T) {
		e := &NotADirectoryError{}
		if got := e.Error(); got != "path exists but is not a directory" {
			t.Errorf("got %q", got)
		}
		de := NewNotADirectoryError("config")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("DirectoryNotWritableError", func(t *testing.T) {
		e := &DirectoryNotWritableError{Cause: cause}
		want := "directory is not writable: perm denied"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
		de := NewDirectoryNotWritableError("config", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestConfigFileErrors(t *testing.T) {
	cause := errors.New("io error")

	t.Run("FlagBindError", func(t *testing.T) {
		e := &FlagBindError{Flag: "port", Cause: cause}
		want := "failed to bind flag port: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("ConfigFileReadError", func(t *testing.T) {
		e := &ConfigFileReadError{Cause: cause}
		want := "failed to read config file: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("ConfigUnmarshalError", func(t *testing.T) {
		e := &ConfigUnmarshalError{Cause: cause}
		want := "failed to unmarshal config: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("ConfigFileWriteError", func(t *testing.T) {
		e := &ConfigFileWriteError{Path: "/etc/conf.yaml", Cause: cause}
		want := "failed to write config file /etc/conf.yaml: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("ConfigMarshalError", func(t *testing.T) {
		e := &ConfigMarshalError{Cause: cause}
		want := "failed to marshal config: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("InvalidURLError", func(t *testing.T) {
		e := &InvalidURLError{Field: "endpoint", Cause: cause}
		want := "invalid endpoint: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("RequiredWhenEnabledError", func(t *testing.T) {
		e := &RequiredWhenEnabledError{Field: "cert_path", Condition: "tls_enabled"}
		want := "cert_path cannot be empty when tls_enabled is true"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestConfigFileConstructors(t *testing.T) {
	cause := errors.New("io error")

	tests := []struct {
		name     string
		newFn    func() *dberrors.QRDBError
		wantCode dberrors.ErrorCode
	}{
		{"FlagBindError", func() *dberrors.QRDBError { return NewFlagBindError("config", "port", cause) }, dberrors.ErrConfiguration},
		{"ConfigFileReadError", func() *dberrors.QRDBError { return NewConfigFileReadError("config", cause) }, dberrors.ErrConfiguration},
		{"ConfigUnmarshalError", func() *dberrors.QRDBError { return NewConfigUnmarshalError("config", cause) }, dberrors.ErrConfiguration},
		{"ConfigFileWriteError", func() *dberrors.QRDBError { return NewConfigFileWriteError("config", "/etc/conf.yaml", cause) }, dberrors.ErrConfiguration},
		{"ConfigMarshalError", func() *dberrors.QRDBError { return NewConfigMarshalError("config", cause) }, dberrors.ErrConfiguration},
		{"InvalidURLError", func() *dberrors.QRDBError { return NewInvalidURLError("validate", "endpoint", cause) }, dberrors.ErrValidation},
		{"RequiredWhenEnabledError", func() *dberrors.QRDBError {
			return NewRequiredWhenEnabledError("validate", "cert_path", "tls_enabled")
		}, dberrors.ErrValidation},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}
