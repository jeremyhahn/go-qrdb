// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tenant

import (
	"context"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// legacyDefaultTenantID is the legacy tenant id retired in Phase 0 of
// the identity refactor. It is permanently reserved so a future caller
// (or a reverted config) cannot re-create a tenant under the old name.
const legacyDefaultTenantID = "default"

// validateTenantIDCharset rejects an id that would corrupt a scoped storage
// key or escape a directory.
//
// It is shared by tenant creation, tenant deletion and the store resolver,
// which builds a keyspace prefix from an id it may never have seen — an
// unknown tenant deliberately falls through to the shared store. A '/' there
// would nest one tenant's namespace INSIDE another's ("acme/system/system/"
// sits under "acme/system/"), so a scan by the outer tenant would list the
// inner one's keys. The check therefore has to run where the prefix is built,
// not only where tenants are created.
func validateTenantIDCharset(id string) error {
	for _, b := range []byte(id) {
		if b < 0x20 || b == '/' || b == '\\' {
			return &InvalidTenantIDCharError{TenantID: id, Byte: b}
		}
	}
	// A segment that begins with a reserved key prefix is refused as well.
	// Confinement means it can no longer ESCAPE — a namespace of "__sys:/db/"
	// reaches nothing another tenant owns — but it would still bury the
	// tenant's own records under a prefix the reserved-key discovery guard
	// filters out of user-facing enumeration, so the tenant would silently
	// fail to list its own data. Refusing the id is cheaper than explaining
	// that.
	if strings.HasPrefix(id, kvstore.ReservedPrefixSystem) {
		return &InvalidTenantIDError{
			TenantID: id,
			Reason:   "id must not begin with the reserved key prefix " + kvstore.ReservedPrefixSystem,
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The reserved set has ONE declaration and TWO policies
//
// reservedTenantIDReason below is the only place the reserved set is written
// down. Two call sites consult it and they differ in exactly one respect,
// which is a difference in POLICY rather than a second copy of the rule:
//
//   - validateReservedTenantID (CREATE) honours the first-boot bypass.
//     Bootstrap has to be able to create the system tenant, so a context
//     marked WithInternalCall skips the set.
//
//   - validateDeletableTenantID (DELETE) does NOT. Nothing in this system
//     legitimately deletes a reserved tenant, at boot or afterwards, and a
//     delete is irreversible: it retires the id, removes the metadata record and
//     destroys the barrier and keyspace. A bypass on that path would be a bypass of
//     the only guard standing between a caller and the platform's own tenant.
//
// Writing the set twice is what would let the two drift; giving one set two
// bypass policies is the actual requirement. (go-qrdb#413 review.)
// ---------------------------------------------------------------------------

// reservedTenantIDReason returns the reason the platform reserves id, or nil
// when the id is free. It is THE declaration of the reserved set:
//
//   - SystemTenantID ("system") — structurally privileged platform tenant.
//   - legacyDefaultTenantID ("default") — retired; permanently reserved.
//   - any id starting with "_" — reserved for internal future use.
//   - any id containing ":" — reserved for compound-id schemes.
func reservedTenantIDReason(id string) error {
	switch {
	case id == SystemTenantID:
		return &ReservedTenantIDError{
			TenantID: id,
			Reason:   "system tenant id is reserved for the platform",
		}
	case id == legacyDefaultTenantID:
		return &ReservedTenantIDError{
			TenantID: id,
			Reason:   "legacy default tenant id is permanently reserved",
		}
	case strings.HasPrefix(id, "_"):
		return &ReservedTenantIDError{
			TenantID: id,
			Reason:   "ids starting with underscore are reserved for internal use",
		}
	case strings.Contains(id, ":"):
		return &ReservedTenantIDError{
			TenantID: id,
			Reason:   "ids containing colon are reserved for compound-id schemes",
		}
	}
	return nil
}

// ValidateTenantIDShape rejects an id whose SHAPE is unusable, independently
// of whether it is reserved: one that would corrupt a scoped storage key, or
// that names a relative path segment. It binds every caller, internal ones
// included — bootstrap may create the system tenant, but nothing may create a
// tenant whose id escapes its volume-pool directory.
func ValidateTenantIDShape(id string) error {
	if err := validateTenantIDCharset(id); err != nil {
		return err
	}
	if id == "." || id == ".." {
		return &InvalidTenantIDError{
			TenantID: id,
			Reason:   "id must not be a relative path segment",
		}
	}
	return nil
}

// validateReservedTenantID rejects tenant ids that the platform reserves for
// its own use, on the CREATE path.
//
// The check is bypassed when the context is marked as an internal call via
// WithInternalCall — that path is reserved for first-boot bootstrap of the
// system tenant. The shape check above is NOT bypassed. Returns a typed error
// when the id is rejected.
func validateReservedTenantID(ctx context.Context, id string) error {
	if err := ValidateTenantIDShape(id); err != nil {
		return err
	}
	if isInternalCall(ctx) {
		return nil
	}
	return reservedTenantIDReason(id)
}

// validateDeletableTenantID rejects tenant ids that the platform reserves, on
// the DELETE path, with NO internal-call bypass.
//
// It exists because the reserved-id rule was applied only at creation, so the
// ids this file declares un-creatable were freely DELETABLE: DeleteTenant took
// a caller-supplied id and ran the irreversible teardown — shred the DEK,
// write the tombstone, remove "__sys:tenants/{id}" — against it, and reported
// success (go-qrdb#413 review).
//
// The reclamation already refused the system tenant (see
// service.TenantKeyPrefixes: "a reclamation that accepted it would empty the
// store it was asked to tidy"), but that guards the LAST step of the teardown
// and the three irreversible control-plane mutations come before it. This is
// the same judgement, hoisted to the front where it can prevent the commits
// rather than only the destructive read.
//
// It deliberately does NOT lean on DeleteTenant's bottom-up database refusal.
// That refusal returns nil when no DatabaseLister is wired, and where one IS
// wired it catches the system tenant only by accident of configuration — it
// happens to own the default "system" database. A guard that depends on a
// database existing is not a guard.
func validateDeletableTenantID(id string) error {
	if err := ValidateTenantIDShape(id); err != nil {
		return err
	}
	return reservedTenantIDReason(id)
}
