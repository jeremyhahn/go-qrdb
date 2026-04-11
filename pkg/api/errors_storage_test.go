//go:build !integration

package api

import (
	"errors"
	"fmt"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestStorageClosedError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &StorageClosedError{}
		if got := e.Error(); got != "storage is closed" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewStorageClosedError("storage.get")
		if de.Code != dberrors.ErrUnavailable {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestValidationErrorStorage(t *testing.T) {
	cause := errors.New("bad value")
	t.Run("WithCause", func(t *testing.T) {
		e := &ValidationError{Message: "field X", Cause: cause}
		want := "field X: bad value"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &ValidationError{Message: "required field"}
		if got := e.Error(); got != "required field" {
			t.Errorf("got %q", got)
		}
		if e.Unwrap() != nil {
			t.Error("Unwrap should be nil")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewValidationError("storage.put", "key required")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
	t.Run("ConstructorWithCause", func(t *testing.T) {
		de := NewValidationCauseError("storage.put", "invalid", cause)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
		var ve *ValidationError
		if !errors.As(de.Err, &ve) {
			t.Fatal("expected ValidationError")
		}
		if ve.Cause != cause {
			t.Error("cause not set")
		}
	})
}

func TestCipherErrorStorage(t *testing.T) {
	cause := errors.New("bad key")
	t.Run("WithCause", func(t *testing.T) {
		e := &CipherError{Operation: "encrypt", Cause: cause}
		want := "encrypt: bad key"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("WithoutCause", func(t *testing.T) {
		e := &CipherError{Operation: "decrypt"}
		if got := e.Error(); got != "decrypt" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewCipherError("crypto.encrypt", "seal", dberrors.ErrEncryption, cause)
		if de.Code != dberrors.ErrEncryption {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestKeySizeError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &KeySizeError{Algorithm: "AES-256", Expected: 32, Actual: 16}
		want := "invalid key size for AES-256: expected 32 bytes, got 16 bytes"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewKeySizeError("crypto.init", "AES-128", 16, 8)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestKeyTooSmallError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &KeyTooSmallError{Algorithm: "ChaCha20", MinSize: 32, Actual: 16}
		want := "master key too small for ChaCha20: expected at least 32 bytes, got 16 bytes"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewKeyTooSmallError("crypto.init", "AES-256", 32, 24)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestFieldEncryptionError(t *testing.T) {
	cause := errors.New("cipher error")
	t.Run("Error", func(t *testing.T) {
		e := &FieldEncryptionError{FieldName: "password", Operation: "encrypt", Cause: cause}
		want := "failed to encrypt field 'password': cipher error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &FieldEncryptionError{FieldName: "x", Operation: "op", Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewFieldEncryptionError("storage.encrypt", "secret", "decrypt", dberrors.ErrDecryption, cause)
		if de.Code != dberrors.ErrDecryption {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestInvalidKeyEncodingError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &InvalidKeyEncodingError{KeyType: "base64", Detail: "illegal character"}
		want := "invalid base64 key: illegal character"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewInvalidKeyEncodingError("storage.decode", "hex", "odd length")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnknownKeyPrefixError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		var prefix byte = 0x5A // 'Z'
		e := &UnknownKeyPrefixError{Prefix: prefix}
		want := fmt.Sprintf("unknown key prefix: %c (0x%02x)", prefix, prefix)
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnknownKeyPrefixError("op", 0xAA)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestStorageSimpleFieldErrors(t *testing.T) {
	tests := []struct {
		name    string
		errFn   func() error
		wantMsg string
	}{
		{"UnknownCompareTarget", func() error { return &UnknownCompareTargetError{Target: 99} }, "unknown compare target: 99"},
		{"UnknownCompareResult", func() error { return &UnknownCompareResultError{Result: 42} }, "unknown compare result: 42"},
		{"UnsupportedValueType", func() error { return &UnsupportedValueTypeError{TypeName: "complex128"} }, "unsupported value type: complex128"},
		{"InvalidLength", func() error { return &InvalidLengthError{Field: "nonce", Expected: 12, Actual: 8} }, "invalid nonce length: expected 12, got 8"},
		{"RevisionBeforeCreation", func() error { return &RevisionBeforeCreationError{Revision: 5, CreateRevision: 10} }, "revision 5 is before key creation at 10"},
		{"UnknownOperationType", func() error { return &UnknownOperationTypeError{OpType: 77} }, "unknown operation type: 77"},
		{"ExpectedStruct", func() error { return &ExpectedStructError{Actual: "slice"} }, "expected struct, got slice"},
		{"ExpectedStructType", func() error { return &ExpectedStructTypeError{Actual: "map"} }, "expected struct type, got map"},
		{"InvalidUint64Encoding", func() error { return &InvalidUint64EncodingError{Expected: 8, Actual: 4} }, "invalid uint64 encoding: expected 8 bytes, got 4"},
		{"PointerRequired", func() error { return &PointerRequiredError{} }, "entity must be a pointer"},
		{"KeyDerivationNotImplemented", func() error { return &KeyDerivationNotImplementedError{} }, "key derivation not yet implemented"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}

func TestStorageConstructors(t *testing.T) {
	tests := []struct {
		name     string
		newFn    func() *dberrors.QRDBError
		wantCode dberrors.ErrorCode
	}{
		{"UnknownCompareTarget", func() *dberrors.QRDBError { return NewUnknownCompareTargetError("op", 3) }, dberrors.ErrInvalidInput},
		{"UnknownCompareResult", func() *dberrors.QRDBError { return NewUnknownCompareResultError("op", 4) }, dberrors.ErrInvalidInput},
		{"UnsupportedValueType", func() *dberrors.QRDBError { return NewUnsupportedValueTypeError("op", "func") }, dberrors.ErrInvalidInput},
		{"InvalidLength", func() *dberrors.QRDBError { return NewInvalidLengthError("op", "iv", 16, 12) }, dberrors.ErrInvalidInput},
		{"RevisionBeforeCreation", func() *dberrors.QRDBError { return NewRevisionBeforeCreationError("op", 1, 5) }, dberrors.ErrNotFound},
		{"UnknownOperationType", func() *dberrors.QRDBError { return NewUnknownOperationTypeError("op", 99) }, dberrors.ErrInvalidInput},
		{"ExpectedStruct", func() *dberrors.QRDBError { return NewExpectedStructError("op", "int") }, dberrors.ErrInvalidInput},
		{"ExpectedStructType", func() *dberrors.QRDBError { return NewExpectedStructTypeError("op", "slice") }, dberrors.ErrInvalidInput},
		{"InvalidUint64Encoding", func() *dberrors.QRDBError { return NewInvalidUint64EncodingError("op", 8, 2) }, dberrors.ErrInvalidInput},
		{"PointerRequired", func() *dberrors.QRDBError { return NewPointerRequiredError("op") }, dberrors.ErrInvalidInput},
		{"KeyDerivationNotImplemented", func() *dberrors.QRDBError { return NewKeyDerivationNotImplementedError("op") }, dberrors.ErrUnimplemented},
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

func TestInvalidEncryptionAlgorithmError(t *testing.T) {
	t.Run("WithSupported", func(t *testing.T) {
		e := &InvalidEncryptionAlgorithmError{Algorithm: "ROT13", FieldName: "data", Supported: []string{"AES-GCM", "ChaCha20"}}
		want := "invalid encryption algorithm 'ROT13' for field 'data': supported values are 'AES-GCM, ChaCha20'"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutSupported", func(t *testing.T) {
		e := &InvalidEncryptionAlgorithmError{Algorithm: "ROT13", FieldName: "data"}
		want := "invalid encryption algorithm 'ROT13' for field 'data'"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewInvalidEncryptionAlgorithmError("crypto", "XOR", "secret", []string{"AES"})
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestUnsupportedFieldTypeError(t *testing.T) {
	t.Run("WithType", func(t *testing.T) {
		e := &UnsupportedFieldTypeError{FieldName: "data", FieldType: "int"}
		want := "field 'data' has unsupported type 'int' for encryption: only string and []byte are supported"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutType", func(t *testing.T) {
		e := &UnsupportedFieldTypeError{FieldName: "data"}
		want := "field 'data' has unsupported type for encryption"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnsupportedFieldTypeError("encrypt", "status", "bool")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestStorageCauseErrors(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name     string
		errFn    func() error
		newFn    func() *dberrors.QRDBError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{
			"JSONMarshal",
			func() error { return &JSONMarshalError{Cause: cause} },
			func() *dberrors.QRDBError { return NewJSONMarshalError("storage.put", cause) },
			"json marshal failed: underlying",
			dberrors.ErrInternal,
		},
		{
			"JSONUnmarshal",
			func() error { return &JSONUnmarshalError{Cause: cause} },
			func() *dberrors.QRDBError { return NewJSONUnmarshalError("storage.get", cause) },
			"json unmarshal failed: underlying",
			dberrors.ErrInternal,
		},
		{
			"Base64Decode",
			func() error { return &Base64DecodeError{FieldName: "ciphertext", Cause: cause} },
			func() *dberrors.QRDBError { return NewBase64DecodeError("decrypt", "ciphertext", cause) },
			"failed to decode base64 for field 'ciphertext': underlying",
			dberrors.ErrDecryption,
		},
		{
			"InvalidBatchPoint",
			func() error { return &InvalidBatchPointError{Index: 7, Cause: cause} },
			func() *dberrors.QRDBError { return NewInvalidBatchPointError("batch", 7, cause) },
			"invalid point at index 7: underlying",
			dberrors.ErrInvalidInput,
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

func TestUnsupportedEncryptionAlgorithmError(t *testing.T) {
	t.Run("WithSupported", func(t *testing.T) {
		e := &UnsupportedEncryptionAlgorithmError{Algorithm: "DES", Supported: []string{"AES-GCM", "ChaCha20"}}
		want := "unsupported algorithm: DES (supported: AES-GCM, ChaCha20)"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutSupported", func(t *testing.T) {
		e := &UnsupportedEncryptionAlgorithmError{Algorithm: "DES"}
		want := "unsupported algorithm: DES"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewUnsupportedEncryptionAlgorithmError("crypto", "3DES", []string{"AES"})
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}
