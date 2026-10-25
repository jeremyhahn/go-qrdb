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

// Package embedded implements the in-process transport for the QRDB SDK.
// It wraps the service layer interfaces directly, bypassing network
// serialization for maximum performance when the QRDB engine is embedded
// in the same process.
package embedded

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.Transport         = (*Transport)(nil)
	_ transport.KVClient          = (*Transport)(nil)
	_ transport.GuardedKVClient   = (*Transport)(nil)
	_ transport.AdminClient       = (*Transport)(nil)
	_ transport.MVCCClient        = (*Transport)(nil)
	_ transport.TransactionClient = (*Transport)(nil)
	_ transport.BatchClient       = (*Transport)(nil)
	_ transport.WatchClient       = (*Transport)(nil)
	_ transport.LeaseClient       = (*Transport)(nil)
	_ transport.IndexClient       = (*Transport)(nil)
	_ transport.TimeSeriesClient  = (*Transport)(nil)
	_ transport.VectorClient      = (*Transport)(nil)
	_ transport.CeremonyClient    = (*Transport)(nil)
	_ transport.CredentialClient  = (*Transport)(nil)
	_ transport.BackupClient      = (*Transport)(nil)
	_ transport.RebalanceClient   = (*Transport)(nil)
	_ transport.DEKClient         = (*Transport)(nil)
	_ transport.CollectionClient  = (*Transport)(nil)
	_ transport.SealStoreClient   = (*Transport)(nil)
	_ transport.TenantClient      = (*Transport)(nil)
	_ transport.LockClient        = (*Transport)(nil)
	_ transport.Client            = (*Transport)(nil)

	// Phase 3 provider assertions.
	_ transport.PermissionProvider = (*Transport)(nil)
	_ transport.RBACProvider       = (*Transport)(nil)
	_ transport.FactorProvider     = (*Transport)(nil)
	_ transport.LoginProvider      = (*Transport)(nil)
	_ transport.SessionProvider    = (*Transport)(nil)
	_ transport.AuditProvider      = (*Transport)(nil)
)

// Transport implements the QRDB SDK transport interface by delegating
// directly to the service layer. No network I/O or serialization is
// performed, making this the fastest possible transport for embedded
// usage.
type Transport struct {
	kvSvc       service.KVServicer
	adminSvc    service.AdminServicer
	mvccSvc     service.MVCCKVServicer
	txSvc       service.TransactionServicer
	batchSvc    service.BatchServicer
	watchSvc    service.WatchServicer
	leaseSvc    service.LeaseServicer
	indexSvc    service.IndexServicer
	tsSvc       service.TimeSeriesServicer
	vectorSvc   service.VectorServicer
	userSvc     user.UserServicer
	roleSvc     role.RoleServicer
	certProv    cert.CertProvider
	certEnroll  service.CertEnrollmentServicer
	invitations service.InvitationServicer
	onboarding  service.TenantOnboardingServicer
	dbSvc       service.DatabaseServicer
	barrierSvc  service.BarrierServicer
	pinBackend  pin.PINBackend

	// holderActLimiter throttles a share holder's claim begin and complete
	// made with no identity on the context; see WithHolderActLimiter.
	holderActLimiter HolderActLimitFunc

	dekSvc          service.DEKServicer
	policySvc       service.PolicyServicer
	ceremonySvc     service.CeremonyServicer
	ceremonyCA      ceremony.CeremonyCA
	credentialSvc   service.CredentialServicer
	backupSvc       service.BackupServicer
	rebalanceSvc    service.RebalanceServicer
	embeddingSvc    service.EmbeddingServicer
	ragSvc          service.RAGServicer
	collectionSvc   service.CollectionServicer
	sealStoreSvc    *seal.SealStore
	tenantSvc       tenant.TenantServicer
	volumeSvc       volume.Service
	lockSvc         service.LockServicer
	authFactorSvc   service.AuthFactorServicer
	auditLogger     audit.AuditLogger
	replicatedAudit *service.OptInAuditLogger
	encMigrator     service.EncryptionMigrator

	// sealProbe gates every request outside the sealed-window allowlist; see
	// WithSealProbe. Nil gates nothing.
	sealProbe SealProbe

	// awaitingAdminCredentialProbe gates every request outside the
	// awaiting-admin-credential allowlist (go-qrdb#548 review item 3); see
	// WithAwaitingAdminCredentialProbe. Nil gates nothing.
	awaitingAdminCredentialProbe AwaitingAdminCredentialProbe

	// sealStreams is the node's cluster-seal stream registry; see
	// WithSealStreams. Nil leaves this transport's own watches unguarded.
	sealStreams *clusterseal.Streams

	// registry is the node's per-(tenant, database) service registry, the
	// seam WithDatabase scopes the data plane through and every named-
	// database SQL query resolves through. nil means the transport can
	// serve only the node's default service set. It is a live-resolving
	// adapter value (e.g. the node's own, which reads its current server on
	// every call), not a snapshot, so the field itself never needs to
	// change once WithServiceRegistry sets it.
	registry service.Registry

	// sqlOnce/sqlSvcCache lazily construct the SQL query service from
	// kvSvc/dbSvc on first Query call. See transport_sql.go (compiled
	// only under the qrdb_sql build tag; without it the fields are
	// unused and the transport does not implement transport.SQLProvider).
	sqlOnce     sync.Once
	sqlSvcCache service.SQLServicer

	// authMW is the AuthMiddleware the node's network transports enforce
	// with, installed by WithAuthMiddleware so this transport's gated
	// writes are authorized by the same instance. Nil -- the default, and
	// what sdk/go/node installs when auth is disabled -- authorizes
	// nothing, exactly as a network transport with no AuthMiddleware.
	authMW *authmw.AuthMiddleware

	// binder binds the caller's identity into every call's context before
	// the auth middleware sees it; see WithContextBinder. Nil binds nothing.
	binder *contextBinder

	// Phase 3 auth/identity wiring. All optional; SDK accessors return
	// typed Missing*Error values when the corresponding service is not
	// configured.
	rbacAuthz         auth.Authorizer
	userAuthFactorSvc service.UserAuthFactorServicer
	loginResolver     *auth.LoginResolver
	sessionIssuer     SessionIssuer
	identityHydrator  IdentityHydrator
	factorVerifierMu  sync.RWMutex
	factorVerifiers   map[transport.FactorType]FactorVerifier
	loginSessions     *loginSessionStore
	poolManager       objpool.Manager
	poolStateStore    objpool.StateStore

	// forwarding makes a database-scoped clone forward what the node refuses
	// as a non-replica; see WithReplicaForwarding. Nil forwards nothing.
	forwarding *replicaForwarding

	// nowFunc is overridable so tests can drive deterministic time
	// without monkey-patching. Defaults to time.Now().UTC at New.
	nowFunc func() time.Time

	closed atomic.Bool
}

// Option configures an embedded transport.
type Option func(*Transport)

// WithMVCCService injects an MVCCKVServicer into the transport.
func WithMVCCService(svc service.MVCCKVServicer) Option {
	return func(t *Transport) {
		t.mvccSvc = svc
	}
}

// WithTransactionService injects a TransactionServicer into the transport.
func WithTransactionService(svc service.TransactionServicer) Option {
	return func(t *Transport) {
		t.txSvc = svc
	}
}

// WithBatchService injects a BatchServicer into the transport.
func WithBatchService(svc service.BatchServicer) Option {
	return func(t *Transport) {
		t.batchSvc = svc
	}
}

// WithWatchService injects a WatchServicer into the transport.
func WithWatchService(svc service.WatchServicer) Option {
	return func(t *Transport) {
		t.watchSvc = svc
	}
}

// WithLeaseService injects a LeaseServicer into the transport.
func WithLeaseService(svc service.LeaseServicer) Option {
	return func(t *Transport) {
		t.leaseSvc = svc
	}
}

// WithIndexService injects an IndexServicer into the transport.
func WithIndexService(svc service.IndexServicer) Option {
	return func(t *Transport) {
		t.indexSvc = svc
	}
}

// WithTimeSeriesService injects a TimeSeriesServicer into the transport.
func WithTimeSeriesService(svc service.TimeSeriesServicer) Option {
	return func(t *Transport) {
		t.tsSvc = svc
	}
}

// WithVectorService injects a VectorServicer into the transport.
func WithVectorService(svc service.VectorServicer) Option {
	return func(t *Transport) {
		t.vectorSvc = svc
	}
}

// WithRAGService injects a RAGServicer into the transport. The option setter
// is ungated so untagged callers (e.g. the node builder) can wire it; the
// RAGClient method implementations live in the qrdb_vector-gated
// transport_rag.go.
func WithRAGService(svc service.RAGServicer) Option {
	return func(t *Transport) {
		t.ragSvc = svc
	}
}

// WithUserService injects a UserServicer into the transport.
func WithUserService(svc user.UserServicer) Option {
	return func(t *Transport) {
		t.userSvc = svc
	}
}

// WithRoleService injects a RoleServicer into the transport.
func WithRoleService(svc role.RoleServicer) Option {
	return func(t *Transport) {
		t.roleSvc = svc
	}
}

// WithCertProvider injects a CertProvider into the transport.
func WithCertProvider(prov cert.CertProvider) Option {
	return func(t *Transport) {
		t.certProv = prov
	}
}

// WithCertEnrollment sets the certificate issue/enrol and trust-anchor
// service (lifecycle.Node.CertEnrollment).
func WithCertEnrollment(svc service.CertEnrollmentServicer) Option {
	return func(t *Transport) {
		t.certEnroll = svc
	}
}

// WithInvitations sets the invitation self-enrolment service
// (lifecycle.Node.Invitations): users:invite and the unauthenticated
// RedeemInvitation.
func WithInvitations(svc service.InvitationServicer) Option {
	return func(t *Transport) {
		t.invitations = svc
	}
}

// WithTenantOnboarding sets the tenant-create service
// (lifecycle.Node.TenantOnboarding).
func WithTenantOnboarding(svc service.TenantOnboardingServicer) Option {
	return func(t *Transport) {
		t.onboarding = svc
	}
}

// WithDatabaseService injects a DatabaseServicer into the transport.
func WithDatabaseService(svc service.DatabaseServicer) Option {
	return func(t *Transport) {
		t.dbSvc = svc
	}
}

// WithCollectionService injects a CollectionServicer into the transport.
func WithCollectionService(svc service.CollectionServicer) Option {
	return func(t *Transport) {
		t.collectionSvc = svc
	}
}

