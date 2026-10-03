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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	quicgo "github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
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

// route pairs an HTTP method with its handler function. It is retained for
// the legacy Server.routes introspection map; request routing itself is
// performed by the chi router.
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
	dekSvc            service.DEKServicer
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
	forwardNodeID string
	addr          string
	server        *http.Server
	listener      net.Listener

	// preboundListener and preboundPacketConn are sockets the embedder bound
	// and handed over (WithListener / WithPacketConn, go-qrdb#374). When set,
	// Start serves on them instead of binding addr, so a caller that reserved
	// a kernel-assigned port never releases it before the server holds it.
	// The server owns them from Start on and closes them in Stop.
	preboundListener   net.Listener
	preboundPacketConn net.PacketConn

	// routes is a legacy introspection map of the STATIC (non-templated)
	// built-in routes, populated as a byproduct of route registration. Routing
	// is performed natively by the chi router; this map exists only so tests
	// and embedders can inspect which static routes were registered.
	routes map[string]route

	// routePerms is the single source of truth for per-route authorization.
	// Every route registered through the registration helpers declares its
	// permission (or an explicit public/authenticated marker) here, at the
	// registration site. RESTMiddleware and SOMiddleware consult it, and the
	// default-deny startup assertion walks the router against it.
	routePerms *authmw.RESTRouteRegistry

	// routeOwners maps every claimed route (authmw.RESTRouteKey) to who
	// claimed it -- "built-in" or "module <name>" -- so a collision names
	// both parties. registeringModule is the module whose RegisterHTTP is
	// running, empty while the built-in routes register. Both are
	// construction-time only.
	routeOwners       map[string]string
	registeringModule string

	// router is the top-level chi router carrying every route natively:
	// capability-module routes (ServiceModule.RegisterHTTP) are registered
	// first, then qrdb's own built-in routes. NotFound falls through to the
	// embedded web UI (when configured) or 404.
	router chi.Router

	// modules are capability modules whose HTTP routes are registered on the chi
	// router. Set via WithServiceModules by the server orchestrator.
	modules     []apimodule.ServiceModule
	tlsConfig   *tls.Config
	tlsErr      error
	caCertPool  *x509.CertPool
	http3Server *http3.Server
	// http3Conn is the UDP socket the HTTP/3 server serves. It is opened
	// by Start so the bind is checked synchronously, and closed by Stop.
	http3Conn net.PacketConn
	host      Host
	// caBootstrap supplies the CERTIFICATES /v1/ca/bootstrap serves, or nil
	// when nothing is configured and the path is left unclaimed.
	// caBootstrapSources counts the sources configured: two is a refusal at
	// Start (CABootstrapConflictError), never a silent pick.
	caBootstrap        func(context.Context) ([]byte, error)
	caBootstrapSources int
	onEtcdUpdated      func(dbName string, cfg *database.EtcdConfig) error
	webUI              http.Handler      // optional embedded web UI file server
	auditLogger        audit.AuditLogger // optional compliance-grade audit logger

	// soPINVerifier handles SO PIN verification for dual-factor auth.
	soPINVerifier soPINVerifier

	// soDualFactor enables the SO dual-factor middleware
	// (authmw.SOMiddleware) on SO-gated routes. Installed only when
	// both this flag is set (auth.so_dual_factor) and soPINVerifier is
	// wired; otherwise the chain is unchanged.
	soDualFactor bool

	// auditFailure is what the SO dual-factor middleware does when it cannot
	// record the second factor's outcome (go-qrdb#445). It is the operator's
	// auth.audit_failure_policy, the SAME value the AuthMiddleware in authMW
	// was built with -- one posture across both assertions, not two keys.
	//
	// Its ZERO VALUE is audit.FailClosed, so a server constructed without
	// WithAuditFailurePolicy gates rather than proceeds unrecorded.
	auditFailure audit.FailurePolicy

	// serviceRegistry provides per-(tenant, database) service resolution.
	// When nil, all handlers fall back to the singleton service fields above,
	// preserving backward compatibility for single-DB/single-tenant deployments.
	serviceRegistry ServiceRegistry

	// sealedWindowProbe controls the sealed-window middleware. When set
	// and IsSealed() returns true, every request not on
	// SealedWindowAllowlist is rejected with 503. When nil, the
	// middleware is not installed and the server behaves as before.
	sealedWindowProbe sealedWindowProbe

	// sealedWindowListener is the boot-time sealed-window listener's probe,
	// and selects that listener's gate: every request refused once the
	// barrier is unsealed, and officer registration refused once enrollment
	// has closed. Nil for a regular listener's gate.
	sealedWindowListener sealedwindow.ListenerProbe

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

