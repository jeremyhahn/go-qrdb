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

// Package rest provides the HTTP/2 and HTTP/3 REST API server for QRDB.
// It delegates all data-plane and control-plane operations to the service
// layer, handling only HTTP routing, serialization, leader forwarding,
// and protocol negotiation.
package rest

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// Host is the narrow interface the REST server requires for leader
// forwarding. It exposes only the Raft address needed to populate
// the X-Quicraft-Forwarded-By header. *quicraft.Host satisfies this
// interface.
type Host interface {
	RaftAddress() string
}

// ClusterKeyReceiver handles cluster peer key-transport endpoints. It is
// satisfied by *clusterkeys.Receiver (duck-typed; the interface lives here
// so the rest package does not import clusterkeys). When set, the server
// registers /v1/cluster/keydelivery (POST) and /v1/cluster/nodekey (GET).
// These routes are pre-exempt from the auth middleware (the receiver
// self-authorises via node cert + placement check) and from the sealed-window
// middleware (key transport must work before the tenant barrier is unsealed).
type ClusterKeyReceiver interface {
	// Handler returns the http.HandlerFunc for POST /v1/cluster/keydelivery.
	Handler() http.HandlerFunc
	// NodeKeyHandler returns the http.HandlerFunc for GET /v1/cluster/nodekey.
	NodeKeyHandler() http.HandlerFunc
}

// LeaderAdminResolver resolves the admin HTTP base URL for a given Raft
// address. When the leader's Raft address is known from a NotLeaderError,
// this resolver returns the corresponding admin HTTP URL (e.g.,
// "https://127.0.0.1:63002") so the request can be forwarded.
type LeaderAdminResolver interface {
	// ResolveAdminURL returns the admin HTTP base URL for the given Raft
	// address. Returns empty string if the address cannot be resolved.
	ResolveAdminURL(raftAddr string) string

	// Register records the mapping from a Raft address to its admin HTTP
	// base URL. Safe for concurrent use.
	Register(raftAddr, adminURL string)
}

// route pairs an HTTP method with its handler function.
type route struct {
	method  string
	handler func(w http.ResponseWriter, r *http.Request)
}

// Server is the HTTP/2+HTTP/3 REST server for QRDB. It routes requests
// to the service layer using map-based dispatch for O(1) routing.
type Server struct {
	kvSvc             service.KVServicer
	adminSvc          service.AdminServicer
	mvccSvc           service.MVCCKVServicer
	txSvc             service.TransactionServicer
	batchSvc          service.BatchServicer
	watchSvc          service.WatchServicer
	leaseSvc          service.LeaseServicer
	indexSvc          service.IndexServicer
	tsSvc             service.TimeSeriesServicer
	vectorSvc         service.VectorServicer
	embeddingSvc      service.EmbeddingServicer
	ragSvc            service.RAGServicer
	databaseSvc       service.DatabaseServicer
	sqlSvc            service.SQLServicer
	collectionSvc     service.CollectionServicer
	lockSvc           service.LockServicer
	barrierSvc        service.BarrierServicer
	encMigrator       service.EncryptionMigrator
	pinBackend        pin.PINBackend
	ceremonySvc       service.CeremonyServicer
	credentialSvc     service.CredentialServicer
	ceremonyCA        ceremony.CeremonyCA
	userSvc           user.UserServicer
	roleSvc           role.RoleServicer
	tenantSvc         tenant.TenantServicer
	backupSvc         service.BackupServicer
	rebalanceSvc      service.RebalanceServicer
	authFactorSvc     service.AuthFactorServicer
	userAuthFactorSvc service.UserAuthFactorServicer
	certProv          cert.CertProvider
	authMW            *authmw.AuthMiddleware
	corsConfig        *CORSConfig
	gatherer          prometheus.Gatherer
	resolver          LeaderAdminResolver
	forwardClient     *http.Client

	// forwardSigner is this node's own private key, used to sign the
	// on-behalf CallerAssertion attached to forwarded write requests. It is
	// derived from the node's TLS identity certificate (the same certificate
	// forwardClient presents as its mTLS client certificate), so the leader
	// can verify the assertion against the presenting peer certificate.
	forwardSigner crypto.Signer

	// forwardNodeID is this node's SPIFFE node id, extracted from its own
	// identity certificate's node-scoped SPIFFE SAN. It is stamped into the
	// on-behalf assertion and re-checked by the leader against the mTLS peer
	// certificate. Empty when the node certificate carries no node SAN, in
	// which case on-behalf assertions are not emitted.
	forwardNodeID   string
	addr            string
	server          *http.Server
	listener        net.Listener
	routes          map[string]route
	tlsConfig       *tls.Config
	tlsErr          error
	caCertPool      *x509.CertPool
	http3Server     *http3.Server
	host            Host
	caBootstrapCert []byte // PEM-encoded CA cert for bootstrap endpoint
	caBootstrapKey  []byte // PEM-encoded CA private key for bootstrap endpoint
	onEtcdUpdated   func(dbName string, cfg *database.EtcdConfig) error
	webUI           http.Handler      // optional embedded web UI file server
	auditLogger     audit.AuditLogger // optional compliance-grade audit logger

	// custodianSvc handles custodian registration, listing, and revocation.
	custodianSvc custodianServicer

	// soPINVerifier handles SO PIN verification for dual-factor auth.
	soPINVerifier soPINVerifier

	// soDualFactor enables the SO dual-factor middleware
	// (authmw.SOMiddleware) on SO-gated routes. Installed only when
	// both this flag is set (auth.so_dual_factor) and soPINVerifier is
	// wired; otherwise the chain is unchanged.
	soDualFactor bool

	// serviceRegistry provides per-(tenant, database) service resolution.
	// When nil, all handlers fall back to the singleton service fields above,
	// preserving backward compatibility for single-DB/single-tenant deployments.
	serviceRegistry ServiceRegistry

	// sealedWindowProbe controls the sealed-window middleware. When set
	// and IsSealed() returns true, every request whose path is not on
	// SealedWindowAllowlist is rejected with 503. When nil, the
	// middleware is not installed and the server behaves as before.
	sealedWindowProbe sealedWindowProbe

	// clusterKeyReceiver handles the cluster peer key-delivery endpoints
	// (/v1/cluster/keydelivery and /v1/cluster/nodekey). When nil these
	// routes are not registered.
	clusterKeyReceiver ClusterKeyReceiver
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithGatherer attaches a Prometheus Gatherer for the /metrics endpoint.
// When nil, the /metrics endpoint is not registered.
func WithGatherer(g prometheus.Gatherer) ServerOption {
	return func(s *Server) {
		s.gatherer = g
	}
}

// WithLeaderForwarding enables transparent leader forwarding for write
// operations (PUT, DELETE). When a write returns NotLeaderError, the
// server uses the resolver to find the leader's admin HTTP URL and
// proxies the request there.
func WithLeaderForwarding(resolver LeaderAdminResolver) ServerOption {
	return func(s *Server) {
		s.resolver = resolver
	}
}

// WithTLS configures the server with TLS for HTTP/2 (via ALPN "h2")
// and starts an HTTP/3 server on the same port via QUIC. The config is
// cloned and pinned to a TLS 1.3 floor: an unset MinVersion is raised to
// TLS 1.3, and an explicitly weaker MinVersion is rejected (surfaced by
// Start as *WeakTLSVersionError) rather than silently negotiating a weaker
// handshake. This mirrors the gRPC and unix transports so the "TLS 1.3
// only" posture holds on every transport, not just the mTLS ones. A nil cfg
// leaves TLS unconfigured; because the transport is TLS-only, Start then
// fails closed with *TLSNotConfiguredError.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		if cfg == nil {
			return
		}
		clone := cfg.Clone()
		switch {
		case clone.MinVersion == 0:
			clone.MinVersion = tls.VersionTLS13
		case clone.MinVersion < tls.VersionTLS13:
			s.tlsErr = &WeakTLSVersionError{MinVersion: clone.MinVersion}
			return
		}
		s.tlsConfig = clone
	}
}

