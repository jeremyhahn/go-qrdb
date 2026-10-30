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

// Package lifecycle provides shared service construction for qrdbd daemon
// and embedded GUI modes. BuildServices accepts already-initialised
// infrastructure (Host, WatchHub, optional seal barrier) and constructs the
// complete service graph, returning a Node that aggregates all services and
// their closers.
//
// This is Step 1 of the lifecycle refactor: service construction only. TLS
// bootstrap, storage open, and barrier init remain with callers for now and
// will be moved in subsequent steps.
package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync/atomic"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Infra holds already-initialised infrastructure that BuildServices requires.
// Callers are responsible for constructing each field before calling
// BuildServices: storage backends, Raft host, watch hub, and (when seal is
// enabled) barrier and registry.
type Infra struct {
	// Host is the node host (Raft adapter or LocalBackend), both its data
	// host and its Raft-admin host: the node composes the admin, rebalance
	// and snapshot services over it as well as the data services. Required.
	Host service.AdminHost

	// NoRevisionHistory declares that the system shard's state machine keeps
	// no MVCC revision history (see StateMachineKeepsRevisionHistory). The
	// MVCC service then reports a lease-attached put without reading it back
	// (service.WithoutRevisionHistory), and revision-answering surfaces
	// refuse to serve over it.
	NoRevisionHistory bool

	// WatchHub delivers change events between the state machine and watchers.
	// Required.
	WatchHub *watch.Hub

	// DataDir is the base data directory used for vector storage, database
	// metadata, and (when SealBarrier is non-nil) the certificate store.
	// Required unless Backend is database.BackendMemory.
	DataDir string

	// Backend is this node's primary KV engine's backend type
	// (database.BackendMemory, BackendPebble, BackendFile, BackendObjstore,
	// BackendRaft). BuildServices uses it, not DataDir's emptiness, to
	// decide whether vector storage follows the primary engine into
	// memory (go-qrdb#578): the zero value is NOT database.BackendMemory,
	// so a caller that leaves this unset gets the persistent (Pebble) path
	// and, with it, DataDir's existing "required" contract enforced -- a
	// caller requesting a persistent backend with no DataDir is refused
	// with EmptyVectorDataDirError rather than silently handed an
	// in-memory vector store a restart or a failover would lose. Only an
	// EXPLICIT database.BackendMemory opts into the in-memory vector path;
	// leaving DataDir empty is not, on its own, a declaration of intent.
	Backend database.BackendType

	// DatabaseManager is an already-open database manager. When non-nil it
	// is used directly; when nil BuildServices creates a new manager rooted
	// at DataDir. Callers that create the manager themselves (e.g. to run
	// migrations before service construction) should pass it here.
	DatabaseManager *database.Manager

	// KVOptions are optional configuration options applied when constructing
	// the KVService. Used for codec injection in daemon mode.
	KVOptions []service.KVServiceOption

	// SealBarrier is the initialised storage barrier. When non-nil, the
	// barrier, credential, ceremony, cert, and tenant services are wired.
	// When nil, those services are omitted from the Node and will be nil.
	SealBarrier *seal.StorageBarrier

	// SealEnabled records the caller's own configuration decision (its
	// seal.enabled equivalent) independent of whether SealBarrier ended up
	// non-nil. A composer sets this from the config value it read BEFORE
	// attempting to construct a barrier, never by inspecting SealBarrier
	// afterward: a barrier that was requested but failed to construct
	// (a bad passphrase, a corrupt seal store, a strategy error) leaves
	// SealBarrier nil too, and SealEnabled is what keeps that construction
	// failure from being reported as an operator's decision to run without
	// encryption. Defaults to false (matching seal.enabled's own default)
	// for composers that never intended a barrier at all; a composer for
	// which "no barrier" is not a state it ever chooses (for example, one
	// that always builds at least a none-strategy barrier) should set this
	// true unconditionally so the plaintext-at-rest warning below never
	// misattributes one of ITS failures either.
	SealEnabled bool

	// Encryptor is the single at-rest encryption authority for all Pebble
	// stores opened by BuildServices (vector storage, etc.). When nil,
	// BuildServices derives a no-op Encryptor from SealBarrier so that
	// callers that only set SealBarrier continue to work unchanged.
	// Prefer passing an Encryptor built by the daemon once so all stores
	// share the same instance.
	Encryptor *atrest.Encryptor

	// BarrierRegistry manages per-tenant barrier lifecycle. Required when
	// SealBarrier is non-nil.
	BarrierRegistry *seal.BarrierRegistry

	// RaftAddrToNodeID maps this cluster's known Raft addresses to their
	// decimal replica-ID string (the same form volume.NodeReport.NodeID
	// carries), built from whatever topology source the composer has at
	// boot time -- the static --peers map for static discovery, or
	// cluster.Member.NodeID/Address for dynamic membership. It is the seam
	// the volume-pool-health ReplicaFilter (security review item 5) uses
	// to translate a placement candidate's raftAddr into the NodeID a
	// pool's per-node health report is keyed by. Nil or a miss is treated
	// as "cannot verify" by that filter, not as "healthy" -- see
	// newVolumePoolHealthFilter's own doc comment.
	RaftAddrToNodeID map[string]string

	// StrategyRegistry is the canonical name→factory lookup for sealing
	// strategies. When BarrierKeyProvider is nil, the TenantService resolves
	// per-tenant barrier strategies through seal.NewBarrierKeyProvider over
	// this registry. When both are nil it knows only the built-in software
	// strategies (passphrase, none).
	StrategyRegistry seal.StrategyRegistry

	// BarrierKeyProvider is the seam through which this node obtains the
	// strategy that seals its own key for a tenant barrier. A composer
	// (go-xkms) injects a hardware- or KMS-backed provider here; it wins
	// over StrategyRegistry.
	BarrierKeyProvider seal.BarrierKeyProvider

	// CredentialCoreSvc is the core credential service built on top of the
	// SealBarrier. Required when SealBarrier is non-nil.
	CredentialCoreSvc *credentials.CredentialService

	// CeremonyDataDir is the data directory for the ceremony service. When
	// empty and SealBarrier is non-nil, it defaults to DataDir/seal.
	CeremonyDataDir string

	// CeremonyCA is an optional certificate authority used to sign Security
	// Officer CSRs outside the full initialization ceremony. When non-nil it
	// is wired into the ceremony service via SetCA, which never overwrites a
	// CA already set. Nil on a joiner, a founder restart and a BYO-TLS node:
	// the sealed window then serves only the holder operations that verify,
	// and BuildServices installs Key A via ReplaceCA once it is readable
	// after unseal. Daemon deployments pass the
	// node's cluster CA here so officer certificates chain to the same CA
	// the server trusts for mTLS client authentication.
	CeremonyCA ceremony.CeremonyCA

	// ServerCertPEM is this node's own TLS server (leaf) certificate PEM,
	// threaded into ceremony.CeremonyConfig.ServerCertPEM so qrdb init can
	// compute its SPKI pin from it on a BYO-TLS node (CeremonyCA nil,
	// because ceremonyCAFromBundle has no CA private key to issue a
	// different certificate with). Ignored when CeremonyCA is non-nil: the
	// CA self-issues its own TLS certificate in that case, unchanged.
	ServerCertPEM []byte

	// ClientCACertPEM is this node's configured client-authentication
	// trust anchor PEM, threaded into
	// ceremony.CeremonyConfig.ClientCACertPEM so the bring-your-own-
	// certificate admin-credential path has a pool to validate against on
	// a BYO-TLS node. Ignored when CeremonyCA is non-nil.
	ClientCACertPEM []byte

	// CeremonyCoreSvc is an already-constructed core ceremony service to
	// reuse instead of building a new one. The sealed-window boot phase
	// (BuildSealServicesOnly) accumulates in-memory state — enclave-stored
	// shares and cache-only officer registrations — that must survive into
	// the post-unseal service graph; threading the instance here preserves
	// it. When nil a fresh ceremony service is constructed.
	CeremonyCoreSvc *ceremony.CeremonyService

	// ClusterTrustDomain is the node's effective SPIFFE trust domain
	// (auth.cluster_trust_domain, or its default when unset). The
	// certificate provider (cert.WithTrustDomain), certificate enrollment,
	// the bootstrap admin and the ceremony (SetTrustDomain) mint every
	// tenant-principal certificate with the SPIFFE ID SAN under it that
	// agrees with its OU, so the mTLS authenticator's pinned derivation
	// resolves the certificates this node issues. It must be the value the
	// authenticator pins. Empty mints none: every issuer refuses instead.
	ClusterTrustDomain string

	// CertKeyProviderFactory, when non-nil, replaces Key B's built-in
	// per-tenant cakey.SoftwareProvider entirely (the CA key provider seam,
	// cert.WithKeyProviderFactory): every client-certificate signature is
	// routed through it, resolved fresh per tenant, and no software key is
	// ever generated or stored under __sys:cert_ca/ for any tenant. An
	// embedder (go-xkms) injects an HSM/TPM/KMS-backed factory here via
	// server.WithCertKeyProviderFactory, resolved before BuildServices runs
	// (see CryptoOptions) for the same reason StrategyRegistry is: the
	// factory must be in place before the CertProvider is constructed
	// below, not after.
	CertKeyProviderFactory cakey.ProviderFactory

	// CertProvider, when non-nil, replaces the built-in
	// cert.DefaultCertProvider -- the login CA (Key B) that issues, revokes,
	// lists and bundles client certificates -- with an embedder's own
	// (go-xkms's CRL/OCSP-capable CA), on every transport and in the
	// client-trust pool. It is used where the built-in one would be built, on
	// a node with a barrier registry. The client-trust pool admits its
	// certificates only when it also implements clienttrust.CertSource.
	// Mutually exclusive with CertKeyProviderFactory, which configures the
	// built-in provider only: BuildServices refuses both with
	// *CertProviderConflictError.
	CertProvider cert.CertProvider

	// TrustAnchors, when non-nil, replaces the built-in per-tenant trust
	// anchor store (trustanchor.NewKVRegistry over the replicated system
	// store): every tenant's registered anchors -- what a bring-your-own
	// login certificate must chain to -- are read and managed through it. An
	// embedder (go-xkms) injects its own trust store through
	// server.WithTrustAnchors (resolved by server.ResolveCryptoOptions and
	// forwarded here by the SDK node builders) and a thin
	// trustanchor.Registry adapter.
	TrustAnchors trustanchor.Registry

	// MTLSBindingPolicy is the operator's auth.mtls_binding_policy, as
	// configured. Under "required" the user service refuses every change that
	// would leave the bootstrap admin unable to log in by certificate
	// (user.WithBootstrapAdminBindingRequired), so no transport can lock the
	// admin out. An unrecognised value fails BuildServices. Empty means
	// "enrolled".
	MTLSBindingPolicy string

	// SeparationOfDuties is the strict separation-of-duties posture, as
	// wiring.ResolveStrictSeparation resolves it from auth.separation_of_duties
	// and FIPS 140-3 mode. Tenant onboarding refuses, under it, a tenant SO
	// that shares the first admin's username or key.
	SeparationOfDuties bool
	// PINBackend holds the node's SO and User PINs. Composers wire either
	// an injected (hardware) backend or NewSealStorePINBackend over the
	// node-local seal store. When nil every PIN-gated operation fails
	// closed with pin.ErrBackendNotConfigured: there is no fallback.
	PINBackend pin.PINBackend

	// FactorRegistry resolves the authentication factors share holders
	// enrol and answer quorum challenges with. The built-in mtls factor is
	// always available; hardware factors (FIDO2, PIV, ...) are registered
	// by the composer.
	FactorRegistry *authfactors.FactorRegistry

	// AuditLogger is the node's compliance audit trail. The ceremony service
	// records every share-holder event on it -- system, recovery, tenant and
	// database scopes alike, each keyed by its owning tenant -- so a composer
	// that serves a sealed window passes the SAME trail to
	// BuildSealServicesOnly as to BuildServices, opened before the window:
	// that is where holders claim envelopes and unseal quorums are gathered.
	// Nil records no holder events.
	AuditLogger audit.AuditLogger

	// AuditFailurePolicy is the node's audit failure posture
	// (auth.audit_failure_policy). A holder registration, claim and rekey
	// opening are recorded before they take effect and, under FailClosed --
	// the zero value -- refused when the trail cannot record them.
	AuditFailurePolicy audit.FailurePolicy

	// ReplicatedAuditPlatformEnabled governs whether the system tenant's own
	// platform events (those carrying no tenant at all) are additionally
	// written to the cluster-wide replicated audit chain
	// (docs/audit/architecture.md#cluster-wide-replicated-trail). An
	// ordinary tenant's events are governed by its own
	// quota.Entitlements.AllowReplicatedAudit instead -- the system tenant
	// is never provisioned through that per-tenant path. Defaults to false:
	// the replicated chain is opt-in, matching the per-tenant entitlement's
	// own secure default.
	ReplicatedAuditPlatformEnabled bool

	// InvitationTTL is auth.invitation_ttl, the raw configured value: how
	// long an invitation (users:invite) stays redeemable. Empty takes
	// config.DefaultInvitationTTLString; parsed by buildEnrollmentServices
	// (config.ParseInvitationTTL), the same validation Config.Validate
	// already applies.
	InvitationTTL string

	// InvitationRedeemLimit is auth.invitation_redeem_limit: the per-source
	// and per-tenant policies the redemption throttle enforces. Zero fields
	// take the config defaults.
	InvitationRedeemLimit qrdbconfig.InvitationRedeemLimitConfig

	// InvitationRequireKeyPin is auth.invitation_require_key_pin: when true,
	// every invitation must be pinned to the invited user's key, for
	// compliance profiles that must never issue a bearer-only token.
	InvitationRequireKeyPin bool

	// Logger is used for service-level structured logging. When nil,
	// slog.Default() is used.
	Logger *slog.Logger

	// EmbeddingConfig configures the embedding provider registry. When
	// Providers is empty no embedding service is constructed and Node.Embedding
	// remains nil.
	EmbeddingConfig qrdbconfig.EmbeddingConfig

	// RAGConfig configures the Retrieval-Augmented Generation pipeline.
	// Node.RAG is only non-nil when EmbeddingConfig.Providers is non-empty and
	// a vector service is available.
	RAGConfig qrdbconfig.RAGConfig

	// ImportSession bounds a chunked import session (config.ImportSession,
	// "import_session:" in YAML): a session's total staged bytes, its chunk
	// count, a tenant's concurrent open sessions, and the session lease's
	// TTL. The zero value resolves to the same package defaults
	// WithImportSessionLimits documents, so a caller that leaves this unset
	// gets production bounds, not unbounded staging.
	ImportSession qrdbconfig.ImportSessionConfig

	// ReplicaID is this node's Raft replica identifier (config.Raft.ReplicaID).
	// It seeds the coordination-free Snowflake generator that assigns uint64
	// user IDs, keeping IDs globally unique across cluster members. Defaults
	// to 1 (standalone) when zero. Must be <= 1023 (the Snowflake node field).
	ReplicaID uint64

	// DefaultTenantQuotas are the operator-configured default resource quotas
	// applied to tenants whose own quotas are unset. When nil, nil-quota
	// tenants fall back to the zero-value Quotas (all limits at system default).
	DefaultTenantQuotas *quota.Quotas

	// DefaultTenantEntitlements are the operator-configured default feature
	// entitlements applied to tenants whose own entitlements are unset. When
	// nil, nil-entitlement tenants fall back to the conservative built-in
	// defaults (prefix-only isolation; dedicated raft/volume denied).
	DefaultTenantEntitlements *quota.Entitlements

	// Profile, when non-nil, is the effective capability profile for this
	// node (build tags x run mode x config, config.EffectiveCapabilities).
	// It is pinned onto the AdminService so the node reports its own truth
	// even when several nodes share one process (the SDK). When nil, the
	// AdminService falls back to the process-global capabilities view.
	Profile *capabilities.Capabilities

	// BackupSink, when non-nil, is wired into the Backup service so backup
	// artifacts (shard payloads + metadata) are persisted durably instead of
	// held only in the daemon process's memory. Without it, backups do not
	// survive a process restart, which makes them unusable for disaster
	// recovery. The caller constructs and owns the sink's lifecycle (e.g. the
	// storage.Storage it wraps); BuildServices only reads from it.
	BackupSink service.BackupSink

	// DEKProvider, when non-nil, replaces the software dek.Keyring as the
	// engine behind the tenant DEK service. It is the seam a composer fills
	// with a device-backed implementation that holds each tenant's keys
	// without exporting them (go-xkms hardware, composed by go-quicbaas). Nil
	// keeps the software keyring, wrapped through KeyWrapRegistry /
	// KeyWrapProvider. keywrap.Provider stays the key-wrap seam; this is the
	// DEK-engine seam beside it.
	DEKProvider dek.Provider

	// HeadSignerProvider, when non-nil, creates every verifiable log's
	// head-signing key and signs its heads. It is the seam a composer fills
	// with a provider that keeps the key on a device or in a KMS and hands
	// back only its reference and public key (go-xkms hardware, composed by
	// go-quicbaas). Nil keeps the software provider over each log's own
	// sealed k/<version> record.
	HeadSignerProvider vlog.HeadSignerProvider

	// Clock, when non-nil, is the time source for the node's security time
	// gates: lease expiry, key-delivery freshness and replay window, the
	// invitation-redeem and holder-act limiters. Nil is the real clock.
	Clock clock.Clock

	// InvitationRedeemLimiter, when non-nil, throttles invitation redemption
	// in place of the built-in per-node token bucket. It is asked once per
	// source address and once per tenant, under the policies the config
	// resolves. BuildServices does not close an injected limiter; its owner
	// does.
	InvitationRedeemLimiter ratelimit.Limiter

	// HolderActLimiter, when non-nil, throttles share-holder acts (claim,
	// approve, ceremony steps) in place of the built-in per-node token
	// bucket. BuildServices does not close an injected limiter.
	HolderActLimiter ratelimit.Limiter

	// KeyWrapRegistry is the canonical name->factory lookup for key-wrap
	// (KEK) providers the Backup service resolves BackupOptions.
	// KeyProviderID through. qrdb provides a default registry holding only
	// the built-in "software" provider (confined to KeyDir, below); go-xkms
	// and other embedders register hardware or cloud-KMS providers into
	// their own registry and inject it here -- the same WithStrategyRegistry
	// composition shape, applied to key wrapping instead of sealing.
	KeyWrapRegistry keywrap.Registry

	// KeyWrapProvider is the narrower sibling of KeyWrapRegistry for an
	// embedder with exactly one additional provider and no registry of its
	// own: it is registered into the default registry under its own
	// ProviderID(). Mutually composable with KeyWrapRegistry left nil.
	KeyWrapProvider keywrap.Provider

	// KeyDir is the directory the built-in "software" key-wrap provider's
	// key files must live inside, confined via *os.Root
	// (keywrap.NewSoftwareProvider) -- every key file BackupOptions.KeyRef
	// / RestoreOptions.DecryptionKeyRef names is a bare file name inside
	// it, never a path (go-qrdb security review, backup-encrypt-default
	// #B4). Only consulted when KeyWrapRegistry is nil. Empty leaves the
	// built-in "software" provider unusable (BackupKeyDirNotConfiguredError
	// on first use) without disabling encrypted backups altogether -- a
	// composer that supplies KeyWrapRegistry or KeyWrapProvider needs no
	// KeyDir at all.
	KeyDir string

	// DEKKeyDir is KeyDir's counterpart for the tenant DEK keyring's own
	// "software" provider instance (dek.WithKeyDir) -- a distinct
	// directory from KeyDir because the keyring's key references default
	// to tenant IDs, a different naming domain than backup's
	// operator-chosen names. KeyWrapRegistry/KeyWrapProvider, when set,
	// are shared with the DEK keyring too (one KEK seam, two consumers);
	// only the key directory is DEK-specific. Empty leaves the keyring's
	// default "software" provider unusable (dek.ErrKeyDirNotConfigured on
	// first Encrypt/Rotate) without disabling the keyring service itself.
	DEKKeyDir string

	// ClusterSealer makes node, tenant and database seals reach every node
	// once the cluster has activated cluster-seal, and lets an unseal clear
	// a cluster seal record. A composer passes a clusterseal.Deferred here
	// and sets its Monitor once the host, the services and the feature gate
	// exist. Nil, or an unset Deferred, seals this node only.
	ClusterSealer clusterseal.Sealer

	// ShardPauser pauses every Raft shard the node hosts before a node seal
	// wipes a key, and resumes them once the node barrier is unsealed
	// (server.NewShardPauser). A raft composer passes one built over its
	// host; nil -- a standalone node -- pauses nothing.
	ShardPauser service.ShardPauser

	// AuthMethodVocabulary, when non-nil, is the permitted-method vocabulary of
	// THIS node's auth chain (usually middleware.AuthMethodVocabulary's
	// IsAuthMethod, bound once the node's middleware is built). Every user
	// store BuildServices creates validates written policies against it
	// (user.WithAuthMethodVocabulary). Nil means the built-in tokens only.
	AuthMethodVocabulary func(method string) bool

	// ClusterCAKeyProvider, when non-nil, replaces the built-in cluster CA
	// (Key A) entirely: BuildServices never constructs a
	// cakey.SoftwareProvider and never reads or writes
	// clusterca.Prefix ("__sys:cluster_ca/"). This is the injection seam an
	// embedder (go-xkms) uses to back Key A with an HSM/TPM/KMS, via
	// cakey.Adapter. See docs/certificate-authority/architecture.md, "The
	// CA key provider seam".
	ClusterCAKeyProvider cakey.Provider

	// ClusterCACertPEM and ClusterCAKeyPEM carry an in-memory cluster CA
	// generated before any store existed (the bootstrap crash window; see
	// docs/certificate-authority/architecture.md, "Bootstrap crash window").
	// Both are empty on every boot after the key has been confirmed
	// persisted into the replicated store — the caller's local cache of the
	// key is deleted at that point, so there is nothing left to pass.
	// Ignored when ClusterCAKeyProvider is set, or when NoClusterCAKey is
	// true. See cakey.SoftwareProvider.AdoptIfAbsent for the adoption
	// semantics.
	ClusterCACertPEM []byte
	ClusterCAKeyPEM  []byte

	// NoClusterCAKey, when true and ClusterCAKeyProvider is nil, leaves
	// Node.ClusterCAProv nil instead of constructing the built-in
	// cakey.SoftwareProvider. This node has no cluster CA key of its own,
	// by the operator's choice -- a BYO-TLS deployment (explicit
	// --tls-ca-file/--tls-cert-file/--tls-key-file) whose operator-supplied
	// CA certificate is a trust anchor qrdb was never handed the private
	// key for, not bootstrap material for Key A. See
	// docs/certificate-authority/architecture.md, "server TLS identity and
	// the cluster/login CA are separate concerns".
	//
	// Every CA-dependent verb already refuses with its own typed error when
	// ClusterCAProv (or the CeremonyCA it backs) is nil -- ceremony-sign-csr
	// and officer-register return ErrCANotInitialized, and
	// AdminService.MintJoinToken returns ClusterJoinTokenNotConfiguredError
	// because cmd/qrdbd never calls SetClusterJoinTokenStore without a
	// provider. Setting NoClusterCAKey only stops the built-in provider from
	// silently generating -- and persisting -- a self-issued cluster CA key
	// nobody asked this node to hold. CertProv (Key B, the per-tenant login
	// CA) is unaffected: it is a different trust domain and is built
	// unconditionally in the same block.
	NoClusterCAKey bool

	// wrapVectorStorage, when non-nil, wraps the vector storage before the
	// vector service and the node's closers take it. It is unexported: the
	// package's own tests use it to hold a background vector job in flight
	// and observe the shutdown order around it (#1171).
	wrapVectorStorage func(vectorstorage.Storage) vectorstorage.Storage
}