// WithServiceModules registers capability modules whose HTTP routes are attached
// to the chi router at construction. A module route that is the same route as
// a built-in or another module's -- including one that differs only in a path
// parameter's name -- fails construction with *RouteCollisionError; nothing
// shadows anything silently (go-qrdb#194).
func WithServiceModules(mods []apimodule.ServiceModule) ServerOption {
	return func(s *Server) {
		s.modules = mods
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

// WithListener makes Start serve HTTPS on ln instead of binding the server's
// address. The caller binds it — typically on port 0, reading the
// kernel-assigned port back from ln.Addr() — and hands it over; the port is
// never released between reservation and serving, which a "listen on :0,
// close, pass the number" reservation cannot promise (go-qrdb#374). ln must be
// a plain TCP listener: Start wraps it in TLS. The server owns ln from Start
// on and closes it in Stop.
//
// Without WithPacketConn, the HTTP/3 socket is still bound by Start on the
// same host and port ln holds. Pass WithPacketConn to pre-bind that too.
func WithListener(ln net.Listener) ServerOption {
	return func(s *Server) { s.preboundListener = ln }
}

// WithPacketConn makes Start serve HTTP/3 on pc instead of binding a UDP
// socket beside the TCP listener. It requires WithListener, and pc must be
// bound on the same host and port as that listener, because the Alt-Svc
// header the TCP side sends advertises HTTP/3 on the TCP listener's port.
// The server owns pc from Start on and closes it in Stop.
func WithPacketConn(pc net.PacketConn) ServerOption {
	return func(s *Server) { s.preboundPacketConn = pc }
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
//
// This is the REGISTRY-LESS fallback, not the primary path. When a
// service.Registry is configured the lock servicer is resolved per
// (tenant, database) through Registry.LockService and this singleton is never
// consulted: a lock key carries no tenant segment, so a process-wide servicer
// built over the raw engines gives every tenant the same lock (go-qrdb#455).
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

// WithPINBackend attaches the node's pin.PINBackend: barrier and ceremony
// initialization provision the SO and User PINs through it, and the
// SO-PIN-gated operations (seal, recovery generation, rekey start, holder
// revocation) verify against it. Without one those operations fail closed
// with pin.ErrBackendNotConfigured.
//
// The backend is decorated with the SO PIN lockout (pin.NewLockoutBackend,
// idempotent: a backend the composer already decorated keeps its one set of
// counters), so a raw backend handed straight to this transport is still
// throttled.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	backend = pin.NewLockoutBackend(backend)
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

// WithDEKService sets the tenant DEK keyring service. Without it the
// /v1/dek routes are not served.
func WithDEKService(svc service.DEKServicer) ServerOption {
	return func(s *Server) { s.dekSvc = svc }
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

// soPINVerifier is the narrow interface the SO dual-factor middleware needs
// from the SO authenticator. Satisfied by *auth.SOAuthenticator.
type soPINVerifier interface {
	VerifySOPIN(ctx context.Context, tenantID string, presentedPIN []byte) error
}

// WithSOAuthenticator attaches an soPINVerifier for SO dual-factor PIN
// verification on SO-permissioned routes.
func WithSOAuthenticator(verifier soPINVerifier) ServerOption {
	return func(s *Server) { s.soPINVerifier = verifier }
}

// WithAuditFailurePolicy selects what the SO dual-factor middleware does when
// the second factor's audit record cannot be written: audit.FailClosed (the
// default and the zero value) refuses the request with 503; audit.FailOpen
// serves it and counts the gap.
//
// It takes the value the orchestrator already resolved from
// auth.audit_failure_policy for the authorization middleware. go-qrdb#445
// ruled ONE posture across both gates, so this option exists to carry that one
// value down -- not to let the SO surface be configured separately.
func WithAuditFailurePolicy(p audit.FailurePolicy) ServerOption {
	return func(s *Server) { s.auditFailure = p }
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

// WithCABootstrap serves certPEM on /v1/ca/bootstrap: the cluster CA
// certificate joining nodes fetch to bootstrap their trust store. The endpoint
// is public (no authentication) -- exactly why it serves certificates and
// nothing else: certPEM is reduced to its CERTIFICATE blocks here, so a caller
// that concatenates the CA key onto it cannot publish the key. Input with no
// certificate configures nothing. It and WithCABootstrapProvider are the two
// sources of the one route; configuring both is refused at Start.
func WithCABootstrap(certPEM []byte) ServerOption {
	return func(s *Server) {
		certs := clustertls.CertificatesOnly(certPEM)
		if len(certs) == 0 {
			return
		}
		s.addCABootstrap(func(context.Context) ([]byte, error) { return certs, nil })
	}
}

// WithCABootstrapProvider serves the bundle provider returns on
// /v1/ca/bootstrap, read per request: the host's first-contact trust bundle
// when the host, not qrdb's cluster CA, owns that trust (go-xkms's xkmsd). The
// host supplies data and qrdb owns the route, as for /health, so the route has
// one owner and a host never registers its own. The bundle is reduced to its
// CERTIFICATE blocks before it is served, whatever the provider returns. A nil
// provider configures nothing; configuring it beside WithCABootstrap is
// refused at Start.
func WithCABootstrapProvider(provider func(context.Context) ([]byte, error)) ServerOption {
	return func(s *Server) {
		if provider == nil {
			return
		}
		s.addCABootstrap(func(ctx context.Context) ([]byte, error) {
			bundle, err := provider(ctx)
			if err != nil {
				return nil, err
			}
			return clustertls.CertificatesOnly(bundle), nil
		})
	}
}

// addCABootstrap records one source of the /v1/ca/bootstrap bundle.
func (s *Server) addCABootstrap(source func(context.Context) ([]byte, error)) {
	s.caBootstrap = source
	s.caBootstrapSources++
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
// not on SealedWindowAllowlist is rejected with 503. Once unsealed the
// middleware is a passthrough; on re-seal it engages again.
//
// The middleware wraps the authentication middleware, so an allowlisted
// request is still authenticated and authorized by it when one is
// installed. The allowlist is anonymous only on a listener without one:
// the boot-time sealed-window listener (see WithSealedWindowListener).
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) {
		s.sealedWindowProbe = probe
		s.sealedWindowListener = nil
	}
}

// WithSealedWindowListener installs the gate of the boot-time sealed-window
// listener (SealedWindowListenerMiddleware) backed by probe: the allowlist
// while the barrier is sealed, less officer registration once enrollment has
// closed, and a 503 for every request once it is unsealed, sampled per
// request. A nil probe refuses everything. It replaces WithSealedWindowProbe;
// the last of the two applied wins.
func WithSealedWindowListener(probe sealedwindow.ListenerProbe) ServerOption {
	return func(s *Server) {
		probe = sealedwindow.OrFailClosed(probe)
		s.sealedWindowProbe = probe
		s.sealedWindowListener = probe
	}
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

// NewSealedWindowServer builds the REST server for a sealed window: the
// barrier, ceremony and credential surfaces only, behind the sealed-window
// listener gate backed by probe. It serves exactly the sealed allowlist
// (pkg/api/sealedwindow) -- barrier status, initialize and unseal, Shamir and
// recovery unseal, officer registration and share claims, the ceremony
// verbs, the root token -- anonymously while the barrier is sealed, closes
// officer enrollment once the ceremony is operational, answers every other
// request (GET /health included) with the sealed 503 envelope, and refuses
// everything once the barrier is unsealed.
//
// It is THE sealed REST surface: qrdb's own boot-time listener
// (server.ServeSealedWindow) is built with it, and a host that runs its own
// listener mounts NewSealedWindowHandler instead of copying handlers. Any of
// the three services may be nil; their routes then answer as they do on any
// server without them. opts add composition (TLS, a ceremony CA, the tenant
// barrier registry) and are applied before the services and the gate, which
// therefore cannot be overridden by them. Build probe with
// server.NewSealedWindowListenerProbe and share it with every other
// transport serving the same window.
func NewSealedWindowServer(addr string, barrier service.BarrierServicer, ceremonySvc service.CeremonyServicer, credential service.CredentialServicer, probe sealedwindow.ListenerProbe, opts ...ServerOption) *Server {
	all := make([]ServerOption, 0, len(opts)+4)
	all = append(all, opts...)
	if barrier != nil {
		all = append(all, WithBarrierService(barrier))
	}
	if ceremonySvc != nil {
		all = append(all, WithCeremonyService(ceremonySvc))
	}
	if credential != nil {
		all = append(all, WithCredentialService(credential))
	}
	all = append(all, WithSealedWindowListener(probe))
	return NewServer(nil, nil, addr, all...)
}

// NewSealedWindowHandler returns the sealed REST surface (see
// NewSealedWindowServer) as an http.Handler for a host to mount on its own
// listener, HTTP/3 included. It is the same handler qrdb's sealed listener
// serves, so a host never copies a sealed-window handler.
func NewSealedWindowHandler(barrier service.BarrierServicer, ceremonySvc service.CeremonyServicer, credential service.CredentialServicer, probe sealedwindow.ListenerProbe, opts ...ServerOption) http.Handler {
	return NewSealedWindowServer("", barrier, ceremonySvc, credential, probe, opts...).Handler()
}

// Handler returns the server's complete handler -- routing wrapped in its
// middleware chain -- for serving on a listener the caller owns.
func (s *Server) Handler() http.Handler {
	return s.server.Handler
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

	// The chi router is the single routing surface. Capability modules
	// (ServiceModule.RegisterHTTP) register first so their routes take
	// precedence; qrdb's own built-in routes are registered natively via the
	// registration helpers, each declaring its required permission (or an
	// explicit public/authenticated marker) in routePerms at the registration
	// site. A default-deny startup assertion then walks the router and fails
	// construction if any route is missing from the registry.
	s.routes = map[string]route{}
	s.routePerms = authmw.NewRESTRouteRegistry()
	r := chi.NewRouter()
	s.router = r

	// Modules register through the same permission-recording helpers the
	// built-in routes use, so their routes feed routePerms and the default-deny
	// assertion identically. A module implementing ModuleAuthFallback gets its
	// fallback attached to every non-public route it registers: the REST
	// middleware invokes it only when qrdb's own authenticators produce no
	// identity for a request on that module's routes (the module's
	// session-token plane), and the resulting identity flows through the SAME
	// enrichment and authorizer as an mTLS identity.
	s.routeOwners = map[string]string{}
	directRoutes := make(map[string][]apimodule.RouteClaim, len(s.modules))
	for _, m := range s.modules {
		routeFn := s.route
		if fb, ok := m.(apimodule.ModuleAuthFallback); ok {
			routeFn = func(method, pattern string, perm permission.Permission, h http.HandlerFunc) {
				s.route(method, pattern, perm, h)
				s.routePerms.SetFallback(method, pattern, fb)
			}
		}
		s.registeringModule = m.Name()
		reg := apimodule.NewHTTPRegistrar(r, routeFn, s.public)
		m.RegisterHTTP(reg)
		// Registration is validated once, below; nothing registered later
		// would be, so later registration is refused outright.
		reg.Freeze()
		directRoutes[m.Name()] = reg.DirectRoutes()
	}
	s.registeringModule = ""
	s.registerRoutes()
	s.assertNoDirectRouteCollisions(directRoutes)

	// Requests matching no route fall through to the embedded web UI when
	// configured (SPA fallback), otherwise 404.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if s.webUI != nil {
			s.webUI.ServeHTTP(w, req)
			return
		}
		http.NotFound(w, req)
	})

	// Method-not-allowed mirrors the legacy dispatcher: 405 with an Allow
	// header listing the methods registered for the path.
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		if allow := s.allowedMethods(req.URL.Path); allow != "" {
			w.Header().Set("Allow", allow)
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// DEFAULT-DENY structural invariant: every registered route must carry a
	// permission or an explicit public/authenticated marker.
	s.assertRoutesAuthorized()

	// Build the middleware chain from innermost to outermost:
	//   router → auth → sealed-window → scope → CORS
	// scopeMiddleware runs before auth so that auth middleware can read the
	// tenant/database from context if it needs to (future-proof). CORS is the
	// outermost layer so that preflight OPTIONS requests are handled before any
	// other processing. The sealed-window middleware wraps auth, so it
	// refuses a non-allowlisted request while the barrier is sealed before
	// any authentication work, and an allowlisted request still reaches auth
	// when auth is installed. Only a listener without auth -- the boot-time
	// sealed-window listener -- serves its allowlist anonymously.
	// The SO dual-factor middleware is the innermost wrapper (directly
	// around the mux) so it runs AFTER the auth middleware has resolved
	// the caller's identity into the request context. It gates only the
	// routes mapped to authmw.SOPermissions and is installed only when
	// explicitly enabled (auth.so_dual_factor) and an SO PIN verifier is
	// wired — default posture is unchanged.
	var handler http.Handler = r
	if s.soDualFactor && s.soPINVerifier != nil {
		handler = authmw.SOMiddleware(s.routePerms, s.roleSvc, s.soPINVerifier, s.auditLogger, s.auditFailure, slog.Default(), handler)
	}
	if s.authMW != nil {
		handler = s.authMW.RESTMiddleware(s.routePerms, handler)
	}
	if s.sealedWindowProbe != nil {
		handler = sealedWindowGate(s.sealedWindowProbe, nil, s.sealedWindowListener)(handler)
	}
	handler = scopeMiddleware(handler)
	if s.corsConfig != nil {
		handler = corsMiddleware(*s.corsConfig, handler)
	}
	// OUTERMOST: every layer below -- sealed window, authentication and
	// authorization, SO dual factor, and chi's routing -- must see the SAME
	// path. See canonicalPathMiddleware. The HTTP/3 server serves this same
	// handler, so both listeners get it.
	handler = canonicalPathMiddleware(handler)

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
		s.closePrebound()
		return s.tlsErr
	}
	if s.caBootstrapSources > 1 {
		s.closePrebound()
		return &CABootstrapConflictError{}
	}
	// TLS-only: fail closed rather than bind a plaintext listener. The
	// orchestrator always supplies a config (operator-provided or
	// auto-provisioned from the node CA); a nil here means a direct caller
	// tried to start the raw server without TLS.
	if s.tlsConfig == nil {
		s.closePrebound()
		return &TLSNotConfiguredError{}
	}

	if s.preboundPacketConn != nil && s.preboundListener == nil {
		s.closePrebound()
		return &PacketConnWithoutListenerError{}
	}
	ln := s.preboundListener
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", s.addr); err != nil {
			return &StartError{Addr: s.addr, Err: err}
		}
	}

	// Report every accepted connection to the modules that observe them.
	// Wrapped BENEATH the TLS listener, so http.Server still sees a *tls.Conn
	// and negotiates HTTP/2 over it.
	observers := apimodule.ConnObserversOf(s.modules)
	ln = observers.Listener(ln, apimodule.TransportREST)

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

	// Bind HTTP/3 on the SAME host and port as the TCP listener via QUIC,
	// BEFORE either transport starts serving.
	//
	// The host is read back from the bound TCP listener rather than
	// re-derived, so the two transports cannot disagree: they serve the
	// same handler with the same TLS config, and a bind address is a
	// containment decision that has to hold on both or it holds on
	// neither. go-qrdb#165 measured the previous behaviour on a live node
	// -- `ss` showed `udp UNCONN *:18443` beside `tcp LISTEN
	// 127.0.0.1:18443`, i.e. a REST server deliberately rebound to
	// loopback still accepted QUIC datagrams on every interface.
	//
	// The UDP socket is opened here rather than inside ListenAndServe on
	// a goroutine for three reasons: a bind failure becomes a Start error
	// the operator sees instead of one log line nobody reads; the address
	// the kernel actually assigned can be compared with the TCP listener's
	// before any datagram is served; and a rejected QUIC bind leaves the
	// REST server unstarted rather than half-started.
	var h3Conn net.PacketConn
	if s.tlsConfig != nil {
		pc := s.preboundPacketConn
		if pc == nil {
			h3Addr := net.JoinHostPort(hostOfAddr(ln.Addr()), portOfAddr(ln.Addr()))
			var h3Err error
			if pc, h3Err = net.ListenPacket("udp", h3Addr); h3Err != nil {
				_ = ln.Close()
				s.listener = nil
				return &HTTP3StartError{Addr: h3Addr, Err: h3Err}
			}
		}
		// The kernel, not this process, decides what a bind string
		// resolves to: a dual-stack or resolver difference between the
		// TCP and UDP stacks would widen the QUIC surface past what the
		// TCP listener accepted, which is exactly the #165 exposure and
		// is invisible from configuration. Refuse to serve it. A
		// pre-bound socket is held to the same rule, and to the same
		// port, which is the one Alt-Svc advertises.
		mismatch := sameBindHost(ln.Addr(), pc.LocalAddr())
		if mismatch == nil && portOfAddr(pc.LocalAddr()) != portOfAddr(ln.Addr()) {
			mismatch = &ListenerPortMismatchError{TCPPort: portOfAddr(ln.Addr()), UDPPort: portOfAddr(pc.LocalAddr())}
		}
		if mismatch != nil {
			_ = pc.Close()
			_ = ln.Close()
			s.listener = nil
			return mismatch
		}
		h3Conn = pc
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

	if h3Conn != nil {
		s.http3Conn = h3Conn
		s.http3Server = &http3.Server{
			Addr:       h3Conn.LocalAddr().String(),
			TLSConfig:  s.tlsConfig,
			Handler:    s.server.Handler,
			QUICConfig: http3QUICConfig(),
		}
		if len(observers) > 0 {
			// http3.Server accepts QUIC connections itself, so there is no
			// net.Listener to wrap. ConnContext runs once per accepted
			// connection, and the connection's own context ends when it closes.
			s.http3Server.ConnContext = func(ctx context.Context, c *quicgo.Conn) context.Context {
				context.AfterFunc(c.Context(), observers.Open(apimodule.TransportHTTP3))
				return ctx
			}
		}
		go func() {
			if serveErr := s.http3Server.Serve(h3Conn); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
				slog.Error("HTTP/3 server error", "error", serveErr)
			}
		}()
		slog.Info("HTTP/3 server started", "addr", h3Conn.LocalAddr().String())
	}

	return nil
}

