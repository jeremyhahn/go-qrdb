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

package service

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// BarrierServicer provides barrier lifecycle operations including
// initialization, seal/unseal, Shamir share management, rekey,
// recovery, and root token generation.
type BarrierServicer interface {
	// BarrierInitialize initializes the barrier with a secret credential.
	// secret is []byte rather than string because a Go string cannot be
	// zeroized; it is borrowed for the duration of the call and never
	// retained, so the caller is free to wipe it on return.
	BarrierInitialize(ctx context.Context, secret []byte) error

	// BarrierUnseal unseals the barrier with a secret credential. secret is
	// a borrow the caller may wipe on return; see BarrierInitialize.
	BarrierUnseal(ctx context.Context, secret []byte) error

	// BarrierSeal transitions the barrier to the sealed state. Returns
	// ErrShamirBarrierRequiresQuorum when the active strategy is Shamir; a
	// seal quorum (BarrierQuorumSubmit, seal.OpSeal) seals instead. Once the cluster has
	// activated cluster-seal it records the node seal and every node seals
	// its own node barrier.
	BarrierSeal(ctx context.Context) error

	// BarrierClusterUnseal clears a cluster-wide seal of the node barrier.
	// It unseals nothing: this node must already have unsealed itself
	// (seal.ErrSealed otherwise). It returns clusterseal.ErrInactive before
	// the cluster has activated cluster-seal.
	BarrierClusterUnseal(ctx context.Context) (*clusterseal.Report, error)

	// BarrierStatus returns the current barrier status.
	BarrierStatus(ctx context.Context) (*BarrierStatusInfo, error)

	// BarrierInitialized reports whether the barrier has been initialized.
	// Unlike BarrierStatus, a failure to read the initialization record is
	// returned rather than reported as "not initialized", so the initialize
	// paths can refuse an initialized barrier before comparing any PIN and
	// fail closed (ProvisionInitializePINs).
	BarrierInitialized(ctx context.Context) (bool, error)

	// BarrierInitializeShamir initializes the barrier under Shamir custody:
	// the barrier unseal key is split threshold-of-totalShares to the
	// enrolled share holders (totalShares must equal their number), each
	// share sealed into an envelope to its holder. No share material is
	// returned. policy is the factor policy of the share set.
	BarrierInitializeShamir(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*ShamirInitInfo, error)

	// BarrierGenerateRecoveryKeys creates the independent recovery-key
	// domain, split threshold-of-N to the enrolled recovery holders.
	BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*RecoveryKeysInfo, error)

	// BarrierDeleteRecoveryKeys removes the recovery-key domain.
	BarrierDeleteRecoveryKeys(ctx context.Context) error

	// BarrierHasRecoveryKeys reports whether a recovery-key domain exists.
	BarrierHasRecoveryKeys(ctx context.Context) (bool, error)

	// BarrierOfficerRegister enrols a share holder (Security Officer or key
	// custodian) for domain. The CA issues the holder a certificate; no
	// share is produced here.
	BarrierOfficerRegister(ctx context.Context, domain seal.QuorumDomain, req *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error)

	// BarrierOfficerList returns every holder of domain with its claim
	// state. It never includes share material.
	BarrierOfficerList(ctx context.Context, domain seal.QuorumDomain) ([]*BarrierOfficerStatus, error)

	// BarrierShareClaimBegin issues the claim nonce to a holder presenting
	// its enrolled certificate.
	BarrierShareClaimBegin(ctx context.Context, domain seal.QuorumDomain, username, callerCertThumbprint string) (*BarrierShareClaimChallenge, error)

	// BarrierShareClaimComplete verifies the holder's signature over the
	// nonce and delivers its share envelope exactly once.
	BarrierShareClaimComplete(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, callerCertThumbprint string) (*BarrierShareClaimResult, error)

	// BarrierQuorumBegin issues the factor challenge a holder answers to
	// submit a share to op in domain. It works while the barrier is sealed.
	BarrierQuorumBegin(ctx context.Context, req *QuorumBeginRequest) (*QuorumChallengeInfo, error)

	// BarrierQuorumSubmit counts one authenticated holder's share toward the
	// round for op in domain, running op when the round reaches M. Unseal,
	// seal, root token, rekey (and so revocation) and recovery all go
	// through it; no share is ever counted without an authenticated holder.
	BarrierQuorumSubmit(ctx context.Context, req *QuorumSubmitRequest) (*QuorumOutcomeInfo, error)

	// BarrierRekeyStart opens a rekey round for domain; a quorum of the
	// current holders then submits through BarrierQuorumSubmit. soPIN is the
	// barrier SO PIN, verified by the service behind the SO PIN lockout and
	// refused while no SO PIN is provisioned.
	BarrierRekeyStart(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, soPIN string) (*QuorumRoundInfo, error)

	// BarrierOfficerRevoke revokes a holder of domain by opening a rekey
	// round that excludes them; the remaining holders complete it. soPIN is
	// verified as for BarrierRekeyStart.
	BarrierOfficerRevoke(ctx context.Context, domain seal.QuorumDomain, username string, threshold int, soPIN string) (*QuorumRoundInfo, error)
}

