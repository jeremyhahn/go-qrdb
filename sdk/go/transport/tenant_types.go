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
	"crypto"
	"strconv"
	"time"
)

// TenantQuotas holds per-tenant resource limits. A value of -1 means
// unlimited and 0 means use the system default.
type TenantQuotas struct {
	MaxDatabases         int64 `json:"max_databases"`
	MaxRaftGroups        int64 `json:"max_raft_groups"`
	MaxReplicasPerGroup  int64 `json:"max_replicas_per_group"`
	MaxTotalReplicas     int64 `json:"max_total_replicas"`
	MaxCollections       int64 `json:"max_collections"`
	MaxKeys              int64 `json:"max_keys"`
	MaxStorageBytes      int64 `json:"max_storage_bytes"`
	MaxDatabaseSizeBytes int64 `json:"max_database_size_bytes"`
	MaxUsers             int64 `json:"max_users"`
	MaxRoles             int64 `json:"max_roles"`
	MaxCertificates      int64 `json:"max_certificates"`
	MaxBackups           int64 `json:"max_backups"`
	MaxVectorDims        int64 `json:"max_vector_dims"`
	MaxTSMetrics         int64 `json:"max_ts_metrics"`
	// SystemBarrierQuota governs an isolated tenant's use of the shared system
	// barrier (bytes): 0 makes the option unavailable, -1 unlimited, >0 a cap.
	SystemBarrierQuota int64 `json:"system_barrier_quota"`
	// MaxDedicatedWALGroups caps the number of dedicated-raft groups a tenant
	// may grant a dedicated WAL. -1 = unlimited; 0 = default (no dedicated WAL).
	MaxDedicatedWALGroups int64 `json:"max_dedicated_wal_groups"`
}