// http3QUICConfig is the QUIC configuration of the HTTP/3 listener. Without
// one quic-go's http3.Server accepts 0-RTT, and 0-RTT early data is
// replayable: an on-path attacker can resend a captured flight and the server
// processes it again, and this listener serves the same handlers as HTTP/2,
// state-changing ones included. Allow0RTT stays false, so a resumed client
// completes a full handshake first. The idle, keep-alive and bidirectional
// stream limits match the native QUIC listener (pkg/api/quic); unidirectional
// streams keep quic-go's default, which HTTP/3's control and QPACK streams
// need.
func http3QUICConfig() *quicgo.Config {
	return &quicgo.Config{
		MaxIdleTimeout:     30 * time.Second,
		KeepAlivePeriod:    10 * time.Second,
		MaxIncomingStreams: 1000,
		Allow0RTT:          false,
	}
}

// closePrebound closes the sockets handed over by WithListener and
// WithPacketConn when Start refuses before serving on them: the server owns
// them from Start on, so a refusal must not leak them to a caller that has
// already given them up.
func (s *Server) closePrebound() {
	if s.preboundListener != nil {
		_ = s.preboundListener.Close()
	}
	if s.preboundPacketConn != nil {
		_ = s.preboundPacketConn.Close()
	}
}

// hostOfAddr returns the host portion of a bound listener address. Reading it
// back off the listener rather than off the configured string is what keeps
// the HTTP/3 socket on the host the TCP listener actually got.
func hostOfAddr(a net.Addr) string {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return ""
	}
	return host
}

// portOfAddr returns the port portion of a bound listener address. It must
// come from the bound listener, not the configured string, so port 0
// (ephemeral) resolves to the port the kernel assigned.
func portOfAddr(a net.Addr) string {
	_, port, err := net.SplitHostPort(a.String())
	if err != nil {
		return ""
	}
	return port
}

// sameBindHost reports whether a QUIC socket landed on the same bind host as
// the TCP listener it shadows. Hosts must be equal; the single exception is
// two spellings of the wildcard (Go reports a "0.0.0.0" UDP bind back as
// "[::]" on a dual-stack host), which name the same "every interface" and
// which this code does not get to choose between.
//
// Two DIFFERENT concrete hosts are a mismatch even though neither is a
// wildcard. Alt-Svc advertises HTTP/3 on the TCP response at the same
// authority, so a client that reached the TCP listener at one address probes
// QUIC at that address; a QUIC socket sitting on another concrete address is
// unreachable to it, and on a dual-stack host resolving a name can land the
// two on 127.0.0.1 and ::1 respectively. Nothing in this file can produce
// that today -- the UDP bind string is the TCP listener's own resolved
// address -- and requiring equality is what keeps it that way.
func sameBindHost(tcpAddr, udpAddr net.Addr) error {
	tcpHost, udpHost := hostOfAddr(tcpAddr), hostOfAddr(udpAddr)
	if tcpHost == udpHost {
		return nil
	}
	if isWildcardHost(tcpHost) && isWildcardHost(udpHost) {
		return nil
	}
	return &ListenerHostMismatchError{TCPHost: tcpHost, UDPHost: udpHost}
}

// isWildcardHost reports whether host names every local interface. An
// unparseable or empty host counts as a wildcard: net.Listen treats "" that
// way, and an address this code cannot read is not one it can vouch for.
func isWildcardHost(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsUnspecified()
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
	// Start owns the UDP socket, so Stop closes it. http3.Server.Serve
	// does not close a PacketConn it did not open.
	if s.http3Conn != nil {
		if err := s.http3Conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Error("HTTP/3 socket close error", "error", err)
		}
		s.http3Conn = nil
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

// route registers a built-in API route on the chi router and declares its
// required permission in the routePerms registry — ONE registration per
// route, with authorization declared at the registration site. Static
// (non-templated) patterns are additionally recorded in the legacy
// Server.routes introspection map.
func (s *Server) route(method, pattern string, perm permission.Permission, h http.HandlerFunc) {
	s.routeIf(true, method, pattern, perm, h)
}

// routeIf registers the route's PERMISSION unconditionally but attaches the
// route to the router only when enabled (its backing service is wired).
// The unconditional registry entry preserves the legacy middleware contract,
// where the permission tables were static: a request for an un-wired route
// is authorized against its declared permission first and then 404s at the
// router, and SO dual-factor gating recognizes SO-permissioned paths even on
// servers that do not serve them.
func (s *Server) routeIf(enabled bool, method, pattern string, perm permission.Permission, h http.HandlerFunc) {
	s.claimRoute(method, pattern)
	s.routePerms.Register(method, pattern, perm)
	if !enabled {
		return
	}
	s.router.Method(method, pattern, h)
	s.recordStaticRoute(method, pattern, h)
}

// routeTarget is route for a route whose request names the tenant it acts on:
// target extracts that tenant, and the middleware authorizes perm against it
// rather than against the caller's own tenant (see authmw.Target).
func (s *Server) routeTarget(method, pattern string, perm permission.Permission, target authmw.RESTTargetFunc, h http.HandlerFunc) {
	s.routeIfTarget(true, method, pattern, perm, target, h)
}

// routeIfTarget is routeIf plus a target extractor. The extractor is attached
// only to a route this server registered: a module that already claimed the
// route keeps its own contract.
func (s *Server) routeIfTarget(enabled bool, method, pattern string, perm permission.Permission, target authmw.RESTTargetFunc, h http.HandlerFunc) {
	if s.routePerms.Covered(method, pattern) {
		s.routeIf(enabled, method, pattern, perm, h)
		return
	}
	s.routeIf(enabled, method, pattern, perm, h)
	s.routePerms.SetTarget(method, pattern, target)
}

// public registers a route that is reachable WITHOUT authentication. The
// route must also be on RESTMiddleware's pre-authentication exact-path
// allowlist; the registry marker makes the exemption explicit for the
// default-deny startup assertion.
func (s *Server) public(method, pattern string, h http.HandlerFunc) {
	s.publicIf(true, method, pattern, h)
}

// publicIf mirrors routeIf for public routes: the public marker is recorded
// unconditionally, the router registration is gated on enabled.
func (s *Server) publicIf(enabled bool, method, pattern string, h http.HandlerFunc) {
	s.claimRoute(method, pattern)
	s.routePerms.RegisterPublic(method, pattern)
	if !enabled {
		return
	}
	s.router.Method(method, pattern, h)
	s.recordStaticRoute(method, pattern, h)
}

// authenticatedIf registers a route that requires an authenticated caller
// but no specific permission (e.g. /metrics for Prometheus scraping). The
// marker is recorded unconditionally, the router registration is gated on
// enabled.
func (s *Server) authenticatedIf(enabled bool, method, pattern string, h http.HandlerFunc) {
	s.claimRoute(method, pattern)
	s.routePerms.RegisterAuthenticated(method, pattern)
	if !enabled {
		return
	}
	s.router.Method(method, pattern, h)
	s.recordStaticRoute(method, pattern, h)
}

// builtinRouteOwner is the routeOwners value of a route qrdb itself serves.
const builtinRouteOwner = "built-in"

// routeClaimant names who is registering right now: a module while its
// RegisterHTTP runs, the built-in surface otherwise.
func (s *Server) routeClaimant() string {
	if s.registeringModule == "" {
		return builtinRouteOwner
	}
	return "module " + s.registeringModule
}

// claimRoute records who owns (method, pattern) and refuses a second claim on
// the same route with *RouteCollisionError (go-qrdb#194).
//
// Modules register first, then the built-ins. A module route that is the same
// route as a built-in USED to be skipped on the built-in side, which let any
// module -- including one a downstream host mounts -- silently take over a
// core endpoint; a second module claiming the first one's route was skipped
// the same way, contrary to the guard the registry documents. Both now fail
// construction, naming the two claimants. "The same route" is decided by
// authmw.RESTRouteKey, so a renamed path parameter is caught as the duplicate
// chi treats it as.
//
// The registry keeps its own duplicate check as a backstop; this one exists
// to say WHO collided.
func (s *Server) claimRoute(method, pattern string) {
	if s.routeOwners == nil {
		s.routeOwners = map[string]string{}
	}
	key := authmw.RESTRouteKey(method, pattern)
	claimant := s.routeClaimant()
	if holder, taken := s.routeOwners[key]; taken {
		panic(&RouteCollisionError{Method: method, Pattern: pattern, Claimant: claimant, Holder: holder})
	}
	s.routeOwners[key] = claimant
	if s.registeringModule != "" {
		s.routePerms.RegisterModulePrefix(moduleRoutePrefix(pattern))
	}
}

// moduleRoutePrefix is the subtree a module route claims: the static segments
// before its first parameter or wildcard, as "/a/b/". A fully static route
// claims no subtree (""), and neither does one whose first segment is already
// dynamic, which would claim the whole server.
//
// The REST middleware refuses an unmatched request under a claimed prefix
// instead of passing it to the router's fallback (go-qrdb#194): a path under a
// module's subtree that no registration matches is not the web UI's, and
// nothing else may answer it.
func moduleRoutePrefix(pattern string) string {
	segments := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	static := make([]string, 0, len(segments))
	for _, seg := range segments {
		if strings.ContainsAny(seg, "{*") {
			if len(static) == 0 {
				return ""
			}
			return "/" + strings.Join(static, "/") + "/"
		}
		static = append(static, seg)
	}
	return ""
}

// restCollisionMethods are the methods an AnyMethod direct registration is
// checked under: chi's Handle claims every one of them.
var restCollisionMethods = [...]string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodConnect,
	http.MethodOptions, http.MethodTrace,
}