// BarrierStatusInfo describes the current state of the storage barrier.
type BarrierStatusInfo struct {
	// Sealed is true when the barrier is in sealed state.
	Sealed bool

	// Initialized is true once the barrier has been initialized. It is the
	// one fact a sealed barrier reports besides Sealed: every other field is
	// zero, and ClusterSeal empty, until the barrier is unsealed.
	Initialized bool

	// Strategy identifies which sealing strategy protects the root key.
	Strategy string

	// HardwareBacked is true when the active strategy uses dedicated
	// cryptographic hardware.
	HardwareBacked bool

	// InitializedAt records when the barrier was first initialized.
	InitializedAt time.Time

	// ShamirThreshold is the minimum number of Shamir shares required to
	// unseal. Zero when the active strategy is not Shamir.
	ShamirThreshold int

	// ShamirTotal is the total number of Shamir shares generated at
	// initialization or last rekey. Zero when the active strategy is not Shamir.
	ShamirTotal int

	// ClusterSeal is the node barrier's cluster seal state. Never nil.
	ClusterSeal *clusterseal.Report
	// ClusterMigration is the cluster-wide encryption migration of the node
	// barrier in progress, read from the replicated record so every node
	// answers the same (WithClusterMigrations). It is nil when none is in
	// progress, and on a sealed node, whose barrier keeps the record
	// unreadable.
	ClusterMigration *clustermig.Report
}

// ShamirInitInfo contains the output of a Shamir initialization or rekey
// operation. Share material is never present here; Security Officers retrieve
// their shares via the BarrierShareClaimBegin / BarrierShareClaimComplete flow.
type ShamirInitInfo struct {
	// Threshold is the minimum number of shares required for reconstruction.
	Threshold int

	// TotalShares is the total number of shares generated.
	TotalShares int

	// ShareCount is the number of shares successfully sealed to officer enclaves.
	ShareCount int
}

// RecoveryKeysInfo contains the output of recovery key generation. Recovery
// share material is never present here; recovery officers retrieve their shares
// via the BarrierRecoveryShareClaimBegin / BarrierRecoveryShareClaimComplete flow.
type RecoveryKeysInfo struct {
	// Threshold is the minimum number of keys required for recovery.
	Threshold int

	// Total is the total number of keys generated.
	Total int

	// ShareCount is the number of recovery shares successfully sealed to officer enclaves.
	ShareCount int
}

