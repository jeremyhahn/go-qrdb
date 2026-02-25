// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package seal provides barrier encryption for go-qrdb's storage layer.
// The Barrier wraps a StorageBackend with transparent authenticated encryption,
// supporting multiple sealing strategies (software, Shamir, hardware via go-xkms),
// multi-tenant isolation, and disaster recovery operations.
package seal

import (
	"context"
	"time"
)

// StrategyID uniquely identifies a sealing strategy.
type StrategyID string

const (
	// StrategyTPM2 uses TPM 2.0 hardware for root key protection.
	StrategyTPM2 StrategyID = "tpm2"

	// StrategyPKCS11 uses PKCS#11 HSM tokens for root key protection.
	StrategyPKCS11 StrategyID = "pkcs11"

	// StrategyAWSKMS uses AWS Key Management Service for root key protection.
	StrategyAWSKMS StrategyID = "awskms"

	// StrategyGCPKMS uses Google Cloud KMS for root key protection.
	StrategyGCPKMS StrategyID = "gcpkms"

	// StrategyAzureKV uses Azure Key Vault for root key protection.
	StrategyAzureKV StrategyID = "azurekv"

	// StrategyVault uses HashiCorp Vault for root key protection.
	StrategyVault StrategyID = "vault"

	// StrategyShamir splits the root key itself into M-of-N Shamir shares.
	// No password is required; the root key IS the Shamir secret.
	StrategyShamir StrategyID = "shamir"

	// StrategySoftware uses password-based encryption (Argon2id + AES-256-GCM)
	// as a software-only fallback for root key protection.
	StrategySoftware StrategyID = "software"
)

// DefaultPreferenceOrder defines the default strategy selection order.
// Hardware-backed strategies are preferred over software-only fallback.
var DefaultPreferenceOrder = []StrategyID{
	StrategyTPM2,
	StrategyPKCS11,
	StrategyAWSKMS,
	StrategyGCPKMS,
	StrategyAzureKV,
	StrategyVault,
	StrategyShamir,
	StrategySoftware,
}

// SealingStrategy defines the interface for root key seal/unseal operations.
// Each strategy wraps a different protection mechanism (TPM, HSM, cloud KMS,
// or software-based encryption).
type SealingStrategy interface {
	// ID returns the unique identifier for this strategy.
	ID() StrategyID

	// Available reports whether this strategy can currently be used.
	// For hardware strategies, this checks device availability.
	Available() bool

	// HardwareBacked returns true if this strategy uses dedicated
	// cryptographic hardware rather than software-only protection.
	HardwareBacked() bool

	// SealRootKey encrypts the root key using the strategy's native
	// protection mechanism.
	SealRootKey(ctx context.Context, rootKey []byte, creds Credentials) (*SealedRootKey, error)

	// UnsealRootKey decrypts a previously sealed root key.
	UnsealRootKey(ctx context.Context, sealed *SealedRootKey, creds Credentials) ([]byte, error)

	// BarrierEncryptor returns a SymmetricEncrypter for barrier data operations.
	// For hardware-backed strategies, keys never leave hardware.
	// For software strategies, a DEK is derived from the root key.
	BarrierEncryptor(ctx context.Context, rootKey []byte) (SymmetricEncrypter, error)
}

// SymmetricEncrypter provides symmetric encryption/decryption operations.
// Implementations handle nonce generation and wire format internally.
type SymmetricEncrypter interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// StorageBackend is a minimal interface for key-value storage.
// go-qrdb's storage.Engine satisfies this via Go duck typing.
type StorageBackend interface {
	Get(key string) ([]byte, error)
	Put(key string, data []byte) error
	Delete(key string) error
	List(prefix string) ([]string, error)
	Exists(key string) (bool, error)
	Close() error
}

// Credentials holds authentication material for seal/unseal operations.
type Credentials struct {
	Secret string
}

// SealedRootKey is the JSON-serializable envelope stored in the backend
// that contains the sealed root key and metadata about how it was sealed.
type SealedRootKey struct {
	// Version is the format version of this sealed root key blob.
	Version int `json:"version"`

	// Strategy identifies which sealing strategy created this blob.
	Strategy StrategyID `json:"strategy"`

	// KDFVersion records which password hashing version was used for
	// software-based sealing. 0 (or absent) implies legacy Argon2id (V1).
	KDFVersion int `json:"kdf_version,omitempty"`

	// Salt is the KDF salt for software-based sealing.
	Salt []byte `json:"salt,omitempty"`

	// Nonce is the AES-GCM nonce for software-based sealing.
	Nonce []byte `json:"nonce,omitempty"`

	// Ciphertext is the encrypted root key for software-based sealing.
	Ciphertext []byte `json:"ciphertext,omitempty"`

	// ShamirThreshold records the M value when sealed with StrategyShamir.
	ShamirThreshold int `json:"shamir_threshold,omitempty"`

	// ShamirTotal records the N value when sealed with StrategyShamir.
	ShamirTotal int `json:"shamir_total,omitempty"`

	// HardwarePayload holds strategy-specific data for hardware strategies
	// (TPM2, PKCS#11, cloud KMS). Opaque to the barrier.
	HardwarePayload []byte `json:"hardware_payload,omitempty"`

	// CreatedAt records when the root key was sealed.
	CreatedAt time.Time `json:"created_at"`
}

// BarrierStatus reports the current state of a Barrier.
type BarrierStatus struct {
	// Sealed is true when the barrier is in sealed state.
	Sealed bool `json:"sealed"`

	// Strategy identifies which sealing strategy protects the root key.
	Strategy StrategyID `json:"strategy"`

	// HardwareBacked is true when the active strategy uses dedicated
	// cryptographic hardware.
	HardwareBacked bool `json:"hardware_backed"`

	// InitializedAt records when the barrier was first initialized.
	InitializedAt time.Time `json:"initialized_at"`
}
