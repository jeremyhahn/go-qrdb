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
//	barrier init --barrier-strategy <none|passphrase>
//	             --passphrase <passphrase> (required for passphrase)
//	             --so-pin <pin>            (optional)
//	             --user-pin <pin>          (required if --so-pin set)
//
// A Shamir barrier is never initialized from a secret: see
// BarrierInitializeShamirRequest. The server refuses one with
// seal.ErrShamirInitializeRequiresHolders.
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
	Sealed bool `json:"sealed"`

	// Initialized is true once the barrier has been initialized. A sealed
	// node reports only Sealed and Initialized; every other field is empty
	// until it is unsealed.
	Initialized bool `json:"initialized"`

	Strategy        string `json:"strategy"`
	HardwareBacked  bool   `json:"hardware_backed"`
	InitializedAt   string `json:"initialized_at,omitempty"`
	ShamirThreshold int    `json:"shamir_threshold,omitempty"`
	ShamirTotal     int    `json:"shamir_total,omitempty"`

	// ClusterSeal is the node barrier's cluster seal state.
	ClusterSeal *ClusterSeal `json:"cluster_seal,omitempty"`
}

// ClusterSeal is one barrier's cluster seal state as the node that answered
// sees it -- the `cluster_seal` object of every barrier status, and the answer
// to BarrierClusterUnseal. It carries no key material.
type ClusterSeal struct {
	// Active is true once the cluster has activated cluster-seal. When it is
	// false every seal is node-local and the other fields are empty.
	Active bool `json:"active"`
	// Sealed and Epoch are the barrier's record; Epoch 0 means none exists.
	Sealed bool   `json:"sealed"`
	Epoch  uint64 `json:"epoch"`
	// Principal, PrincipalTenant, Node and At say who wrote the record, from
	// which tenant, through which node, and when (RFC 3339, UTC).
	Principal       string `json:"principal,omitempty"`
	PrincipalTenant string `json:"principal_tenant,omitempty"`
	Node            string `json:"node,omitempty"`
	At              string `json:"at,omitempty"`
	// Local is the answering node's own state for the barrier: serving,
	// sealed, held, cut_off or unconfirmed.
	Local string `json:"local,omitempty"`
}

// BarrierSealRequest carries the Security Officer PIN supplied when sealing
// the barrier. The server verifies it against the node's pin.PINBackend once
// an SO PIN has been provisioned, and refuses the seal when no backend is
// configured.
type BarrierSealRequest struct {
	SOPin string `json:"so_pin,omitempty"`
}

