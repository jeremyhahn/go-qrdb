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

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// CertTargetGuard decides whether the caller in ctx may bind a key to target,
// an existing account of tenantID. service.NewCertTargetGuard is the
// production one -- the same guard certs:issue and certs:enroll apply -- so
// every verb that binds a key holds one rule: no Security Officer target
// (their keys come only from the ceremony), and an Owner target only for an
// Owner.
type CertTargetGuard interface {
	AuthorizeCertTarget(ctx context.Context, tenantID string, target *user.User) error
}

// WithCertTargetGuard configures the guard AuthorizeCertBindingTarget
// applies. Without it a binding write is refused: the rule cannot be
// evaluated, and it fails closed.
func WithCertTargetGuard(g CertTargetGuard) Option {
	return func(m *AuthMiddleware) {
		m.certTargets = g
	}
}

// CertTargetGuardRequiredError refuses a users:add_cert_binding write on a
// middleware built without a CertTargetGuard.
type CertTargetGuardRequiredError struct{}

// Error describes the refusal.
func (e *CertTargetGuardRequiredError) Error() string {
	return "auth/middleware: binding a key to an account requires a certificate target guard, and none is configured"
}

// AuthorizeCertBindingTarget gates users:add_cert_binding on the account the
// write targets, read from its record: binding a thumbprint is binding a key,
// so it is held to the certificate target guard exactly as certs:issue and
// certs:enroll are. A nil m enforces nothing, as every AuthMiddleware gate
// does when no RBAC is configured.
func (m *AuthMiddleware) AuthorizeCertBindingTarget(ctx context.Context, userSvc user.UserServicer, userID uint64) error {
	if m == nil {
		return nil
	}
	if m.certTargets == nil {
		return &CertTargetGuardRequiredError{}
	}
	target, err := userSvc.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if target == nil {
		return &user.UserNotFoundError{ID: userID}
	}
	return m.certTargets.AuthorizeCertTarget(ctx, target.TenantID, target)
}
