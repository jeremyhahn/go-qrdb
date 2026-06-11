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

import "time"

// TenantQuotas holds per-tenant resource limits. A value of -1 means
// unlimited and 0 means use the system default.
type TenantQuotas struct {
	MaxDatabases    int64 `json:"max_databases"`
	MaxRaftGroups   int64 `json:"max_raft_groups"`
	MaxCollections  int64 `json:"max_collections"`
	MaxKeys         int64 `json:"max_keys"`
	MaxStorageBytes int64 `json:"max_storage_bytes"`
	MaxUsers        int64 `json:"max_users"`
	MaxRoles        int64 `json:"max_roles"`
	MaxCertificates int64 `json:"max_certificates"`
	MaxBackups      int64 `json:"max_backups"`
	MaxVectorDims   int64 `json:"max_vector_dims"`
	MaxTSMetrics    int64 `json:"max_ts_metrics"`
}

// TenantIsolationType controls the data isolation strategy for a tenant.
type TenantIsolationType string

const (
	// TenantIsolationBarrier applies namespace prefixing plus an optional
	// per-tenant cryptographic barrier. Default for all new tenants.
	TenantIsolationBarrier TenantIsolationType = "barrier"

	// TenantIsolationPrefix applies only namespace prefixing. No crypto
	// barrier is added.
	TenantIsolationPrefix TenantIsolationType = "prefix"
)

// TenantInfo contains information about a tenant.
type TenantInfo struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	IsolationType TenantIsolationType `json:"isolation_type,omitempty"`
	Quotas        *TenantQuotas       `json:"quotas,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

// CreateTenantRequest contains parameters for creating a tenant.
type CreateTenantRequest struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	IsolationType TenantIsolationType `json:"isolation_type,omitempty"`
}

// UpdateTenantRequest contains the mutable fields for updating a tenant.
// Zero-value fields are ignored.
type UpdateTenantRequest struct {
	TenantID      string              `json:"tenant_id"`
	Name          string              `json:"name,omitempty"`
	IsolationType TenantIsolationType `json:"isolation_type,omitempty"`
}

// UpdateTenantResponse contains the updated tenant.
type UpdateTenantResponse struct {
	Tenant TenantInfo `json:"tenant"`
}

// CreateTenantResponse contains the created tenant.
type CreateTenantResponse struct {
	Tenant TenantInfo `json:"tenant"`
}

// GetTenantResponse contains a tenant.
type GetTenantResponse struct {
	Tenant TenantInfo `json:"tenant"`
}

// ListTenantsResponse contains a list of tenants.
type ListTenantsResponse struct {
	Tenants []TenantInfo `json:"tenants"`
}

// TenantBarrierInitRequest contains parameters for initializing a tenant barrier.
// Passphrase carries the tenant admin's caller-supplied secret used to seal
// the per-tenant barrier; it is required for the passphrase strategy (there
// is no derived fallback). Strategy selects the seal strategy; when empty,
// the passphrase (software) strategy is used for backward compatibility.
type TenantBarrierInitRequest struct {
	TenantID   string `json:"tenant_id"`
	Passphrase string `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	// Strategy selects the seal strategy for the per-tenant barrier. When
	// empty the passphrase (software) strategy is used. Valid values are the
	// StrategyID strings registered in the server's StrategyRegistry (e.g.
	// "passphrase", "none", "shamir", "tpm2", "pkcs11", "aws-kms",
	// "gcp-kms", "azure-kv", "vault").
	Strategy string `json:"strategy,omitempty"`
}

// TenantBarrierInitResponse contains the outcome of a tenant barrier
// initialization. Share material is never returned here; Security Officers
// retrieve their individual shares via the tenant barrier
// TenantBarrierShareClaimBegin/TenantBarrierShareClaimComplete flow.
type TenantBarrierInitResponse struct {
	// TenantID is the tenant whose barrier was initialized.
	TenantID string `json:"tenant_id"`

	// Strategy is the seal strategy the barrier was initialized with.
	Strategy string `json:"strategy"`

	// Threshold is the minimum number of shares required to unseal. Zero for
	// non-Shamir strategies.
	Threshold int `json:"threshold,omitempty"`

	// TotalShares is the total number of shares generated. Zero for non-Shamir
	// strategies.
	TotalShares int `json:"total_shares,omitempty"`

	// ShareCount is the number of shares successfully sealed to officer
	// enclaves. Zero for non-Shamir strategies.
	ShareCount int `json:"share_count,omitempty"`
}

