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
)

// legacyDefaultTenantID is the legacy tenant id retired in Phase 0 of
// the identity refactor. It is permanently reserved so a future caller
// (or a reverted config) cannot re-create a tenant under the old name.
const legacyDefaultTenantID = "default"

// validateReservedTenantID rejects tenant ids that the platform reserves
// for its own use. The check is bypassed when the context is marked as an
// internal call via WithInternalCall — that path is reserved for first-boot
// bootstrap of the system tenant. Returns a typed error when the id is
// rejected.
//
// Reserved ids:
//   - SystemTenantID ("system") — structurally privileged platform tenant.
//   - legacyDefaultTenantID ("default") — retired; permanently reserved.
//   - any id starting with "_" — reserved for internal future use.
//   - any id containing ":" — reserved for compound-id schemes.
//
// Additionally, any id containing a control character (byte < 0x20) or the
// unit-separator byte (0x1f) used as the scoped-key separator is rejected with
// an *InvalidTenantIDCharError regardless of the internal-call bypass, as is
// any id containing a path separator ('/' or '\') or equal to "." or ".." —
// tenant ids become filesystem path segments under barrier isolation
// (volume-pool directories), so a path-traversal id like "../../x" must be
// rejected before it ever reaches a filepath.Join sink.
func validateReservedTenantID(ctx context.Context, id string) error {
	// Charset check applies to ALL callers — even internal calls must not
	// create a tenant whose id would corrupt scoped storage keys or escape
	// its volume-pool directory.
	for _, b := range []byte(id) {
		if b < 0x20 || b == '/' || b == '\\' {
			return &InvalidTenantIDCharError{TenantID: id, Byte: b}
		}
	}
	if id == "." || id == ".." {
		return &InvalidTenantIDError{
			TenantID: id,
			Reason:   "id must not be a relative path segment",
		}
	}

	if isInternalCall(ctx) {
		return nil
	}
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
