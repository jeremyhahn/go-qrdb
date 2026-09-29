// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const (
	rootTokenSize = 32
	// #nosec G101 -- HKDF/HMAC domain-separation label, not a credential.
	rootTokenHMACInfo = "go-qrdb/root-token/v1"
)

// RootToken represents a generated root token that proves a quorum of share
// holders consented. The token is derived from the root key using
// HMAC-SHA256 over a random nonce.
type RootToken struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// deriveRootToken mints a root token from the root key an OpRootToken quorum
// authenticated. The key is read under a borrow and never copied out.
func deriveRootToken(rootKey *secure.Buffer) (*RootToken, error) {
	nonce := make([]byte, rootTokenSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	var tokenBytes []byte
	if err := rootKey.WithBytes(func(key []byte) error {
		mac := hmac.New(sha256.New, key)
		mac.Write(nonce)
		mac.Write([]byte(rootTokenHMACInfo))
		tokenBytes = mac.Sum(nil)
		return nil
	}); err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	return &RootToken{Token: hex.EncodeToString(tokenBytes), CreatedAt: time.Now()}, nil
}
