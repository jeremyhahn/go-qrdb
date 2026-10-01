// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

// SealMode identifies the sealing mode.
type SealMode int

const (
	// SealModeAuto selects the best available sealing mode automatically.
	SealModeAuto SealMode = iota

	// SealModeShamir uses Shamir secret sharing for seal/unseal operations.
	SealModeShamir
)

// EncryptionBarrier provides encryption/decryption using the master key.
// Unlike the full StorageBarrier struct, this interface exposes only the
// cryptographic operations needed by consumers.
type EncryptionBarrier interface {
	// Encrypt encrypts plaintext using the master key.
	Encrypt(plaintext []byte) ([]byte, error)

	// Decrypt decrypts ciphertext using the master key.
	Decrypt(ciphertext []byte) ([]byte, error)

	// IsSealed reports whether the barrier is in sealed state.
	IsSealed() bool
}
