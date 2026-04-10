// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAESGCMEncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, AESKeySize)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	plaintext := []byte("hello, world! this is a test of AES-GCM encryption.")

	ciphertext, err := AESGCMEncrypt(key, plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)
	assert.Len(t, ciphertext, len(plaintext)+AESGCMOverhead)

	decrypted, err := AESGCMDecrypt(key, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestAESGCMEncryptDecrypt_EmptyPlaintext(t *testing.T) {
	key := make([]byte, AESKeySize)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	ciphertext, err := AESGCMEncrypt(key, []byte{})
	require.NoError(t, err)
	assert.Len(t, ciphertext, AESGCMOverhead)

	decrypted, err := AESGCMDecrypt(key, ciphertext)
	require.NoError(t, err)
	assert.Empty(t, decrypted)
}

func TestAESGCMDecrypt_ShortCiphertext(t *testing.T) {
	key := make([]byte, AESKeySize)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	_, err = AESGCMDecrypt(key, []byte{1, 2, 3})
	require.Error(t, err)

	var ctErr *CiphertextTooShortError
	assert.ErrorAs(t, err, &ctErr)
	assert.Equal(t, "AESGCMDecrypt", ctErr.Op)
}

func TestAESGCMDecrypt_CorruptedData(t *testing.T) {
	key := make([]byte, AESKeySize)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	ciphertext, err := AESGCMEncrypt(key, []byte("test data"))
	require.NoError(t, err)

	// Corrupt the ciphertext.
	ciphertext[len(ciphertext)-1] ^= 0xFF

	_, err = AESGCMDecrypt(key, ciphertext)
	require.Error(t, err)

	var cryptoErr *CryptoError
	assert.ErrorAs(t, err, &cryptoErr)
}

func TestAESGCMDecrypt_WrongKey(t *testing.T) {
	key1 := make([]byte, AESKeySize)
	key2 := make([]byte, AESKeySize)
	_, err := io.ReadFull(rand.Reader, key1)
	require.NoError(t, err)
	_, err = io.ReadFull(rand.Reader, key2)
	require.NoError(t, err)

	ciphertext, err := AESGCMEncrypt(key1, []byte("secret"))
	require.NoError(t, err)

	_, err = AESGCMDecrypt(key2, ciphertext)
	require.Error(t, err)
}

func TestAESGCMEncrypt_InvalidKeySize(t *testing.T) {
	_, err := AESGCMEncrypt([]byte("short"), []byte("data"))
	require.Error(t, err)

	var cryptoErr *CryptoError
	assert.ErrorAs(t, err, &cryptoErr)
	assert.Equal(t, "AESGCMEncrypt", cryptoErr.Op)
}

func TestAESGCMDecrypt_InvalidKeySize(t *testing.T) {
	_, err := AESGCMDecrypt([]byte("short"), make([]byte, AESGCMOverhead+1))
	require.Error(t, err)

	var cryptoErr *CryptoError
	assert.ErrorAs(t, err, &cryptoErr)
}

func TestSecureMemZero(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0xFF}
	SecureMemZero(data)

	expected := make([]byte, len(data))
	assert.True(t, bytes.Equal(expected, data))
}

func TestSecureMemZero_EmptySlice(t *testing.T) {
	data := []byte{}
	SecureMemZero(data) // Should not panic.
	assert.Empty(t, data)
}

func TestAESGCMOverhead_Value(t *testing.T) {
	// 12-byte nonce + 16-byte tag = 28.
	assert.Equal(t, 28, AESGCMOverhead)
}

func TestAESGCMEncrypt_ProducesDifferentCiphertexts(t *testing.T) {
	key := make([]byte, AESKeySize)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	plaintext := []byte("same plaintext")

	ct1, err := AESGCMEncrypt(key, plaintext)
	require.NoError(t, err)

	ct2, err := AESGCMEncrypt(key, plaintext)
	require.NoError(t, err)

	// Random nonces mean different ciphertexts.
	assert.NotEqual(t, ct1, ct2)
}

func BenchmarkAESGCMEncrypt(b *testing.B) {
	key := make([]byte, AESKeySize)
	_, _ = io.ReadFull(rand.Reader, key)
	plaintext := make([]byte, 256)
	_, _ = io.ReadFull(rand.Reader, plaintext)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = AESGCMEncrypt(key, plaintext)
	}
}

func BenchmarkAESGCMDecrypt(b *testing.B) {
	key := make([]byte, AESKeySize)
	_, _ = io.ReadFull(rand.Reader, key)
	plaintext := make([]byte, 256)
	_, _ = io.ReadFull(rand.Reader, plaintext)

	ciphertext, _ := AESGCMEncrypt(key, plaintext)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = AESGCMDecrypt(key, ciphertext)
	}
}
