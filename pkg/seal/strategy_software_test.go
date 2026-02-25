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
	"context"
	"testing"
)

// ─── SoftwareStrategy identity ──────────────────────────────────────

func TestSoftwareStrategy_ID(t *testing.T) {
	s := NewSoftwareStrategy()
	if s.ID() != StrategySoftware {
		t.Errorf("expected %s, got %s", StrategySoftware, s.ID())
	}
}

func TestSoftwareStrategy_Available(t *testing.T) {
	s := NewSoftwareStrategy()
	if !s.Available() {
		t.Error("expected Available() = true for software strategy")
	}
}

func TestSoftwareStrategy_HardwareBacked(t *testing.T) {
	s := NewSoftwareStrategy()
	if s.HardwareBacked() {
		t.Error("expected HardwareBacked() = false for software strategy")
	}
}

// ─── SealRootKey / UnsealRootKey ────────────────────────────────────

func TestSoftwareStrategy_SealUnseal_RoundTrip(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()
	creds := Credentials{Secret: "test-password-2026"}

	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i + 42)
	}

	sealed, err := s.SealRootKey(ctx, rootKey, creds)
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	if sealed.Version != sealedRootKeyVersion {
		t.Errorf("expected version %d, got %d", sealedRootKeyVersion, sealed.Version)
	}
	if sealed.Strategy != StrategySoftware {
		t.Errorf("expected strategy %s, got %s", StrategySoftware, sealed.Strategy)
	}
	if sealed.KDFVersion != 1 {
		t.Errorf("expected KDF version 1, got %d", sealed.KDFVersion)
	}
	if len(sealed.Salt) != argon2SaltLen {
		t.Errorf("expected salt length %d, got %d", argon2SaltLen, len(sealed.Salt))
	}
	if len(sealed.Nonce) != gcmNonceSize {
		t.Errorf("expected nonce length %d, got %d", gcmNonceSize, len(sealed.Nonce))
	}
	if sealed.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}

	unsealedKey, err := s.UnsealRootKey(ctx, sealed, creds)
	if err != nil {
		t.Fatalf("UnsealRootKey: %v", err)
	}

	if !bytes.Equal(unsealedKey, rootKey) {
		t.Error("unsealed root key does not match original")
	}
}

func TestSoftwareStrategy_SealProducesUniqueSalts(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()
	creds := Credentials{Secret: "same-password"}
	rootKey := make([]byte, aesKeySize)

	sealed1, err := s.SealRootKey(ctx, rootKey, creds)
	if err != nil {
		t.Fatalf("SealRootKey 1: %v", err)
	}
	sealed2, err := s.SealRootKey(ctx, rootKey, creds)
	if err != nil {
		t.Fatalf("SealRootKey 2: %v", err)
	}

	if bytes.Equal(sealed1.Salt, sealed2.Salt) {
		t.Error("expected unique salts for each seal operation")
	}
	if bytes.Equal(sealed1.Ciphertext, sealed2.Ciphertext) {
		t.Error("expected different ciphertexts due to different salts/nonces")
	}
}

func TestSoftwareStrategy_UnsealRootKey_WrongPassword(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()
	correctCreds := Credentials{Secret: "correct-password"}
	wrongCreds := Credentials{Secret: "wrong-password"}

	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}

	sealed, err := s.SealRootKey(ctx, rootKey, correctCreds)
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	_, err = s.UnsealRootKey(ctx, sealed, wrongCreds)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestSoftwareStrategy_UnsealRootKey_NilSealed(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()
	creds := Credentials{Secret: "pw"}

	_, err := s.UnsealRootKey(ctx, nil, creds)
	if err != ErrNilSealedData {
		t.Fatalf("expected ErrNilSealedData, got %v", err)
	}
}

