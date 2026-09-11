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

// BarrierInitializeRequest holds the parameters used to initialize the
// cryptographic barrier. The canonical CLI verb tree (qrdb / xkmsctl /
// quicbaas / platformctl) exposes:
//
//	barrier init --barrier-strategy <none|software|shamir>
//	             --secret <passphrase>     (required for software and shamir)
//	             --so-pin <pin>            (optional)
//	             --user-pin <pin>          (required if --so-pin set)
//
// BarrierStrategy + SOPin + UserPin are optional on the wire to remain
// backward compatible with servers that pre-date the canonical surface.
//
// Secret is the strategy wrapping passphrase. SOPin/UserPin, when
// provided, are routed to the canonical pin.PINBackend on the server
// (qrdb/pkg/pin) — independent of and complementary to Secret.
//
// Secret is []byte rather than string because a Go string is immutable and
// therefore cannot be zeroized: a passphrase materialized as one survives on
// the heap until the collector happens to reuse the page. The caller owns
// Secret, may hand it straight out of a secure.Buffer, and MUST
// secure.Wipe it once the call returns. Every transport in this SDK treats
// it as a borrow for the duration of the call and retains nothing.
//
// "Retains nothing" is the precise claim, and it is narrower than "the
// plaintext is gone". Wiping this slice destroys THIS slice and every alias of
// it; it does not reach the transient copy each encoder necessarily makes —
// the base64 rendering in a REST JSON body, protobuf's out-of-place
// marshalling, the MCP JSON-RPC request body. Those are short-lived and
// unreferenced once the call completes, so the collector reclaims them, but
// they are not zeroized and nothing here can zeroize them: serializing a value
// means copying it, and the wire format owns that copy. What the []byte buys
// is that the copy the CALLER holds, and the one the daemon decodes, are both
// destroyable on demand — which a string never was.
type BarrierInitializeRequest struct {
	Secret          []byte `json:"secret"`
	BarrierStrategy string `json:"barrier_strategy,omitempty"`
	SOPin           string `json:"so_pin,omitempty"`
	UserPin         string `json:"user_pin,omitempty"`
}

// BarrierUnsealRequest holds the secret used to unseal the barrier when
// the barrier was initialized with a single-master-key strategy
// (--barrier-strategy=software). PIN fields are intentionally absent —
// unseal uses the strategy's passphrase directly and requires no PIN
// authorization.
//
// Secret is caller-owned wipeable bytes; see BarrierInitializeRequest.
type BarrierUnsealRequest struct {
	Secret []byte `json:"secret"`
}

// BarrierStatusResponse describes the current state of the cryptographic
// barrier. ShamirThreshold and ShamirTotal carry the configured M-of-N
// quorum when the barrier uses the Shamir strategy and are zero otherwise.
type BarrierStatusResponse struct {
	Sealed          bool   `json:"sealed"`
	Strategy        string `json:"strategy"`
	HardwareBacked  bool   `json:"hardware_backed"`
	InitializedAt   string `json:"initialized_at,omitempty"`
	ShamirThreshold int    `json:"shamir_threshold,omitempty"`
	ShamirTotal     int    `json:"shamir_total,omitempty"`
}

// BarrierSealRequest carries the optional Security Officer PIN supplied
// when sealing the barrier. The SO PIN is verified against the canonical
// pin.PINBackend when one is configured and a SO PIN has been set; when
// no backend or PIN is configured the field is ignored.
//
// This type is part of the canonical SO-PIN-gated administrative-op
// surface. Older clients that call BarrierSeal without a body still work
// because the server treats a missing SOPin as a no-op when no PIN
// backend is configured.
type BarrierSealRequest struct {
	SOPin string `json:"so_pin,omitempty"`
}

// BarrierInitializeShamirRequest holds the parameters for initializing the
// barrier with Shamir secret sharing M-of-N. The canonical CLI verb is
// `barrier init-shamir`.
//
// SOPin/UserPin, when provided, are routed to the canonical
// pin.PINBackend on the server — independent of and complementary to
// Secret.
// Secret is caller-owned wipeable bytes; see BarrierInitializeRequest.
type BarrierInitializeShamirRequest struct {
	Secret      []byte `json:"secret"`
	Threshold   int    `json:"threshold"`
	TotalShares int    `json:"total_shares"`
	SOPin       string `json:"so_pin,omitempty"`
	UserPin     string `json:"user_pin,omitempty"`
}

