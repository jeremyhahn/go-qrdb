// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package aesgcm

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

// failReader is an io.Reader that always returns an error.
type failReader struct{}

func (failReader) Read([]byte) (int, error) {
	return 0, &CipherError{Op: "test", Err: io.ErrUnexpectedEOF}
}

// withFailReader swaps the package randReader with a failing reader for
// the duration of the callback, then restores the original.
func withFailReader(fn func()) {
	orig := randReader
	randReader = failReader{}
	defer func() { randReader = orig }()
	fn()
}

// withFailGCM swaps the package newGCM with a function that always returns
// an error, then restores the original after the callback.
func withFailGCM(fn func()) {
	orig := newGCM
	newGCM = func([]byte) (cipher.AEAD, error) {
		return nil, &CipherError{Op: "create cipher", Err: io.ErrUnexpectedEOF}
	}
	defer func() { newGCM = orig }()
	fn()
}

func TestEncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	plaintext := []byte("the quick brown fox jumps over the lazy dog")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Ciphertext must be longer than plaintext (nonce + tag overhead).
	if len(ciphertext) <= len(plaintext) {
		t.Fatalf("ciphertext length %d should exceed plaintext length %d",
			len(ciphertext), len(plaintext))
	}

	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptInvalidKeyLength(t *testing.T) {
	tests := []struct {
		name   string
		keyLen int
	}{
		{name: "TooShort_16", keyLen: 16},
		{name: "TooLong_64", keyLen: 64},
		{name: "Empty", keyLen: 0},
		{name: "OffByOne_31", keyLen: 31},
		{name: "OffByOne_33", keyLen: 33},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := make([]byte, tt.keyLen)
			_, err := Encrypt(key, []byte("test"))
			if err == nil {
				t.Fatal("expected error for key length", tt.keyLen)
			}

			var keyErr *InvalidKeyLengthError
			if !errors.As(err, &keyErr) {
				t.Fatalf("expected *InvalidKeyLengthError, got %T: %v", err, err)
			}
			if keyErr.Expected != 32 {
				t.Fatalf("expected Expected=32, got %d", keyErr.Expected)
			}
			if keyErr.Actual != tt.keyLen {
				t.Fatalf("expected Actual=%d, got %d", tt.keyLen, keyErr.Actual)
			}
		})
	}
}

// TestEncryptNonceIsModuleOwned asserts that Encrypt no longer draws the nonce
// from randReader: NewGCMWithRandomNonce samples it inside the FIPS module, so
// a failing package reader cannot affect Encrypt. This replaces the former
// TestEncryptRandReaderFailure, whose "generate nonce" error path no longer
// exists.
func TestEncryptNonceIsModuleOwned(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	var ciphertext []byte
	withFailReader(func() {
		var err error
		ciphertext, err = Encrypt(key, []byte("test"))
		if err != nil {
			t.Fatalf("Encrypt must not depend on randReader: %v", err)
		}
	})

	plaintext, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(plaintext) != "test" {
		t.Fatalf("round trip mismatch: got %q", plaintext)
	}
}

// TestEncryptNoncesAreUnique asserts the module-owned nonce actually varies,
// which is the property NewGCMWithRandomNonce exists to guarantee.
func TestEncryptNoncesAreUnique(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	first, err := Encrypt(key, []byte("test"))
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	second, err := Encrypt(key, []byte("test"))
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}
	if bytes.Equal(first[:12], second[:12]) {
		t.Fatal("two encryptions reused the same nonce")
	}
}

func TestEncryptGCMFailure(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	withFailGCM(func() {
		_, err := Encrypt(key, []byte("test"))
		if err == nil {
			t.Fatal("expected error when GCM creation fails")
		}

		var cipherErr *CipherError
		if !errors.As(err, &cipherErr) {
			t.Fatalf("expected *CipherError, got %T: %v", err, err)
		}
	})
}

func TestDecryptInvalidKeyLength(t *testing.T) {
	tests := []struct {
		name   string
		keyLen int
	}{
		{name: "TooShort_16", keyLen: 16},
		{name: "TooLong_64", keyLen: 64},
		{name: "Empty", keyLen: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := make([]byte, tt.keyLen)
			_, err := Decrypt(key, []byte("some ciphertext placeholder data"))
			if err == nil {
				t.Fatal("expected error for key length", tt.keyLen)
			}

			var keyErr *InvalidKeyLengthError
			if !errors.As(err, &keyErr) {
				t.Fatalf("expected *InvalidKeyLengthError, got %T: %v", err, err)
			}
			if keyErr.Actual != tt.keyLen {
				t.Fatalf("expected Actual=%d, got %d", tt.keyLen, keyErr.Actual)
			}
		})
	}
}

func TestDecryptGCMFailure(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	withFailGCM(func() {
		_, err := Decrypt(key, make([]byte, 64))
		if err == nil {
			t.Fatal("expected error when GCM creation fails")
		}

		var cipherErr *CipherError
		if !errors.As(err, &cipherErr) {
			t.Fatalf("expected *CipherError, got %T: %v", err, err)
		}
	})
}

