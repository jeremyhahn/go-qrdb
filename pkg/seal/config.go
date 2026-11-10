// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import "time"

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
	// When set, InitializeShamir and the share-holder quorum
	// (SubmitHolderShare) become available.
	Shamir *ShamirConfig

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

	// SealDrainTimeout bounds how long Seal waits for the encrypted stores'
	// in-flight flushes, durable writes and compactions to drain before the
	// key leaves memory. Zero means DefaultSealDrainTimeout; a negative value
	// does not wait. Seal seals either way, and reports the stores that did
	// not drain (see StorageBarrier.Seal).
	SealDrainTimeout time.Duration
}

// DefaultSealDrainTimeout is the SealDrainTimeout a zero BarrierConfig uses.
const DefaultSealDrainTimeout = 10 * time.Second

// sealDrainTimeout resolves SealDrainTimeout's zero value to the default.
func (c BarrierConfig) sealDrainTimeout() time.Duration {
	if c.SealDrainTimeout == 0 {
		return DefaultSealDrainTimeout
	}
	return c.SealDrainTimeout
}

// TenantBarrierConfig configures per-tenant barrier behavior.
type TenantBarrierConfig struct {
	// Enabled enables per-tenant barriers. When false (default),
	// all operations use the system barrier for backward compatibility.
	Enabled bool `json:"enabled"`

	// TenantIDs lists the tenant IDs to pre-register on startup.
	TenantIDs []string `json:"tenant_ids,omitempty"`
}