// assertNoDirectRouteCollisions refuses a route a module registered straight
// on the router when the same route was also registered through the
// permission-recording path -- by a module's Handle/HandlePublic or by a
// built-in (go-qrdb#194).
//
// chi resolves such a pair by silently keeping whichever handler registered
// last, while the registry covers the pattern with the permission-recorded
// registration. The default-deny assertion cannot see it -- it asks only
// whether a pattern is covered, and this one is. The result would be a handler
// nobody authorized served under a permission somebody else declared, so it
// fails construction instead. A direct route that NO registration covers is
// left to the default-deny assertion, which refuses it by name.
func (s *Server) assertNoDirectRouteCollisions(direct map[string][]apimodule.RouteClaim) {
	modules := make([]string, 0, len(direct))
	for name := range direct {
		modules = append(modules, name)
	}
	sort.Strings(modules)
	for _, name := range modules {
		for _, claim := range direct[name] {
			methods := []string{claim.Method}
			if claim.Method == apimodule.AnyMethod {
				methods = restCollisionMethods[:]
			}
			for _, method := range methods {
				if s.routePerms.Covered(method, claim.Pattern) {
					panic(&DirectRouteCollisionError{
						Module:  name,
						Method:  method,
						Pattern: claim.Pattern,
						Holder:  s.routeOwners[authmw.RESTRouteKey(method, claim.Pattern)],
					})
				}
			}
		}
	}
}

// recordStaticRoute mirrors static registrations into the legacy routes map.
func (s *Server) recordStaticRoute(method, pattern string, h http.HandlerFunc) {
	if !strings.ContainsAny(pattern, "{*") {
		s.routes[pattern] = route{method: method, handler: h}
	}
}

// notImplemented returns a handler that reports 501 for a service that was
// not wired into this server, preserving the legacy dispatcher's nil-service
// guards (e.g. /kv/{key} without a KV service, /v1/barrier/officers without
// a barrier service).
func (s *Server) notImplemented(service string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: service})
	}
}

// allowedMethodProbeOrder is the order methods are probed when computing the
// Allow header for a 405 response. It mirrors the Allow strings the legacy
// dispatcher and handlers emitted ("GET, PUT, DELETE", "GET, POST", ...).
var allowedMethodProbeOrder = [...]string{
	http.MethodGet,
	http.MethodPut,
	http.MethodPost,
	http.MethodDelete,
	http.MethodPatch,
	http.MethodHead,
	http.MethodOptions,
}

// allowedMethods returns a comma-separated list of the methods registered
// for path, in canonical order, for the 405 Allow header.
func (s *Server) allowedMethods(path string) string {
	var allow []string
	for _, m := range allowedMethodProbeOrder {
		rctx := chi.NewRouteContext()
		if s.router.Match(rctx, m, path) {
			allow = append(allow, m)
		}
	}
	return strings.Join(allow, ", ")
}

// assertRoutesAuthorized is the DEFAULT-DENY startup assertion: it walks the
// chi routing tree and fails server construction if any registered route —
// built-in or module-registered — is missing from the routePerms registry
// (i.e. carries neither a permission nor an explicit public/authenticated
// marker). This replaces the old table-based default-deny with a structural
// invariant enforced at construction time.
func (s *Server) assertRoutesAuthorized() {
	var missing []string
	_ = chi.Walk(s.router, func(method, routePattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// chi.Walk emits mounted patterns with a trailing slash; a route may
		// be registered either with or without one, so accept both forms.
		trimmed := routePattern
		if len(trimmed) > 1 {
			trimmed = strings.TrimSuffix(trimmed, "/")
		}
		if !s.routePerms.Covered(method, routePattern) && !s.routePerms.Covered(method, trimmed) {
			missing = append(missing, method+" "+trimmed)
		}
		return nil
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf(
			"rest: DEFAULT-DENY startup assertion failed: %d route(s) registered without a permission or explicit public/authenticated marker: %s",
			len(missing), strings.Join(missing, ", ")))
	}
}

