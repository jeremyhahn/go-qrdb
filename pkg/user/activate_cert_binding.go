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

package user

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// Compile-time interface check.
var _ CertActivator = (*kvUserStore)(nil)

// ActivateWithCertBinding implements CertActivator. It is invitation
// redemption's one write: a pending account gets its first certificate
// binding and moves to active in a SINGLE conditional commit, so a reader
// never observes an account that is active but unbound, or bound but still
// pending.
//
// Unlike AddCertBinding, which may re-bind or refresh an existing account's
// keys at any time, this method accepts exactly one caller: a PENDING account
// holding no certificate binding yet. Every other starting state is refused
// with *AccountNotPendingError and nothing is written — including the race
// where two redemptions of the same invitation reach here concurrently; see
// mutateCertBindings for the identical retry-on-lost-commit shape this
// mirrors.
func (s *kvUserStore) ActivateWithCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*User, error) {
	if thumbprint == "" {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must not be empty"}
	}
	// Only canonical SPKI thumbprints (base64url of 32 bytes) may be stored,
	// matching AddCertBinding's own validation.
	if !IsValidSPKIThumbprint(thumbprint) {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must be a base64url-encoded SHA-256 SPKI thumbprint"}
	}
	if notAfterUnix > 0 {
		if notAfter := time.Unix(notAfterUnix, 0).UTC(); !time.Now().Before(notAfter) {
			return nil, &CertBindingExpiredError{Thumbprint: thumbprint, NotAfter: notAfter}
		}
	}
	if _, ok := s.store.(kvstore.AtomicKVStore); !ok {
		return nil, &StoreError{Op: "activate_with_cert_binding", Detail: "conditional commit", Err: ErrStoreNotAtomic}
	}
	if userID == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	var lastConflict error
	for attempt := 0; attempt < certBindingMutationAttempts; attempt++ {
		base, err := s.store.Get(ctx, primaryKey(userID))
		if err != nil {
			return nil, &StoreError{Op: "activate_with_cert_binding", Detail: "primary lookup", Err: err}
		}
		if base == nil {
			return nil, &UserNotFoundError{ID: userID}
		}
		existing := &User{}
		if uerr := json.Unmarshal(base, existing); uerr != nil {
			return nil, &StoreError{Op: "activate_with_cert_binding", Detail: "unmarshal", Err: uerr}
		}
		if existing.Lifecycle != LifecyclePending {
			return nil, &AccountNotPendingError{ID: existing.ID, Lifecycle: existing.Lifecycle}
		}
		if len(existing.CertBindings) > 0 {
			return nil, &AccountNotPendingError{ID: existing.ID, Lifecycle: existing.Lifecycle, HasCredential: true}
		}

		// A thumbprint resolves to exactly one identity within a tenant, the
		// same conflict rule AddCertBinding enforces: binding a key already
		// bound to a different account in the tenant is refused rather than
		// silently stealing it — which is also how an inviter's OWN key gets
		// refused when presented at redemption.
		existingID, err := s.store.Get(ctx, certThumbprintIndexKey(existing.TenantID, thumbprint))
		if err != nil {
			return nil, &StoreError{Op: "activate_with_cert_binding", Detail: "index lookup", Err: err}
		}
		if existingID != nil {
			boundID, decErr := decodeID(existingID)
			if decErr != nil {
				return nil, &StoreError{Op: "activate_with_cert_binding", Detail: "decode index value", Err: decErr}
			}
			if boundID != userID {
				return nil, &CertBindingConflictError{
					Thumbprint:   thumbprint,
					BoundUserID:  boundID,
					TargetUserID: userID,
				}
			}
		}

		b := CertBinding{Thumbprint: thumbprint, Kind: CertBindingSPKIThumbprint}
		if notAfterUnix > 0 {
			b.NotAfter = time.Unix(notAfterUnix, 0).UTC()
		}
		// existing is a local decode of the committed record; mutating it
		// writes nothing until updateUserAt's conditional commit below.
		existing.AddCertBinding(b)

		updated, uerr := s.updateUserAt(ctx, &UpdateUserRequest{
			ID:           userID,
			CertBindings: existing.CertBindings,
			Lifecycle:    LifecycleActive,
		}, base)
		if uerr == nil {
			s.emitAudit(ctx, "activate_with_cert_binding", userID, map[string]any{
				"username":   updated.Username,
				"tenantID":   updated.TenantID,
				"thumbprint": thumbprint,
				"expires":    notAfterUnix > 0,
			})
			return updated, nil
		}
		var lost *ConcurrentUpdateError
		if !errors.As(uerr, &lost) {
			return nil, uerr
		}
		lastConflict = uerr
	}

	// Exhausted the bound. Report the conflict rather than pretending: a
	// redemption that did not commit must never be told it activated the
	// account.
	return nil, lastConflict
}