// New creates a new embedded transport that delegates to the given
// service implementations. The kvSvc and adminSvc parameters must be
// non-nil. Optional services are injected via functional options.
//
// kvSvc and, when configured via WithMVCCService/WithTransactionService/
// WithBatchService/WithWatchService, the MVCC, transaction, batch and
// watch services are wrapped with this package's hard-confinement
// decorators (service.NewHardConfinedKV and friends) HERE, after options
// have been applied, rather than by each caller that builds one of these
// services. New is the one seam every composer of an embedded transport
// goes through -- sdk/go/standalone.NewServer, sdk/go/node, the desktop's
// cmd/qrdb/bindings_server.go, and cmd/qrdbd/start.go -- and these
// services are UN-namespaced (see service.hardConfinedPrefixes' own doc
// comment): no composite (tenant, db) prefix sits in front of them the
// way pkg/server's registry prepends one, so a literal
// "__sys:cluster_ca/..." request physically reaches that record if the
// opened data directory was ever part of a cluster. Wrapping only inside
// NewServer left every other composer -- including the desktop webview's
// bindings, which talk to a Transport built by bindings_server.go, not
// NewServer -- unprotected (go-qrdb#570). The wrap constructors are
// nil-safe: a service a particular composer never configured stays nil.
// pkg/cakey's own n.KV/n.Transaction access does not go through this
// function at all, so it is unaffected.
func New(kvSvc service.KVServicer, adminSvc service.AdminServicer, opts ...Option) (*Transport, error) {
	if kvSvc == nil {
		return nil, &NilServiceError{Service: "KVServicer"}
	}
	if adminSvc == nil {
		return nil, &NilServiceError{Service: "AdminServicer"}
	}
	t := &Transport{
		kvSvc:         service.NewHardConfinedKV(kvSvc),
		adminSvc:      adminSvc,
		loginSessions: newLoginSessionStore(),
		nowFunc:       func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.binder != nil && t.binder.bind == nil {
		return nil, &NilContextBinderError{}
	}
	t.mvccSvc = service.NewHardConfinedMVCC(t.mvccSvc)
	t.txSvc = service.NewHardConfinedTransaction(t.txSvc)
	t.batchSvc = service.NewHardConfinedBatch(t.batchSvc)
	t.watchSvc = t.admitWatch(service.NewHardConfinedWatch(t.watchSvc))
	return t, nil
}

// admitWatch wraps svc so every event it delivers is re-admitted through
// t.authMW, ending a watch whose tenant was suspended after it opened. It
// is the one place a watch service this transport serves gets its stream
// admission: New applies it to the configured service and WithDatabase to
// each database-scoped one, whose registry may admit nothing of its own
// (the public server.NewRESTServiceRegistry does not). svc is returned
// unchanged when no AuthMiddleware is installed.
func (t *Transport) admitWatch(svc service.WatchServicer) service.WatchServicer {
	if t.authMW == nil {
		return svc
	}
	return service.NewAdmittedWatch(svc, t.authMW)
}

// Connect is a no-op for embedded transports since the service layer
// is already available in-process.
func (t *Transport) Connect(_ context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return nil
}

// Close marks the transport as closed. Subsequent operations will
// return ClosedError.
func (t *Transport) Close() error {
	t.closed.Store(true)
	return nil
}

// Healthy returns true when the transport is open. For embedded
// transports, the service layer is always reachable if not closed.
func (t *Transport) Healthy(_ context.Context) bool {
	return !t.closed.Load()
}

// Request dispatches a generic request to the appropriate service method
// based on the method name. This is provided for interface compliance;
// callers should prefer the typed KVClient and AdminClient methods.
func (t *Transport) Request(ctx context.Context, method string, req, resp any) error {
	ctx, err := t.admit(ctx, "Request")
	if err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	handler, ok := requestHandlers[method]
	if !ok {
		return &UnknownMethodError{Method: method}
	}

	return handler(ctx, t, req, resp)
}

// requestHandler is a function that handles a generic Request dispatch.
type requestHandler func(ctx context.Context, t *Transport, req, resp any) error

// requestHandlers maps method names to their handler functions for O(1)
// dispatch in the generic Request method.
var requestHandlers = map[string]requestHandler{
	"Put":                         handlePut,
	"Get":                         handleGet,
	"Delete":                      handleDelete,
	"List":                        handleList,
	"Health":                      handleHealth,
	"Status":                      handleStatus,
	"Peers":                       handlePeers,
	"AddMember":                   handleAddMember,
	"RemoveMember":                handleRemoveMember,
	"PromoteMember":               handlePromoteMember,
	"RetireJoinIdentity":          handleRetireJoinIdentity,
	"RequestSnapshot":             handleRequestSnapshot,
	"Drain":                       handleDrain,
	"PutWithLease":                handlePutWithLease,
	"GetRevision":                 handleGetRevision,
	"GetAtRevision":               handleGetAtRevision,
	"Range":                       handleRange,
	"Compact":                     handleCompact,
	"CurrentRevision":             handleCurrentRevision,
	"RestoreToRevision":           handleRestoreToRevision,
	"ExecuteTransaction":          handleExecuteTransaction,
	"ExecuteBatch":                handleExecuteBatch,
	"Watch":                       handleWatch,
	"WatchPrefix":                 handleWatchPrefix,
	"LeaseGrant":                  handleLeaseGrant,
	"LeaseRevoke":                 handleLeaseRevoke,
	"LeaseKeepAlive":              handleLeaseKeepAlive,
	"LeaseTimeToLive":             handleLeaseTimeToLive,
	"LeaseList":                   handleLeaseList,
	"TSWrite":                     handleTSWrite,
	"TSWriteBatch":                handleTSWriteBatch,
	"TSQuery":                     handleTSQuery,
	"TSAggregate":                 handleTSAggregate,
	"TSWritePoint":                handleTSWritePoint,
	"TSRichQuery":                 handleTSRichQuery,
	"TSDeleteSeries":              handleTSDeleteSeries,
	"TSGetRetention":              handleTSGetRetention,
	"TSSetRetention":              handleTSSetRetention,
	"TSListMetrics":               handleTSListMetrics,
	"Capabilities":                handleCapabilities,
	"StartShard":                  handleStartShard,
	"StopShard":                   handleStopShard,
	"GetShardState":               handleGetShardState,
	"GetTopology":                 handleGetTopology,
	"ReplicaStates":               handleReplicaStates,
	"CreateNamespace":             handleCreateNamespace,
	"DeleteNamespace":             handleDeleteNamespace,
	"ListNamespaces":              handleListNamespaces,
	"GetNamespaceInfo":            handleGetNamespaceInfo,
	"UpsertVector":                handleUpsertVector,
	"GetVector":                   handleGetVector,
	"DeleteVector":                handleDeleteVector,
	"BatchUpsert":                 handleBatchUpsert,
	"BatchDeleteVectors":          handleBatchDeleteVectors,
	"ListVectors":                 handleListVectors,
	"UpdateMetadata":              handleUpdateMetadata,
	"SearchSimilar":               handleSearchSimilar,
	"SearchByMetadata":            handleSearchByMetadata,
	"HybridSearch":                handleHybridSearch,
	"SearchByID":                  handleSearchByID,
	"RebuildIndex":                handleRebuildIndex,
	"GetRebuildProgress":          handleGetRebuildProgress,
	"GetIndexStats":               handleGetIndexStats,
	"CompactNamespace":            handleCompactNamespace,
	"GetCompactionProgress":       handleGetCompactionProgress,
	"TenantDEKStatus":             dekRequestHandler("TenantDEKStatus", (*Transport).TenantDEKStatus),
	"TenantDEKRotate":             dekRequestHandler("TenantDEKRotate", (*Transport).TenantDEKRotate),
	"TenantDEKRetire":             dekRequestHandler("TenantDEKRetire", (*Transport).TenantDEKRetire),
	"TenantDEKEncrypt":            dekRequestHandler("TenantDEKEncrypt", (*Transport).TenantDEKEncrypt),
	"TenantDEKDecrypt":            dekRequestHandler("TenantDEKDecrypt", (*Transport).TenantDEKDecrypt),
	"TenantDEKRewrap":             dekRequestHandler("TenantDEKRewrap", (*Transport).TenantDEKRewrap),
	"BarrierInitialize":           handleBarrierInitialize,
	"BarrierUnseal":               handleBarrierUnseal,
	"BarrierSeal":                 handleBarrierSeal,
	"BarrierStatus":               handleBarrierStatus,
	"BarrierInitializeShamir":     handleBarrierInitializeShamir,
	"BarrierGenerateRecoveryKeys": handleBarrierGenerateRecoveryKeys,
	"BarrierDeleteRecoveryKeys":   handleBarrierDeleteRecoveryKeys,
	"BarrierHasRecoveryKeys":      handleBarrierHasRecoveryKeys,
	"CeremonyInitialize":          handleCeremonyInitialize,
	"CeremonyStatus":              handleCeremonyStatus,
	"CeremonySignCSR":             handleCeremonySignCSR,
	"CredentialSubmit":            handleCredentialSubmit,
	"CredentialStrategy":          handleCredentialStrategy,
	"CredentialDelete":            handleCredentialDelete,
	"CredentialList":              handleCredentialList,
	"CredentialRotate":            handleCredentialRotate,
	"CredentialListVersions":      handleCredentialListVersions,
	"CredentialGCExpired":         handleCredentialGCExpired,
	"IndexCreate":                 handleIndexCreate,
	"IndexDrop":                   handleIndexDrop,
	"IndexQuery":                  handleIndexQuery,
	"IndexScan":                   handleIndexScan,
	"IndexList":                   handleIndexList,
}

// ---------------------------------------------------------------------------
// SDK option to service-layer mapping helpers
// ---------------------------------------------------------------------------

// writeModeMap provides O(1) mapping from transport-layer WriteMode to
// the service-layer writemode.Mode.
var writeModeMap = map[transport.WriteMode]writemode.Mode{
	transport.WriteModeSync:       writemode.Sync,
	transport.WriteModeWALDurable: writemode.WALDurable,
	transport.WriteModeBatched:    writemode.Batched,
}

// mapWriteMode converts a transport-layer WriteMode to the corresponding
// service-layer writemode.Mode. Unrecognized modes default to Sync.
func mapWriteMode(m transport.WriteMode) writemode.Mode {
	if mode, ok := writeModeMap[m]; ok {
		return mode
	}
	return writemode.Sync
}

// consistencyMap provides O(1) mapping from transport-layer ConsistencyLevel
// to the service-layer consistency.Level.
var consistencyMap = map[transport.ConsistencyLevel]consistency.Level{
	transport.ConsistencyStrong:       consistency.Strong,
	transport.ConsistencyLinearizable: consistency.Linearizable,
	transport.ConsistencyEventual:     consistency.Eventual,
	transport.ConsistencyFollower:     consistency.FollowerRead,
}

// mapConsistencyLevel converts a transport-layer ConsistencyLevel to the
// corresponding service-layer consistency.Level. Unrecognized levels
// default to Linearizable.
func mapConsistencyLevel(l transport.ConsistencyLevel) consistency.Level {
	if level, ok := consistencyMap[l]; ok {
		return level
	}
	return consistency.Linearizable
}

// resolveWriteMode resolves the variadic WriteOption slice and returns the
// mapped writemode.Mode and whether a mode was explicitly set.
func resolveWriteMode(opts []transport.WriteOption) (writemode.Mode, bool) {
	wo := transport.ResolveWriteOptions(opts...)
	if !wo.ModeSet {
		return writemode.Sync, false
	}
	return mapWriteMode(wo.Mode), true
}

// parseTSConsistency parses the optional consistency string from an SDK
// time-series request (e.g. TSQueryRequest.Consistency). An empty string
// returns (zero, false, nil), meaning the caller should use the plain
// service method. An unrecognised value returns a non-nil error.
func parseTSConsistency(s string) (consistency.ReadOptions, bool, error) {
	if s == "" {
		return consistency.ReadOptions{}, false, nil
	}
	level, err := consistency.ParseLevel(s)
	if err != nil {
		return consistency.ReadOptions{}, false, err
	}
	return consistency.ReadOptions{
		Consistency: level,
		AllowStale:  level == consistency.Eventual,
	}, true, nil
}

// resolveReadConsistency resolves the variadic ReadOption slice and returns
// the mapped consistency.ReadOptions and whether consistency was explicitly set.
func resolveReadConsistency(opts []transport.ReadOption) (consistency.ReadOptions, bool) {
	ro := transport.ResolveReadOptions(opts...)
	if !ro.ConsistencySet {
		// WithAllowStale() on its own (no explicit level) is an eventual read,
		// matching the network transports which map allow-stale to eventual.
		if ro.AllowStale {
			return consistency.ReadOptions{
				Consistency: consistency.Eventual,
				Timeout:     ro.TimeoutMs,
				AllowStale:  true,
			}, true
		}
		return consistency.ReadOptions{}, false
	}
	// AllowStale is true when the caller explicitly requests eventual
	// consistency or when the WithAllowStale option is set.
	allowStale := ro.AllowStale || ro.Consistency == transport.ConsistencyEventual
	return consistency.ReadOptions{
		Consistency: mapConsistencyLevel(ro.Consistency),
		Timeout:     ro.TimeoutMs,
		AllowStale:  allowStale,
	}, true
}

// KVClient implementation
// ---------------------------------------------------------------------------

// Put stores a key-value pair through the embedded KV service.
// Optional WriteOption controls the durability guarantee.
func (t *Transport) Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error {
	ctx, err := t.admit(ctx, "Put")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "Put"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if mode, ok := resolveWriteMode(opts); ok {
		return translateError(t.kvSvc.PutWithMode(ctx, key, value, mode))
	}
	return translateError(t.kvSvc.Put(ctx, key, value))
}

// Get retrieves a value by key through the embedded KV service.
// Optional ReadOption controls the consistency level.
func (t *Transport) Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error) {
	ctx, err := t.admit(ctx, "Get")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "Get"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if ro, ok := resolveReadConsistency(opts); ok {
		data, err := t.kvSvc.GetWithConsistency(ctx, key, ro)
		if err != nil {
			return nil, translateError(err)
		}
		return data, nil
	}
	data, err := t.kvSvc.Get(ctx, key)
	if err != nil {
		return nil, translateError(err)
	}
	return data, nil
}

// Delete removes a key through the embedded KV service.
// Optional WriteOption controls the durability guarantee.
func (t *Transport) Delete(ctx context.Context, key string, opts ...transport.WriteOption) error {
	ctx, err := t.admit(ctx, "Delete")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "Delete"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if mode, ok := resolveWriteMode(opts); ok {
		return translateError(t.kvSvc.DeleteWithMode(ctx, key, mode))
	}
	return translateError(t.kvSvc.Delete(ctx, key))
}

// PutIfAbsent atomically stores value under key only when key is absent,
// delegating to the KV service's own guarded primitive: the condition is
// evaluated inside the state machine, so replicas cannot diverge. This is the
// GuardedKVClient capability, which the embedded transport can offer because it
// reaches the service directly (go-qrdb#119).
func (t *Transport) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	ctx, err := t.admit(ctx, "PutIfAbsent")
	if err != nil {
		return false, err
	}
	if err := t.authorize(ctx, "PutIfAbsent"); err != nil {
		return false, err
	}
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}
	stored, err := t.kvSvc.PutIfAbsent(ctx, key, value)
	return stored, translateError(err)
}

// CompareAndDelete atomically removes key only when its stored value still
// equals expected, delegating to the KV service's guarded delete. It is the
// release half of PutIfAbsent.
func (t *Transport) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	ctx, err := t.admit(ctx, "CompareAndDelete")
	if err != nil {
		return false, err
	}
	if err := t.authorize(ctx, "CompareAndDelete"); err != nil {
		return false, err
	}
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}
	deleted, err := t.kvSvc.CompareAndDelete(ctx, key, expected)
	return deleted, translateError(err)
}

// CompareAndSwap atomically replaces key's value with next only when its
// stored value still equals expected, delegating to the KV service's guarded
// swap. It is the edit half of the guarded set; an absent key is left absent,
// because a swap is not a create.
func (t *Transport) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	ctx, err := t.admit(ctx, "CompareAndSwap")
	if err != nil {
		return false, err
	}
	if err := t.authorize(ctx, "CompareAndSwap"); err != nil {
		return false, err
	}
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}
	swapped, err := t.kvSvc.CompareAndSwap(ctx, key, expected, next)
	return swapped, translateError(err)
}

// List returns sorted keys matching the given prefix through the
// embedded KV service. Optional ReadOption controls the consistency level.
func (t *Transport) List(ctx context.Context, prefix string, opts ...transport.ReadOption) ([]string, error) {
	ctx, err := t.admit(ctx, "List")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "List"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if ro, ok := resolveReadConsistency(opts); ok {
		keys, err := t.kvSvc.ListWithConsistency(ctx, prefix, ro)
		if err != nil {
			return nil, translateError(err)
		}
		return keys, nil
	}
	keys, err := t.kvSvc.List(ctx, prefix)
	if err != nil {
		return nil, translateError(err)
	}
	return keys, nil
}

// Scan returns all key-value pairs matching the given prefix through
// the embedded KV service. Optional ReadOption controls the consistency level.
func (t *Transport) Scan(ctx context.Context, prefix string, opts ...transport.ReadOption) (map[string][]byte, error) {
	ctx, err := t.admit(ctx, "Scan")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "Scan"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if ro, ok := resolveReadConsistency(opts); ok {
		data, err := t.kvSvc.ScanWithConsistency(ctx, prefix, ro)
		if err != nil {
			return nil, translateError(err)
		}
		return data, nil
	}
	data, err := t.kvSvc.Scan(ctx, prefix)
	if err != nil {
		return nil, translateError(err)
	}
	return data, nil
}

// Exists checks whether a key exists. Performs a check via Get.
// Optional ReadOption controls the consistency level.
func (t *Transport) Exists(ctx context.Context, key string, opts ...transport.ReadOption) (bool, error) {
	ctx, err := t.admit(ctx, "Exists")
	if err != nil {
		return false, err
	}
	if err := t.authorize(ctx, "Exists"); err != nil {
		return false, err
	}
	if t.closed.Load() {
		return false, &transport.ClosedError{}
	}
	if ro, ok := resolveReadConsistency(opts); ok {
		exists, err := t.kvSvc.ExistsWithConsistency(ctx, key, ro)
		if err != nil {
			return false, translateError(err)
		}
		return exists, nil
	}
	_, err = t.Get(ctx, key)
	if err != nil {
		var knf *transport.KeyNotFoundError
		if errors.As(err, &knf) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// AdminClient implementation
// ---------------------------------------------------------------------------

// Health returns the health status through the embedded admin service.
func (t *Transport) Health(ctx context.Context) (*transport.HealthResponse, error) {
	ctx, err := t.admit(ctx, "Health")
	if err != nil {
		return nil, err
	}
	// Health needs no identity on every transport (REST's /health is
	// server.publicIf with no permission at all), but it is not the unseal
	// call, so the gate above refuses it while sealed.
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	status, err := t.adminSvc.Health(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.HealthResponse{
		Status:             status.Status,
		IsLeader:           status.Leader,
		DiagnosticsDropped: status.DiagnosticsDropped,
	}, nil
}

// Status returns detailed node status through the embedded admin service.
func (t *Transport) Status(ctx context.Context) (*transport.StatusResponse, error) {
	ctx, err := t.admit(ctx, "Status")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "Status"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	info, err := t.adminSvc.Status(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.StatusResponse{
		NodeID:      info.NodeID,
		RaftAddress: info.RaftAddress,
		IsLeader:    info.IsLeader,
		LeaderID:    info.LeaderID,
		ShardCount:  info.ShardCount,
		IsDraining:  info.IsDraining,
		Shards:      info.Shards,
	}, nil
}

// Peers returns the peer membership through the embedded admin service.
func (t *Transport) Peers(ctx context.Context) (*transport.PeersResponse, error) {
	ctx, err := t.admit(ctx, "Peers")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "Peers"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	info, err := t.adminSvc.Peers(ctx)
	if err != nil {
		return nil, translateError(err)
	}

	shards := make([]transport.ShardPeers, 0, len(info.Shards))
	for _, sp := range info.Shards {
		peers := make([]transport.PeerInfo, 0, len(sp.Peers))
		for _, p := range sp.Peers {
			peers = append(peers, transport.PeerInfo{
				ReplicaID: p.ReplicaID,
				Address:   p.Address,
				IsLeader:  p.IsLeader,
			})
		}
		shards = append(shards, transport.ShardPeers{
			ShardID:  sp.ShardID,
			LeaderID: sp.LeaderID,
			Peers:    peers,
		})
	}

	return &transport.PeersResponse{Shards: shards}, nil
}

// AddMember adds a new replica through the embedded admin service.
func (t *Transport) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	ctx, err := t.admit(ctx, "AddMember")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "AddMember"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.adminSvc.AddMember(ctx, shardID, replicaID, raftAddr, identity))
}

// RemoveMember removes a replica through the embedded admin service.
func (t *Transport) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	ctx, err := t.admit(ctx, "RemoveMember")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "RemoveMember"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.adminSvc.RemoveMember(ctx, shardID, replicaID))
}

