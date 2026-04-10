//go:build !integration

package api

import (
	"errors"
	"fmt"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestUnsupportedQueryTypeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnsupportedQueryTypeError{TypeName: "fuzzy"}
		if got := e.Error(); got != "unsupported query type: fuzzy" {
			t.Errorf("got %q, want %q", got, "unsupported query type: fuzzy")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnsupportedQueryTypeError("query.Execute", "fuzzy")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
		var target *UnsupportedQueryTypeError
		if !errors.As(de.Err, &target) {
			t.Fatal("expected UnsupportedQueryTypeError")
		}
		if target.TypeName != "fuzzy" {
			t.Errorf("TypeName = %q, want %q", target.TypeName, "fuzzy")
		}
	})
}

func TestUnexpectedResultTypeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnexpectedResultTypeError{TypeName: "float64"}
		if got := e.Error(); got != "unexpected result type: float64" {
			t.Errorf("got %q, want %q", got, "unexpected result type: float64")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnexpectedResultTypeError("query.Parse", "float64")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v, want ErrInternal", de.Code)
		}
	})
}

func TestUnsupportedModeOperationError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnsupportedModeOperationError{Operation: "rebalance", Mode: "standalone"}
		if got := e.Error(); got != "rebalance not supported in standalone" {
			t.Errorf("got %q, want %q", got, "rebalance not supported in standalone")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnsupportedModeOperationError("server.Op", "rebalance", "standalone")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
	})
}

func TestShardNotFoundInModeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ShardNotFoundInModeError{ShardID: 42, Mode: "cluster"}
		expected := "shard 42 not found in cluster"
		if got := e.Error(); got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewShardNotFoundInModeError("shard.Get", 42, "cluster")
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v, want ErrNotFound", de.Code)
		}
	})
}

func TestUnknownServerModeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnknownServerModeError{Mode: "alien"}
		got := e.Error()
		if got == "" {
			t.Error("expected non-empty error string")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownServerModeError("server.Init", "alien")
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v, want ErrConfiguration", de.Code)
		}
	})
}

func TestServerStateError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ServerStateError{State: "shutting down"}
		if got := e.Error(); got != "shutting down" {
			t.Errorf("got %q, want %q", got, "shutting down")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewServerStateError("server.Handle", "shutting down")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v, want ErrInternal", de.Code)
		}
	})
}

func TestNoListenersConfiguredError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &NoListenersConfiguredError{Message: "no listeners"}
		if got := e.Error(); got != "no listeners" {
			t.Errorf("got %q, want %q", got, "no listeners")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewNoListenersConfiguredError("server.Start", "no listeners")
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v, want ErrConfiguration", de.Code)
		}
	})
}

func TestUnknownProviderError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnknownProviderError{Provider: "vault"}
		if got := e.Error(); got != "unknown provider: vault" {
			t.Errorf("got %q, want %q", got, "unknown provider: vault")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownProviderError("kms.Init", "vault")
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v, want ErrConfiguration", de.Code)
		}
	})
}

func TestEntityShardPinError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &EntityShardPinError{EntityType: "user", ShardID: 99}
		expected := `entity "user" pinned to unknown shard 99`
		if got := e.Error(); got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEntityShardPinError("shard.Route", "user", 99)
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v, want ErrConfiguration", de.Code)
		}
	})
}

func TestEncryptionKeySizeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &EncryptionKeySizeError{Expected: 32, Actual: 16}
		expected := "encryption key must be 32 bytes, got 16 bytes"
		if got := e.Error(); got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEncryptionKeySizeError("crypto.Init", 32, 16)
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v, want ErrConfiguration", de.Code)
		}
	})
}

