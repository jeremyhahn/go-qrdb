// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

// BarrierConfig configures a Barrier instance.
type BarrierConfig struct {
	// PreferenceOrder specifies the order in which sealing strategies are
	// evaluated. The first available strategy is used to seal the root key.
	// If nil or empty, DefaultPreferenceOrder is used.
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
}

// ShamirConfig configures Shamir secret sharing for barrier operations.
type ShamirConfig struct {
	// Threshold is the minimum number of shares (M) needed to reconstruct.
	Threshold int `json:"threshold"`
	// TotalShares is the total number of shares (N) to generate.
	TotalShares int `json:"total_shares"`
}

// TenantBarrierConfig configures per-tenant barrier behavior.
type TenantBarrierConfig struct {
	// Enabled enables per-tenant barriers. When false (default),
	// all operations use the system barrier for backward compatibility.
	Enabled bool `json:"enabled"`

	// TenantIDs lists the tenant IDs to pre-register on startup.
	TenantIDs []string `json:"tenant_ids,omitempty"`
}