// BarrierInitializeShamirResponse is the share-free result of a Shamir
// initialization. Share material is never returned here; Security Officers
// retrieve their individual shares via the ShareClaimBegin/ShareClaimComplete
// flow using their mTLS certificate.
type BarrierInitializeShamirResponse struct {
	Threshold   int `json:"threshold"`
	TotalShares int `json:"total_shares"`
	ShareCount  int `json:"share_count"`
}

// BarrierUnsealShareRequest holds a single Shamir share for incremental
// unsealing.
type BarrierUnsealShareRequest struct {
	Share string `json:"share"`
}

// BarrierUnsealShareResponse reports progress toward the unseal threshold.
type BarrierUnsealShareResponse struct {
	Required  int  `json:"required"`
	Submitted int  `json:"submitted"`
	Complete  bool `json:"complete"`
}

// BarrierUnsealSharesRequest holds multiple Shamir shares for batch
// unsealing. The canonical CLI verb is `barrier unseal-shares`.
type BarrierUnsealSharesRequest struct {
	Shares []string `json:"shares"`
}

// BarrierRekeyRequest holds parameters for rekeying the barrier. The
// canonical CLI verb is `barrier rekey` and accepts an optional SO PIN
// (required when a PINBackend is configured) and optional Shamir
// threshold/total. Rekey is a Shamir-only operation: the server
// generates a fresh unseal key and share set itself, so no secret
// material travels on the request.
//
// SOPin, when provided, is verified against the canonical pin.PINBackend
// before the rekey is executed. When no backend or PIN is configured the
// field is ignored, preserving dev-mode flows.
type BarrierRekeyRequest struct {
	SOPin     string `json:"so_pin,omitempty"`
	Threshold int    `json:"threshold"`
	Total     int    `json:"total"`
}

// BarrierRekeyResponse is the share-free result of a rekey operation. New
// shares are sealed to enrolled Security Officers and claimed via the
// ShareClaimBegin/ShareClaimComplete flow.
type BarrierRekeyResponse struct {
	Threshold   int `json:"threshold"`
	TotalShares int `json:"total_shares"`
	ShareCount  int `json:"share_count"`
}

// BarrierEnableEncryptionRequest migrates a previously-unencrypted barrier to a
// keyed strategy (enable encryption after the fact). TenantID, when set, scopes
// the migration to that tenant's dedicated barrier; empty means the system
// barrier (admin scope). The migration runs asynchronously on the server.
//
// Passphrase is caller-owned wipeable bytes; see BarrierInitializeRequest.
type BarrierEnableEncryptionRequest struct {
	TenantID        string `json:"tenant_id,omitempty"`
	Strategy        string `json:"strategy"`
	Mechanism       string `json:"mechanism,omitempty"`
	Availability    string `json:"availability,omitempty"`
	Passphrase      []byte `json:"passphrase,omitempty"`
	ShamirThreshold int    `json:"shamir_threshold,omitempty"`
	ShamirTotal     int    `json:"shamir_total,omitempty"`
}

// BarrierEnableEncryptionResponse acknowledges that the migration has started.
// Clients poll BarrierStatus for the strategy change to observe completion.
type BarrierEnableEncryptionResponse struct {
	Status string `json:"status"`
	Scope  string `json:"scope"`
}

// BarrierGenerateRecoveryKeysRequest holds parameters for generating
// recovery keys. The canonical CLI verb is `barrier recovery generate`
// and supports an optional SO PIN.
type BarrierGenerateRecoveryKeysRequest struct {
	Threshold int    `json:"threshold"`
	Total     int    `json:"total"`
	SOPin     string `json:"so_pin,omitempty"`
}

// BarrierRecoveryKeysResponse is the share-free result of recovery key
// generation. Recovery shares are sealed to enrolled recovery officers and
// claimed via the RecoveryShareClaimBegin/RecoveryShareClaimComplete flow.
type BarrierRecoveryKeysResponse struct {
	Threshold  int `json:"threshold"`
	Total      int `json:"total"`
	ShareCount int `json:"share_count"`
}

// BarrierRecoverWithKeysRequest holds recovery keys used to unseal the
// barrier during disaster recovery. The canonical CLI verb is
// `barrier recovery recover` with repeatable `--share` flags.
type BarrierRecoverWithKeysRequest struct {
	Keys []string `json:"keys"`
}

// BarrierHasRecoveryKeysResponse indicates whether recovery keys have been
// generated for the barrier.
type BarrierHasRecoveryKeysResponse struct {
	HasKeys bool `json:"has_keys"`
}