// PromoteMember promotes a committed observer through the embedded admin
// service. Implements transport.MemberPromoter.
func (t *Transport) PromoteMember(ctx context.Context, shardID, replicaID uint64) error {
	ctx, err := t.admit(ctx, "PromoteMember")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "PromoteMember"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	promoter, ok := t.adminSvc.(service.MemberPromoter)
	if !ok {
		return translateError(&service.MemberPromotionUnsupportedError{})
	}
	return translateError(promoter.PromoteMember(ctx, shardID, replicaID))
}

// TransferLeadership hands shardID's leadership to targetReplicaID through
// the embedded admin service and returns once that replica leads. Implements
// transport.LeaderTransferrer.
func (t *Transport) TransferLeadership(ctx context.Context, shardID, targetReplicaID uint64) error {
	ctx, err := t.admit(ctx, "TransferLeadership")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "TransferLeadership"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	transferrer, ok := t.adminSvc.(service.LeaderTransferrer)
	if !ok {
		return translateError(&service.LeaderTransferUnsupportedError{})
	}
	return translateError(transferrer.TransferLeadership(ctx, shardID, targetReplicaID))
}

var _ transport.LeaderTransferrer = (*Transport)(nil)

// MintJoinToken mints a single-use, identity-bound member-side CSR join
// token through the embedded admin service. Implements
// transport.ClusterJoinTokenMinter. Returns
// *service.ClusterJoinTokenNotConfiguredError (translated) when adminSvc
// does not implement service.ClusterJoinTokenMinter at all -- an embedded
// node built without member-side CSR join wired at all, rather than one
// configured with no store, but the two are indistinguishable to a caller
// and the real AdminService answers the same error for both.
func (t *Transport) MintJoinToken(ctx context.Context, req *transport.MintJoinTokenRequest) (*transport.MintJoinTokenResponse, error) {
	ctx, err := t.admit(ctx, "MintJoinToken")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "MintJoinToken"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	minter, ok := t.adminSvc.(service.ClusterJoinTokenMinter)
	if !ok {
		return nil, translateError(&service.ClusterJoinTokenNotConfiguredError{})
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "MintJoinToken", Err: &transport.NilRequestError{}}
	}
	token, expiresAt, err := minter.MintJoinToken(ctx, req.NodeID, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.MintJoinTokenResponse{Token: token, ExpiresAt: expiresAt}, nil
}

var _ transport.ClusterJoinTokenMinter = (*Transport)(nil)

// AddObserverMember adds a new replica through the embedded admin service
// as a non-voting observer rather than a voter. Implements
// transport.ObserverMemberAdder.
func (t *Transport) AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	ctx, err := t.admit(ctx, "AddObserverMember")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "AddObserverMember"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	adder, ok := t.adminSvc.(service.ObserverMemberAdder)
	if !ok {
		return translateError(&service.ClusterJoinRequestReviewUnsupportedError{})
	}
	return translateError(adder.AddObserverMember(ctx, shardID, replicaID, raftAddr, identity))
}

var _ transport.ObserverMemberAdder = (*Transport)(nil)

// ListPendingJoinRequests lists tokenless member-side CSR join requests
// awaiting an operator's decision, through the embedded admin service.
// Implements transport.JoinRequestReviewer.
func (t *Transport) ListPendingJoinRequests(ctx context.Context) ([]*transport.PendingJoinRequest, error) {
	ctx, err := t.admit(ctx, "ListPendingJoinRequests")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListPendingJoinRequests"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	reviewer, ok := t.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		return nil, translateError(&service.ClusterJoinRequestReviewUnsupportedError{})
	}
	pending, err := reviewer.ListPendingJoinRequests(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	out := make([]*transport.PendingJoinRequest, 0, len(pending))
	for _, p := range pending {
		out = append(out, &transport.PendingJoinRequest{
			ID:          p.ID,
			NodeURI:     p.NodeURI,
			ReplicaID:   p.ReplicaID,
			Features:    p.Features,
			SubmittedAt: p.SubmittedAt,
			ExpiresAt:   p.ExpiresAt,
		})
	}
	return out, nil
}

// ApproveJoinRequest binds id's queued CSR to an operator-chosen node
// identity, through the embedded admin service. Implements
// transport.JoinRequestReviewer.
func (t *Transport) ApproveJoinRequest(ctx context.Context, id, nodeID string) error {
	ctx, err := t.admit(ctx, "ApproveJoinRequest")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "ApproveJoinRequest"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	reviewer, ok := t.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		return translateError(&service.ClusterJoinRequestReviewUnsupportedError{})
	}
	return translateError(reviewer.ApproveJoinRequest(ctx, id, nodeID))
}

// RejectJoinRequest records a terminal refusal of id's queued CSR, through
// the embedded admin service. Implements transport.JoinRequestReviewer.
func (t *Transport) RejectJoinRequest(ctx context.Context, id, reason string) error {
	ctx, err := t.admit(ctx, "RejectJoinRequest")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "RejectJoinRequest"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	reviewer, ok := t.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		return translateError(&service.ClusterJoinRequestReviewUnsupportedError{})
	}
	return translateError(reviewer.RejectJoinRequest(ctx, id, reason))
}

var _ transport.JoinRequestReviewer = (*Transport)(nil)

// RetireJoinIdentity permanently refuses any future admission for a node
// identity through the embedded admin service. Unlike MintJoinToken, its
// response carries no secret, so it stays wired through the embedded
// transport like every other ordinary admin verb (see docs/embedding.md).
func (t *Transport) RetireJoinIdentity(ctx context.Context, nodeID, reason string) error {
	ctx, err := t.admit(ctx, "RetireJoinIdentity")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "RetireJoinIdentity"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	retirer, ok := t.adminSvc.(service.JoinIdentityRetirer)
	if !ok {
		return translateError(&service.ClusterJoinTokenNotConfiguredError{})
	}
	return translateError(retirer.RetireJoinIdentity(ctx, nodeID, reason))
}

// RequestSnapshot triggers a snapshot through the embedded admin service.
func (t *Transport) RequestSnapshot(ctx context.Context, shardID uint64) error {
	ctx, err := t.admit(ctx, "RequestSnapshot")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "RequestSnapshot"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.adminSvc.RequestSnapshot(ctx, shardID))
}

// Drain transitions the host into drain mode through the embedded
// admin service.
func (t *Transport) Drain(ctx context.Context) error {
	ctx, err := t.admit(ctx, "Drain")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "Drain"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return translateError(t.adminSvc.Drain(ctx))
}

// Capabilities returns the compile-time feature set through the embedded
// admin service.
func (t *Transport) Capabilities(ctx context.Context) (*transport.CapabilitiesResponse, error) {
	ctx, err := t.admit(ctx, "Capabilities")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "Capabilities"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	caps, err := t.adminSvc.Capabilities(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.CapabilitiesResponse{
		Mode:        caps.Mode,
		KV:          caps.KV,
		MVCC:        caps.MVCC,
		Transaction: caps.Transaction,
		Batch:       caps.Batch,
		Watch:       caps.Watch,
		Lease:       caps.Lease,
		Lock:        caps.Lock,
		Index:       caps.Index,
		Cluster:     caps.Cluster,
		Backup:      caps.Backup,
		Rebalance:   caps.Rebalance,
		TimeSeries:  caps.TimeSeries,
		Vector:      caps.Vector,
		SQL:         caps.SQL,
		MySQL:       caps.MySQL,
		PostgreSQL:  caps.PostgreSQL,
		Etcd:        caps.Etcd,
		FTS:         caps.FTS,

		UnencryptedAtRest: caps.UnencryptedAtRest,
	}, nil
}

// StartShard initializes and starts a Raft group for the given shard
// through the embedded admin service.
func (t *Transport) StartShard(ctx context.Context, req *transport.StartShardRequest) error {
	ctx, err := t.admit(ctx, "StartShard")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "StartShard"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	svcReq := &service.StartShardRequest{
		ShardID:   req.ShardID,
		ReplicaID: req.ReplicaID,
		Members:   req.Members,
		Join:      req.Join,
	}
	if err := t.adminSvc.StartShard(ctx, svcReq); err != nil {
		return &transport.ShardStartError{ShardID: req.ShardID, Err: err}
	}
	return nil
}

// StopShard stops the Raft group for the given shard through the
// embedded admin service.
func (t *Transport) StopShard(ctx context.Context, shardID uint64) error {
	ctx, err := t.admit(ctx, "StopShard")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "StopShard"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if err := t.adminSvc.StopShard(ctx, shardID); err != nil {
		return &transport.ShardStopError{ShardID: shardID, Err: err}
	}
	return nil
}

// GetShardState returns the Raft state for a shard through the
// embedded admin service.
func (t *Transport) GetShardState(ctx context.Context, shardID uint64) (*transport.ShardStateResponse, error) {
	ctx, err := t.admit(ctx, "GetShardState")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetShardState"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.adminSvc.GetShardState(ctx, shardID)
	if err != nil {
		return nil, &transport.ShardStateError{ShardID: shardID, Err: err}
	}
	return &transport.ShardStateResponse{
		ShardID:           resp.ShardID,
		ConfigChangeIndex: resp.ConfigChangeIndex,
		Term:              resp.Term,
		LeaderID:          resp.LeaderID,
		HasLeader:         resp.HasLeader,
	}, nil
}

// GetTopology returns the full cluster topology through the embedded
// admin service.
func (t *Transport) GetTopology(ctx context.Context) (*transport.TopologyResponse, error) {
	ctx, err := t.admit(ctx, "GetTopology")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetTopology"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.adminSvc.GetTopology(ctx)
	if err != nil {
		return nil, &transport.TopologyError{Err: err}
	}
	shards := make([]transport.ShardInfo, len(resp.Shards))
	for i, s := range resp.Shards {
		shards[i] = transport.ShardInfo{
			ShardID:   s.ShardID,
			ReplicaID: s.ReplicaID,
			IsLeader:  s.IsLeader,
			LeaderID:  s.LeaderID,
			Nodes:     s.Nodes,
		}
	}
	return &transport.TopologyResponse{
		NodeID:      resp.NodeID,
		RaftAddress: resp.RaftAddress,
		ShardCount:  resp.ShardCount,
		Shards:      shards,
	}, nil
}

// ReplicaStates implements transport.AdminClient.
func (t *Transport) ReplicaStates(ctx context.Context, shardID uint64) (*transport.ReplicaStatesResponse, error) {
	ctx, err := t.admit(ctx, "ReplicaStates")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ReplicaStates"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	reader, ok := t.adminSvc.(service.ReplicaStateReader)
	if !ok {
		return nil, translateError(&service.ReplicaStateReadUnsupportedError{})
	}
	resp, err := reader.ReplicaStates(ctx, shardID)
	if err != nil {
		return nil, translateError(err)
	}
	replicas := make([]transport.ReplicaState, len(resp.Replicas))
	for i, rs := range resp.Replicas {
		replicas[i] = transport.ReplicaState{
			ShardID:      rs.ShardID,
			ReplicaID:    rs.ReplicaID,
			AppliedIndex: rs.AppliedIndex,
			Hash:         rs.Hash,
		}
	}
	return &transport.ReplicaStatesResponse{NodeID: resp.NodeID, Replicas: replicas}, nil
}

// ---------------------------------------------------------------------------
// MVCCClient implementation
// ---------------------------------------------------------------------------

// PutWithLease stores a key-value pair with an attached lease through
// the embedded MVCC service. Optional WriteOption controls the durability
// guarantee.
func (t *Transport) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...transport.WriteOption) (*transport.MVCCKeyValue, error) {
	ctx, err := t.admit(ctx, "PutWithLease")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "PutWithLease"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.mvccSvc == nil {
		return nil, &NilServiceError{Service: "MVCCKVServicer"}
	}
	if mode, ok := resolveWriteMode(opts); ok {
		kv, err := t.mvccSvc.PutWithLeaseAndMode(ctx, key, value, leaseID, mode)
		if err != nil {
			return nil, translateError(err)
		}
		return toTransportKV(kv), nil
	}
	kv, err := t.mvccSvc.PutWithLease(ctx, key, value, leaseID)
	if err != nil {
		return nil, translateError(err)
	}
	return toTransportKV(kv), nil
}

// GetRevision retrieves a key-value pair with full MVCC revision metadata
// through the embedded MVCC service. Optional ReadOption controls the
// consistency level.
func (t *Transport) GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	ctx, err := t.admit(ctx, "GetRevision")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetRevision"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.mvccSvc == nil {
		return nil, &NilServiceError{Service: "MVCCKVServicer"}
	}
	if ro, ok := resolveReadConsistency(opts); ok {
		kv, err := t.mvccSvc.GetRevisionWithConsistency(ctx, key, ro)
		if err != nil {
			return nil, translateError(err)
		}
		return toTransportKV(kv), nil
	}
	kv, err := t.mvccSvc.GetRevision(ctx, key)
	if err != nil {
		return nil, translateError(err)
	}
	return toTransportKV(kv), nil
}

// GetAtRevision retrieves a key-value pair at a historical MVCC revision
// through the embedded MVCC service. Optional ReadOption controls the
// consistency level.
func (t *Transport) GetAtRevision(ctx context.Context, key string, revision int64, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	ctx, err := t.admit(ctx, "GetAtRevision")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetAtRevision"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.mvccSvc == nil {
		return nil, &NilServiceError{Service: "MVCCKVServicer"}
	}
	if ro, ok := resolveReadConsistency(opts); ok {
		kv, err := t.mvccSvc.GetAtRevisionWithConsistency(ctx, key, revision, ro)
		if err != nil {
			return nil, translateError(err)
		}
		return toTransportKV(kv), nil
	}
	kv, err := t.mvccSvc.GetAtRevision(ctx, key, revision)
	if err != nil {
		return nil, translateError(err)
	}
	return toTransportKV(kv), nil
}

// Range returns key-value pairs in the half-open interval [start, end)
// through the embedded MVCC service. Optional ReadOption controls the
// consistency level.
func (t *Transport) Range(ctx context.Context, start, end []byte, rangeOpts transport.RangeOptions, opts ...transport.ReadOption) (*transport.RangeResponse, error) {
	ctx, err := t.admit(ctx, "Range")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "Range"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.mvccSvc == nil {
		return nil, &NilServiceError{Service: "MVCCKVServicer"}
	}
	if ro, ok := resolveReadConsistency(opts); ok {
		kvs, count, revision, err := t.mvccSvc.RangeWithConsistency(ctx, start, end, ro, toServiceRangeOpts(rangeOpts)...)
		if err != nil {
			return nil, translateError(err)
		}
		return &transport.RangeResponse{
			Kvs:      toTransportKVSlice(kvs),
			Count:    count,
			Revision: revision,
		}, nil
	}
	kvs, count, revision, err := t.mvccSvc.Range(ctx, start, end, toServiceRangeOpts(rangeOpts)...)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.RangeResponse{
		Kvs:      toTransportKVSlice(kvs),
		Count:    count,
		Revision: revision,
	}, nil
}

// Compact discards all MVCC revisions older than the specified revision
// through the embedded MVCC service.
func (t *Transport) Compact(ctx context.Context, revision int64) error {
	ctx, err := t.admit(ctx, "Compact")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "Compact"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.mvccSvc == nil {
		return &NilServiceError{Service: "MVCCKVServicer"}
	}
	return translateError(t.mvccSvc.Compact(ctx, revision))
}