// Node aggregates all services built by BuildServices and provides a
// coordinated Stop method that closes resources in reverse order. A Node
// must not be copied: it owns its closers and the client trust pool
// WireTrustAnchors wired, and a copy would share the first and detach the
// second.
type Node struct {
	// Core data-plane services.
	KV          service.KVServicer
	Admin       service.AdminServicer
	MVCC        service.MVCCKVServicer
	Batch       service.BatchServicer
	Transaction service.TransactionServicer
	Lease       service.LeaseServicer
	Index       service.IndexServicer
	TimeSeries  service.TimeSeriesServicer
	Watch       service.WatchServicer

	// Lock is the node's lock servicer over the node's RAW, pre-namespace
	// engines. It is the registry-LESS composition's servicer -- the embedded
	// transport, and single-database servers -- where the whole store and the
	// caller's own keyspace are the same set. A multi-tenant server must NOT
	// serve locks from it: a lock key carries no tenant segment, so every
	// tenant would take the same lock (go-qrdb#455). Those servers resolve
	// through service.Registry.LockService instead, which builds a lock
	// service over the (tenant, database)-namespaced engines, and the
	// transports prefer the registry whenever one is configured.
	Lock service.LockServicer

	Collection service.CollectionServicer
	Database   service.DatabaseServicer
	Vector     service.VectorServicer

	// Cluster operations services. Rebalance inspects/moves shards via the
	// host; Backup snapshots/restores shards via the host's snapshot/export
	// APIs (see NewHostShardSnapshotProvider).
	Rebalance service.RebalanceServicer
	Backup    service.BackupServicer

	// AI/ML services. Both are nil when no embedding providers are configured
	// (EmbeddingConfig.Providers is empty) or when the qrdb_vector build tag
	// is absent.
	Embedding service.EmbeddingServicer
	RAG       service.RAGServicer

	// Auth services backed by KV storage.
	User user.UserServicer
	Role role.RoleServicer

	// Security services. All are nil when no SealBarrier was provided.
	Barrier    service.BarrierServicer
	Ceremony   service.CeremonyServicer
	Credential service.CredentialServicer
	Tenant     tenant.TenantServicer
	CertProv   cert.CertProvider

	// ReplicatedAudit is the cluster-wide, per-tenant-opt-in replicated
	// audit sink (docs/audit/architecture.md#cluster-wide-replicated-trail):
	// Emit proposes protocol.OpAuditAppend, and Query/VerifyTenantChain read
	// back from whichever shard serves the tenant. It is always non-nil --
	// resolving a tenant's entitlement before every call, so toggling it
	// takes effect on the tenant's very next request -- but Emit and Query
	// are no-ops for a tenant whose entitlement is off, exactly as if no
	// replicated sink existed at all. It is additional to, never a
	// replacement for, the node-local trail every subsystem already writes
	// through: this is exposed separately so a caller can read the
	// cluster-wide chain specifically, which the node-local trail cannot
	// answer for (it is one node's disk, not Raft-replicated).
	ReplicatedAudit *service.OptInAuditLogger

	// ClusterCAProv is the cluster CA (Key A) provider: the built-in
	// cakey.SoftwareProvider over the replicated store, or
	// Infra.ClusterCAKeyProvider when one was injected. Nil when neither
	// exists -- a BYO-TLS node built with Infra.NoClusterCAKey and no
	// injected provider -- in which case CertProv (Key B) is still
	// non-nil: the two keys are separate trust domains, and only Key A is
	// ever withheld this way. Used for node-leaf issuance and member-side
	// cluster-join CSR signing (pkg/clusterenroll).
	ClusterCAProv cakey.Provider

	// TrustAnchors is every tenant's registered trust anchors. A composer
	// installs it on its client-auth trust pool
	// (clienttrust.Pool.SetTrustAnchors); WireTrustAnchors does both steps.
	TrustAnchors trustanchor.Registry

	// ClientTrustChanges announces every change to the set of CAs a client
	// certificate may chain to: a trust anchor added or removed, and a
	// tenant's login CA coming into use (cert.WithNewCAHook) -- the moment a
	// tenant onboarded a moment ago first has a CA to trust. A composer
	// subscribes its trust pool's Invalidate, so the next handshake sees the
	// change instead of waiting out the pool's cache.
	ClientTrustChanges *trustanchor.Changes

	// clientTrust is the trust pool WireTrustAnchors wired, which
	// TenantOnboarding rebuilds before answering a create so this node's
	// next handshake admits the admin certificate the answer carries. Nil
	// until wired; a node with no pool has no client trust to rebuild.
	clientTrust atomic.Pointer[clienttrust.Pool]

	// CertTargets is the certificate target guard every verb that binds a key
	// applies: certs:issue and certs:enroll through CertEnrollment (internally,
	// unconditionally), and users:add_cert_binding through the auth
	// middleware (AuthMiddleware.AuthorizeCertBindingTarget).
	CertTargets service.CertTargetGuard

	// PendingAdmins holds the first admins of tenants whose identity records
	// are behind their own not-yet-initialized barrier (pkg/tenantadmin).
	PendingAdmins tenantadmin.Store

	// PendingSOs holds the security officers of those same tenants, named at
	// tenant create, until their barrier is up (the __sys:pending-so record).
	PendingSOs tenantadmin.Store

	// BarrierInit is the per-node barrier init records and anchors
	// (pkg/barrierinit) every tenant barrier init claims its owner's
	// statement through. Nil exactly when Tenant is.
	BarrierInit *BarrierInit

	// CertEnrollment is the certificate issue/enrol and trust-anchor service
	// every transport delegates to; TenantOnboarding the tenant-create one.
	// Both are nil exactly when CertProv is.
	CertEnrollment   service.CertEnrollmentServicer
	TenantOnboarding service.TenantOnboardingServicer
	// onboarding is TenantOnboarding's concrete service, which AttachSOPINs
	// gives the SO PIN seam once the node's SO authenticator is built.
	onboarding *service.TenantOnboardingService

	// Invitations is the invitation self-enrolment service every transport's
	// users:invite and (unauthenticated) redemption verbs delegate to. Nil
	// exactly when CertEnrollment is: both require CertProv, and redemption
	// issues through the same Key B CA SignCertificate does.
	Invitations service.InvitationServicer

	// StoreResolver maps a tenant ID to the KVStore backing its IAM data.
	// It is non-nil only when a BarrierRegistry was supplied: barrier-mode
	// tenants route their user/role records through their own TenantBarrier
	// while prefix/system tenants use the shared system store. It is a
	// forward seam for the SDK to expose; there is no SDK accessor yet.
	StoreResolver tenant.StoreResolver

	// UserAuthFactor is the M14 user-side auth-factor service, TENANT-ROUTED
	// through StoreResolver. It is non-nil exactly when StoreResolver is: the
	// resolver is the tenant boundary of factor resolution (go-qrdb#393), so a
	// composition that cannot resolve a tenant gets no factor service rather
	// than one serving every tenant out of one keyspace.
	UserAuthFactor service.UserAuthFactorServicer

	// DEK is the per-tenant data encryption keyring service. It is non-nil
	// exactly when StoreResolver is: the keyring is a record in each
	// tenant's resolved store, so it inherits that store's replication,
	// barrier encryption, sealed-tenant refusal and teardown.
	DEK service.DEKServicer

	// Log is the verifiable-log service. It rides the audit resolver, so
	// it is non-nil whenever the build carries the qrdb_vlog tag, and nil
	// when it does not. Its head-signing keys come from
	// Infra.HeadSignerProvider, or the software provider when that is nil.
	Log service.LogService

	// Policy is the tenant-scoped ABAC policy store. It is non-nil exactly
	// when StoreResolver is: each policy is a record in its tenant's
	// resolved store, so it inherits that store's replication, barrier
	// encryption and sealed-tenant refusal.
	Policy service.PolicyServicer

	// KeyspaceReclaim reclaims a dropped database's data from the store it
	// shares with other databases. It is always non-nil and is already wired
	// into the database manager; it is exposed so a composer that serves
	// volume-isolated tenants can attach the StoreResolver that routes their
	// keys to their dedicated per-tenant Host (SetStoreResolver), which does
	// not exist yet at the point BuildServices runs.
	KeyspaceReclaim *service.KeyspaceReclaimService

	// ReclaimSweeper drains pending reclaim jobs (go-qrdb#547) -- durable
	// records of keyspace an operation orphaned instead of deleting inline:
	// TRUNCATE TABLE's superseded generation, RENAME INDEX's old name (see
	// service.ReclaimJob). It is always non-nil, built here -- the one
	// composition point every composer shares -- so that starting its Run
	// loop is never a construction a composer can forget (go-qrdb#174). Every
	// composer that serves relational DDL (the daemon, the SDK node) starts
	// Run on every node; the server installs its resolver.
	ReclaimSweeper *service.ReclaimSweeper

	// PlacementStore is the replicated placement table for dedicated tenant
	// Raft groups (Phase 4c). It is always non-nil: the DatabaseService writes
	// a database's computed placement here on create, and the daemon threads it
	// into the RaftBackendFactory so the table is the source of truth for
	// resolving a dedicated-raft database's shard.
	PlacementStore *placement.Store

	// PlacementChanges is the placement table's change feed: every placement
	// written or deleted, as this node applies it. Replica forwarding drops a
	// cached leader from it. It is a feed only, over the same replicated
	// keys; every write goes through PlacementStore. PlacementStore itself
	// keeps no feed, so the tenant shard reconciler converges on its poll.
	PlacementChanges PlacementChanges

	// ClusterMigStore is the replicated coordination plane for cluster-wide
	// enable-encryption migrations (Phase D). It lives in the same system KV as
	// the placement table; the daemon threads it into the migration coordinator +
	// per-node reconciler. Non-nil whenever the system KV is available.
	ClusterMigStore *clustermig.Store

	// closers are called in reverse order on Stop.
	closers []func() error

	// adminCompletionBounds bounds each pending-admin completion
	// (completePendingTenantAdmin); zero takes
	// bootstrap.AdminCompletionBounds' defaults.
	adminCompletionBounds bootstrap.AdminCompletionBounds

	// homeLeader answers whether this node leads a tenant's home group; set
	// by AttachTenantHomes, unset on a node that is every tenant's only
	// writer.
	homeLeader atomic.Pointer[databaseLeadership]

	// work is what the node runs on its own behalf; Stop cancels it and
	// waits for it before any closer runs.
	work nodeWork
}

