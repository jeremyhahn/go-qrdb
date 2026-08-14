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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// ---------------------------------------------------------------------------
// Auth types
// ---------------------------------------------------------------------------

// Identity represents an authenticated entity carrying its subject, tenant,
// roles, and arbitrary metadata.
type Identity = auth.Identity

// Authenticator validates incoming requests and extracts identity.
type Authenticator = auth.Authenticator

// Authorizer checks whether an identity is allowed to perform an action on a
// resource.
type Authorizer = auth.Authorizer

// NoopAuthorizer permits all actions. Useful for development or single-user
// deployments.
type NoopAuthorizer = auth.NoopAuthorizer

// ---------------------------------------------------------------------------
// Auth context helpers
// ---------------------------------------------------------------------------

// WithIdentity stores an Identity in the context.
var WithIdentity = auth.WithIdentity

// WithTLSState stores the mTLS connection state on the context. It is the ONE
// carrier the stack uses to hand a verified peer certificate from a transport
// down to the code that binds an operation to the caller's certificate — most
// notably the Security Officer share claim. Transports that terminate TLS
// themselves (raw-socket MCP, unix) must install it; HTTP-shaped transports
// read *http.Request.TLS directly.
var WithTLSState = auth.WithTLSState

// TLSStateFromContext retrieves the mTLS connection state stored by
// WithTLSState. It returns nil when the context carries none, so callers fail
// closed.
var TLSStateFromContext = auth.TLSStateFromContext

// IdentityFromContext retrieves the Identity from context.
// Returns nil if no identity is stored.
var IdentityFromContext = auth.IdentityFromContext

// IdentityIsSystemTenant reports whether identity is scoped to the
// structurally privileged system tenant. A nil identity is treated as
// system-scoped (no auth context implies an internal, unauthenticated
// pipeline such as platform boot). A non-nil identity delegates to
// IsSystemTenant(identity.TenantID) — the single source of truth for the
// string-level comparison. (Aliases rbac.IsSystemTenant.)
var IdentityIsSystemTenant = rbac.IsSystemTenant
