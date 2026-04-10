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

package quota

import "context"

// Checker validates whether a tenant can create additional resources
// within their quota limits.
type Checker interface {
	// Check returns nil if the tenant has capacity for one more of the
	// given resource, or an *ExceededError if the limit is reached.
	Check(ctx context.Context, tenantID string, resource Resource) error

	// CheckN returns nil if the tenant has capacity for n more of the
	// given resource, or an *ExceededError if the limit would be exceeded.
	CheckN(ctx context.Context, tenantID string, resource Resource, n int64) error

	// Usage returns the current count for the given resource and tenant.
	Usage(ctx context.Context, tenantID string, resource Resource) (int64, error)
}

// CountFunc is a function that returns the current count of a resource
// for a tenant. Used by the Service to query actual usage from the
// underlying data layer.
type CountFunc func(ctx context.Context, tenantID string) (int64, error)

// QuotaProvider resolves the quota configuration for a given tenant.
type QuotaProvider interface {
	// Quotas returns the resource limits configured for the tenant with
	// the given ID.
	Quotas(ctx context.Context, tenantID string) (*Quotas, error)
}
