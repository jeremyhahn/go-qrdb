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

import "github.com/jeremyhahn/go-qrdb/pkg/service"

// ---------------------------------------------------------------------------
// Policy-minted delegations (#1187): the embedder's side of a tenant's
// standing delegation policy, so `node.Node.MintDelegation` is reachable
// without importing pkg/*.
// ---------------------------------------------------------------------------

// MintDelegationRequest asks `node.Node.MintDelegation` for a delegation
// certificate inside the delegating tenant's standing policy: the tenant,
// the principal's CSR, and an optional narrowing of the policy's scope and
// lifetime.
type MintDelegationRequest = service.MintDelegationRequest

// IssuedDelegation is a minted delegation certificate: its PEM, serial,
// scope and lifetime.
type IssuedDelegation = service.IssuedDelegation

// DelegationPolicyNotFoundError reports that the tenant holds no standing
// delegation policy for the principal: no policy was put, or the tenant
// deleted it, so no delegation is minted.
type DelegationPolicyNotFoundError = service.DelegationPolicyNotFoundError
