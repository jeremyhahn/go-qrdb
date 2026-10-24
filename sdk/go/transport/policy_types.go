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

package transport

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
)

// ABACPolicyRequest names one stored ABAC policy. TenantID is optional: the
// policy API acts on the caller's own tenant, and naming another tenant is
// refused with PermissionDenied.
type ABACPolicyRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	Name     string `json:"name"`
}

// ABACPolicyListRequest names the tenant whose policies are listed. An
// empty TenantID is the caller's own tenant.
type ABACPolicyListRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
}

// ABACPolicyListResponse carries a tenant's policy documents, sorted by
// name.
type ABACPolicyListResponse struct {
	Policies []*abac.Policy `json:"policies"`
}

// ABACPolicyClient manages the caller's tenant ABAC policy documents.
//
// Every method acts on the caller's own tenant; a document whose
// metadata.tenant, or a request whose TenantID, names another tenant is
// refused with PermissionDenied. Update and delete are guarded swaps: a
// concurrent change to the same policy is refused with Aborted rather than
// overwritten. A document's spec.enabled keeps its presence: omitted reads
// back omitted, false reads back false.
type ABACPolicyClient interface {
	// CreateABACPolicy validates and stores a new policy, refusing a name
	// the tenant already holds with AlreadyExists. It returns the stored
	// document.
	CreateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error)

	// GetABACPolicy returns one stored policy document, or NotFound.
	GetABACPolicy(ctx context.Context, req *ABACPolicyRequest) (*abac.Policy, error)

	// ListABACPolicies returns every policy document the tenant holds.
	ListABACPolicies(ctx context.Context, req *ABACPolicyListRequest) (*ABACPolicyListResponse, error)

	// UpdateABACPolicy replaces an existing policy's document, named by its
	// metadata.name, keeping its creation time. It returns the stored
	// document, or NotFound when the policy does not exist.
	UpdateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error)

	// DeleteABACPolicy removes one policy, or returns NotFound.
	DeleteABACPolicy(ctx context.Context, req *ABACPolicyRequest) error
}