// BarrierOfficerRegisterRequest carries the parameters for enrolling a
// share holder.
type BarrierOfficerRegisterRequest struct {
	// Username is the holder's unique identifier in its scope.
	Username string

	// Role is so or custodian; empty means so.
	Role seal.HolderRole

	// CSRPEM is the holder's PEM certificate signing request; the CA
	// assigns the certificate identity.
	CSRPEM []byte

	// EncryptionKEM and EncryptionKey name an optional separate envelope
	// recipient key (HPKE suite name and key encoding).
	EncryptionKEM string
	EncryptionKey []byte

	// Factors are additional authentication factors to enrol.
	Factors []ceremony.FactorEnrolment

	// Designated names the designated factor; empty means "mtls".
	Designated string
}

// BarrierOfficerRegisterResult is the outcome of a successful registration.
type BarrierOfficerRegisterResult struct {
	// CertPEM is the certificate the CA issued to the holder.
	CertPEM []byte

	// Role, Designated, EncryptionKEM and Factors echo what was enrolled.
	Role          seal.HolderRole
	Designated    string
	EncryptionKEM string
	Factors       []string
}

// BarrierOfficerStatus is a non-secret view of an enrolled share holder.
type BarrierOfficerStatus = ceremony.OfficerStatus

// BarrierShareClaimChallenge is the challenge issued by BarrierShareClaimBegin.
type BarrierShareClaimChallenge struct {
	// NonceHex is the hex-encoded random challenge bytes the holder signs.
	NonceHex string

	// ExpiresAt is the deadline by which the holder must complete the claim.
	ExpiresAt time.Time
}

// BarrierShareClaimResult is returned to the holder exactly once.
type BarrierShareClaimResult struct {
	// ShareIndex is the holder's share index.
	ShareIndex int

	// Envelope is the holder's share envelope (ceremony.ShareEnvelope JSON),
	// sealed to the holder's key. It is excluded from struct marshalling and
	// must only traverse the holder's own session.
	Envelope []byte `json:"-"`
}

// QuorumBeginRequest asks for a quorum challenge.
type QuorumBeginRequest struct {
	Domain   seal.QuorumDomain
	Op       seal.QuorumOp
	Username string
	// Factor selects the factor; empty means the holder's designated one.
	Factor string
}

// QuorumChallengeInfo is the challenge a holder answers.
type QuorumChallengeInfo = ceremony.QuorumChallenge

// QuorumSubmitRequest is one holder's share with its factor response.
type QuorumSubmitRequest struct {
	Domain           seal.QuorumDomain
	Op               seal.QuorumOp
	Username         string
	ChallengeID      string
	Factor           string
	Response         []byte
	ResponseMetadata map[string]string

	// PeerCertThumbprint is set by the TRANSPORT from the verified TLS
	// session, never from the request body.
	PeerCertThumbprint string

	// Share is the base64 share value from the holder's envelope. It is
	// consumed (wiped) by the submission.
	Share []byte
}

// QuorumOutcomeInfo reports a submission's effect.
type QuorumOutcomeInfo struct {
	Domain    seal.QuorumDomain
	Op        seal.QuorumOp
	Required  int
	Submitted int
	Complete  bool

	// RootToken is set when an OpRootToken round completed.
	RootToken *RootTokenInfo

	// Rekey is set when an OpRekey round completed.
	Rekey *ShamirInitInfo
}

// RootTokenInfo represents a generated root token.
type RootTokenInfo struct {
	// Token is the hex-encoded root token value.
	Token string

	// CreatedAt records when the token was generated.
	CreatedAt time.Time
}

// QuorumRoundInfo is the public state of an open round.
type QuorumRoundInfo = seal.QuorumRoundInfo

