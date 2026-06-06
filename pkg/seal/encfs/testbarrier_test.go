// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// fakeBarrier is a deterministic AES-256-GCM-backed barrier used in
// unit tests. It mimics the semantics of seal.StorageBarrier without
// requiring a full strategy chain or persistent root key: callers can
// flip the seal state at will to verify that encfs surfaces ErrSealed
// correctly.
type fakeBarrier struct {
	aead   cipher.AEAD
	sealed atomic.Bool
}

func newFakeBarrier(t interface {
	Helper()
	Fatalf(string, ...any)
}) *fakeBarrier {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	blk, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	return &fakeBarrier{aead: aead}
}

func (b *fakeBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	if b.sealed.Load() {
		return nil, seal.ErrSealed
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+16)
	out = append(out, nonce[:]...)
	return b.aead.Seal(out, nonce[:], plaintext, nil), nil
}

func (b *fakeBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if b.sealed.Load() {
		return nil, seal.ErrSealed
	}
	if len(ciphertext) < 12+16 {
		return nil, errors.New("fakeBarrier: ciphertext too short")
	}
	nonce := ciphertext[:12]
	body := ciphertext[12:]
	return b.aead.Open(nil, nonce, body, nil)
}

func (b *fakeBarrier) seal()   { b.sealed.Store(true) }
func (b *fakeBarrier) unseal() { b.sealed.Store(false) }