// BarrierInitializeShamirRequest initializes the barrier under Shamir custody.
// The server generates the root key and an independent unseal key, splits
// the unseal key Threshold-of-TotalShares to the share holders already
// enrolled in the unseal domain, and seals one envelope to each. No secret
// travels on the request and no share travels on the response. The
// canonical CLI verb is `barrier init-shamir`.
//
// Policy selects how holders authenticate a quorum submission: "designated"
// (the default; one factor fixed at enrolment) or "any-enrolled". It is
// bound into the holder roster the wrapped root key is sealed under.
//
// SOPin/UserPin, when provided, are provisioned into the node's PIN backend.
type BarrierInitializeShamirRequest struct {
	Threshold   int    `json:"threshold"`
	TotalShares int    `json:"total_shares"`
	Policy      string `json:"policy,omitempty"`
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

// BarrierGenerateRecoveryKeysRequest generates the recovery key: an unseal
// key independent of the unseal domain's, split Threshold-of-N to the
// holders enrolled in the recovery domain, with its own wrapped root-key
// blob. N is the number of enrolled recovery holders. The canonical CLI verb
// is `barrier recovery generate`.
type BarrierGenerateRecoveryKeysRequest struct {
	Threshold int    `json:"threshold"`
	Policy    string `json:"policy,omitempty"`
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

// BarrierHasRecoveryKeysResponse indicates whether recovery keys have been
// generated for the barrier.
type BarrierHasRecoveryKeysResponse struct {
	HasKeys bool `json:"has_keys"`
}

// ---------------------------------------------------------------------------
// Share holders (Security Officers and key custodians)
// ---------------------------------------------------------------------------

// Quorum domains name the key a holder's share belongs to.
const (
	// DomainUnseal is the unseal key the barrier is normally opened with.
	DomainUnseal = "unseal"

	// DomainRecovery is the independent recovery key.
	DomainRecovery = "recovery"
)

// Quorum operations a holder quorum authorizes.
const (
	// OpUnseal opens the sealed barrier (unseal domain).
	OpUnseal = "unseal"

	// OpSeal seals the unsealed barrier (unseal domain).
	OpSeal = "seal"

	// OpRootToken derives the root token (unseal domain).
	OpRootToken = "root-token"

	// OpRekey completes an open rekey round (either domain).
	OpRekey = "rekey"

	// OpRecover opens the sealed barrier with the recovery key.
	OpRecover = "recover"
)

// Holder roles. One person holds one role per barrier scope.
const (
	// RoleSecurityOfficer is a Security Officer share holder.
	RoleSecurityOfficer = "so"

	// RoleCustodian is a key-custodian share holder.
	RoleCustodian = "custodian"
)

// Factor policies select which factors a holder may authenticate a quorum
// submission with.
const (
	// PolicyDesignated admits only the factor fixed at enrolment.
	PolicyDesignated = "designated"

	// PolicyAnyEnrolled admits any factor the holder enrolled.
	PolicyAnyEnrolled = "any-enrolled"
)

// HolderFactorEnrolment enrols one authentication factor for a holder. Name
// is a factor registered on the server ("mtls" is built in; hardware
// factors are injected by the composer). Params are the factor's own
// enrolment parameters.
type HolderFactorEnrolment struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params,omitempty"`
}

// OfficerRegisterRequest enrols a share holder in the unseal or recovery
// domain. CSRPEM is PEM text. The node's CA assigns the certificate
// identity; the CSR supplies only the public key. EncryptionKEM/EncryptionKey enrol a separate HPKE
// recipient key the share envelope is sealed to; when empty the
// certificate key is used (P-256, P-384 or X25519-convertible keys only).
// Designated names the factor fixed at enrolment under the designated
// policy; "mtls" when empty. Nobody may enrol themselves.
type OfficerRegisterRequest struct {
	Username      string                  `json:"username"`
	Role          string                  `json:"role,omitempty"`
	CSRPEM        string                  `json:"csr_pem"`
	EncryptionKEM string                  `json:"encryption_kem,omitempty"`
	EncryptionKey []byte                  `json:"encryption_key,omitempty"`
	Factors       []HolderFactorEnrolment `json:"factors,omitempty"`
	Designated    string                  `json:"designated,omitempty"`
}

// OfficerRegisterResponse carries the CA-issued holder certificate as PEM
// text. The share envelope is not part of it: the holder claims that itself.
type OfficerRegisterResponse struct {
	CertPEM       string   `json:"cert_pem"`
	Role          string   `json:"role"`
	Designated    string   `json:"designated,omitempty"`
	EncryptionKEM string   `json:"encryption_kem,omitempty"`
	Factors       []string `json:"factors,omitempty"`
}

// OfficerStatus is a holder's enrolment and claim state. It never carries
// share material.
type OfficerStatus struct {
	Username       string    `json:"username"`
	Role           string    `json:"role"`
	CertThumbprint string    `json:"cert_thumbprint,omitempty"`
	ShareIndex     int       `json:"share_index"`
	Designated     string    `json:"designated,omitempty"`
	Factors        []string  `json:"factors,omitempty"`
	EncryptionKEM  string    `json:"encryption_kem,omitempty"`
	EnrolledAt     time.Time `json:"enrolled_at"`
	ClaimedAt      time.Time `json:"claimed_at,omitempty"`
	SharePending   bool      `json:"share_pending"`
}

// ShareClaimBeginRequest opens the claim handshake for the named holder. The
// caller must present the holder's certificate on the connection.
type ShareClaimBeginRequest struct {
	Username string `json:"username"`
}

// ShareClaimBeginResponse carries the nonce the holder signs.
type ShareClaimBeginResponse struct {
	NonceHex  string    `json:"nonce_hex"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ShareClaimCompleteRequest completes the claim handshake with the holder's
// signature over the nonce.
type ShareClaimCompleteRequest struct {
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature []byte `json:"signature"`
}

// ShareClaimCompleteResponse delivers the holder's share envelope exactly
// once. Envelope is HPKE-sealed to the holder's enrolled encryption key;
// only the holder can open it, and the server keeps no copy.
type ShareClaimCompleteResponse struct {
	ShareIndex int    `json:"share_index"`
	Envelope   []byte `json:"envelope"`
}

// QuorumBeginRequest asks for the factor challenge a holder answers to
// submit a share to Op in Domain. Factor may be empty under the designated
// policy.
type QuorumBeginRequest struct {
	Domain   string `json:"domain"`
	Op       string `json:"op"`
	Username string `json:"username"`
	Factor   string `json:"factor,omitempty"`
}

// QuorumChallenge is the one-use challenge a holder's factor answers.
type QuorumChallenge struct {
	ChallengeID string            `json:"challenge_id"`
	Domain      string            `json:"domain"`
	Op          string            `json:"op"`
	Username    string            `json:"username"`
	Factor      string            `json:"factor"`
	Nonce       []byte            `json:"nonce"`
	MethodData  map[string]string `json:"method_data,omitempty"`
	ExpiresAt   time.Time         `json:"expires_at"`
}

// QuorumSubmitRequest submits one holder's share, authenticated by the
// factor response to a QuorumChallenge. The server counts each holder once
// per round. Share is the plaintext share the holder recovered from its
// envelope; the caller owns it and must wipe it after the call.
type QuorumSubmitRequest struct {
	Domain           string            `json:"domain"`
	Op               string            `json:"op"`
	Username         string            `json:"username"`
	ChallengeID      string            `json:"challenge_id"`
	Factor           string            `json:"factor"`
	Response         []byte            `json:"response"`
	ResponseMetadata map[string]string `json:"response_metadata,omitempty"`
	Share            []byte            `json:"share"`
}

// QuorumOutcome reports a round's progress after a submission. When the
// round completes, RootToken is set for OpRootToken and
// Threshold/TotalShares/ShareCount describe the new split for OpRekey.
type QuorumOutcome struct {
	Domain             string    `json:"domain"`
	Op                 string    `json:"op"`
	Required           int       `json:"required"`
	Submitted          int       `json:"submitted"`
	Complete           bool      `json:"complete"`
	RootToken          string    `json:"root_token,omitempty"`
	RootTokenCreatedAt time.Time `json:"root_token_created_at,omitempty"`
	Threshold          int       `json:"threshold,omitempty"`
	TotalShares        int       `json:"total_shares,omitempty"`
	ShareCount         int       `json:"share_count,omitempty"`
}

// RekeyStartRequest opens a rekey round on Domain. The rekey completes when
// a fresh M-of-N quorum of the current holders submits to OpRekey; it
// redistributes the key to every holder then enrolled at Threshold under
// Policy.
type RekeyStartRequest struct {
	Domain    string `json:"domain"`
	Threshold int    `json:"threshold"`
	Policy    string `json:"policy,omitempty"`
	SOPin     string `json:"so_pin,omitempty"`
}

// OfficerRevokeRequest revokes a holder. Revocation is a rekey round that
// drops the holder atomically: M of the remaining holders must submit, and
// the key is redistributed to the N-1 who remain at Threshold (the current
// threshold when zero). It is refused when the threshold exceeds N-1.
type OfficerRevokeRequest struct {
	Domain    string `json:"domain"`
	Username  string `json:"username"`
	Threshold int    `json:"threshold,omitempty"`
	SOPin     string `json:"so_pin,omitempty"`
}

// QuorumRound describes an open quorum round.
type QuorumRound struct {
	Domain       string    `json:"domain"`
	Op           string    `json:"op"`
	Required     int       `json:"required"`
	Submitted    int       `json:"submitted"`
	ExpiresAt    time.Time `json:"expires_at"`
	NewThreshold int       `json:"new_threshold,omitempty"`
	Policy       string    `json:"policy,omitempty"`
	Remove       []string  `json:"remove,omitempty"`
}

// ---------------------------------------------------------------------------
// Per-tenant share holders
// ---------------------------------------------------------------------------

// TenantOfficerRegisterRequest enrols a share holder for a tenant barrier.
// The fields after TenantID mean what they mean on OfficerRegisterRequest.
type TenantOfficerRegisterRequest struct {
	TenantID      string                  `json:"tenant_id"`
	Username      string                  `json:"username"`
	Role          string                  `json:"role,omitempty"`
	CSRPEM        string                  `json:"csr_pem"`
	EncryptionKEM string                  `json:"encryption_kem,omitempty"`
	EncryptionKey []byte                  `json:"encryption_key,omitempty"`
	Factors       []HolderFactorEnrolment `json:"factors,omitempty"`
	Designated    string                  `json:"designated,omitempty"`
}

// TenantShareClaimBeginRequest opens a tenant holder's claim handshake.
type TenantShareClaimBeginRequest struct {
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
}

// TenantShareClaimCompleteRequest completes a tenant holder's claim.
type TenantShareClaimCompleteRequest struct {
	TenantID  string `json:"tenant_id"`
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature []byte `json:"signature"`
}

// TenantQuorumBeginRequest asks for a tenant holder's quorum challenge. A
// tenant barrier has only the unseal domain.
type TenantQuorumBeginRequest struct {
	TenantID string `json:"tenant_id"`
	Op       string `json:"op"`
	Username string `json:"username"`
	Factor   string `json:"factor,omitempty"`
}

// TenantQuorumSubmitRequest submits one tenant holder's share; see
// QuorumSubmitRequest.
type TenantQuorumSubmitRequest struct {
	TenantID         string            `json:"tenant_id"`
	Op               string            `json:"op"`
	Username         string            `json:"username"`
	ChallengeID      string            `json:"challenge_id"`
	Factor           string            `json:"factor"`
	Response         []byte            `json:"response"`
	ResponseMetadata map[string]string `json:"response_metadata,omitempty"`
	Share            []byte            `json:"share"`
}

// TenantRekeyStartRequest opens a rekey round on a tenant barrier.
type TenantRekeyStartRequest struct {
	TenantID  string `json:"tenant_id"`
	Threshold int    `json:"threshold"`
	Policy    string `json:"policy,omitempty"`
}

// TenantOfficerRevokeRequest revokes a tenant holder; see
// OfficerRevokeRequest.
type TenantOfficerRevokeRequest struct {
	TenantID  string `json:"tenant_id"`
	Username  string `json:"username"`
	Threshold int    `json:"threshold,omitempty"`
}
