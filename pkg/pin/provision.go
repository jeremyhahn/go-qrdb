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
	"context"
	"errors"
)

// ErrBackendNotConfigured is returned when an operation needs the SO or User
// PIN and no PINBackend has been injected. It fails closed: there is no
// in-memory or plaintext fallback.
var ErrBackendNotConfigured = errors.New("pin: no PIN backend configured; PIN-gated operations are refused")

// Provision is the single path that establishes a node's SO and User PINs.
// Barrier initialization and the initialization ceremony both call it, so
// whichever runs first sets the PINs and the other, supplying the same PINs,
// verifies them instead of failing on "already set". A PIN that is already
// set and does not match is refused with the backend's own error — Provision
// never overwrites a PIN. The SO PIN comparison counts against the lockout
// counter of the caller in ctx (ForCaller).
//
// Empty soPIN and userPIN is a no-op (nothing to provision). A userPIN
// without a soPIN is refused with ErrSOPINRequired, because the User PIN is
// set under SO PIN authorization. PINs are compared by the backend (hashed,
// constant-time for the software backend), never with ==.
func Provision(ctx context.Context, backend PINBackend, soPIN, userPIN string) error {
	if soPIN == "" && userPIN == "" {
		return nil
	}
	if backend == nil {
		return ErrBackendNotConfigured
	}
	backend = ForCaller(ctx, backend)
	if soPIN == "" {
		return ErrSOPINRequired
	}
	if backend.SOPINSet() {
		if err := backend.VerifySOPIN(soPIN); err != nil {
			return err
		}
	} else if err := backend.SetSOPIN("", soPIN); err != nil {
		return err
	}
	if userPIN == "" {
		return nil
	}
	if backend.UserPINSet() {
		return backend.VerifyUserPIN(userPIN)
	}
	return backend.SetUserPIN(soPIN, userPIN)
}

// ProvisionRollback is an OPTIONAL capability a PINBackend may implement to
// undo a Provision call whose caller failed LATER in the same logical
// transaction (go-qrdb#560 review B3): ceremony Initialize provisions the
// SO/User PINs before enrolling M-of-N officers, and a failure partway
// through that enrollment -- a CA signing fault, a persist I/O error --
// must not leave the PINs provisioned while the officers that would have
// made the ceremony usable are rolled back, since Provision's own
// idempotent re-verify would then force a corrected retry to supply the
// SAME PINs rather than genuinely new ones.
//
// Reached by type assertion, exactly like FIDO2HashVerifier above: not
// every backend can support it. *SoftwareBackend un-sets its in-memory
// state so a retry within the SAME process provisions cleanly; a hardware
// backend (TPM2, PKCS#11) that has already committed an irreversible
// native provisioning step must not claim to -- RollbackProvision's
// absence (no type assertion match) is the correct answer for one, not an
// error, and ceremony.Initialize's rollback is best-effort for exactly
// that reason.
type ProvisionRollback interface {
	// RollbackProvision reverts a Provision call that has not been acted on
	// by anything outside the caller's own failed transaction. It is always
	// safe to call when nothing was ever provisioned (a no-op).
	RollbackProvision() error
}

// RequireProvisionedSOPIN gates an operation that is meaningful only once the
// node has an SO PIN -- a rekey, a holder revocation -- behind it. Unlike
// RequireSOPIN it does not admit the call when no SO PIN has been
// provisioned: it refuses with ErrPINNotSet, since there is nothing to check
// the presented PIN against. With no backend it fails closed with
// ErrBackendNotConfigured. The comparison counts against the lockout counter
// of the caller in ctx (ForCaller).
func RequireProvisionedSOPIN(ctx context.Context, backend PINBackend, soPIN string) error {
	if backend == nil {
		return ErrBackendNotConfigured
	}
	backend = ForCaller(ctx, backend)
	if !backend.SOPINSet() {
		return ErrPINNotSet
	}
	return backend.VerifySOPIN(soPIN)
}

// RequireSOPIN gates an administrative operation behind SO PIN
// authorization. With no backend it fails closed. With a backend on which no
// SO PIN has been provisioned it admits the call: the node has no PIN to
// check yet, and provisioning one is the first administrative act. The
// comparison counts against the lockout counter of the caller in ctx
// (ForCaller).
func RequireSOPIN(ctx context.Context, backend PINBackend, soPIN string) error {
	if backend == nil {
		return ErrBackendNotConfigured
	}
	backend = ForCaller(ctx, backend)
	if !backend.SOPINSet() {
		return nil
	}
	return backend.VerifySOPIN(soPIN)
}