// BarrierService implements BarrierServicer by delegating to a
// StorageBarrier instance from the seal package and, optionally, to a
// CeremonyService for Security Officer share distribution.
type BarrierService struct {
	barrier  *seal.StorageBarrier
	ceremony *ceremony.CeremonyService
	sealer   clusterseal.Sealer
	// registry, when set, makes a seal of the node barrier seal the whole
	// node: every tenant barrier with it (WithBarrierRegistry).
	registry *seal.BarrierRegistry
	// databases, when set, has a node seal mark every barrier-enabled
	// database sealed (WithBarrierDatabases).
	databases BarrierDatabases
	// shards, when set, has a node seal pause every Raft shard the node
	// hosts before it wipes a key (WithShardPauser).
	shards ShardPauser
	// migrations, when set, has the status report the node barrier's
	// cluster-wide encryption migration (WithClusterMigrations).
	migrations ClusterMigrations
}

// Compile-time interface check.
var _ BarrierServicer = (*BarrierService)(nil)

// NewBarrierService creates a new BarrierServicer backed by the given
// StorageBarrier.
func NewBarrierService(barrier *seal.StorageBarrier, opts ...BarrierServiceOption) BarrierServicer {
	s := &BarrierService{barrier: barrier}
	for _, o := range opts {
		o(s)
	}
	return s
}

// NewBarrierServiceWithCeremony creates a new BarrierServicer backed by the
// given StorageBarrier and its share-holder authority. The CeremonyService
// installed itself as the barrier's distributor and holder verifier when it
// was constructed.
func NewBarrierServiceWithCeremony(barrier *seal.StorageBarrier, svc *ceremony.CeremonyService, opts ...BarrierServiceOption) BarrierServicer {
	s := &BarrierService{barrier: barrier, ceremony: svc}
	for _, o := range opts {
		o(s)
	}
	return s
}

// BarrierInitialize is refused when this BarrierService was constructed
// with a ceremony (NewBarrierServiceWithCeremony, s.ceremony != nil): that
// shape is qrdbd's own system barrier, which `qrdb init` -- the only
// system bootstrap -- brings up itself, in the sealed window, before
// anything is unsealed, issuing the bootstrap admin's credential in the
// same call. A standalone `barrier init` against it would run outside
// that window with no ceremony and no admin-credential issuance, so it
// is refused with a typed *SystemBarrierInitRefusedError naming the
// replacement.
//
// A BarrierService built WITHOUT a ceremony (plain NewBarrierService) has
// no such model at all -- the embedded SDK's barrier (sdk/go/transport/
// embedded), for one, is a library-level encryption barrier with no
// multi-tenant RBAC, no first-boot admin binding, and no ceremony
// concept to merge into -- so it delegates to the real
// StorageBarrier.Initialize unchanged, exactly as it always has.
func (s *BarrierService) BarrierInitialize(ctx context.Context, secret []byte) error {
	if s.ceremony != nil {
		return &SystemBarrierInitRefusedError{}
	}
	creds, err := seal.CredentialsFor(ctx, secret)
	if err != nil {
		return err
	}
	defer seal.DestroyCredentials(creds)
	return s.barrier.Initialize(ctx, creds)
}

// BarrierUnseal unseals the barrier with a secret credential by delegating
// to the StorageBarrier's Unseal method. secret is BORROWED, as in
// BarrierInitialize.
func (s *BarrierService) BarrierUnseal(ctx context.Context, secret []byte) error {
	creds, err := seal.CredentialsFor(ctx, secret)
	if err != nil {
		return err
	}
	defer seal.DestroyCredentials(creds)
	return s.barrier.Unseal(ctx, creds)
}

// BarrierSeal transitions the barrier to the sealed state. For Shamir-protected
// barriers the single-admin seal path is blocked; a seal quorum of holders
// (BarrierQuorumSubmit with seal.OpSeal) seals instead.
//
// Once the cluster has activated cluster-seal the seal is recorded first and
// then enforced -- on this node by the sealer, on every other node by its own
// monitor. Before activation only this node is sealed.
func (s *BarrierService) BarrierSeal(ctx context.Context) error {
	if s.barrier.ActiveStrategy() == seal.StrategyShamir {
		return seal.ErrShamirBarrierRequiresQuorum
	}
	return s.seal(ctx)
}

