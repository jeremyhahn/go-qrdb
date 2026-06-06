// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package pin provides PIN management for the QuicRaft / QRDB stack.
//
// PINBackend is the canonical interface implemented by software (Argon2id /
// PBKDF2 hashing) and hardware (TPM2, PKCS#11) backends. The software
// backend lives here in qrdb; hardware backends live in go-xkms and are
// injected at composition time via qrdbserver.WithPINBackend.
//
// Service is the single entry point for PIN operations across the stack
// (REST / gRPC / QUIC / MCP handlers, CLI commands, SDK callers).
package pin

import "time"

// StrategyID identifies a PIN backend strategy.
type StrategyID string

const (
	// StrategySoftware uses Argon2id / PBKDF2 password hashing with
	// storage-backed persistence. The canonical default for qrdb-only
	// deployments and the fallback when no hardware backend is configured.
	StrategySoftware StrategyID = "software"

	// StrategyTPM2 uses TPM 2.0 platform-key auth for PIN verification.
	// Implemented in go-xkms/pkg/pin/tpm2_backend.go.
	StrategyTPM2 StrategyID = "tpm2"

	// StrategyPKCS11 uses a PKCS#11 token's native C_Login / C_SetPIN for
	// PIN management. Implemented in go-xkms.
	StrategyPKCS11 StrategyID = "pkcs11"

	// minPINLength is the minimum allowed PIN length enforced uniformly
	// across all backends.
	minPINLength = 6
)

// LockoutStatus reports the current lockout state of a PIN backend. Hardware
// backends (TPM2 dictionary-attack protection, PKCS#11 token lockout)
// populate it; the software backend reports nil (no native lockout).
type LockoutStatus struct {
	// FailedAttempts is the number of failed PIN attempts since the last
	// successful verification or reset.
	FailedAttempts int `json:"failed_attempts"`

	// MaxAttempts is the threshold at which the backend transitions to
	// the locked state.
	MaxAttempts int `json:"max_attempts"`

	// IsLocked is true while the backend is rejecting PIN attempts.
	IsLocked bool `json:"is_locked"`

	// LockoutUntil is the wall-clock time at which the lockout
	// auto-recovers. Zero when no lockout is active.
	LockoutUntil time.Time `json:"lockout_until,omitempty"`

	// RecoverySeconds is the remaining time (in seconds) until the
	// backend exits the locked state.
	RecoverySeconds int `json:"recovery_seconds"`
}

// LockoutConfig configures lockout behaviour for software-side counters
// when a backend needs to enforce its own (most backends rely on hardware
// dictionary-attack protection).
type LockoutConfig struct {
	MaxAttempts     int           `json:"max_attempts"`
	LockoutDuration time.Duration `json:"lockout_duration"`
	Backoff         bool          `json:"backoff"`
}

// DefaultLockoutConfig returns reasonable production defaults for the
// optional software lockout layer (5 attempts / 5 minutes / exponential
// backoff). Most callers should not need to configure this — hardware
// backends supersede it.
func DefaultLockoutConfig() LockoutConfig {
	return LockoutConfig{
		MaxAttempts:     5,
		LockoutDuration: 5 * time.Minute,
		Backoff:         true,
	}
}

// validatePINLength enforces the uniform minimum PIN length across all
// backends. Returns ErrPINTooShort when pin has fewer than minPINLength
// characters.
func validatePINLength(pin string) error {
	if len(pin) < minPINLength {
		return ErrPINTooShort
	}
	return nil
}