// WithCACertPool sets the CA certificate pool used by the forwarding
// HTTP client to verify peer TLS certificates. This replaces
// InsecureSkipVerify with proper CA-based verification.
func WithCACertPool(pool *x509.CertPool) ServerOption {
	return func(s *Server) {
		s.caCertPool = pool
	}
}

// WithHost sets the Host interface for leader forwarding. The host
// provides the local Raft address for the X-Quicraft-Forwarded-By header.
func WithHost(h Host) ServerOption {
	return func(s *Server) {
		s.host = h
	}
}

// WithMVCC attaches an MVCCKVServicer for MVCC-aware key-value operations
// including revision reads, range queries, and compaction.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(s *Server) {
		s.mvccSvc = svc
	}
}

// WithTransaction attaches a TransactionServicer for atomic compare-and-swap
// transaction operations.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(s *Server) {
		s.txSvc = svc
	}
}

// WithBatch attaches a BatchServicer for atomic multi-operation batch
// execution.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(s *Server) {
		s.batchSvc = svc
	}
}

// WithWatch attaches a WatchServicer for real-time change notification
// streaming via Server-Sent Events.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(s *Server) {
		s.watchSvc = svc
	}
}

// WithLease attaches a LeaseServicer for distributed time-to-live lease
// management.
func WithLease(svc service.LeaseServicer) ServerOption {
	return func(s *Server) {
		s.leaseSvc = svc
	}
}

// WithLock attaches a LockServicer for distributed lock management
// operations including blocking acquisition, try-lock, release, extend,
// status query, and list.
func WithLock(svc service.LockServicer) ServerOption {
	return func(s *Server) {
		s.lockSvc = svc
	}
}

// WithIndex attaches an IndexServicer for secondary index operations.
func WithIndex(svc service.IndexServicer) ServerOption {
	return func(s *Server) {
		s.indexSvc = svc
	}
}

// WithTimeSeries attaches a TimeSeriesServicer for time-series data
// storage and aggregation operations.
func WithTimeSeries(svc service.TimeSeriesServicer) ServerOption {
	return func(s *Server) {
		s.tsSvc = svc
	}
}

// WithVector attaches a VectorServicer for vector database operations
// including namespace management, vector CRUD, similarity search, and
// index maintenance.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(s *Server) {
		s.vectorSvc = svc
	}
}

// WithEmbedding attaches an EmbeddingServicer for text embedding operations
// including single embed, batch embed, and model listing.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(s *Server) { s.embeddingSvc = svc }
}

// WithRAG attaches a RAGServicer for Retrieval-Augmented Generation operations
// including document ingestion, semantic retrieval, deletion, and listing.
func WithRAG(svc service.RAGServicer) ServerOption {
	return func(s *Server) { s.ragSvc = svc }
}

// WithDatabaseService attaches a DatabaseServicer for database management
// operations including create, get, list, and drop.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) { s.databaseSvc = svc }
}

// WithSQLService attaches a SQLServicer for ad-hoc SQL query execution.
// When set, the POST /sql/query route (and its /v1/sql/query mount) is
// registered.
func WithSQLService(svc service.SQLServicer) ServerOption {
	return func(s *Server) { s.sqlSvc = svc }
}

// WithOnEtcdUpdated registers a callback invoked after the etcd
// configuration for a database is updated. The orchestrator typically
// uses this to start or restart the etcd protocol server.
func WithOnEtcdUpdated(fn func(dbName string, cfg *database.EtcdConfig) error) ServerOption {
	return func(s *Server) { s.onEtcdUpdated = fn }
}

// WithCollectionService attaches a CollectionServicer for document
// collection management operations including collection CRUD and
// document CRUD with pagination.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) { s.collectionSvc = svc }
}

// WithBarrierService attaches a BarrierServicer for barrier lifecycle
// operations including initialization, seal/unseal, Shamir share management,
// rekey, recovery, and root token generation.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(s *Server) { s.barrierSvc = svc }
}

// WithEncryptionMigrator configures the enable-encryption migrator, enabling
// POST /v1/barrier/enable-encryption. When nil that route is not registered.
func WithEncryptionMigrator(m service.EncryptionMigrator) ServerOption {
	return func(s *Server) { s.encMigrator = m }
}

// WithPINBackend attaches the canonical pin.PINBackend used for the
// optional SO PIN / User PIN compliance-gating layer on barrier
// administrative operations (initialize/rekey/seal). When nil, PIN
// fields supplied in barrier requests are ignored — preserving
// dev-mode and unconfigured-deployment flows.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	return func(s *Server) { s.pinBackend = backend }
}

// WithCeremonyService attaches a CeremonyServicer for ceremony lifecycle
// operations including initialization, certificate claiming, share
// distribution, CSR signing, and status queries.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(s *Server) { s.ceremonySvc = svc }
}

// WithCredentialService attaches a CredentialServicer for credential
// management operations including storage, submission, and deletion.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(s *Server) { s.credentialSvc = svc }
}

// WithCeremonyCA sets the CeremonyCA used by the ceremony initialization
// handler. The CA cannot be serialized over HTTP, so it must be injected
// at server creation time and is held as a server-side reference.
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption {
	return func(s *Server) { s.ceremonyCA = ca }
}

// WithUserService attaches a UserServicer for user management operations.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(s *Server) { s.userSvc = svc }
}

// WithRoleService attaches a RoleServicer for role management operations.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(s *Server) { s.roleSvc = svc }
}

// WithTenantService attaches a TenantServicer for tenant lifecycle management
// operations including create, get, list, delete, and quota updates.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantSvc = svc }
}

// WithBackupService attaches a BackupServicer for backup lifecycle operations
// including create, list, get, delete, restore, and verify.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(s *Server) { s.backupSvc = svc }
}

// WithRebalanceService attaches a RebalanceServicer for cluster rebalance
// operations including shard statistics, plan generation, execution, and
// imbalance reporting.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(s *Server) { s.rebalanceSvc = svc }
}

// WithAuthFactorService attaches an AuthFactorServicer for custodian auth
// factor management (enroll, challenge, verify, list, revoke).
// Handlers return 501 when this service is nil.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(s *Server) { s.authFactorSvc = svc }
}