// CurrentRevision returns the latest committed MVCC revision through
// the embedded MVCC service.
func (t *Transport) CurrentRevision(ctx context.Context) (int64, error) {
	ctx, err := t.admit(ctx, "CurrentRevision")
	if err != nil {
		return 0, err
	}
	if err := t.authorize(ctx, "CurrentRevision"); err != nil {
		return 0, err
	}
	if t.closed.Load() {
		return 0, &transport.ClosedError{}
	}
	if t.mvccSvc == nil {
		return 0, &NilServiceError{Service: "MVCCKVServicer"}
	}
	rev, err := t.mvccSvc.CurrentRevision(ctx)
	if err != nil {
		return 0, translateError(err)
	}
	return rev, nil
}

// RestoreToRevision returns a bounded set of keys to the value they held at
// an earlier MVCC revision through the embedded MVCC service -- a logical
// undo, never a Raft rewind. Exactly one of req.Prefix and req.Keys must be
// set.
func (t *Transport) RestoreToRevision(ctx context.Context, req *transport.RestoreRevisionRequest) (*transport.RestoreRevisionResult, error) {
	ctx, err := t.admit(ctx, "RestoreToRevision")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "RestoreToRevision"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.mvccSvc == nil {
		return nil, &NilServiceError{Service: "MVCCKVServicer"}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "RestoreToRevision", Err: &transport.NilRequestError{}}
	}

	result, err := t.mvccSvc.RestoreToRevision(ctx, toServiceRestoreRevisionRequest(req))
	if err != nil {
		return nil, translateError(err)
	}
	return toTransportRestoreRevisionResult(result), nil
}

// ---------------------------------------------------------------------------
// TransactionClient implementation
// ---------------------------------------------------------------------------

// ExecuteTransaction atomically evaluates compares and executes the
// corresponding operation set through the embedded transaction service.
// Optional WriteOption controls the durability guarantee.
func (t *Transport) ExecuteTransaction(ctx context.Context, req *transport.TransactionRequest, opts ...transport.WriteOption) (*transport.TransactionResponse, error) {
	ctx, err := t.admit(ctx, "ExecuteTransaction")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ExecuteTransaction"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.txSvc == nil {
		return nil, &NilServiceError{Service: "TransactionServicer"}
	}

	if mode, ok := resolveWriteMode(opts); ok {
		resp, err := t.txSvc.ExecuteWithMode(ctx,
			toServiceCompares(req.Compares),
			toServiceOps(req.SuccessOps),
			toServiceOps(req.FailureOps),
			mode,
		)
		if err != nil {
			return nil, translateError(err)
		}
		return &transport.TransactionResponse{
			Succeeded: resp.Succeeded,
			Revision:  resp.Revision,
			Responses: toTransportOpResponses(resp.Responses),
		}, nil
	}

	resp, err := t.txSvc.Execute(ctx,
		toServiceCompares(req.Compares),
		toServiceOps(req.SuccessOps),
		toServiceOps(req.FailureOps),
	)
	if err != nil {
		return nil, translateError(err)
	}

	// Revision is copied on BOTH paths. It was dropped on both, so every
	// transaction through this transport reported revision 0 — invisible until
	// a caller depended on it (go-qrdb#44).
	return &transport.TransactionResponse{
		Succeeded: resp.Succeeded,
		Revision:  resp.Revision,
		Responses: toTransportOpResponses(resp.Responses),
	}, nil
}

// ---------------------------------------------------------------------------
// BatchClient implementation
// ---------------------------------------------------------------------------

// ExecuteBatch applies the given operations atomically through the
// embedded batch service. Optional WriteOption controls the durability
// guarantee.
func (t *Transport) ExecuteBatch(ctx context.Context, ops []transport.Op, opts ...transport.WriteOption) (*transport.BatchResponse, error) {
	ctx, err := t.admit(ctx, "ExecuteBatch")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ExecuteBatch"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.batchSvc == nil {
		return nil, &NilServiceError{Service: "BatchServicer"}
	}

	if mode, ok := resolveWriteMode(opts); ok {
		resps, err := t.batchSvc.ExecuteWithMode(ctx, toServiceOps(ops), mode)
		if err != nil {
			return nil, translateError(err)
		}
		return &transport.BatchResponse{
			Responses: toTransportOpResponses(resps.Responses),
			Revision:  resps.Revision,
		}, nil
	}

	resps, err := t.batchSvc.Execute(ctx, toServiceOps(ops))
	if err != nil {
		return nil, translateError(err)
	}

	return &transport.BatchResponse{
		Responses: toTransportOpResponses(resps.Responses),
		Revision:  resps.Revision,
	}, nil
}

// ImportFile parses and imports key-value pairs from the given reader by
// driving a chunked import session (ImportBegin/ImportAppend/ImportCommit
// below) -- see transport.ImportFileChunked. The filename parameter is
// used for format auto-detection when opts.Format is "auto" or empty.
func (t *Transport) ImportFile(ctx context.Context, filename string, data io.Reader, opts transport.ImportOptions) (*transport.ImportResult, error) {
	ctx, err := t.admit(ctx, "ImportFile")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ImportFile"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.batchSvc == nil {
		return nil, &NilServiceError{Service: "BatchServicer"}
	}
	return transport.ImportFileChunked(ctx, t, filename, data, opts, 0)
}

// ImportBegin starts a chunked import session directly against the
// embedded BatchServicer -- no wire framing, no network boundary.
func (t *Transport) ImportBegin(ctx context.Context, opts transport.ImportOptions) (string, error) {
	ctx, err := t.admit(ctx, "ImportBegin")
	if err != nil {
		return "", err
	}
	if err := t.authorize(ctx, "ImportBegin"); err != nil {
		return "", err
	}
	if t.closed.Load() {
		return "", &transport.ClosedError{}
	}
	if t.batchSvc == nil {
		return "", &NilServiceError{Service: "BatchServicer"}
	}

	sessionID, err := t.batchSvc.ImportBegin(ctx, service.ImportOptions{
		Format:        opts.Format,
		Filename:      opts.Filename,
		KeyField:      opts.KeyField,
		ValueMode:     opts.ValueMode,
		ValueField:    opts.ValueField,
		SkipHeader:    opts.SkipHeader,
		Transactional: opts.Transactional,
	})
	if err != nil {
		return "", translateError(err)
	}
	return sessionID, nil
}

// ImportAppend stages a chunk of a chunked import session directly
// against the embedded BatchServicer.
func (t *Transport) ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error {
	ctx, err := t.admit(ctx, "ImportAppend")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "ImportAppend"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.batchSvc == nil {
		return &NilServiceError{Service: "BatchServicer"}
	}

	if err := t.batchSvc.ImportAppend(ctx, sessionID, seq, chunk); err != nil {
		return translateError(err)
	}
	return nil
}

// ImportCommit applies every staged chunk of a chunked import session
// atomically, directly against the embedded BatchServicer.
func (t *Transport) ImportCommit(ctx context.Context, sessionID string) (*transport.ImportResult, error) {
	ctx, err := t.admit(ctx, "ImportCommit")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ImportCommit"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.batchSvc == nil {
		return nil, &NilServiceError{Service: "BatchServicer"}
	}

	result, err := t.batchSvc.ImportCommit(ctx, sessionID)
	if err != nil {
		return nil, translateError(err)
	}
	if result == nil {
		return &transport.ImportResult{}, nil
	}

	errs := make([]transport.ImportError, len(result.Errors))
	for i, e := range result.Errors {
		errs[i] = transport.ImportError{Row: e.Row, Key: e.Key, Message: e.Message}
	}
	return &transport.ImportResult{
		Total:     result.Total,
		Succeeded: result.Succeeded,
		Failed:    result.Failed,
		Errors:    errs,
	}, nil
}

// ImportAbort discards a chunked import session without applying
// anything, directly against the embedded BatchServicer.
func (t *Transport) ImportAbort(ctx context.Context, sessionID string) error {
	ctx, err := t.admit(ctx, "ImportAbort")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "ImportAbort"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.batchSvc == nil {
		return &NilServiceError{Service: "BatchServicer"}
	}

	if err := t.batchSvc.ImportAbort(ctx, sessionID); err != nil {
		return translateError(err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// WatchClient implementation
// ---------------------------------------------------------------------------

// streamRefusal is a stream open's refusal once it has subscribed: the node
// gate (the probe), then the tenant's and the database's refusal through the
// registry, when it reports one (service.StreamRefuser) -- the same refusals
// a network listener's stream asks.
func (t *Transport) streamRefusal(method, tenantID, db string) error {
	if err := t.gate(method); err != nil {
		return err
	}
	if refuser, ok := t.registry.(service.StreamRefuser); ok {
		return refuser.StreamRefusal(tenantID, db)
	}
	return nil
}

// watchOpener opens a watcher over the embedded watch service.
// service.WatchServicer.Watch and service.WatchServicer.WatchPrefix both have
// this signature, so Watch and WatchPrefix differ only in the method value
// they hand to watchStream.
type watchOpener func(context.Context, string, ...watch.WatchOption) (watch.Watcher, error)

// Watch creates a watcher that delivers events for the specified key
// through the embedded watch service.
func (t *Transport) Watch(ctx context.Context, key string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	ctx, err := t.admit(ctx, "Watch")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "Watch"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.watchSvc == nil {
		return nil, &NilServiceError{Service: "WatchServicer"}
	}

	return t.watchStream(ctx, "Watch", t.watchSvc.Watch, key, opts)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix through the embedded watch service.
func (t *Transport) WatchPrefix(ctx context.Context, prefix string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	ctx, err := t.admit(ctx, "WatchPrefix")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "WatchPrefix"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.watchSvc == nil {
		return nil, &NilServiceError{Service: "WatchServicer"}
	}

	return t.watchStream(ctx, "WatchPrefix", t.watchSvc.WatchPrefix, prefix, opts)
}

// watchStream opens a watcher for target through open and spawns the goroutine
// that forwards its responses to the returned channel.
//
// A refused start revision fails HERE, before any watcher exists, so there is
// no stream for a terminal frame to travel on and no WatchResponse.Err() to
// classify it — the open error is all the caller ever sees. It therefore runs
// through transport.ClassifyWatchError, exactly as the open error does in
// sdk/go/transport/{grpc,unix,rest}/transport_watch.go, so a compacted or
// unsupported start revision arrives as *transport.WatchCompactedError or
// *transport.WatchStartRevisionUnsupportedError on this transport too.
// Translating alone left it a generic RequestError, and the remedy the watch
// error table documents — errors.As for the unsupported case, drop
// StartRevision, retry live — matched on every other transport and silently
// did not on this one (go-qrdb#495).
//
// Classification runs AFTER translateError, not instead of it: it inspects the
// message and returns its argument unchanged when nothing matches, so every
// non-watch refusal keeps the transport type translateError maps it to.
//
// With a seal stream registry (WithSealStreams) the watch subscribes FIRST
// and the gate is asked again before it opens (clusterseal.Streams.GuardWatcher):
// a seal closes the gate before it ends streams, so one that lands after the
// gate admitted this request either refuses it here or ends it.
func (t *Transport) watchStream(ctx context.Context, method string, open watchOpener, target string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	openWatcher := func() (watch.Watcher, error) {
		return open(ctx, target, toServiceWatchOpts(opts)...)
	}
	var (
		watcher watch.Watcher
		err     error
	)
	if t.sealStreams != nil {
		tenantID := tenantid.Normalize(tenant.FromContext(ctx))
		watcher, err = t.sealStreams.GuardWatcher(tenantID, tenant.DefaultDatabase,
			func() error { return t.streamRefusal(method, tenantID, tenant.DefaultDatabase) }, openWatcher)
	} else {
		watcher, err = openWatcher()
	}
	if err != nil {
		var sealed *BarrierSealedError
		if errors.As(err, &sealed) {
			return nil, err
		}
		return nil, transport.ClassifyWatchError(translateError(err))
	}

	ch := make(chan transport.WatchResponse)
	go forwardWatchResponses(ctx, watcher, ch)

	return ch, nil
}

// forwardWatchResponses relays responses from watcher to ch until the watcher's
// event channel closes or ctx is done. The channel is always closed on exit,
// and the watcher is always closed with it: this goroutine owns the watcher for
// the rest of its life, exactly as the wire transports' server-side handlers do
// (pkg/api/grpc/service.go, pkg/api/quic/handlers_watch.go).
//
// Every channel operation selects on ctx.Done(). A consumer abandons this
// stream by cancelling ctx — sdk/go's Coordinator.Watch drain returns on
// ctx.Done() and never reads the raw channel again — so a bare send would park
// this goroutine for the life of the process the moment a cancel raced a
// delivery, one leaked goroutine per cancelled watch and no error anywhere
// (go-qrdb#406). A buffer would not fix that; it would only move the block to
// the send after the buffer fills. The select is what releases it.
//
// The embedded transport is its own server, so it carries the server-side half
// of the watch contract too: when the hub evicts a watcher it records the
// reason on the watcher and closes the event channel WITHOUT emitting a
// terminal response, and it is the handler that turns that into one. Every
// wire transport already does — the identical `if wErr := w.Err()` on the
// channel-closed path in pkg/api/rest/handlers_watch.go, pkg/api/grpc/service.go,
// pkg/api/quic/handlers_watch.go and pkg/api/mcp/tools_watch.go — and embedded
// skipping it made an overflow eviction, or a compaction racing a replay,
// indistinguishable from a clean close on the transport where the failure is
// quietest (go-qrdb#405).
func forwardWatchResponses(ctx context.Context, watcher watch.Watcher, ch chan<- transport.WatchResponse) {
	defer close(ch)
	defer func() { _ = watcher.Close() }()

	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-watcher.Events():
			if !ok {
				sendTerminalWatchResponse(ctx, watcher, ch)
				return
			}
			out := toTransportWatchResponse(resp)
			select {
			case ch <- out:
			case <-ctx.Done():
				return
			}
			// A terminal response ends the stream. The wire clients all stop
			// reading after one rather than waiting for a close that can say
			// nothing more.
			if out.Canceled {
				return
			}
		}
	}
}

// sendTerminalWatchResponse synthesizes the terminal frame for a watcher whose
// event channel has closed, and sends nothing when the watch ended cleanly —
// the distinction a caller reads through transport.WatchResponse.Err().
//
// The reason is the watcher error's own message, verbatim, because that is what
// the wire transports carry: pkg/api/rest/handlers_watch.go writes wErr.Error()
// straight into CancelReason, and pkg/api/grpc and pkg/api/quic pass it through
// mapServiceError, whose message is err.Error(), for their clients to read back
// off the status. Keeping the text identical is what makes
// transport.WatchResponse.Err() classify an embedded cancellation into the same
// typed error — WatchCompactedError, WatchStartRevisionUnsupportedError, or a
// generic WatchCanceledError — as every other transport does for the same
// hub-side condition.
//
// WatchID is populated because it is available here, as it is in the REST
// handler that synthesizes the same frame. The gRPC and QUIC clients leave it
// zero only because a transport status carries no watch id to recover it from.
func sendTerminalWatchResponse(ctx context.Context, watcher watch.Watcher, ch chan<- transport.WatchResponse) {
	wErr := watcher.Err()
	if wErr == nil {
		return
	}
	select {
	case ch <- transport.WatchResponse{
		WatchID:      watcher.WatchID(),
		Canceled:     true,
		CancelReason: wErr.Error(),
	}:
	case <-ctx.Done():
	}
}

// ---------------------------------------------------------------------------
// LeaseClient implementation
// ---------------------------------------------------------------------------

// LeaseGrant creates a new lease with the specified TTL through the
// embedded lease service.
func (t *Transport) LeaseGrant(ctx context.Context, ttl int64) (*transport.LeaseInfo, error) {
	ctx, err := t.admit(ctx, "LeaseGrant")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LeaseGrant"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.leaseSvc == nil {
		return nil, &NilServiceError{Service: "LeaseServicer"}
	}
	l, err := t.leaseSvc.Grant(ctx, ttl)
	if err != nil {
		return nil, translateError(err)
	}
	return toTransportLease(l), nil
}

// LeaseRevoke immediately expires a lease and deletes attached keys
// through the embedded lease service.
func (t *Transport) LeaseRevoke(ctx context.Context, leaseID int64) error {
	ctx, err := t.admit(ctx, "LeaseRevoke")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "LeaseRevoke"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.leaseSvc == nil {
		return &NilServiceError{Service: "LeaseServicer"}
	}
	return translateError(t.leaseSvc.Revoke(ctx, leaseID))
}

// LeaseKeepAlive resets the TTL on an existing lease through the
// embedded lease service.
func (t *Transport) LeaseKeepAlive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	ctx, err := t.admit(ctx, "LeaseKeepAlive")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LeaseKeepAlive"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.leaseSvc == nil {
		return nil, &NilServiceError{Service: "LeaseServicer"}
	}
	l, err := t.leaseSvc.KeepAlive(ctx, leaseID)
	if err != nil {
		return nil, translateError(err)
	}
	return toTransportLease(l), nil
}