// TenantEntitlements describes which isolation tiers and dedicated-storage
// features a tenant's plan permits.
type TenantEntitlements struct {
	AllowedIsolationModes []string `json:"allowed_isolation_modes,omitempty"`
	AllowDedicatedRaft    bool     `json:"allow_dedicated_raft,omitempty"`
	AllowVolumeStorage    bool     `json:"allow_volume_storage,omitempty"`
	// AllowSystemManagedBarrier permits a dedicated-isolation tenant to opt into
	// the system-managed auto-unseal key-management mode for its barrier. When
	// false (the secure default) the tenant must self-manage its barrier keys.
	AllowSystemManagedBarrier bool `json:"allow_system_managed_barrier,omitempty"`
	// AllowedBarrierModes lists the dedicated-barrier sealing strategies the
	// isolated tenant may select (none/passphrase/shamir or an xkms hardware id).
	AllowedBarrierModes []string `json:"allowed_barrier_modes,omitempty"`
	// AllowDedicatedWAL permits a tenant to opt a dedicated-raft database into
	// a dedicated WAL group (requires AllowDedicatedRaft).
	AllowDedicatedWAL bool `json:"allow_dedicated_wal,omitempty"`
	// AllowedVolumePools lists the named volume pools a volume/raft tenant
	// may be placed on; the first entry is its default. Fail-closed: empty
	// entitles no pool.
	AllowedVolumePools []string `json:"allowed_volume_pools,omitempty"`
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

// TenantStatus is a tenant's operational status. The server validates it and
// its transitions; only TenantStatusActive admits the tenant's principals.
type TenantStatus string

const (
	// TenantStatusActive is a tenant in normal service. New tenants start
	// here.
	TenantStatusActive TenantStatus = "active"

	// TenantStatusSuspended is a tenant temporarily out of service; its
	// principals are refused until it is reactivated.
	TenantStatusSuspended TenantStatus = "suspended"

	// TenantStatusDisabled is a tenant taken out of service; its principals
	// are refused until it is reactivated. It cannot move to suspended.
	TenantStatusDisabled TenantStatus = "disabled"
)

// TenantInfo contains information about a tenant.
type TenantInfo struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	IsolationType TenantIsolationType `json:"isolation_type,omitempty"`
	// VolumePool is the named volume pool for an IsolationVolume /
	// IsolationRaft tenant, or the pool the registry placed an IsolationBarrier
	// tenant's barrier on. Empty for prefix tenants.
	VolumePool   string              `json:"volume_pool,omitempty"`
	Quotas       *TenantQuotas       `json:"quotas,omitempty"`
	Entitlements *TenantEntitlements `json:"entitlements,omitempty"`
	// Description is the tenant's free-text description; empty when unset.
	Description string `json:"description,omitempty"`
	// Metadata is the tenant's caller-defined string map; nil when unset.
	Metadata map[string]string `json:"metadata,omitempty"`
	// Status is the tenant's operational status. A principal of a tenant
	// that is not TenantStatusActive is refused.
	Status    TenantStatus `json:"status,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	// Refusal is set when the answering node will not serve this tenant's
	// data; nil otherwise. Another node may still serve it.
	Refusal *TenantRefusal `json:"refusal,omitempty"`
}

// TenantRefusal is the answering node's own view of why it will not serve
// a tenant's dedicated-isolation data (pkg/tenant.Refusal's wire form).
type TenantRefusal struct {
	// Code is stable: "pool_unavailable" | "provider_missing".
	Code string `json:"code"`
	// Message is the typed error's text, for display only -- never parsed.
	Message string `json:"message"`
}

// CreateTenantRequest contains parameters for creating a tenant.
type CreateTenantRequest struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	IsolationType TenantIsolationType `json:"isolation_type,omitempty"`
	// BarrierAutoUnseal opts a dedicated-isolation tenant (barrier/volume) into
	// the system-managed auto-unseal key-management mode (entitlement-gated).
	// The default (false) is the secure self-managed mode.
	BarrierAutoUnseal bool `json:"barrier_auto_unseal,omitempty"`
	// VolumePool names the volume pool an IsolationVolume/IsolationRaft
	// tenant is placed on. Empty (the default) uses the tenant's default
	// pool, the first entry of its AllowedVolumePools. Rejected for
	// prefix/barrier isolation.
	VolumePool string `json:"volume_pool,omitempty"`

	// AdminUsername is the tenant's first admin. Required: a tenant is
	// created with its first admin's login credential, or not at all.
	AdminUsername string `json:"admin_username"`
	// AdminCSRPEM is the admin's certificate signing request, signed by the
	// tenant's own CA; the admin keeps its key. Exactly one of this and
	// AdminCertPEM.
	AdminCSRPEM string `json:"admin_csr_pem,omitempty"`
	// AdminCertPEM is the admin's own certificate, leaf first; it must chain
	// to one of TrustAnchorsPEM.
	AdminCertPEM string `json:"admin_cert_pem,omitempty"`
	// AdminChainPEM holds intermediates for AdminCertPEM.
	AdminChainPEM string `json:"admin_chain_pem,omitempty"`
	// TrustAnchorsPEM registers the tenant's trust anchors with it.
	TrustAnchorsPEM string `json:"trust_anchors_pem,omitempty"`
	// SOUsername is the tenant's security officer, optional: a second
	// principal, distinct from the admin by name and by key, holding the
	// tenant's so role alone. SOUsername, exactly one of SOCSRPEM and
	// SOCertPEM, and SOPin are given together or not at all.
	SOUsername string `json:"so_username,omitempty"`
	// SOCSRPEM is the SO's certificate signing request, signed as
	// AdminCSRPEM is.
	SOCSRPEM string `json:"so_csr_pem,omitempty"`
	// SOCertPEM is the SO's own certificate, leaf first; it must chain to
	// one of TrustAnchorsPEM.
	SOCertPEM string `json:"so_cert_pem,omitempty"`
	// SOChainPEM holds intermediates for SOCertPEM.
	SOChainPEM string `json:"so_chain_pem,omitempty"`
	// SOPin is the SO's second factor, the PIN the SO gate
	// (auth.so_dual_factor) asks the SO for.
	SOPin string `json:"so_pin,omitempty"`
}

// UpdateTenantRequest contains the mutable fields for updating a tenant.
// Zero-value fields are ignored.
type UpdateTenantRequest struct {
	TenantID      string              `json:"tenant_id"`
	Name          string              `json:"name,omitempty"`
	IsolationType TenantIsolationType `json:"isolation_type,omitempty"`
	// Description, when non-nil, replaces the tenant's description; a
	// pointer to "" clears it.
	Description *string `json:"description,omitempty"`
	// Metadata, when non-nil, replaces the tenant's whole metadata map; a
	// non-nil empty map clears it. omitzero (not omitempty) keeps an empty
	// map on the wire, so a clear is distinguishable from "unchanged".
	Metadata map[string]string `json:"metadata,omitzero"`
	// Status, when non-nil, moves the tenant to that status along the
	// allowed transitions; it needs tenants:set_status. Nil leaves it
	// unchanged.
	Status *TenantStatus `json:"status,omitempty"`
	// IsolationPolicy, when non-nil, sets the tenant's own isolation
	// policy: "strict", or "" for inherit (the cluster floor applies). It
	// tightens only, and is refused with *feature.NotActiveError until the
	// strict-isolation cluster feature is active. Nil leaves it unchanged.
	IsolationPolicy *string `json:"isolation_policy,omitempty"`
}

// UpdateTenantResponse contains the updated tenant.
type UpdateTenantResponse struct {
	Tenant TenantInfo `json:"tenant"`
}

// CreateTenantResponse contains the created tenant.
type CreateTenantResponse struct {
	Tenant TenantInfo `json:"tenant"`

	// Admin is the tenant's first admin as onboarding left it.
	Admin TenantAdminCredential `json:"admin"`

	// SO is the tenant's security officer as onboarding left it, when the
	// request named one.
	SO *TenantAdminCredential `json:"so,omitempty"`
}

// TenantAdminCredential is a tenant's first admin, or its SO, and the login
// certificate bound to it. There is never a private key: the account keeps
// its own.
type TenantAdminCredential struct {
	// UserID is zero while AwaitsBarrierInit.
	UserID   uint64 `json:"user_id,omitempty,string"`
	Username string `json:"username"`
	// CertPEM is the admin's certificate: signed from the CSR, or the
	// bring-your-own one as enrolled.
	CertPEM string `json:"cert_pem"`
	// Serial is empty for a bring-your-own certificate.
	Serial     string `json:"serial,omitempty"`
	Thumbprint string `json:"thumbprint"`
	// AwaitsBarrierInit: the tenant keeps its identity records behind its own
	// barrier, so the admin's account is created when the admin initializes
	// it -- which only this certificate may do.
	AwaitsBarrierInit bool `json:"awaits_barrier_init,omitempty"`
}

// TrustAnchor is one of a tenant's registered trust anchors: a CA that may
// vouch for that tenant's users' bring-your-own login certificates, and no
// other tenant's.
type TrustAnchor struct {
	// Fingerprint is the lowercase hex SHA-256 of the certificate's DER;
	// RemoveTrustAnchor takes it.
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	CertPEM     string    `json:"cert_pem"`
	// AddedAt is zero for an anchor store that does not record it.
	AddedAt time.Time `json:"added_at,omitzero"`
}

// AddTrustAnchorsRequest registers every CA certificate in CertsPEM as a
// trust anchor of TenantID, which must be the caller's own tenant.
type AddTrustAnchorsRequest struct {
	TenantID string `json:"-"`
	CertsPEM string `json:"certs_pem"`
}

// TrustAnchorList is a tenant's trust anchors.
type TrustAnchorList struct {
	Anchors []TrustAnchor `json:"anchors"`
}

// GetTenantResponse contains a tenant.
type GetTenantResponse struct {
	Tenant TenantInfo `json:"tenant"`
}

// ListTenantsResponse contains a list of tenants.
type ListTenantsResponse struct {
	Tenants []TenantInfo `json:"tenants"`
}

// DeletedTenant is one retired tenant id and its deletion record. A deleted
// tenant id is retired permanently: no tenant can ever be created under it
// again.
type DeletedTenant struct {
	// ID is the retired tenant id.
	ID string `json:"id"`
	// Name is the deleted tenant's display name.
	Name string `json:"name,omitempty"`
	// IsolationType is the isolation the deleted tenant used.
	IsolationType TenantIsolationType `json:"isolation_type,omitempty"`
	// VolumePool is the named volume pool the deleted tenant was placed on;
	// empty for prefix isolation.
	VolumePool string `json:"volume_pool,omitempty"`
	// CreatedAt is when the deleted tenant was created; zero when unrecorded.
	CreatedAt time.Time `json:"created_at,omitempty"`
	// DeletedAt is when the id was retired; zero when the record could not be
	// decoded, which does not change the retirement.
	DeletedAt time.Time `json:"deleted_at"`
}

// ListDeletedTenantsRequest pages through retired tenant ids.
type ListDeletedTenantsRequest struct {
	// PageSize is the number of records per page. 0 uses the server default;
	// a size above the server maximum is served at the maximum.
	PageSize int32 `json:"page_size,omitempty"`
	// PageToken is the previous response's NextPageToken; empty for the
	// first page.
	PageToken string `json:"page_token,omitempty"`
}

// ListDeletedTenantsResponse is one page of retired tenant ids, in ascending
// id order.
type ListDeletedTenantsResponse struct {
	Tenants []DeletedTenant `json:"tombstones"`
	// NextPageToken resumes the listing; empty on the last page.
	NextPageToken string `json:"next_page_token,omitempty"`
}

// TenantResiduePath is one directory the deleted-tenant reconcile removed, or
// left in place and why. Path is empty when a whole id was left alone.
type TenantResiduePath struct {
	TenantID string `json:"tenant_id"`
	Path     string `json:"path,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ReconcileDeletedTenantsResponse reports one deleted-tenant reconcile pass on
// the node that served the request.
type ReconcileDeletedTenantsResponse struct {
	// Inspected is the number of retired ids examined.
	Inspected int `json:"inspected"`
	// Reclaimed lists the directories removed from the node.
	Reclaimed []TenantResiduePath `json:"reclaimed,omitempty"`
	// Skipped lists what was left in place, with the reason.
	Skipped []TenantResiduePath `json:"skipped,omitempty"`
	// Failed lists the retired ids the pass could not fully reclaim. When it
	// is non-empty the call also returns *ReconcileDeletedTenantsError.
	Failed []TenantResidueFailure `json:"failed,omitempty"`
}

// TenantResidueFailure is one retired id the deleted-tenant reconcile could
// not fully reclaim. The id keeps its records, so the next pass retries it.
type TenantResidueFailure struct {
	TenantID string `json:"tenant_id"`
	// Op is the step that failed ("remove", "close-runtime", ...).
	Op string `json:"op"`
	// Path is empty when the step is not about one path.
	Path string `json:"path,omitempty"`
	// Error is the failure's message as the node reported it.
	Error string `json:"error"`
}

// ReconcileDeletedTenantsError is returned by ReconcileDeletedTenants,
// TOGETHER with the response, when the pass failed for some retired ids. The
// response still describes what was reclaimed and skipped for the others;
// Failed repeats the response's list of what was not.
//
// Err is the node's own typed error when the transport runs in-process (the
// embedded transport: a *tenant.ResidueReconcileFailuresError, reachable through
// errors.As); a network transport carries only the per-id messages, so Err is
// nil there.
type ReconcileDeletedTenantsError struct {
	Failed []TenantResidueFailure
	Err    error
}

func (e *ReconcileDeletedTenantsError) Error() string {
	msg := "transport: reconcile deleted tenants: " + strconv.Itoa(len(e.Failed)) + " failed"
	for _, f := range e.Failed {
		msg += "; tenant " + f.TenantID + ": " + f.Op
		if f.Path != "" {
			msg += " " + f.Path
		}
		msg += ": " + f.Error
	}
	return msg
}

// Unwrap returns the node's own typed error, when the transport has one.
func (e *ReconcileDeletedTenantsError) Unwrap() error { return e.Err }

// ReconcileFailure is the error a transport returns beside a
// ReconcileDeletedTenants response: nil when nothing failed, otherwise a
// *ReconcileDeletedTenantsError naming every failed id and wrapping cause.
func ReconcileFailure(resp *ReconcileDeletedTenantsResponse, cause error) error {
	if resp == nil || len(resp.Failed) == 0 {
		return nil
	}
	return &ReconcileDeletedTenantsError{Failed: resp.Failed, Err: cause}
}

// TenantBarrierInitRequest contains parameters for initializing a tenant barrier.
// Passphrase carries the tenant admin's caller-supplied secret used to seal
// the per-tenant barrier; it is required for the passphrase strategy (there
// is no derived fallback). Strategy selects the seal strategy; when empty,
// the passphrase (software) strategy is used for backward compatibility.
//
// Passphrase is []byte, matching TenantBarrierUnsealRequest.Key, so the caller
// can zeroize it after the call — a Go string cannot be. The caller owns the
// slice and MUST secure.Wipe it once the call returns; no transport retains it.
type TenantBarrierInitRequest struct {
	TenantID   string `json:"tenant_id"`
	Passphrase []byte `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	// Strategy selects the seal strategy for the per-tenant barrier. When
	// empty the passphrase (software) strategy is used. Valid values are the
	// StrategyID strings registered in the server's StrategyRegistry (e.g.
	// "passphrase", "none", "shamir", "tpm2", "pkcs11", "aws-kms",
	// "gcp-kms", "azure-kv", "vault").
	Strategy string `json:"strategy,omitempty"`

	// Officers is the inline Shamir share-holder roster to validate and
	// register atomically with initialization, through the same holder
	// engine standalone officer registration (TenantBarrierOfficerRegister)
	// uses -- mirroring how `qrdb init` takes the system barrier's own
	// officer roster inline, because a dedicated tenant's own IAM is
	// encrypted behind the very barrier its officers exist to help
	// initialize. Refused with a typed error on any strategy but Shamir;
	// nil-equivalent there. Its length must equal Shares.
	Officers []OfficerRegisterRequest `json:"officers,omitempty"`

	// Policy is the factor policy the officers in Officers enrol under
	// ("" selects the holder engine's default, "designated"). Ignored when
	// Officers is empty.
	Policy string `json:"policy,omitempty"`

	// Signer is the first admin's key. A network transport initializes in
	// two calls on one connection -- init/begin for a nonce, init/complete
	// with Signer's signature over it and the connection's TLS exporter
	// (ProveTenantBarrierInit) -- so the node knows the caller holds the key,
	// not merely a certificate. It may be backed by a device (TPM, PIV,
	// PKCS#11) through crypto.Signer; the private key never leaves it. The
	// embedded transport is trusted in-process and does not use it.
	Signer crypto.Signer `json:"-"`
}

// TenantBarrierHolderCert is the certificate issued to one officer of an
// inline Shamir roster, registered atomically with the tenant barrier's own
// initialization (TenantBarrierInitRequest.Officers).
type TenantBarrierHolderCert struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	CertPEM  string `json:"cert_pem"`
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

	// Holders carries the certificate issued to each officer in an inline
	// Shamir roster (TenantBarrierInitRequest.Officers), in roster order.
	// Empty when no roster was supplied.
	Holders []TenantBarrierHolderCert `json:"holders,omitempty"`

	// Roster and UnsealKeyCheck are the encoded holder roster and the check
	// value of the unseal key a Shamir barrier was split to, which the
	// owner's init statement signs between a Shamir init's two requests,
	// which TenantBarrierInit makes on every transport. Empty for
	// non-Shamir strategies.
	Roster         []byte `json:"roster,omitempty"`
	UnsealKeyCheck []byte `json:"unseal_key_check,omitempty"`

	// AdminPending reports a committed init whose tenant first admin is not
	// created yet: a Shamir init unseals only the node that served it, so
	// the tenant's home group completes the admin once Holders have
	// unsealed enough other nodes for it to elect a leader. The holder
	// certificates above are what they unseal with; a retried init answers
	// already-initialized.
	AdminPending bool `json:"admin_pending,omitempty"`
}

// TenantBarrierSignedInitBeginRequest asks for the first half of a two-phase
// signed tenant barrier init: the init statement the tenant's first admin
// signs away from the node, for a caller whose key cannot sign over the
// connection that carries the call (TenantBarrierInitRequest.Signer). The
// fields are TenantBarrierInitRequest's, plus PublicKey and KeyRef.
type TenantBarrierSignedInitBeginRequest struct {
	TenantID string `json:"tenant_id"`
	// Strategy selects the seal strategy; empty is passphrase.
	Strategy string `json:"strategy,omitempty"`
	// KeyRef is the strategy's reference to the tenant's key, for a
	// strategy that names one; empty otherwise.
	KeyRef    string `json:"key_ref,omitempty"`
	Threshold int    `json:"threshold,omitempty"`
	Shares    int    `json:"shares,omitempty"`
	// Officers and Policy are a Shamir init's inline roster, as on
	// TenantBarrierInitRequest.
	Officers []OfficerRegisterRequest `json:"officers,omitempty"`
	Policy   string                   `json:"policy,omitempty"`
	// PublicKey is the DER SubjectPublicKeyInfo of the key that signs the
	// statement: the tenant's pending first admin's.
	PublicKey []byte `json:"public_key"`
}

// TenantBarrierSignedInitBeginResponse carries the statement to sign and the
// reference TenantBarrierSignedInitComplete redeems it with.
type TenantBarrierSignedInitBeginResponse struct {
	TenantID string `json:"tenant_id"`
	Strategy string `json:"strategy"`
	// Reference names the statement to TenantBarrierSignedInitComplete. It
	// is single-use, spent by any complete attempt, and lapses at ExpiresAt.
	Reference []byte `json:"reference"`
	// Statement is the exact message to sign, detached: ECDSA over its
	// SHA-256 digest in ASN.1 form, RSA PKCS #1 v1.5 over its SHA-256 digest,
	// or pure Ed25519 over the bytes themselves.
	Statement []byte `json:"statement"`
	// ExpiresAt is when Reference lapses.
	ExpiresAt time.Time `json:"expires_at"`
	// Threshold, TotalShares, ShareCount and Holders report a Shamir init's
	// prepared split; zero for every other strategy.
	Threshold   int                       `json:"threshold,omitempty"`
	TotalShares int                       `json:"total_shares,omitempty"`
	ShareCount  int                       `json:"share_count,omitempty"`
	Holders     []TenantBarrierHolderCert `json:"holders,omitempty"`
}

// TenantBarrierSignedInitCompleteRequest redeems a begun statement with the
// detached signature over it.
type TenantBarrierSignedInitCompleteRequest struct {
	TenantID string `json:"tenant_id"`
	// Reference is TenantBarrierSignedInitBeginResponse.Reference.
	Reference []byte `json:"reference"`
	// Signature is the detached signature over
	// TenantBarrierSignedInitBeginResponse.Statement.
	Signature []byte `json:"signature"`
	// Passphrase is the passphrase strategy's credential; nil otherwise.
	Passphrase []byte `json:"passphrase,omitempty"`
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

// UpdateTenantEntitlementsRequest contains parameters for updating tenant
// feature entitlements.
type UpdateTenantEntitlementsRequest struct {
	TenantID     string             `json:"tenant_id"`
	Entitlements TenantEntitlements `json:"entitlements"`
}

// UpdateTenantEntitlementsResponse contains the updated tenant.
type UpdateTenantEntitlementsResponse struct {
	Tenant TenantInfo `json:"tenant"`
}

// TenantBarrierUnsealRequest unseals a tenant barrier with its credential
// (the passphrase or key it was initialized with). A Shamir tenant barrier
// refuses it: M share holders unseal it through TenantBarrierQuorumSubmit.
type TenantBarrierUnsealRequest struct {
	TenantID string `json:"tenant_id"`
	Key      []byte `json:"key,omitempty"`
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

	// ClusterSeal is the tenant barrier's cluster seal state.
	ClusterSeal *ClusterSeal `json:"cluster_seal,omitempty"`

	// KeyFingerprint names the answering node's own key of the barrier
	// without revealing it. Every node serving the barrier holds its own key,
	// so every node reports a different fingerprint. Empty while sealed or
	// unencrypted.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`

	// Nodes are the nodes holding a replica of the tenant's groups, each
	// with the API endpoints it publishes: the nodes a barrier init fans out
	// to, since each holds its own key. Empty on a standalone node.
	Nodes []TenantBarrierNode `json:"nodes,omitempty"`
}

// TenantBarrierNode is one node serving a tenant barrier. Endpoints maps a
// protocol name ("rest", "grpc", "quic", "mcp") to the address the node
// serves it on.
type TenantBarrierNode struct {
	Node      string            `json:"node"`
	Endpoints map[string]string `json:"endpoints,omitempty"`
}
