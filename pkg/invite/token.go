// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package invite

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// TokenBytes is the length of the random material behind an invitation token.
//
// 32 bytes is chosen so the token is not guessable: an invitation link is a
// bearer credential that grants first-factor enrollment for a named user, and
// unlike a password it is never rate-limited by a login form.
const TokenBytes = 32

// GenerateToken returns a fresh invitation token as a hex string.
//
// The plaintext is returned exactly once, to be delivered to the invited user.
// It is never stored; only TokenDigest of it reaches the database.
func GenerateToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", &TokenGenerationError{Cause: err}
	}
	return hex.EncodeToString(b), nil
}

// TokenDigest returns the hex-encoded SHA-256 of a plaintext token.
//
// A plain digest rather than a slow password hash is correct here: the token
// carries 256 bits of uniform randomness, so there is no dictionary to attack
// and nothing for a work factor to buy. It is also what allows the digest to be
// used directly as a storage key, making redemption a single keyed lookup
// rather than a scan.
func TokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