// addCloser registers a closer to be called during Stop.
func (n *Node) addCloser(fn func() error) {
	n.closers = append(n.closers, fn)
}

// Stop cancels the work the node runs on its own behalf and waits for it,
// then calls all registered closers in LIFO order. All errors are collected;
// the first non-nil error is returned.
func (n *Node) Stop(_ context.Context) error {
	n.work.stop()
	var firstErr error
	for i := len(n.closers) - 1; i >= 0; i-- {
		if err := n.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BuildServices constructs all service-layer objects from the provided
// already-initialised infrastructure. It does not open storage, create
// listeners, or initialize the seal barrier — those responsibilities remain
// with the caller.
//
// Services that depend on a seal barrier (Barrier, Ceremony, Credential,
// Tenant, CertProv) are constructed only when infra.SealBarrier is non-nil.
// The returned Node always has KV, Admin, MVCC, Batch, Transaction, Lease,
// Index, TimeSeries, Watch, Lock, Collection, Database, and Vector populated
// (or non-nil errors returned for construction failures).
func BuildServices(ctx context.Context, infra Infra) (*Node, error) {
	if infra.Host == nil {
		return nil, &NilHostError{}
	}
	if infra.WatchHub == nil {
		return nil, &NilWatchHubError{}
	}
	if infra.CertProvider != nil && infra.CertKeyProviderFactory != nil {
		return nil, &CertProviderConflictError{}
	}

	log := infra.Logger
	if log == nil {
		log = slog.Default()
	}

	n := &Node{}

	// Wire the cluster-wide replicated audit sink (phase 2,
	// docs/audit/architecture.md#cluster-wide-replicated-trail). The
	// isolation and entitlement lookups are bound to n itself rather than a
	// snapshot: n.Tenant is populated later in this function (several
	// branches below, depending on infra.SealBarrier / infra.BarrierRegistry),
	// but every Emit/Query/VerifyTenantChain call happens long after
	// BuildServices has returned, by which point n.Tenant -- if this
	// composition has one at all -- is already set, so the lookups pick it
	// up automatically with no restart.
	auditResolver := service.NewSystemShardAuditResolver(infra.Host, shard.ShardKV, &nodeAuditIsolationLookup{node: n})
	n.ReplicatedAudit = service.NewOptInAuditLogger(
		service.NewReplicatedLogger(auditResolver),
		&nodeAuditEntitlementLookup{node: n},
		infra.ReplicatedAuditPlatformEnabled,
	)
	// Every ordinary Emit call already made through infra.AuditLogger below
	// (the node-local trail) now ALSO reaches the replicated chain when the
	// event's tenant is entitled -- the fix for "nothing ever proposes
	// OpAuditAppend" (architecture.md's Status section). A composition that
	// passed no node-local logger at all (infra.AuditLogger nil) gets none
	// here either: introducing a sink where the caller asked for none would
	// change its behavior, not merely extend it.
	if infra.AuditLogger != nil {
		infra.AuditLogger = audit.NewAggregateLogger(infra.AuditLogger, n.ReplicatedAudit)
	}

	// Build core data-plane services.
	n.KV = service.NewKVService(infra.Host, shard.ShardKV, infra.KVOptions...)
	var mvccOpts []service.MVCCKVServiceOption
	if infra.NoRevisionHistory {
		mvccOpts = append(mvccOpts, service.WithoutRevisionHistory())
	}
	n.MVCC = service.NewMVCCKVService(infra.Host, shard.ShardKV, mvccOpts...)
	var adminOpts []service.AdminServiceOption
	if infra.Profile != nil {
		adminOpts = append(adminOpts, service.WithCapabilityProfile(*infra.Profile))
	}
	// The platform's node-keyspace compaction policy is a record in the
	// shared store's compaction table; a shared store with no revision
	// history has none to hold.
	if table, ok := n.MVCC.(service.CompactionStateStore); ok && !infra.NoRevisionHistory {
		adminOpts = append(adminOpts, service.WithNodeCompaction(table))
	}
	n.Admin = service.NewAdminService(infra.Host, adminOpts...)
	n.Batch = service.NewBatchService(infra.Host, shard.ShardKV, service.WithImportSessionLimits(
		infra.ImportSession.ResolvedMaxSessionBytes(),
		infra.ImportSession.ResolvedMaxChunksPerSession(),
		infra.ImportSession.ResolvedMaxSessionsPerTenant(),
		infra.ImportSession.ResolvedSessionTTLSeconds(),
	))
	n.Transaction = service.NewTransactionService(infra.Host, shard.ShardKV)
	n.Lease = service.NewLeaseService(infra.Host, shard.ShardKV)
	n.Index = service.NewIndexService(infra.Host, shard.ShardKV)
	n.TimeSeries = service.NewTimeSeriesService(infra.Host, shard.ShardKV)
	n.Watch = service.NewWatchService(infra.WatchHub)

	lockSvc, err := service.NewLockService(n.Transaction, n.Lease, n.Watch, n.MVCC, log)
	if err != nil {
		return nil, &LockServiceError{Err: err}
	}
	n.Lock = lockSvc

	// SequenceServicer backs Collection's IDStrategyIncrement document IDs
	// with a Raft-replicated compare-and-swap counter (see
	// service.CollectionService.WithSequenceService) instead of the
	// process-local counter that would otherwise restart at 0 on every
	// process restart and diverge across replicas.
	n.Collection = service.NewCollectionServiceWithIndex(n.KV, n.Index).
		WithSequenceService(service.NewSequenceService(n.MVCC, n.Transaction))

	// Cluster operations. Rebalance inspects shard stats and moves shards via
	// the host. Backup snapshots/restores shards via the host's snapshot/export
	// APIs through a host-backed ShardSnapshotProvider. Both are functional only
	// on a clustered Raft host; on the single-node LocalBackend, Rebalance has
	// no peers to balance and Backup reports BackupUnsupportedError per shard.
	n.Rebalance = service.NewRebalanceService(infra.Host)
	var backupOpts []service.BackupServiceOption
	if infra.BackupSink != nil {
		backupOpts = append(backupOpts, service.WithBackupSink(infra.BackupSink))
	}
	if infra.KeyWrapRegistry != nil {
		backupOpts = append(backupOpts, service.WithKeyWrapRegistry(infra.KeyWrapRegistry))
	}
	if infra.KeyWrapProvider != nil {
		backupOpts = append(backupOpts, service.WithKeyWrapProvider(infra.KeyWrapProvider))
	}
	backupOpts = append(backupOpts,
		service.WithKeyDir(infra.KeyDir),
		service.WithAuditLogger(infra.AuditLogger),
		service.WithAuditFailurePolicy(infra.AuditFailurePolicy),
	)
	backupSvc, err := service.NewBackupService(service.NewHostShardSnapshotProvider(infra.Host), backupOpts...)
	if err != nil {
		return nil, &BackupServiceError{Err: err}
	}
	n.Backup = backupSvc
	// Releases the cached "software" key-wrap provider's *os.Root, if the
	// default registry ever resolved one (service.BackupService.Close).
	// NewBackupService always returns *service.BackupService; the
	// assertion only guards a future BackupServicer implementation this
	// constructor might be changed to return.
	if closer, ok := backupSvc.(*service.BackupService); ok {
		n.addCloser(closer.Close)
	}

	// Auth services (user, role) backed by KV storage.
	//
	// GUARDED: n.Transaction is already built above, unconditionally, so
	// authStore gets kvstore.MultiGuardedBatchKVStore capability at no extra
	// cost — the same shared implementation (ExecuteGuardedBatch) cmd/qrdbd's
	// role/user stores use (go-qrdb#555 owner ruling: every composer that
	// builds these stores gets the guarded adapter). The plain n.User/n.Role
	// built directly over it just below stay sequential; where a
	// StoreResolver is wired further down, the routed services that replace
	// them (role/user.NewRoutingService over authStore) are what use it,
	// committing a
	// system-tenant create atomically once kvstore.ProbeGuardedBatch confirms
	// the state machine can evaluate the batch.
	authStore, err := NewGuardedKVStoreAdapter(n.KV, n.Index, n.Transaction)
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	userIDGen, err := dao.NewSnowflakeGenerator(resolveReplicaID(infra.ReplicaID))
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	userOpts, err := UserStoreOptions(infra.MTLSBindingPolicy, log)
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	// Every user store below validates written permitted-method policies
	// against THIS node's auth-chain vocabulary.
	userOpts = append(userOpts, user.WithAuthMethodVocabulary(infra.AuthMethodVocabulary))
	userSvc, err := user.NewStore(authStore, userIDGen, userOpts...)
	if err != nil {
		return nil, &UserServiceError{Err: err}
	}
	n.User = userSvc

	holders, err := user.NewRoleRevoker(userSvc)
	if err != nil {
		return nil, &RoleServiceError{Err: err}
	}
	roleSvc, err := role.NewStore(authStore, role.WithHolderRevoker(holders))
	if err != nil {
		return nil, &RoleServiceError{Err: err}
	}
	n.Role = roleSvc

	// Database service. Use the caller-supplied manager when available
	// (callers may have run migrations or created databases before calling
	// BuildServices). Otherwise create a fresh manager.
	dbManager := infra.DatabaseManager
	if dbManager == nil {
		dbManager = database.NewManager(log, infra.DataDir)
		n.addCloser(func() error { return dbManager.Close() })
	}

	// Wire the per-database encryption barrier manager now that the seal
	// subsystem's BarrierRegistry is available (BuildServices runs after the
	// barrier is unsealed), before the catalog is attached and its databases
	// opened below. SetBarrierManager also re-registers the barrier for every
	// already-open database that has one enabled on a dedicated store, for a
	// caller that opened databases before handing the manager over. When
	// infra.BarrierRegistry is nil (seal disabled) no manager is set, so
	// per-database barrier operations correctly fail closed with
	// BarrierManagerNotConfiguredError. Guarded on BarrierManager() == nil so
	// a caller that already wired one explicitly is not clobbered.
	//
	// The manager keeps its barriers in a registry of its own over the system
	// barrier, never in infra.BarrierRegistry's tenant namespace: a database
	// named like a tenant would otherwise resolve to that tenant's barrier.
	if infra.BarrierRegistry != nil && dbManager.BarrierManager() == nil {
		bm, err := database.NewDatabaseBarrierManager(log, infra.BarrierRegistry.SystemBarrier(),
			database.WithBarrierAuditTrail(infra.AuditLogger),
			database.WithBarrierStrategyRegistry(infra.StrategyRegistry))
		if err != nil {
			return nil, &DatabaseBarrierManagerError{Err: err}
		}
		dbManager.SetBarrierManager(bm)
	}
	// The node unseal brings back only the system-managed databases the node
	// seal sealed, and never one whose own cluster seal record refuses it.
	if bm := dbManager.BarrierManager(); bm != nil && infra.ClusterSealer != nil {
		bm.SetUnsealGate(databaseUnsealGate(infra.ClusterSealer))
	}

	// The verifiable-log service routes as the audit chain does, but also
	// refuses a barrier tenant and a barrier database: its log registry and
	// sealed signing keys sit under the system barrier, not the tenant's or
	// the database's. A build without the qrdb_vlog tag has no log service,
	// which is not a boot failure.
	logResolver := service.NewSystemShardLogResolver(infra.Host, shard.ShardKV, &nodeAuditIsolationLookup{node: n})
	logSvc, err := buildLogService(logResolver, dbManager, infra.HeadSignerProvider)
	if err != nil {
		return nil, err
	}
	n.Log = logSvc

	// Replicated placement table (Phase 4c) over the shared system KV, plus the
	// load-aware scheduler over the live cluster topology (host roster +
	// RebalanceService load scoring). These are wired into the DatabaseService
	// so a dedicated-raft database's replica placement is computed and persisted
	// at create time; the store is also exposed on the Node so the daemon can
	// make it the RaftBackendFactory's source of truth.
	// Cluster-migration coordination plane over the same replicated system KV.
	migStore, err := newClusterMigStore(authStore)
	if err != nil {
		return nil, err
	}
	n.ClusterMigStore = migStore

	var placementOpts []service.DatabaseServiceOption
	if feed, ferr := placement.NewStore(newPlacementKV(n.KV, n.Index, n.Watch)); ferr == nil {
		n.PlacementChanges = feed
	}
	if store, perr := placement.NewStore(authStore); perr == nil {
		n.PlacementStore = store
		var schedOpts []placement.SchedulerOption
		// A host whose groups gate features never has a group placed on a
		// node that cannot apply what the group uses.
		if admitter, ok := infra.Host.(service.ReplicaAdmitter); ok {
			schedOpts = append(schedOpts, placement.WithReplicaFilter(
				func(_ string, shardID uint64, raftAddr string) error {
					return admitter.AdmitReplica(shardID, raftAddr)
				}))
		}
		// Security review item 5: a raft tenant's replica is never placed
		// on a node whose latest health report does not show the
		// tenant's volume pool ready there. ANDed with the feature-gate
		// filter above via WithReplicaFilter, not replacing it. Requires
		// both the replicated catalog (attached to BarrierRegistry
		// before BuildServices runs, same ordering the TenantService's
		// own volume-pool reservation already depends on) and a raft-
		// address-to-node-id snapshot from the composer; either missing
		// leaves this filter un-added, matching every other optional
		// filter here.
		if infra.BarrierRegistry != nil && len(infra.RaftAddrToNodeID) > 0 {
			if volumeSvc, ok := infra.BarrierRegistry.VolumeService(); ok {
				if allocator := infra.BarrierRegistry.VolumeAllocator(); allocator != nil {
					schedOpts = append(schedOpts, placement.WithReplicaFilter(
						newVolumePoolHealthFilter(allocator, volumeSvc, infra.RaftAddrToNodeID,
							func(ctx context.Context) (*quicraft.Membership, error) {
								return infra.Host.SyncGetShardMembership(ctx, shard.ShardKV)
							})))
				}
			}
		}
		if scheduler, serr := placement.NewScheduler(service.NewHostTopologyProvider(infra.Host, n.Rebalance), schedOpts...); serr == nil {
			placementOpts = []service.DatabaseServiceOption{
				service.WithPlacementStore(store),
				service.WithPlacementScheduler(scheduler),
				service.WithDatabaseLogger(log),
			}
		}
	}
	// Cluster-wide shard IDs: every raft database create claims its ID from
	// one replicated high-water mark in the system store, once the cluster
	// has activated cluster-shard-ids (see newShardClaimer).
	claimer, err := newShardClaimer(authStore, infra.Host)
	if err != nil {
		return nil, err
	}
	dbManager.SetShardIDClaimer(claimer)

	// Thread the same DatabaseBarrierManager instance dbManager was just
	// wired with into DatabaseService, so SealDatabase/UnsealDatabase/
	// DatabaseBarrierStatus (which read DatabaseService.barrierMgr directly)
	// and Create/Open (which read Manager.barrierMgr) operate against the
	// same seal.BarrierRegistry. Appended to placementOpts so it survives the
	// tenant-quota-opts rebuild below.
	if bm := dbManager.BarrierManager(); bm != nil {
		placementOpts = append(placementOpts, service.WithBarrierManager(bm))
	}
	if infra.ClusterSealer != nil {
		placementOpts = append(placementOpts, service.WithDatabaseClusterSealer(infra.ClusterSealer))
	}
	// Routes an IsolationVolume/IsolationRaft database's DataDir under its
	// tenant's catalog-resolved pool directory; TenantInfo itself carries
	// only the pool NAME (VolumePool), never a resolved path.
	if infra.BarrierRegistry != nil {
		placementOpts = append(placementOpts, service.WithTenantDataRootResolver(infra.BarrierRegistry))
	}

	n.Database = service.NewDatabaseService(dbManager, placementOpts...)

	// Vector storage uses the directory when one is available, exactly as
	// before go-qrdb#578; what #578 adds is infra.Backend deciding what
	// happens when NO directory is available, instead of DataDir's
	// emptiness alone deciding everything. A directory, when the caller
	// supplies one, is used for Pebble-backed vector storage regardless of
	// the primary engine's own backend type -- sdk/go/driver/node
	// deliberately roots a memory-backend node's vector store and database
	// catalog in a real directory (its own resolveDataDir doc comment:
	// "the vector store ... [is] file-backed regardless of the KV
	// backend"), and that choice is the caller's to make, not something
	// this function overrides. An EMPTY directory is where the decision
	// actually lives: Backend == database.BackendMemory is the caller's
	// explicit choice to run ephemeral, honoured by MemoryVectorStorage;
	// any other backend with no directory is a configuration mistake, not
	// a request for an ephemeral store, and is refused
	// (EmptyVectorDataDirError) instead of silently handed one anyway.
	//
	// Encryption-at-rest applies only to the Pebble-backed path: there is no
	// file for atrest to wrap when nothing is written to disk.
	var vectorStorage vectorstorage.Storage
	if infra.DataDir == "" && infra.Backend != database.BackendMemory {
		return nil, &EmptyVectorDataDirError{Backend: infra.Backend}
	}
	if infra.DataDir == "" {
		vectorStorage = vectorstorage.NewMemoryVectorStorage()
	} else {
		// Route encryption through the shared Encryptor so vector storage and
		// the MVCC state machine use one code path and one instance. When
		// Infra.Encryptor is nil, derive a no-op Encryptor from SealBarrier
		// for callers that only set that field.
		enc := infra.Encryptor
		if enc == nil {
			enc = atrest.New(infra.SealBarrier)
		}

		vectorDataDir := filepath.Join(infra.DataDir, "vector")
		vectorCfg := vectorstorage.DefaultConfig(vectorDataDir)

		vectorStorageOpts := []vectorstorage.StorageOption{
			vectorstorage.WithEncryptor(enc),
		}
		pebbleVectorStorage, err := vectorstorage.NewPebbleVectorStorage(vectorCfg, logger.NewAdapter(newMinimalLogger(log)), vectorStorageOpts...)
		if err != nil {
			return nil, &VectorStorageError{Err: err}
		}
		vectorStorage = pebbleVectorStorage
	}
	if infra.wrapVectorStorage != nil {
		vectorStorage = infra.wrapVectorStorage(vectorStorage)
	}
	vectorLog := logger.NewAdapter(newMinimalLogger(log))
	n.addCloser(func() error { return vectorStorage.Close() })

	vectorSvc, err := service.NewVectorService(
		vectorStorage,
		vectorLog,
	)
	if err != nil {
		return nil, &VectorServiceError{Err: err}
	}
	// Closers run last-added first, so this runs before the vectorStorage
	// closer above: the service cancels and waits for its async compactions
	// and index rebuilds before the Pebble DB beneath them closes (#1171).
	if closer, ok := vectorSvc.(io.Closer); ok {
		n.addCloser(closer.Close)
	}
	n.Vector = vectorSvc

	// DROP DATABASE reclamation (go-qrdb#412). The manager's own teardown
	// reclaims what a database holds on this node's filesystem; for a
	// shared-store database -- the default shape -- none of its data is
	// there, and before this seam existed the drop orphaned every key it
	// owned. This is the one place the database manager and the data-plane
	// engines meet in EVERY composition that can drop a database (the
	// daemon, the SDK's in-process node, an embedding), which is why it is
	// wired here rather than at a server.
	//
	// It is wired after n.Vector because a vector namespace is part of what a
	// database owns and is not reached by deleting keys.
	systemCompaction, _ := n.MVCC.(service.CompactionStateStore)
	n.KeyspaceReclaim = service.NewKeyspaceReclaimService(service.ReclaimTarget{
		KV:         n.KV,
		Index:      n.Index,
		Vector:     n.Vector,
		Compaction: systemCompaction,
	})
	dbManager.SetKeyspaceReclaimer(n.KeyspaceReclaim)

	// go-qrdb#547's reclaim-job sweeper, over the un-namespaced system store
	// where every pending job's index entry lives. Each job's own store is
	// resolved per entry once the server installs its resolver
	// (server.WithReclaimSweeper). Every composer starts this ONE instance's
	// Run loop (see the ReclaimSweeper field doc comment); none constructs
	// its own.
	n.ReclaimSweeper = service.NewReclaimSweeper(n.MVCC)

	// Embedding and RAG services: constructed only when providers are
	// configured. buildEmbeddingAndRAG is a no-op stub in non-vector builds.
	embSvc, ragSvc, err := buildEmbeddingAndRAG(
		infra.EmbeddingConfig,
		infra.RAGConfig,
		n.Vector,
		n.KV,
		log,
	)
	if err != nil {
		return nil, err
	}
	n.Embedding = embSvc
	n.RAG = ragSvc

	// Seal-gated services: only constructed when a barrier is provided.
	if infra.SealBarrier != nil {
		if err := n.buildSealServices(ctx, infra, log, authStore); err != nil {
			return nil, err
		}
		// A Shamir database barrier's holders are enrolled and verified by
		// the same CA and factors as the system and tenant
		// barriers' holders.
		if bm := dbManager.BarrierManager(); bm != nil && n.Ceremony != nil {
			if coreSvc, ok := service.CeremonyServiceCore(n.Ceremony); ok {
				bm.SetHolderDeps(coreSvc.HolderDeps())
			}
		}
		// Tenant storage isolation: when a barrier registry and a barrier-mode
		// tenant service are present, route IAM (user/role) reads and writes
		// through a StoreResolver. Barrier-isolated tenants persist their IAM
		// data via their own TenantBarrier (own DEK + "{tenantID}/" namespace);
		// prefix/system tenants continue to use the shared system store.
		if infra.BarrierRegistry != nil && n.Tenant != nil {
			resolver, err := tenant.NewStoreResolver(authStore, infra.BarrierRegistry, n.Tenant)
			if err != nil {
				return nil, &UserServiceError{Err: err}
			}
			n.StoreResolver = resolver
			// An isolated tenant's CA and certificate records follow its
			// identity into its home store.
			if router, ok := n.CertProv.(cert.TenantStoreRouter); ok {
				router.SetStoreResolver(resolver)
			}

			routedUsers, err := user.NewRoutingService(resolver, userIDGen, userOpts...)
			if err != nil {
				return nil, &UserServiceError{Err: err}
			}
			n.User = routedUsers

			routedHolders, err := user.NewRoleRevoker(routedUsers)
			if err != nil {
				return nil, &RoleServiceError{Err: err}
			}
			routedRoles, err := role.NewRoutingService(resolver, role.WithHolderRevoker(routedHolders))
			if err != nil {
				return nil, &RoleServiceError{Err: err}
			}
			n.Role = routedRoles

			if err := n.buildUserAuthFactorService(ctx, resolver, authStore, infra.AuditLogger, log); err != nil {
				return nil, err
			}
		}
		// Over the FINAL (routed) user and role services; see
		// buildEnrollmentServices.
		n.buildEnrollmentServices(infra, log)
	} else {
		// When the seal subsystem is disabled, wire a KV-backed tenant service
		// so tenant CRUD operates through Raft consensus. Barrier operations
		// on this implementation return ErrBarrierNotEnabled.
		// The fenced adapter carries the transaction plane so CreateTenant can
		// commit a tenant only while its tombstone is absent, atomically.
		kvTenantSvc, err := tenant.NewKVTenantService(&fencedKVStoreAdapter{kvStoreAdapter: &kvStoreAdapter{svc: n.KV, idx: n.Index}, tx: n.Transaction}, log)
		if err != nil {
			return nil, &TenantServiceError{Err: err}
		}
		if w, ok := kvTenantSvc.(tenant.UndoWiring); ok {
			w.SetTenantUndoGate(tenantUndoRequired(infra.Host))
			w.SetUndoCleanupRoster(nodeIdentity(infra.Host), undoCleanupRoster(infra.Host))
		}
		n.Tenant = kvTenantSvc

		// Barrier protection none is a valid barrier, not an unconfigured
		// one: a node whose composer chose seal.enabled: false serves
		// BarrierStatus (strategy none, unsealed) on every transport and
		// refuses the verbs that would change it. A barrier that was
		// requested but failed to construct also lands here, and is no
		// protection none: it gets no barrier service.
		if !infra.SealEnabled {
			barrierSvc, berr := service.NewSealDisabledBarrierService(ctx)
			if berr != nil {
				return nil, &BarrierServiceError{Err: berr}
			}
			n.Barrier = barrierSvc
		}

		// Route IAM here TOO. Keyspace isolation is not a property of
		// encryption: a node with the seal subsystem off still has tenants,
		// and they are still entitled to separate keyspaces. Leaving n.User
		// and n.Role on the raw authStore in this branch meant every prefix
		// tenant on a seal-disabled node shared one IAM keyspace — the exact
		// exposure #41 closes everywhere else.
		//
		// The resolver takes no barrier registry here. Prefix and system
		// tenants get their confined "{tenant}/" view of the shared store as
		// usual; a dedicated-isolation tenant fails closed with
		// ErrTenantSealed, because without a registry there is no barrier to
		// route it through and serving it from the shared store would hand a
		// tenant that asked for cryptographic isolation an unencrypted
		// keyspace.
		resolver, rerr := tenant.NewStoreResolver(authStore, nil, kvTenantSvc)
		if rerr != nil {
			return nil, &UserServiceError{Err: rerr}
		}
		n.StoreResolver = resolver

		routedUsers, uerr := user.NewRoutingService(resolver, userIDGen, userOpts...)
		if uerr != nil {
			return nil, &UserServiceError{Err: uerr}
		}
		n.User = routedUsers

		routedHolders, rherr := user.NewRoleRevoker(routedUsers)
		if rherr != nil {
			return nil, &RoleServiceError{Err: rherr}
		}
		routedRoles, roerr := role.NewRoutingService(resolver, role.WithHolderRevoker(routedHolders))
		if roerr != nil {
			return nil, &RoleServiceError{Err: roerr}
		}
		n.Role = routedRoles

		if uafErr := n.buildUserAuthFactorService(ctx, resolver, authStore, infra.AuditLogger, log); uafErr != nil {
			return nil, uafErr
		}
	}

	// DELETE TENANT teardown (go-qrdb#413). The tenant service is built above
	// in both branches -- barrier-backed when the seal subsystem is on,
	// KV-backed when it is off -- and both need the same three seams, because
	// keyspace isolation is not a property of encryption and a prefix tenant
	// on a seal-disabled node is exactly the case whose data stayed READABLE
	// after a delete.
	//
	// The reclaimer is the SAME service DROP DATABASE uses. A tenant's
	// keyspace and a database's are the same shapes of key over different
	// scopes, so there is one walk, not two.
	if w, ok := n.Tenant.(tenant.TeardownWiring); ok {
		w.SetDatabaseLister(dbManager)
		w.SetKeyspaceReclaimer(n.KeyspaceReclaim)
	}

	// The tenant DEK keyring rides the StoreResolver both branches above
	// build, so it is served whether or not the seal subsystem is enabled.
	// An injected DEKProvider does not need the resolver, so it is served too.
	if n.StoreResolver != nil || infra.DEKProvider != nil {
		dekSvc, err := buildDEKService(infra.DEKProvider, n.StoreResolver, infra.KeyWrapRegistry, infra.KeyWrapProvider, infra.DEKKeyDir)
		if err != nil {
			return nil, err
		}
		n.DEK = dekSvc
		// Releases the cached "software" key-wrap provider's *os.Root, if
		// the default registry ever resolved one (service.DEKService.Close),
		// mirroring the identical cleanup for n.Backup just above.
		if closer, ok := dekSvc.(*service.DEKService); ok {
			n.addCloser(closer.Close)
		}
	}

	// The database catalog: every database's record, replicated in the
	// system store and, for a tenant isolated in a home, in that home --
	// routed by the same resolver as the tenant's IAM records. Every
	// database it records is opened here, on every node, so a database
	// created through another node, or before a restart, is served.
	// A database whose backend is not raft is held by the node that creates
	// it, and only that node opens it (OD-2): the catalog records this
	// node's identity on what it creates, and OpenAll below skips what other
	// nodes hold. A standalone node is the only node, and records none.
	if id := nodeIdentity(infra.Host); id != LocalNodeID {
		dbManager.SetLocalNode(id)
	}
	homes, _ := n.StoreResolver.(database.CatalogHomes)
	catalog, err := database.NewKVCatalog(newKVStoreAdapter(n.KV, n.Index), homes)
	if err != nil {
		return nil, &DatabaseCatalogError{Err: err}
	}
	if err := openDatabaseCatalog(ctx, infra.Host, dbManager, catalog); err != nil {
		return nil, err
	}

	// ABAC policy documents ride the same StoreResolver.
	if n.StoreResolver != nil {
		policySvc, err := buildPolicyService(n.StoreResolver)
		if err != nil {
			return nil, err
		}
		n.Policy = policySvc
	}

	// Wire tenant-driven quota and entitlement enforcement into the database
	// service now that the tenant service exists. The tenant service is both
	// the quota.QuotaProvider and the quota.EntitlementChecker; CreateForTenant
	// consults them (the entitlement plane first, then the numeric quota plane)
	// before provisioning a tenant-scoped database. System-tenant creates
	// short-circuit ahead of both checks.
	if n.Tenant != nil {
		dbOpts := buildDatabaseQuotaOpts(n.Tenant, log)
		if len(dbOpts) > 0 {
			// Rebuild with both the quota/entitlement options and the placement
			// options so neither set is dropped when the tenant service exists.
			dbOpts = append(dbOpts, placementOpts...)
			n.Database = service.NewDatabaseService(dbManager, dbOpts...)
		}
		// dbManager.Open warned for the barrier records it can classify
		// alone; a volume- or raft-isolated tenant's needs the tenant
		// service, which exists only now.
		service.WarnBarriersOffDataPath(ctx, dbManager.List(), n.Tenant, log)
	}

	// An isolated tenant's home group is a placement no DROP DATABASE removes,
	// so DELETE TENANT retires it through the database service -- the one
	// built last above, whichever branch built it.
	if w, ok := n.Tenant.(tenant.TeardownWiring); ok {
		if r, ok := n.Database.(tenant.HomeRetirer); ok {
			w.SetHomeRetirer(r)
		}
	}

	// The vector namespace create guard (validateTenantCreate) reads the
	// cluster-wide isolation floor; n.Tenant is the reader, and it exists
	// only now, after n.Vector was already built above. This replaces the
	// old boot-time tenant.strict_isolation config bool: the guard now
	// tracks the real, live setting rather than a snapshot taken before the
	// tenant service existed.
	if reader, ok := n.Tenant.(tenant.ClusterIsolationPolicyReader); ok {
		if v, ok := n.Vector.(interface {
			SetClusterIsolationPolicyReader(tenant.ClusterIsolationPolicyReader)
		}); ok {
			v.SetClusterIsolationPolicyReader(reader)
		}
	}

	// infra.SealEnabled is the composer's own seal.enabled config decision,
	// read BEFORE it attempted to build a barrier -- NOT an inference from
	// SealBarrier's nilness, which a failed passphrase/Shamir/strategy
	// construction also produces and which is a distinct failure, not an
	// operator's choice to run without encryption (that failure is the
	// composer's own responsibility to report). Requiring SealBarrier to
	// ALSO be nil is a deliberate second belt: a composer that supplies a
	// real barrier without yet knowing about SealEnabled (an older call
	// site, a test fixture) must not be accused of running one it doesn't
	// have -- a false positive here wrongly tells an operator their data is
	// unencrypted, which is worse than the false negative of an omitted
	// warning from a call site whose barrier construction failed AND whose
	// composer forgot to report it. This is the single place both the
	// daemon (cmd/qrdbd) and the embedded SDK node (sdk/go/node) funnel
	// through on every successful boot path, sealed or not, so logging here
	// -- rather than in each caller, and only once THIS function's own
	// construction has fully succeeded -- guarantees the warning fires
	// exactly once per node start regardless of composer, and never for a
	// node whose service graph failed to build here. It does not cover a
	// later failure elsewhere in the boot sequence (server.New, transport
	// listen, etc.), which BuildServices cannot see. Wording mirrors
	// go-quicraft's own "strategy none" WARN so an operator sees the same
	// fact whether the barrier exists-but-does-nothing or does not exist at
	// all.
	//
	// There is no live command, and no safe in-place flag flip, that adds
	// encryption to a deployment that ran this way: enable-encryption
	// re-encodes files that are already in qrdb's encrypted-file format, and
	// this deployment's files were never written in that format at all.
	// Simply setting seal.enabled: true and restarting does NOT encrypt
	// existing data -- the encrypting filesystem passes pre-existing
	// plaintext files through unmodified on read and can fail outright
	// (encfs: invalid header) the first time a file is reopened for
	// writing, which can wedge the boot. The hint therefore does not name a
	// procedure to run in place; the supported path is a new node/tenant
	// provisioned with seal.enabled: true, with data moved into it through
	// backup/restore or an application-level re-seed (see
	// docs/barrier-seal/user.md).
	if !infra.SealEnabled && infra.SealBarrier == nil {
		log.Warn("qrdb/seal: seal disabled; at-rest data is stored in plaintext",
			"hint", "encryption cannot be added in place; provision a new node/tenant with seal.enabled: true and migrate data via backup/restore")
	}

	// A standalone node serves every tenant barrier. A clustered node starts
	// its watcher over the placement table once its tenants' Raft groups are
	// composed (ComposeRaftTenantCluster).
	if standaloneHost(infra.Host) {
		if err := startLocalBarrierInitWatcher(n, log); err != nil {
			log.Warn("barrier init: watcher not started; this node initializes no tenant barrier from its owner's statement",
				slog.Any("error", err))
		}
	}

	return n, nil
}

// infraClock returns the node's injected clock, or the real one when the
// composer supplied none.
func infraClock(infra Infra) clock.Clock {
	if infra.Clock == nil {
		return clock.Real()
	}
	return infra.Clock
}

// buildDEKService builds the tenant DEK service. A non-nil provider is the
// composer's dek.Provider seam and is served as is; otherwise the software
// dek.Keyring is built over resolver.
// buildLogService builds the verifiable-log service over the audit resolver
// and the node's databases with the composer's head-signer provider; a nil
// provider is the software default. It returns a nil service and no error when
// the build does not carry the qrdb_vlog tag.
func buildLogService(resolver service.AuditShardResolver, databases service.LogDatabaseLookup, signers vlog.HeadSignerProvider) (service.LogService, error) {
	svc, err := service.NewLogService(service.LogServiceConfig{Resolver: resolver, Databases: databases, Signers: signers})
	var notCompiled *vlog.NotCompiledError
	if errors.As(err, &notCompiled) {
		return nil, nil
	}
	return svc, err
}

func buildDEKService(provider dek.Provider, resolver tenant.StoreResolver, keyWrapRegistry keywrap.Registry, keyWrapProvider keywrap.Provider, keyDir string) (service.DEKServicer, error) {
	if provider != nil {
		svc, err := service.NewDEKService(provider)
		if err != nil {
			return nil, &DEKServiceError{Err: err}
		}
		return svc, nil
	}
	opts := []dek.KeyringOption{}
	if keyWrapRegistry != nil {
		// A shared registry is already populated with whatever extras its
		// composer (or the Backup service, built just above, which
		// applies KeyWrapProvider to this SAME registry first) wants --
		// registering KeyWrapProvider into it a second time here would
		// collide. WithKeyWrapProvider's own doc comment reserves it for
		// "no registry of its own"; a shared registry already covers it.
		opts = append(opts, dek.WithKeyWrapRegistry(keyWrapRegistry))
	} else if keyWrapProvider != nil {
		opts = append(opts, dek.WithKeyWrapProvider(keyWrapProvider))
	}
	opts = append(opts, dek.WithKeyDir(keyDir))
	keyring, err := dek.NewKeyring(resolver, opts...)
	if err != nil {
		return nil, &DEKServiceError{Err: err}
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		return nil, &DEKServiceError{Err: err}
	}
	return svc, nil
}

// buildPolicyService builds the tenant-scoped ABAC policy store over
// resolver.
func buildPolicyService(resolver tenant.StoreResolver) (service.PolicyServicer, error) {
	svc, err := service.NewPolicyService(resolver)
	if err != nil {
		return nil, &PolicyServiceError{Err: err}
	}
	return svc, nil
}

// buildDatabaseQuotaOpts derives the DatabaseService options that wire the
// tenant service's quota and entitlement enforcement. Returns nil when the
// tenant service is not a QuotaProvider (no enforcement possible).
func buildDatabaseQuotaOpts(tenantSvc tenant.TenantServicer, log *slog.Logger) []service.DatabaseServiceOption {
	provider, ok := tenantSvc.(quota.QuotaProvider)
	if !ok {
		return nil
	}
	quotaSvc, err := quota.NewService(provider, log)
	if err != nil {
		return nil
	}
	// Current-usage counters read the persisted per-tenant usage record.
	quotaSvc.RegisterCounter(quota.ResourceDatabases, func(ctx context.Context, tenantID string) (int64, error) {
		return tenantSvc.GetQuotaUsage(ctx, tenantID, quota.ResourceDatabases)
	})
	quotaSvc.RegisterCounter(quota.ResourceRaftGroups, func(ctx context.Context, tenantID string) (int64, error) {
		return tenantSvc.GetQuotaUsage(ctx, tenantID, quota.ResourceRaftGroups)
	})
	quotaSvc.RegisterCounter(quota.ResourceTotalReplicas, func(ctx context.Context, tenantID string) (int64, error) {
		return tenantSvc.GetQuotaUsage(ctx, tenantID, quota.ResourceTotalReplicas)
	})
	// ResourceReplicasPerGroup is a per-group cap evaluated against a zero
	// current usage; the counter always reports zero so a CheckN reduces to
	// "requested replicas <= MaxReplicasPerGroup".
	quotaSvc.RegisterCounter(quota.ResourceReplicasPerGroup, func(ctx context.Context, tenantID string) (int64, error) {
		return 0, nil
	})
	opts := []service.DatabaseServiceOption{service.WithQuotaChecker(quotaSvc)}
	if ec, ok := tenantSvc.(quota.EntitlementChecker); ok {
		opts = append(opts, service.WithEntitlementChecker(ec))
	}
	// Route IsolationVolume database storage under the tenant's DataRoot.
	if lookup, ok := tenantSvc.(service.TenantInfoLookup); ok {
		opts = append(opts, service.WithTenantInfoLookup(lookup))
	}
	// Record per-database raft-group usage so MaxRaftGroups is enforced for each
	// dedicated-raft (IsolationRaft) database.
	if rec, ok := tenantSvc.(service.QuotaUsageRecorder); ok {
		opts = append(opts, service.WithQuotaUsageRecorder(rec))
	}
	return opts
}

// buildSealServices constructs the security services that depend on the
// seal barrier: BarrierService, CredentialService, CeremonyService,
// TenantService, and CertProvider.
func (n *Node) buildSealServices(ctx context.Context, infra Infra, log *slog.Logger, authStore kvstore.KVStore) error {
	if infra.CredentialCoreSvc != nil {
		n.Credential = service.NewCredentialService(infra.CredentialCoreSvc)

		ceremonyCoreSvc, err := ceremonyService(infra, log)
		if err != nil {
			return err
		}
		n.Ceremony = service.NewCeremonyService(ceremonyCoreSvc)
		n.Barrier = service.NewBarrierServiceWithCeremony(infra.SealBarrier, ceremonyCoreSvc, n.barrierServiceOpts(infra)...)
	} else {
		n.Barrier = service.NewBarrierService(infra.SealBarrier, n.barrierServiceOpts(infra)...)
	}

	// The node unseal brings back only the system-managed tenants the node
	// seal sealed, and never one whose own cluster seal record refuses it.
	if infra.BarrierRegistry != nil && infra.ClusterSealer != nil {
		sealer := infra.ClusterSealer
		infra.BarrierRegistry.Attach(seal.WithTenantUnsealGate(func(tenantID string) bool {
			return sealer.Refuses(clusterseal.TenantBarrier(tenantID))
		}))
	}

	if infra.BarrierRegistry != nil {
		// CertProvider uses the KV service as its underlying store and the
		// barrier for transparent encryption of certificate material,
		// unless infra.CertKeyProviderFactory replaces Key B's key material
		// entirely.
		//
		// GUARDED: n.Transaction is already built above, unconditionally (see
		// authStore), so certStore gets kvstore.MultiGuardedBatchKVStore
		// capability at no extra cost -- the built-in cakey.SoftwareProvider
		// behind ClusterCAProv, below, requires it to write its key and
		// certificate records together in one atomic batch.
		certStore, err := NewGuardedKVStoreAdapter(n.KV, n.Index, n.Transaction)
		if err != nil {
			return &CertStoreError{Err: err}
		}
		n.ClientTrustChanges = &trustanchor.Changes{}
		if infra.CertProvider != nil {
			n.CertProv = infra.CertProvider
		} else {
			certOpts := []cert.ProviderOption{
				cert.WithTrustDomain(infra.ClusterTrustDomain),
				cert.WithNewCAHook(n.ClientTrustChanges.Notify),
				// certs:revoke has no single caller every transport funnels
				// through the way certs:issue/certs:enroll go through
				// CertEnrollmentService, so its own audit trail (cert.revoke)
				// belongs here, in the provider every transport's handler
				// calls directly.
				cert.WithAuditLogger(infra.AuditLogger),
			}
			if infra.CertKeyProviderFactory != nil {
				certOpts = append(certOpts, cert.WithKeyProviderFactory(infra.CertKeyProviderFactory))
			}
			// n.Watch is already live (set above, unconditionally on
			// infra.WatchHub): wiring it here only enables
			// DefaultCertProvider.WatchForDeletions, which starts nothing by
			// itself. Every composer calls StartNodeWatchers,
			// below, once, with its own long-lived background context, after
			// BuildServices returns.
			certOpts = append(certOpts, cert.WithWatchService(n.Watch))
			n.CertProv = cert.NewDefaultProvider(certStore, infra.SealBarrier, certOpts...)
		}

		// Cluster CA (Key A): an injected provider replaces the built-in one
		// entirely. Otherwise the built-in cakey.SoftwareProvider is
		// constructed to ADOPT in-memory key material this boot's caller
		// holds -- the bootstrap crash window, generated before any store
		// existed on the node that is bootstrapping the cluster -- or,
		// when the caller holds none, to behave exactly like CertProv (Key
		// B): generate on first use when the store is empty, load when it
		// is not. Every ordinary caller of BuildServices (standalone mode,
		// the SDK, tests) supplies no material at all. Only cmd/qrdbd's
		// Raft-mode bootstrap supplies material, and only while its local
		// ca-key.pem cache still exists; it deletes that cache ONLY after
		// confirming persistence, so "no material AND an empty store"
		// cannot arise from a prior successful boot -- see
		// docs/certificate-authority/architecture.md, "Bootstrap crash
		// window".
		//
		// Construction performs NO I/O: adoption/initialization is lazy,
		// deferred to first Signer/Certificate use. The replicated store is
		// not yet readable immediately after BuildServices runs in Raft
		// mode -- the shard has no leader yet -- so reading it here would
		// race that window on every single boot, not only the bootstrap one.
		switch {
		case infra.ClusterCAKeyProvider != nil:
			n.ClusterCAProv = infra.ClusterCAKeyProvider
		case infra.NoClusterCAKey:
			// This node has no cluster CA key, by the operator's choice,
			// and none is injected: leave ClusterCAProv nil rather than
			// stand up a built-in provider that would generate (and
			// persist) a self-issued Key A nobody asked for. See
			// Infra.NoClusterCAKey.
		default:
			n.ClusterCAProv = clusterca.NewProvider(certStore, infra.SealBarrier,
				cakey.WithAdoptMaterial(infra.ClusterCAKeyPEM, infra.ClusterCACertPEM))
		}

		// Once Key A is reachable through a provider (the replicated store's
		// cakey.SoftwareProvider, or an injected HSM/TPM/KMS one -- either
		// way, n.ClusterCAProv above), ceremony signing moves onto it,
		// overriding whatever CeremonyCA infra.CeremonyCA supplied. That
		// input is the bootstrap-window CeremonyCA
		// (ceremony.NewProviderCA(cakey.StaticProvider), wrapping the
		// in-memory key cmd/qrdbd's awaitUnseal holds before any store
		// exists) built for BuildSealServicesOnly, which has no KV service
		// and so no ClusterCAProv to offer. BuildServices is reached only
		// after the store is live, so from here on ceremony-sign-csr
		// resolves the signer and certificate fresh from ClusterCAProv on
		// every call (ProviderCA's own doc comment) -- reflecting a
		// re-seal/re-unseal or an HSM failover immediately, exactly like
		// every other post-unseal consumer of Key A. This runs BEFORE
		// buildTenantServiceOpts below, which reads the ceremony CA back out
		// via coreSvc.CA() for WithTenantCeremonyCA -- a tenant created from
		// here on must see the SAME override, not the bootstrap-window value
		// that was live when n.Ceremony was first constructed.
		if n.Ceremony != nil && n.ClusterCAProv != nil {
			if coreSvc, ok := service.CeremonyServiceCore(n.Ceremony); ok {
				if providerCA, caErr := ceremony.NewProviderCA(n.ClusterCAProv); caErr == nil {
					// ReplaceCA, not SetCA: the ceremony core service may be
					// the SAME instance reused from the sealed window
					// (Infra.CeremonyCoreSvc), whose SetCA(infra.CeremonyCA)
					// already ran there and set a bootstrap-window CA.
					// SetCA's "never overwrite" rule would make that
					// permanent; ReplaceCA swaps it for ClusterCAProv in
					// every ceremony state, so a joiner and a restarted
					// founder -- which reach here operational, with no CA
					// of their own -- serve every CA operation from the
					// replicated Key A. Holder authorities built earlier
					// share the core's live CA cell and see it too.
					coreSvc.ReplaceCA(providerCA)
				}
			}
		}

		tenantOpts := buildTenantServiceOpts(infra)
		// A tenant onboarded before its barrier existed gets its first admin's
		// account as soon as the barrier is usable.
		tenantOpts = append(tenantOpts, tenant.WithBarrierReadyHook(n.completePendingTenantAdmin(log)))
		if n.Ceremony != nil {
			if coreSvc, ok := service.CeremonyServiceCore(n.Ceremony); ok {
				tenantOpts = append(tenantOpts, tenant.WithTenantHolderDeps(coreSvc.HolderDeps()))
			}
		}
		// A prefix tenant's IAM lives in the shared store, confined to its own
		// "{tenantID}/" namespace -- exactly tenant.IdentityNamespace, the
		// same confinement StoreResolver.sharedFor applies for every OTHER
		// prefix-tenant IAM read/write, including the ones `roles
		// list`/`reconcile` make moments after this CreateTenant call
		// returns. The routed role service (n.Role, reassigned further below)
		// cannot be used here: it is built from n.Tenant, which is this very
		// construction and does not exist yet. Reusing the unscoped
		// n.Role / role.NewStore(authStore) instance instead -- without this
		// namespace wrapper -- would write the seeded roles to a DIFFERENT
		// key than the routed reads later look under, landing roles the
		// tenant's own `roles list` can never see. See tenant.RoleSeeder's
		// doc comment.
		tenantOpts = append(tenantOpts, tenant.WithRoleSeeder(roleSeederAdapter{shared: authStore}))
		// Every node serving an isolated tenant initializes its own key from
		// the owner's signed epoch-1 init statement (startBarrierInitWatcher,
		// startLocalBarrierInitWatcher), so an isolated tenant is admitted, and
		// its statement claimed, only once every member can: both wait on the
		// per-node-barrier-keys activation, which a standalone node does not
		// gate. Onboarding pins each tenant's first admin as the anchor its
		// statements verify against.
		barrierInit := newBarrierInit(n.KV, infraClock(infra))
		perNodeKeys := perNodeBarrierKeysRequired(infra.Host)
		tenantOpts = append(tenantOpts,
			tenant.WithPerNodeBarrierKeysGate(perNodeKeys),
			tenant.WithBarrierInitBook(barrierInit.book(perNodeKeys)))
		// Tenant metadata (TenantInfo) is replicated cluster state: back it with
		// the REPLICATED system shard via the KV service — exactly like the
		// user, role, and placement stores — so a tenant created on one node is
		// visible on every node. The per-tenant barrier lifecycle still runs
		// through infra.BarrierRegistry; only TenantInfo persistence moves onto
		// Raft consensus. (Previously this used infra.SealBarrier, the
		// node-local seal store, which left a created tenant invisible to
		// followers and broke tenant resolution after a leader change/restart.)
		tenantMetaStore := kvSealBackend{KVStore: newKVStoreAdapter(n.KV, n.Index), tx: n.Transaction}
		tenantSvcInst, err := tenant.NewTenantService(
			tenantMetaStore, infra.BarrierRegistry, log, tenantOpts...)
		if err != nil {
			return &TenantServiceError{Err: err}
		}
		n.Tenant = tenantSvcInst
		barrierInit.selfInit = tenantSvcInst
		barrierInit.tenants = tenantSvcInst
		n.BarrierInit = barrierInit

		// One-time, non-fatal compatibility migration: older builds persisted
		// TenantInfo to the node-local seal store. Copy any such records that are
		// not yet in the replicated store so existing tenants survive the upgrade.
		// On a fresh cluster the source is empty and this is a no-op.
		if migrated, mErr := tenant.MigrateMetadata(ctx, infra.SealBarrier, tenantMetaStore, log); mErr != nil {
			log.Warn("tenant metadata migration failed; continuing",
				slog.String("error", mErr.Error()))
		} else if migrated > 0 {
			log.Info("migrated tenant metadata from local seal store to replicated store",
				slog.Int("count", migrated))
		}
	}

	return nil
}

// ceremonyService returns the node's core ceremony service — infra's when
// one is threaded through from the sealed-window phase or across an
// in-process data-plane restart, a new one otherwise — configured with the
// CA, the factor registry and the PIN backend.
// Configuration is applied to a threaded instance too, so the post-unseal
// graph cannot run with dependencies the sealed window had, and a threaded
// instance is rebound to this graph's barrier: a restart builds a new barrier
// instance, and the ceremony service carries the holders and the unclaimed
// envelopes across to it.
func ceremonyService(infra Infra, log *slog.Logger) (*ceremony.CeremonyService, error) {
	svc := infra.CeremonyCoreSvc
	if svc != nil {
		svc.Rebind(infra.SealBarrier)
	}
	if svc == nil {
		dataDir := infra.CeremonyDataDir
		if dataDir == "" {
			dataDir = filepath.Join(infra.DataDir, "seal")
		}
		created, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{
			DataDir:         dataDir,
			ServerCertPEM:   infra.ServerCertPEM,
			ClientCACertPEM: infra.ClientCACertPEM,
		}, infra.SealBarrier, log)
		if err != nil {
			return nil, &CeremonyServiceError{Err: err}
		}
		svc = created
	}
	svc.SetCA(infra.CeremonyCA)
	// Every certificate the ceremony issues names its tenant SPIFFE ID under
	// this domain, agreeing with its OU (auth.TenantFromClientCert).
	svc.SetTrustDomain(infra.ClusterTrustDomain)
	svc.SetFactorRegistry(infra.FactorRegistry)
	// The SO PIN lockout is installed where the composer chose the backend
	// (NewSealStorePINBackend, server.WithPINBackend); NewLockoutBackend is
	// idempotent, so a backend that arrives decorated keeps its one counter,
	// and one that does not is never left without a lockout.
	svc.SetPINBackend(pin.NewLockoutBackend(infra.PINBackend))
	svc.SetQuorumLimiter(QuorumLimitWith(infra.HolderActLimiter))
	svc.SetClock(infra.Clock)
	svc.SetAuditLogger(infra.AuditLogger)
	svc.SetAuditFailurePolicy(infra.AuditFailurePolicy)
	return svc, nil
}

// buildEnrollmentServices constructs tenant onboarding and certificate
// enrolment over the node's FINAL user and role services. It runs after
// BuildServices has replaced n.User and n.Role with their tenant-routed
// versions: built any earlier, the two services would hold the raw shared
// store's IAM services and never see an account of a tenant whose identity
// records are routed elsewhere -- or, for a prefix tenant, see them under
// the wrong namespace -- so a CSR for an account created through the API
// would be refused as "no such account". A node without a certificate
// provider gets neither service.
func (n *Node) buildEnrollmentServices(infra Infra, log *slog.Logger) {
	if n.CertProv == nil {
		return
	}
	certStore := newKVStoreAdapter(n.KV, n.Index)

	// Tenant onboarding and certificate enrolment. Trust anchors and
	// pending tenant admins live in the same replicated store as Key B,
	// each confined to its tenant's identity namespace, unless the
	// embedder injected its own trust store (infra.TrustAnchors). Every
	// change to the anchors is announced on ClientTrustChanges so a
	// composer's client-auth trust pool drops its cached widening at once.
	anchors := infra.TrustAnchors
	if anchors == nil {
		anchors = trustanchor.NewKVRegistry(certStore,
			trustanchor.WithAnchorAuditLogger(infra.AuditLogger),
			trustanchor.WithAnchorAuditPolicy(infra.AuditFailurePolicy))
	}
	n.TrustAnchors = trustanchor.Observe(anchors, n.ClientTrustChanges.Notify)
	n.PendingAdmins = tenantadmin.NewKVStore(certStore)
	n.PendingSOs = tenantadmin.NewSOKVStore(certStore)
	n.CertTargets = service.NewCertTargetGuard()
	enrollmentOpts := []service.CertEnrollmentOption{
		service.WithEnrollmentTrustDomain(infra.ClusterTrustDomain),
		service.WithEnrollmentAuditLogger(infra.AuditLogger),
		service.WithEnrollmentAuditPolicy(infra.AuditFailurePolicy),
		service.WithEnrollmentClientTrust(service.ClientTrustRefresherFunc(n.settleClientTrust)),
	}
	// The tenants' standing delegation policies sit beside their
	// certificates, in the CA that mints under them (#1186).
	if policies, ok := n.CertProv.(cert.DelegationPolicyStore); ok {
		enrollmentOpts = append(enrollmentOpts, service.WithDelegationPolicyStore(policies))
	}
	n.CertEnrollment = service.NewCertEnrollmentService(n.CertProv, n.User, n.TrustAnchors, enrollmentOpts...)
	onboardingDeps := service.TenantOnboardingDeps{
		Tenants:     n.Tenant,
		Users:       n.User,
		Roles:       n.Role,
		Certs:       n.CertProv,
		Anchors:     n.TrustAnchors,
		Pending:     n.PendingAdmins,
		PendingSOs:  n.PendingSOs,
		TrustDomain: infra.ClusterTrustDomain,
		Logger:      log,
		Audit:       infra.AuditLogger,
		AuditPolicy: infra.AuditFailurePolicy,
		ClientTrust: service.ClientTrustRefresherFunc(n.refreshClientTrust),

		SeparationOfDuties: infra.SeparationOfDuties,
	}
	// The anchor every node verifies a tenant barrier's epoch-1 init statement
	// against is the first admin onboarding records.
	if n.BarrierInit != nil {
		onboardingDeps.BarrierAnchors = n.BarrierInit.Anchors
	}
	// The resolver routes isolated tenants to their home stores, so it is
	// also what provisions one; its source is attached once the data plane
	// that hosts the homes is built (AttachTenantHomes in raft mode,
	// AttachLocalTenantHomes on a standalone node).
	if homes, ok := n.StoreResolver.(tenant.HomeRouter); ok {
		onboardingDeps.Homes = homes
	}
	// A tenant whose identity records are behind its own barrier keeps its
	// Key B CA there too, sealed until its first admin initializes it, so that
	// admin's CSR is signed by the cluster CA (Key A), as system first boot
	// signs its own admin's. A node with no cluster CA key (BYO-TLS) leaves
	// the seam nil: the onboarding service signs a system-managed barrier's
	// admin with its Key B, which serves from create, and refuses a
	// self-managed one's CSR as system first boot refuses one, so that admin
	// brings their own certificate.
	if n.ClusterCAProv != nil {
		signer, err := clusterCAAdminSigner(n.ClusterCAProv, infra.ClusterTrustDomain)
		if err != nil {
			log.Warn("lifecycle: tenant first-admin CSRs for self-managed isolated tenants will be refused",
				slog.String("error", err.Error()))
		} else {
			onboardingDeps.PendingAdminSigner = signer
		}
	}
	n.onboarding = service.NewTenantOnboardingService(onboardingDeps)
	n.TenantOnboarding = n.onboarding

	// Invitation self-enrolment. invitationUsers is n.User (the FINAL,
	// tenant-routed service this function runs after) asserted to
	// user.CertActivator too; the routing service implements both, so this
	// never fails in production -- it guards only a Users seam a future
	// composer might narrow.
	if invitationUsers, ok := n.User.(service.InvitationUsers); ok && n.StoreResolver != nil {
		invites, err := invite.NewTenantStore(n.StoreResolver)
		// A TTL Config.Validate already rejected is refused here too, rather
		// than guessed at with a fallback: an invitation's lifetime is a
		// security-relevant bound, not a value to silently substitute for.
		ttl, terr := qrdbconfig.ParseInvitationTTL(infra.InvitationTTL)
		perSource, perTenant, lerr := infra.InvitationRedeemLimit.Resolved()
		if err != nil || terr != nil || lerr != nil {
			log.Warn("lifecycle: invitation self-enrolment is disabled",
				"store_error", err, "ttl_error", terr, "redeem_limit_error", lerr)
		} else {
			// Redemption is the one verb any caller may make with no
			// credential, so it is always throttled: a composer's limiter
			// when injected (its owner closes it), else a per-node token
			// bucket this node closes on stop.
			limiter := infra.InvitationRedeemLimiter
			if limiter == nil {
				bucket := ratelimit.NewTokenBucketLimiter(ratelimit.WithClock(infra.Clock))
				n.addCloser(bucket.Close)
				limiter = bucket
			}
			n.Invitations = service.NewInvitationService(service.InvitationDeps{
				RedeemLimiter:   limiter,
				RedeemPerSource: ratelimit.Policy{RatePerSecond: perSource.RatePerSecond, Burst: perSource.Burst},
				RedeemPerTenant: ratelimit.Policy{RatePerSecond: perTenant.RatePerSecond, Burst: perTenant.Burst},
				Now:             infraClock(infra).Now,
				Invitations:     invites,
				Users:           invitationUsers,
				Certs:           n.CertProv,
				Anchors:         n.TrustAnchors,
				Tenants:         n.Tenant,
				TTL:             ttl,
				TrustDomain:     infra.ClusterTrustDomain,
				AuditLogger:     infra.AuditLogger,
				AuditPolicy:     infra.AuditFailurePolicy,
				RequireKeyPin:   infra.InvitationRequireKeyPin,
			})
		}
	}
}

// BuildSealServicesOnly constructs the subset of seal-gated services that
// do NOT require a Raft host: Barrier, Credential, Ceremony, and (when a
// BarrierRegistry is supplied) Tenant. It is intended for the pre-host
// "sealed-window" boot phase where the data-plane Pebble instance cannot
// yet be opened because the storage barrier is sealed.
//
// The returned Node has nil core data-plane services (KV, MVCC, Watch,
// etc.) and nil CertProvider. The caller is expected to bring the host
// up after the operator drives the unseal API and to then call
// BuildServices for the full service graph; the seal service instances
// returned here can be threaded into BuildServices' Infra (via
// SealBarrier, BarrierRegistry, CredentialCoreSvc) so the post-unseal
// graph reuses them rather than re-instantiating.
//
// infra.SealBarrier and infra.CredentialCoreSvc are required. infra.Host
// and infra.WatchHub are NOT consulted and may be nil. infra.DataDir is
// used only as the default for the ceremony data dir when
// infra.CeremonyDataDir is empty.
func BuildSealServicesOnly(ctx context.Context, infra Infra) (*Node, error) {
	_ = ctx // reserved for future use by barrier operations
	if infra.SealBarrier == nil {
		return nil, &NilSealBarrierError{}
	}
	if infra.CredentialCoreSvc == nil {
		return nil, &NilCredentialCoreError{}
	}

	log := infra.Logger
	if log == nil {
		log = slog.Default()
	}

	n := &Node{}
	n.Credential = service.NewCredentialService(infra.CredentialCoreSvc)

	ceremonyCoreSvc, err := ceremonyService(infra, log)
	if err != nil {
		return nil, err
	}
	n.Ceremony = service.NewCeremonyService(ceremonyCoreSvc)
	n.Barrier = service.NewBarrierServiceWithCeremony(infra.SealBarrier, ceremonyCoreSvc)

	if infra.BarrierRegistry != nil {
		tenantOpts := buildTenantServiceOpts(infra)
		tenantOpts = append(tenantOpts, tenant.WithTenantHolderDeps(ceremonyCoreSvc.HolderDeps()))
		// The sealed-window service graph has no Raft host (the data plane
		// cannot be opened while the storage barrier is sealed), so this tenant
		// service is necessarily backed by the node-local seal store rather than
		// the replicated system shard. That is acceptable because the
		// sealed-window listener exposes ONLY the barrier, ceremony, and
		// credential services — tenant CRUD is never invoked during the sealed
		// window. The replicated, cluster-visible tenant service is built
		// post-host in BuildServices (buildSealServices); per-tenant barrier
		// restore at boot (BarrierRegistry.RestoreVolumeTenants) reads the
		// volume catalog's Assignments and the registry's own
		// __registry:tenant_posture/ records, not TenantInfo, so this
		// seal-backed instance is never consulted for tenant resolution.
		tenantSvcInst, err := tenant.NewTenantService(
			infra.SealBarrier, infra.BarrierRegistry, log, tenantOpts...)
		if err != nil {
			return nil, &TenantServiceError{Err: err}
		}
		n.Tenant = tenantSvcInst
	}

	return n, nil
}

// resolveReplicaID maps a configured Raft ReplicaID onto the Snowflake node
// field. A zero ReplicaID (standalone default not yet materialised) maps to
// node 1, matching the qrdb standalone convention. Values above the 10-bit
// Snowflake node range are rejected by dao.NewSnowflakeGenerator.
func resolveReplicaID(id uint64) uint64 {
	if id == 0 {
		return 1
	}
	return id
}

// buildTenantServiceOpts returns the TenantServiceOptions derived from the
// Infra. The TenantService resolves barrier strategies through
// Infra.BarrierKeyProvider, else the default provider over
// Infra.StrategyRegistry, and names this node by nodeIdentity in every
// barrier-key request.
func buildTenantServiceOpts(infra Infra) []tenant.TenantServiceOption {
	// An undone create's acknowledgements are written under nodeIdentity and
	// counted against a roster of the same identities.
	opts := []tenant.TenantServiceOption{
		tenant.WithNodeID(nodeIdentity(infra.Host)),
		tenant.WithTenantUndoGate(tenantUndoRequired(infra.Host)),
		tenant.WithUndoCleanupRoster(undoCleanupRoster(infra.Host)),
	}
	// Reserve the node's own data directory so an operator-supplied dedicated
	// DataRoot (WithDataRoot) can never be placed equal to, above, or below the
	// system data dir — which would nest a tenant's dedicated disk inside the
	// daemon's own storage and cause data-confusion. Pool-base and existing-
	// tenant overlap are checked unconditionally in CreateTenant; this activates
	// the system-data-dir arm of that check in production.
	if infra.DataDir != "" {
		opts = append(opts, tenant.WithReservedDataRoots(infra.DataDir))
	}
	switch {
	case infra.BarrierKeyProvider != nil:
		opts = append(opts, tenant.WithBarrierKeyProvider(infra.BarrierKeyProvider))
	case infra.StrategyRegistry != nil:
		opts = append(opts, tenant.WithBarrierKeyProvider(seal.NewBarrierKeyProvider(infra.StrategyRegistry)))
	}
	if infra.DefaultTenantQuotas != nil {
		opts = append(opts, tenant.WithDefaultQuotas(infra.DefaultTenantQuotas))
	}
	if infra.DefaultTenantEntitlements != nil {
		opts = append(opts, tenant.WithDefaultEntitlements(infra.DefaultTenantEntitlements))
	}
	if infra.ClusterSealer != nil {
		opts = append(opts, tenant.WithClusterSealer(infra.ClusterSealer))
	}
	// The TenantService's own volume-pool reservation
	// (reserveAndRegisterVolumeTenant) needs the SAME allocator
	// BarrierRegistry resolves tenant placement through -- see
	// BarrierRegistry.VolumeAllocator's doc comment. Composers (cmd/qrdbd,
	// sdk/go/node) attach the allocator onto BarrierReg themselves; this is
	// the single place that forwards it into the TenantService they share.
	if infra.BarrierRegistry != nil {
		if allocator := infra.BarrierRegistry.VolumeAllocator(); allocator != nil {
			opts = append(opts, tenant.WithVolumeAllocator(allocator))
		}
	}
	return opts
}

// databaseUnsealGate is the node unseal's veto for a database barrier: the
// cluster seal record of the database, named by its tenant and name as the
// barrier is keyed, refuses it.
func databaseUnsealGate(sealer clusterseal.Sealer) func(name, tenantID string) bool {
	return func(name, tenantID string) bool {
		return sealer.Refuses(clusterseal.DatabaseBarrier(tenantid.Normalize(tenantID), name))
	}
}

// barrierServiceOpts returns the BarrierService options infra asks for.
func (n *Node) barrierServiceOpts(infra Infra) []service.BarrierServiceOption {
	var opts []service.BarrierServiceOption
	if infra.ClusterSealer != nil {
		opts = append(opts, service.WithClusterSealer(infra.ClusterSealer))
	}
	// A seal of the node barrier seals the whole node: every tenant barrier
	// with it.
	if infra.BarrierRegistry != nil {
		opts = append(opts, service.WithBarrierRegistry(infra.BarrierRegistry))
	}
	if infra.DatabaseManager != nil {
		opts = append(opts, service.WithBarrierDatabases(infra.DatabaseManager))
	}
	// It pauses every Raft shard the node hosts before any key goes.
	if infra.ShardPauser != nil {
		opts = append(opts, service.WithShardPauser(infra.ShardPauser))
	}
	// Its status reports a cluster-wide encryption migration from the
	// replicated record, so every node answers the same.
	if n.ClusterMigStore != nil {
		opts = append(opts, service.WithClusterMigrations(n.ClusterMigStore))
	}
	return opts
}

// newKVStoreAdapter returns a kvstore.AtomicKVStore backed by the given
// KVServicer. Writes are proposed through the service; reads are
// linearizable. Every engine implements PutIfAbsent/CompareAndSwap/
// CompareAndDelete natively (kvStoreAdapter's own doc comment), so the
// widened return type costs nothing and lets a caller needing only the
// single-key atomic capability use this adapter directly rather than
// re-asserting for a capability it already has. A caller that needs
// MULTI-key atomic batches instead -- cakey.NewSoftwareProvider's split
// key/certificate records, in particular -- needs
// kvstore.MultiGuardedBatchKVStore, which this adapter does NOT provide;
// see NewGuardedKVStoreAdapter below.
//
// idx is the IndexServicer for the SAME shard, which is what makes the
// returned store's secondary indexes real. Pass nil only where no index
// subsystem exists; the store then refuses index operations with
// kvstore.ErrIndexUnsupported rather than answering them empty.
func newKVStoreAdapter(svc service.KVServicer, idx service.IndexServicer) kvstore.AtomicKVStore {
	return &kvStoreAdapter{svc: svc, idx: idx}
}

// newMinimalLogger wraps a *slog.Logger as a *logger.Logger for packages
// that require the project's structured logger type. A warn-level JSON
// logger is created from the slog.Logger's handler level.
func newMinimalLogger(log *slog.Logger) *logger.Logger {
	_ = log // slog.Logger attrs not currently extractable into LogConfig
	l, err := logger.New(logger.LogConfig{
		Level:  "warn",
		Format: "json",
		Output: "stderr",
	})
	if err != nil {
		panic("lifecycle: failed to create minimal logger: " + err.Error())
	}
	return l
}

// buildUserAuthFactorService migrates any legacy untenanted auth-factor
// records into their owning tenants and assigns the tenant-routed M14 service
// to n.UserAuthFactor.
//
// It is called from BOTH IAM-routing branches — seal-enabled and seal-disabled
// — for the same reason routing itself is: keyspace isolation is not a
// property of encryption, and a node with the seal subsystem off still has
// tenants whose credentials must not resolve each other's logins
// (go-qrdb#393).
//
// legacy is the UNCONFINED shared store, which is exactly what the resolver
// wraps; the migration reads the flat "__sys:auth/..." keys out of it and
// writes each record into "{tenant}/__sys:auth/...". It runs inside the
// service, before the service answers anything — NOT here. This function is
// called while the shard is still electing, and a linearizable read at that
// moment fails with "not leader, leader unknown" on a node that is otherwise
// healthy, which would turn a migration into a failed boot.
func (n *Node) buildUserAuthFactorService(_ context.Context, resolver tenant.StoreResolver, legacy kvstore.KVStore, trail audit.AuditLogger, log *slog.Logger) error {
	svc, err := service.NewRoutingUserAuthFactorService(service.RoutingUserAuthFactorServiceConfig{
		Resolver:    resolver,
		LegacyStore: legacy,
		AuditLogger: trail,
		Logger:      log,
	})
	if err != nil {
		return &UserAuthFactorServiceError{Err: err}
	}
	n.UserAuthFactor = svc
	return nil
}

// UserStoreOptions maps the operator's auth.mtls_binding_policy onto the user
// store options that enforce it at the data layer, with logger as the store's
// logger. Under "required" the store refuses every change that would leave the
// bootstrap admin unable to log in by certificate -- deleting, renaming,
// disabling or suspending it, dropping mtls from its methods, or withdrawing
// its last live binding (user.WithBootstrapAdminBindingRequired) -- and warns
// through logger as the admin's last key nears expiry. An unrecognised value is
// an *auth.InvalidMTLSBindingPolicyError. BuildServices applies it; a composer
// that builds its own user stores -- the daemon does -- applies the same
// options through this one function.
func UserStoreOptions(mtlsBindingPolicy string, logger *slog.Logger) ([]user.Option, error) {
	policy, err := auth.ParseMTLSBindingPolicy(mtlsBindingPolicy)
	if err != nil {
		return nil, err
	}
	opts := []user.Option{user.WithLogger(logger)}
	if policy == auth.MTLSBindingRequired {
		opts = append(opts, user.WithBootstrapAdminBindingRequired())
	}
	return opts, nil
}

// StateMachineKeepsRevisionHistory reports whether the registered state
// machine type smType keeps MVCC revision history: its descriptor declares
// statemachine.CapabilityMVCC. An unregistered type is reported as keeping
// none.
func StateMachineKeepsRevisionHistory(smType string) bool {
	return keepsRevisionHistory(statemachine.Describe, smType)
}

// keepsRevisionHistory applies StateMachineKeepsRevisionHistory's rule to
// the descriptor describe returns for smType, so a test can supply a
// test-local registry instead of registering into the process-global one.
func keepsRevisionHistory(describe func(string) (statemachine.Descriptor, error), smType string) bool {
	d, err := describe(smType)
	if err != nil {
		return false
	}
	return slices.Contains(d.Capabilities, statemachine.CapabilityMVCC)
}