func TestCertGenerationError(t *testing.T) {
	cause := fmt.Errorf("key gen failed")
	t.Run("Error", func(t *testing.T) {
		e := &CertGenerationError{Step: "generate key", Cause: cause}
		expected := "failed to generate key: key gen failed"
		if got := e.Error(); got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &CertGenerationError{Step: "sign", Cause: cause}
		if e.Unwrap() != cause {
			t.Errorf("Unwrap returned wrong error")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewCertGenerationError("tls.Setup", "generate key", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v, want ErrInternal", de.Code)
		}
	})
}

func TestDataDirOperationError(t *testing.T) {
	cause := fmt.Errorf("permission denied")
	t.Run("Error", func(t *testing.T) {
		e := &DataDirOperationError{Action: "create", Path: "/data", Cause: cause}
		expected := "failed to create /data: permission denied"
		if got := e.Error(); got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &DataDirOperationError{Action: "create", Path: "/data", Cause: cause}
		if e.Unwrap() != cause {
			t.Errorf("Unwrap returned wrong error")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewDataDirOperationError("server.Init", "create", "/data", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v, want ErrInternal", de.Code)
		}
	})
}

func TestStateMachineClosedError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &StateMachineClosedError{}
		if got := e.Error(); got != "state machine is closed" {
			t.Errorf("got %q, want %q", got, "state machine is closed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewStateMachineClosedError("sm.Update")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v, want ErrUnavailable", de.Code)
		}
	})
}

func TestNilFieldError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &NilFieldError{Field: "config"}
		if got := e.Error(); got != "config must not be nil" {
			t.Errorf("got %q, want %q", got, "config must not be nil")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewNilFieldError("server.Init", "config")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
	})
}

func TestEmptyFieldError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &EmptyFieldError{Field: "name"}
		if got := e.Error(); got != "name must not be empty" {
			t.Errorf("got %q, want %q", got, "name must not be empty")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEmptyFieldError("user.Create", "name")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
	})
}

func TestKeyLengthError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &KeyLengthError{Expected: 32, Actual: 24}
		expected := "expected key length 32, got 24"
		if got := e.Error(); got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewKeyLengthError("crypto.Validate", 32, 24)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
	})
}

func TestKeyTooShortError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &KeyTooShortError{Component: "prefix"}
		if got := e.Error(); got != "key too short for prefix" {
			t.Errorf("got %q, want %q", got, "key too short for prefix")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewKeyTooShortError("key.Parse", "prefix")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
	})
}

func TestFeatureDisabledError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &FeatureDisabledError{Feature: "encryption"}
		if got := e.Error(); got != "encryption not enabled" {
			t.Errorf("got %q, want %q", got, "encryption not enabled")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewFeatureDisabledError("server.Encrypt", "encryption")
		if de.Code != dberrors.ErrPreconditionFailed {
			t.Errorf("code = %v, want ErrPreconditionFailed", de.Code)
		}
	})
}

func TestZeroValueError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ZeroValueError{Field: "port"}
		if got := e.Error(); got != "port must not be zero" {
			t.Errorf("got %q, want %q", got, "port must not be zero")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewZeroValueError("config.Validate", "port")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
	})
}

func TestHTTPStatusError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &HTTPStatusError{StatusCode: 503, Body: "service unavailable"}
		expected := "HTTP 503: service unavailable"
		if got := e.Error(); got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewHTTPStatusError("http.Do", dberrors.ErrUnavailable, 503, "service unavailable")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v, want ErrUnavailable", de.Code)
		}
	})
}

func TestRemoteError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &RemoteError{Message: "connection refused"}
		if got := e.Error(); got != "connection refused" {
			t.Errorf("got %q, want %q", got, "connection refused")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewRemoteError("rpc.Call", dberrors.ErrUnavailable, "connection refused")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v, want ErrUnavailable", de.Code)
		}
	})
}

func TestMinimumValueError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &MinimumValueError{Field: "replicas", Minimum: "3"}
		if got := e.Error(); got != "replicas must be at least 3" {
			t.Errorf("got %q, want %q", got, "replicas must be at least 3")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewMinimumValueError("config.Validate", "replicas", "3")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
	})
}