// BarrierInitialized reports whether the barrier has been initialized,
// reading the persisted record on a barrier this process did not initialize.
// A read failure is returned, never reported as "not initialized", so a caller
// gating on the answer (ProvisionInitializePINs) fails closed.
func (s *BarrierService) BarrierInitialized(ctx context.Context) (bool, error) {
	return s.barrier.Initialized(ctx)
}

// BarrierStatus returns the current barrier status, mapping the seal
// package's BarrierStatus to the service-layer BarrierStatusInfo type.
//
// A sealed barrier answers only that it is sealed and whether it is
// initialized, the flag a sealed node serves on its sealed surface. The flag
// comes from memory once the barrier has read its metadata, which the server
// does at boot; a barrier that has not reads it here once and remembers it.
// Nothing else about the barrier is disclosed until it is unsealed.
func (s *BarrierService) BarrierStatus(ctx context.Context) (*BarrierStatusInfo, error) {
	if s.barrier.IsSealed() {
		return s.sealedStatus(ctx)
	}
	status := s.barrier.Status()
	if status.Sealed {
		return s.sealedStatus(ctx)
	}
	return &BarrierStatusInfo{
		Sealed:           status.Sealed,
		Initialized:      true,
		Strategy:         string(status.Strategy),
		HardwareBacked:   status.HardwareBacked,
		InitializedAt:    status.InitializedAt,
		ShamirThreshold:  status.ShamirThreshold,
		ShamirTotal:      status.ShamirTotal,
		ClusterSeal:      clusterseal.ReportOf(s.sealer, clusterseal.NodeBarrier()),
		ClusterMigration: s.nodeMigration(ctx),
	}, nil
}

// migrationReportTimeout bounds the replicated read behind a status's
// migration report, so a node without a quorum still answers its status
// promptly, with the migration unconfirmed.
const migrationReportTimeout = 2 * time.Second

// nodeMigration reports the node barrier's migration: the system scope, an
// empty tenant and database. A record that cannot be read just now is
// reported unconfirmed, not absent, and does not fail the status.
func (s *BarrierService) nodeMigration(ctx context.Context) *clustermig.Report {
	if s.migrations == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, migrationReportTimeout)
	defer cancel()
	r, err := s.migrations.Report(ctx, "", "")
	if err != nil {
		return &clustermig.Report{Unconfirmed: true}
	}
	return r
}

// sealedStatus is a sealed barrier's whole status: sealed, and whether it is
// initialized.
func (s *BarrierService) sealedStatus(ctx context.Context) (*BarrierStatusInfo, error) {
	initialized, known := s.barrier.InitializedCached()
	if !known {
		var err error
		if initialized, err = s.barrier.Initialized(ctx); err != nil {
			return nil, err
		}
	}
	return &BarrierStatusInfo{Sealed: true, Initialized: initialized, ClusterSeal: &clusterseal.Report{}}, nil
}

// BarrierInitializeShamir initializes the barrier under Shamir custody. The
// request's threshold and share count win over the daemon configuration.
//
// Refused on the same shape, and for the same reason, as BarrierInitialize
// (go-qrdb#548): a BarrierService built with a ceremony is qrdbd's own
// system barrier, which `qrdb init` -- the only system bootstrap -- brings
// up itself, in the sealed window, issuing the bootstrap admin's
// credential in the same call. A standalone `barrier init-shamir` against
// it would run outside that window with no ceremony and no
// admin-credential issuance (go-qrdb#560 review item 8).
func (s *BarrierService) BarrierInitializeShamir(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*ShamirInitInfo, error) {
	if s.ceremony != nil {
		return nil, &SystemBarrierInitRefusedError{}
	}
	if threshold != 0 || totalShares != 0 {
		s.barrier.SetShamirConfig(&seal.ShamirConfig{Threshold: threshold, TotalShares: totalShares})
	}
	initialize := s.barrier.InitializeShamir
	if s.ceremony != nil {
		initialize = s.ceremony.InitializeShamir
	}
	result, err := initialize(ctx, policy)
	if err != nil {
		return nil, err
	}
	return &ShamirInitInfo{Threshold: result.Threshold, TotalShares: result.TotalShares, ShareCount: result.ShareCount}, nil
}

