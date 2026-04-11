//go:build !integration

package api

import (
	"errors"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestSecuritySimpleErrors(t *testing.T) {
	tests := []struct {
		name     string
		errFn    func() error
		newFn    func() *dberrors.QRDBError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{"SealedError", func() error { return &SealedError{} }, func() *dberrors.QRDBError { return NewSealedError("kms.encrypt") }, "database is sealed", dberrors.ErrUnavailable},
		{"NoActiveKey", func() error { return &NoActiveKeyError{} }, func() *dberrors.QRDBError { return NewNoActiveKeyError("kms.encrypt") }, "no active encryption key available", dberrors.ErrUnavailable},
		{"NoEncrypter", func() error { return &NoEncrypterError{} }, func() *dberrors.QRDBError { return NewNoEncrypterError("encrypt") }, "no encrypter available", dberrors.ErrUnavailable},
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

func TestSecurityFieldErrors(t *testing.T) {
	tests := []struct {
		name    string
		errFn   func() error
		newFn   func() *dberrors.QRDBError
		wantMsg string
	}{
		{"ManagerClosed", func() error { return &ManagerClosedError{Manager: "encryption"} }, func() *dberrors.QRDBError { return NewManagerClosedError("encrypt", "encryption") }, "encryption is closed"},
		{"NotInitialized", func() error { return &NotInitializedError{Component: "key manager"} }, func() *dberrors.QRDBError { return NewNotInitializedError("km.get", "key manager") }, "key manager not initialized"},
		{"AlreadyInProgress", func() error { return &AlreadyInProgressError{Operation: "rotation"} }, func() *dberrors.QRDBError { return NewAlreadyInProgressError("rotate", "rotation") }, "rotation already in progress"},
		{"NoActiveOperation", func() error { return &NoActiveOperationError{Operation: "rotation"} }, func() *dberrors.QRDBError { return NewNoActiveOperationError("cancel", "rotation") }, "no active rotation to cancel"},
		{"UnsupportedProvider", func() error { return &UnsupportedProviderError{Provider: "gcp"} }, func() *dberrors.QRDBError { return NewUnsupportedProviderError("km.init", "gcp") }, "unsupported key provider: gcp"},
		{"EnvVarNotFound", func() error { return &EnvVarNotFoundError{Name: "DB_KEY"} }, func() *dberrors.QRDBError { return NewEnvVarNotFoundError("env.get", "DB_KEY") }, "environment variable not found: DB_KEY"},
		{"PEMDecode", func() error { return &PEMDecodeError{Subject: "private key"} }, func() *dberrors.QRDBError { return NewPEMDecodeError("tls.load", "private key") }, "failed to decode private key PEM"},
		{"WrappingKey", func() error { return &WrappingKeyError{Message: "key too short"} }, func() *dberrors.QRDBError { return NewWrappingKeyError("wrap", "key too short") }, "key too short"},
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

func TestInitializationRequiredError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &InitializationRequiredError{Backend: "HSM", Initializer: "Initialize()"}
		want := "HSM backend requires explicit initialization - use Initialize()"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewInitializationRequiredError("km.init", "TPM", "tpm.Init()")
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnsupportedOperationErrorSecurity(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &UnsupportedOperationError{Operation: "import", Backend: "env"}
		want := "import operation not supported for env backend"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnsupportedOperationError("km.import", "import", "file")
		if de.Code != dberrors.ErrNotImplemented {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestKeyVersionNotFoundErrorSecurity(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &KeyVersionNotFoundError{Version: 3}
		want := "encryption key version 3 not found"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewKeyVersionNotFoundError("decrypt", 5)
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestDataTruncatedError(t *testing.T) {
	t.Run("WithField", func(t *testing.T) {
		e := &DataTruncatedError{Field: "ciphertext", Message: "too short for nonce"}
		want := "ciphertext too short for nonce"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutField", func(t *testing.T) {
		e := &DataTruncatedError{Message: "data corrupted"}
		if got := e.Error(); got != "data corrupted" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewDataTruncatedError("decrypt", "payload", "too short")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestCryptoErrorSecurity(t *testing.T) {
	cause := errors.New("gcm error")
	t.Run("WithCause", func(t *testing.T) {
		e := &CryptoError{Operation: "encrypt", Cause: cause}
		want := "encrypt failed: gcm error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &CryptoError{Operation: "decrypt"}
		want := "decrypt failed"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != nil {
			t.Error("Unwrap should be nil")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewCryptoError("km.encrypt", "seal", dberrors.ErrEncryption, cause)
		if de.Code != dberrors.ErrEncryption {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestBatchEncryptionError(t *testing.T) {
	cause := errors.New("bad data")
	t.Run("Error", func(t *testing.T) {
		e := &BatchEncryptionError{Index: 3, Operation: "encrypt", Cause: cause}
		want := "failed to encrypt item 3: bad data"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &BatchEncryptionError{Index: 0, Operation: "op", Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewBatchEncryptionError("batch.encrypt", "decrypt", 5, dberrors.ErrDecryption, cause)
		if de.Code != dberrors.ErrDecryption {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestCiphertextTooShortError(t *testing.T) {
	t.Run("WithDetail", func(t *testing.T) {
		e := &CiphertextTooShortError{Detail: "need at least 28 bytes"}
		want := "ciphertext too short: need at least 28 bytes"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutDetail", func(t *testing.T) {
		e := &CiphertextTooShortError{}
		if got := e.Error(); got != "ciphertext too short" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewCiphertextTooShortError("decrypt", "nonce missing")
		if de.Code != dberrors.ErrDecryption {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnsupportedAlgorithmErrorSecurity(t *testing.T) {
	cause := errors.New("not implemented")
	t.Run("WithCause", func(t *testing.T) {
		e := &UnsupportedAlgorithmError{Algorithm: "3DES", Cause: cause}
		want := "unsupported algorithm: 3DES: not implemented"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &UnsupportedAlgorithmError{Algorithm: "RC4"}
		want := "unsupported algorithm: RC4"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnsupportedAlgorithmError("encrypt", "DES", cause)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestStorageOperationErrorSecurity(t *testing.T) {
	cause := errors.New("pebble error")
	t.Run("Error", func(t *testing.T) {
		e := &StorageOperationError{Operation: "put key", Cause: cause}
		want := "put key failed: pebble error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &StorageOperationError{Operation: "get", Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewStorageOperationError("km.store", "save", cause)
		if de.Code != dberrors.ErrStorage {
			t.Errorf("code = %v", de.Code)
		}
	})
}
