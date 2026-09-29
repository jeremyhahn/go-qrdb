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

import "errors"

// ErrBackendNotConfigured is returned when an operation needs the SO or User
// PIN and no PINBackend has been injected. It fails closed: there is no
// in-memory or plaintext fallback.
var ErrBackendNotConfigured = errors.New("pin: no PIN backend configured; PIN-gated operations are refused")

// Provision is the single path that establishes a node's SO and User PINs.
// Barrier initialization and the initialization ceremony both call it, so
// whichever runs first sets the PINs and the other, supplying the same PINs,
// verifies them instead of failing on "already set". A PIN that is already
// set and does not match is refused with the backend's own error — Provision
// never overwrites a PIN.
//
// Empty soPIN and userPIN is a no-op (nothing to provision). A userPIN
// without a soPIN is refused with ErrSOPINRequired, because the User PIN is
// set under SO PIN authorization. PINs are compared by the backend (hashed,
// constant-time for the software backend), never with ==.
func Provision(backend PINBackend, soPIN, userPIN string) error {
	if soPIN == "" && userPIN == "" {
		return nil
	}
	if backend == nil {
		return ErrBackendNotConfigured
	}
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

// RequireSOPIN gates an administrative operation behind SO PIN
// authorization. With no backend it fails closed. With a backend on which no
// SO PIN has been provisioned it admits the call: the node has no PIN to
// check yet, and provisioning one is the first administrative act.
func RequireSOPIN(backend PINBackend, soPIN string) error {
	if backend == nil {
		return ErrBackendNotConfigured
	}
	if !backend.SOPINSet() {
		return nil
	}
	return backend.VerifySOPIN(soPIN)
}