// LeaseTimeToLive returns the current state of a lease through the
// embedded lease service.
func (t *Transport) LeaseTimeToLive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	ctx, err := t.admit(ctx, "LeaseTimeToLive")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LeaseTimeToLive"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.leaseSvc == nil {
		return nil, &NilServiceError{Service: "LeaseServicer"}
	}
	l, err := t.leaseSvc.TimeToLive(ctx, leaseID)
	if err != nil {
		return nil, translateError(err)
	}
	return toTransportLease(l), nil
}

// LeaseList returns all active leases through the embedded lease
// service.
func (t *Transport) LeaseList(ctx context.Context) ([]*transport.LeaseInfo, error) {
	ctx, err := t.admit(ctx, "LeaseList")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "LeaseList"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.leaseSvc == nil {
		return nil, &NilServiceError{Service: "LeaseServicer"}
	}
	leases, err := t.leaseSvc.Leases(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return toTransportLeaseSlice(leases), nil
}

// ---------------------------------------------------------------------------
// TimeSeriesClient implementation
// ---------------------------------------------------------------------------

// TSWrite stores a single time-series data point through the embedded
// time-series service.
func (t *Transport) TSWrite(ctx context.Context, point *transport.TSDataPoint) error {
	ctx, err := t.admit(ctx, "TSWrite")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "TSWrite"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return &NilServiceError{Service: "TimeSeriesServicer"}
	}
	tags := tagsToMap(point.Tags)
	ts := time.Unix(0, point.TimestampNs)
	return translateError(t.tsSvc.Write(ctx, point.Metric, point.Value, ts, tags))
}

// TSWriteBatch stores multiple time-series data points atomically
// through the embedded time-series service.
func (t *Transport) TSWriteBatch(ctx context.Context, points []transport.TSDataPoint) error {
	ctx, err := t.admit(ctx, "TSWriteBatch")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "TSWriteBatch"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return &NilServiceError{Service: "TimeSeriesServicer"}
	}
	servicePoints := make([]service.DataPoint, len(points))
	for i, p := range points {
		servicePoints[i] = service.DataPoint{
			Metric:    p.Metric,
			Value:     p.Value,
			Timestamp: time.Unix(0, p.TimestampNs),
			Tags:      tagsToMap(p.Tags),
		}
	}
	return translateError(t.tsSvc.WriteBatch(ctx, servicePoints))
}

// TSQuery retrieves data points for a metric within a time range
// through the embedded time-series service. When req.Consistency is
// set, it routes to QueryWithConsistency.
func (t *Transport) TSQuery(ctx context.Context, req *transport.TSQueryRequest) (*transport.TSQueryResponse, error) {
	ctx, err := t.admit(ctx, "TSQuery")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "TSQuery"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return nil, &NilServiceError{Service: "TimeSeriesServicer"}
	}

	start := time.Unix(0, req.StartNs)
	end := time.Unix(0, req.EndNs)

	var opts []service.TSQueryOption
	if len(req.Tags) > 0 {
		opts = append(opts, service.WithTags(tagsToMap(req.Tags)))
	}
	if req.Limit > 0 {
		opts = append(opts, service.WithTSLimit(int(req.Limit)))
	}
	if req.Descending {
		opts = append(opts, service.WithDescending())
	}

	ropts, roptsSet, roptsErr := parseTSConsistency(req.Consistency)
	if roptsErr != nil {
		return nil, translateError(roptsErr)
	}

	var points []service.DataPoint
	if roptsSet {
		points, err = t.tsSvc.QueryWithConsistency(ctx, req.Metric, start, end, ropts, opts...)
	} else {
		points, err = t.tsSvc.Query(ctx, req.Metric, start, end, opts...)
	}
	if err != nil {
		return nil, translateError(err)
	}

	return &transport.TSQueryResponse{
		Points: toTransportDataPoints(points),
	}, nil
}

// TSAggregate computes an aggregation over data points in a time range
// through the embedded time-series service. When req.Consistency is
// set, it routes to AggregateWithConsistency.
func (t *Transport) TSAggregate(ctx context.Context, req *transport.TSAggregateRequest) (*transport.TSAggregateResponse, error) {
	ctx, err := t.admit(ctx, "TSAggregate")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "TSAggregate"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return nil, &NilServiceError{Service: "TimeSeriesServicer"}
	}

	start := time.Unix(0, req.StartNs)
	end := time.Unix(0, req.EndNs)
	fn := service.AggFunc(req.Function)
	interval := time.Duration(req.IntervalNs)

	ropts, roptsSet, roptsErr := parseTSConsistency(req.Consistency)
	if roptsErr != nil {
		return nil, translateError(roptsErr)
	}

	var results []service.AggResult
	if roptsSet {
		results, err = t.tsSvc.AggregateWithConsistency(ctx, req.Metric, start, end, fn, interval, ropts)
	} else {
		results, err = t.tsSvc.Aggregate(ctx, req.Metric, start, end, fn, interval)
	}
	if err != nil {
		return nil, translateError(err)
	}

	return &transport.TSAggregateResponse{
		Results: toTransportAggResults(results),
	}, nil
}

// TSWritePoint stores a single validated data point through the
// embedded time-series service.
func (t *Transport) TSWritePoint(ctx context.Context, point *transport.TSDataPoint) error {
	ctx, err := t.admit(ctx, "TSWritePoint")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "TSWritePoint"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return &NilServiceError{Service: "TimeSeriesServicer"}
	}
	dp := &timeseries.DataPoint{
		Metric:    point.Metric,
		Value:     point.Value,
		Timestamp: time.Unix(0, point.TimestampNs),
		Tags:      tagsToMap(point.Tags),
	}
	return translateError(t.tsSvc.WritePoint(ctx, dp))
}

// TSRichQuery performs a rich query with aggregation, downsampling, and
// grouping through the embedded time-series service. When req.Consistency
// is set, it routes to QueryRichWithConsistency.
func (t *Transport) TSRichQuery(ctx context.Context, req *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	ctx, err := t.admit(ctx, "TSRichQuery")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "TSRichQuery"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return nil, &NilServiceError{Service: "TimeSeriesServicer"}
	}

	svcReq := &timeseries.QueryRequest{
		Metric:      req.Metric,
		Tags:        tagsToMap(req.Tags),
		StartTime:   time.Unix(0, req.StartNs),
		EndTime:     time.Unix(0, req.EndNs),
		Aggregation: timeseries.AggFunc(req.Aggregation),
		Interval:    time.Duration(req.IntervalNs),
		GroupBy:     req.GroupBy,
		Limit:       req.Limit,
		Percentile:  req.Percentile,
	}

	ropts, roptsSet, roptsErr := parseTSConsistency(req.Consistency)
	if roptsErr != nil {
		return nil, translateError(roptsErr)
	}

	var result *timeseries.QueryResult
	if roptsSet {
		result, err = t.tsSvc.QueryRichWithConsistency(ctx, svcReq, ropts)
	} else {
		result, err = t.tsSvc.QueryRich(ctx, svcReq)
	}
	if err != nil {
		return nil, translateError(err)
	}

	return toTransportRichQueryResponse(result), nil
}

// TSDeleteSeries deletes time-series data matching the criteria through
// the embedded time-series service.
func (t *Transport) TSDeleteSeries(ctx context.Context, req *transport.TSDeleteRequest) (*transport.TSDeleteResponse, error) {
	ctx, err := t.admit(ctx, "TSDeleteSeries")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "TSDeleteSeries"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return nil, &NilServiceError{Service: "TimeSeriesServicer"}
	}

	svcReq := &timeseries.DeleteRequest{
		Metric: req.Metric,
		Tags:   tagsToMap(req.Tags),
	}
	if req.StartNs != 0 {
		svcReq.StartTime = time.Unix(0, req.StartNs)
	}
	if req.EndNs != 0 {
		svcReq.EndTime = time.Unix(0, req.EndNs)
	}

	err = t.tsSvc.DeleteSeries(ctx, svcReq)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.TSDeleteResponse{}, nil
}

// TSGetRetention retrieves the retention policy for a metric through
// the embedded time-series service.
func (t *Transport) TSGetRetention(ctx context.Context, req *transport.TSGetRetentionRequest) (*transport.TSGetRetentionResponse, error) {
	ctx, err := t.admit(ctx, "TSGetRetention")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "TSGetRetention"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return nil, &NilServiceError{Service: "TimeSeriesServicer"}
	}

	svc, err := t.timeSeriesFor(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	policy, err := svc.GetRetentionPolicy(req.Metric)
	if err != nil {
		return nil, translateError(err)
	}

	return &transport.TSGetRetentionResponse{
		Policy: toTransportRetentionPolicy(policy),
	}, nil
}

// TSSetRetention sets the retention policy for a metric through
// the embedded time-series service.
func (t *Transport) TSSetRetention(ctx context.Context, req *transport.TSSetRetentionRequest) error {
	ctx, err := t.admit(ctx, "TSSetRetention")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "TSSetRetention"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return &NilServiceError{Service: "TimeSeriesServicer"}
	}

	svc, err := t.timeSeriesFor(ctx)
	if err != nil {
		return translateError(err)
	}
	policy := toServiceRetentionPolicy(req.Policy)
	return translateError(svc.SetRetentionPolicy(ctx, req.Metric, policy))
}

// TSListMetrics lists metric names matching a prefix through the
// embedded time-series service. When req.Consistency is set, it routes
// to ListMetricsWithConsistency.
func (t *Transport) TSListMetrics(ctx context.Context, req *transport.TSListMetricsRequest) (*transport.TSListMetricsResponse, error) {
	ctx, err := t.admit(ctx, "TSListMetrics")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "TSListMetrics"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tsSvc == nil {
		return nil, &NilServiceError{Service: "TimeSeriesServicer"}
	}

	ropts, roptsSet, roptsErr := parseTSConsistency(req.Consistency)
	if roptsErr != nil {
		return nil, translateError(roptsErr)
	}

	var metrics []string
	if roptsSet {
		metrics, err = t.tsSvc.ListMetricsWithConsistency(ctx, req.Prefix, int(req.Limit), ropts)
	} else {
		var svc service.TimeSeriesServicer
		if svc, err = t.timeSeriesFor(ctx); err == nil {
			metrics, err = svc.ListMetrics(req.Prefix, int(req.Limit))
		}
	}
	if err != nil {
		return nil, translateError(err)
	}

	return &transport.TSListMetricsResponse{
		Metrics: metrics,
	}, nil
}

// ---------------------------------------------------------------------------
// VectorClient implementation
// ---------------------------------------------------------------------------

// CreateNamespace creates a new vector namespace through the embedded
// vector service.
func (t *Transport) CreateNamespace(ctx context.Context, req *transport.CreateNamespaceRequest) (*transport.CreateNamespaceResponse, error) {
	ctx, err := t.admit(ctx, "CreateNamespace")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CreateNamespace"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	svcReq := &service.CreateNamespaceRequest{
		Namespace:      req.Namespace,
		Dimension:      req.Dimension,
		DistanceMetric: req.DistanceMetric,
		IndexType:      req.IndexType,
		IndexConfig:    req.IndexConfig,
		Metadata:       req.Metadata,
		ShardID:        req.ShardID,
	}
	resp, err := t.vectorSvc.CreateNamespace(ctx, svcReq)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.CreateNamespaceResponse{
		Namespace: resp.Namespace,
		Dimension: resp.Dimension,
		Timestamp: resp.Timestamp,
		Success:   resp.Success,
	}, nil
}

// DeleteNamespace removes a namespace and all its vectors through the
// embedded vector service.
func (t *Transport) DeleteNamespace(ctx context.Context, req *transport.DeleteNamespaceRequest) (*transport.DeleteNamespaceResponse, error) {
	ctx, err := t.admit(ctx, "DeleteNamespace")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DeleteNamespace"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.DeleteNamespace(ctx, &service.DeleteNamespaceRequest{
		Namespace: req.Namespace,
		ShardID:   req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.DeleteNamespaceResponse{
		Success:        resp.Success,
		VectorsDeleted: resp.VectorsDeleted,
		Timestamp:      resp.Timestamp,
	}, nil
}

// ListNamespaces returns all vector namespaces through the embedded
// vector service.
func (t *Transport) ListNamespaces(ctx context.Context, req *transport.ListNamespacesRequest) (*transport.ListNamespacesResponse, error) {
	ctx, err := t.admit(ctx, "ListNamespaces")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListNamespaces"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.ListNamespaces(ctx, &service.ListNamespacesRequest{
		Prefix:  req.Prefix,
		Limit:   req.Limit,
		Offset:  req.Offset,
		ShardID: req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.ListNamespacesResponse{
		Namespaces: toTransportNamespaceInfoSlice(resp.Namespaces),
		TotalCount: resp.TotalCount,
		Timestamp:  resp.Timestamp,
	}, nil
}

// GetNamespaceInfo returns metadata about a namespace through the
// embedded vector service.
func (t *Transport) GetNamespaceInfo(ctx context.Context, req *transport.GetNamespaceInfoRequest) (*transport.GetNamespaceInfoResponse, error) {
	ctx, err := t.admit(ctx, "GetNamespaceInfo")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetNamespaceInfo"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{
		Namespace: req.Namespace,
		ShardID:   req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.GetNamespaceInfoResponse{
		Namespace:      resp.Namespace,
		Dimension:      resp.Dimension,
		VectorCount:    resp.VectorCount,
		DistanceMetric: resp.DistanceMetric,
		IndexType:      resp.IndexType,
		IndexConfig:    resp.IndexConfig,
		Metadata:       resp.Metadata,
		CreatedAt:      resp.CreatedAt,
		UpdatedAt:      resp.UpdatedAt,
	}, nil
}

