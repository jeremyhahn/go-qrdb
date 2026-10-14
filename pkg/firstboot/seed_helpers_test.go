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

package firstboot

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// builtInRoleAlreadySeeded is BuiltInRoleAlreadySeededWithin's single lookup,
// without the retry: it fails closed on a nil resolver, a lookup error or a nil
// result, and otherwise returns RoleMatchesSeed's verdict. Production retries
// the lookup; the tests use this to assert on one lookup without waiting out
// BuiltInRoleSeedRetryWindow.
func builtInRoleAlreadySeeded(ctx context.Context, resolver role.NameResolver, tenantID string, want *role.Role) bool {
	if resolver == nil {
		return false
	}
	existing, err := resolver.GetRoleByName(ctx, tenantID, want.Name)
	if err != nil || existing == nil {
		return false
	}
	return RoleMatchesSeed(existing, want)
}
