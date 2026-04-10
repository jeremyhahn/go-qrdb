// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package seal provides barrier encryption for go-qrdb's storage layer.
// The StorageBarrier wraps a StorageBackend with transparent authenticated
// encryption, supporting multiple sealing strategies (software, Shamir,
// hardware via go-xkms), multi-tenant isolation, and disaster recovery
// operations.
//
// Core types (StrategyID, SealingStrategy, SymmetricEncrypter, etc.) are
// defined in go-quicraft/pkg/seal and re-exported here as type aliases so
// that go-qrdb consumers do not need to import go-quicraft directly.
package seal

import (
	"time"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
)

// --- Re-exported types from go-quicraft/pkg/seal ---

// StrategyID uniquely identifies a sealing strategy.
type StrategyID = quicraftseal.StrategyID

// SealingStrategy defines the interface for root key seal/unseal operations.
type SealingStrategy = quicraftseal.SealingStrategy

// SymmetricEncrypter provides symmetric encryption/decryption operations.
type SymmetricEncrypter = quicraftseal.SymmetricEncrypter

// StorageBackend is a context-aware interface for key-value storage using
// string keys. All methods accept context.Context and use string keys.
type StorageBackend = quicraftseal.StorageBackend

// Credentials holds authentication material for seal/unseal operations.
type Credentials = quicraftseal.Credentials

// SealedRootKey is the persisted form of an encrypted root key.
type SealedRootKey = quicraftseal.SealedRootKey

// ShareAccumulator collects shares until the quorum threshold is met.
type ShareAccumulator = quicraftseal.ShareAccumulator

// ShareSplitter splits a secret into M-of-N Shamir shares.
type ShareSplitter = quicraftseal.ShareSplitter

// QuorumProgress reports the current state of a quorum accumulator.
type QuorumProgress = quicraftseal.QuorumProgress

// ShamirInitResult contains the output of Shamir initialization.
type ShamirInitResult = quicraftseal.ShamirInitResult

// ShamirConfig configures Shamir secret sharing for barrier operations.
type ShamirConfig = quicraftseal.ShamirConfig

// SoftwareStrategy is a type alias for go-quicraft's SoftwareStrategy.
// This enables go-qrdb consumers to use the software sealing strategy
// without importing go-quicraft directly.
type SoftwareStrategy = quicraftseal.SoftwareStrategy

// NewSoftwareStrategy is a constructor alias for go-quicraft's software
// strategy. It creates a SoftwareStrategy that auto-selects the KDF
// based on FIPS mode: PBKDF2-SHA256 when GOFIPS140 is set, Argon2id otherwise.
var NewSoftwareStrategy = quicraftseal.NewSoftwareStrategy

// EpochBarrier is a type alias for go-quicraft's Barrier. The EpochBarrier
// provides algorithm-agile authenticated encryption with epoch-based key
// rotation, HKDF DEK derivation, and AEAD safety tracking.
type EpochBarrier = quicraftseal.Barrier

// BarrierConfig is a type alias for go-quicraft's BarrierConfig.
type EpochBarrierConfig = quicraftseal.BarrierConfig

// DefaultBarrierConfig is a constructor alias for go-quicraft's
// DefaultBarrierConfig.
var DefaultEpochBarrierConfig = quicraftseal.DefaultBarrierConfig

// InvocationCounter tracks AEAD encrypt operations and enforces limits.
type InvocationCounter = quicraftseal.InvocationCounter

// NonceTrackerProvider provides nonce reuse detection for AEAD ciphers.
type NonceTrackerProvider = quicraftseal.NonceTrackerProvider

// AEADProvider creates cipher.AEAD instances for a given algorithm and key.
type AEADProvider = quicraftseal.AEADProvider

// SymmetricAlgorithm identifies a symmetric encryption algorithm.
type SymmetricAlgorithm = quicraftseal.SymmetricAlgorithm

// NewInvocationCounter creates an InMemoryInvocationCounter.
var NewInvocationCounter = quicraftseal.NewInvocationCounter

// NewNonceTracker creates an InMemoryNonceTracker.
var NewNonceTracker = quicraftseal.NewNonceTracker

// NewSoftwareAEADProvider creates a software AEAD provider.
var NewSoftwareAEADProvider = quicraftseal.NewSoftwareAEADProvider

// ErrInvocationLimitExceeded is returned when the AEAD invocation limit is exceeded.
var ErrInvocationLimitExceeded = quicraftseal.ErrInvocationLimitExceeded

// ErrNonceReuse is returned when nonce reuse is detected.
var ErrNonceReuse = quicraftseal.ErrNonceReuse

// --- Re-exported constants ---

const (
	StrategyTPM2     = quicraftseal.StrategyTPM2
	StrategyPKCS11   = quicraftseal.StrategyPKCS11
	StrategyAWSKMS   = quicraftseal.StrategyAWSKMS
	StrategyGCPKMS   = quicraftseal.StrategyGCPKMS
	StrategyAzureKV  = quicraftseal.StrategyAzureKV
	StrategyVault    = quicraftseal.StrategyVault
	StrategyShamir   = quicraftseal.StrategyShamir
	StrategySoftware = quicraftseal.StrategySoftware
)

// DefaultPreferenceOrder defines strategy selection priority.
var DefaultPreferenceOrder = quicraftseal.DefaultPreferenceOrder

// DefaultQuorumTTL is the default time-to-live for a quorum accumulator
// session, re-exported from go-quicraft.
const DefaultQuorumTTL = quicraftseal.DefaultQuorumTTL

// BarrierStatus reports the current state of a StorageBarrier.
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
