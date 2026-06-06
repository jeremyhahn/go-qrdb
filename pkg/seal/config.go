// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import "os"

// PKCS11Config holds the HSM seal strategy configuration. The user PIN is
// never stored in config text; it is loaded from the environment variable
// named by UserPinEnvVar at runtime.
type PKCS11Config struct {
	// ModuleLibPath is the filesystem path to the PKCS#11 shared library
	// (e.g., /usr/lib/softhsm/libsofthsm2.so or /usr/lib/opensc-pkcs11.so).
	ModuleLibPath string `yaml:"module_lib_path"`

	// SlotID is the PKCS#11 slot to use when TokenLabel is empty.
	SlotID uint `yaml:"slot_id"`

	// TokenLabel selects the slot by token label instead of SlotID when set.
	// When non-empty, the strategy scans all slots to find the matching token.
	TokenLabel string `yaml:"token_label,omitempty"`

	// UserPinEnvVar is the name of the environment variable that holds the
	// user PIN for CKU_USER login. The PIN is never persisted to disk.
	// Example: "QRDB_HSM_USER_PIN"
	UserPinEnvVar string `yaml:"user_pin_env_var"`

	// KEKLabel is the CKA_LABEL of the AES key on the token that wraps the
	// root key. The KEK must already exist on the token; the strategy does
	// not create it. Key creation is a Security Officer ceremony concern.
	KEKLabel string `yaml:"kek_label"`
}

// UserPIN reads the user PIN from the environment variable named by
// UserPinEnvVar. Returns an empty string if the variable is not set or
// UserPinEnvVar is empty.
func (c *PKCS11Config) UserPIN() string {
	if c == nil || c.UserPinEnvVar == "" {
		return ""
	}
	return os.Getenv(c.UserPinEnvVar)
}

// BarrierConfig configures a StorageBarrier instance.
type BarrierConfig struct {
	// PreferenceOrder is the explicit, ordered list of candidate sealing
	// strategies; the first available one seals the root key. Required —
	// there is no implicit default. Strategy selection with an empty
	// PreferenceOrder fails with ErrNoStrategySpecified.
	PreferenceOrder []StrategyID

	// RootKeyPath is the storage key under which the sealed root key blob
	// is persisted. Must be non-empty.
	RootKeyPath string

	// AuditLogger receives audit events for seal/unseal operations.
	// If nil, audit logging is disabled.
	AuditLogger AuditLogger

	// Shamir configures optional Shamir secret sharing for barrier operations.
	// When set, InitializeShamir and UnsealWithShare/UnsealWithShares methods
	// become available.
	Shamir *ShamirConfig

	// PKCS11 configures the HSM-backed seal strategy. When non-nil and the
	// binary is built with the pkcs11 build tag, NewPKCS11Strategy uses this
	// config to locate the module library, slot, and KEK. When nil or built
	// without the pkcs11 tag, the strategy reports unavailable.
	//
	// Strategy preference order (highest to lowest): TPM2 > PKCS#11 > Software.
	// TPM2 is injected by go-xkms at construction and always takes priority.
	PKCS11 *PKCS11Config `yaml:"pkcs11,omitempty"`

	// DeploymentID identifies the deployment for future EpochBarrier
	// integration. A value of 0 means unset.
	DeploymentID uint64

	// InvocationCounter allows injection of a custom invocation counter.
	// Nil = default InMemoryInvocationCounter.
	InvocationCounter InvocationCounter

	// NonceTrackerProvider allows injection of a custom nonce tracker.
	// Nil = default InMemoryNonceTracker.
	NonceTrackerProvider NonceTrackerProvider

	// AEADProvider allows injection of a custom cipher.AEAD factory.
	// Nil = default SoftwareAEADProvider.
	AEADProvider AEADProvider

	// InvocationLimit is the max encrypt operations per key epoch.
	// Default: 2^32 (NIST SP 800-38D for random 96-bit nonces).
	// Set to 0 for default; -1 to disable.
	InvocationLimit int64

	// RotationThreshold is the percentage of InvocationLimit at which
	// ShouldRotate() returns true. Default: 95.0.
	RotationThreshold float64
}

// TenantBarrierConfig configures per-tenant barrier behavior.
type TenantBarrierConfig struct {
	// Enabled enables per-tenant barriers. When false (default),
	// all operations use the system barrier for backward compatibility.
	Enabled bool `json:"enabled"`

	// TenantIDs lists the tenant IDs to pre-register on startup.
	TenantIDs []string `json:"tenant_ids,omitempty"`
}