// WithUserAuthFactorService attaches a UserAuthFactorServicer for the
// user-side 1-to-many auth factor model (M14). Handlers return 501 when
// this service is nil.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) ServerOption {
	return func(s *Server) { s.userAuthFactorSvc = svc }
}

// WithCustodianService attaches a custodianServicer for custodian lifecycle
// management (register, list, revoke). Handlers return 501 when nil.
func WithCustodianService(svc custodianServicer) ServerOption {
	return func(s *Server) { s.custodianSvc = svc }
}

// WithSOAuthenticator attaches an soPINVerifier for SO dual-factor PIN
// verification on SO-permissioned routes.
func WithSOAuthenticator(verifier soPINVerifier) ServerOption {
	return func(s *Server) { s.soPINVerifier = verifier }
}

// WithSODualFactor enables Security Officer dual-factor enforcement:
// routes mapped to SO-gated permissions additionally require a valid
// in-band SO PIN via the X-QRDB-SO-PIN header. The middleware is only
// installed when an SO authenticator is also wired via
// WithSOAuthenticator. Driven by the auth.so_dual_factor config flag.
func WithSODualFactor() ServerOption {
	return func(s *Server) { s.soDualFactor = true }
}

// WithCertProvider attaches a CertProvider for certificate management
// operations.
func WithCertProvider(svc cert.CertProvider) ServerOption {
	return func(s *Server) { s.certProv = svc }
}

// WithAuthMiddleware attaches authentication and authorization middleware
// that wraps the HTTP handler for all REST endpoints.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) { s.authMW = mw }
}

// WithCORS enables Cross-Origin Resource Sharing for browser-based access.
// The CORS middleware is the outermost handler wrapper so that preflight
// OPTIONS requests are handled before authentication.
func WithCORS(cfg CORSConfig) ServerOption {
	return func(s *Server) { s.corsConfig = &cfg }
}

// WithCABootstrap enables the /v1/ca/bootstrap endpoint that serves the
// cluster CA certificate bundle for joining nodes. The endpoint is public
// (no authentication required) so that nodes without TLS credentials can
// bootstrap their trust store.
func WithCABootstrap(certPEM, keyPEM []byte) ServerOption {
	return func(s *Server) {
		s.caBootstrapCert = certPEM
		s.caBootstrapKey = keyPEM
	}
}

// WithKVService attaches a KVServicer for key-value operations. This is an
// alternative to passing kvSvc as a positional parameter and is useful when
// constructing a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(s *Server) { s.kvSvc = svc }
}

// WithAdminService attaches an AdminServicer for cluster administration
// operations. This is an alternative to passing adminSvc as a positional
// parameter and is useful when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(s *Server) { s.adminSvc = svc }
}

// WithWebUI attaches an http.Handler that serves embedded web UI static
// files. When set, any request that does not match an API route is forwarded
// to this handler. The handler should serve index.html for SPA fallback.
func WithWebUI(h http.Handler) ServerOption {
	return func(s *Server) { s.webUI = h }
}

// WithAuditLogger attaches a compliance-grade AuditLogger to the REST server.
// When set, GET /v1/audit/events is registered and delegates to the logger.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
}

// WithServiceRegistry attaches a ServiceRegistry for per-(tenant, database)
// request scoping. When set, every data-plane handler resolves its service
// via the registry rather than the singleton service fields. Backward compat:
// when nil, handlers use the singleton fields (single-DB/single-tenant mode).
func WithServiceRegistry(reg ServiceRegistry) ServerOption {
	return func(s *Server) { s.serviceRegistry = reg }
}

// WithSealedWindowProbe installs the sealed-window middleware backed by
// the supplied probe. While probe.IsSealed() returns true, every request
// whose path is not on SealedWindowAllowlist is rejected with 503.
// Once unsealed the middleware is a passthrough; on re-seal it engages
// again.
//
// The middleware is wrapped just outside the per-request mux but inside
// any auth middleware, so allowlisted endpoints (notably /v1/barrier/*)
// remain reachable without authentication during the sealed window —
// the Vault model where possession of the unseal credential is itself
// the authorization to drive unseal.
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) { s.sealedWindowProbe = probe }
}

// WithClusterKeyReceiver registers the cluster peer key-delivery routes when
// set. The receiver handles /v1/cluster/keydelivery (POST) for incoming HPKE
// envelopes and /v1/cluster/nodekey (GET) for certificate advertisement. Both
// routes are pre-exempt from the auth middleware and the sealed-window
// middleware so they work in every daemon state. When nil, the routes are not
// registered and peer key transport is disabled on this node.
func WithClusterKeyReceiver(rcv ClusterKeyReceiver) ServerOption {
	return func(s *Server) { s.clusterKeyReceiver = rcv }
}

