//go:build !integration

package api

import (
	"errors"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestEmbeddingInvalidInputError(t *testing.T) {
	sentinel := errors.New("sentinel")
	t.Run("Error", func(t *testing.T) {
		e := &EmbeddingInvalidInputError{Message: "empty text", Sentinel: sentinel}
		if got := e.Error(); got != "invalid input: empty text" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != sentinel {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEmbeddingInvalidInputError("embed", "bad", sentinel)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmbeddingSimpleErrors(t *testing.T) {
	tests := []struct {
		name     string
		newFn    func() *dberrors.QRDBError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{"EmptyResponse", func() *dberrors.QRDBError { return NewEmbeddingEmptyResponseError("embed") }, "no embeddings returned", dberrors.ErrInternal},
		{"EmptyInResponse", func() *dberrors.QRDBError { return NewEmbeddingEmptyInResponseError("embed") }, "no embeddings in response", dberrors.ErrInternal},
		{"CacheCapacity", func() *dberrors.QRDBError { return NewEmbeddingCacheCapacityError("cache") }, "capacity must be positive", dberrors.ErrInvalidInput},
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

func TestEmbeddingRetryExhaustedError(t *testing.T) {
	cause := errors.New("timeout")
	t.Run("Error", func(t *testing.T) {
		e := &EmbeddingRetryExhaustedError{Retries: 3, Cause: cause}
		if got := e.Error(); got != "failed after 3 retries: timeout" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEmbeddingRetryExhaustedError("embed", 5, cause)
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmbeddingCauseErrors(t *testing.T) {
	cause := errors.New("io error")
	tests := []struct {
		name     string
		errFn    func() error
		newFn    func() *dberrors.QRDBError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{
			"MarshalRequest",
			func() error { return &EmbeddingMarshalRequestError{Cause: cause} },
			func() *dberrors.QRDBError { return NewEmbeddingMarshalRequestError("embed", cause) },
			"failed to marshal request: io error",
			dberrors.ErrInternal,
		},
		{
			"CreateRequest",
			func() error { return &EmbeddingCreateRequestError{Cause: cause} },
			func() *dberrors.QRDBError { return NewEmbeddingCreateRequestError("embed", cause) },
			"failed to create request: io error",
			dberrors.ErrInternal,
		},
		{
			"ReadResponse",
			func() error { return &EmbeddingReadResponseError{Cause: cause} },
			func() *dberrors.QRDBError { return NewEmbeddingReadResponseError("embed", cause) },
			"failed to read response: io error",
			dberrors.ErrInternal,
		},
		{
			"UnmarshalResponse",
			func() error { return &EmbeddingUnmarshalResponseError{Cause: cause} },
			func() *dberrors.QRDBError { return NewEmbeddingUnmarshalResponseError("embed", cause) },
			"failed to unmarshal response: io error",
			dberrors.ErrInternal,
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

func TestEmbeddingProviderUnavailableError(t *testing.T) {
	cause := errors.New("conn refused")
	t.Run("WithCause", func(t *testing.T) {
		e := &EmbeddingProviderUnavailableError{Cause: cause}
		if got := e.Error(); got != "provider unavailable: conn refused" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithDetail", func(t *testing.T) {
		e := &EmbeddingProviderUnavailableError{Detail: "server down"}
		if got := e.Error(); got != "provider unavailable: server down" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEmbeddingProviderUnavailableError("embed", cause)
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
	t.Run("ServerNotRunning", func(t *testing.T) {
		de := NewEmbeddingServerNotRunningError("embed", "http://localhost:8080")
		var target *EmbeddingProviderUnavailableError
		if !errors.As(de.Err, &target) {
			t.Fatal("expected EmbeddingProviderUnavailableError")
		}
		if target.Detail != "server not running at http://localhost:8080" {
			t.Errorf("detail = %q", target.Detail)
		}
	})
}

func TestEmbeddingIndexErrors(t *testing.T) {
	t.Run("InvalidIndex", func(t *testing.T) {
		e := &EmbeddingInvalidIndexError{Index: -1}
		if got := e.Error(); got != "invalid embedding index: -1" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("InvalidIndexConstructor", func(t *testing.T) {
		de := NewEmbeddingInvalidIndexError("embed", -1)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
	t.Run("EmptyAtIndex", func(t *testing.T) {
		e := &EmbeddingEmptyAtIndexError{Index: 5}
		if got := e.Error(); got != "received empty embedding at index 5" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("EmptyAtIndexConstructor", func(t *testing.T) {
		de := NewEmbeddingEmptyAtIndexError("embed", 5)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
	t.Run("MissingAtIndex", func(t *testing.T) {
		e := &EmbeddingMissingAtIndexError{Index: 2}
		if got := e.Error(); got != "missing embedding at index 2" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("MissingAtIndexConstructor", func(t *testing.T) {
		de := NewEmbeddingMissingAtIndexError("embed", 2)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmbeddingDimensionsMismatchError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &EmbeddingDimensionsMismatchError{Expected: 768, Received: 384}
		want := "dimensions mismatch: expected 768 dimensions, got 384"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEmbeddingDimensionsMismatchError("embed", 512, 256)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmbeddingHTTPError(t *testing.T) {
	sentinel := errors.New("rate limited")

	t.Run("WithSentinel", func(t *testing.T) {
		e := &EmbeddingHTTPError{StatusCode: 429, Message: "too many requests", Sentinel: sentinel}
		want := "rate limited: too many requests"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != sentinel {
			t.Error("Unwrap failed")
		}
	})

	t.Run("WithoutSentinel", func(t *testing.T) {
		e := &EmbeddingHTTPError{StatusCode: 500, Message: "server error"}
		want := "HTTP 500: server error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("ConstructorStatusCodes", func(t *testing.T) {
		statusCodes := []struct {
			code     int
			wantCode dberrors.ErrorCode
		}{
			{401, dberrors.ErrUnauthorized},
			{403, dberrors.ErrUnauthorized},
			{400, dberrors.ErrInvalidInput},
			{404, dberrors.ErrNotFound},
			{429, dberrors.ErrRateLimited},
			{500, dberrors.ErrUnavailable},
			{503, dberrors.ErrUnavailable},
		}
		for _, sc := range statusCodes {
			de := NewEmbeddingHTTPError("embed", sc.code, "msg", sentinel)
			if de.Code != sc.wantCode {
				t.Errorf("status %d: code = %v, want %v", sc.code, de.Code, sc.wantCode)
			}
		}
	})

	t.Run("ConstructorNoSentinel", func(t *testing.T) {
		de := NewEmbeddingHTTPError("embed", 500, "error", nil)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v, want ErrInternal", de.Code)
		}
	})

	t.Run("ModelNotFound", func(t *testing.T) {
		de := NewEmbeddingHTTPModelNotFoundError("embed", "gpt-4", "not found", sentinel)
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("UnknownError", func(t *testing.T) {
		de := NewEmbeddingHTTPUnknownError("embed", 418, "teapot")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmbeddingProviderAlreadyRegisteredError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &EmbeddingProviderAlreadyRegisteredError{Name: "openai"}
		if got := e.Error(); got != "provider openai already registered" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewEmbeddingProviderAlreadyRegisteredError("registry", "ollama")
		if de.Code != dberrors.ErrAlreadyExists {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmbeddingCloseErrors(t *testing.T) {
	cause := errors.New("close failed")

	t.Run("CloseProviderError", func(t *testing.T) {
		e := &EmbeddingCloseProviderError{Name: "openai", Cause: cause}
		if got := e.Error(); got != "failed to close provider openai: close failed" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})

	t.Run("CloseError", func(t *testing.T) {
		errs := []error{cause}
		e := &EmbeddingCloseError{Errors: errs}
		if got := e.Error(); got != "errors closing providers: [close failed]" {
			t.Errorf("got %q", got)
		}
		de := NewEmbeddingCloseError("registry", errs)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestEmbeddingModelNotFoundError(t *testing.T) {
	sentinel := errors.New("not found")

	t.Run("WithSentinel", func(t *testing.T) {
		e := &EmbeddingModelNotFoundError{Model: "gpt-4", Sentinel: sentinel}
		want := "not found: gpt-4"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != sentinel {
			t.Error("Unwrap failed")
		}
	})

	t.Run("WithoutSentinel", func(t *testing.T) {
		e := &EmbeddingModelNotFoundError{Model: "llama"}
		if got := e.Error(); got != "model not found: llama" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("Constructor", func(t *testing.T) {
		de := NewEmbeddingModelNotFoundError("embed", "bert", sentinel)
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}