// registerRoutes registers every built-in route natively on the chi router,
// declaring each route's permission at the registration site. Conditional
// registration (routes gated on an optional service being wired) mirrors the
// legacy dispatcher exactly; path-parameter families that the old dispatcher
// resolved by prefix parsing are registered unconditionally with the same
// family handlers, which nil-check their service themselves.
func (s *Server) registerRoutes() {
	// Server configuration (public: the first-boot setup wizard checks
	// provisioning state before any user or certificate exists).
	s.public(http.MethodGet, "/v1/config/initialized", s.handleConfigInitialized)

	// Admin routes are attached only when an admin service is provided; the
	// permissions are declared unconditionally (routeIf).
	adminOn := s.adminSvc != nil
	s.routeIf(adminOn, http.MethodGet, "/status", permission.AdminStatus, s.handleStatus)
	s.routeIf(adminOn, http.MethodGet, "/peers", permission.AdminPeers, s.handlePeers)
	s.publicIf(adminOn, http.MethodGet, "/health", s.handleHealth)
	s.routeIf(adminOn, http.MethodPost, "/drain", permission.AdminDrain, s.handleDrain)
	s.routeIf(adminOn, http.MethodGet, "/capabilities", permission.AdminCapabilities, s.handleCapabilities)
	s.routeIf(adminOn, http.MethodPost, "/membership/add", permission.AdminAddMember, s.handleMembershipAdd)
	s.routeIf(adminOn, http.MethodPost, "/membership/remove", permission.AdminRemoveMember, s.handleMembershipRemove)
	s.routeIf(adminOn, http.MethodPost, "/snapshot", permission.AdminSnapshot, s.handleSnapshot)
	s.routeIf(adminOn, http.MethodPost, "/admin/shards", permission.AdminStartShard, s.handleStartShard)
	s.routeIf(adminOn, http.MethodGet, "/admin/topology", permission.AdminTopology, s.handleGetTopology)
	s.routeIf(adminOn, http.MethodGet, "/admin/node-compaction", permission.AdminNodeCompaction, s.handleGetNodeCompaction)
	s.routeIf(adminOn, http.MethodPut, "/admin/node-compaction", permission.AdminNodeCompaction, s.handleSetNodeCompaction)

	// Admin per-shard path-param routes (always attached; the handler
	// nil-checks the admin service).
	s.route(http.MethodDelete, "/admin/shards/{id}", permission.AdminStopShard, s.handleAdminShardByID)
	s.route(http.MethodGet, "/admin/shards/{id}/state", permission.AdminShardState, s.handleAdminShardByID)

	// /metrics requires authentication but no specific permission: any
	// authenticated peer may scrape.
	s.authenticatedIf(s.gatherer != nil, http.MethodGet, "/metrics", s.handleMetrics)

	// MVCC routes: range/compact/revision self-guard on mvccSvc inside the
	// handler, so they are attached unconditionally. The KV scan route calls
	// kvSvc directly and is attached only when kvSvc is provided.
	s.route(http.MethodGet, "/kv/range", permission.KVRange, s.handleRange)
	s.route(http.MethodPost, "/kv/compact", permission.MVCCCompact, s.handleCompact)
	s.route(http.MethodGet, "/kv/revision", permission.MVCCCurrentRevision, s.handleCurrentRevision)
	s.route(http.MethodPost, "/kv/restore", permission.MVCCRestore, s.handleRestoreToRevision)
	s.routeIf(s.kvSvc != nil, http.MethodGet, "/kv/scan", permission.KVList, s.handleKVScan)

	// Guarded write verbs (go-qrdb#126). Registered unconditionally like the
	// MVCC routes above: the handlers resolve the KV service themselves and
	// report 501 when none is wired, matching the /kv data-plane guard.
	//
	// The permissions match the gRPC service's proto-declared options exactly
	// (qrdb.proto: PutIfAbsent and CompareAndSwap are kv:put, CompareAndDelete
	// is kv:delete) rather than introducing a guarded-specific permission: a
	// guarded put is still a put and a guarded delete is still a delete, so a
	// caller already trusted to write a key is trusted to write it
	// conditionally. Diverging here would mean the same identity could reach
	// the verb over gRPC and not over REST, which is the uniformity the
	// GuardedKVClient capability exists to promise.
	//
	// These are static POST paths under /kv, which the router resolves ahead
	// of the /kv/* wildcard below. No POST is registered on that wildcard, so
	// nothing that used to route is displaced.
	s.route(http.MethodPost, "/kv/put-if-absent", permission.KVPut, s.handleKVPutIfAbsent)
	s.route(http.MethodPost, "/kv/compare-and-delete", permission.KVDelete, s.handleKVCompareAndDelete)
	s.route(http.MethodPost, "/kv/compare-and-swap", permission.KVPut, s.handleKVCompareAndSwap)

	// KV data plane: /kv (list / missing-key) and /kv/{key}. Without a KV
	// service the routes still attach and report 501, preserving the legacy
	// nil-service guard.
	kvHandler := http.HandlerFunc(s.handleKV)
	if s.kvSvc == nil {
		kvHandler = s.notImplemented("kv")
	}
	s.route(http.MethodGet, "/kv", permission.KVGet, kvHandler)
	s.route(http.MethodPut, "/kv", permission.KVPut, kvHandler)
	s.route(http.MethodDelete, "/kv", permission.KVDelete, kvHandler)
	s.route(http.MethodGet, "/kv/*", permission.KVGet, kvHandler)
	s.route(http.MethodPut, "/kv/*", permission.KVPut, kvHandler)
	s.route(http.MethodDelete, "/kv/*", permission.KVDelete, kvHandler)

	// Transaction route.
	s.route(http.MethodPost, "/tx", permission.TransactionExecute, s.handleTransaction)

	// Batch routes.
	s.route(http.MethodPost, "/batch", permission.BatchExecute, s.handleBatch)
	s.route(http.MethodPost, "/batch/import", permission.BatchExecute, s.handleBatchImport)

	// Watch route (Server-Sent Events; the middleware chain never wraps the
	// ResponseWriter, so the handler's http.Flusher assertion holds).
	s.route(http.MethodGet, "/watch", permission.WatchWatch, s.handleWatch)

	// Lease and lock routes are attached UNCONDITIONALLY. Every handler
	// resolves its service first and answers 501 with the typed
	// not-implemented refusal when the node has none -- a raft-only family on
	// a standalone node, or a composition without it -- before anything is
	// proposed. Attaching them only when a service was wired made REST answer
	// a bare router 404 where gRPC, Unix, QUIC and MCP answer Unimplemented,
	// so a client could not tell "this node does not serve leases" from "no
	// such path".
	s.route(http.MethodPost, "/lease/grant", permission.LeaseGrant, s.handleLeaseGrant)
	s.route(http.MethodPost, "/lease/revoke", permission.LeaseRevoke, s.handleLeaseRevoke)
	s.route(http.MethodPost, "/lease/keepalive", permission.LeaseKeepAlive, s.handleLeaseKeepAlive)
	s.route(http.MethodGet, "/lease/ttl", permission.LeaseTTL, s.handleLeaseTimeToLive)
	s.route(http.MethodGet, "/lease/list", permission.LeaseList, s.handleLeaseList)

	s.route(http.MethodPost, "/lock/acquire", permission.LocksAcquire, s.handleLockAcquire)
	s.route(http.MethodPost, "/lock/trylock", permission.LocksAcquire, s.handleLockTryLock)
	s.route(http.MethodPost, "/lock/release", permission.LocksRelease, s.handleLockRelease)
	s.route(http.MethodPost, "/lock/extend", permission.LocksExtend, s.handleLockExtend)
	s.route(http.MethodGet, "/lock/status", permission.LocksStatus, s.handleLockStatus)
	s.route(http.MethodGet, "/lock/list", permission.LocksList, s.handleLockList)

	// Index routes.
	s.route(http.MethodPost, "/index/create", permission.IndexCreate, s.handleIndexCreate)
	s.route(http.MethodPost, "/index/drop", permission.IndexDrop, s.handleIndexDrop)
	s.route(http.MethodGet, "/index/query", permission.IndexQuery, s.handleIndexQuery)
	s.route(http.MethodGet, "/index/scan", permission.IndexScan, s.handleIndexScan)
	s.route(http.MethodGet, "/index/list", permission.IndexList, s.handleIndexList)

	// TimeSeries routes.
	s.route(http.MethodPost, "/ts/write", permission.TSWrite, s.handleTSWrite)
	s.route(http.MethodPost, "/ts/write-batch", permission.TSWriteBatch, s.handleTSWriteBatch)
	s.route(http.MethodPost, "/ts/write-point", permission.TSWritePoint, s.handleTSWritePoint)
	s.route(http.MethodGet, "/ts/query", permission.TSQuery, s.handleTSQuery)
	s.route(http.MethodPost, "/ts/query-rich", permission.TSRichQuery, s.handleTSRichQuery)
	s.route(http.MethodGet, "/ts/aggregate", permission.TSAggregate, s.handleTSAggregate)
	s.route(http.MethodGet, "/ts/metrics", permission.TSListMetrics, s.handleTSListMetrics)

	// TimeSeries per-metric path-param routes.
	s.route(http.MethodDelete, "/ts/series/{metric}", permission.TSDeleteSeries, s.handleTSSeries)
	s.route(http.MethodGet, "/ts/series/{metric}/retention", permission.TSGetRetention, s.handleTSSeries)
	s.route(http.MethodPut, "/ts/series/{metric}/retention", permission.TSSetRetention, s.handleTSSeries)

	// Vector routes.
	s.route(http.MethodPost, "/vector/namespace", permission.VectorNamespaceCreate, s.handleVectorCreateNamespace)
	s.route(http.MethodPost, "/vector/namespace/delete", permission.VectorNamespaceDelete, s.handleVectorDeleteNamespace)
	s.route(http.MethodGet, "/vector/namespaces", permission.VectorNamespaceList, s.handleVectorListNamespaces)
	s.route(http.MethodGet, "/vector/namespace/info", permission.VectorNamespaceInfo, s.handleVectorGetNamespaceInfo)
	s.route(http.MethodPost, "/vector/upsert", permission.VectorUpsert, s.handleVectorUpsert)
	s.route(http.MethodGet, "/vector/get", permission.VectorGet, s.handleVectorGet)
	s.route(http.MethodPost, "/vector/delete", permission.VectorDelete, s.handleVectorDelete)
	s.route(http.MethodPost, "/vector/batch/upsert", permission.VectorBatchUpsert, s.handleVectorBatchUpsert)
	s.route(http.MethodPost, "/vector/batch/delete", permission.VectorBatchDelete, s.handleVectorBatchDeleteVectors)
	s.route(http.MethodGet, "/vector/list", permission.VectorList, s.handleVectorListVectors)
	s.route(http.MethodPost, "/vector/metadata", permission.VectorUpdateMetadata, s.handleVectorUpdateMetadata)
	s.route(http.MethodPost, "/vector/search/similar", permission.VectorSearchSimilar, s.handleVectorSearchSimilar)
	s.route(http.MethodPost, "/vector/search/metadata", permission.VectorSearchByMetadata, s.handleVectorSearchByMetadata)
	s.route(http.MethodPost, "/vector/search/hybrid", permission.VectorHybridSearch, s.handleVectorHybridSearch)
	s.route(http.MethodPost, "/vector/search/by-id", permission.VectorSearchByID, s.handleVectorSearchByID)
	s.route(http.MethodPost, "/vector/index/rebuild", permission.VectorRebuildIndex, s.handleVectorRebuildIndex)
	s.route(http.MethodGet, "/vector/index/rebuild/progress", permission.VectorGetRebuildProg, s.handleVectorGetRebuildProgress)
	s.route(http.MethodGet, "/vector/index/stats", permission.VectorGetIndexStats, s.handleVectorGetIndexStats)
	s.route(http.MethodPost, "/vector/compact", permission.VectorCompact, s.handleVectorCompactNamespace)
	s.route(http.MethodGet, "/vector/compact/progress", permission.VectorGetCompactProg, s.handleVectorGetCompactionProgress)

	// Embedding routes.
	embeddingOn := s.embeddingSvc != nil
	s.routeIf(embeddingOn, http.MethodPost, "/v1/embeddings", permission.EmbeddingEmbed, s.handleEmbedText)
	s.routeIf(embeddingOn, http.MethodPost, "/v1/embeddings/batch", permission.EmbeddingEmbed, s.handleBatchEmbed)
	s.routeIf(embeddingOn, http.MethodGet, "/v1/embedding/models", permission.EmbeddingListModels, s.handleListEmbeddingModels)

	// RAG routes.
	ragOn := s.ragSvc != nil
	s.routeIf(ragOn, http.MethodPost, "/v1/rag/ingest", permission.RAGIngest, s.handleRAGIngest)
	s.routeIf(ragOn, http.MethodPost, "/v1/rag/query", permission.RAGQuery, s.handleRAGQuery)
	s.routeIf(ragOn, http.MethodPost, "/v1/rag/documents/delete", permission.RAGDeleteDocument, s.handleRAGDeleteDocument)
	s.routeIf(ragOn, http.MethodGet, "/v1/rag/documents", permission.RAGListDocuments, s.handleRAGListDocuments)

	// Database management routes.
	dbOn := s.databaseSvc != nil
	s.routeIf(dbOn, http.MethodPost, "/v1/databases", permission.DatabasesCreate, s.handleCreateDatabase)
	s.routeIf(dbOn, http.MethodGet, "/v1/databases/list", permission.DatabasesList, s.handleListDatabases)

	// Database per-database path-param routes (the handler nil-checks the
	// database service). The config sub-resources are updated with PATCH.
	s.route(http.MethodGet, "/v1/databases/{name}", permission.DatabasesRead, s.handleDatabaseByName)
	s.route(http.MethodDelete, "/v1/databases/{name}", permission.DatabasesDelete, s.handleDatabaseByName)
	s.route(http.MethodPatch, "/v1/databases/{name}/barrier", permission.DatabasesUpdate, s.handleDatabaseByName)
	s.route(http.MethodPatch, "/v1/databases/{name}/connection", permission.DatabasesUpdate, s.handleDatabaseByName)
	s.route(http.MethodPatch, "/v1/databases/{name}/sql", permission.DatabasesUpdate, s.handleDatabaseByName)
	// Setting a compaction policy is compacting on a schedule: mvcc:compact,
	// not databases:update.
	s.route(http.MethodPatch, "/v1/databases/{name}/compaction", permission.MVCCCompact, s.handleDatabaseByName)
	s.route(http.MethodPatch, "/v1/databases/{name}/etcd", permission.DatabasesUpdate, s.handleDatabaseByName)
	s.route(http.MethodPatch, "/v1/databases/{name}/protocol", permission.DatabasesUpdate, s.handleDatabaseByName)
	s.route(http.MethodPatch, "/v1/databases/{name}/raft", permission.DatabasesUpdate, s.handleDatabaseByName)
	s.route(http.MethodPatch, "/v1/databases/{name}/scale", permission.DatabasesScale, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/seal", permission.DatabasesSeal, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/unseal", permission.DatabasesUnseal, s.handleDatabaseByName)
	s.route(http.MethodGet, "/v1/databases/{name}/barrier/status", permission.DatabasesBarrierStatus, s.handleDatabaseByName)
	// Shamir custody of a database barrier (docs/databases/api.md). Claims and
	// quorum submissions are identity-gated: the holder's certificate or
	// factor proof is the credential.
	s.route(http.MethodPost, "/v1/databases/{name}/barrier/officers", permission.DatabasesOfficerRegister, s.handleDatabaseByName)
	s.route(http.MethodGet, "/v1/databases/{name}/barrier/officers", permission.DatabasesOfficerList, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/barrier/shamir/initialize", permission.DatabasesInitializeShamir, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/barrier/officers/{username}/claim/begin", permission.IdentityGated, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/barrier/officers/{username}/claim/complete", permission.IdentityGated, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/barrier/quorum/begin", permission.IdentityGated, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/barrier/quorum/submit", permission.IdentityGated, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/barrier/rekey/start", permission.DatabasesRekey, s.handleDatabaseByName)
	s.route(http.MethodPost, "/v1/databases/{name}/barrier/officers/revoke", permission.DatabasesOfficerRevoke, s.handleDatabaseByName)

	// Collection management routes. Attached when either the singleton is
	// wired or a registry is configured, for the reason the lease routes give
	// above: the per-(tenant, database) collection servicer resolves at
	// request time, so gating on the singleton alone would answer
	// StatusNotImplemented before collectionSvcFor could resolve one.
	collOn := s.collectionSvc != nil || s.serviceRegistry != nil
	s.routeIf(collOn, http.MethodPost, "/v1/collections", permission.CollectionsCreate, s.handleCreateCollection)
	s.routeIf(collOn, http.MethodGet, "/v1/collections/list", permission.CollectionsList, s.handleListCollections)

	// Collection / document path-param routes (the handler nil-checks the
	// collection service).
	s.route(http.MethodGet, "/v1/collections/{database}/{name}", permission.CollectionsRead, s.handleCollectionByPath)
	s.route(http.MethodDelete, "/v1/collections/{database}/{name}", permission.CollectionsDrop, s.handleCollectionByPath)
	s.route(http.MethodPost, "/v1/collections/{database}/{name}/documents", permission.CollectionsInsert, s.handleCollectionByPath)
	s.route(http.MethodGet, "/v1/collections/{database}/{name}/documents", permission.CollectionsList, s.handleCollectionByPath)
	s.route(http.MethodGet, "/v1/collections/{database}/{name}/documents/{id}", permission.CollectionsRead, s.handleCollectionByPath)
	s.route(http.MethodPut, "/v1/collections/{database}/{name}/documents/{id}", permission.CollectionsUpdate, s.handleCollectionByPath)
	s.route(http.MethodDelete, "/v1/collections/{database}/{name}/documents/{id}", permission.CollectionsDelete, s.handleCollectionByPath)

	// Collection catch-alls: the legacy dispatcher routed EVERY path under
	// /v1/collections/ to handleCollectionByPath, which performs its own
	// parsing (trailing slashes, missing segments -> 400/404, method checks
	// with precise Allow headers). The catch-all keeps those semantics for
	// paths the templated routes above do not match.
	s.route(http.MethodGet, "/v1/collections/*", permission.CollectionsRead, s.handleCollectionByPath)
	s.route(http.MethodPost, "/v1/collections/*", permission.CollectionsInsert, s.handleCollectionByPath)
	s.route(http.MethodPut, "/v1/collections/*", permission.CollectionsUpdate, s.handleCollectionByPath)
	s.route(http.MethodDelete, "/v1/collections/*", permission.CollectionsDelete, s.handleCollectionByPath)

	// SQL query routes. /sql/query is the canonical data-plane mount used
	// by the TypeScript SDK; /v1/sql/query is the mount used by the Go
	// database/sql driver. Both dispatch to the same handler. The SQL layer
	// is a read-through over the KV range surface, hence KVRange.
	sqlOn := s.sqlSvc != nil
	s.routeIf(sqlOn, http.MethodPost, "/sql/query", permission.KVRange, s.handleSQLQuery)
	s.routeIf(sqlOn, http.MethodPost, "/v1/sql/query", permission.KVRange, s.handleSQLQuery)

	// User management routes.
	userOn := s.userSvc != nil
	s.routeIfTarget(userOn, http.MethodPost, "/users", permission.UsersCreate, createUserTarget, s.handleCreateUser)
	s.routeIfTarget(userOn, http.MethodGet, "/users/list", permission.UsersList, authmw.RESTQueryTenant("tenantID"), s.handleListUsers)

	// User path-param routes (the handler nil-checks the user service).
	s.routeTarget(http.MethodGet, "/users/{id}", permission.UsersRead, authmw.RESTPathUser("id"), s.handleUser)
	s.routeTarget(http.MethodPut, "/users/{id}", permission.UsersUpdate, authmw.RESTPathUser("id"), s.handleUser)
	s.routeTarget(http.MethodDelete, "/users/{id}", permission.UsersDelete, authmw.RESTPathUser("id"), s.handleUser)
	s.routeTarget(http.MethodPost, "/users/{id}/enable", permission.UsersEnable, authmw.RESTPathUser("id"), s.handleUser)
	s.routeTarget(http.MethodPost, "/users/{id}/disable", permission.UsersDisable, authmw.RESTPathUser("id"), s.handleUser)
	s.routeTarget(http.MethodPost, "/users/{id}/cert-bindings", permission.UsersAddCertBinding, authmw.RESTPathUser("id"), s.handleUser)
	s.routeTarget(http.MethodDelete, "/users/{id}/cert-bindings/{thumbprint}", permission.UsersRemoveCertBinding, authmw.RESTPathUser("id"), s.handleUser)
	s.route(http.MethodGet, "/users/by-cert-thumbprint/{thumbprint}", permission.UsersGetByCertThumbprint, s.handleUser)
	s.routeTarget(http.MethodGet, "/users/by-username/{username}", permission.UsersRead, authmw.RESTQueryTenant("tenantID"), s.handleUser)
	s.routeTarget(http.MethodPost, "/users/lookup", permission.UsersRead, lookupUserTarget, s.handleUser)

	// Role management routes.
	rolesOn := s.roleSvc != nil
	s.routeIfTarget(rolesOn, http.MethodPost, "/roles", permission.RolesCreate, createRoleTarget, s.handleCreateRole)
	s.routeIfTarget(rolesOn, http.MethodGet, "/roles/list", permission.RolesList, authmw.RESTQueryTenant("tenantID"), s.handleListRoles)
	s.routeIfTarget(rolesOn, http.MethodGet, "/roles/by-name", permission.RolesRead, authmw.RESTQueryTenant("tenant_id"), s.handleGetRoleByName)
	s.routeIf(rolesOn, http.MethodPost, "/roles/resolve-permissions", permission.PermissionsResolve, s.handleResolvePermissions)
	// Reconcile carries roles:create, not a verb of its own. Reconciliation IS
	// the seeding operation applied to an existing tenant, and a dedicated
	// permission would be missing from every role seeded before it existed —
	// so nobody would hold the grant needed to repair that exact absence.
	s.routeIfTarget(rolesOn, http.MethodPost, "/roles/reconcile", permission.RolesCreate, reconcileRolesTarget, s.handleReconcileRoles)
	s.routeIfTarget(rolesOn, http.MethodPost, "/roles/assign", permission.RolesAssign, assignRoleTarget, s.handleAssignRole)
	s.routeIfTarget(rolesOn, http.MethodPost, "/roles/unassign", permission.RolesUnassign, assignRoleTarget, s.handleUnassignRole)
	s.routeIf(rolesOn, http.MethodGet, "/permissions", permission.PermissionsList, s.handleListPermissions)

	// Role path-param routes (the handler nil-checks the role service).
	s.routeTarget(http.MethodGet, "/roles/{id}", permission.RolesRead, authmw.RESTPathRole("id"), s.handleRole)
	s.routeTarget(http.MethodPut, "/roles/{id}", permission.RolesUpdate, authmw.RESTPathRole("id"), s.handleRole)
	s.routeTarget(http.MethodDelete, "/roles/{id}", permission.RolesDelete, authmw.RESTPathRole("id"), s.handleRole)

	// Without a role service the legacy dispatcher still routed the /roles/
	// keyword paths to handleRole, which reports 501. Attach the same
	// fallback (router-only: the permissions above are already registered).
	if !rolesOn {
		for _, mp := range [...]struct{ method, pattern string }{
			{http.MethodGet, "/roles/list"},
			{http.MethodGet, "/roles/by-name"},
			{http.MethodPost, "/roles/resolve-permissions"},
			{http.MethodPost, "/roles/reconcile"},
			{http.MethodPost, "/roles/assign"},
			{http.MethodPost, "/roles/unassign"},
		} {
			s.router.Method(mp.method, mp.pattern, http.HandlerFunc(s.handleRole))
		}
	}

	// Tenant management routes. Attached unconditionally so the router
	// always returns a meaningful status (201/200/501) rather than 404.
	// Handlers return 501 when tenantSvc is nil (seal barrier not configured).
	s.route(http.MethodPost, "/v1/tenants", permission.TenantsCreate, s.handleCreateTenant)
	s.route(http.MethodGet, "/v1/tenants/list", permission.TenantsList, s.handleListTenants)
	// Retired tenant ids. Both are two static segments below /v1/tenants/, so
	// they shadow no {id} route: /v1/tenants/deleted still reads the tenant
	// named "deleted", and no /v1/tenants/{id}/list or /reconcile exists.
	s.route(http.MethodGet, "/v1/tenants/deleted/list", permission.TenantsListDeleted, s.handleListDeletedTenants)
	s.route(http.MethodPost, "/v1/tenants/deleted/reconcile", permission.TenantsReconcileDeleted, s.handleReconcileDeletedTenants)

	// Tenant path-param routes, including the per-tenant barrier plane. The
	// barrier sub-routes carry tenant-scoped (ScopeTenantOnly) permissions;
	// the handler additionally validates the {id} against the caller's
	// tenant. Officer share-claim routes are identity-gated: the registered
	// officer's mTLS certificate is the credential and the handler enforces
	// registry lookup, thumbprint match, and nonce signature.
	s.route(http.MethodGet, "/v1/tenants/{id}", permission.TenantsRead, s.handleTenantByID)
	s.route(http.MethodPut, "/v1/tenants/{id}", permission.TenantsUpdate, s.handleTenantByID)
	s.route(http.MethodDelete, "/v1/tenants/{id}", permission.TenantsDelete, s.handleTenantByID)
	// Quotas and entitlements are the tenant's plan: platform-operator
	// permissions (ScopeCross), so a tenant Admin cannot raise its own limits.
	s.route(http.MethodPost, "/v1/tenants/{id}/quotas", permission.TenantsSetQuotas, s.handleTenantByID)
	s.route(http.MethodPatch, "/v1/tenants/{id}/entitlements", permission.TenantsSetEntitlements, s.handleTenantByID)
	// barrier/status, like barrier/init and barrier/unseal below, is
	// IdentityGated at the ROUTE level: a self-managed dedicated-isolation
	// tenant's IAM is encrypted behind its own barrier, so on a node where
	// that barrier is sealed (or its key not yet delivered) NO role can be
	// resolved to satisfy an RBAC check — yet status/unseal are precisely
	// the operations that end that state. The handlers run the full RBAC
	// check first and fall back to the tenant's own authenticated identity
	// ONLY while the middleware reports the tenant's IAM as unreachable
	// (middleware.IdentityIAMSealed); see authorizeTenantBarrierSealedOp.
	s.route(http.MethodGet, "/v1/tenants/{id}/barrier/status", permission.IdentityGated, s.handleTenantByID)
	s.routeTarget(http.MethodGet, "/v1/tenants/{id}/barrier/officers", permission.TenantBarrierOfficerList, authmw.RESTPathTenant("id"), s.handleTenantByID)
	// barrier/init is IdentityGated at the ROUTE level because its real
	// authorization is conditional and lives in the handler
	// (authorizeTenantBarrierInit): an initialized barrier demands the full
	// tenant_barrier:init RBAC check, while a NEVER-initialized barrier
	// accepts the tenant's own authenticated identity — trust-on-first-init.
	// Without that window a self-managed tenant can never be provisioned:
	// its IAM lives behind the very barrier this endpoint initializes, so no
	// role able to satisfy the RBAC check can exist before the call.
	s.route(http.MethodPost, "/v1/tenants/{id}/barrier/init", permission.IdentityGated, s.handleTenantByID)
	s.route(http.MethodPost, "/v1/tenants/{id}/barrier/unseal", permission.IdentityGated, s.handleTenantByID)
	s.routeTarget(http.MethodPost, "/v1/tenants/{id}/barrier/seal", permission.TenantBarrierSeal, authmw.RESTPathTenant("id"), s.handleTenantByID)
	s.routeTarget(http.MethodPost, "/v1/tenants/{id}/barrier/officers", permission.TenantBarrierOfficerRegister, authmw.RESTPathTenant("id"), s.handleTenantByID)
	s.route(http.MethodPost, "/v1/tenants/{id}/barrier/officers/{username}/claim/begin", permission.IdentityGated, s.handleTenantByID)
	s.route(http.MethodPost, "/v1/tenants/{id}/barrier/officers/{username}/claim/complete", permission.IdentityGated, s.handleTenantByID)
	s.route(http.MethodPost, "/v1/tenants/{id}/barrier/quorum/begin", permission.IdentityGated, s.handleTenantByID)
	s.route(http.MethodPost, "/v1/tenants/{id}/barrier/quorum/submit", permission.IdentityGated, s.handleTenantByID)
	s.routeTarget(http.MethodPost, "/v1/tenants/{id}/barrier/rekey/start", permission.TenantBarrierRekey, authmw.RESTPathTenant("id"), s.handleTenantByID)
	s.routeTarget(http.MethodPost, "/v1/tenants/{id}/barrier/officers/revoke", permission.TenantBarrierOfficerRevoke, authmw.RESTPathTenant("id"), s.handleTenantByID)

	// The legacy dispatcher routed the bare "/v1/tenants/" (trailing slash,
	// empty id) to handleTenantByID, which reports 400 missing-parameter.
	s.route(http.MethodGet, "/v1/tenants/", permission.TenantsRead, s.handleTenantByID)

	// Backup management routes.
	backupOn := s.backupSvc != nil
	s.routeIf(backupOn, http.MethodPost, "/v1/backups", permission.BackupCreate, s.handleCreateBackup)
	s.routeIf(backupOn, http.MethodGet, "/v1/backups/list", permission.BackupList, s.handleListBackups)
	s.routeIf(backupOn, http.MethodPost, "/v1/backups/restore", permission.BackupRestore, s.handleRestoreBackup)

	// Backup path-param routes (the handler nil-checks the backup service).
	s.route(http.MethodGet, "/v1/backups/{id}", permission.BackupRead, s.handleBackupByID)
	s.route(http.MethodDelete, "/v1/backups/{id}", permission.BackupDelete, s.handleBackupByID)
	s.route(http.MethodPost, "/v1/backups/{id}/verify", permission.BackupVerify, s.handleBackupByID)

	// Cluster rebalance routes.
	rebalanceOn := s.rebalanceSvc != nil
	s.routeIf(rebalanceOn, http.MethodPost, "/v1/rebalance/plan", permission.RebalancePlan, s.handleRebalancePlan)
	s.routeIf(rebalanceOn, http.MethodPost, "/v1/rebalance/execute", permission.RebalanceExecute, s.handleRebalanceExecute)
	s.routeIf(rebalanceOn, http.MethodGet, "/v1/rebalance/report", permission.RebalanceReport, s.handleImbalanceReport)

	// Rebalance per-shard stats route (the handler nil-checks the service).
	s.route(http.MethodGet, "/v1/rebalance/shards/{id}", permission.RebalanceStats, s.handleRebalanceShardStats)

	// Auth factor routes. Attached unconditionally; handlers return 501
	// when authFactorSvc is nil (service not configured).
	s.route(http.MethodGet, "/v1/authfactors/list", permission.AuthFactorList, s.handleAuthFactorList)
	s.route(http.MethodPost, "/v1/authfactors/{factor}/enroll", permission.AuthFactorEnroll, s.handleAuthFactorByPath)
	s.route(http.MethodPost, "/v1/authfactors/{factor}/challenge", permission.AuthFactorChallenge, s.handleAuthFactorByPath)
	s.route(http.MethodPost, "/v1/authfactors/{factor}/verify", permission.AuthFactorVerify, s.handleAuthFactorByPath)
	s.route(http.MethodDelete, "/v1/authfactors/{factor}", permission.AuthFactorRevoke, s.handleAuthFactorByPath)

	// User auth factor routes (M14). Attached unconditionally; handlers
	// return 501 when userAuthFactorSvc is nil.
	s.route(http.MethodPost, "/v1/users/factors", permission.UserAuthFactorsAdd, s.handleUserAuthFactorAdd)
	s.route(http.MethodGet, "/v1/users/factors/list", permission.UserAuthFactorsList, s.handleUserAuthFactorList)
	s.route(http.MethodPost, "/v1/users/factors/login", permission.UserAuthFactorsLogin, s.handleUserAuthFactorLogin)
	s.route(http.MethodPost, "/v1/users/factors/rotate", permission.UserAuthFactorsRotate, s.handleUserAuthFactorRotate)
	s.route(http.MethodGet, "/v1/users/factors/{id}", permission.UserAuthFactorsGet, s.handleUserAuthFactorByPath)
	s.route(http.MethodDelete, "/v1/users/factors/{id}", permission.UserAuthFactorsRemove, s.handleUserAuthFactorByPath)
	s.route(http.MethodPut, "/v1/users/factors/{id}/label", permission.UserAuthFactorsRelabel, s.handleUserAuthFactorByPath)

	// Certificate management routes.
	certsOn := s.certProv != nil
	s.routeIf(certsOn, http.MethodPost, "/certs/generate", permission.CertsGenerate, s.handleGenerateCert)
	s.routeIf(certsOn, http.MethodPost, "/certs/export", permission.CertsExport, s.handleExportCert)
	s.routeIf(certsOn, http.MethodPost, "/certs/revoke", permission.CertsRevoke, s.handleRevokeCert)
	s.routeIf(certsOn, http.MethodGet, "/certs/list", permission.CertsList, s.handleListCerts)

	// CA bootstrap endpoint (public, no auth required: joining nodes fetch
	// the trust anchor before they hold credentials).
	// Claimed ONLY when exactly one source supplies the bundle. Claimed
	// unconditionally, the path was reserved on a node that served nothing
	// there, and a host that serves its own first-contact bundle (xkmsd)
	// collided with the reservation at boot. Two sources are refused at Start.
	if s.caBootstrapSources == 1 {
		s.public(http.MethodGet, "/v1/ca/bootstrap", s.handleCABootstrap)
	}

	// Cluster peer key-transport endpoints. Both routes are pre-exempt from
	// the auth middleware (exact-path allowlist in middleware/rest.go) and on
	// the SealedWindowAllowlist (/v1/cluster/ prefix) so they work in every
	// daemon state. The receiver self-authorises via node-cert verification
	// and placement-table gate.
	var keyDeliveryH, nodeKeyH http.HandlerFunc
	if s.clusterKeyReceiver != nil {
		keyDeliveryH = s.clusterKeyReceiver.Handler()
		nodeKeyH = s.clusterKeyReceiver.NodeKeyHandler()
	}
	s.publicIf(s.clusterKeyReceiver != nil, http.MethodPost, "/v1/cluster/keydelivery", keyDeliveryH)
	s.publicIf(s.clusterKeyReceiver != nil, http.MethodGet, "/v1/cluster/nodekey", nodeKeyH)

	// Barrier lifecycle routes.
	barrierOn := s.barrierSvc != nil
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/initialize", permission.SealBootstrap, s.handleBarrierInitialize)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/unseal", permission.SealUnseal, s.handleBarrierUnseal)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/seal", permission.SealSeal, s.handleBarrierSeal)
	s.routeIf(barrierOn, http.MethodGet, "/v1/barrier/status", permission.SealStatus, s.handleBarrierStatus)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/cluster-unseal", permission.SealUnseal, s.handleBarrierClusterUnseal)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/shamir/initialize", permission.SealInitializeShamir, s.handleBarrierShamirInitialize)
	// Registered unconditionally, not gated on barrierOn or s.encMigrator
	// (go-qrdb#558): the handler's own encMigrator nil-check already answers
	// with a typed 501 when unavailable (no barrier / standalone), and gating
	// the ROUTE itself made this transport alone 404 for that condition
	// instead of returning it -- gRPC/Unix/QUIC/MCP always expose the
	// method/tool and let the same nil-check decide, never depending on
	// barrierOn.
	s.routeIf(true, http.MethodPost, "/v1/barrier/enable-encryption", permission.SealEnableEncryption, s.handleBarrierEnableEncryption)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/recovery/generate", permission.SealGenerateRecovery, s.handleBarrierRecoveryGenerate)
	s.routeIf(barrierOn, http.MethodDelete, "/v1/barrier/recovery", permission.SealGenerateRecovery, s.handleBarrierRecoveryDelete)
	s.routeIf(barrierOn, http.MethodGet, "/v1/barrier/recovery/status", permission.SealStatus, s.handleBarrierRecoveryStatus)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/recovery/officers", permission.SealOfficerRegister, s.handleBarrierRecoveryOfficerRegister)

	// Security Officer registration/listing on the shared /v1/barrier/officers
	// path. Attached unconditionally (mirroring the legacy dispatcher): a
	// server without a barrier service reports 501 rather than 404.
	officerListH := http.HandlerFunc(s.handleBarrierOfficerList)
	officerRegisterH := http.HandlerFunc(s.handleBarrierOfficerRegister)
	if !barrierOn {
		officerListH = s.notImplemented("barrier")
		officerRegisterH = s.notImplemented("barrier")
	}
	s.route(http.MethodGet, "/v1/barrier/officers", permission.SealOfficerList, officerListH)
	s.route(http.MethodPost, "/v1/barrier/officers", permission.SealOfficerRegister, officerRegisterH)

	// Officer share-claim path-param routes (identity-gated: the registered
	// officer's mTLS certificate is the credential; the handler enforces
	// registry lookup, thumbprint match, and nonce signature).
	s.route(http.MethodPost, "/v1/barrier/officers/{username}/claim/begin", permission.IdentityGated, s.handleBarrierOfficersByPath)
	s.route(http.MethodPost, "/v1/barrier/officers/{username}/claim/complete", permission.IdentityGated, s.handleBarrierOfficersByPath)
	s.route(http.MethodPost, "/v1/barrier/recovery/officers/{username}/claim/begin", permission.IdentityGated, s.handleBarrierOfficersByPath)
	s.route(http.MethodPost, "/v1/barrier/recovery/officers/{username}/claim/complete", permission.IdentityGated, s.handleBarrierOfficersByPath)

	// Holder quorum rounds. Begin and submit are identity-gated: the
	// holder's factor proof is the credential, verified against the roster
	// the wrapped root key is sealed under, and both are reachable while the
	// barrier is sealed. Rekey and revocation open rounds under RBAC and SO
	// PIN authorization.
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/quorum/begin", permission.IdentityGated, s.handleBarrierQuorumBegin)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/quorum/submit", permission.IdentityGated, s.handleBarrierQuorumSubmit)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/rekey/start", permission.SealRekey, s.handleBarrierRekeyStart)
	s.routeIf(barrierOn, http.MethodPost, "/v1/barrier/officers/revoke", permission.SealOfficerRevoke, s.handleBarrierOfficerRevoke)

	// Ceremony lifecycle routes. Share holders claim through the barrier
	// officer routes above.
	ceremonyOn := s.ceremonySvc != nil
	s.routeIf(ceremonyOn, http.MethodPost, "/v1/ceremony/initialize", permission.CeremonyInitialize, s.handleCeremonyInitialize)
	s.routeIf(ceremonyOn, http.MethodGet, "/v1/ceremony/status", permission.CeremonyStatus, s.handleCeremonyStatus)
	s.routeIf(ceremonyOn, http.MethodPost, "/v1/ceremony/sign-csr", permission.CeremonySignCSR, s.handleCeremonySignCSR)

	// Credential management routes.
	credOn := s.credentialSvc != nil
	s.routeIf(credOn, http.MethodPost, "/v1/credentials/submit", permission.CredentialsSubmit, s.handleCredentialSubmit)
	s.routeIf(credOn, http.MethodPost, "/v1/credentials/store", permission.CredentialsStore, s.handleCredentialStore)
	s.routeIf(credOn, http.MethodGet, "/v1/credentials/strategy", permission.CredentialsStrategy, s.handleCredentialStrategy)
	s.routeIf(credOn, http.MethodGet, "/v1/credentials", permission.CredentialsList, s.handleCredentialList)

	// Credential path-param routes (the handler nil-checks the service).
	s.route(http.MethodDelete, "/v1/credentials/{name}", permission.CredentialsDelete, s.handleCredentialByPath)
	s.route(http.MethodPost, "/v1/credentials/{name}/rotate", permission.CredentialsRotate, s.handleCredentialByPath)
	s.route(http.MethodPost, "/v1/credentials/{name}/gc", permission.CredentialsGCExpired, s.handleCredentialByPath)
	s.route(http.MethodGet, "/v1/credentials/{name}/versions", permission.CredentialsListVersions, s.handleCredentialByPath)
	s.route(http.MethodGet, "/v1/credentials/{name}/versions/{version}", permission.CredentialsRetrieveVersion, s.handleCredentialByPath)
	s.route(http.MethodGet, "/v1/credentials/{name}/value", permission.CredentialsRetrieve, s.handleCredentialByPath)

	// Tenant DEK keyring routes. Every route acts on the caller's own tenant.
	dekOn := s.dekSvc != nil
	s.routeIf(dekOn, http.MethodGet, "/v1/dek/status", permission.DEKStatus, s.handleDEKStatus)
	s.routeIf(dekOn, http.MethodPost, "/v1/dek/rotate", permission.DEKRotate, s.handleDEKRotate)
	s.routeIf(dekOn, http.MethodPost, "/v1/dek/retire", permission.DEKRetire, s.handleDEKRetire)
	s.routeIf(dekOn, http.MethodPost, "/v1/dek/encrypt", permission.DEKEncrypt, s.handleDEKEncrypt)
	s.routeIf(dekOn, http.MethodPost, "/v1/dek/decrypt", permission.DEKDecrypt, s.handleDEKDecrypt)
	s.routeIf(dekOn, http.MethodPost, "/v1/dek/rewrap", permission.DEKRewrap, s.handleDEKRewrap)

	// Audit event retrieval route.
	s.routeIf(s.auditLogger != nil, http.MethodGet, "/v1/audit/events", permission.AuditQuery, s.handleAuditQuery)
	s.routeIf(s.auditLogger != nil, http.MethodGet, "/v1/audit/verify", permission.AuditVerify, s.handleAuditVerify)
}