// NewServer creates a new REST server that listens on the given address
// and delegates to the provided service implementations. kvSvc and adminSvc
// may be nil when only non-KV services (barrier, cert, auth) are needed;
// their routes are registered only when the respective service is non-nil.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
	}

	for _, opt := range opts {
		opt(s)
	}

	// Initialize the forwarding HTTP client when a resolver is configured.
	if s.resolver != nil {
		transport := &http.Transport{}
		if s.tlsConfig != nil {
			transport.TLSClientConfig = &tls.Config{
				RootCAs: s.caCertPool,
				// Present this node's own listener identity (the same
				// cert/key mesh identity clustertls issues every node --
				// "self-signed cluster CA verifies both roles", see
				// tlsBundleFromMaterial) as the CLIENT certificate for
				// forwarded requests. Every REST listener in an mTLS
				// cluster requires a peer certificate on every connection
				// (pkg/auth/middleware/rest.go, ErrNoPeerCertificate) --
				// without this, forwardToLeader's proxied request is
				// rejected with 401 no_peer_cert before it ever reaches
				// the leader's handler, silently breaking cross-node
				// leader forwarding (KV PUT/DELETE and database scale
				// alike) in exactly the mTLS-secured topology this
				// mechanism exists for.
				Certificates:         s.tlsConfig.Certificates,
				GetClientCertificate: s.tlsConfig.GetClientCertificate,
				MinVersion:           tls.VersionTLS13,
			}
			transport.ForceAttemptHTTP2 = true
		}
		s.forwardClient = &http.Client{
			Timeout:   25 * time.Second,
			Transport: transport,
		}

		// Derive the on-behalf assertion signer and this node's SPIFFE node id
		// from the node's own identity certificate (the same certificate
		// forwardClient presents as its mTLS client certificate). This lets a
		// forwarded write carry a signed assertion of the ORIGINAL caller's
		// identity that the leader verifies against the presenting peer
		// certificate, so RBAC re-authorizes as the caller rather than the
		// forwarding node. When no identity certificate or node SAN is present
		// (e.g. auth-disabled deployments), the signer/nodeID stay unset and
		// no assertion is emitted -- behavior is unchanged.
		s.forwardSigner, s.forwardNodeID = deriveForwardIdentity(s.tlsConfig)
	}

	// Map-based dispatch for O(1) routing with method enforcement.
	s.routes = map[string]route{}

	// Server configuration routes (always available).
	s.routes["/v1/config/initialized"] = route{method: http.MethodGet, handler: s.handleConfigInitialized}

	// Admin routes are registered only when an admin service is provided.
	if s.adminSvc != nil {
		s.routes["/status"] = route{method: http.MethodGet, handler: s.handleStatus}
		s.routes["/peers"] = route{method: http.MethodGet, handler: s.handlePeers}
		s.routes["/health"] = route{method: http.MethodGet, handler: s.handleHealth}
		s.routes["/drain"] = route{method: http.MethodPost, handler: s.handleDrain}
		s.routes["/capabilities"] = route{method: http.MethodGet, handler: s.handleCapabilities}
		s.routes["/membership/add"] = route{method: http.MethodPost, handler: s.handleMembershipAdd}
		s.routes["/membership/remove"] = route{method: http.MethodPost, handler: s.handleMembershipRemove}
		s.routes["/snapshot"] = route{method: http.MethodPost, handler: s.handleSnapshot}
		s.routes["/admin/shards"] = route{method: http.MethodPost, handler: s.handleStartShard}
		s.routes["/admin/topology"] = route{method: http.MethodGet, handler: s.handleGetTopology}
	}

	// Register /metrics when a Prometheus gatherer is provided.
	if s.gatherer != nil {
		s.routes["/metrics"] = route{
			method:  http.MethodGet,
			handler: s.handleMetrics,
		}
	}

	// MVCC routes: range/compact/revision self-guard on mvccSvc inside the
	// handler, so they are registered unconditionally. The KV scan route
	// calls kvSvc directly and is registered only when kvSvc is provided.
	s.routes["/kv/range"] = route{method: http.MethodGet, handler: s.handleRange}
	s.routes["/kv/compact"] = route{method: http.MethodPost, handler: s.handleCompact}
	s.routes["/kv/revision"] = route{method: http.MethodGet, handler: s.handleCurrentRevision}
	s.routes["/kv/restore"] = route{method: http.MethodPost, handler: s.handleRestoreToRevision}
	if s.kvSvc != nil {
		s.routes["/kv/scan"] = route{method: http.MethodGet, handler: s.handleKVScan}
	}

	// Transaction route.
	s.routes["/tx"] = route{method: http.MethodPost, handler: s.handleTransaction}

	// Batch routes.
	s.routes["/batch"] = route{method: http.MethodPost, handler: s.handleBatch}
	s.routes["/batch/import"] = route{method: http.MethodPost, handler: s.handleBatchImport}

	// Watch route (Server-Sent Events).
	s.routes["/watch"] = route{method: http.MethodGet, handler: s.handleWatch}

	// Lease routes: only registered when a lease service is wired. Without
	// gating, proposals would reach a SM instance that has no lease subsystem,
	// causing SM.Apply to fail and blocking all subsequent linearizable reads.
	if s.leaseSvc != nil || s.serviceRegistry != nil {
		s.routes["/lease/grant"] = route{method: http.MethodPost, handler: s.handleLeaseGrant}
		s.routes["/lease/revoke"] = route{method: http.MethodPost, handler: s.handleLeaseRevoke}
		s.routes["/lease/keepalive"] = route{method: http.MethodPost, handler: s.handleLeaseKeepAlive}
		s.routes["/lease/ttl"] = route{method: http.MethodGet, handler: s.handleLeaseTimeToLive}
		s.routes["/lease/list"] = route{method: http.MethodGet, handler: s.handleLeaseList}
	}

	// Lock routes: only registered when a lock service is wired.
	if s.lockSvc != nil || s.serviceRegistry != nil {
		s.routes["/lock/acquire"] = route{method: http.MethodPost, handler: s.handleLockAcquire}
		s.routes["/lock/trylock"] = route{method: http.MethodPost, handler: s.handleLockTryLock}
		s.routes["/lock/release"] = route{method: http.MethodPost, handler: s.handleLockRelease}
		s.routes["/lock/extend"] = route{method: http.MethodPost, handler: s.handleLockExtend}
		s.routes["/lock/status"] = route{method: http.MethodGet, handler: s.handleLockStatus}
		s.routes["/lock/list"] = route{method: http.MethodGet, handler: s.handleLockList}
	}

	// Index routes.
	s.routes["/index/create"] = route{method: http.MethodPost, handler: s.handleIndexCreate}
	s.routes["/index/drop"] = route{method: http.MethodPost, handler: s.handleIndexDrop}
	s.routes["/index/query"] = route{method: http.MethodGet, handler: s.handleIndexQuery}
	s.routes["/index/scan"] = route{method: http.MethodGet, handler: s.handleIndexScan}
	s.routes["/index/list"] = route{method: http.MethodGet, handler: s.handleIndexList}

	// TimeSeries routes.
	s.routes["/ts/write"] = route{method: http.MethodPost, handler: s.handleTSWrite}
	s.routes["/ts/write-batch"] = route{method: http.MethodPost, handler: s.handleTSWriteBatch}
	s.routes["/ts/write-point"] = route{method: http.MethodPost, handler: s.handleTSWritePoint}
	s.routes["/ts/query"] = route{method: http.MethodGet, handler: s.handleTSQuery}
	s.routes["/ts/query-rich"] = route{method: http.MethodPost, handler: s.handleTSRichQuery}
	s.routes["/ts/aggregate"] = route{method: http.MethodGet, handler: s.handleTSAggregate}
	s.routes["/ts/metrics"] = route{method: http.MethodGet, handler: s.handleTSListMetrics}

	// Vector routes.
	s.routes["/vector/namespace"] = route{method: http.MethodPost, handler: s.handleVectorCreateNamespace}
	s.routes["/vector/namespace/delete"] = route{method: http.MethodPost, handler: s.handleVectorDeleteNamespace}
	s.routes["/vector/namespaces"] = route{method: http.MethodGet, handler: s.handleVectorListNamespaces}
	s.routes["/vector/namespace/info"] = route{method: http.MethodGet, handler: s.handleVectorGetNamespaceInfo}
	s.routes["/vector/upsert"] = route{method: http.MethodPost, handler: s.handleVectorUpsert}
	s.routes["/vector/get"] = route{method: http.MethodGet, handler: s.handleVectorGet}
	s.routes["/vector/delete"] = route{method: http.MethodPost, handler: s.handleVectorDelete}
	s.routes["/vector/batch/upsert"] = route{method: http.MethodPost, handler: s.handleVectorBatchUpsert}
	s.routes["/vector/batch/delete"] = route{method: http.MethodPost, handler: s.handleVectorBatchDeleteVectors}
	s.routes["/vector/list"] = route{method: http.MethodGet, handler: s.handleVectorListVectors}
	s.routes["/vector/metadata"] = route{method: http.MethodPost, handler: s.handleVectorUpdateMetadata}
	s.routes["/vector/search/similar"] = route{method: http.MethodPost, handler: s.handleVectorSearchSimilar}
	s.routes["/vector/search/metadata"] = route{method: http.MethodPost, handler: s.handleVectorSearchByMetadata}
	s.routes["/vector/search/hybrid"] = route{method: http.MethodPost, handler: s.handleVectorHybridSearch}
	s.routes["/vector/search/by-id"] = route{method: http.MethodPost, handler: s.handleVectorSearchByID}
	s.routes["/vector/index/rebuild"] = route{method: http.MethodPost, handler: s.handleVectorRebuildIndex}
	s.routes["/vector/index/rebuild/progress"] = route{method: http.MethodGet, handler: s.handleVectorGetRebuildProgress}
	s.routes["/vector/index/stats"] = route{method: http.MethodGet, handler: s.handleVectorGetIndexStats}
	s.routes["/vector/compact"] = route{method: http.MethodPost, handler: s.handleVectorCompactNamespace}
	s.routes["/vector/compact/progress"] = route{method: http.MethodGet, handler: s.handleVectorGetCompactionProgress}

	// Embedding routes.
	if s.embeddingSvc != nil {
		s.routes["/v1/embeddings"] = route{method: http.MethodPost, handler: s.handleEmbedText}
		s.routes["/v1/embeddings/batch"] = route{method: http.MethodPost, handler: s.handleBatchEmbed}
		s.routes["/v1/embedding/models"] = route{method: http.MethodGet, handler: s.handleListEmbeddingModels}
	}

	// RAG routes.
	if s.ragSvc != nil {
		s.routes["/v1/rag/ingest"] = route{method: http.MethodPost, handler: s.handleRAGIngest}
		s.routes["/v1/rag/query"] = route{method: http.MethodPost, handler: s.handleRAGQuery}
		s.routes["/v1/rag/documents/delete"] = route{method: http.MethodPost, handler: s.handleRAGDeleteDocument}
		s.routes["/v1/rag/documents"] = route{method: http.MethodGet, handler: s.handleRAGListDocuments}
	}

	// Database management routes.
	if s.databaseSvc != nil {
		s.routes["/v1/databases"] = route{method: http.MethodPost, handler: s.handleCreateDatabase}
		s.routes["/v1/databases/list"] = route{method: http.MethodGet, handler: s.handleListDatabases}
	}

	// Collection management routes.
	if s.collectionSvc != nil {
		s.routes["/v1/collections"] = route{method: http.MethodPost, handler: s.handleCreateCollection}
		s.routes["/v1/collections/list"] = route{method: http.MethodGet, handler: s.handleListCollections}
	}

	// SQL query routes. /sql/query is the canonical data-plane mount used
	// by the TypeScript SDK; /v1/sql/query is the mount used by the Go
	// database/sql driver. Both dispatch to the same handler.
	if s.sqlSvc != nil {
		s.routes["/sql/query"] = route{method: http.MethodPost, handler: s.handleSQLQuery}
		s.routes["/v1/sql/query"] = route{method: http.MethodPost, handler: s.handleSQLQuery}
	}

	// User management routes.
	if s.userSvc != nil {
		s.routes["/users"] = route{method: http.MethodPost, handler: s.handleCreateUser}
		s.routes["/users/list"] = route{method: http.MethodGet, handler: s.handleListUsers}
	}

	// Role management routes.
	if s.roleSvc != nil {
		s.routes["/roles"] = route{method: http.MethodPost, handler: s.handleCreateRole}
		s.routes["/roles/list"] = route{method: http.MethodGet, handler: s.handleListRoles}
		s.routes["/roles/by-name"] = route{method: http.MethodGet, handler: s.handleGetRoleByName}
		s.routes["/roles/resolve-permissions"] = route{method: http.MethodPost, handler: s.handleResolvePermissions}
		s.routes["/roles/assign"] = route{method: http.MethodPost, handler: s.handleAssignRole}
		s.routes["/roles/unassign"] = route{method: http.MethodPost, handler: s.handleUnassignRole}
		s.routes["/permissions"] = route{method: http.MethodGet, handler: s.handleListPermissions}
	}

	// Tenant management routes. Registered unconditionally so the router
	// always returns a meaningful status (201/200/501) rather than 404.
	// Handlers return 501 when tenantSvc is nil (seal barrier not configured).
	s.routes["/v1/tenants"] = route{method: http.MethodPost, handler: s.handleCreateTenant}
	s.routes["/v1/tenants/list"] = route{method: http.MethodGet, handler: s.handleListTenants}

	// Backup management routes.
	if s.backupSvc != nil {
		s.routes["/v1/backups"] = route{method: http.MethodPost, handler: s.handleCreateBackup}
		s.routes["/v1/backups/list"] = route{method: http.MethodGet, handler: s.handleListBackups}
		s.routes["/v1/backups/restore"] = route{method: http.MethodPost, handler: s.handleRestoreBackup}
	}

	// Cluster rebalance routes.
	if s.rebalanceSvc != nil {
		s.routes["/v1/rebalance/plan"] = route{method: http.MethodPost, handler: s.handleRebalancePlan}
		s.routes["/v1/rebalance/execute"] = route{method: http.MethodPost, handler: s.handleRebalanceExecute}
		s.routes["/v1/rebalance/report"] = route{method: http.MethodGet, handler: s.handleImbalanceReport}
	}

	// Auth factor routes. Registered unconditionally; handlers return 501 when
	// authFactorSvc is nil (service not configured).
	s.routes["/v1/authfactors/list"] = route{method: http.MethodGet, handler: s.handleAuthFactorList}

	// User auth factor routes (M14). Registered unconditionally; handlers
	// return 501 when userAuthFactorSvc is nil.
	s.routes["/v1/users/factors"] = route{method: http.MethodPost, handler: s.handleUserAuthFactorAdd}
	s.routes["/v1/users/factors/list"] = route{method: http.MethodGet, handler: s.handleUserAuthFactorList}
	s.routes["/v1/users/factors/login"] = route{method: http.MethodPost, handler: s.handleUserAuthFactorLogin}
	s.routes["/v1/users/factors/rotate"] = route{method: http.MethodPost, handler: s.handleUserAuthFactorRotate}

	// Certificate management routes.
	if s.certProv != nil {
		s.routes["/certs/generate"] = route{method: http.MethodPost, handler: s.handleGenerateCert}
		s.routes["/certs/export"] = route{method: http.MethodPost, handler: s.handleExportCert}
		s.routes["/certs/revoke"] = route{method: http.MethodPost, handler: s.handleRevokeCert}
		s.routes["/certs/list"] = route{method: http.MethodGet, handler: s.handleListCerts}
	}

	// CA bootstrap endpoint (public, no auth required).
	if len(s.caBootstrapCert) > 0 {
		s.routes["/v1/ca/bootstrap"] = route{method: http.MethodGet, handler: s.handleCABootstrap}
	}

	// Cluster peer key-transport endpoints. Both routes are pre-exempt from the
	// auth middleware (/v1/cluster/ prefix in middleware/rest.go) and are on the
	// SealedWindowAllowlist (/v1/cluster/ prefix) so they work in every daemon
	// state. The receiver self-authorises via node-cert verification and
	// placement-table gate.
	if s.clusterKeyReceiver != nil {
		s.routes["/v1/cluster/keydelivery"] = route{method: http.MethodPost, handler: s.clusterKeyReceiver.Handler()}
		s.routes["/v1/cluster/nodekey"] = route{method: http.MethodGet, handler: s.clusterKeyReceiver.NodeKeyHandler()}
	}

	// Barrier lifecycle routes.
	if s.barrierSvc != nil {
		s.routes["/v1/barrier/initialize"] = route{method: http.MethodPost, handler: s.handleBarrierInitialize}
		s.routes["/v1/barrier/unseal"] = route{method: http.MethodPost, handler: s.handleBarrierUnseal}
		s.routes["/v1/barrier/seal"] = route{method: http.MethodPost, handler: s.handleBarrierSeal}
		s.routes["/v1/barrier/status"] = route{method: http.MethodGet, handler: s.handleBarrierStatus}
		s.routes["/v1/barrier/shamir/initialize"] = route{method: http.MethodPost, handler: s.handleBarrierShamirInitialize}
		s.routes["/v1/barrier/shamir/unseal-share"] = route{method: http.MethodPost, handler: s.handleBarrierShamirUnsealShare}
		s.routes["/v1/barrier/shamir/unseal-shares"] = route{method: http.MethodPost, handler: s.handleBarrierShamirUnsealShares}
		s.routes["/v1/barrier/shamir/seal-vote"] = route{method: http.MethodPost, handler: s.handleBarrierShamirSealVote}
		s.routes["/v1/barrier/rekey"] = route{method: http.MethodPost, handler: s.handleBarrierRekey}
		if s.encMigrator != nil {
			s.routes["/v1/barrier/enable-encryption"] = route{method: http.MethodPost, handler: s.handleBarrierEnableEncryption}
		}
		s.routes["/v1/barrier/recovery/generate"] = route{method: http.MethodPost, handler: s.handleBarrierRecoveryGenerate}
		s.routes["/v1/barrier/recovery/recover"] = route{method: http.MethodPost, handler: s.handleBarrierRecoveryRecover}
		s.routes["/v1/barrier/recovery"] = route{method: http.MethodDelete, handler: s.handleBarrierRecoveryDelete}
		s.routes["/v1/barrier/recovery/status"] = route{method: http.MethodGet, handler: s.handleBarrierRecoveryStatus}
		s.routes["/v1/barrier/root-token"] = route{method: http.MethodPost, handler: s.handleBarrierRootToken}
		// Security Officer static routes. GET and POST /v1/barrier/officers
		// share the same path; they are dispatched via handleBarrierOfficers
		// from dispatch() which performs method-based routing.
		s.routes["/v1/barrier/officers/claim-status"] = route{method: http.MethodGet, handler: s.handleBarrierOfficerClaimStatus}
		s.routes["/v1/barrier/recovery/officers"] = route{method: http.MethodPost, handler: s.handleBarrierRecoveryOfficerRegister}
		// Path-param claim routes (/{username}/claim/begin|complete) are
		// dispatched via handleBarrierOfficersByPath in dispatch() using the
		// /v1/barrier/officers/ and /v1/barrier/recovery/officers/ prefixes.
	}

	// Ceremony lifecycle routes.
	if s.ceremonySvc != nil {
		s.routes["/v1/ceremony/initialize"] = route{method: http.MethodPost, handler: s.handleCeremonyInitialize}
		s.routes["/v1/ceremony/status"] = route{method: http.MethodGet, handler: s.handleCeremonyStatus}
		s.routes["/v1/ceremony/claim-cert/begin"] = route{method: http.MethodPost, handler: s.handleCeremonyClaimCertBegin}
		s.routes["/v1/ceremony/claim-cert/complete"] = route{method: http.MethodPost, handler: s.handleCeremonyClaimCertComplete}
		s.routes["/v1/ceremony/officer-share/begin"] = route{method: http.MethodPost, handler: s.handleCeremonyBeginClaimShare}
		s.routes["/v1/ceremony/officer-share/complete"] = route{method: http.MethodPost, handler: s.handleCeremonyCompleteClaimShare}
		s.routes["/v1/ceremony/sign-csr"] = route{method: http.MethodPost, handler: s.handleCeremonySignCSR}
		s.routes["/v1/init/enroll-external-officer"] = route{method: http.MethodPost, handler: s.handleCeremonyEnrollExternalOfficer}
	}

	// Custodian lifecycle routes (Phase 4).
	if s.custodianSvc != nil {
		s.routes["/v1/ceremony/enroll-custodian"] = route{method: http.MethodPost, handler: s.handleEnrollCustodian}
		s.routes["/v1/ceremony/revoke-custodian"] = route{method: http.MethodPost, handler: s.handleRevokeCustodian}
		s.routes["/v1/ceremony/claim-share/begin"] = route{method: http.MethodPost, handler: s.handleCustodianClaimShareBegin}
		s.routes["/v1/ceremony/claim-share/complete"] = route{method: http.MethodPost, handler: s.handleCustodianClaimShareComplete}
		s.routes["/v1/barrier/unseal-share"] = route{method: http.MethodPost, handler: s.handleUnsealShare}
	}

	// Credential management routes.
	if s.credentialSvc != nil {
		s.routes["/v1/credentials/submit"] = route{method: http.MethodPost, handler: s.handleCredentialSubmit}
		s.routes["/v1/credentials/store"] = route{method: http.MethodPost, handler: s.handleCredentialStore}
		s.routes["/v1/credentials/strategy"] = route{method: http.MethodGet, handler: s.handleCredentialStrategy}
		s.routes["/v1/credentials"] = route{method: http.MethodGet, handler: s.handleCredentialList}
	}

	// Audit event retrieval route.
	// Authorization: requires "audit:query" permission.
	// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
	if s.auditLogger != nil {
		s.routes["/v1/audit/events"] = route{method: http.MethodGet, handler: s.handleAuditQuery}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.dispatch)

	// Build the middleware chain from innermost to outermost:
	//   mux → auth → sealed-window → scope → CORS
	// scopeMiddleware runs before auth so that auth middleware can read the
	// tenant/database from context if it needs to (future-proof). CORS is the
	// outermost layer so that preflight OPTIONS requests are handled before any
	// other processing. The sealed-window middleware sits between auth and
	// scope so that allowlisted endpoints (e.g. /v1/barrier/unseal) bypass
	// auth — possession of the unseal credential is the authorization, per
	// the Vault model.
	// The SO dual-factor middleware is the innermost wrapper (directly
	// around the mux) so it runs AFTER the auth middleware has resolved
	// the caller's identity into the request context. It gates only the
	// routes mapped to authmw.SOPermissions and is installed only when
	// explicitly enabled (auth.so_dual_factor) and an SO PIN verifier is
	// wired — default posture is unchanged.
	var handler http.Handler = mux
	if s.soDualFactor && s.soPINVerifier != nil {
		handler = authmw.SOMiddleware(s.soPINVerifier, s.auditLogger, slog.Default(), handler)
	}
	if s.authMW != nil {
		handler = s.authMW.RESTMiddleware(handler)
	}
	if s.sealedWindowProbe != nil {
		handler = SealedWindowMiddleware(s.sealedWindowProbe, nil)(handler)
	}
	handler = scopeMiddleware(handler)
	if s.corsConfig != nil {
		handler = corsMiddleware(*s.corsConfig, handler)
	}

	s.server = &http.Server{
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

// Start begins listening and serving HTTP requests in the background.
// When TLS is configured, the listener is wrapped with tls.NewListener
// for automatic HTTP/2 via ALPN "h2", and an HTTP/3 server is started
// on the same port via QUIC.
func (s *Server) Start() error {
	// A WithTLS config with an explicitly weak MinVersion was rejected at
	// option-application time; surface it now.
	if s.tlsErr != nil {
		return s.tlsErr
	}
	// TLS-only: fail closed rather than bind a plaintext listener. The
	// orchestrator always supplies a config (operator-provided or
	// auto-provisioned from the node CA); a nil here means a direct caller
	// tried to start the raw server without TLS.
	if s.tlsConfig == nil {
		return &TLSNotConfiguredError{}
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return &StartError{Addr: s.addr, Err: err}
	}

	// Wrap listener with TLS (always configured; guarded above).
	ln = tls.NewListener(ln, s.tlsConfig)
	s.listener = ln

	// Add Alt-Svc header middleware to advertise HTTP/3 availability.
	if s.tlsConfig != nil {
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		altSvc := fmt.Sprintf(`h3=":%s"; ma=86400`, port)
		origHandler := s.server.Handler
		s.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Alt-Svc", altSvc)
			origHandler.ServeHTTP(w, r)
		})
	}

	proto := "HTTP"
	if s.tlsConfig != nil {
		proto = "HTTPS (HTTP/2+TLS)"
	}
	slog.Info("REST server started", "addr", ln.Addr().String(), "proto", proto)

	go func() {
		if serveErr := s.server.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("REST server error", "error", serveErr)
		}
	}()

	// Start HTTP/3 server on the same port via QUIC when TLS is configured.
	if s.tlsConfig != nil {
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		h3Addr := net.JoinHostPort("0.0.0.0", port)
		s.http3Server = &http3.Server{
			Addr:      h3Addr,
			TLSConfig: s.tlsConfig,
			Handler:   s.server.Handler,
		}
		go func() {
			if h3Err := s.http3Server.ListenAndServe(); h3Err != nil && !errors.Is(h3Err, http.ErrServerClosed) {
				slog.Error("HTTP/3 server error", "error", h3Err)
			}
		}()
		slog.Info("HTTP/3 server started", "addr", h3Addr)
	}

	return nil
}