// BarrierGenerateRecoveryKeys creates the recovery-key domain. With a
// ceremony service it generates through the recovery holder authority
// (ceremony.CeremonyService.GenerateRecoveryKeys), which refuses a recovery
// holder registration or unenrolment until the generation has finished.
func (s *BarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*RecoveryKeysInfo, error) {
	generate := s.barrier.GenerateRecoveryKeys
	if s.ceremony != nil {
		generate = s.ceremony.GenerateRecoveryKeys
	}
	result, err := generate(ctx, threshold, policy)
	if err != nil {
		return nil, err
	}
	return &RecoveryKeysInfo{Threshold: result.Threshold, Total: result.TotalShares, ShareCount: result.ShareCount}, nil
}

// BarrierDeleteRecoveryKeys removes the recovery-key domain.
func (s *BarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	return s.barrier.DeleteRecoveryKeys(ctx)
}

// BarrierHasRecoveryKeys reports whether a recovery-key domain exists.
func (s *BarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	return s.barrier.HasRecoveryKeys(ctx)
}

// BarrierOfficerRegister enrols a share holder. The authenticated caller is
// recorded as the enroller, who may not enrol itself.
func (s *BarrierService) BarrierOfficerRegister(ctx context.Context, domain seal.QuorumDomain, req *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}
	if req == nil {
		return nil, ErrNilRequest
	}
	result, err := s.ceremony.RegisterOfficer(ctx, domain, HolderRegistration(ctx, req))
	if err != nil {
		return nil, err
	}
	return &BarrierOfficerRegisterResult{
		CertPEM:       result.CertPEM,
		Role:          result.Role,
		Designated:    result.Designated,
		EncryptionKEM: result.EncryptionKEM,
		Factors:       result.Factors,
	}, nil
}

// HolderRegistration builds the ceremony registration for req, recording the
// authenticated caller in ctx as the enroller. Tenant holder registration
// uses it too, so the self-enrolment rule is enforced identically.
func HolderRegistration(ctx context.Context, req *BarrierOfficerRegisterRequest) ceremony.RegisterOfficerRequest {
	out := ceremony.RegisterOfficerRequest{
		Username:   req.Username,
		Role:       req.Role,
		CSRPEM:     req.CSRPEM,
		Factors:    req.Factors,
		Designated: req.Designated,
	}
	if req.EncryptionKEM != "" || len(req.EncryptionKey) > 0 {
		out.EncryptionKey = &ceremony.EncryptionKeyEnrolment{KEM: req.EncryptionKEM, PublicKey: req.EncryptionKey}
	}
	if id := auth.IdentityFromContext(ctx); id != nil {
		out.Enroller = id.Subject
	}
	return out
}

// BarrierOfficerList returns every holder of domain.
func (s *BarrierService) BarrierOfficerList(ctx context.Context, domain seal.QuorumDomain) ([]*BarrierOfficerStatus, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}
	return s.ceremony.ListOfficers(ctx, domain)
}

// BarrierShareClaimBegin issues the claim nonce to a holder.
func (s *BarrierService) BarrierShareClaimBegin(ctx context.Context, domain seal.QuorumDomain, username, callerCertThumbprint string) (*BarrierShareClaimChallenge, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}
	challenge, err := s.ceremony.BeginClaimShare(ctx, domain, username, callerCertThumbprint)
	if err != nil {
		return nil, err
	}
	return &BarrierShareClaimChallenge{NonceHex: hex.EncodeToString(challenge.Nonce), ExpiresAt: challenge.ExpiresAt}, nil
}

