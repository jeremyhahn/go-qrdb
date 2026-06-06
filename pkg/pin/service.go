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

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"sync/atomic"
)

// FIDO2PINHashSize is the CTAP2-mandated PIN hash length (first 16 bytes
// of SHA-256 of the raw PIN).
const FIDO2PINHashSize = 16

// FIDO2PINHashSetter is the callback invoked by Service whenever a PIN is
// set or changed. Authenticator integrations register one of these to
// receive the new CTAP2 hash so it can be propagated to the FIDO2
// firmware without a separate round trip.
type FIDO2PINHashSetter func(hash []byte)

// FIDO2HashVerifier is the optional interface PINBackend implementations
// expose when they can verify a CTAP2 PIN hash directly. Service falls
// back to false when the configured backend does not implement it.
type FIDO2HashVerifier interface {
	VerifyFIDO2Hash(hash []byte) bool
}

// Service is the single entry point for PIN operations across the stack.
// It validates inputs (PIN length), delegates to the configured
// PINBackend, and propagates FIDO2 PIN hashes to the authenticator via
// the registered setter callback.
type Service struct {
	backend         PINBackend
	fido2HashSetter atomic.Pointer[FIDO2PINHashSetter]
	log             *slog.Logger
}

// NewService constructs a Service backed by the given PINBackend. A nil
// log defaults to slog.Default().
func NewService(backend PINBackend, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{backend: backend, log: log}
}

// SetFIDO2HashSetter registers the callback that receives newly computed
// FIDO2 PIN hashes. Call after authenticator initialization. Passing nil
// clears the registration.
func (s *Service) SetFIDO2HashSetter(setter FIDO2PINHashSetter) {
	s.fido2HashSetter.Store(&setter)
}

// Backend returns the underlying PINBackend.
func (s *Service) Backend() PINBackend { return s.backend }

// Strategy returns the backend's strategy identifier.
func (s *Service) Strategy() StrategyID { return s.backend.Strategy() }

// SetSOPIN sets the Security Officer PIN after enforcing the minimum
// length policy.
func (s *Service) SetSOPIN(currentSOPIN, newSOPIN string) error {
	if err := validatePINLength(newSOPIN); err != nil {
		return err
	}
	return s.backend.SetSOPIN(currentSOPIN, newSOPIN)
}

// SetUserPIN sets the user PIN and propagates the FIDO2 hash to the
// authenticator.
func (s *Service) SetUserPIN(soPIN, newUserPIN string) error {
	if err := validatePINLength(newUserPIN); err != nil {
		return err
	}
	if err := s.backend.SetUserPIN(soPIN, newUserPIN); err != nil {
		return err
	}
	s.pushFIDO2Hash(newUserPIN)
	return nil
}

// ChangeSOPIN changes the Security Officer PIN after enforcing the
// minimum length policy on the new value.
func (s *Service) ChangeSOPIN(currentSOPIN, newSOPIN string) error {
	if err := validatePINLength(newSOPIN); err != nil {
		return err
	}
	return s.backend.ChangeSOPIN(currentSOPIN, newSOPIN)
}

// ChangeUserPIN changes the user PIN and propagates the new FIDO2 hash
// to the authenticator.
func (s *Service) ChangeUserPIN(currentUserPIN, newUserPIN string) error {
	if err := validatePINLength(newUserPIN); err != nil {
		return err
	}
	if err := s.backend.ChangeUserPIN(currentUserPIN, newUserPIN); err != nil {
		return err
	}
	s.pushFIDO2Hash(newUserPIN)
	return nil
}

// VerifySOPIN verifies the SO PIN against the backend's native mechanism.
func (s *Service) VerifySOPIN(pin string) error { return s.backend.VerifySOPIN(pin) }

// VerifyUserPIN verifies the user PIN against the backend's native
// mechanism and propagates the FIDO2 hash on success so the authenticator
// can answer CTAP2 PIN verifications without a separate PIN change.
func (s *Service) VerifyUserPIN(pin string) error {
	if err := s.backend.VerifyUserPIN(pin); err != nil {
		return err
	}
	s.pushFIDO2Hash(pin)
	return nil
}

// IsInitialized reports whether the backend has completed setup.
func (s *Service) IsInitialized() bool { return s.backend.IsInitialized() }

// IsPINSet reports whether the user PIN has been configured. Alias for
// UserPINSet kept for caller ergonomics.
func (s *Service) IsPINSet() bool { return s.backend.UserPINSet() }

// SOPINSet reports whether the SO PIN has been configured.
func (s *Service) SOPINSet() bool { return s.backend.SOPINSet() }

// UserPINSet reports whether the user PIN has been configured.
func (s *Service) UserPINSet() bool { return s.backend.UserPINSet() }

// GetLockoutStatus returns the backend's native lockout state.
func (s *Service) GetLockoutStatus() *LockoutStatus { return s.backend.GetLockoutStatus() }

// ResetLockout clears the backend's lockout counters using SO PIN
// authorization.
func (s *Service) ResetLockout(soPIN string) error { return s.backend.ResetLockout(soPIN) }

// VerifyFIDO2Hash verifies a CTAP2 PIN hash via the backend's optional
// FIDO2HashVerifier capability. Returns false when the hash length is
// wrong or the backend does not implement the verifier interface.
func (s *Service) VerifyFIDO2Hash(hash []byte) bool {
	if len(hash) != FIDO2PINHashSize {
		return false
	}
	if v, ok := s.backend.(FIDO2HashVerifier); ok {
		return v.VerifyFIDO2Hash(hash)
	}
	return false
}

// ComputeFIDO2PINHash returns SHA-256(pin)[:16] as required by CTAP2.
// Callers using the result for in-memory operations should treat it as
// secret and zero it after use.
func ComputeFIDO2PINHash(pin string) []byte {
	digest := sha256.Sum256([]byte(pin))
	out := make([]byte, FIDO2PINHashSize)
	copy(out, digest[:FIDO2PINHashSize])
	return out
}

// VerifyFIDO2PINHash compares two CTAP2 PIN hashes in constant time.
// Returns false on length mismatch.
func VerifyFIDO2PINHash(expected, actual []byte) bool {
	if len(expected) != FIDO2PINHashSize || len(actual) != FIDO2PINHashSize {
		return false
	}
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

// pushFIDO2Hash computes the FIDO2 hash from pin and invokes the
// registered setter (when present). The hash is zeroed before return.
func (s *Service) pushFIDO2Hash(pin string) {
	setter := s.fido2HashSetter.Load()
	if setter == nil {
		return
	}
	hash := ComputeFIDO2PINHash(pin)
	defer zeroBytes(hash)
	(*setter)(hash)
	s.log.Debug("pushed FIDO2 PIN hash to authenticator")
}
