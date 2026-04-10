// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package aesgcm

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"testing"
)

// TestDefaultNewGCM_NewGCMFailureBranch exercises the cipher.NewGCM error
// branch inside defaultNewGCM by injecting a block cipher implementation
// that returns an error from NewGCM.
//
// The standard library's cipher.NewGCM only fails for non-standard block
// sizes. We inject a custom block implementation that reports a non-standard
// block size to trigger that path.
func TestDefaultNewGCM_NewGCMFailureBranch(t *testing.T) {
	// Save and restore the real newGCM.
	orig := newGCM
	defer func() { newGCM = orig }()

	// Swap newGCM with a function that calls aes.NewCipher successfully but
	// then wraps a call to cipher.NewGCM with a wrong-size block to force
	// an error.
	newGCM = func(key []byte) (cipher.AEAD, error) {
		// Use a valid AES key to get a real block cipher.
		block, err := aes.NewCipher(make([]byte, 32))
		if err != nil {
			return nil, &CipherError{Op: "create cipher", Err: err}
		}
		// Wrap the block to report block size != 16, making cipher.NewGCM fail.
		_, err = cipher.NewGCM(wrongSizeBlock{block})
		if err != nil {
			return nil, &CipherError{Op: "create GCM", Err: err}
		}
		return nil, errors.New("unreachable")
	}

	key := make([]byte, 32)
	_, err := Encrypt(key, []byte("data"))
	if err == nil {
		t.Fatal("expected error when NewGCM fails")
	}
	var ce *CipherError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CipherError, got %T: %v", err, err)
	}
	if ce.Op != "create GCM" {
		t.Errorf("expected Op=%q, got %q", "create GCM", ce.Op)
	}
}

// wrongSizeBlock wraps a cipher.Block but reports a BlockSize of 17,
// causing cipher.NewGCM to return an error.
type wrongSizeBlock struct {
	cipher.Block
}

func (wrongSizeBlock) BlockSize() int { return 17 }