// BarrierGenerateRootTokenRequest holds the Shamir shares required to
// generate a new root token.
type BarrierGenerateRootTokenRequest struct {
	Shares []string `json:"shares"`
}

// BarrierRootTokenResponse returns a newly generated root token.
type BarrierRootTokenResponse struct {
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

// ---------------------------------------------------------------------------
// Security Officer registration and share-claim types (§7.5)
// ---------------------------------------------------------------------------

// OfficerRegisterRequest carries the parameters for registering a new
// Security Officer against a system or recovery barrier.
type OfficerRegisterRequest struct {
	Username          string `json:"username"`
	CSRPEM            []byte `json:"csr_pem"`
	AuthenticatorType string `json:"authenticator_type,omitempty"`
	AuthenticatorRef  string `json:"authenticator_ref,omitempty"`
}

// OfficerRegisterResponse is returned to the admin after successful officer
// registration. The CertPEM must be delivered to the officer out of band;
// it is not stored server-side after this response.
type OfficerRegisterResponse struct {
	CertPEM    []byte `json:"cert_pem"`
	ShareIndex int    `json:"share_index"`
}

// OfficerStatus is a non-secret view of an enrolled Security Officer. All
// fields are public metadata; no share material is present.
type OfficerStatus struct {
	Username       string    `json:"username"`
	CertThumbprint string    `json:"cert_thumbprint,omitempty"`
	ShareIndex     int       `json:"share_index"`
	AuthFactor     string    `json:"auth_factor"`
	EnrolledAt     time.Time `json:"enrolled_at"`
	ClaimedAt      time.Time `json:"claimed_at,omitempty"`
	SharePending   bool      `json:"share_pending"`
}

// ShareClaimBeginRequest starts the share-claim handshake for the named SO.
type ShareClaimBeginRequest struct {
	Username string `json:"username"`
}

// ShareClaimBeginResponse carries the challenge nonce for the SO to sign.
type ShareClaimBeginResponse struct {
	NonceHex  string    `json:"nonce_hex"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ShareClaimCompleteRequest carries the signed nonce to complete the claim.
type ShareClaimCompleteRequest struct {
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature []byte `json:"signature"`
}

// ShareClaimCompleteResponse is returned to the Security Officer exactly once
// upon a successful CompleteClaimShare. ShareBytes must be wiped by the caller
// after recording or presenting them to the user; they are never logged and
// never re-served.
type ShareClaimCompleteResponse struct {
	ShareIndex int    `json:"share_index"`
	ShareBytes []byte `json:"share_bytes"` // present ONLY in this response type
}

// ShamirSealVoteRequest carries a single Shamir share as a seal vote.
type ShamirSealVoteRequest struct {
	Share string `json:"share"`
}

// ShamirSealVoteResponse reports progress toward the seal-quorum threshold.
type ShamirSealVoteResponse struct {
	Required  int  `json:"required"`
	Submitted int  `json:"submitted"`
	Complete  bool `json:"complete"`
}

// ---------------------------------------------------------------------------
// Tenant officer/claim types (§7.5 — add TenantID as first field)
// ---------------------------------------------------------------------------

// TenantOfficerRegisterRequest carries the parameters for registering an SO
// against a specific tenant's barrier.
type TenantOfficerRegisterRequest struct {
	TenantID          string `json:"tenant_id"`
	Username          string `json:"username"`
	CSRPEM            []byte `json:"csr_pem"`
	AuthenticatorType string `json:"authenticator_type,omitempty"`
	AuthenticatorRef  string `json:"authenticator_ref,omitempty"`
}

// TenantShareClaimBeginRequest starts the share-claim handshake for a tenant
// barrier officer.
type TenantShareClaimBeginRequest struct {
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
}

// TenantShareClaimCompleteRequest completes the share-claim handshake for a
// tenant barrier officer.
type TenantShareClaimCompleteRequest struct {
	TenantID  string `json:"tenant_id"`
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature []byte `json:"signature"`
}

// TenantOfficerClaimStatusRequest requests officer claim status for a tenant.
type TenantOfficerClaimStatusRequest struct {
	TenantID string `json:"tenant_id"`
}

// TenantShamirSealVoteRequest carries a Shamir share as a seal vote for a
// specific tenant's barrier.
type TenantShamirSealVoteRequest struct {
	TenantID string `json:"tenant_id"`
	Share    string `json:"share"`
}
