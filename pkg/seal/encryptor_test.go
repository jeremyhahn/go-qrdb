// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"testing"
)

// ─── softwareBarrierEncryptor ───────────────────────────────────────

func TestEncryptor_EncryptDecrypt_RoundTrip(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i + 1)
	}

	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	tests := []struct {
		name      string
		plaintext []byte
	}{
		{"short_message", []byte("hello")},
		{"empty_plaintext", []byte{}},
		{"binary_data", []byte{0x00, 0xFF, 0x01, 0xFE}},
		{"long_message", bytes.Repeat([]byte("A"), 1024)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ciphertext, err := enc.Encrypt(tt.plaintext)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}

			decrypted, err := enc.Decrypt(ciphertext)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}

			if !bytes.Equal(decrypted, tt.plaintext) {
				t.Errorf("expected %v, got %v", tt.plaintext, decrypted)
			}
		})
	}
}

func TestEncryptor_Decrypt_TamperedCiphertext(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}

	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	ciphertext, err := enc.Encrypt([]byte("secret data"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Tamper with the ciphertext (flip last byte).
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0xFF

	_, err = enc.Decrypt(tampered)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for tampered ciphertext, got %v", err)
	}
}

func TestEncryptor_Decrypt_TruncatedCiphertext(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	// Ciphertext shorter than nonce + overhead.
	truncated := make([]byte, gcmNonceSize-1)
	_, err = enc.Decrypt(truncated)
	if err == nil {
		t.Fatal("expected error for truncated ciphertext")
	}
}

func TestEncryptor_Decrypt_EmptyCiphertext(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	_, err = enc.Decrypt([]byte{})
	if err == nil {
		t.Fatal("expected error for empty ciphertext")
	}
}

func TestEncryptor_DEK_ReturnsCopy(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}

	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	dek1 := enc.DEK()
	dek2 := enc.DEK()

	// Both should be equal.
	if !bytes.Equal(dek1, dek2) {
		t.Error("expected equal DEKs from consecutive calls")
	}

	// Modifying returned slice should not affect internal DEK.
	dek1[0] ^= 0xFF
	dek3 := enc.DEK()
	if bytes.Equal(dek1, dek3) {
		t.Error("modifying returned DEK should not affect internal state")
	}
}

func TestEncryptor_DEK_Length(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	dek := enc.DEK()
	if len(dek) != aesKeySize {
		t.Fatalf("expected DEK length %d, got %d", aesKeySize, len(dek))
	}
}

func TestEncryptor_Zero_WipesDEK(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i + 1)
	}

	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	// Verify DEK is non-zero before wipe.
	dekBefore := enc.DEK()
	allZero := true
	for _, b := range dekBefore {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("expected non-zero DEK before wipe")
	}

	enc.Zero()

	// Verify internal DEK is zeroed.
	allZero = true
	for _, b := range enc.dek {
		if b != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		t.Error("expected DEK to be zeroed after Zero()")
	}
}

func TestEncryptor_EncryptProducesDifferentCiphertexts(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}

	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	plaintext := []byte("same data")
	ct1, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	ct2, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}

	// Due to random nonces, ciphertexts should differ.
	if bytes.Equal(ct1, ct2) {
		t.Error("expected different ciphertexts for same plaintext (random nonce)")
	}
}

// ─── Encrypt/Decrypt with invalid DEK ───────────────────────────────

func TestEncryptor_Encrypt_InvalidDEKSize(t *testing.T) {
	// A DEK of 15 bytes is not a valid AES key size, causing aes.NewCipher
	// to return an error. This covers encryptor.go line 74-75.
	enc := &softwareBarrierEncryptor{dek: make([]byte, 15)}
	_, err := enc.Encrypt([]byte("data"))
	if err == nil {
		t.Fatal("expected error from invalid DEK size")
	}
}

func TestEncryptor_Decrypt_InvalidDEKSize(t *testing.T) {
	// Same as above but for the Decrypt path. Covers encryptor.go line 95-96.
	enc := &softwareBarrierEncryptor{dek: make([]byte, 15)}
	// Provide enough bytes to pass the length check.
	fakeData := make([]byte, 64)
	_, err := enc.Decrypt(fakeData)
	if err == nil {
		t.Fatal("expected error from invalid DEK size during decrypt")
	}
}

