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

import "github.com/jeremyhahn/go-qrdb/pkg/auth"

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

// IdentityFromContext retrieves the Identity from context.
// Returns nil if no identity is stored.
var IdentityFromContext = auth.IdentityFromContext
