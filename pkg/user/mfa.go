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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// FactorLister is the minimal capability needed to compute a user's derived
// Factors metadata and MFA state at READ time from the real enrollment
// records, rather than from a stored, independently-writable field.
//
// pkg/service.UserAuthFactorServicer (backing the AuthFactorService /
// /v1/users/factors API already wired on every transport) satisfies this
// structurally. It is declared HERE, narrowly, rather than imported from
// pkg/service, because pkg/service imports pkg/user; pkg/auth carries no
// such cycle, so the pkg/auth.AuthFactor return type is safe to depend on
// directly.
//
// This is the ONE source of truth: there is no parallel User.Factors field
// to drift from what List reports, by construction.
type FactorLister interface {
	// List returns every factor (active AND revoked) belonging to userID,
	// in registration order.
	List(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error)
}

// FactorView is a user's enrolled-factor state at one moment: the factor
// METADATA every transport projects, and the MFA fields derived from exactly
// that same set. It is computed from ONE List call, so Factors and the MFA
// fields can never describe two different moments.
type FactorView struct {
	// Factors lists the active factors' metadata, in registration order. The
	// factor's opaque Credential/SPKICredential/Metadata never appear here,
	// and so never cross any transport.
	Factors []transport.FactorMetadata

	// MFAEnabled is true iff at least one factor is active. An enrolled
	// factor is a fact about what the account HAS, not a policy statement
	// that MFA is REQUIRED for it.
	MFAEnabled bool

	// MFAMethods lists the distinct active factor type names in
	// first-registration order.
	MFAMethods []string

	// MFAEnrolledAt is the earliest active registration, RFC 3339 UTC, or
	// empty when there is none.
	MFAEnrolledAt string
}

// ResolveFactorView lists userID's factors through lister ONCE and derives
// its FactorView from the active ones.
//
// A nil lister means the auth-factor subsystem is not configured for this
// deployment, so the account has no enrolled factors to report and the zero
// FactorView is correct. A List failure is NOT absence: it is returned as a
// *FactorLookupError, so a factor-store outage fails the read instead of
// reporting an account that has a second factor as one without.
func ResolveFactorView(ctx context.Context, lister FactorLister, userID uint64) (FactorView, error) {
	if lister == nil {
		return FactorView{}, nil
	}
	all, err := lister.List(ctx, userID)
	if err != nil {
		return FactorView{}, &FactorLookupError{UserID: userID, Err: err}
	}
	var view FactorView
	seen := make(map[string]struct{})
	var earliest time.Time
	for _, f := range all {
		if !f.IsActive() {
			continue
		}
		name := string(f.Type)
		meta := transport.FactorMetadata{ID: f.ID, FactorName: name}
		if !f.RegisteredAt.IsZero() {
			meta.CreatedAt = f.RegisteredAt.UTC().Format(time.RFC3339)
		}
		view.Factors = append(view.Factors, meta)
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			view.MFAMethods = append(view.MFAMethods, name)
		}
		if !view.MFAEnabled || f.RegisteredAt.Before(earliest) {
			earliest = f.RegisteredAt
		}
		view.MFAEnabled = true
	}
	if !earliest.IsZero() {
		view.MFAEnrolledAt = earliest.UTC().Format(time.RFC3339)
	}
	return view, nil
}
