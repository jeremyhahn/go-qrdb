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

package tenant

import "context"

// DefaultTenantID is the tenant ID used when no tenant is set in the context.
const DefaultTenantID = "default"

// contextKey is an unexported type used as the key for tenant ID values
// in context.Context. Using a dedicated type prevents collisions with
// keys from other packages.
type contextKey struct{}

// WithTenantID returns a new context carrying the given tenant ID.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, contextKey{}, tenantID)
}

// FromContext extracts the tenant ID from the context.
// Returns DefaultTenantID when no tenant is set or the value is empty.
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(contextKey{}).(string); ok && v != "" {
		return v
	}
	return DefaultTenantID
}
