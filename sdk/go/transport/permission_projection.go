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

package transport

import "github.com/jeremyhahn/go-qrdb/pkg/permission"

// ScopeFromDomain converts the domain-layer permission.Scope to the SDK
// PermissionScope. Every defined scope maps to its own SDK value; a value
// outside the defined set is an error. This used to answer
// PermissionScopeSelf for anything it did not name, and it did not name
// ScopeTenantOnly — so every tenant-only permission crossed the SDK boundary
// labelled "self", the one scope that the system-tenant rule lifts. The
// projection was a fail-open of exactly the kind go-qrdb#504 removed from
// LookupScope, one layer up.
func ScopeFromDomain(s permission.Scope) (PermissionScope, error) {
	switch s {
	case permission.ScopeSelf:
		return PermissionScopeSelf, nil
	case permission.ScopeCross:
		return PermissionScopeCross, nil
	case permission.ScopeTenantOnly:
		return PermissionScopeTenantOnly, nil
	case permission.ScopeSelfStrict:
		return PermissionScopeSelfStrict, nil
	default:
		return "", &permission.InvalidScopeError{Value: s.String()}
	}
}

// PermissionInfoFromDomain projects a domain permission into the SDK shape,
// resolving the scope from the global registry. A registered permission
// always has a declared scope (the build gate enforces it), so the error is
// unreachable for anything All() returns — it is propagated rather than
// swallowed because an unreachable error that is silently mapped to a scope
// is how the default this replaces came to exist.
func PermissionInfoFromDomain(p permission.Permission) (PermissionInfo, error) {
	scope, err := permission.LookupScope(p)
	if err != nil {
		return PermissionInfo{}, err
	}
	sdkScope, err := ScopeFromDomain(scope)
	if err != nil {
		return PermissionInfo{}, err
	}
	return PermissionInfo{
		Name:     p.String(),
		Resource: permission.Resource(p),
		Action:   permission.Action(p),
		Scope:    sdkScope,
	}, nil
}