func TestEncryptor_Encrypt_ZeroDEK(t *testing.T) {
	// A zero-length DEK is also invalid for AES.
	enc := &softwareBarrierEncryptor{dek: []byte{}}
	_, err := enc.Encrypt([]byte("data"))
	if err == nil {
		t.Fatal("expected error from empty DEK")
	}
}

func TestEncryptor_Decrypt_ZeroDEK(t *testing.T) {
	enc := &softwareBarrierEncryptor{dek: []byte{}}
	_, err := enc.Decrypt(make([]byte, 64))
	if err == nil {
		t.Fatal("expected error from empty DEK during decrypt")
	}
}

// ─── deriveDEK ──────────────────────────────────────────────────────

func TestDeriveDEK_ProducesCorrectLength(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	dek, err := deriveDEK(rootKey)
	if err != nil {
		t.Fatalf("deriveDEK: %v", err)
	}
	if len(dek) != aesKeySize {
		t.Fatalf("expected DEK length %d, got %d", aesKeySize, len(dek))
	}
}

func TestDeriveDEK_DifferentRootKeys(t *testing.T) {
	rootKey1 := make([]byte, aesKeySize)
	rootKey2 := make([]byte, aesKeySize)
	for i := range rootKey2 {
		rootKey2[i] = byte(i + 1)
	}

	dek1, err := deriveDEK(rootKey1)
	if err != nil {
		t.Fatalf("deriveDEK 1: %v", err)
	}
	dek2, err := deriveDEK(rootKey2)
	if err != nil {
		t.Fatalf("deriveDEK 2: %v", err)
	}

	if bytes.Equal(dek1, dek2) {
		t.Error("expected different DEKs for different root keys")
	}
}

func TestDeriveDEK_Deterministic(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}

	dek1, err := deriveDEK(rootKey)
	if err != nil {
		t.Fatalf("deriveDEK 1: %v", err)
	}
	dek2, err := deriveDEK(rootKey)
	if err != nil {
		t.Fatalf("deriveDEK 2: %v", err)
	}

	if !bytes.Equal(dek1, dek2) {
		t.Error("expected deterministic DEK derivation from same root key")
	}
}

func TestDeriveDEK_VariousRootKeySizes(t *testing.T) {
	// HKDF accepts any key size. Verify DEK is always 32 bytes.
	for _, size := range []int{1, 16, 32, 64, 128} {
		rootKey := make([]byte, size)
		for i := range rootKey {
			rootKey[i] = byte(i + 1)
		}
		dek, err := deriveDEK(rootKey)
		if err != nil {
			t.Fatalf("deriveDEK (key size %d): %v", size, err)
		}
		if len(dek) != aesKeySize {
			t.Errorf("key size %d: expected DEK length %d, got %d", size, aesKeySize, len(dek))
		}
	}
}

// ─── newSoftwareBarrierEncryptorFromDEK ─────────────────────────────

func TestNewSoftwareBarrierEncryptorFromDEK_RoundTrip(t *testing.T) {
	// Create an encryptor from a root key, get its DEK, then create another
	// encryptor from that DEK and verify they produce compatible results.
	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}

	enc1, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	dek := enc1.DEK()
	enc2 := newSoftwareBarrierEncryptorFromDEK(dek)

	// Encrypt with enc1, decrypt with enc2.
	plaintext := []byte("cross-encryptor test")
	ciphertext, err := enc1.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("enc1.Encrypt: %v", err)
	}

	decrypted, err := enc2.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("enc2.Decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestNewSoftwareBarrierEncryptorFromDEK_Isolation(t *testing.T) {
	dek := make([]byte, aesKeySize)
	for i := range dek {
		dek[i] = byte(i + 10)
	}

	enc := newSoftwareBarrierEncryptorFromDEK(dek)

	// Modify original DEK slice.
	dek[0] = 0xFF

	// Internal DEK should be unaffected.
	internalDEK := enc.DEK()
	if internalDEK[0] == 0xFF {
		t.Error("expected encryptor DEK to be isolated from input slice")
	}
}
