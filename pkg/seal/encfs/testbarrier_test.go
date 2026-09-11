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
	// NewGCMWithRandomNonce is the FIPS-approved construction; it samples the
	// nonce itself and prepends it, which is the same layout this fake wrote
	// by hand.
	aead, err := cipher.NewGCMWithRandomNonce(blk)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	return &fakeBarrier{aead: aead}
}

func (b *fakeBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	if b.sealed.Load() {
		return nil, seal.ErrSealed
	}
	out := make([]byte, 0, 12+len(plaintext)+16)
	// #nosec G407 -- the nonce argument MUST be nil here: this AEAD is cipher.NewGCMWithRandomNonce, which samples a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the ciphertext. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	return b.aead.Seal(out, nil, plaintext, nil), nil
}

func (b *fakeBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if b.sealed.Load() {
		return nil, seal.ErrSealed
	}
	if len(ciphertext) < 12+16 {
		return nil, errors.New("fakeBarrier: ciphertext too short")
	}
	return b.aead.Open(nil, nil, ciphertext, nil)
}

func (b *fakeBarrier) seal()   { b.sealed.Store(true) }
func (b *fakeBarrier) unseal() { b.sealed.Store(false) }