func TestSoftwareStrategy_UnsealRootKey_TamperedCiphertext(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()
	creds := Credentials{Secret: "password"}

	rootKey := make([]byte, aesKeySize)
	sealed, err := s.SealRootKey(ctx, rootKey, creds)
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Tamper with ciphertext.
	if len(sealed.Ciphertext) > 0 {
		sealed.Ciphertext[0] ^= 0xFF
	}

	_, err = s.UnsealRootKey(ctx, sealed, creds)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for tampered ciphertext, got %v", err)
	}
}

func TestSoftwareStrategy_UnsealRootKey_TamperedSalt(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()
	creds := Credentials{Secret: "password"}

	rootKey := make([]byte, aesKeySize)
	sealed, err := s.SealRootKey(ctx, rootKey, creds)
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Tamper with the salt. This causes a different derived key, so decryption
	// should fail with ErrInvalidCredentials.
	sealed.Salt[0] ^= 0xFF

	_, err = s.UnsealRootKey(ctx, sealed, creds)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for tampered salt, got %v", err)
	}
}

// ─── BarrierEncryptor ───────────────────────────────────────────────

func TestSoftwareStrategy_BarrierEncryptor_Success(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()

	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}

	enc, err := s.BarrierEncryptor(ctx, rootKey)
	if err != nil {
		t.Fatalf("BarrierEncryptor: %v", err)
	}
	if enc == nil {
		t.Fatal("expected non-nil encryptor")
	}

	// Verify the encryptor works.
	plaintext := []byte("encryptor test data")
	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestSoftwareStrategy_BarrierEncryptor_DeterministicDEK(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()

	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i + 10)
	}

	enc1, err := s.BarrierEncryptor(ctx, rootKey)
	if err != nil {
		t.Fatalf("BarrierEncryptor 1: %v", err)
	}
	enc2, err := s.BarrierEncryptor(ctx, rootKey)
	if err != nil {
		t.Fatalf("BarrierEncryptor 2: %v", err)
	}

	// Same root key should produce same DEK (HKDF is deterministic).
	sw1, ok := enc1.(*softwareBarrierEncryptor)
	if !ok {
		t.Fatal("expected softwareBarrierEncryptor")
	}
	sw2, ok := enc2.(*softwareBarrierEncryptor)
	if !ok {
		t.Fatal("expected softwareBarrierEncryptor")
	}

	if !bytes.Equal(sw1.DEK(), sw2.DEK()) {
		t.Error("expected same DEK from same root key")
	}
}

func TestSoftwareStrategy_BarrierEncryptor_CrossCompatible(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()
	rootKey := make([]byte, aesKeySize)

	// Get two encryptors from the same root key.
	enc1, err := s.BarrierEncryptor(ctx, rootKey)
	if err != nil {
		t.Fatalf("BarrierEncryptor 1: %v", err)
	}
	enc2, err := s.BarrierEncryptor(ctx, rootKey)
	if err != nil {
		t.Fatalf("BarrierEncryptor 2: %v", err)
	}

	// Data encrypted with enc1 should be decryptable with enc2.
	plaintext := []byte("cross-compatible data")
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

func TestSoftwareStrategy_BarrierEncryptor_DifferentRootKeysFail(t *testing.T) {
	s := NewSoftwareStrategy()
	ctx := context.Background()

	rootKey1 := make([]byte, aesKeySize)
	rootKey2 := make([]byte, aesKeySize)
	rootKey2[0] = 0xFF

	enc1, err := s.BarrierEncryptor(ctx, rootKey1)
	if err != nil {
		t.Fatalf("BarrierEncryptor 1: %v", err)
	}
	enc2, err := s.BarrierEncryptor(ctx, rootKey2)
	if err != nil {
		t.Fatalf("BarrierEncryptor 2: %v", err)
	}

	plaintext := []byte("cannot cross decrypt")
	ciphertext, err := enc1.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("enc1.Encrypt: %v", err)
	}

	_, err = enc2.Decrypt(ciphertext)
	if err == nil {
		t.Fatal("expected decryption failure with different root key")
	}
}
