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

// Package middleware provides authentication and authorization middleware
// for gRPC interceptors and REST HTTP handlers. It chains pluggable
// authenticators, enriches identities from the user store, and enforces
// permission-based authorization using map-based dispatch.
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// AuthMiddleware chains authenticators and an authorizer to enforce
// access control on gRPC and REST endpoints. It optionally enriches
// identities from the user store and logs authorization decisions to
// an audit logger.
type AuthMiddleware struct {
	authenticators []auth.Authenticator
	authorizer     auth.Authorizer
	users          user.UserServicer
	usersFallback  user.UserServicer
	audit          audit.Logger
}

// authLookupSyncTimeout bounds the per-request linearizable user lookup. A
// healthy ReadIndex answers far faster; on a transient consensus slowdown the
// lookup fails over to the StaleRead fallback within this budget rather than
// stalling the request. Kept sub-second so a brief blip cannot cascade into
// multi-second per-request latency.
const authLookupSyncTimeout = 500 * time.Millisecond

// Option configures an AuthMiddleware.
type Option func(*AuthMiddleware)

// New creates a new AuthMiddleware with the given authorizer and options.
// The authorizer is required; authenticators are added via
// WithAuthenticator options.
func New(authorizer auth.Authorizer, opts ...Option) *AuthMiddleware {
	m := &AuthMiddleware{
		authorizer: authorizer,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// WithAuthenticator appends an authenticator to the chain. Authenticators
// are tried in the order they are added; the first to succeed provides
// the identity.
func WithAuthenticator(a auth.Authenticator) Option {
	return func(m *AuthMiddleware) {
		m.authenticators = append(m.authenticators, a)
	}
}

// WithUserService configures user-store enrichment. When set, the
// middleware looks up the authenticated subject in the user store to
// enrich the identity with persistent role assignments and to enforce
// the user-enabled check.
func WithUserService(u user.UserServicer) Option {
	return func(m *AuthMiddleware) {
		m.users = u
	}
}

// WithUserServiceFallback configures a fallback user-store for when the
// primary store is unavailable (e.g. Raft ReadIndex not yet operational
// after cluster restart). The fallback typically uses StaleRead which
// reads from the local state machine directly. This is safe because
// bootstrap ensures user/role data is replicated before servers start.
func WithUserServiceFallback(u user.UserServicer) Option {
	return func(m *AuthMiddleware) {
		m.usersFallback = u
	}
}

// WithAuditLogger configures audit logging for authorization decisions.
// When set, every authorize call is recorded with its outcome.
func WithAuditLogger(a audit.Logger) Option {
	return func(m *AuthMiddleware) {
		m.audit = a
	}
}

// authenticate runs the authenticator chain and returns an enriched
// identity stored in the returned context. It tries each authenticator
// in order; the first to succeed provides the base identity.
//
// When a UserServicer is configured, the identity is enriched with the
// user's persistent RoleIDs. If the user is disabled, a
// UserDisabledError is returned. If the user is not found in the store,
// the base identity is used without enrichment.
func (m *AuthMiddleware) authenticate(ctx context.Context) (context.Context, *auth.Identity, error) {
	if len(m.authenticators) == 0 {
		return ctx, nil, auth.ErrUnauthenticated
	}

	var lastErr error
	var identity *auth.Identity

	for _, authn := range m.authenticators {
		id, err := authn.Authenticate(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		identity = id
		break
	}

	if identity == nil {
		// All authenticators failed. If the last authenticator returned
		// an AuthError (e.g., ErrUnauthenticated), return it directly
		// to preserve the original error type.
		var authErr *auth.AuthError
		if errors.As(lastErr, &authErr) {
			return ctx, nil, lastErr
		}
		return ctx, nil, &AuthChainError{Err: lastErr}
	}

	// Enrich identity from user store when configured.
	if m.users != nil {
		u, err := m.lookupUser(ctx, identity.TenantID, identity.Subject)
		if err != nil {
			// If user not found, proceed with base identity (no enrichment).
			var unf *user.UserNotFoundError
			if !errors.As(err, &unf) {
				// Transient store error (Raft not ready, timeout, etc).
				// Return 503/UNAVAILABLE so clients retry rather than
				// proceeding without roles (which would cause false 403).
				slog.Warn("auth/middleware: user store unavailable",
					"subject", identity.Subject,
					"tenant", identity.TenantID,
					"error", err,
				)
				return ctx, nil, &StoreUnavailableError{Err: err}
			}
		} else {
			// User found; check enabled status.
			if !u.Enabled {
				return ctx, nil, &UserDisabledError{
					Username: u.Username,
					TenantID: u.TenantID,
				}
			}
			// Enrich identity with persistent role assignments.
			identity.Roles = u.RoleIDs
		}
	}

	ctx = auth.WithIdentity(ctx, identity)

	// Bind the canonical tenant context from the authenticated identity so
	// that tenant.FromContext(ctx) returns the authenticated tenant for every
	// downstream service on every transport. Fall back to SystemTenantID for
	// system/admin identities where TenantID is empty (no regression).
	tenantID := identity.TenantID
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}
	ctx = tenant.WithTenantID(ctx, tenantID)

	return ctx, identity, nil
}

// lookupUser looks up a user by username, first trying the primary user
// store (SyncRead for linearizable reads), then falling back to the
// optional fallback store (StaleRead from local state machine) when the
// primary fails with a transient error. The fallback is safe because
// bootstrap ensures user/role data is replicated before servers start.
func (m *AuthMiddleware) lookupUser(ctx context.Context, tenantID, subject string) (*user.User, error) {
	// Bound the linearizable (SyncRead) lookup tightly so a transient
	// ReadIndex slowdown fails over to the StaleRead fallback in well under a
	// second instead of stalling every authenticated request for seconds. A
	// healthy ReadIndex answers in microseconds; the only cost of a short
	// budget is a marginally-staler read on a blip, which is safe for
	// auth (user/role data is replicated before servers start and changes
	// rarely). This is the key guard against a brief consensus blip
	// cascading into per-request latency that trips client/connect timeouts.
	lookupCtx, cancel := context.WithTimeout(ctx, authLookupSyncTimeout)
	u, err := m.users.GetUserByUsername(lookupCtx, tenantID, subject)
	cancel()
	if err == nil {
		return u, nil
	}

	// UserNotFoundError is authoritative — don't fall back.
	var unf *user.UserNotFoundError
	if errors.As(err, &unf) {
		return nil, err
	}

	// Primary failed with a transient error; try fallback.
	if m.usersFallback != nil {
		fallbackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		u, ferr := m.usersFallback.GetUserByUsername(fallbackCtx, tenantID, subject)
		cancel()
		if ferr == nil {
			slog.Debug("auth/middleware: used fallback user store",
				"subject", subject, "tenant", tenantID)
			return u, nil
		}
		// If fallback also returns UserNotFoundError, use that.
		if errors.As(ferr, &unf) {
			return nil, ferr
		}
	}

	// Both primary and fallback failed (or no fallback configured).
	return nil, err
}

// authorize checks whether the identity is permitted to perform the
// action on the resource, and optionally records the decision in the
// audit log.
func (m *AuthMiddleware) authorize(ctx context.Context, identity *auth.Identity, perm permission.Permission) error {
	resource := permission.Resource(perm)
	action := permission.Action(perm)

	err := m.authorizer.Authorize(ctx, identity, resource, action)

	// Audit the decision when a logger is configured.
	if m.audit != nil {
		result := audit.ResultSuccess
		if err != nil {
			result = audit.ResultDenied
		}

		entry := &audit.Entry{
			Timestamp: time.Now().UTC(),
			Subject:   identity.Subject,
			TenantID:  identity.TenantID,
			Action:    "auth.authorize",
			Resource:  perm.String(),
			Result:    result,
		}

		if auditErr := m.audit.Log(ctx, entry); auditErr != nil {
			slog.Error("auth/middleware: audit log failed",
				"error", auditErr,
				"subject", identity.Subject,
				"permission", perm.String(),
			)
		}
	}

	return err
}

// AuthorizeTenant authorizes the authenticated identity in ctx to perform a
// tenant-scoped operation against an explicit target tenant. It is intended
// for handlers where the true target tenant is carried in the request body
// or URL (for example the per-tenant barrier {id}) rather than implied by
// the identity's own tenant.
//
// The identity is read from the context (it must have been authenticated by
// the surrounding middleware). The target tenant is injected into the
// context so the RBAC authorizer's scope rule evaluates against the request
// target, not the ambient tenant. For a ScopeTenantOnly permission this
// denies the system tenant and any cross-tenant access; the decision is
// audited like any other authorize call.
//
// Returns auth.ErrUnauthenticated when no identity is present in ctx.
func (m *AuthMiddleware) AuthorizeTenant(ctx context.Context, targetTenantID string, perm permission.Permission) error {
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return auth.ErrUnauthenticated
	}
	ctx = tenant.WithTenantID(ctx, targetTenantID)
	return m.authorize(ctx, identity, perm)
}