// dispatch serves a request through the chi router. It is retained as the
// historical single entry point: many tests (and the embeddable surface)
// invoke it directly, and it serves the full route table WITHOUT the
// authn/authz middleware chain — exactly like the legacy map-based
// dispatcher it replaced.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// handleCABootstrap serves the cluster CA certificate for joining nodes, as
// PEM with content type "application/pem-certificate-chain". It never serves
// key material: the cluster CA private key does not leave the node holding
// it. This endpoint is
// public (exempted from authentication in the middleware) because joining
// nodes do not yet have TLS credentials.
func (s *Server) handleCABootstrap(w http.ResponseWriter, r *http.Request) {
	if s.caBootstrap == nil {
		// Unreachable through the router (the route is registered only with
		// a source), but a direct call must not dereference nil.
		http.Error(w, "CA bootstrap not configured", http.StatusServiceUnavailable)
		return
	}
	bundle, err := s.caBootstrap(r.Context())
	if err != nil {
		slog.Warn("rest: CA bootstrap bundle unavailable", slog.Any("error", err))
		http.Error(w, "CA bootstrap bundle unavailable", http.StatusServiceUnavailable)
		return
	}
	if len(bundle) == 0 {
		http.Error(w, "CA bootstrap bundle unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/pem-certificate-chain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bundle)
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
