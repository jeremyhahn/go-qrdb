// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNilInputError(t *testing.T) {
	err := &NilInputError{Op: "Create", Field: "config"}
	assert.Contains(t, err.Error(), "Create")
	assert.Contains(t, err.Error(), "config")
	assert.Contains(t, err.Error(), "must not be nil")
}

func TestUnsupportedAlgorithmError(t *testing.T) {
	err := &UnsupportedAlgorithmError{Op: "Init", Algorithm: "DES"}
	assert.Contains(t, err.Error(), "Init")
	assert.Contains(t, err.Error(), "DES")
	assert.Contains(t, err.Error(), "unsupported algorithm")
}

func TestSealedError(t *testing.T) {
	err := &SealedError{Op: "Encrypt"}
	assert.Contains(t, err.Error(), "Encrypt")
	assert.Contains(t, err.Error(), "sealed")
}

func TestCryptoError(t *testing.T) {
	t.Run("with wrapped error", func(t *testing.T) {
		inner := errors.New("aes failure")
		err := &CryptoError{Op: "Encrypt", Detail: "block cipher", Err: inner}
		assert.Contains(t, err.Error(), "Encrypt")
		assert.Contains(t, err.Error(), "block cipher")
		assert.Contains(t, err.Error(), "aes failure")
		assert.Equal(t, inner, err.Unwrap())
	})

	t.Run("without wrapped error", func(t *testing.T) {
		err := &CryptoError{Op: "Encrypt", Detail: "block cipher"}
		assert.Contains(t, err.Error(), "block cipher")
		assert.Nil(t, err.Unwrap())
	})
}

func TestAlreadyInProgressError(t *testing.T) {
	err := &AlreadyInProgressError{Op: "Start", Resource: "rekey"}
	assert.Contains(t, err.Error(), "Start")
	assert.Contains(t, err.Error(), "rekey")
	assert.Contains(t, err.Error(), "already in progress")
}

func TestNoActiveOperationError(t *testing.T) {
	err := &NoActiveOperationError{Op: "Cancel", Resource: "rekey"}
	assert.Contains(t, err.Error(), "Cancel")
	assert.Contains(t, err.Error(), "no active")
}

func TestNotImplementedError(t *testing.T) {
	err := &NotImplementedError{Message: "recovery"}
	assert.Contains(t, err.Error(), "not implemented")
	assert.Contains(t, err.Error(), "recovery")
}

func TestManagerClosedError(t *testing.T) {
	err := &ManagerClosedError{Op: "Get", Resource: "manager"}
	assert.Contains(t, err.Error(), "Get")
	assert.Contains(t, err.Error(), "manager")
	assert.Contains(t, err.Error(), "closed")
}

func TestNotFoundError(t *testing.T) {
	err := &NotFoundError{Resource: "tenant DEK", ID: "abc"}
	assert.Contains(t, err.Error(), "tenant DEK")
	assert.Contains(t, err.Error(), "abc")
	assert.Contains(t, err.Error(), "not found")
}

func TestDecodeError(t *testing.T) {
	inner := errors.New("bad format")
	err := &DecodeError{Op: "Load", Resource: "entry", Err: inner}
	assert.Contains(t, err.Error(), "Load")
	assert.Contains(t, err.Error(), "decode")
	assert.Contains(t, err.Error(), "bad format")
	assert.Equal(t, inner, err.Unwrap())
}

func TestEncodeError(t *testing.T) {
	inner := errors.New("marshal failure")
	err := &EncodeError{Op: "Save", Resource: "entry", Err: inner}
	assert.Contains(t, err.Error(), "Save")
	assert.Contains(t, err.Error(), "encode")
	assert.Contains(t, err.Error(), "marshal failure")
	assert.Equal(t, inner, err.Unwrap())
}

func TestStorageOperationError(t *testing.T) {
	inner := errors.New("disk full")
	err := &StorageOperationError{Op: "Put", Detail: "write data", Err: inner}
	assert.Contains(t, err.Error(), "Put")
	assert.Contains(t, err.Error(), "write data")
	assert.Contains(t, err.Error(), "disk full")
	assert.Equal(t, inner, err.Unwrap())
}

func TestCiphertextTooShortError(t *testing.T) {
	err := &CiphertextTooShortError{Op: "Decrypt", Detail: "less than nonce"}
	assert.Contains(t, err.Error(), "Decrypt")
	assert.Contains(t, err.Error(), "too short")
}

func TestDataTruncatedError(t *testing.T) {
	t.Run("with field", func(t *testing.T) {
		err := &DataTruncatedError{Field: "algorithm", Message: "length exceeds data"}
		assert.Contains(t, err.Error(), "algorithm")
		assert.Contains(t, err.Error(), "length exceeds data")
	})

	t.Run("without field", func(t *testing.T) {
		err := &DataTruncatedError{Message: "too short"}
		assert.Contains(t, err.Error(), "data truncated")
		assert.Contains(t, err.Error(), "too short")
	})
}

func TestRequiredFieldError(t *testing.T) {
	err := &RequiredFieldError{Field: "tenant ID"}
	assert.Contains(t, err.Error(), "required field missing")
	assert.Contains(t, err.Error(), "tenant ID")
}

func TestInvalidInputError(t *testing.T) {
	t.Run("with wrapped error", func(t *testing.T) {
		inner := &RequiredFieldError{Field: "name"}
		err := &InvalidInputError{Op: "Create", Err: inner}
		assert.Contains(t, err.Error(), "Create")
		assert.Contains(t, err.Error(), "invalid input")
		assert.Contains(t, err.Error(), "name")
		assert.Equal(t, inner, err.Unwrap())
	})

	t.Run("without wrapped error", func(t *testing.T) {
		err := &InvalidInputError{Op: "Create"}
		assert.Contains(t, err.Error(), "invalid input")
		assert.Nil(t, err.Unwrap())
	})
}

func TestIsNotFound(t *testing.T) {
	t.Run("is not found", func(t *testing.T) {
		err := &NotFoundError{Resource: "key", ID: "abc"}
		assert.True(t, IsNotFound(err))
	})

	t.Run("is not a not found error", func(t *testing.T) {
		err := &SealedError{Op: "test"}
		assert.False(t, IsNotFound(err))
	})

	t.Run("nil error", func(t *testing.T) {
		assert.False(t, IsNotFound(nil))
	})
}