func TestDecryptTruncatedData(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "Empty", data: []byte{}},
		{name: "OneByte", data: []byte{0x01}},
		{name: "ElevenBytes", data: make([]byte, 11)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decrypt(key, tt.data)
			if err == nil {
				t.Fatal("expected error for truncated data")
			}

			var cipherErr *CipherError
			if !errors.As(err, &cipherErr) {
				t.Fatalf("expected *CipherError, got %T: %v", err, err)
			}

			var tooShort *DataTooShortError
			if !errors.As(err, &tooShort) {
				t.Fatalf("expected wrapped *DataTooShortError, got %T: %v", cipherErr.Err, err)
			}

			if tooShort.ActualSize != len(tt.data) {
				t.Fatalf("expected ActualSize=%d, got %d", len(tt.data), tooShort.ActualSize)
			}
		})
	}
}

func TestDecryptCorruptedData(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	plaintext := []byte("sensitive data")
	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	t.Run("FlippedBit", func(t *testing.T) {
		corrupted := make([]byte, len(ciphertext))
		copy(corrupted, ciphertext)
		// Flip a bit in the ciphertext portion (after the nonce).
		corrupted[len(corrupted)-1] ^= 0xFF

		_, err := Decrypt(key, corrupted)
		if err == nil {
			t.Fatal("expected error for corrupted ciphertext")
		}

		var decErr *DecryptionError
		if !errors.As(err, &decErr) {
			t.Fatalf("expected *DecryptionError, got %T: %v", err, err)
		}
	})

	t.Run("WrongKey", func(t *testing.T) {
		wrongKey := make([]byte, 32)
		if _, err := rand.Read(wrongKey); err != nil {
			t.Fatalf("rand.Read: %v", err)
		}

		_, err := Decrypt(wrongKey, ciphertext)
		if err == nil {
			t.Fatal("expected error for wrong key")
		}

		var decErr *DecryptionError
		if !errors.As(err, &decErr) {
			t.Fatalf("expected *DecryptionError, got %T: %v", err, err)
		}
	})
}

func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	if len(key) != 32 {
		t.Fatalf("expected 32-byte key, got %d bytes", len(key))
	}

	// Verify key is not all zeros (astronomically unlikely but sanity check).
	allZero := true
	for _, b := range key {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("generated key is all zeros")
	}
}

func TestGenerateKeyUniqueness(t *testing.T) {
	key1, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey 1: %v", err)
	}

	key2, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey 2: %v", err)
	}

	if bytes.Equal(key1, key2) {
		t.Fatal("two generated keys should not be identical")
	}
}

func TestGenerateKeyRandReaderFailure(t *testing.T) {
	withFailReader(func() {
		_, err := GenerateKey()
		if err == nil {
			t.Fatal("expected error when rand reader fails")
		}

		var cipherErr *CipherError
		if !errors.As(err, &cipherErr) {
			t.Fatalf("expected *CipherError, got %T: %v", err, err)
		}
		if cipherErr.Op != "generate key" {
			t.Fatalf("expected Op=%q, got %q", "generate key", cipherErr.Op)
		}
	})
}

func TestEncryptDecryptEmptyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	ciphertext, err := Encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("Encrypt empty plaintext: %v", err)
	}

	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt empty plaintext: %v", err)
	}

	if len(decrypted) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(decrypted))
	}
}

func TestDefaultNewGCMCipherError(t *testing.T) {
	// Pass an invalid key length directly to defaultNewGCM to exercise the
	// aes.NewCipher error path inside the helper.
	_, err := defaultNewGCM([]byte("short"))
	if err == nil {
		t.Fatal("expected error for invalid key in defaultNewGCM")
	}

	var cipherErr *CipherError
	if !errors.As(err, &cipherErr) {
		t.Fatalf("expected *CipherError, got %T: %v", err, err)
	}
	if cipherErr.Op != "create cipher" {
		t.Fatalf("expected Op=%q, got %q", "create cipher", cipherErr.Op)
	}
}

func TestErrorMessages(t *testing.T) {
	t.Run("InvalidKeyLengthError", func(t *testing.T) {
		e := &InvalidKeyLengthError{Expected: 32, Actual: 16}
		msg := e.Error()
		if msg == "" {
			t.Fatal("error message should not be empty")
		}
	})

	t.Run("CipherError", func(t *testing.T) {
		inner := &InvalidKeyLengthError{Expected: 32, Actual: 0}
		e := &CipherError{Op: "test op", Err: inner}
		msg := e.Error()
		if msg == "" {
			t.Fatal("error message should not be empty")
		}
		if e.Unwrap() != inner {
			t.Fatal("Unwrap should return the inner error")
		}
	})

	t.Run("DecryptionError", func(t *testing.T) {
		inner := &DataTooShortError{MinSize: 12, ActualSize: 0}
		e := &DecryptionError{Err: inner}
		msg := e.Error()
		if msg == "" {
			t.Fatal("error message should not be empty")
		}
		if e.Unwrap() != inner {
			t.Fatal("Unwrap should return the inner error")
		}
	})

	t.Run("DataTooShortError", func(t *testing.T) {
		e := &DataTooShortError{MinSize: 12, ActualSize: 5}
		msg := e.Error()
		if msg == "" {
			t.Fatal("error message should not be empty")
		}
	})
}
