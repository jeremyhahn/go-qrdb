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

package auth

// SORoleRequiredError is the SO gate's refusal of a caller that does not hold
// the SO role. The credential is good and the role is absent, so it
// classifies as AuthFailureUnauthorized: 403 / PermissionDenied.
type SORoleRequiredError struct{}

// Error returns the refusal message.
func (e *SORoleRequiredError) Error() string { return "SO role required" }

// SORoleConflictError is the strict separation-of-duties refusal of an
// account holding the Security Officer role together with its tenant's Admin
// role: a role write that would leave an account holding both, or an
// SO-exclusive act by a caller that holds both (go-qrdb#1128). The credential
// is good and the combination is forbidden, so it classifies as
// AuthFailureUnauthorized: 403 / PermissionDenied.
type SORoleConflictError struct {
	// TenantID is the tenant whose SO and Admin roles were combined.
	TenantID string
}

// Error returns the refusal message.
func (e *SORoleConflictError) Error() string {
	return "separation of duties: the SO role cannot be held with the admin role of tenant " + e.TenantID
}

// Structured-refusal reasons every network transport carries beside the
// message of an SO refusal, so a client rebuilds the typed error -- and an
// embedder matches sdk.SORoleRequiredError or sdk.SORoleConflictError --
// without parsing text (go-qrdb#1129).
const (
	ReasonSORoleRequired = "SO_ROLE_REQUIRED"
	ReasonSORoleConflict = "SO_ROLE_CONFLICT"
)

// soConflictKeyTenant is the refusal metadata key of an
// *SORoleConflictError's TenantID.
const soConflictKeyTenant = "tenant_id"

// Metadata encodes e as structured-refusal metadata.
func (e *SORoleConflictError) Metadata() map[string]string {
	return map[string]string{soConflictKeyTenant: e.TenantID}
}

// SORoleConflictFromMetadata rebuilds the error Metadata encoded. The
// reason, not the metadata, is what says the roles conflict, so a missing
// tenant key leaves TenantID empty rather than losing the type.
func SORoleConflictFromMetadata(md map[string]string) *SORoleConflictError {
	return &SORoleConflictError{TenantID: md[soConflictKeyTenant]}
}
