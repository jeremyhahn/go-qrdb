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

package authtest

import (
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// GrantAuthorizer is an auth.Authorizer that permits an identity exactly the
// "resource:action" pairs granted to its subject, and only in its own tenant:
// the confinement the RBAC authorizer applies to a tenant-scoped grant. It
// records every subject it evaluated, so a test can prove an identity was
// never consulted.
type GrantAuthorizer struct {
	mu        sync.Mutex
	grants    map[string]map[string]bool
	evaluated []string
}

var _ auth.Authorizer = (*GrantAuthorizer)(nil)

// NewGrantAuthorizer returns a GrantAuthorizer granting each subject the
// listed "resource:action" permissions.
func NewGrantAuthorizer(grants map[string][]string) *GrantAuthorizer {
	g := &GrantAuthorizer{grants: make(map[string]map[string]bool, len(grants))}
	for subject, perms := range grants {
		set := make(map[string]bool, len(perms))
		for _, p := range perms {
			set[p] = true
		}
		g.grants[subject] = set
	}
	return g
}

// Authorize implements auth.Authorizer.
func (g *GrantAuthorizer) Authorize(ctx context.Context, id *auth.Identity, resource, action string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.evaluated = append(g.evaluated, id.Subject)
	if tenant.FromContext(ctx) != id.TenantID {
		return auth.ErrUnauthorized
	}
	if g.grants[id.Subject][resource+":"+action] {
		return nil
	}
	return auth.ErrUnauthorized
}

// Evaluated reports whether subject was ever evaluated.
func (g *GrantAuthorizer) Evaluated(subject string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.evaluated {
		if s == subject {
			return true
		}
	}
	return false
}