// BarrierShareClaimComplete delivers the holder's share envelope exactly once.
func (s *BarrierService) BarrierShareClaimComplete(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, callerCertThumbprint string) (*BarrierShareClaimResult, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}
	result, err := s.ceremony.CompleteClaimShare(ctx, domain, username, nonceHex, signature, callerCertThumbprint)
	if err != nil {
		return nil, err
	}
	return &BarrierShareClaimResult{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
}

// BarrierQuorumBegin issues a quorum challenge.
func (s *BarrierService) BarrierQuorumBegin(ctx context.Context, req *QuorumBeginRequest) (*QuorumChallengeInfo, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}
	if req == nil {
		return nil, ErrNilRequest
	}
	return s.ceremony.BeginQuorum(ctx, req.Domain, req.Op, req.Username, req.Factor)
}

// BarrierQuorumSubmit counts one authenticated holder's share. A completed
// seal round seals the node barrier as BarrierSeal does (seal): once the
// cluster has activated cluster-seal the seal is recorded first and every
// node seals the whole node; before activation this node is sealed whole.
func (s *BarrierService) BarrierQuorumSubmit(ctx context.Context, req *QuorumSubmitRequest) (*QuorumOutcomeInfo, error) {
	if req == nil {
		return nil, ErrNilRequest
	}
	out, err := s.barrier.SubmitHolderShareWith(ctx, &seal.HolderSubmission{
		Domain: req.Domain,
		Op:     req.Op,
		Holder: req.Username,
		Proof: seal.HolderProof{
			ChallengeID:        req.ChallengeID,
			Factor:             req.Factor,
			Response:           req.Response,
			ResponseMetadata:   req.ResponseMetadata,
			PeerCertThumbprint: req.PeerCertThumbprint,
		},
		Share: req.Share,
	}, func() error { return s.seal(ctx) })
	if err != nil {
		return nil, err
	}
	return QuorumOutcomeFrom(out), nil
}

// QuorumOutcomeFrom maps a seal outcome onto the service type.
func QuorumOutcomeFrom(out *seal.QuorumOutcome) *QuorumOutcomeInfo {
	info := &QuorumOutcomeInfo{
		Domain:    out.Domain,
		Op:        out.Op,
		Required:  out.Required,
		Submitted: out.Submitted,
		Complete:  out.Complete,
	}
	if out.RootToken != nil {
		info.RootToken = &RootTokenInfo{Token: out.RootToken.Token, CreatedAt: out.RootToken.CreatedAt}
	}
	if out.Rekey != nil {
		info.Rekey = &ShamirInitInfo{Threshold: out.Rekey.Threshold, TotalShares: out.Rekey.TotalShares, ShareCount: out.Rekey.ShareCount}
	}
	return info
}

// BarrierRekeyStart opens a rekey round for domain.
//
// soPIN is verified here, behind the node's one SO PIN lockout in the PIN
// backend, so every transport gets the same check
// (ceremony.CeremonyService.AuthorizeSOPIN).
func (s *BarrierService) BarrierRekeyStart(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, soPIN string) (*QuorumRoundInfo, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}
	if err := s.ceremony.AuthorizeSOPIN(ctx, soPIN); err != nil {
		return nil, err
	}
	return s.ceremony.StartRekey(ctx, domain, seal.RekeyParams{Threshold: threshold, Policy: policy})
}

// BarrierOfficerRevoke revokes a holder of domain through a quorum-gated
// rekey. soPIN is verified as for BarrierRekeyStart.
func (s *BarrierService) BarrierOfficerRevoke(ctx context.Context, domain seal.QuorumDomain, username string, threshold int, soPIN string) (*QuorumRoundInfo, error) {
	if s.ceremony == nil {
		return nil, ErrCeremonyNotConfigured
	}
	if err := s.ceremony.AuthorizeSOPIN(ctx, soPIN); err != nil {
		return nil, err
	}
	return s.ceremony.RevokeOfficer(ctx, domain, username, threshold)
}
