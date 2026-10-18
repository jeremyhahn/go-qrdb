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

package middleware

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// InvalidAuthorizeTargetError reports an Authorize request naming an
// identity without the subject and tenant that identify it.
type InvalidAuthorizeTargetError struct{}

// Error implements error.
func (e *InvalidAuthorizeTargetError) Error() string {
	return "middleware: authorize target identity needs a subject and a tenant"
}

// EvaluateAuthorization answers, through the server's configured auth chain,
// whether target may perform action on resource in the tenant the request
// targets. It is the one implementation behind the Authorize endpoint on
// every transport.
//
// The two results are distinct. err refuses the question itself: an
// unauthenticated caller, a malformed target, or a caller asking about
// another identity without permission.PermissionsResolve in that identity's
// tenant -- the permission that already lets a caller resolve a role set's
// effective permissions, which is all an answer about another identity
// discloses. decision is the answer: nil when target is permitted, the
// authorizer's typed denial otherwise.
//
// A nil target, or one naming the caller's own subject and tenant, asks about
// the caller and needs authentication only (permission.IdentityGated); the
// caller's authenticated identity is evaluated, never the roles the request
// supplies. Another identity is evaluated as supplied, the same contract as
// the embedded RBACClient.Authorize.
func (m *AuthMiddleware) EvaluateAuthorization(ctx context.Context, target *auth.Identity, resource, action string) (decision, err error) {
	caller := auth.IdentityFromContext(ctx)
	if m == nil || caller == nil || caller.Subject == "" {
		return nil, auth.ErrUnauthenticated
	}
	if target == nil || (target.Subject == caller.Subject && target.TenantID == caller.TenantID) {
		return m.authorizer.Authorize(ctx, caller, resource, action), nil
	}
	if target.Subject == "" || target.TenantID == "" {
		return nil, &InvalidAuthorizeTargetError{}
	}
	if err := m.AuthorizeTenant(ctx, target.TenantID, permission.PermissionsResolve); err != nil {
		return nil, err
	}
	return m.authorizer.Authorize(ctx, target, resource, action), nil
}
