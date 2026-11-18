// Copyright 2026 Jeremy Hahn
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
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// StrictSOGrants returns the strict-only SO grants (permission.StrictSOGrants)
// that roleIDs, all of tenantID, earn under posture strict: all of them when
// roleIDs hold tenantID's built-in SO by its canonical ID (SystemRoleID), none
// otherwise. A custom role carrying the bare ID "so" earns nothing.
//
// It is the one rule both Authorize (which extends these grants) and
// ListOwnPermissions (which lists them) apply, so the listing matches what
// the authorizer grants under each posture.
func StrictSOGrants(strict bool, roleIDs []string, tenantID string) []permission.Permission {
	if !strict {
		return nil
	}
	if !slices.Contains(roleIDs, SystemRoleID(tenantID, RoleNameSO)) {
		return nil
	}
	return slices.Clone(permission.StrictSOGrants)
}
