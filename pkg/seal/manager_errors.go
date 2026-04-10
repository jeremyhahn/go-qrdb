// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

// InvalidShareError is returned when an invalid or malformed share is
// submitted during the unseal process.
type InvalidShareError struct {
	Reason string
}

func (e *InvalidShareError) Error() string {
	return "qrdb/seal: invalid share: " + e.Reason
}

// BarrierSealedError is returned when encrypt or decrypt is attempted
// while the encryption barrier is sealed.
type BarrierSealedError struct{}

func (e *BarrierSealedError) Error() string {
	return "qrdb/seal: encryption barrier is sealed"
}

// DEKDerivationError is returned when data encryption key derivation fails.
type DEKDerivationError struct {
	TenantID string
	Err      error
}

func (e *DEKDerivationError) Error() string {
	msg := "qrdb/seal: DEK derivation failed for tenant " + e.TenantID
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *DEKDerivationError) Unwrap() error { return e.Err }

// InvalidConfigError is returned when the manager configuration is invalid.
type InvalidConfigError struct {
	Field  string
	Reason string
}

func (e *InvalidConfigError) Error() string {
	return "qrdb/seal: invalid config: " + e.Field + ": " + e.Reason
}

// TenantDEKNotFoundError is returned when no DEK exists for a tenant.
type TenantDEKNotFoundError struct {
	TenantID string
}

func (e *TenantDEKNotFoundError) Error() string {
	return "qrdb/seal: tenant DEK not found: " + e.TenantID
}
