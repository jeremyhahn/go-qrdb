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

package pin

// PINBackend is the canonical interface every PIN management implementation
// satisfies. The interface is owned by qrdb so consumers across the stack
// (qrdb's ceremony, go-quicbaas's SDK, go-trusted-platform's CLI) all
// program against a single contract.
//
// Backends fall into two categories:
//
//   - Software (qrdb/pkg/pin.SoftwareBackend): PINs hashed with Argon2id
//     or PBKDF2 and persisted to a Storage. No native lockout.
//
//   - Hardware (go-xkms): TPM2 uses PlatformSRK password auth with TPM
//     dictionary-attack protection; PKCS#11 uses token-native C_Login /
//     C_SetPIN with the HSM's lockout policy.
//
// Higher layers inject the appropriate backend at composition time via
// qrdbserver.WithPINBackend; no transitional re-exports are kept around.
type PINBackend interface {
	// Strategy returns the backend strategy identifier (e.g. StrategySoftware,
	// StrategyTPM2, StrategyPKCS11). Used by transport handlers to decide
	// whether a backend supports a given operation.
	Strategy() StrategyID

	// SetSOPIN sets the Security Officer PIN. For first-time setup pass
	// an empty currentSOPIN; for subsequent changes both fields must be
	// supplied so the current PIN can be verified before the new one is
	// persisted.
	SetSOPIN(currentSOPIN, newSOPIN string) error

	// SetUserPIN sets the user PIN. The caller must supply the SO PIN
	// for authorization; the backend verifies it before persisting the
	// new user PIN.
	SetUserPIN(soPIN, newUserPIN string) error

	// ChangeSOPIN changes the SO PIN from currentSOPIN to newSOPIN.
	ChangeSOPIN(currentSOPIN, newSOPIN string) error

	// ChangeUserPIN changes the user PIN from currentUserPIN to newUserPIN.
	ChangeUserPIN(currentUserPIN, newUserPIN string) error

	// VerifySOPIN verifies the SO PIN using the backend's native mechanism.
	// Returns nil on success; backend-specific errors (ErrPINInvalid,
	// ErrPINNotSet, ErrTPMLocked, etc.) on failure.
	VerifySOPIN(pin string) error

	// VerifyUserPIN verifies the user PIN using the backend's native
	// mechanism with the same error semantics as VerifySOPIN.
	VerifyUserPIN(pin string) error

	// IsInitialized reports whether the backend has completed first-time
	// setup (SO PIN configured or equivalent hardware provisioning).
	IsInitialized() bool

	// SOPINSet reports whether the SO PIN has been configured.
	SOPINSet() bool

	// UserPINSet reports whether the user PIN has been configured.
	UserPINSet() bool

	// GetLockoutStatus reports the backend's native lockout state. Returns
	// nil when the backend has no lockout (software backend) so callers
	// can distinguish absent-state from cleared-state.
	GetLockoutStatus() *LockoutStatus

	// ResetLockout clears the backend's lockout counters using SO PIN
	// authorization. Returns nil unconditionally when the backend has no
	// lockout mechanism.
	ResetLockout(soPIN string) error
}