// Stop gracefully shuts down the REST server. When HTTP/3 is active, it
// is closed before the HTTP/2+TLS server is shut down.
func (s *Server) Stop() {
	if s.server == nil {
		return
	}
	// Stop HTTP/3 first.
	if s.http3Server != nil {
		if err := s.http3Server.Close(); err != nil {
			slog.Error("HTTP/3 server close error", "error", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		slog.Error("REST server shutdown error", "error", err)
	}
	slog.Info("REST server stopped")
}

// Addr returns the listener address. Useful for tests that use port 0.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// dispatch routes incoming requests using map-based dispatch for O(1)
// path lookup. The /kv prefix is handled separately to support path
// parameters (/kv/{key}).
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routes[r.URL.Path]
	if !ok {
		// Check for /kv prefix routing. Requires kvSvc to be configured.
		if len(r.URL.Path) >= 3 && r.URL.Path[:3] == "/kv" {
			if s.kvSvc == nil {
				writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "kv"})
				return
			}
			s.handleKV(w, r)
			return
		}
		// User routes with path params (GET/PUT/DELETE /users/{id},
		// POST /users/{id}/enable, /users/{id}/disable).
		if len(r.URL.Path) > 7 && r.URL.Path[:7] == "/users/" {
			s.handleUser(w, r)
			return
		}
		// Role routes with path params (GET/PUT/DELETE /roles/{id}).
		if len(r.URL.Path) > 7 && r.URL.Path[:7] == "/roles/" {
			s.handleRole(w, r)
			return
		}
		// Admin per-shard routes with path params:
		//   DELETE /admin/shards/{id}       -> stop shard
		//   GET    /admin/shards/{id}/state -> shard Raft state
		const adminShardsPrefix = "/admin/shards/"
		if len(r.URL.Path) > len(adminShardsPrefix) && r.URL.Path[:len(adminShardsPrefix)] == adminShardsPrefix {
			s.handleAdminShardByID(w, r)
			return
		}
		// TimeSeries series routes with path params
		// (DELETE /ts/series/{metric}, GET/PUT /ts/series/{metric}/retention).
		if len(r.URL.Path) > 11 && r.URL.Path[:11] == "/ts/series/" {
			s.handleTSSeries(w, r)
			return
		}
		// Database routes with path params
		// (GET /v1/databases/{name}, DELETE /v1/databases/{name}).
		if len(r.URL.Path) > 14 && r.URL.Path[:14] == "/v1/databases/" {
			s.handleDatabaseByName(w, r)
			return
		}
		// Collection routes with path params
		// (GET/DELETE /v1/collections/{db}/{name},
		// POST/GET /v1/collections/{db}/{name}/documents,
		// GET/PUT/DELETE /v1/collections/{db}/{name}/documents/{id}).
		if len(r.URL.Path) > 16 && r.URL.Path[:16] == "/v1/collections/" {
			s.handleCollectionByPath(w, r)
			return
		}
		// Barrier officer routes (GET/POST /v1/barrier/officers and path-param
		// claim routes under /v1/barrier/officers/{username}/... and
		// /v1/barrier/recovery/officers/{username}/...).
		const barrierOfficersPrefix = "/v1/barrier/officers/"
		const barrierRecoveryOfficersPrefix = "/v1/barrier/recovery/officers/"
		if r.URL.Path == "/v1/barrier/officers" {
			// Exact path: GET lists, POST registers.
			if s.barrierSvc == nil {
				writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
				return
			}
			switch r.Method {
			case http.MethodGet:
				s.handleBarrierOfficerList(w, r)
			case http.MethodPost:
				s.handleBarrierOfficerRegister(w, r)
			default:
				w.Header().Set("Allow", "GET, POST")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}
		if len(r.URL.Path) > len(barrierOfficersPrefix) && r.URL.Path[:len(barrierOfficersPrefix)] == barrierOfficersPrefix {
			s.handleBarrierOfficersByPath(w, r)
			return
		}
		if len(r.URL.Path) > len(barrierRecoveryOfficersPrefix) && r.URL.Path[:len(barrierRecoveryOfficersPrefix)] == barrierRecoveryOfficersPrefix {
			s.handleBarrierOfficersByPath(w, r)
			return
		}
		// Tenant routes with path params
		// (GET /v1/tenants/{id}, DELETE /v1/tenants/{id},
		// POST /v1/tenants/{id}/quotas).
		if len(r.URL.Path) >= 12 && r.URL.Path[:12] == "/v1/tenants/" {
			s.handleTenantByID(w, r)
			return
		}
		// Backup routes with path params:
		//   GET    /v1/backups/{id}        -> get backup
		//   DELETE /v1/backups/{id}        -> delete backup
		//   POST   /v1/backups/{id}/verify -> verify backup
		// The keyword routes (list, restore) are matched earlier via s.routes.
		if len(r.URL.Path) > 12 && r.URL.Path[:12] == "/v1/backups/" {
			s.handleBackupByID(w, r)
			return
		}
		// Rebalance shard-stats route with path param:
		//   GET /v1/rebalance/shards/{id} -> shard statistics
		const rebalanceShardsPrefix = "/v1/rebalance/shards/"
		if len(r.URL.Path) > len(rebalanceShardsPrefix) && r.URL.Path[:len(rebalanceShardsPrefix)] == rebalanceShardsPrefix {
			s.handleRebalanceShardStats(w, r)
			return
		}
		// Credential routes with path params:
		//   DELETE /v1/credentials/{name}
		//   POST   /v1/credentials/{name}/rotate
		//   POST   /v1/credentials/{name}/gc
		//   GET    /v1/credentials/{name}/versions
		//   GET    /v1/credentials/{name}/versions/{version}.
		if len(r.URL.Path) > 16 && r.URL.Path[:16] == "/v1/credentials/" {
			s.handleCredentialByPath(w, r)
			return
		}
		// Auth factor routes with path params:
		//   POST /v1/authfactors/{factor}/enroll
		//   POST /v1/authfactors/{enrollment_id}/challenge
		//   POST /v1/authfactors/{enrollment_id}/verify
		//   DELETE /v1/authfactors/{enrollment_id}
		if len(r.URL.Path) > 16 && r.URL.Path[:16] == "/v1/authfactors/" {
			s.handleAuthFactorByPath(w, r)
			return
		}
		// User auth factor routes with path params (M14):
		//   GET    /v1/users/factors/{id}
		//   DELETE /v1/users/factors/{id}
		//   PUT    /v1/users/factors/{id}/label
		const userFactorPrefix = "/v1/users/factors/"
		if len(r.URL.Path) > len(userFactorPrefix) && r.URL.Path[:len(userFactorPrefix)] == userFactorPrefix {
			// Skip the keyword routes already in s.routes (list, login, rotate).
			suffix := r.URL.Path[len(userFactorPrefix):]
			if suffix != "list" && suffix != "login" && suffix != "rotate" {
				s.handleUserAuthFactorByPath(w, r)
				return
			}
		}
		// Fall through to embedded web UI if configured.
		if s.webUI != nil {
			s.webUI.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Method != rt.method {
		w.Header().Set("Allow", rt.method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rt.handler(w, r)
}

// handleCABootstrap serves the cluster CA certificate bundle for joining
// nodes. The response contains PEM-encoded CA cert + CA key concatenated,
// with content type "application/pem-certificate-chain". This endpoint is
// public (exempted from authentication in the middleware) because joining
// nodes do not yet have TLS credentials.
func (s *Server) handleCABootstrap(w http.ResponseWriter, r *http.Request) {
	if len(s.caBootstrapCert) == 0 {
		http.Error(w, "CA bootstrap not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/pem-certificate-chain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.caBootstrapCert)
	if len(s.caBootstrapKey) > 0 {
		_, _ = w.Write(s.caBootstrapKey)
	}
}

// peerAdminRegistry is a thread-safe registry that maps Raft addresses to
// admin HTTP base URLs. It first checks explicit registrations, then falls
// back to the adminPort-based derivation when configured.
type peerAdminRegistry struct {
	mu        sync.RWMutex
	peers     map[string]string // raftAddr -> adminURL (explicit)
	adminPort string            // fallback: derive from raftAddr host + this port
	scheme    string            // "http" or "https" for fallback URL generation
}

// NewPeerAdminRegistry creates a new peer admin registry. When adminPort
// is non-empty, it is used as a fallback to derive admin URLs from Raft
// addresses that are not explicitly registered. The fallback scheme
// defaults to "http".
func NewPeerAdminRegistry(adminPort string) *peerAdminRegistry {
	return &peerAdminRegistry{
		peers:     make(map[string]string),
		adminPort: adminPort,
		scheme:    "http",
	}
}

// NewPeerAdminRegistryTLS creates a new peer admin registry with HTTPS
// scheme for fallback URL generation.
func NewPeerAdminRegistryTLS(adminPort string) *peerAdminRegistry {
	return &peerAdminRegistry{
		peers:     make(map[string]string),
		adminPort: adminPort,
		scheme:    "https",
	}
}

// ResolveAdminURL returns the admin HTTP base URL for the given Raft address.
// It first checks explicit registrations, then falls back to replacing the
// Raft port with the configured admin port.
func (r *peerAdminRegistry) ResolveAdminURL(raftAddr string) string {
	r.mu.RLock()
	url, ok := r.peers[raftAddr]
	r.mu.RUnlock()
	if ok {
		return url
	}

	// Fallback: derive admin URL from Raft address host + admin port.
	if r.adminPort != "" {
		host, _, err := net.SplitHostPort(raftAddr)
		if err != nil {
			return ""
		}
		return r.scheme + "://" + net.JoinHostPort(host, r.adminPort)
	}

	return ""
}

// Register records a Raft address to admin URL mapping.
func (r *peerAdminRegistry) Register(raftAddr, adminURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[raftAddr] = adminURL
}

// headerForwardedBy carries the Raft address of the node that most recently
// forwarded a request. It identifies the immediate forwarder for logging and
// diagnostics; loop prevention itself is bounded by headerForwardHops below.
const headerForwardedBy = "X-Quicraft-Forwarded-By"

// headerForwardHops carries the number of leader-forwarding hops a request
// has already taken, as a decimal integer. It bounds forwarding chains to
// maxForwardHops so a genuine loop terminates, while still permitting the
// multi-hop path a non-member entry node may need to reach the real leader
// (guessed member -> real leader), plus one hop of slack.
const headerForwardHops = "X-Quicraft-Forward-Hops"

// maxForwardHops is the maximum number of leader-forwarding hops permitted
// for a single client request before it is rejected as a forwarding loop.
const maxForwardHops = 3

// FetchJSON issues an HTTP GET to the given URL and decodes the JSON
// response into dst. The caCertPool is used to verify the server's TLS
// certificate. When nil, the system certificate pool is used. This is
// exported for use by CLI commands.
func FetchJSON(url string, dst any, caCertPool *x509.CertPool) error {
	return fetchJSON(url, dst, caCertPool)
}

// FetchJSONWithMTLS issues an HTTP GET with mTLS authentication. The
// clientCert is presented to the server during the TLS handshake.
func FetchJSONWithMTLS(url string, dst any, caCertPool *x509.CertPool, clientCert tls.Certificate) error {
	return fetchJSON(url, dst, caCertPool, clientCert)
}
