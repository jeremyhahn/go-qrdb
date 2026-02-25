// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import "errors"

var (
	// ErrSealed is returned when an operation is attempted on a sealed barrier.
	ErrSealed = errors.New("qrdb/seal: barrier is sealed")

	// ErrAlreadyUnsealed is returned when attempting to unseal a barrier that
	// is already in the unsealed state.
	ErrAlreadyUnsealed = errors.New("qrdb/seal: barrier is already unsealed")

	// ErrAlreadyInitialized is returned when Initialize is called on a barrier
	// that has already been initialized with a root key.
	ErrAlreadyInitialized = errors.New("qrdb/seal: barrier already initialized")

	// ErrNotInitialized is returned when Unseal is called on a barrier that
	// has not been initialized. Call Initialize first.
	ErrNotInitialized = errors.New("qrdb/seal: barrier not initialized")

	// ErrInvalidCredentials is returned when the provided credentials fail to
	// unseal the root key (wrong password, wrong token, etc).
	ErrInvalidCredentials = errors.New("qrdb/seal: invalid credentials")

	// ErrNoAvailableStrategy is returned when no registered sealing strategy
	// reports itself as available.
	ErrNoAvailableStrategy = errors.New("qrdb/seal: no available sealing strategy")

	// ErrStrategyNotFound is returned when a requested strategy ID is not
	// registered with the barrier.
	ErrStrategyNotFound = errors.New("qrdb/seal: requested strategy not registered")

	// ErrStrategyMismatch is returned when attempting to unseal data with a
	// different strategy than the one that sealed it.
	ErrStrategyMismatch = errors.New("qrdb/seal: sealed with different strategy than requested")

	// ErrCorruptRootKey is returned when the root key blob stored in the
	// backend fails to deserialize or has an unexpected format.
	ErrCorruptRootKey = errors.New("qrdb/seal: root key blob corrupted")

	// ErrNilSealedData is returned when a nil SealedData pointer is passed
	// to an unseal operation.
	ErrNilSealedData = errors.New("qrdb/seal: sealed data is nil")

	// ErrEncryptorNotAvailable is returned when the barrier's encryptor
	// is not available (barrier not unsealed or encryptor not initialized).
	ErrEncryptorNotAvailable = errors.New("qrdb/seal: encryptor not available")

	// ErrHardwareEncryptorRequired is returned when GetMasterKey is called
	// on a hardware-backed strategy that keeps keys in hardware.
	ErrHardwareEncryptorRequired = errors.New("qrdb/seal: master key not exportable from hardware-backed strategy")

	// ErrShamirNotConfigured is returned when a Shamir operation is attempted
	// on a barrier that does not have Shamir secret sharing configured.
	ErrShamirNotConfigured = errors.New("qrdb/seal: shamir not configured")

	// ErrShamirThresholdInvalid is returned when the Shamir threshold is less
	// than 2 or greater than the total number of shares.
	ErrShamirThresholdInvalid = errors.New("qrdb/seal: shamir threshold must be >= 2 and <= total shares")

	// ErrShamirDuplicateShare is returned when a share that has already been
	// submitted is submitted again to the quorum accumulator.
	ErrShamirDuplicateShare = errors.New("qrdb/seal: duplicate share submitted")

	// ErrShamirQuorumExpired is returned when the quorum TTL has elapsed and
	// all accumulated shares have been discarded.
	ErrShamirQuorumExpired = errors.New("qrdb/seal: quorum TTL expired, submit shares again")

	// ErrShamirQuorumIncomplete is returned when not enough shares have been
	// provided to meet the reconstruction threshold.
	ErrShamirQuorumIncomplete = errors.New("qrdb/seal: quorum not yet complete, need more shares")

	// ErrShamirNoQuorum is returned when no active quorum accumulator exists.
	ErrShamirNoQuorum = errors.New("qrdb/seal: no active quorum")

	// ErrShamirCombineFailed is returned when the Shamir combine operation
	// fails to reconstruct the secret from the provided shares.
	ErrShamirCombineFailed = errors.New("qrdb/seal: failed to combine shares")

	// ErrShamirShareNotFound is returned when a requested Shamir share does
	// not exist in storage.
	ErrShamirShareNotFound = errors.New("qrdb/seal: shamir share not found")

	// ErrShamirVerificationFailed is returned when share verification detects
	// inconsistent or corrupted shares.
	ErrShamirVerificationFailed = errors.New("qrdb/seal: shamir share verification failed")

	// ErrShamirNoSharesFound is returned when no shares exist in storage.
	ErrShamirNoSharesFound = errors.New("qrdb/seal: no shamir shares found")

	// ErrShamirStorageFailed is returned when a share storage operation fails.
	ErrShamirStorageFailed = errors.New("qrdb/seal: shamir storage operation failed")

	// ErrShamirNilStorage is returned when a nil storage backend is provided
	// to the Shamir strategy constructor.
	ErrShamirNilStorage = errors.New("qrdb/seal: shamir storage backend must not be nil")

	// ErrShamirSplitFailed is returned when the Shamir secret splitting
	// operation fails.
	ErrShamirSplitFailed = errors.New("qrdb/seal: shamir split failed")

	// ErrShamirSerializationFailed is returned when JSON serialization or
	// deserialization of a Shamir share fails.
	ErrShamirSerializationFailed = errors.New("qrdb/seal: shamir serialization failed")

	// ErrNilSealedBackend is returned when a nil sealed backend is provided.
	ErrNilSealedBackend = errors.New("qrdb/seal: sealed backend must not be nil")

	// ErrSecretNotFound is returned when a named secret is not found.
	ErrSecretNotFound = errors.New("qrdb/seal: secret not found")

	// ErrInvalidSecretName is returned when a secret name is empty.
	ErrInvalidSecretName = errors.New("qrdb/seal: secret name must not be empty")

	// ErrResealFailed is returned when re-sealing a secret fails.
	ErrResealFailed = errors.New("qrdb/seal: reseal failed")

	// ErrRecoveryKeysNotFound is returned when recovery key metadata does
	// not exist in storage.
	ErrRecoveryKeysNotFound = errors.New("qrdb/seal: recovery keys not found")

	// ErrRootTokenVerificationFailed is returned when the reconstructed
	// secret from Shamir shares does not produce a valid DEK.
	ErrRootTokenVerificationFailed = errors.New("qrdb/seal: root token share verification failed")

	// ErrTenantNotFound is returned when a requested tenant ID is not
	// registered in the barrier registry.
	ErrTenantNotFound = errors.New("qrdb/seal: tenant not found")

	// ErrTenantAlreadyExists is returned when attempting to register a
	// tenant that is already registered in the barrier registry.
	ErrTenantAlreadyExists = errors.New("qrdb/seal: tenant already exists")

	// ErrEmptyTenantID is returned when an empty tenant ID is provided
	// to a tenant barrier operation.
	ErrEmptyTenantID = errors.New("qrdb/seal: tenant ID must not be empty")

	// ErrNilSystemBarrier is returned when a nil system barrier is provided
	// to the barrier registry or tenant barrier constructor.
	ErrNilSystemBarrier = errors.New("qrdb/seal: system barrier must not be nil")

	// ErrTenantSealed is returned when an operation is attempted on a
	// sealed tenant barrier.
	ErrTenantSealed = errors.New("qrdb/seal: tenant barrier is sealed")

	// ErrTenantAlreadyInitialized is returned when Initialize is called
	// on a tenant barrier that already has a root key.
	ErrTenantAlreadyInitialized = errors.New("qrdb/seal: tenant barrier already initialized")

	// ErrTenantNotInitialized is returned when Unseal is called on a
	// tenant barrier that has not been initialized.
	ErrTenantNotInitialized = errors.New("qrdb/seal: tenant barrier not initialized")

	// ErrTenantAlreadyUnsealed is returned when Unseal is called on a
	// tenant barrier that is already unsealed.
	ErrTenantAlreadyUnsealed = errors.New("qrdb/seal: tenant barrier is already unsealed")

	// ErrNoStrategy is returned when a tenant is registered without a
	// sealing strategy.
	ErrNoStrategy = errors.New("qrdb/seal: sealing strategy is required")

	// ErrNilStorageBackend is returned when a nil storage backend is
	// provided to the tenant barrier constructor.
	ErrNilStorageBackend = errors.New("qrdb/seal: storage backend must not be nil")
)
