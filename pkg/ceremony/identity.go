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

package ceremony

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// maxHolderNameLen bounds a username that becomes a certificate CN and a
// SPIFFE path segment.
const maxHolderNameLen = 64

// validPrincipalName reports whether name can be a certificate CN and one
// SPIFFE path segment: 1..64 characters from [A-Za-z0-9._@-].
func validPrincipalName(name string) bool {
	if name == "" || len(name) > maxHolderNameLen || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '@', r == '-':
		default:
			return false
		}
	}
	return true
}

// holderProfiles maps a holder role onto its certificate profile.
var holderProfiles = map[seal.HolderRole]string{
	seal.RoleSecurityOfficer: auth.ProfileSecurityOfficer,
	seal.RoleCustodian:       auth.ProfileCustodian,
}
