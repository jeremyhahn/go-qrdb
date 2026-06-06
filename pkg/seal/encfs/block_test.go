// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func freshBlockCrypto(t *testing.T) *blockCrypto {
	t.Helper()
	var dek [dekSize]byte
	if _, err := rand.Read(dek[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	c, err := newBlockCrypto(dek[:])
	if err != nil {
		t.Fatalf("newBlockCrypto: %v", err)
	}
	return c
}

func TestBlockCrypto_NewRequires32ByteDEK(t *testing.T) {
	for _, n := range []int{0, 1, 16, 31, 33, 64} {
		if _, err := newBlockCrypto(make([]byte, n)); !errors.Is(err, ErrDEKWrongLength) {
			t.Errorf("len=%d error = %v, want ErrDEKWrongLength", n, err)
		}
	}
}

func TestBlockCrypto_RoundTripFullBlock(t *testing.T) {
	c := freshBlockCrypto(t)
	pt := bytes.Repeat([]byte("A"), BlockSize)
	ct, err := c.encrypt(pt)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(ct) != BlockSize+BlockOverhead {
		t.Errorf("ciphertext len = %d, want %d", len(ct), BlockSize+BlockOverhead)
	}
	got, err := c.decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Error("plaintext mismatch after round trip")
	}
}

func TestBlockCrypto_RoundTripShortBlock(t *testing.T) {
	c := freshBlockCrypto(t)
	pt := []byte("hello, encfs world")
	ct, err := c.encrypt(pt)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(ct) != len(pt)+BlockOverhead {
		t.Errorf("ciphertext len = %d, want %d", len(ct), len(pt)+BlockOverhead)
	}
	got, err := c.decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Error("plaintext mismatch after round trip")
	}
}

func TestBlockCrypto_RoundTripEmptyBlock(t *testing.T) {
	c := freshBlockCrypto(t)
	ct, err := c.encrypt(nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(ct) != BlockOverhead {
		t.Errorf("empty ciphertext len = %d, want %d", len(ct), BlockOverhead)
	}
	got, err := c.decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("plaintext len = %d, want 0", len(got))
	}
}

func TestBlockCrypto_OversizedPlaintext(t *testing.T) {
	c := freshBlockCrypto(t)
	if _, err := c.encrypt(make([]byte, BlockSize+1)); !errors.Is(err, ErrBlockTooLarge) {
		t.Errorf("error = %v, want ErrBlockTooLarge", err)
	}
}

func TestBlockCrypto_TooShortCiphertext(t *testing.T) {
	c := freshBlockCrypto(t)
	if _, err := c.decrypt(make([]byte, BlockOverhead-1)); !errors.Is(err, ErrBlockTooShort) {
		t.Errorf("error = %v, want ErrBlockTooShort", err)
	}
}

func TestBlockCrypto_TooLongCiphertext(t *testing.T) {
	c := freshBlockCrypto(t)
	if _, err := c.decrypt(make([]byte, CiphertextBlockSize+1)); !errors.Is(err, ErrBlockTooShort) {
		t.Errorf("error = %v, want ErrBlockTooShort", err)
	}
}

func TestBlockCrypto_TamperFails(t *testing.T) {
	c := freshBlockCrypto(t)
	ct, err := c.encrypt([]byte("payload"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Flip a bit in the ciphertext body (skip the inline nonce).
	ct[inlineNonceSize] ^= 0x01
	if _, err := c.decrypt(ct); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("error = %v, want ErrAuthFailed on body tamper", err)
	}
}

func TestBlockCrypto_NonceTamperFails(t *testing.T) {
	c := freshBlockCrypto(t)
	ct, err := c.encrypt([]byte("payload"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ct[0] ^= 0x01
	if _, err := c.decrypt(ct); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("error = %v, want ErrAuthFailed on nonce tamper", err)
	}
}

func TestBlockCrypto_WrongKeyFails(t *testing.T) {
	c1 := freshBlockCrypto(t)
	ct, err := c1.encrypt([]byte("payload"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	c2 := freshBlockCrypto(t)
	if _, err := c2.decrypt(ct); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("error = %v, want ErrAuthFailed on wrong key", err)
	}
}

func TestBlockCrypto_FreshNoncePerEncrypt(t *testing.T) {
	c := freshBlockCrypto(t)
	pt := []byte("identical plaintext")
	seen := map[[inlineNonceSize]byte]struct{}{}
	for i := 0; i < 64; i++ {
		ct, err := c.encrypt(pt)
		if err != nil {
			t.Fatalf("encrypt %d: %v", i, err)
		}
		var nonce [inlineNonceSize]byte
		copy(nonce[:], ct[:inlineNonceSize])
		if _, dup := seen[nonce]; dup {
			t.Fatalf("duplicate nonce at iteration %d", i)
		}
		seen[nonce] = struct{}{}
	}
}

func TestBlockCrypto_DifferentNoncesProduceDifferentCiphertext(t *testing.T) {
	c := freshBlockCrypto(t)
	pt := []byte("identical input")
	ct1, err := c.encrypt(pt)
	if err != nil {
		t.Fatalf("encrypt 1: %v", err)
	}
	ct2, err := c.encrypt(pt)
	if err != nil {
		t.Fatalf("encrypt 2: %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext must differ (random nonce)")
	}
}
