//go:build !integration

package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestRequiredFieldError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &RequiredFieldError{Field: "username"}
		if got := e.Error(); got != "username is required" {
			t.Errorf("got %q, want %q", got, "username is required")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewRequiredFieldError("password")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v, want ErrInvalidInput", de.Code)
		}
		if de.Op != "api.validate" {
			t.Errorf("op = %q, want %q", de.Op, "api.validate")
		}
		var target *RequiredFieldError
		if !errors.As(de.Err, &target) {
			t.Fatal("expected RequiredFieldError in Err")
		}
		if target.Field != "password" {
			t.Errorf("field = %q, want %q", target.Field, "password")
		}
	})
}

func TestServiceUnavailableError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ServiceUnavailableError{Service: "KMS"}
		if got := e.Error(); got != "KMS service not available" {
			t.Errorf("got %q, want %q", got, "KMS service not available")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewServiceUnavailableError("vector")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v, want ErrUnavailable", de.Code)
		}
		var target *ServiceUnavailableError
		if !errors.As(de.Err, &target) {
			t.Fatal("expected ServiceUnavailableError")
		}
		if target.Service != "vector" {
			t.Errorf("service = %q, want %q", target.Service, "vector")
		}
	})
}

func TestNotImplementedError(t *testing.T) {
	t.Run("WithFeature", func(t *testing.T) {
		e := &NotImplementedError{Feature: "streaming"}
		if got := e.Error(); got != "streaming not yet implemented" {
			t.Errorf("got %q, want %q", got, "streaming not yet implemented")
		}
	})

	t.Run("WithoutFeature", func(t *testing.T) {
		e := &NotImplementedError{}
		if got := e.Error(); got != "not yet implemented" {
			t.Errorf("got %q, want %q", got, "not yet implemented")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewNotImplementedError("watch")
		if de.Code != dberrors.ErrNotImplemented {
			t.Errorf("code = %v, want ErrNotImplemented", de.Code)
		}
	})
}

func TestUnknownToolError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnknownToolError{Tool: "qrdb_foo"}
		if got := e.Error(); got != "unknown tool: qrdb_foo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownToolError("bar")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestInvalidFormatError(t *testing.T) {
	cause := errors.New("bad parse")

	t.Run("WithCause", func(t *testing.T) {
		e := &InvalidFormatError{Field: "ttl", Cause: cause}
		want := "invalid ttl format: bad parse"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap did not return cause")
		}
	})

	t.Run("WithMessage", func(t *testing.T) {
		e := &InvalidFormatError{Field: "id", Message: "must be UUID"}
		want := "invalid id format: must be UUID"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != nil {
			t.Error("Unwrap should be nil when no Cause")
		}
	})

	t.Run("ConstructorWithCause", func(t *testing.T) {
		de := NewInvalidFormatError("json", cause)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("ConstructorWithMessage", func(t *testing.T) {
		de := NewInvalidFormatMessageError("date", "must be RFC3339")
		var target *InvalidFormatError
		if !errors.As(de.Err, &target) {
			t.Fatal("expected InvalidFormatError")
		}
		if target.Message != "must be RFC3339" {
			t.Errorf("message = %q", target.Message)
		}
	})
}

func TestUnexpectedResponseError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnexpectedResponseError{Service: "auth", Method: "Login"}
		want := "unexpected response type from auth Login"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUnexpectedResponseError("kms", "Encrypt")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestOperationFailedError(t *testing.T) {
	cause := errors.New("disk full")

	t.Run("Error", func(t *testing.T) {
		e := &OperationFailedError{Operation: "backup", Cause: cause}
		want := "backup failed: disk full"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewOperationFailedError("restore", cause)
		if de.Op != "api.restore" {
			t.Errorf("op = %q", de.Op)
		}
	})
}

func TestInvalidBase64Error(t *testing.T) {
	cause := errors.New("illegal base64")

	t.Run("Error", func(t *testing.T) {
		e := &InvalidBase64Error{Field: "ciphertext", Cause: cause}
		want := "invalid base64 ciphertext: illegal base64"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewInvalidBase64Error("data", cause)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestStreamError(t *testing.T) {
	cause := errors.New("connection reset")

	t.Run("Error", func(t *testing.T) {
		e := &StreamError{Cause: cause}
		if got := e.Error(); got != "stream error: connection reset" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewStreamError(cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestBatchItemError(t *testing.T) {
	cause := errors.New("invalid key")

	t.Run("WithCause", func(t *testing.T) {
		e := &BatchItemError{Index: 3, Field: "key", Cause: cause}
		want := "item 3: key: invalid key"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("WithMessage", func(t *testing.T) {
		e := &BatchItemError{Index: 5, Message: "missing value"}
		want := "item 5: missing value"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("ConstructorWithCause", func(t *testing.T) {
		de := NewBatchItemError(1, "key", cause)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("ConstructorWithMessage", func(t *testing.T) {
		de := NewBatchItemMessageError(2, "empty value")
		var target *BatchItemError
		if !errors.As(de.Err, &target) {
			t.Fatal("expected BatchItemError")
		}
		if target.Message != "empty value" {
			t.Errorf("message = %q", target.Message)
		}
	})
}

func TestServiceNotConfiguredError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ServiceNotConfiguredError{Service: "secrets"}
		if got := e.Error(); got != "secrets service not configured" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewServiceNotConfiguredError("auth")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmptyCollectionError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &EmptyCollectionError{Field: "items"}
		if got := e.Error(); got != "items cannot be empty" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewEmptyCollectionError("vectors")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestRangeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &RangeError{Field: "port", Min: 1024, Max: 65535}
		want := "port must be between 1024 and 65535"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewRangeError("limit", 1, 100)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestNotFoundError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &NotFoundError{Resource: "user", ID: "abc-123"}
		want := "user not found: abc-123"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewNotFoundError("key", "mykey")
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestNoResponseError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &NoResponseError{Operation: "list"}
		if got := e.Error(); got != "no response received from list" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewNoResponseError("query")
		if de.Op != "api.query" {
			t.Errorf("op = %q", de.Op)
		}
	})
}

func TestMaxLimitError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &MaxLimitError{Field: "keys", Max: 1000}
		want := "maximum 1000 keys allowed per batch"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewMaxLimitError("items", 500)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestMarshalError(t *testing.T) {
	cause := errors.New("invalid UTF-8")

	t.Run("Error", func(t *testing.T) {
		e := &MarshalError{Cause: cause}
		if got := e.Error(); got != "failed to marshal result: invalid UTF-8" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewMarshalError(cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestInvalidTypeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &InvalidTypeError{Field: "count", Expected: "an integer"}
		want := "count must be an integer"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewInvalidTypeError("ttl", "a duration")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestNilInputError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &NilInputError{Name: "request"}
		if got := e.Error(); got != "request cannot be nil" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewNilInputError("quic.Get", "config")
		if de.Op != "quic.Get" {
			t.Errorf("op = %q", de.Op)
		}
	})
}

func TestEncodeDecodeErrors(t *testing.T) {
	cause := errors.New("encoding failed")

	t.Run("EncodeError", func(t *testing.T) {
		e := &EncodeError{Subject: "request", Cause: cause}
		want := "failed to encode request: encoding failed"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
		de := NewEncodeError("quic.Put", "payload", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("DecodeError", func(t *testing.T) {
		e := &DecodeError{Subject: "response", Cause: cause}
		want := "failed to decode response: encoding failed"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
		de := NewDecodeError("quic.Get", "body", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestClientClosedError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ClientClosedError{}
		if got := e.Error(); got != "client is closed" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewClientClosedError("quic.Get")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestConnectionError(t *testing.T) {
	cause := errors.New("timeout")

	t.Run("WithTarget", func(t *testing.T) {
		e := &ConnectionError{Action: "connect", Target: "localhost:8080", Cause: cause}
		want := "failed to connect localhost:8080: timeout"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("WithoutTarget", func(t *testing.T) {
		e := &ConnectionError{Action: "dial", Cause: cause}
		want := "failed to dial: timeout"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewConnectionError("quic", "open", "node1", cause)
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestWriteReadErrors(t *testing.T) {
	cause := errors.New("io error")

	t.Run("WriteError", func(t *testing.T) {
		e := &WriteError{Field: "header", Cause: cause}
		want := "failed to write header: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
		de := NewWriteError("quic", "body", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("ReadError", func(t *testing.T) {
		e := &ReadError{Field: "payload", Cause: cause}
		want := "failed to read payload: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
		de := NewReadError("quic", "length", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestSizeLimitError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &SizeLimitError{Field: "value", Size: 2048, Maximum: 1024}
		want := "value size 2048 exceeds maximum 1024"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewSizeLimitError("quic", "key", 500, 256)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestConfigurationError(t *testing.T) {
	cause := errors.New("missing cert")

	t.Run("Error", func(t *testing.T) {
		e := &ConfigurationError{Action: "load TLS", Cause: cause}
		want := "failed to load TLS: missing cert"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewConfigurationError("server", "parse config", cause)
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestComponentUnavailableError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ComponentUnavailableError{Component: "backup manager"}
		if got := e.Error(); got != "backup manager not available" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewComponentUnavailableError("seal handler")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestBatchItemValidationError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &BatchItemValidationError{Index: 7, Message: "key is required"}
		want := "item 7 key is required"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewBatchItemValidationError(0, "empty value")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestInvalidInputError(t *testing.T) {
	cause := errors.New("bad input")

	t.Run("WithCause", func(t *testing.T) {
		e := &InvalidInputError{Message: "invalid key", Cause: cause}
		want := "invalid key: bad input"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("WithoutCause", func(t *testing.T) {
		e := &InvalidInputError{Message: "too short"}
		if got := e.Error(); got != "too short" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != nil {
			t.Error("expected nil Unwrap")
		}
	})

	t.Run("ConstructorMessage", func(t *testing.T) {
		de := NewInvalidInputError("bad data")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("ConstructorCause", func(t *testing.T) {
		de := NewInvalidInputCauseError("parse failed", cause)
		var target *InvalidInputError
		if !errors.As(de.Err, &target) {
			t.Fatal("expected InvalidInputError")
		}
		if target.Cause != cause {
			t.Error("cause mismatch")
		}
	})
}

func TestDataTooShortError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &DataTooShortError{Size: 2}
		if got := e.Error(); got != "data too short: 2 bytes" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewDataTooShortError("quic.decode", 0)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestMissingSubOpcodeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &MissingSubOpcodeError{Service: "KMS"}
		if got := e.Error(); got != "KMS request missing sub-opcode" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewMissingSubOpcodeError("dispatch", "vector")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnknownSubOpcodeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnknownSubOpcodeError{Service: "auth", SubOpcode: 0xFF}
		want := "unknown auth sub-opcode: 0xFF"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownSubOpcodeError("dispatch", "kms", 0xAB)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestInvalidBatchCheckError(t *testing.T) {
	cause := errors.New("bad check")

	t.Run("Error", func(t *testing.T) {
		e := &InvalidBatchCheckError{Index: 2, Cause: cause}
		want := "invalid check at index 2: bad check"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewInvalidBatchCheckError("txn", 0, cause)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestShardNotFoundError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ShardNotFoundError{ShardID: 42}
		want := "shard 42 not found on this node"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewShardNotFoundError("route", 99)
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestItemReadError(t *testing.T) {
	cause := errors.New("eof")

	t.Run("Error", func(t *testing.T) {
		e := &ItemReadError{Field: "value", Index: 3, Cause: cause}
		want := "failed to read value for item 3: eof"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewItemReadError("decode", "key", 1, cause)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmptyListError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &EmptyListError{Field: "keys"}
		if got := e.Error(); got != "keys is empty" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewEmptyListError("batch", "items")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnknownFederationSubOpError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnknownFederationSubOpError{SubOp: 0x10}
		want := "unknown federation sub-operation: 0x10"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownFederationSubOpError("federation", 0x20)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestAuthenticationErrors(t *testing.T) {
	tests := []struct {
		name        string
		constructor func() *dberrors.QRDBError
		wantMsg     string
		wantCode    dberrors.ErrorCode
	}{
		{"AuthenticationDisabled", func() *dberrors.QRDBError { return NewAuthenticationDisabledError("login") }, "authentication is not enabled", dberrors.ErrConfiguration},
		{"PasswordAuthDisabled", func() *dberrors.QRDBError { return NewPasswordAuthDisabledError("login") }, "password-based authentication is disabled; use mTLS certificate or FIDO2/WebAuthn authentication", dberrors.ErrUnauthorized},
		{"TokenRequired", func() *dberrors.QRDBError { return NewTokenRequiredError("api") }, "authentication token required", dberrors.ErrUnauthorized},
		{"TokenRevoked", func() *dberrors.QRDBError { return NewTokenRevokedError("api") }, "token has been revoked", dberrors.ErrUnauthorized},
		{"InsufficientPermissions", func() *dberrors.QRDBError { return NewInsufficientPermissionsError("admin") }, "insufficient permissions", dberrors.ErrForbidden},
		{"InvalidCredentials", func() *dberrors.QRDBError { return NewInvalidCredentialsError("login") }, "invalid credentials", dberrors.ErrUnauthorized},
		{"CredentialsRequired", func() *dberrors.QRDBError { return NewCredentialsRequiredError("login") }, "username and password are required", dberrors.ErrUnauthorized},
		{"NoPasswordSet", func() *dberrors.QRDBError { return NewNoPasswordSetError("login") }, "user has no password set; use IAM authentication", dberrors.ErrUnauthorized},
		{"InvalidTokenClaims", func() *dberrors.QRDBError { return NewInvalidTokenClaimsError("auth") }, "invalid token claims", dberrors.ErrUnauthorized},
		{"InvalidRefreshToken", func() *dberrors.QRDBError { return NewInvalidRefreshTokenError("refresh") }, "invalid refresh token", dberrors.ErrUnauthorized},
		{"InvalidUserType", func() *dberrors.QRDBError { return NewInvalidUserTypeError("auth") }, "invalid user type", dberrors.ErrInternal},
		{"RolesNotFound", func() *dberrors.QRDBError { return NewRolesNotFoundError("assign") }, "one or more roles do not exist", dberrors.ErrInvalidInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			de := tt.constructor()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
			if de.Err.Error() != tt.wantMsg {
				t.Errorf("msg = %q, want %q", de.Err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestInvalidTokenError(t *testing.T) {
	cause := errors.New("expired")

	t.Run("WithCause", func(t *testing.T) {
		e := &InvalidTokenError{Cause: cause}
		if got := e.Error(); got != "invalid token: expired" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("WithoutCause", func(t *testing.T) {
		e := &InvalidTokenError{}
		if got := e.Error(); got != "invalid token" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewInvalidTokenError("auth", cause)
		if de.Code != dberrors.ErrUnauthorized {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnexpectedSigningMethodError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnexpectedSigningMethodError{Algorithm: "RS384"}
		if got := e.Error(); got != "unexpected signing method: RS384" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUnexpectedSigningMethodError("jwt", "HS256")
		if de.Code != dberrors.ErrUnauthorized {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestAlreadyExistsError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &AlreadyExistsError{Resource: "user", ID: "admin"}
		if got := e.Error(); got != "user already exists: admin" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewAlreadyExistsError("iam", "role", "viewer")
		if de.Code != dberrors.ErrAlreadyExists {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestBuiltInRoleError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &BuiltInRoleError{Action: "delete", RoleName: "admin"}
		want := "cannot delete built-in role: admin"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewBuiltInRoleError("iam", "modify", "system")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUserHasRoleError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UserHasRoleError{RoleName: "admin"}
		if got := e.Error(); got != "user already has role: admin" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUserHasRoleError("iam", "editor")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUserMissingRoleError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UserMissingRoleError{RoleName: "admin"}
		if got := e.Error(); got != "user does not have role: admin" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUserMissingRoleError("iam", "viewer")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestReservedKeyPrefixError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ReservedKeyPrefixError{Prefix: "__sys/"}
		want := "key uses reserved internal prefix '__sys/'"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewReservedKeyPrefixError("kv.put", "__internal/")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestDeprecatedOperationError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &DeprecatedOperationError{Message: "use v2 API"}
		if got := e.Error(); got != "use v2 API" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewDeprecatedOperationError("api", "this endpoint is deprecated")
		if de.Code != dberrors.ErrUnimplemented {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnknownManagerTypeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnknownManagerTypeError{TypeName: "custom"}
		if got := e.Error(); got != "unknown auth manager type: custom" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownManagerTypeError("auth", "unknown")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIteratorError(t *testing.T) {
	cause := errors.New("corrupt data")

	t.Run("Error", func(t *testing.T) {
		e := &IteratorError{Cause: cause}
		if got := e.Error(); got != "iterator error: corrupt data" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewIteratorError("storage.list", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnknownOpcodeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnknownOpcodeError{Opcode: 0xFE}
		want := "unknown opcode: 0xFE"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownOpcodeError("unix", 0x01)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestHandlerClosedError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &HandlerClosedError{}
		if got := e.Error(); got != "handler is closed" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewHandlerClosedError("unix.handle")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestProtocolStatusError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ProtocolStatusError{Message: "not found"}
		if got := e.Error(); got != "not found" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewProtocolStatusError("client", dberrors.ErrNotFound, "resource missing")
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestFeatureUnsupportedError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &FeatureUnsupportedError{Feature: "watch"}
		if got := e.Error(); got != "watch is not supported" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewFeatureUnsupportedError("api", "streaming")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestCodecErrors(t *testing.T) {
	t.Run("NilEntityError", func(t *testing.T) {
		e := &NilEntityError{Operation: "encode"}
		if got := e.Error(); got != "cannot encode nil entity" {
			t.Errorf("got %q", got)
		}
		de := NewNilEntityError("codec", "decode")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("EmptyDataError", func(t *testing.T) {
		e := &EmptyDataError{}
		if got := e.Error(); got != "cannot decode empty data" {
			t.Errorf("got %q", got)
		}
		de := NewEmptyDataError("codec.decode")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("CodecNotFoundError", func(t *testing.T) {
		e := &CodecNotFoundError{Codec: "protobuf"}
		if got := e.Error(); got != "codec protobuf not found in registry" {
			t.Errorf("got %q", got)
		}
		de := NewCodecNotFoundError("registry", "msgpack")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("NoSuitableCodecError", func(t *testing.T) {
		e := &NoSuitableCodecError{EntityType: "UserEvent"}
		if got := e.Error(); got != "no suitable codec found for entity type UserEvent" {
			t.Errorf("got %q", got)
		}
		de := NewNoSuitableCodecError("codec", "CustomType")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("NonPointerError", func(t *testing.T) {
		e := &NonPointerError{TypeName: "string"}
		if got := e.Error(); got != "entity must be a pointer, got string" {
			t.Errorf("got %q", got)
		}
		de := NewNonPointerError("codec", "int")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestServerErrors(t *testing.T) {
	t.Run("UnsupportedOpcodeError", func(t *testing.T) {
		e := &UnsupportedOpcodeError{Opcode: 0xFF}
		want := "unsupported operation: 0xff"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewUnsupportedOpcodeError("server", 0x01)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("InvalidDataError", func(t *testing.T) {
		e := &InvalidDataError{Context: "missing header"}
		if got := e.Error(); got != "invalid data: missing header" {
			t.Errorf("got %q", got)
		}
		de := NewInvalidDataError("server", "truncated")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("UnsupportedQueryTypeError", func(t *testing.T) {
		e := &UnsupportedQueryTypeError{TypeName: "spatial"}
		if got := e.Error(); got != "unsupported query type: spatial" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("UnexpectedResultTypeError", func(t *testing.T) {
		e := &UnexpectedResultTypeError{TypeName: "[]byte"}
		if got := e.Error(); got != "unexpected result type: []byte" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("UnsupportedModeOperationError", func(t *testing.T) {
		e := &UnsupportedModeOperationError{Operation: "write", Mode: "readonly"}
		if got := e.Error(); got != "write not supported in readonly" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("ShardNotFoundInModeError", func(t *testing.T) {
		e := &ShardNotFoundInModeError{ShardID: 5, Mode: "standalone"}
		want := "shard 5 not found in standalone"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("UnknownServerModeError", func(t *testing.T) {
		e := &UnknownServerModeError{Mode: "invalid"}
		want := `unknown server mode "invalid", valid modes are: standalone, cluster, devcluster`
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("ServerStateError", func(t *testing.T) {
		e := &ServerStateError{State: "shutting down"}
		if got := e.Error(); got != "shutting down" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("NoListenersConfiguredError", func(t *testing.T) {
		e := &NoListenersConfiguredError{Message: "no listeners"}
		if got := e.Error(); got != "no listeners" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("ShutdownError", func(t *testing.T) {
		errs := []error{errors.New("err1"), errors.New("err2")}
		e := &ShutdownError{Count: 2, Errors: errs}
		want := fmt.Sprintf("shutdown completed with 2 errors: %v", errs)
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewShutdownError("server", errs)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestMiscErrors(t *testing.T) {
	cause := errors.New("test cause")

	t.Run("UnknownProviderError", func(t *testing.T) {
		e := &UnknownProviderError{Provider: "aws"}
		if got := e.Error(); got != "unknown provider: aws" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("EntityShardPinError", func(t *testing.T) {
		e := &EntityShardPinError{EntityType: "secret", ShardID: 10}
		want := `entity "secret" pinned to unknown shard 10`
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("EncryptionKeySizeError", func(t *testing.T) {
		e := &EncryptionKeySizeError{Expected: 32, Actual: 16}
		want := "encryption key must be 32 bytes, got 16 bytes"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("CertGenerationError", func(t *testing.T) {
		e := &CertGenerationError{Step: "generate key", Cause: cause}
		want := "failed to generate key: test cause"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("DataDirOperationError", func(t *testing.T) {
		e := &DataDirOperationError{Action: "create", Path: "/data", Cause: cause}
		want := "failed to create /data: test cause"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("StateMachineClosedError", func(t *testing.T) {
		e := &StateMachineClosedError{}
		if got := e.Error(); got != "state machine is closed" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("NilFieldError", func(t *testing.T) {
		e := &NilFieldError{Field: "config"}
		if got := e.Error(); got != "config must not be nil" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("EmptyFieldError", func(t *testing.T) {
		e := &EmptyFieldError{Field: "name"}
		if got := e.Error(); got != "name must not be empty" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("KeyLengthError", func(t *testing.T) {
		e := &KeyLengthError{Expected: 32, Actual: 16}
		want := "expected key length 32, got 16"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("KeyTooShortError", func(t *testing.T) {
		e := &KeyTooShortError{Component: "entity prefix"}
		if got := e.Error(); got != "key too short for entity prefix" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("FeatureDisabledError", func(t *testing.T) {
		e := &FeatureDisabledError{Feature: "encryption"}
		if got := e.Error(); got != "encryption not enabled" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("ZeroValueError", func(t *testing.T) {
		e := &ZeroValueError{Field: "shard_id"}
		if got := e.Error(); got != "shard_id must not be zero" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("HTTPStatusError", func(t *testing.T) {
		e := &HTTPStatusError{StatusCode: 404, Body: "not found"}
		want := "HTTP 404: not found"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("RemoteError", func(t *testing.T) {
		e := &RemoteError{Message: "server error"}
		if got := e.Error(); got != "server error" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("MinimumValueError", func(t *testing.T) {
		e := &MinimumValueError{Field: "replicas", Minimum: "3"}
		if got := e.Error(); got != "replicas must be at least 3" {
			t.Errorf("got %q", got)
		}
	})
}

// TestAPIServiceNotConfiguredErrorHint verifies the optional Hint is appended to the message
// so refusals can name the remedy (e.g. "requires mode: raft").
func TestAPIServiceNotConfiguredErrorHint(t *testing.T) {
	err := &ServiceNotConfiguredError{Service: "watch", Hint: "requires mode: raft"}
	if got := err.Error(); !strings.Contains(got, "watch") || !strings.Contains(got, "requires mode: raft") {
		t.Fatalf("Error() = %q, want the service name and the hint", got)
	}
	bare := &ServiceNotConfiguredError{Service: "watch"}
	if strings.Contains(bare.Error(), ":  ") || strings.HasSuffix(bare.Error(), ": ") {
		t.Fatalf("Error() without hint = %q, must not carry a dangling separator", bare.Error())
	}
}