// UpdateTenantQuotasRequest contains parameters for updating tenant quotas.
type UpdateTenantQuotasRequest struct {
	TenantID string       `json:"tenant_id"`
	Quotas   TenantQuotas `json:"quotas"`
}

// UpdateTenantQuotasResponse contains the updated tenant.
type UpdateTenantQuotasResponse struct {
	Tenant TenantInfo `json:"tenant"`
}

// TenantBarrierUnsealRequest contains parameters for unsealing a tenant barrier.
type TenantBarrierUnsealRequest struct {
	TenantID string `json:"tenant_id"`
	Share    []byte `json:"share,omitempty"`
	Key      []byte `json:"key,omitempty"`
}

// TenantBarrierUnsealShareRequest contains parameters for submitting a single
// Shamir share toward a per-tenant barrier's quorum threshold.
type TenantBarrierUnsealShareRequest struct {
	TenantID string `json:"tenant_id"`
	Share    string `json:"share"`
}

// TenantBarrierUnsealProgress reports quorum progress while submitting Shamir
// shares to unseal a per-tenant barrier. Once Complete is true the barrier has
// been unsealed and the quorum accumulator has been reset.
type TenantBarrierUnsealProgress struct {
	// TenantID is the tenant whose barrier is being unsealed.
	TenantID string `json:"tenant_id"`

	// Required is the number of shares needed to reach the threshold.
	Required int `json:"required"`

	// Submitted is the number of distinct shares submitted so far.
	Submitted int `json:"submitted"`

	// Complete indicates the threshold has been met and the barrier unsealed.
	Complete bool `json:"complete"`

	// Sealed reports whether the barrier is still sealed.
	Sealed bool `json:"sealed"`
}

// TenantBarrierSealRequest contains parameters for sealing a per-tenant
// barrier. Sealing requires no credentials — it is the inverse of unseal and
// returns the tenant barrier to the sealed state, refusing further reads or
// writes until unsealed again.
type TenantBarrierSealRequest struct {
	TenantID string `json:"tenant_id"`
}

// TenantBarrierStatusRequest is the request to query a tenant barrier's
// current sealed/initialized state and active strategy metadata.
type TenantBarrierStatusRequest struct {
	TenantID string `json:"tenant_id"`
}

// TenantBarrierStatusResponse contains the current state of a per-tenant
// barrier. The fields mirror seal.BarrierStatus.
type TenantBarrierStatusResponse struct {
	// TenantID is the tenant whose barrier is being reported on.
	TenantID string `json:"tenant_id"`

	// Sealed is true when the tenant barrier is in sealed state.
	Sealed bool `json:"sealed"`

	// Initialized is true when the tenant barrier has been initialized via
	// TenantBarrierInit at least once.
	Initialized bool `json:"initialized"`

	// Strategy identifies which sealing strategy protects the tenant root
	// key (e.g. "software", "shamir", "pkcs11", "tpm2").
	Strategy string `json:"strategy"`

	// HardwareBacked is true when the active strategy uses dedicated
	// cryptographic hardware (HSM, TPM, etc.).
	HardwareBacked bool `json:"hardware_backed"`

	// ShamirThreshold is the minimum number of Shamir shares required to
	// unseal this tenant barrier. Zero when the active strategy is not Shamir.
	ShamirThreshold int `json:"shamir_threshold,omitempty"`

	// ShamirTotal is the total number of Shamir shares generated at
	// initialization. Zero when the active strategy is not Shamir.
	ShamirTotal int `json:"shamir_total,omitempty"`
}
