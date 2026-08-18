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

package role

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// System role identifiers moved from RFC 4122 v5 (name-based, SHA-1) to
// RFC 9562 §5.8 v8 (name-based, SHA-256) because SHA-1 does not exist under
// FIPS 140-3: a binary built with GODEBUG=fips140=only panics the moment it
// is called, so the old derivation could not even run, let alone be argued
// as an innocent use.
//
// The identifiers it produced are persisted data — they are the role
// records' primary keys, both secondary indexes, and the contents of every
// user record's RoleIDs — so the change is versioned rather than a flag
// day: writes emit only the v8 form, and reads accept both. Reads by
// identifier need nothing special, since a stored identifier is looked up
// verbatim. What does need care is the reverse direction, where code holds
// a tenant and a role NAME and wants the identifier: re-deriving there
// yields the v8 identifier, which on an already-seeded cluster names no
// record at all. The two functions below are that direction, and they
// answer from the store's name index rather than from a hash.

// NameResolver is the narrow name-lookup surface the two functions below
// need. RoleServicer satisfies it, as does any downstream adapter over a
// role store that can answer "which role is named N in tenant T".
type NameResolver interface {
	// GetRoleByName retrieves a role by tenant-scoped name.
	GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error)
}

// ResolveSystemRoleID returns the identifier under which the named system
// role is persisted in tenantID, falling back to the canonical identifier
// from SystemRoleID when the cluster holds no role of that name yet.
//
// Grants must be recorded against the resolved identifier, never against
// the canonical one directly: role seeding is name-unique and tolerates
// already-exists, so a cluster seeded before the move off UUIDv5 keeps its
// system roles under their legacy identifiers forever. Writing a canonical
// identifier into such a cluster's user records would produce a grant that
// resolves to no role, silently stripping the user's permissions.
//
// A nil svc yields the canonical identifier — the caller has no store to
// consult, so there is no persisted identifier to prefer. Any store error
// other than "no such role" is returned; callers must treat it as a failure
// to resolve rather than as an absent role.
func ResolveSystemRoleID(ctx context.Context, svc NameResolver, tenantID, roleName string) (string, error) {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}
	canonical := deterministicUUID(tenantID, roleName)
	if svc == nil {
		return canonical, nil
	}

	r, err := svc.GetRoleByName(ctx, tenantID, roleName)
	if err != nil {
		var notFound *RoleNotFoundError
		if errors.As(err, &notFound) {
			return canonical, nil
		}
		return "", err
	}
	// A nil role with a nil error breaks the RoleServicer contract, but the
	// resolver is reached from the authorization path, where a nil
	// dereference would be an outage. Read it as "no such role".
	if r == nil {
		return canonical, nil
	}
	return r.ID, nil
}

// SystemRoleIDs returns every identifier that denotes the named system role
// in tenantID: the canonical one from SystemRoleID, plus the persisted one
// when a legacy seeding left them different.
//
// Authorization rules that recognise a role by identifier — the SO
// deny-list and the strict-separation-of-duties Owner strip — must match
// against the whole set. Matching the canonical identifier alone would let
// a grant made before the migration escape a control that exists to void
// it, which is the one direction of this change that is not merely a
// compatibility concern.
//
// The canonical identifier is always first, so the result is never empty.
func SystemRoleIDs(ctx context.Context, svc NameResolver, tenantID, roleName string) ([]string, error) {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}
	canonical := deterministicUUID(tenantID, roleName)

	persisted, err := ResolveSystemRoleID(ctx, svc, tenantID, roleName)
	if err != nil {
		return nil, err
	}
	if persisted == canonical {
		return []string{canonical}, nil
	}
	return []string{canonical, persisted}, nil
}