// UpsertVector inserts or updates a vector through the embedded vector
// service.
func (t *Transport) UpsertVector(ctx context.Context, req *transport.UpsertVectorRequest) (*transport.UpsertVectorResponse, error) {
	ctx, err := t.admit(ctx, "UpsertVector")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "UpsertVector"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.UpsertVector(ctx, &service.UpsertVectorRequest{
		Namespace: req.Namespace,
		ID:        req.ID,
		Vector:    req.Vector,
		Metadata:  req.Metadata,
		ShardID:   req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.UpsertVectorResponse{
		ID:        resp.ID,
		Version:   resp.Version,
		Timestamp: resp.Timestamp,
		Success:   resp.Success,
	}, nil
}

// GetVector retrieves a single vector by ID through the embedded vector
// service.
func (t *Transport) GetVector(ctx context.Context, req *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	ctx, err := t.admit(ctx, "GetVector")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetVector"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.GetVector(ctx, &service.GetVectorRequest{
		Namespace:       req.Namespace,
		ID:              req.ID,
		IncludeVector:   req.IncludeVector,
		IncludeMetadata: req.IncludeMetadata,
		ShardID:         req.ShardID,
		Consistency:     service.ConsistencyLevel(req.Consistency),
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.GetVectorResponse{
		ID:        resp.ID,
		Vector:    resp.Vector,
		Metadata:  resp.Metadata,
		Version:   resp.Version,
		CreatedAt: resp.CreatedAt,
		UpdatedAt: resp.UpdatedAt,
		Found:     resp.Found,
	}, nil
}

// DeleteVector removes a single vector by ID through the embedded
// vector service.
func (t *Transport) DeleteVector(ctx context.Context, req *transport.DeleteVectorRequest) (*transport.DeleteVectorResponse, error) {
	ctx, err := t.admit(ctx, "DeleteVector")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DeleteVector"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.DeleteVector(ctx, &service.DeleteVectorRequest{
		Namespace: req.Namespace,
		ID:        req.ID,
		ShardID:   req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.DeleteVectorResponse{
		Success:   resp.Success,
		Timestamp: resp.Timestamp,
	}, nil
}

// BatchUpsert inserts or updates multiple vectors through the embedded
// vector service.
func (t *Transport) BatchUpsert(ctx context.Context, req *transport.BatchUpsertRequest) (*transport.BatchUpsertResponse, error) {
	ctx, err := t.admit(ctx, "BatchUpsert")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BatchUpsert"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	svcVectors := toServiceVectorDataPtrSlice(req.Vectors)
	resp, err := t.vectorSvc.BatchUpsert(ctx, &service.BatchUpsertRequest{
		Namespace: req.Namespace,
		Vectors:   svcVectors,
		ShardID:   req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.BatchUpsertResponse{
		IDs:           resp.IDs,
		Versions:      resp.Versions,
		Timestamp:     resp.Timestamp,
		Success:       resp.Success,
		InsertedCount: resp.InsertedCount,
	}, nil
}

// BatchDeleteVectors removes multiple vectors by ID through the embedded
// vector service.
func (t *Transport) BatchDeleteVectors(ctx context.Context, req *transport.BatchDeleteVectorsRequest) (*transport.BatchDeleteVectorsResponse, error) {
	ctx, err := t.admit(ctx, "BatchDeleteVectors")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "BatchDeleteVectors"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.BatchDeleteVectors(ctx, &service.BatchDeleteVectorsRequest{
		Namespace: req.Namespace,
		IDs:       req.IDs,
		ShardID:   req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.BatchDeleteVectorsResponse{
		DeletedCount: resp.DeletedCount,
		FailedIDs:    resp.FailedIDs,
		Timestamp:    resp.Timestamp,
		Success:      resp.Success,
	}, nil
}

// ListVectors returns a paginated list of vectors through the embedded
// vector service.
func (t *Transport) ListVectors(ctx context.Context, req *transport.ListVectorsRequest) (*transport.ListVectorsResponse, error) {
	ctx, err := t.admit(ctx, "ListVectors")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListVectors"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.ListVectors(ctx, &service.ListVectorsRequest{
		Namespace:       req.Namespace,
		Limit:           req.Limit,
		Cursor:          req.Cursor,
		IDPrefix:        req.IDPrefix,
		IncludeVectors:  req.IncludeVectors,
		IncludeMetadata: req.IncludeMetadata,
		ShardID:         req.ShardID,
		Consistency:     service.ConsistencyLevel(req.Consistency),
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.ListVectorsResponse{
		Vectors:    toTransportVectorDataPtrSlice(resp.Vectors),
		NextCursor: resp.NextCursor,
		TotalCount: resp.TotalCount,
		Timestamp:  resp.Timestamp,
	}, nil
}

// UpdateMetadata updates the metadata for an existing vector through
// the embedded vector service.
func (t *Transport) UpdateMetadata(ctx context.Context, req *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	ctx, err := t.admit(ctx, "UpdateMetadata")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "UpdateMetadata"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{
		Namespace:     req.Namespace,
		ID:            req.ID,
		Metadata:      req.Metadata,
		MergeMetadata: req.MergeMetadata,
		DeleteKeys:    req.DeleteKeys,
		ShardID:       req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.UpdateVectorMetadataResponse{
		ID:        resp.ID,
		Version:   resp.Version,
		Timestamp: resp.Timestamp,
		Success:   resp.Success,
	}, nil
}

// SearchSimilar performs a k-nearest-neighbor search through the
// embedded vector service.
func (t *Transport) SearchSimilar(ctx context.Context, req *transport.SearchSimilarRequest) (*transport.SearchSimilarResponse, error) {
	ctx, err := t.admit(ctx, "SearchSimilar")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "SearchSimilar"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.SearchSimilar(ctx, &service.SearchSimilarRequest{
		Namespace:       req.Namespace,
		QueryVector:     req.QueryVector,
		K:               req.K,
		EfSearch:        req.EfSearch,
		MinScore:        req.MinScore,
		IncludeVectors:  req.IncludeVectors,
		IncludeMetadata: req.IncludeMetadata,
		MetadataFilter:  req.MetadataFilter,
		ShardID:         req.ShardID,
		Consistency:     service.ConsistencyLevel(req.Consistency),
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.SearchSimilarResponse{
		Results:       toTransportSearchResults(resp.Results),
		Timestamp:     resp.Timestamp,
		QueryDuration: resp.QueryDuration,
	}, nil
}

// SearchByMetadata returns vectors matching metadata filters through
// the embedded vector service.
func (t *Transport) SearchByMetadata(ctx context.Context, req *transport.SearchByMetadataRequest) (*transport.SearchByMetadataResponse, error) {
	ctx, err := t.admit(ctx, "SearchByMetadata")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "SearchByMetadata"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.SearchByMetadata(ctx, &service.SearchByMetadataRequest{
		Namespace:       req.Namespace,
		Filters:         req.Filters,
		FilterOperator:  req.FilterOperator,
		Limit:           req.Limit,
		Offset:          req.Offset,
		IncludeVectors:  req.IncludeVectors,
		IncludeMetadata: req.IncludeMetadata,
		ShardID:         req.ShardID,
		Consistency:     service.ConsistencyLevel(req.Consistency),
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.SearchByMetadataResponse{
		Results:    toTransportVectorDataPtrSlice(resp.Results),
		TotalCount: resp.TotalCount,
		Timestamp:  resp.Timestamp,
		NextCursor: resp.NextCursor,
	}, nil
}

// HybridSearch combines vector similarity and metadata filtering
// through the embedded vector service.
func (t *Transport) HybridSearch(ctx context.Context, req *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	ctx, err := t.admit(ctx, "HybridSearch")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "HybridSearch"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.HybridSearch(ctx, &service.HybridSearchRequest{
		Namespace:          req.Namespace,
		QueryVector:        req.QueryVector,
		K:                  req.K,
		EfSearch:           req.EfSearch,
		MinScore:           req.MinScore,
		MetadataFilter:     req.MetadataFilter,
		FilterOperator:     req.FilterOperator,
		RerankingAlgorithm: req.RerankingAlgorithm,
		IncludeVectors:     req.IncludeVectors,
		IncludeMetadata:    req.IncludeMetadata,
		ShardID:            req.ShardID,
		Consistency:        service.ConsistencyLevel(req.Consistency),
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.HybridSearchResponse{
		Results:         toTransportSearchResults(resp.Results),
		TotalCandidates: resp.TotalCandidates,
		TotalFiltered:   resp.TotalFiltered,
		Timestamp:       resp.Timestamp,
		QueryDuration:   resp.QueryDuration,
	}, nil
}

// SearchByID searches using an existing vector's ID as the query
// through the embedded vector service.
func (t *Transport) SearchByID(ctx context.Context, req *transport.SearchByIDRequest) (*transport.SearchByIDResponse, error) {
	ctx, err := t.admit(ctx, "SearchByID")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "SearchByID"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.SearchByID(ctx, &service.SearchByIDRequest{
		Namespace:       req.Namespace,
		ID:              req.ID,
		K:               req.K,
		EfSearch:        req.EfSearch,
		MinScore:        req.MinScore,
		IncludeVectors:  req.IncludeVectors,
		IncludeMetadata: req.IncludeMetadata,
		MetadataFilter:  req.MetadataFilter,
		ExcludeSelf:     req.ExcludeSelf,
		ShardID:         req.ShardID,
		Consistency:     service.ConsistencyLevel(req.Consistency),
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.SearchByIDResponse{
		SourceID:      resp.SourceID,
		Results:       toTransportSearchResults(resp.Results),
		Timestamp:     resp.Timestamp,
		QueryDuration: resp.QueryDuration,
	}, nil
}

// RebuildIndex triggers an asynchronous index rebuild through the
// embedded vector service.
func (t *Transport) RebuildIndex(ctx context.Context, req *transport.RebuildIndexRequest) (*transport.RebuildIndexResponse, error) {
	ctx, err := t.admit(ctx, "RebuildIndex")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "RebuildIndex"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.RebuildIndex(ctx, &service.RebuildIndexRequest{
		Namespace:   req.Namespace,
		IndexConfig: req.IndexConfig,
		Async:       req.Async,
		ShardID:     req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.RebuildIndexResponse{
		JobID:                  resp.JobID,
		Status:                 resp.Status,
		VectorsProcessed:       resp.VectorsProcessed,
		TotalVectors:           resp.TotalVectors,
		PercentComplete:        resp.PercentComplete,
		EstimatedTimeRemaining: resp.EstimatedTimeRemaining,
		Timestamp:              resp.Timestamp,
		Success:                resp.Success,
	}, nil
}

// GetRebuildProgress returns the current progress of an index rebuild
// through the embedded vector service.
func (t *Transport) GetRebuildProgress(ctx context.Context, req *transport.GetRebuildProgressRequest) (*transport.GetRebuildProgressResponse, error) {
	ctx, err := t.admit(ctx, "GetRebuildProgress")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetRebuildProgress"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.GetRebuildProgress(ctx, &service.GetRebuildProgressRequest{
		JobID:     req.JobID,
		Namespace: req.Namespace,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.GetRebuildProgressResponse{
		JobID:                    resp.JobID,
		Namespace:                resp.Namespace,
		Status:                   resp.Status,
		VectorsProcessed:         resp.VectorsProcessed,
		TotalVectors:             resp.TotalVectors,
		PercentComplete:          resp.PercentComplete,
		ElapsedTimeMs:            resp.ElapsedTimeMs,
		EstimatedTimeRemainingMs: resp.EstimatedTimeRemainingMs,
		StartedAt:                resp.StartedAt,
		CompletedAt:              resp.CompletedAt,
		Error:                    resp.Error,
	}, nil
}

// GetIndexStats returns statistics about a namespace's vector index
// through the embedded vector service.
func (t *Transport) GetIndexStats(ctx context.Context, req *transport.GetIndexStatsRequest) (*transport.GetIndexStatsResponse, error) {
	ctx, err := t.admit(ctx, "GetIndexStats")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetIndexStats"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.GetIndexStats(ctx, &service.GetIndexStatsRequest{
		Namespace: req.Namespace,
		ShardID:   req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.GetIndexStatsResponse{
		Namespace:         resp.Namespace,
		IndexType:         resp.IndexType,
		VectorCount:       resp.VectorCount,
		Dimension:         resp.Dimension,
		IndexSizeBytes:    resp.IndexSizeBytes,
		MemoryUsageBytes:  resp.MemoryUsageBytes,
		IndexConfig:       resp.IndexConfig,
		SearchPerformance: toTransportIndexPerformanceMetrics(resp.SearchPerformance),
		Timestamp:         resp.Timestamp,
	}, nil
}

// CompactNamespace triggers compaction of a namespace's storage through
// the embedded vector service.
func (t *Transport) CompactNamespace(ctx context.Context, req *transport.CompactNamespaceRequest) (*transport.CompactNamespaceResponse, error) {
	ctx, err := t.admit(ctx, "CompactNamespace")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CompactNamespace"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.CompactNamespace(ctx, &service.CompactNamespaceRequest{
		Namespace: req.Namespace,
		Async:     req.Async,
		TimeoutMs: req.TimeoutMs,
		ShardID:   req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.CompactNamespaceResponse{
		OperationID:    resp.OperationID,
		StartedAt:      resp.StartedAt,
		CompletedAt:    resp.CompletedAt,
		BytesCompacted: resp.BytesCompacted,
		Success:        resp.Success,
	}, nil
}

// GetCompactionProgress returns the current progress of a compaction
// through the embedded vector service.
func (t *Transport) GetCompactionProgress(ctx context.Context, req *transport.GetCompactionProgressRequest) (*transport.GetCompactionProgressResponse, error) {
	ctx, err := t.admit(ctx, "GetCompactionProgress")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetCompactionProgress"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.vectorSvc == nil {
		return nil, &NilServiceError{Service: "VectorServicer"}
	}
	resp, err := t.vectorSvc.GetCompactionProgress(ctx, &service.GetCompactionProgressRequest{
		Namespace:   req.Namespace,
		OperationID: req.OperationID,
		ShardID:     req.ShardID,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.GetCompactionProgressResponse{
		OperationID:     resp.OperationID,
		Namespace:       resp.Namespace,
		BytesCompacted:  resp.BytesCompacted,
		TotalBytes:      resp.TotalBytes,
		PercentComplete: resp.PercentComplete,
		InProgress:      resp.InProgress,
		StartedAt:       resp.StartedAt,
		CompletedAt:     resp.CompletedAt,
		Error:           resp.Error,
	}, nil
}

// ---------------------------------------------------------------------------
// Type conversion helpers
// ---------------------------------------------------------------------------

// toTransportKV converts a service-layer KeyValue to a transport-layer
// MVCCKeyValue.
func toTransportKV(kv *service.KeyValue) *transport.MVCCKeyValue {
	if kv == nil {
		return nil
	}
	return &transport.MVCCKeyValue{
		Key:            kv.Key,
		Value:          kv.Value,
		CreateRevision: kv.CreateRevision,
		ModRevision:    kv.ModRevision,
		Version:        kv.Version,
		LeaseID:        kv.LeaseID,
	}
}

// toTransportKVSlice converts a slice of service-layer KeyValue pointers
// to a slice of transport-layer MVCCKeyValue pointers.
func toTransportKVSlice(kvs []*service.KeyValue) []*transport.MVCCKeyValue {
	if kvs == nil {
		return nil
	}
	out := make([]*transport.MVCCKeyValue, len(kvs))
	for i, kv := range kvs {
		out[i] = toTransportKV(kv)
	}
	return out
}

// transportRestoreActionFromService converts service-layer RestoreAction to
// transport-layer RestoreAction.
var transportRestoreActionFromService = map[service.RestoreAction]transport.RestoreAction{
	service.RestoreActionRevert:   transport.RestoreActionRevert,
	service.RestoreActionRecreate: transport.RestoreActionRecreate,
	service.RestoreActionDelete:   transport.RestoreActionDelete,
}

// toServiceRestoreRevisionRequest converts a transport-layer RestoreRevisionRequest
// to its service-layer representation. Prefix and Keys are converted from
// []byte / [][]byte to string / []string -- a lossless, byte-transparent
// conversion in Go, unlike the JSON/base64 encoding the wire transports
// apply to preserve the same bytes across the network.
func toServiceRestoreRevisionRequest(req *transport.RestoreRevisionRequest) *service.RestoreRevisionRequest {
	keys := make([]string, len(req.Keys))
	for i, k := range req.Keys {
		keys[i] = string(k)
	}
	return &service.RestoreRevisionRequest{
		Revision: req.Revision,
		Prefix:   string(req.Prefix),
		Keys:     keys,
		DryRun:   req.DryRun,
		Force:    req.Force,
	}
}

// toTransportRestoreRevisionChanges converts service-layer RestoreKeyChange values
// to transport-layer RestoreKeyChange pointers.
func toTransportRestoreRevisionChanges(changes []service.RestoreKeyChange) []*transport.RestoreKeyChange {
	out := make([]*transport.RestoreKeyChange, len(changes))
	for i, c := range changes {
		out[i] = &transport.RestoreKeyChange{
			Key:          []byte(c.Key),
			Action:       transportRestoreActionFromService[c.Action],
			FromRevision: c.FromRevision,
			ToRevision:   c.ToRevision,
			ValueBytes:   c.ValueBytes,
		}
	}
	return out
}

// toTransportRestoreRevisionResult converts a service-layer
// RestoreRevisionResult to its transport-layer representation.
func toTransportRestoreRevisionResult(result *service.RestoreRevisionResult) *transport.RestoreRevisionResult {
	if result == nil {
		return nil
	}
	return &transport.RestoreRevisionResult{
		Revision:   result.Revision,
		Changes:    toTransportRestoreRevisionChanges(result.Changes),
		Conflicts:  toTransportRestoreRevisionChanges(result.Conflicts),
		Applied:    result.Applied,
		Skipped:    result.Skipped,
		Conflicted: result.Conflicted,
		DryRun:     result.DryRun,
	}
}

// toServiceCompares converts transport-layer Compare values to
// service-layer Compare values.
func toServiceCompares(compares []transport.Compare) []service.Compare {
	out := make([]service.Compare, len(compares))
	for i, c := range compares {
		out[i] = service.Compare{
			Key:            c.Key,
			Target:         service.CompareTarget(c.Target),
			Result:         service.CompareResult(c.Result),
			Value:          c.Value,
			Version:        c.Version,
			CreateRevision: c.CreateRevision,
			ModRevision:    c.ModRevision,
		}
	}
	return out
}

// toServiceOps converts transport-layer Op values to service-layer
// Op values.
func toServiceOps(ops []transport.Op) []service.Op {
	out := make([]service.Op, len(ops))
	for i, op := range ops {
		out[i] = service.Op{
			Type:     service.OpType(op.Type),
			Key:      op.Key,
			Value:    op.Value,
			RangeEnd: op.RangeEnd,
			LeaseID:  op.LeaseID,
		}
	}
	return out
}

// toTransportOpResponses converts service-layer OpResponse values to
// transport-layer OpResponse values.
func toTransportOpResponses(resps []service.OpResponse) []transport.OpResponse {
	out := make([]transport.OpResponse, len(resps))
	for i, r := range resps {
		out[i] = transport.OpResponse{
			Kvs:       toTransportKVSlice(r.Kvs),
			Count:     r.Count,
			Succeeded: r.Succeeded,
		}
	}
	return out
}

// toServiceRangeOpts converts transport-layer RangeOptions to a
// variadic slice of service-layer RangeOption functions.
func toServiceRangeOpts(opts transport.RangeOptions) []service.RangeOption {
	var out []service.RangeOption
	if opts.Limit != 0 {
		out = append(out, service.WithLimit(opts.Limit))
	}
	if opts.Revision != 0 {
		out = append(out, service.WithRevision(opts.Revision))
	}
	if opts.CountOnly {
		out = append(out, service.WithCountOnly())
	}
	if opts.KeysOnly {
		out = append(out, service.WithKeysOnly())
	}
	if opts.SortOrder != transport.SortNone || opts.SortTarget != transport.SortByKey {
		out = append(out, service.WithSort(
			service.SortOrder(opts.SortOrder),
			service.SortTarget(opts.SortTarget),
		))
	}
	return out
}

// toServiceWatchOpts converts transport-layer WatchOptions to a
// variadic slice of service-layer WatchOption functions.
func toServiceWatchOpts(opts transport.WatchOptions) []watch.WatchOption {
	var out []watch.WatchOption
	if opts.StartRevision != 0 {
		out = append(out, watch.WithStartRevision(opts.StartRevision))
	}
	if opts.PrevKV {
		out = append(out, watch.WithPrevKV())
	}
	if opts.ProgressNotify {
		out = append(out, watch.WithProgressNotify())
	}
	return out
}

// toTransportWatchResponse converts a service-layer WatchResponse to a
// transport-layer WatchResponse.
func toTransportWatchResponse(resp watch.WatchResponse) transport.WatchResponse {
	events := make([]*transport.WatchEvent, 0, len(resp.Events))
	for _, evt := range resp.Events {
		if evt == nil {
			continue
		}
		events = append(events, &transport.WatchEvent{
			Type:   transport.EventType(evt.Type),
			Kv:     toTransportKV(evt.Kv),
			PrevKv: toTransportKV(evt.PrevKv),
		})
	}
	return transport.WatchResponse{
		WatchID:      resp.WatchID,
		Events:       events,
		Canceled:     resp.Canceled,
		CancelReason: resp.CancelReason,
		Revision:     resp.Revision,
	}
}

// toTransportLease converts a service-layer Lease to a transport-layer
// LeaseInfo.
func toTransportLease(l *lease.Lease) *transport.LeaseInfo {
	if l == nil {
		return nil
	}
	return &transport.LeaseInfo{
		ID:           l.ID,
		TTL:          l.TTL,
		GrantedTTL:   l.GrantedTTL,
		RemainingTTL: l.RemainingTTL,
		Keys:         l.Keys,
	}
}

// toTransportLeaseSlice converts a slice of service-layer Lease pointers
// to a slice of transport-layer LeaseInfo pointers.
func toTransportLeaseSlice(ls []*lease.Lease) []*transport.LeaseInfo {
	if ls == nil {
		return nil
	}
	out := make([]*transport.LeaseInfo, len(ls))
	for i, l := range ls {
		out[i] = toTransportLease(l)
	}
	return out
}

// tagsToMap converts a slice of transport TSTag values to a map.
func tagsToMap(tags []transport.TSTag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, tag := range tags {
		m[tag.Key] = tag.Value
	}
	return m
}

// mapToTags converts a map of tags to a slice of transport TSTag values.
func mapToTags(m map[string]string) []transport.TSTag {
	if len(m) == 0 {
		return nil
	}
	tags := make([]transport.TSTag, 0, len(m))
	for k, v := range m {
		tags = append(tags, transport.TSTag{Key: k, Value: v})
	}
	return tags
}

// toTransportDataPoints converts a slice of service-layer DataPoint
// values to a slice of transport-layer TSDataPoint values.
func toTransportDataPoints(points []service.DataPoint) []transport.TSDataPoint {
	out := make([]transport.TSDataPoint, len(points))
	for i, p := range points {
		out[i] = transport.TSDataPoint{
			Metric:      p.Metric,
			Value:       p.Value,
			TimestampNs: p.Timestamp.UnixNano(),
			Tags:        mapToTags(p.Tags),
		}
	}
	return out
}

// toTransportDataPointPtrs converts a slice of service-layer DataPoint
// pointers to a slice of transport-layer TSDataPoint values.
func toTransportDataPointPtrs(points []*timeseries.DataPoint) []transport.TSDataPoint {
	out := make([]transport.TSDataPoint, len(points))
	for i, p := range points {
		out[i] = transport.TSDataPoint{
			Metric:      p.Metric,
			Value:       p.Value,
			TimestampNs: p.Timestamp.UnixNano(),
			Tags:        mapToTags(p.Tags),
		}
	}
	return out
}

// toTransportAggResults converts a slice of service-layer AggResult
// values to a slice of transport-layer TSAggResult values.
func toTransportAggResults(results []service.AggResult) []transport.TSAggResult {
	out := make([]transport.TSAggResult, len(results))
	for i, r := range results {
		out[i] = transport.TSAggResult{
			TimestampNs: r.Timestamp.UnixNano(),
			Value:       r.Value,
			Count:       r.Count,
		}
	}
	return out
}

// toTransportRichQueryResponse converts a service-layer QueryResult to
// a transport-layer TSRichQueryResponse.
func toTransportRichQueryResponse(result *timeseries.QueryResult) *transport.TSRichQueryResponse {
	resp := &transport.TSRichQueryResponse{
		Metric:          result.Metric,
		Tags:            mapToTags(result.Tags),
		Points:          toTransportDataPointPtrs(result.Points),
		AggregatedValue: result.AggregatedValue,
	}

	if len(result.GroupedResults) > 0 {
		resp.GroupedResults = make([]transport.TSGroupedResult, len(result.GroupedResults))
		for i, gr := range result.GroupedResults {
			resp.GroupedResults[i] = transport.TSGroupedResult{
				Tags:            mapToTags(gr.Tags),
				Points:          toTransportDataPointPtrs(gr.Points),
				AggregatedValue: gr.AggregatedValue,
			}
		}
	}

	return resp
}

// toTransportRetentionPolicy converts a service-layer RetentionPolicy
// to a transport-layer TSRetentionPolicy.
func toTransportRetentionPolicy(p *timeseries.RetentionPolicy) *transport.TSRetentionPolicy {
	if p == nil {
		return nil
	}
	return &transport.TSRetentionPolicy{
		DurationNs:   int64(p.Duration),
		ResolutionNs: int64(p.Resolution),
		Aggregation:  transport.AggFunc(p.Aggregation),
	}
}

// toServiceRetentionPolicy converts a transport-layer TSRetentionPolicy
// to a service-layer RetentionPolicy.
func toServiceRetentionPolicy(p *transport.TSRetentionPolicy) *timeseries.RetentionPolicy {
	if p == nil {
		return nil
	}
	return &timeseries.RetentionPolicy{
		Duration:    time.Duration(p.DurationNs),
		Resolution:  time.Duration(p.ResolutionNs),
		Aggregation: timeseries.AggFunc(p.Aggregation),
	}
}

// toTransportNamespaceInfo converts a service-layer NamespaceInfo to a
// transport-layer NamespaceInfo.
func toTransportNamespaceInfo(info *service.NamespaceInfo) *transport.NamespaceInfo {
	if info == nil {
		return nil
	}
	return &transport.NamespaceInfo{
		Namespace:      info.Namespace,
		Dimension:      info.Dimension,
		VectorCount:    info.VectorCount,
		DistanceMetric: info.DistanceMetric,
		IndexType:      info.IndexType,
		IndexConfig:    info.IndexConfig,
		Metadata:       info.Metadata,
		CreatedAt:      info.CreatedAt,
		UpdatedAt:      info.UpdatedAt,
	}
}

// toTransportNamespaceInfoSlice converts a slice of service-layer
// NamespaceInfo pointers to a slice of transport-layer NamespaceInfo pointers.
func toTransportNamespaceInfoSlice(infos []*service.NamespaceInfo) []*transport.NamespaceInfo {
	if infos == nil {
		return nil
	}
	out := make([]*transport.NamespaceInfo, len(infos))
	for i, info := range infos {
		out[i] = toTransportNamespaceInfo(info)
	}
	return out
}

// toTransportVectorData converts a service-layer VectorData pointer to a
// transport-layer VectorData pointer.
func toTransportVectorData(v *service.VectorData) *transport.VectorData {
	if v == nil {
		return nil
	}
	return &transport.VectorData{
		ID:        v.ID,
		Vector:    v.Vector,
		Metadata:  v.Metadata,
		Version:   v.Version,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
	}
}

// toTransportVectorDataPtrSlice converts a slice of service-layer VectorData
// pointers to a slice of transport-layer VectorData pointers.
func toTransportVectorDataPtrSlice(vs []*service.VectorData) []*transport.VectorData {
	if vs == nil {
		return nil
	}
	out := make([]*transport.VectorData, len(vs))
	for i, v := range vs {
		out[i] = toTransportVectorData(v)
	}
	return out
}

// toServiceVectorDataPtrSlice converts a slice of transport-layer VectorData
// pointers to a slice of service-layer VectorData pointers.
func toServiceVectorDataPtrSlice(vs []*transport.VectorData) []*service.VectorData {
	if vs == nil {
		return nil
	}
	out := make([]*service.VectorData, len(vs))
	for i, v := range vs {
		out[i] = &service.VectorData{
			ID:       v.ID,
			Vector:   v.Vector,
			Metadata: v.Metadata,
			Version:  v.Version,
		}
	}
	return out
}

// toTransportSearchResults converts a slice of service-layer
// VectorSearchResult pointers to a slice of transport-layer VectorSearchResult pointers.
func toTransportSearchResults(results []*service.VectorSearchResult) []*transport.VectorSearchResult {
	if results == nil {
		return nil
	}
	out := make([]*transport.VectorSearchResult, len(results))
	for i, r := range results {
		out[i] = &transport.VectorSearchResult{
			ID:       r.ID,
			Score:    r.Score,
			Vector:   r.Vector,
			Metadata: r.Metadata,
			Distance: r.Distance,
			Rank:     r.Rank,
		}
	}
	return out
}

// toTransportIndexPerformanceMetrics converts a service-layer
// IndexPerformanceMetrics to a transport-layer IndexPerformanceMetrics.
func toTransportIndexPerformanceMetrics(m *service.IndexPerformanceMetrics) *transport.IndexPerformanceMetrics {
	if m == nil {
		return nil
	}
	return &transport.IndexPerformanceMetrics{
		AverageSearchTimeMs: m.AverageSearchTimeMs,
		P50SearchTimeMs:     m.P50SearchTimeMs,
		P95SearchTimeMs:     m.P95SearchTimeMs,
		P99SearchTimeMs:     m.P99SearchTimeMs,
		TotalSearches:       m.TotalSearches,
		CacheHitRate:        m.CacheHitRate,
	}
}

// ---------------------------------------------------------------------------
// Generic Request dispatch handlers
// ---------------------------------------------------------------------------

func handlePut(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*putRequest)
	if !ok {
		return &InvalidRequestError{Method: "Put"}
	}
	return t.Put(ctx, r.Key, r.Value)
}

func handleGet(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*getRequest)
	if !ok {
		return &InvalidRequestError{Method: "Get"}
	}
	data, err := t.Get(ctx, r.Key)
	if err != nil {
		return err
	}
	if out, ok := resp.(*[]byte); ok {
		*out = data
	}
	return nil
}

func handleDelete(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*deleteRequest)
	if !ok {
		return &InvalidRequestError{Method: "Delete"}
	}
	return t.Delete(ctx, r.Key)
}

func handleList(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*listRequest)
	if !ok {
		return &InvalidRequestError{Method: "List"}
	}
	keys, err := t.List(ctx, r.Prefix)
	if err != nil {
		return err
	}
	if out, ok := resp.(*[]string); ok {
		*out = keys
	}
	return nil
}

func handleHealth(ctx context.Context, t *Transport, _, resp any) error {
	health, err := t.Health(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.HealthResponse); ok {
		*out = health
	}
	return nil
}

func handleStatus(ctx context.Context, t *Transport, _, resp any) error {
	status, err := t.Status(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.StatusResponse); ok {
		*out = status
	}
	return nil
}

func handlePeers(ctx context.Context, t *Transport, _, resp any) error {
	peers, err := t.Peers(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.PeersResponse); ok {
		*out = peers
	}
	return nil
}

func handleAddMember(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.AddMemberRequest)
	if !ok {
		return &InvalidRequestError{Method: "AddMember"}
	}
	return t.AddMember(ctx, r.ShardID, r.ReplicaID, r.Address, r.Identity)
}

func handleRemoveMember(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.RemoveMemberRequest)
	if !ok {
		return &InvalidRequestError{Method: "RemoveMember"}
	}
	return t.RemoveMember(ctx, r.ShardID, r.ReplicaID)
}

func handlePromoteMember(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.PromoteMemberRequest)
	if !ok {
		return &InvalidRequestError{Method: "PromoteMember"}
	}
	return t.PromoteMember(ctx, r.ShardID, r.ReplicaID)
}

func handleRetireJoinIdentity(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.RetireJoinIdentityRequest)
	if !ok {
		return &InvalidRequestError{Method: "RetireJoinIdentity"}
	}
	return t.RetireJoinIdentity(ctx, r.NodeID, r.Reason)
}

func handleRequestSnapshot(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.SnapshotRequest)
	if !ok {
		return &InvalidRequestError{Method: "RequestSnapshot"}
	}
	return t.RequestSnapshot(ctx, r.ShardID)
}

func handleDrain(ctx context.Context, t *Transport, _, _ any) error {
	return t.Drain(ctx)
}

func handleCapabilities(ctx context.Context, t *Transport, _, resp any) error {
	caps, err := t.Capabilities(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CapabilitiesResponse); ok {
		*out = caps
	}
	return nil
}

func handleStartShard(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.StartShardRequest)
	if !ok {
		return &InvalidRequestError{Method: "StartShard"}
	}
	return t.StartShard(ctx, r)
}

func handleStopShard(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.StopShardRequest)
	if !ok {
		return &InvalidRequestError{Method: "StopShard"}
	}
	return t.StopShard(ctx, r.ShardID)
}

func handleGetShardState(ctx context.Context, t *Transport, req, resp any) error {
	shardID, ok := req.(uint64)
	if !ok {
		return &InvalidRequestError{Method: "GetShardState"}
	}
	state, err := t.GetShardState(ctx, shardID)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.ShardStateResponse); ok {
		*out = state
	}
	return nil
}

func handleGetTopology(ctx context.Context, t *Transport, _, resp any) error {
	topo, err := t.GetTopology(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TopologyResponse); ok {
		*out = topo
	}
	return nil
}

func handleReplicaStates(ctx context.Context, t *Transport, req, resp any) error {
	var shardID uint64
	if req != nil {
		id, ok := req.(uint64)
		if !ok {
			return &InvalidRequestError{Method: "ReplicaStates"}
		}
		shardID = id
	}
	states, err := t.ReplicaStates(ctx, shardID)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.ReplicaStatesResponse); ok {
		*out = states
	}
	return nil
}

func handlePutWithLease(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*putWithLeaseRequest)
	if !ok {
		return &InvalidRequestError{Method: "PutWithLease"}
	}
	kv, err := t.PutWithLease(ctx, r.Key, r.Value, r.LeaseID)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.MVCCKeyValue); ok {
		*out = kv
	}
	return nil
}

func handleGetRevision(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*getRevisionRequest)
	if !ok {
		return &InvalidRequestError{Method: "GetRevision"}
	}
	kv, err := t.GetRevision(ctx, r.Key)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.MVCCKeyValue); ok {
		*out = kv
	}
	return nil
}

func handleGetAtRevision(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*getAtRevisionRequest)
	if !ok {
		return &InvalidRequestError{Method: "GetAtRevision"}
	}
	kv, err := t.GetAtRevision(ctx, r.Key, r.Revision)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.MVCCKeyValue); ok {
		*out = kv
	}
	return nil
}

