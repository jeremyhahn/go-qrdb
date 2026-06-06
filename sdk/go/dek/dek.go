// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package dek re-exports go-qrdb's DEK management types so SDK consumers
// never need to import the internal pkg/dek path directly. All public types,
// constructors, and error types are aliases — there is no logic in this file
// beyond forwarding to the canonical implementation.
//
// This is the SINGLE authoritative source for TenantDEKManager and
// ClusterDEKManager. Any consumer that re-implements these types instead of
// importing this package is wrong. See bootstrap.go for the canonical
// default-tenant bootstrap helper.
//
// See github.com/jeremyhahn/go-qrdb/pkg/dek for the canonical documentation.
package dek

import (
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TenantDEKManager is the SDK re-export of dek.TenantDEKManager.
type TenantDEKManager = dek.TenantDEKManager

// ClusterDEKManager is the SDK re-export of dek.ClusterDEKManager.
type ClusterDEKManager = dek.ClusterDEKManager

// TenantDEKStatus is the SDK re-export of dek.TenantDEKStatus.
type TenantDEKStatus = dek.TenantDEKStatus

// TenantDEKManagerConfig is the SDK re-export of dek.TenantDEKManagerConfig.
type TenantDEKManagerConfig = dek.TenantDEKManagerConfig

// StorageBackend is the SDK re-export of dek.StorageBackend.
type StorageBackend = dek.StorageBackend

// EncryptionBarrier is the SDK re-export of seal.EncryptionBarrier, which
// ClusterDEKManager requires for key wrapping.
type EncryptionBarrier = seal.EncryptionBarrier

// Algorithm and key-size constants.
const (
	AlgorithmAES256GCM = dek.AlgorithmAES256GCM
	AESKeySize         = dek.AESKeySize
)

// Error type re-exports.
type (
	NilInputError             = dek.NilInputError
	UnsupportedAlgorithmError = dek.UnsupportedAlgorithmError
	SealedError               = dek.SealedError
	CryptoError               = dek.CryptoError
	NotInitializedError       = dek.NotInitializedError
	AlreadyInProgressError    = dek.AlreadyInProgressError
	NoActiveOperationError    = dek.NoActiveOperationError
	NotImplementedError       = dek.NotImplementedError
	ManagerClosedError        = dek.ManagerClosedError
	NotFoundError             = dek.NotFoundError
	DecodeError               = dek.DecodeError
	EncodeError               = dek.EncodeError
	StorageOperationError     = dek.StorageOperationError
	CiphertextTooShortError   = dek.CiphertextTooShortError
	DataTruncatedError        = dek.DataTruncatedError
	RequiredFieldError        = dek.RequiredFieldError
	InvalidInputError         = dek.InvalidInputError
)

// IsNotFound is the SDK re-export of dek.IsNotFound.
func IsNotFound(err error) bool { return dek.IsNotFound(err) }

// IsValidAlgorithm is the SDK re-export of dek.IsValidAlgorithm.
func IsValidAlgorithm(algorithm string) bool { return dek.IsValidAlgorithm(algorithm) }

// NewClusterDEKManager is the SDK re-export of dek.NewClusterDEKManager.
func NewClusterDEKManager(
	barrier EncryptionBarrier,
	logger *slog.Logger,
) (*ClusterDEKManager, error) {
	return dek.NewClusterDEKManager(barrier, logger)
}

// NewTenantDEKManager is the SDK re-export of dek.NewTenantDEKManager.
func NewTenantDEKManager(
	clusterDEK *ClusterDEKManager,
	storage StorageBackend,
	logger *slog.Logger,
	config *TenantDEKManagerConfig,
) (TenantDEKManager, error) {
	return dek.NewTenantDEKManager(clusterDEK, storage, logger, config)
}
