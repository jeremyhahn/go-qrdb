// Copyright 2026 Jeremy Hahn
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

import "context"

// ---------------------------------------------------------------------------
// SessionClient
// ---------------------------------------------------------------------------
//
// SessionClient is the minimal session-management surface added in
// Phase 3. Today it covers the two operations transport callers need
// for an authenticated request-loop:
//
//   - ResolveIdentity: given a bearer string, return the live identity
//   - Revoke         : kill a bearer ahead of its natural expiry
//
// LoginClient handles issuance (BeginLogin / FinishLogin); SessionClient
// owns the validation / invalidation half. Splitting the surface keeps
// the auth-handshake API independent of the steady-state validation API
// so transports that delegate one but not the other don't pay the tax
// of an unused method.

// SessionClient is the SDK surface for resolving and revoking bearer
// tokens issued by LoginClient.FinishLogin.
type SessionClient interface {
	// ResolveIdentity returns the Identity bound to the given bearer.
	// Returns InvalidBearerError when the bearer is unknown or expired.
	ResolveIdentity(ctx context.Context, bearer string) (*Identity, error)

	// Revoke invalidates the bearer immediately. Subsequent
	// ResolveIdentity calls for the same bearer return
	// InvalidBearerError.
	Revoke(ctx context.Context, bearer string) error
}