func handleRange(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*rangeRequest)
	if !ok {
		return &InvalidRequestError{Method: "Range"}
	}
	result, err := t.Range(ctx, r.Start, r.End, r.Opts)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.RangeResponse); ok {
		*out = result
	}
	return nil
}

func handleCompact(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*compactRequest)
	if !ok {
		return &InvalidRequestError{Method: "Compact"}
	}
	return t.Compact(ctx, r.Revision)
}

func handleCurrentRevision(ctx context.Context, t *Transport, _, resp any) error {
	rev, err := t.CurrentRevision(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(*int64); ok {
		*out = rev
	}
	return nil
}

func handleRestoreToRevision(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.RestoreRevisionRequest)
	if !ok {
		return &InvalidRequestError{Method: "RestoreToRevision"}
	}
	result, err := t.RestoreToRevision(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.RestoreRevisionResult); ok {
		*out = result
	}
	return nil
}

func handleExecuteTransaction(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TransactionRequest)
	if !ok {
		return &InvalidRequestError{Method: "ExecuteTransaction"}
	}
	result, err := t.ExecuteTransaction(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TransactionResponse); ok {
		*out = result
	}
	return nil
}

func handleExecuteBatch(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.BatchRequest)
	if !ok {
		return &InvalidRequestError{Method: "ExecuteBatch"}
	}
	result, err := t.ExecuteBatch(ctx, r.Ops)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BatchResponse); ok {
		*out = result
	}
	return nil
}

func handleWatch(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*watchRequest)
	if !ok {
		return &InvalidRequestError{Method: "Watch"}
	}
	ch, err := t.Watch(ctx, r.Key, r.Opts)
	if err != nil {
		return err
	}
	if out, ok := resp.(*<-chan transport.WatchResponse); ok {
		*out = ch
	}
	return nil
}

func handleWatchPrefix(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*watchPrefixRequest)
	if !ok {
		return &InvalidRequestError{Method: "WatchPrefix"}
	}
	ch, err := t.WatchPrefix(ctx, r.Prefix, r.Opts)
	if err != nil {
		return err
	}
	if out, ok := resp.(*<-chan transport.WatchResponse); ok {
		*out = ch
	}
	return nil
}

func handleLeaseGrant(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*leaseGrantRequest)
	if !ok {
		return &InvalidRequestError{Method: "LeaseGrant"}
	}
	info, err := t.LeaseGrant(ctx, r.TTL)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.LeaseInfo); ok {
		*out = info
	}
	return nil
}

func handleLeaseRevoke(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*leaseRevokeRequest)
	if !ok {
		return &InvalidRequestError{Method: "LeaseRevoke"}
	}
	return t.LeaseRevoke(ctx, r.LeaseID)
}

func handleLeaseKeepAlive(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*leaseKeepAliveRequest)
	if !ok {
		return &InvalidRequestError{Method: "LeaseKeepAlive"}
	}
	info, err := t.LeaseKeepAlive(ctx, r.LeaseID)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.LeaseInfo); ok {
		*out = info
	}
	return nil
}

func handleLeaseTimeToLive(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*leaseTimeToLiveRequest)
	if !ok {
		return &InvalidRequestError{Method: "LeaseTimeToLive"}
	}
	info, err := t.LeaseTimeToLive(ctx, r.LeaseID)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.LeaseInfo); ok {
		*out = info
	}
	return nil
}

func handleLeaseList(ctx context.Context, t *Transport, _, resp any) error {
	leases, err := t.LeaseList(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(*[]*transport.LeaseInfo); ok {
		*out = leases
	}
	return nil
}

func handleTSWrite(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.TSDataPoint)
	if !ok {
		return &InvalidRequestError{Method: "TSWrite"}
	}
	return t.TSWrite(ctx, r)
}

func handleTSWriteBatch(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*tsWriteBatchRequest)
	if !ok {
		return &InvalidRequestError{Method: "TSWriteBatch"}
	}
	return t.TSWriteBatch(ctx, r.Points)
}

func handleTSQuery(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TSQueryRequest)
	if !ok {
		return &InvalidRequestError{Method: "TSQuery"}
	}
	result, err := t.TSQuery(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TSQueryResponse); ok {
		*out = result
	}
	return nil
}

func handleTSAggregate(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TSAggregateRequest)
	if !ok {
		return &InvalidRequestError{Method: "TSAggregate"}
	}
	result, err := t.TSAggregate(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TSAggregateResponse); ok {
		*out = result
	}
	return nil
}

func handleTSWritePoint(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.TSDataPoint)
	if !ok {
		return &InvalidRequestError{Method: "TSWritePoint"}
	}
	return t.TSWritePoint(ctx, r)
}

func handleTSRichQuery(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TSRichQueryRequest)
	if !ok {
		return &InvalidRequestError{Method: "TSRichQuery"}
	}
	result, err := t.TSRichQuery(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TSRichQueryResponse); ok {
		*out = result
	}
	return nil
}

func handleTSDeleteSeries(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TSDeleteRequest)
	if !ok {
		return &InvalidRequestError{Method: "TSDeleteSeries"}
	}
	result, err := t.TSDeleteSeries(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TSDeleteResponse); ok {
		*out = result
	}
	return nil
}

func handleTSGetRetention(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TSGetRetentionRequest)
	if !ok {
		return &InvalidRequestError{Method: "TSGetRetention"}
	}
	result, err := t.TSGetRetention(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TSGetRetentionResponse); ok {
		*out = result
	}
	return nil
}

func handleTSSetRetention(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.TSSetRetentionRequest)
	if !ok {
		return &InvalidRequestError{Method: "TSSetRetention"}
	}
	return t.TSSetRetention(ctx, r)
}

func handleTSListMetrics(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TSListMetricsRequest)
	if !ok {
		return &InvalidRequestError{Method: "TSListMetrics"}
	}
	result, err := t.TSListMetrics(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TSListMetricsResponse); ok {
		*out = result
	}
	return nil
}

func handleCreateNamespace(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.CreateNamespaceRequest)
	if !ok {
		return &InvalidRequestError{Method: "CreateNamespace"}
	}
	result, err := t.CreateNamespace(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CreateNamespaceResponse); ok {
		*out = result
	}
	return nil
}

func handleDeleteNamespace(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.DeleteNamespaceRequest)
	if !ok {
		return &InvalidRequestError{Method: "DeleteNamespace"}
	}
	result, err := t.DeleteNamespace(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.DeleteNamespaceResponse); ok {
		*out = result
	}
	return nil
}

func handleListNamespaces(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.ListNamespacesRequest)
	if !ok {
		return &InvalidRequestError{Method: "ListNamespaces"}
	}
	result, err := t.ListNamespaces(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.ListNamespacesResponse); ok {
		*out = result
	}
	return nil
}

func handleGetNamespaceInfo(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.GetNamespaceInfoRequest)
	if !ok {
		return &InvalidRequestError{Method: "GetNamespaceInfo"}
	}
	result, err := t.GetNamespaceInfo(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.GetNamespaceInfoResponse); ok {
		*out = result
	}
	return nil
}

func handleUpsertVector(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.UpsertVectorRequest)
	if !ok {
		return &InvalidRequestError{Method: "UpsertVector"}
	}
	result, err := t.UpsertVector(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.UpsertVectorResponse); ok {
		*out = result
	}
	return nil
}

func handleGetVector(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.GetVectorRequest)
	if !ok {
		return &InvalidRequestError{Method: "GetVector"}
	}
	result, err := t.GetVector(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.GetVectorResponse); ok {
		*out = result
	}
	return nil
}

func handleDeleteVector(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.DeleteVectorRequest)
	if !ok {
		return &InvalidRequestError{Method: "DeleteVector"}
	}
	result, err := t.DeleteVector(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.DeleteVectorResponse); ok {
		*out = result
	}
	return nil
}

func handleBatchUpsert(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.BatchUpsertRequest)
	if !ok {
		return &InvalidRequestError{Method: "BatchUpsert"}
	}
	result, err := t.BatchUpsert(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BatchUpsertResponse); ok {
		*out = result
	}
	return nil
}

func handleBatchDeleteVectors(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.BatchDeleteVectorsRequest)
	if !ok {
		return &InvalidRequestError{Method: "BatchDeleteVectors"}
	}
	result, err := t.BatchDeleteVectors(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.BatchDeleteVectorsResponse); ok {
		*out = result
	}
	return nil
}

func handleListVectors(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.ListVectorsRequest)
	if !ok {
		return &InvalidRequestError{Method: "ListVectors"}
	}
	result, err := t.ListVectors(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.ListVectorsResponse); ok {
		*out = result
	}
	return nil
}

func handleUpdateMetadata(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.UpdateVectorMetadataRequest)
	if !ok {
		return &InvalidRequestError{Method: "UpdateMetadata"}
	}
	result, err := t.UpdateMetadata(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.UpdateVectorMetadataResponse); ok {
		*out = result
	}
	return nil
}

func handleSearchSimilar(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.SearchSimilarRequest)
	if !ok {
		return &InvalidRequestError{Method: "SearchSimilar"}
	}
	result, err := t.SearchSimilar(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.SearchSimilarResponse); ok {
		*out = result
	}
	return nil
}

func handleSearchByMetadata(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.SearchByMetadataRequest)
	if !ok {
		return &InvalidRequestError{Method: "SearchByMetadata"}
	}
	result, err := t.SearchByMetadata(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.SearchByMetadataResponse); ok {
		*out = result
	}
	return nil
}

func handleHybridSearch(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.HybridSearchRequest)
	if !ok {
		return &InvalidRequestError{Method: "HybridSearch"}
	}
	result, err := t.HybridSearch(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.HybridSearchResponse); ok {
		*out = result
	}
	return nil
}

func handleSearchByID(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.SearchByIDRequest)
	if !ok {
		return &InvalidRequestError{Method: "SearchByID"}
	}
	result, err := t.SearchByID(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.SearchByIDResponse); ok {
		*out = result
	}
	return nil
}

func handleRebuildIndex(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.RebuildIndexRequest)
	if !ok {
		return &InvalidRequestError{Method: "RebuildIndex"}
	}
	result, err := t.RebuildIndex(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.RebuildIndexResponse); ok {
		*out = result
	}
	return nil
}

func handleGetRebuildProgress(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.GetRebuildProgressRequest)
	if !ok {
		return &InvalidRequestError{Method: "GetRebuildProgress"}
	}
	result, err := t.GetRebuildProgress(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.GetRebuildProgressResponse); ok {
		*out = result
	}
	return nil
}

func handleGetIndexStats(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.GetIndexStatsRequest)
	if !ok {
		return &InvalidRequestError{Method: "GetIndexStats"}
	}
	result, err := t.GetIndexStats(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.GetIndexStatsResponse); ok {
		*out = result
	}
	return nil
}

func handleCompactNamespace(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.CompactNamespaceRequest)
	if !ok {
		return &InvalidRequestError{Method: "CompactNamespace"}
	}
	result, err := t.CompactNamespace(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CompactNamespaceResponse); ok {
		*out = result
	}
	return nil
}

func handleGetCompactionProgress(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.GetCompactionProgressRequest)
	if !ok {
		return &InvalidRequestError{Method: "GetCompactionProgress"}
	}
	result, err := t.GetCompactionProgress(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.GetCompactionProgressResponse); ok {
		*out = result
	}
	return nil
}

// ---------------------------------------------------------------------------
// Generic request types for Request dispatch
// ---------------------------------------------------------------------------

type putRequest struct {
	Key   string
	Value []byte
}

type getRequest struct {
	Key string
}

type deleteRequest struct {
	Key string
}

type listRequest struct {
	Prefix string
}

type putWithLeaseRequest struct {
	Key     string
	Value   []byte
	LeaseID int64
}

type getRevisionRequest struct {
	Key string
}

type getAtRevisionRequest struct {
	Key      string
	Revision int64
}

type rangeRequest struct {
	Start []byte
	End   []byte
	Opts  transport.RangeOptions
}

type compactRequest struct {
	Revision int64
}

type watchRequest struct {
	Key  string
	Opts transport.WatchOptions
}

type watchPrefixRequest struct {
	Prefix string
	Opts   transport.WatchOptions
}

type leaseGrantRequest struct {
	TTL int64
}

type leaseRevokeRequest struct {
	LeaseID int64
}

type leaseKeepAliveRequest struct {
	LeaseID int64
}

type leaseTimeToLiveRequest struct {
	LeaseID int64
}

type tsWriteBatchRequest struct {
	Points []transport.TSDataPoint
}
