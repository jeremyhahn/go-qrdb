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

// Package server provides the multi-protocol server orchestrator for QRDB.
// It manages the lifecycle of REST, gRPC, Unix socket, MCP, MySQL,
// PostgreSQL, and etcd servers based on the configuration, starting them
// sequentially with rollback on failure and stopping them gracefully in
// reverse order.
package server

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
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	quicapi "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	unixapi "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/joinadvertise"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// SQLProtocolServer is the interface implemented by MySQL and PostgreSQL
// wire protocol servers. Build-tagged files register start functions that
// create concrete implementations.
type SQLProtocolServer interface {
	Start() error
	Stop()
	Addr() string
}

// EtcdProtocolServer is the interface implemented by the per-database
// etcd wire protocol server. Build-tagged files register start/stop
// functions that create concrete implementations.
type EtcdProtocolServer interface {
	Start() error
	Stop()
	Addr() string
}

// startSQLFn is a function that creates and starts a SQL protocol server.
// Build-tagged files register these via init().
type startSQLFn func(s *Server) error

// stopSQLFn is a function that stops a SQL protocol server.
type stopSQLFn func(s *Server)

// Package-level function variables set by build-tagged init() functions.
// When the build tag is absent, the function remains nil and the protocol
// is silently skipped.
var (
	startMySQLFn startSQLFn
	stopMySQLFn  stopSQLFn
	startPgSQLFn startSQLFn
	stopPgSQLFn  stopSQLFn
)

// startEtcdFn is a function that creates and starts an etcd protocol server
// for a specific database.
type startEtcdFn func(s *Server, dbName string, cfg *database.EtcdConfig) (EtcdProtocolServer, error)

// stopEtcdFn is a function that stops an etcd protocol server.
type stopEtcdFn func(srv EtcdProtocolServer)

var (
	startEtcdServerFn startEtcdFn
	stopEtcdServerFn  stopEtcdFn
)

// buildSQLServiceFn is a function that constructs the shared ad-hoc SQL
// query service consumed by every transport (REST, gRPC, QUIC, Unix, MCP).
type buildSQLServiceFn func(s *Server) service.SQLServicer

// newSQLServiceFn is set by init() in the qrdb_sql build-tagged
// server_sql.go. When the build tag is absent it remains nil, no
// transport receives a SQL service, and each transport surfaces its
// existing nil-service error for the /sql/query surface.
var newSQLServiceFn buildSQLServiceFn

// Server orchestrates the REST, gRPC, Unix socket, MCP, MySQL,
// PostgreSQL, and etcd API servers for QRDB. It starts and stops servers
// based on the protocol configuration and manages their lifecycles with
// rollback on startup failure.
type Server struct {
	// sealedListener is the boot-time sealed-window listener's probe, built
	// once in New so its Admission is shared by all of the listener's
	// transports. Nil on any other server.
	sealedListener sealedwindow.ListenerProbe

	// forwardingUntrusted logs, once, that replica forwarding was asked for
	// without a CA pool to verify a replica with.
	forwardingUntrusted sync.Once

	// inProcess is the embedded transport's replica forwarder; see
	// ReplicaForwarder.
	inProcess inProcessForwarding

	config      *config.Config
	kvSvc       service.KVServicer
	adminSvc    service.AdminServicer
	restSrv     *rest.Server
	grpcSrv     *grpcapi.Server
	quicSrv     *quicapi.Server
	unixSrv     *unixapi.Server
	mcpSrv      *mcpapi.Server
	mysqlSrv    SQLProtocolServer
	pgsqlSrv    SQLProtocolServer
	etcdServers sync.Map // database name (string) -> EtcdProtocolServer
	wg          sync.WaitGroup
	logger      *slog.Logger
	opts        serverOptions

	// sweeperCancel stops the collection sweeper (see collection_sweeper.go);
	// nil when none is running.
	sweeperCancel context.CancelFunc

	// compactorCancel stops the compaction scheduler (see
	// compaction_scheduler.go); nil when none is running.
	compactorCancel context.CancelFunc

	// retentionCancel stops the time-series retention scheduler (see
	// retention_scheduler.go); nil when none is running.
	retentionCancel context.CancelFunc
}

// serverOptions holds optional dependencies injected via ServerOption.
type serverOptions struct {
	// seamOptions are the composition seams (seam_options.go).
	seamOptions

	// replicaLocator and replicaTimeout configure replica forwarding on
	// every network listener (WithReplicaForwarding); nil forwards nothing.
	replicaLocator *replicaforward.Locator
	replicaTimeout time.Duration

	// standaloneMode marks the raft-free composition; it refuses the etcd
	// compatibility listener (see WithStandaloneMode).
	standaloneMode bool

	// tenantMigrationAdmission gates tenant-scope migrations through the
	// transports; see WithTenantMigrationAdmission.
	tenantMigrationAdmission TenantMigrationAdmission

	tlsConfig  *tls.Config
	caCertPool *x509.CertPool
	gatherer   prometheus.Gatherer
	resolver   rest.LeaderAdminResolver
	host       rest.Host
	kvSvc      service.KVServicer
	adminSvc   service.AdminServicer
	// modules are capability modules a higher layer (e.g. go-xkms) registers via
	// WithModule. Each is exposed across every transport by the start* methods.
	modules []apimodule.ServiceModule
	// mcpServerOpts are embedder-supplied MCP transport options appended
	// after the built-in ones at MCP server construction (WithMCPServerOptions)
	// — e.g. the OAuth discovery surface (mcpapi.WithBearerChallenge,
	// mcpapi.WithProtectedResourceMetadata) go-quicbaas configures.
	mcpServerOpts      []mcpapi.ServerOption
	userSvc            user.UserServicer
	roleSvc            role.RoleServicer
	certProv           cert.CertProvider
	certEnroll         service.CertEnrollmentServicer
	invitations        service.InvitationServicer
	onboarding         service.TenantOnboardingServicer
	authMW             *authmw.AuthMiddleware
	caBootstrapCert    []byte
	caBootstrapBundle  func(context.Context) ([]byte, error)
	batchSvc           service.BatchServicer
	txSvc              service.TransactionServicer
	mvccSvc            service.MVCCKVServicer
	leaseSvc           service.LeaseServicer
	lockSvc            service.LockServicer
	tsSvc              service.TimeSeriesServicer
	indexSvc           service.IndexServicer
	vectorSvc          service.VectorServicer
	embeddingSvc       service.EmbeddingServicer
	ragSvc             service.RAGServicer
	watchSvc           service.WatchServicer
	databaseSvc        service.DatabaseServicer
	dbManager          *database.Manager
	barrierSvc         service.BarrierServicer
	encMigrator        service.EncryptionMigrator
	clusterEncMigrator service.EncryptionMigrator
	ceremonySvc        service.CeremonyServicer
	credentialSvc      service.CredentialServicer
	ceremonyCA         ceremony.CeremonyCA
	serverTLSKey       crypto.Signer
	collectionSvc      service.CollectionServicer
	barrierRegistry    *seal.BarrierRegistry
	dekSvc             service.DEKServicer
	tenantSvc          tenant.TenantServicer
	tenantStoreRes     tenant.StoreResolver
	backupSvc          service.BackupServicer
	rebalanceSvc       service.RebalanceServicer
	dbRegistry         *DatabaseServiceRegistry

	// collectionSweepInterval and collectionStaleAfter tune the collection
	// sweeper; zero keeps the defaults. See WithCollectionSweep.
	collectionSweepInterval time.Duration
	collectionStaleAfter    time.Duration
	// compactionInterval tunes how often the compaction scheduler runs the
	// databases' policies; zero keeps DefaultCompactionInterval. See
	// WithCompactionInterval.
	compactionInterval time.Duration
	// retentionInterval tunes how often the retention scheduler sweeps
	// time-series metrics for expired points; zero keeps
	// DefaultRetentionInterval. See WithRetentionInterval.
	retentionInterval time.Duration
	// collectionSweepLeader reports whether this node leads the system
	// shard; see WithCollectionSweepLeader. Nil means a single node.
	collectionSweepLeader func() bool
	keyspaceReclaim       *service.KeyspaceReclaimService
	reclaimSweeper        *service.ReclaimSweeper
	tenantHosts           *TenantHostRegistry
	raftBackend           *RaftBackendFactory
	quotaAccountant       *quota.Accountant
	webUI                 http.Handler
	auditLogger           audit.AuditLogger
	replicatedAudit       *service.OptInAuditLogger
	authFactorSvc         service.AuthFactorServicer
	userAuthFactorSvc     service.UserAuthFactorServicer
	soAuthenticator       *auth.SOAuthenticator

	// strategyRegistry is the canonical name→factory lookup for sealing
	// strategies. qrdb provides a default registry pre-populated with
	// Software + None; higher layers (xkms via quicbaas composition)
	// register hardware-backed strategies into it.
	strategyRegistry seal.StrategyRegistry

	// barrierKeyProvider is the seam through which this node obtains the
	// strategy that seals its own key for a tenant barrier. Nil means the
	// default provider over strategyRegistry.
	barrierKeyProvider seal.BarrierKeyProvider

	// keyWrapRegistry is the canonical name->factory lookup for key-wrap
	// (KEK) providers the Backup service resolves BackupOptions.
	// KeyProviderID through. qrdb provides a default registry holding only
	// the built-in "software" provider; higher layers (xkms via quicbaas
	// composition) register hardware or cloud-KMS providers into it.
	keyWrapRegistry keywrap.Registry

	// keyWrapProvider is the narrower sibling of keyWrapRegistry for a
	// caller with exactly one additional provider and no registry of its
	// own.
	keyWrapProvider keywrap.Provider

	// keyDir is the directory the built-in "software" key-wrap provider's
	// key files must live inside, confined via *os.Root. Only consulted
	// when keyWrapRegistry is nil.
	keyDir string

	// dekKeyDir is keyDir's counterpart for the tenant DEK keyring's own
	// "software" provider instance -- a distinct directory from keyDir
	// because the keyring's key references default to tenant IDs, a
	// different naming domain than backup's operator-chosen names.
	// keyWrapRegistry/keyWrapProvider, when set, are shared with the DEK
	// keyring too; only the key directory is DEK-specific.
	dekKeyDir string

	// pinBackend is the canonical PIN management implementation. nil
	// means every PIN-gated operation fails closed with
	// pin.ErrBackendNotConfigured; composers wire a software backend over
	// the node-local seal store (lifecycle.NewSealStorePINBackend), and
	// hardware-aware callers inject a TPM2 or PKCS#11 backend here.
	pinBackend pin.PINBackend

	// factorRegistry is the canonical authentication-factor lookup
	// (FIDO2 / PIV / PKCS#11 / mTLS). qrdb provides a default registry
	// with only the software mTLS factor; higher layers register
	// hardware-backed factors from go-xkms.
	factorRegistry *authfactors.FactorRegistry

	// certKeyProviderFactory replaces Key B's built-in per-tenant
	// cakey.SoftwareProvider entirely. nil means qrdb's built-in software
	// provider signs every tenant's client certificates, barrier-encrypted
	// and persisted under __sys:cert_ca/; higher layers inject an
	// HSM/TPM/KMS-backed cakey.ProviderFactory here (see
	// WithCertKeyProviderFactory), resolved fresh per tenant.
	certKeyProviderFactory cakey.ProviderFactory

	// trustAnchors replaces the built-in per-tenant trust anchor store (see
	// WithTrustAnchors). nil means qrdb's built-in store, replicated inside
	// each tenant's own identity boundary.
	trustAnchors trustanchor.Registry

	// sealedWindowProbe controls the sealed-window middleware /
	// interceptors on REST and gRPC. While the probe reports sealed,
	// every endpoint not on the allowlist returns 503 / Unavailable.
	// Wired identically across protocol servers so operators see the
	// same envelope on every transport.
	sealedWindowProbe SealedWindowProbe

	// sealedWindowListener marks the boot-time sealed-window listener built
	// by ServeSealedWindow. Its gates refuse every request once the barrier
	// is unsealed, and it serves no SQL wire protocol.
	sealedWindowListener bool

	// clusterSeal enforces cluster seals on this node; see WithClusterSeal.
	clusterSeal ClusterSeal

	// clusterKeyReceiver handles the /v1/cluster/keydelivery and
	// /v1/cluster/nodekey endpoints. When nil the routes are not registered.
	clusterKeyReceiver rest.ClusterKeyReceiver

	// clusterJoinProvider, clusterJoinStore and clusterJoinTrustDomain
	// configure the member-side /v1/enroll/join route (SPKI/Direct CSR
	// join), the plain-HTTPS twin of the Noise_NK listener
	// clusterenroll.StartJoinServer runs. All three are set together or
	// not at all; see WithClusterJoin.
	clusterJoinProvider    cakey.Provider
	clusterJoinStore       kvstore.AtomicKVStore
	clusterJoinTrustDomain string

	// restListener, restPacketConn, grpcListener, quicPacketConn and
	// mcpListener are sockets the embedder pre-bound (WithRESTListener,
	// WithRESTPacketConn, WithGRPCListener, WithQUICPacketConn,
	// WithMCPListener, go-qrdb#374). Each replaces the bind of the
	// corresponding configured port.
	restListener   net.Listener
	restPacketConn net.PacketConn
	grpcListener   net.Listener
	quicPacketConn net.PacketConn
	mcpListener    net.Listener

	// grpcMaxConcurrentStreams carries an embedder's explicit opt-in into the
	// TCP gRPC and Unix socket servers' per-connection stream cap; see
	// WithGRPCMaxConcurrentStreams. Zero leaves each server's own default.
	grpcMaxConcurrentStreams uint32

	// mcpMaxConnections overrides the MCP server's DefaultMaxConnections; see
	// WithMCPMaxConnections. Zero leaves the MCP server's own default.
	mcpMaxConnections int

	// awaitingAdminCredentialProbe controls the awaiting-admin-credential
	// gate on every network transport (go-qrdb#548 review item 3). While
	// the probe reports awaiting, only ceremony status and
	// ceremony-initialize are served -- admitted with no authentication at
	// all, since there is no bound identity to authenticate as -- and
	// every other request is refused with
	// *middleware.AwaitingAdminCredentialError. Wired identically across
	// protocol servers so operators see the same envelope on every
	// transport. See WithAwaitingAdminCredentialProbe.
	awaitingAdminCredentialProbe AwaitingAdminCredentialProbe
}

// SealedWindowProbe is the abstraction the sealed-window middleware /
// interceptors need to know whether the storage barrier is currently
// sealed. *seal.StorageBarrier satisfies this directly via its
// IsSealed method; tests may substitute a deterministic fake.
type SealedWindowProbe interface {
	IsSealed() bool
}

// AwaitingAdminCredentialProbe is the abstraction the awaiting-admin-
// credential gate needs to know whether the node is currently "awaiting
// admin credential" (go-qrdb#548 review item 3): auth.mtls_binding_policy
// is "required" and the bootstrap admin holds no live certificate binding.
type AwaitingAdminCredentialProbe interface {
	AwaitingAdminCredential() bool
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*serverOptions)

// WithTLS configures TLS for REST, gRPC, and MCP servers.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(o *serverOptions) {
		o.tlsConfig = cfg
	}
}

// WithGatherer attaches a Prometheus Gatherer for the REST /metrics endpoint.
func WithGatherer(g prometheus.Gatherer) ServerOption {
	return func(o *serverOptions) {
		o.gatherer = g
	}
}

// WithModule registers a capability module into the unified server. The module's
// gRPC services, HTTP routes and MCP tools are attached to every transport during
// startup, and its Start/Stop lifecycle runs with the server's. Modules are the
// generic generalization of the strategy/factor/PIN registries: a higher layer
// injects one instead of building its own transport stack. Registering two modules
// with the same Name is a programming error and panics at wiring time.
func WithModule(m apimodule.ServiceModule) ServerOption {
	return func(o *serverOptions) {
		for _, existing := range o.modules {
			if existing.Name() == m.Name() {
				panic(fmt.Sprintf("server: duplicate ServiceModule name %q", m.Name()))
			}
		}
		o.modules = append(o.modules, m)
	}
}

// WithMCPServerOptions appends embedder-supplied MCP transport options at
// MCP server construction, after the built-in ones. This is how a higher
// layer configures the MCP authorization discovery surface — the 401
// WWW-Authenticate challenge (mcpapi.WithBearerChallenge) and the RFC 9728
// Protected Resource Metadata document
// (mcpapi.WithProtectedResourceMetadata) — while the bearer-token validator
// itself is registered as an auth.Authenticator through the auth middleware
// (wiring.AuthMiddlewareConfig.ExtraAuthenticators / the SDK node's
// WithAuthenticators). qrdb implements no OAuth flow of its own.
func WithMCPServerOptions(opts ...mcpapi.ServerOption) ServerOption {
	return func(o *serverOptions) {
		o.mcpServerOpts = append(o.mcpServerOpts, opts...)
	}
}

// WithLeaderForwarding enables transparent leader forwarding on the REST
// server for write operations.
func WithLeaderForwarding(resolver rest.LeaderAdminResolver) ServerOption {
	return func(o *serverOptions) {
		o.resolver = resolver
	}
}

// WithRESTListener makes the REST server serve HTTPS on ln, a TCP listener
// the embedder has already bound, instead of binding server.admin_port. A
// caller that needs a kernel-assigned port binds ":0", reads the port back
// from ln.Addr(), and hands the listener over, so the port is never released
// between reservation and serving (go-qrdb#374).
//
// The listener outlives any one Server: Stop ends this Server's use of it
// without closing it, so successive Servers built from the same options — an
// embedded node rebuilding its data plane, or a sealed-window server followed
// by the node's own — serve on the same socket. Whoever builds the Servers
// closes it when done (see PreboundSockets; the SDK node does so in Close).
// Start closes it when it refuses to serve, as do the SDK node's
// constructors when they return an error, and Start refuses it with a
// *PreboundSocketError when REST is disabled. ln must support SetDeadline,
// as a *net.TCPListener does.
//
// The HTTP/3 socket is still bound by the server, on ln's host and port,
// unless WithRESTPacketConn supplies it too.
func WithRESTListener(ln net.Listener) ServerOption {
	return func(o *serverOptions) {
		o.restListener = ln
	}
}

// WithRESTPacketConn makes the REST server serve HTTP/3 on pc instead of
// binding a UDP socket beside its TCP listener. It requires WithRESTListener,
// and pc must be bound on that listener's host and port — the port Alt-Svc
// advertises — or Start fails. Ownership is WithRESTListener's.
func WithRESTPacketConn(pc net.PacketConn) ServerOption {
	return func(o *serverOptions) {
		o.restPacketConn = pc
	}
}

// WithGRPCListener makes the gRPC server serve on ln, a TCP listener the
// embedder has already bound, instead of binding server.grpc_port. See
// WithRESTListener for why and for ownership.
func WithGRPCListener(ln net.Listener) ServerOption {
	return func(o *serverOptions) {
		o.grpcListener = ln
	}
}

// WithQUICPacketConn makes the native QUIC server serve on pc, a UDP socket
// the embedder has already bound, instead of binding server.quic_port. See
// WithRESTListener for why and for ownership.
func WithQUICPacketConn(pc net.PacketConn) ServerOption {
	return func(o *serverOptions) {
		o.quicPacketConn = pc
	}
}

// WithMCPListener makes the MCP server serve on ln, a TCP listener the
// embedder has already bound, instead of binding mcp.port. See
// WithRESTListener for why and for ownership.
func WithMCPListener(ln net.Listener) ServerOption {
	return func(o *serverOptions) {
		o.mcpListener = ln
	}
}

// WithGRPCMaxConcurrentStreams overrides the TCP gRPC and Unix socket
// servers' DefaultMaxConcurrentStreams, the number of simultaneously active
// RPCs grpc-go admits on one HTTP/2 connection.
func WithGRPCMaxConcurrentStreams(n uint32) ServerOption {
	return func(o *serverOptions) {
		o.grpcMaxConcurrentStreams = n
	}
}

// WithMCPMaxConnections overrides the MCP server's DefaultMaxConnections, the
// number of simultaneously open connections its /mcp listener admits
// (go-qrdb#576).
func WithMCPMaxConnections(n int) ServerOption {
	return func(o *serverOptions) {
		o.mcpMaxConnections = n
	}
}

// WithCACertPool sets the CA certificate pool for TLS peer verification
// in the REST server's forwarding client.
func WithCACertPool(pool *x509.CertPool) ServerOption {
	return func(o *serverOptions) {
		o.caCertPool = pool
	}
}

// WithHost sets the Host interface for REST leader forwarding headers.
func WithHost(h rest.Host) ServerOption {
	return func(o *serverOptions) {
		o.host = h
	}
}

// WithUserService configures the user management service for all protocol
// servers that support it.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(o *serverOptions) { o.userSvc = svc }
}

// WithRoleService configures the role management service for all protocol
// servers that support it.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(o *serverOptions) { o.roleSvc = svc }
}

// WithCertProvider configures the certificate provider for all protocol
// servers that support it.
func WithCertProvider(svc cert.CertProvider) ServerOption {
	return func(o *serverOptions) { o.certProv = svc }
}

// WithCertEnrollment configures the certificate issue/enrol and trust-anchor
// service for every protocol server (lifecycle.Node.CertEnrollment).
func WithCertEnrollment(svc service.CertEnrollmentServicer) ServerOption {
	return func(o *serverOptions) { o.certEnroll = svc }
}

// WithInvitations configures the invitation self-enrolment service
// (lifecycle.Node.Invitations) for the protocol servers that support it.
func WithInvitations(svc service.InvitationServicer) ServerOption {
	return func(o *serverOptions) { o.invitations = svc }
}

// WithTenantOnboarding configures the tenant-create service for every
// protocol server (lifecycle.Node.TenantOnboarding).
func WithTenantOnboarding(svc service.TenantOnboardingServicer) ServerOption {
	return func(o *serverOptions) { o.onboarding = svc }
}

// WithAuthMiddleware configures authentication and authorization middleware
// for REST and gRPC protocol servers.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(o *serverOptions) { o.authMW = mw }
}

// WithSealedWindowProbe installs the sealed-window middleware on REST
// and the equivalent interceptors on gRPC. While the probe reports
// sealed, every endpoint not on the allowlist returns 503 / Unavailable
// with body {"error":"barrier sealed","code":"SEALED"}. A probe that also
// implements sealedwindow.StateProbe -- the cluster seal's, once applied --
// reports a node cut off from the seal records as unconfirmed, refused
// instead as SEAL_STATE_UNCONFIRMED: retryable, and never reported as
// sealed. Once unsealed the wrappers are passthrough; on re-seal they engage again. The same
// option drives all protocol surfaces so operators see consistent
// behavior across transports.
func WithSealedWindowProbe(probe SealedWindowProbe) ServerOption {
	return func(o *serverOptions) {
		o.sealedWindowProbe = probe
		o.sealedWindowListener = false
	}
}

// WithAwaitingAdminCredentialProbe installs the awaiting-admin-credential
// gate on REST, gRPC, native QUIC, MCP and the Unix socket transport
// (go-qrdb#548 review item 3). While probe.AwaitingAdminCredential()
// returns true, only ceremony status and ceremony-initialize are served,
// with no authentication at all; every other request is refused with
// *middleware.AwaitingAdminCredentialError. Once the probe reports false
// every transport serves normally, with no restart required.
func WithAwaitingAdminCredentialProbe(probe AwaitingAdminCredentialProbe) ServerOption {
	return func(o *serverOptions) {
		o.awaitingAdminCredentialProbe = probe
	}
}

// withSealedWindowListener installs the boot-time sealed-window listener's
// gates on every transport, backed by probe: the allowlist while the barrier
// is sealed, and a refusal of every request once it is unsealed, sampled per
// request. The listener runs without authentication -- while sealed there is
// no identity plane to authenticate against -- so nothing may be served
// through it against an unsealed barrier, even in the moment between the
// unseal and the listener's Stop. Only ServeSealedWindow applies it.
func withSealedWindowListener(probe SealedWindowProbe) ServerOption {
	return func(o *serverOptions) {
		o.sealedWindowProbe = probe
		o.sealedWindowListener = true
	}
}

// sealedWindowListenerProbe is a sealed-window listener's probe: the
// barrier's sealed state, one Admission shared by every transport that serves
// the window, and whether officer enrollment is still open to an
// unauthenticated caller, read from the ceremony core the window serves
// (ceremony.CeremonyService.EnrollmentOpen). With no ceremony core there is
// no enrollment to keep open, so it reports closed.
type sealedWindowListenerProbe struct {
	SealedWindowProbe
	*sealedwindow.Admission
	ceremony *ceremony.CeremonyService
}

// EnrollmentOpen implements sealedwindow.ListenerProbe under the request's
// context; ceremony.CeremonyService.EnrollmentOpen bounds the read further
// and fails closed.
func (p sealedWindowListenerProbe) EnrollmentOpen(ctx context.Context) bool {
	return p.ceremony != nil && p.ceremony.EnrollmentOpen(ctx)
}

var _ sealedwindow.ListenerProbe = sealedWindowListenerProbe{}

// NewSealedWindowListenerProbe returns the probe that gates a sealed-window
// surface: sealed reports the barrier's state, and ceremonySvc (which may be
// nil) decides whether anonymous officer enrollment is still open.
//
// Build ONE per sealed window and pass it to every transport that serves the
// window -- the REST handler (rest.NewSealedWindowHandler), the MCP tool set
// (mcp.SealedWindowTools) and the gRPC listener interceptors -- so they share
// one Admission: a request that unseals the barrier then waits for requests
// already admitted on any transport, and none admitted after it runs
// anonymously against the unsealed barrier. qrdb's own boot-time listener
// builds its probe here too. A nil sealed probe yields a probe that refuses
// everything.
func NewSealedWindowListenerProbe(sealed SealedWindowProbe, ceremonySvc service.CeremonyServicer) sealedwindow.ListenerProbe {
	if sealed == nil {
		return sealedwindow.OrFailClosed(nil)
	}
	p := sealedWindowListenerProbe{SealedWindowProbe: sealed, Admission: &sealedwindow.Admission{}}
	if ceremonySvc != nil {
		if core, ok := service.CeremonyServiceCore(ceremonySvc); ok {
			p.ceremony = core
		}
	}
	return p
}

// listenerProbe returns the boot-time sealed-window listener's probe.
func (s *Server) listenerProbe() sealedwindow.ListenerProbe {
	return s.sealedListener
}

// WithCABootstrap enables the /v1/ca/bootstrap endpoint on the REST
// server for cluster TLS bootstrapping. It takes the CA certificate only;
// the endpoint is public and never serves key material.
func WithCABootstrap(certPEM []byte) ServerOption {
	return func(o *serverOptions) {
		o.caBootstrapCert = certPEM
	}
}

// WithBootstrapCABundle makes the host the source of the bundle served on
// /v1/ca/bootstrap, read per request through bundle: first-contact trust for a
// host that owns it (go-xkms's xkmsd) rather than qrdb's cluster CA. qrdb owns
// the route and the host supplies only the data, as for /health, so a host
// never registers the path itself. Only CERTIFICATE blocks are served. When
// neither this nor WithCABootstrap is configured the path is not registered at
// all; configuring both is refused by Start with *rest.CABootstrapConflictError.
func WithBootstrapCABundle(bundle func(ctx context.Context) ([]byte, error)) ServerOption {
	return func(o *serverOptions) {
		o.caBootstrapBundle = bundle
	}
}

// WithClusterKeyReceiver wires the cluster peer key-transport receiver into
// the REST server. When set, /v1/cluster/keydelivery and /v1/cluster/nodekey
// are registered. Both paths are pre-exempt from the auth middleware and the
// sealed-window middleware so they operate in every daemon state. When nil the
// routes are omitted (single-node or non-clustered deployments).
func WithClusterKeyReceiver(rcv rest.ClusterKeyReceiver) ServerOption {
	return func(o *serverOptions) { o.clusterKeyReceiver = rcv }
}

// WithClusterJoin enables the /v1/enroll/join route on the REST server: the
// SPKI/Direct plain-HTTPS twin of clusterenroll's Noise_NK join listener,
// both bound to the same cakey.Provider, the same replicated join-token
// store, and the same pinned trust domain, so a joining node may use
// either transport interchangeably. provider must be the cluster CA (Key
// A) provider, available only once the barrier is unsealed
// (confirmClusterCAPersisted), which is why this route is never a member of
// the sealed-window allowlist. When provider is nil the route is not
// registered at all.
func WithClusterJoin(provider cakey.Provider, store kvstore.AtomicKVStore, trustDomain string) ServerOption {
	return func(o *serverOptions) {
		o.clusterJoinProvider = provider
		o.clusterJoinStore = store
		o.clusterJoinTrustDomain = trustDomain
	}
}

// WithBatch configures the batch service for all protocol servers.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(o *serverOptions) { o.batchSvc = svc }
}

// WithTransaction configures the transaction service for all protocol servers.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(o *serverOptions) { o.txSvc = svc }
}

// WithMVCC configures the MVCC key-value service for all protocol servers.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(o *serverOptions) { o.mvccSvc = svc }
}

// WithLease configures the lease service for all protocol servers.
func WithLease(svc service.LeaseServicer) ServerOption {
	return func(o *serverOptions) { o.leaseSvc = svc }
}

// WithLockService configures the distributed lock service for all protocol
// servers that support it.
//
// This is the REGISTRY-LESS fallback, not the primary path. When a
// service.Registry is configured the lock servicer is resolved per
// (tenant, database) and this singleton is never consulted: a lock key
// carries no tenant segment, so a process-wide servicer built over the raw
// engines gives every tenant the same lock (go-qrdb#455).
func WithLockService(svc service.LockServicer) ServerOption {
	return func(o *serverOptions) { o.lockSvc = svc }
}

// WithTimeSeries configures the time series service for all protocol servers.
func WithTimeSeries(svc service.TimeSeriesServicer) ServerOption {
	return func(o *serverOptions) { o.tsSvc = svc }
}

// WithIndex configures the index service for all protocol servers.
func WithIndex(svc service.IndexServicer) ServerOption {
	return func(o *serverOptions) { o.indexSvc = svc }
}

// WithVector configures the vector service for all protocol servers.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(o *serverOptions) { o.vectorSvc = svc }
}

// WithEmbedding configures the embedding service for all protocol servers
// that support text embedding operations.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(o *serverOptions) { o.embeddingSvc = svc }
}

// WithRAG configures the RAG service for all protocol servers that support
// Retrieval-Augmented Generation operations.
func WithRAG(svc service.RAGServicer) ServerOption {
	return func(o *serverOptions) { o.ragSvc = svc }
}

// WithWatch configures the watch service for all protocol servers.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(o *serverOptions) { o.watchSvc = svc }
}

// WithDatabaseService configures the database management service for all
// protocol servers.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(o *serverOptions) { o.databaseSvc = svc }
}

// WithDatabaseManager configures the database manager for SQL wire
// protocol servers (MySQL and PostgreSQL). When set, SQL servers use
// multi-database support via provider.NewManagerProvider.
func WithDatabaseManager(manager *database.Manager) ServerOption {
	return func(o *serverOptions) { o.dbManager = manager }
}

// WithBarrierService configures the barrier service for all protocol servers.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(o *serverOptions) { o.barrierSvc = svc }
}

// WithEncryptionMigrator configures the enable-encryption migrator, exposing
// the migration endpoint on the protocol servers that support it (REST today).
func WithEncryptionMigrator(m service.EncryptionMigrator) ServerOption {
	return func(o *serverOptions) { o.encMigrator = m }
}

// WithTenantMigrationAdmission sets the admission the transports' scoped
// router consults before a tenant-scope migration; see
// TenantMigrationAdmission.
func WithTenantMigrationAdmission(a TenantMigrationAdmission) ServerOption {
	return func(o *serverOptions) { o.tenantMigrationAdmission = a }
}

// WithClusterEncryptionMigrator configures the cluster-coordination migrator the
// transports use INSTEAD of the local scoped router: it writes the replicated
// migration intent and seals per-node credentials, and the per-node reconcilers
// drive the actual recycles. When set it takes precedence over the wrapped
// scoped router in effectiveEncryptionMigrator.
func WithClusterEncryptionMigrator(m service.EncryptionMigrator) ServerOption {
	return func(o *serverOptions) { o.clusterEncMigrator = m }
}

// WithCeremonyService configures the ceremony service for all protocol servers.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(o *serverOptions) { o.ceremonySvc = svc }
}

// WithCredentialService configures the credential service for all protocol servers.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(o *serverOptions) { o.credentialSvc = svc }
}

// WithCeremonyCA configures the ceremony CA used for ceremony initialization.
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption {
	return func(o *serverOptions) { o.ceremonyCA = ca }
}

// WithServerTLSKey sets the private key of the server TLS leaf the transports
// auto-provision from the CeremonyCA (see WithCeremonyCA). Only the key's
// public half reaches the CA and every handshake signs through key, so a
// composer passes a device-held signer (TPM, PKCS#11, cloud KMS) and the leaf
// key never leaves the device. Unset, each transport's leaf gets a fresh
// in-memory ECDSA P-256 key -- the software default. It has no effect when
// WithTLS supplies the listener's configuration outright.
func WithServerTLSKey(key crypto.Signer) ServerOption {
	return func(o *serverOptions) { o.serverTLSKey = key }
}

// WithCollectionService configures the collection service for all protocol servers.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(o *serverOptions) { o.collectionSvc = svc }
}

// WithBarrierRegistry sets the barrier registry for per-tenant encryption
// isolation. When set, the registry is available for runtime tenant barrier
// lifecycle operations (register, unseal, seal, remove).
func WithBarrierRegistry(reg *seal.BarrierRegistry) ServerOption {
	return func(o *serverOptions) {
		o.barrierRegistry = reg
	}
}

// WithDEKService configures the tenant DEK keyring service for all protocol
// servers.
func WithDEKService(svc service.DEKServicer) ServerOption {
	return func(o *serverOptions) { o.dekSvc = svc }
}

// WithTenantService sets the tenant lifecycle service for multi-tenant
// provisioning. When set, tenants can be created, queried, and deleted
// via the API layer with barrier registry integration.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(o *serverOptions) {
		o.tenantSvc = svc
	}
}

// WithTenantStoreResolver sets the resolver that maps a tenant to the store
// holding that tenant's IAM data. It is what makes an auto-constructed
// user-side auth-factor service tenant-scoped; without it Start() constructs
// no such service at all (see ensureUserAuthFactorService).
func WithTenantStoreResolver(resolver tenant.StoreResolver) ServerOption {
	return func(o *serverOptions) {
		o.tenantStoreRes = resolver
	}
}

// WithBackupService sets the backup/restore service. When set, backups can be
// created, listed, restored, and verified across all wire transports.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(o *serverOptions) {
		o.backupSvc = svc
	}
}

// WithRebalanceService sets the cluster rebalance service. When set, shard
// statistics, rebalance planning, and execution are exposed across all wire
// transports.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(o *serverOptions) {
		o.rebalanceSvc = svc
	}
}

// WithKeyspaceReclaimService supplies the service that reclaims a dropped
// database's data from the store it shares with other databases
// (lifecycle.Node.KeyspaceReclaim). It is already wired into the database
// manager by the time it reaches here; the server's interest is narrower --
// it is the only layer that knows which tenants are volume isolated, so it
// attaches the StoreResolver that routes those tenants' keys to their
// dedicated per-tenant Host. Without it a volume-isolated tenant's drop
// scans the system store, finds nothing and reports success (go-qrdb#412).
func WithKeyspaceReclaimService(svc *service.KeyspaceReclaimService) ServerOption {
	return func(o *serverOptions) {
		o.keyspaceReclaim = svc
	}
}

// WithReclaimSweeper supplies the sweeper that drains pending reclaim jobs
// (lifecycle.Node.ReclaimSweeper, go-qrdb#547). The composer that built it
// also starts it (see sdk/go/node's startReclaimSweep and cmd/qrdbd's
// startReclaimSweeper); the server's part is the resolver that routes each
// job to the node that sweeps it and to its store, which only the server can
// build -- it resolves through the same registry the transports use (see
// wireReclaimStoreResolver). Without a DatabaseServiceRegistry the server
// serves no relational DDL, so no job can be published and none is
// installed.
func WithReclaimSweeper(sweeper *service.ReclaimSweeper) ServerOption {
	return func(o *serverOptions) {
		o.reclaimSweeper = sweeper
	}
}

// WithDatabaseServiceRegistry sets the per-database service registry.
// When set, protocol handlers can resolve the correct service set for a
// given database name instead of falling back to the single shared set.
func WithDatabaseServiceRegistry(reg *DatabaseServiceRegistry) ServerOption {
	return func(o *serverOptions) {
		o.dbRegistry = reg
	}
}

// WithTenantHostRegistry sets the per-tenant volume Host registry. When set,
// the transport-neutral service registry routes IsolationVolume tenants to
// their dedicated per-tenant Raft Host instead of the shared system services.
func WithTenantHostRegistry(reg *TenantHostRegistry) ServerOption {
	return func(o *serverOptions) {
		o.tenantHosts = reg
	}
}

// WithRaftBackendFactory sets the dedicated per-database raft shard factory.
// When set, the transport-neutral service registry routes IsolationRaft tenants
// to their database's dedicated Raft group (shard) on the shared system Host
// instead of the shared system services.
func WithRaftBackendFactory(f *RaftBackendFactory) ServerOption {
	return func(o *serverOptions) {
		o.raftBackend = f
	}
}

// WithQuotaAccountant sets the storage-byte quota accountant. When set, the
// transport-neutral service registry wraps the resolved KV and MVCC servicers
// for every non-system tenant with a positive MaxStorageBytes limit so writes
// are enforced (rejected with quota.ExceededError before commit) and accounted.
// The background reconciler flushes and corrects the accumulated usage.
func WithQuotaAccountant(acct *quota.Accountant) ServerOption {
	return func(o *serverOptions) {
		o.quotaAccountant = acct
	}
}

// WithKVService configures the KV service for all protocol servers. This is an
// alternative to passing kvSvc as a positional parameter in New and is useful
// when constructing a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(o *serverOptions) { o.kvSvc = svc }
}

// WithAdminService configures the admin service for all protocol servers. This
// is an alternative to passing adminSvc as a positional parameter in New and is
// useful when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(o *serverOptions) { o.adminSvc = svc }
}

// WithWebUI attaches an http.Handler that serves an embedded web UI from the
// REST server. Non-API requests are forwarded to this handler.
func WithWebUI(h http.Handler) ServerOption {
	return func(o *serverOptions) { o.webUI = h }
}

// WithAuditLogger configures a compliance-grade AuditLogger for all protocol
// servers. When set, the audit query endpoint is registered on REST, gRPC
// (Unix socket), QUIC, and MCP servers.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(o *serverOptions) { o.auditLogger = l }
}

// WithReplicatedAuditLogger configures the cluster-wide, per-tenant-opt-in
// replicated audit sink (docs/audit/architecture.md#cluster-wide-replicated-trail)
// for all protocol servers -- lifecycle.Node.ReplicatedAudit in production.
// When set, GET .../audit/events?replicated=true and the replicated verify
// capability are registered on REST, gRPC (Unix socket), QUIC, and MCP
// servers, alongside WithAuditLogger's node-local trail.
func WithReplicatedAuditLogger(l *service.OptInAuditLogger) ServerOption {
	return func(o *serverOptions) { o.replicatedAudit = l }
}

// WithAuthFactorService configures the auth factor service for custodian
// enrollment, challenge issuance, and verification across all protocol servers.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(o *serverOptions) { o.authFactorSvc = svc }
}

// WithUserAuthFactorService configures the user-side N-factor service (M14,
// docs/08-bootstrap-auth-architecture.md §5e) across all protocol servers
// that support it (REST, gRPC, QUIC, MCP, Unix). This is the service the SDK
// client's UserAuthFactors() accessor talks to (Add / List / Get / Remove /
// Rotate / Relabel / Login) and is distinct from WithAuthFactorService (the
// ceremony-time enrollment surface). When not supplied explicitly, and a User
// service AND a tenant store resolver are configured, Start() constructs the
// tenant-routed service automatically via ensureUserAuthFactorService — see
// that method, including why there is no unrouted fallback.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) ServerOption {
	return func(o *serverOptions) { o.userAuthFactorSvc = svc }
}

// WithSOAuthenticator configures the Security Officer dual-factor PIN
// authenticator. When set, SO-permissioned endpoints require an in-band
// X-QRDB-SO-PIN header over TLS for verification against the hash-committed
// PIN (never stored plaintext).
func WithSOAuthenticator(a *auth.SOAuthenticator) ServerOption {
	return func(o *serverOptions) { o.soAuthenticator = a }
}

// WithStrategyRegistry injects a populated StrategyRegistry so per-tenant
// barriers can resolve composer-supplied seal strategies by name: the default
// BarrierKeyProvider (seal.NewBarrierKeyProvider) resolves over it.
// qrdb provides a default registry pre-populated with Software + None; higher
// layers (xkms via quicbaas composition) call Register on the supplied
// registry to add hardware-backed strategies. WithBarrierKeyProvider, when
// also given, wins.
//
// The TenantService that consumes this registry is constructed during
// lifecycle.BuildServices — before server.New runs — so the SDK node builders
// (NewStandaloneNode/NewClusteredNode) resolve this option via
// ResolveCryptoOptions and forward it into lifecycle.Infra.StrategyRegistry up
// front. Passing it only to server.New would be too late; use the node
// builders (or populate lifecycle.Infra directly) for hardware-strategy
// injection to take effect.
func WithStrategyRegistry(reg seal.StrategyRegistry) ServerOption {
	return func(o *serverOptions) { o.strategyRegistry = reg }
}

// WithBarrierKeyProvider injects the seam through which each node obtains
// the strategy that seals its own key for a tenant barrier. The provider is
// handed the barrier, this node's identity, the owner's chosen strategy and
// the owner's opaque KeyRef, and declares what unsealing needs and who holds
// the wrapping key. go-qrdb ships only the software provider over a
// StrategyRegistry; a composer (go-xkms) injects hardware- or KMS-backed ones.
//
// Like WithStrategyRegistry, the TenantService that consumes it is built
// during lifecycle.BuildServices, before server.New runs: the SDK node
// builders resolve it via ResolveCryptoOptions and forward it into
// lifecycle.Infra.BarrierKeyProvider up front.
func WithBarrierKeyProvider(p seal.BarrierKeyProvider) ServerOption {
	return func(o *serverOptions) { o.barrierKeyProvider = p }
}

// WithKeyWrapRegistry injects a populated keywrap.Registry so the Backup
// service can resolve BackupOptions.KeyProviderID against hardware or
// cloud-KMS key-wrap providers (TPM2, PKCS#11, cloud KMS, Vault) by name.
// qrdb provides a default registry holding only the built-in "software"
// provider; higher layers (xkms via quicbaas composition) call Register on
// the supplied registry to add hardware-backed providers -- the same
// composition shape WithStrategyRegistry uses for sealing strategies.
//
// The Backup service is constructed during lifecycle.BuildServices —
// before server.New runs — so the SDK node builders resolve this option via
// ResolveCryptoOptions and forward it into lifecycle.Infra.KeyWrapRegistry
// up front, exactly as WithStrategyRegistry's own doc comment describes.
func WithKeyWrapRegistry(reg keywrap.Registry) ServerOption {
	return func(o *serverOptions) { o.keyWrapRegistry = reg }
}

// WithKeyWrapProvider is the narrower sibling of WithKeyWrapRegistry for a
// caller with exactly one additional key-wrap provider and no registry of
// its own: it is registered under its own ProviderID() into the (default,
// software-only, unless WithKeyWrapRegistry is also applied) registry.
func WithKeyWrapProvider(p keywrap.Provider) ServerOption {
	return func(o *serverOptions) { o.keyWrapProvider = p }
}

// WithKeyDir configures the directory the built-in "software" key-wrap
// provider's key files must live inside -- every key reference a backup
// names is a bare file name inside it, never a path (go-qrdb security
// review, backup-encrypt-default #B4). Only consulted when
// WithKeyWrapRegistry/WithKeyWrapProvider do not already supply a
// registry of the caller's own.
//
// Like WithKeyWrapRegistry, this is forwarded through ResolveCryptoOptions
// into lifecycle.Infra.KeyDir up front; passing it only to server.New
// would be too late.
func WithKeyDir(dir string) ServerOption {
	return func(o *serverOptions) { o.keyDir = dir }
}

// WithDEKKeyDir configures the directory the built-in "software" key-wrap
// provider's key files must live inside for the tenant DEK keyring --
// WithKeyDir's counterpart for the Backup service. The two are distinct
// directories because the keyring's key references default to tenant IDs,
// a different naming domain than backup's operator-chosen names;
// WithKeyWrapRegistry/WithKeyWrapProvider, when applied, are shared with
// the DEK keyring too (one registry, two consumers).
//
// Like WithKeyDir, this is forwarded through ResolveCryptoOptions into
// lifecycle.Infra.DEKKeyDir up front; passing it only to server.New would
// be too late.
func WithDEKKeyDir(dir string) ServerOption {
	return func(o *serverOptions) { o.dekKeyDir = dir }
}

// WithPINBackend injects the canonical PIN backend. Without one every
// PIN-gated operation fails closed with pin.ErrBackendNotConfigured. Hardware
// callers inject TPM2 or PKCS#11 backends from go-xkms here; the
// ceremony service uses the backend to store and verify the SO and
// User PINs (never as raw bytes).
//
// The backend is decorated with the node's SO PIN lockout
// (pin.NewLockoutBackend) once, here, when the option is built -- not each
// time it is applied -- so every resolution of the option, by the server and
// by a composer that hands ResolveCryptoOptions' backend to lifecycle, yields
// the same instance and therefore one counter. A backend that is already
// decorated is kept as it is.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	locked := pin.NewLockoutBackend(backend)
	return func(o *serverOptions) { o.pinBackend = locked }
}

// WithFactorRegistry injects a populated authentication-factor registry.
// The registry starts empty; there are no default factors. Higher layers
// (xkms via quicbaas composition, or xkmsd's embedded node) register
// go-xkms hardware factor plugins (fido2/piv/pkcs11) and inject via this
// option. When set and no explicit WithAuthFactorService is supplied, the
// server (via ensureAuthFactorService in Start) and the SDK node builders
// (via wireClientAndServer) construct the unified AuthFactorService from
// the registry automatically so every transport exposes the
// enrollment/challenge/verify surface. mTLS login is handled natively by
// the auth.LoginResolver path and is not an authfactors.Factor.
func WithFactorRegistry(reg *authfactors.FactorRegistry) ServerOption {
	return func(o *serverOptions) { o.factorRegistry = reg }
}

// WithCertKeyProviderFactory injects the cakey.ProviderFactory that resolves
// Key B (the login CA, pkg/cert) material, one tenant at a time, in place
// of qrdb's built-in per-tenant cakey.SoftwareProvider. When set, no
// software key is ever generated or stored under __sys:cert_ca/ for any
// tenant: every client-certificate signature is routed through
// f.ProviderForTenant(tenantID). This is the Key B half of the CA key
// provider seam (see docs/certificate-authority/architecture.md); the
// cluster CA (Key A) has its own, separate injection point.
//
// f MUST resolve its own key per tenant -- per-tenant keys are REQUIRED, not
// merely supported (owner ruling, go-qrdb#591's review) -- exactly as
// cakey.ProviderFactory's own doc comment requires; a factory that returns
// the same Provider for every tenant defeats the issuer-tenant binding
// pkg/clienttrust.Pool.VerifyIssuer enforces.
//
// The CertProvider that consumes this is constructed during
// lifecycle.BuildServices — strictly BEFORE server.New runs — so, like
// WithStrategyRegistry, the SDK node builders and qrdbd resolve this option
// via ResolveCryptoOptions and forward it into
// lifecycle.Infra.CertKeyProviderFactory up front. Passing it only to
// server.New would be too late: the DefaultCertProvider would already have
// been constructed with the built-in software provider.
func WithCertKeyProviderFactory(f cakey.ProviderFactory) ServerOption {
	return func(o *serverOptions) { o.certKeyProviderFactory = f }
}

// WithTrustAnchors injects the trust store that holds every tenant's
// registered trust anchors -- the CA certificates a tenant's bring-your-own
// login certificates must chain to -- in place of qrdb's built-in store
// (trustanchor.NewKVRegistry, replicated inside each tenant's own identity
// boundary). The store is read through its trustanchor.Source half,
// CABundle(ctx, tenantID), the shape of a CA bundle provider, so an embedder
// (go-xkms) adapts its own trust store with no glue beyond tenant scoping.
//
// Like WithCertKeyProvider, the services that consume it are built during
// lifecycle.BuildServices, before server.New runs: the SDK node builders
// resolve it via ResolveCryptoOptions and forward it into
// lifecycle.Infra.TrustAnchors up front.
func WithTrustAnchors(r trustanchor.Registry) ServerOption {
	return func(o *serverOptions) { o.trustAnchors = r }
}

// CryptoOptions holds the hardware/seal injection values carried by a set of
// ServerOptions. It exists because the TenantService (which consumes the
// StrategyRegistry to resolve per-tenant hardware/cloud seal strategies) is
// constructed during lifecycle.BuildServices — strictly BEFORE server.New
// runs. An embedder (the SDK node builders, quicbaas) must therefore resolve
// these values from its ServerOptions and forward them into lifecycle.Infra
// up front; passing them only to server.New would be too late and the
// injection would silently no-op.
type CryptoOptions struct {
	StrategyRegistry       seal.StrategyRegistry
	BarrierKeyProvider     seal.BarrierKeyProvider
	PINBackend             pin.PINBackend
	FactorRegistry         *authfactors.FactorRegistry
	CertKeyProviderFactory cakey.ProviderFactory
	TrustAnchors           trustanchor.Registry
	AuditLogger            audit.AuditLogger
	// ReplicatedAuditLogger is the cluster-wide, per-tenant-opt-in replicated
	// audit chain passed through WithReplicatedAuditLogger; nil when none was.
	ReplicatedAuditLogger *service.OptInAuditLogger
	// KeyWrapRegistry and KeyWrapProvider carry WithKeyWrapRegistry /
	// WithKeyWrapProvider into lifecycle.Infra.KeyWrapRegistry /
	// KeyWrapProvider, the same forwarding StrategyRegistry needs and for
	// the same reason: the Backup service is built before server.New runs.
	KeyWrapRegistry keywrap.Registry
	KeyWrapProvider keywrap.Provider
	KeyDir          string
	// DEKKeyDir carries WithDEKKeyDir into lifecycle.Infra.DEKKeyDir --
	// KeyDir's counterpart for the tenant DEK keyring's own "software"
	// provider instance.
	DEKKeyDir string
	// AuthFactorService is the caller-supplied AuthFactorServicer, if any.
	// When non-nil the caller passed server.WithAuthFactorService explicitly.
	// When nil and FactorRegistry is non-nil, the server and SDK node builders
	// construct the service automatically via ensureAuthFactorService.
	AuthFactorService service.AuthFactorServicer
	// CeremonyCA is the CA passed through WithCeremonyCA. The node builders
	// hand it to the ceremony service, which issues every share holder's
	// certificate from it.
	CeremonyCA ceremony.CeremonyCA

	// The composition seams (see seam_options.go). Each is nil when no
	// option set it, which keeps the software default.
	BackupSink              service.BackupSink
	Clock                   clock.Clock
	StorageFactory          StorageFactory
	DEKProvider             dek.Provider
	InvitationRedeemLimiter ratelimit.Limiter
	HolderActLimiter        ratelimit.Limiter
}

// ResolveCryptoOptions applies the given ServerOptions to a throwaway option
// set and returns the crypto-injection values (strategy registry, seal
// strategy, PIN backend, factor registry). Embedders call this before
// lifecycle.BuildServices so an injected StrategyRegistry reaches the
// TenantService. The ServerOption setters are pure field assignments, so
// applying the full set here has no side effects.
func ResolveCryptoOptions(opts ...ServerOption) CryptoOptions {
	o := &serverOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	return CryptoOptions{
		StrategyRegistry:       o.strategyRegistry,
		BarrierKeyProvider:     o.barrierKeyProvider,
		PINBackend:             o.pinBackend,
		FactorRegistry:         o.factorRegistry,
		CertKeyProviderFactory: o.certKeyProviderFactory,
		TrustAnchors:           o.trustAnchors,
		AuditLogger:            o.auditLogger,
		ReplicatedAuditLogger:  o.replicatedAudit,
		KeyWrapRegistry:        o.keyWrapRegistry,
		KeyWrapProvider:        o.keyWrapProvider,
		KeyDir:                 o.keyDir,
		DEKKeyDir:              o.dekKeyDir,
		AuthFactorService:      o.authFactorSvc,
		CeremonyCA:             o.ceremonyCA,

		BackupSink:              o.backupSink,
		Clock:                   o.clock,
		StorageFactory:          o.storageFactory,
		DEKProvider:             o.dekProvider,
		InvitationRedeemLimiter: o.invitationRedeemLimiter,
		HolderActLimiter:        o.holderActLimiter,
	}
}

// ResolveTLSConfig applies the given ServerOptions to a throwaway option set
// and returns the explicit *tls.Config passed via WithTLS, or nil when none
// was supplied (the composer relies on CeremonyCA auto-provisioning
// instead). Embedders that need to wire something onto the listener TLS
// config before server.New runs call this the same way ResolveCryptoOptions
// is called for crypto injection -- e.g. sdk/go/node wiring the shared
// pkg/clienttrust dynamic client-auth trust pool onto it.
func ResolveTLSConfig(opts ...ServerOption) *tls.Config {
	o := &serverOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	return o.tlsConfig
}

// New creates a new multi-protocol server orchestrator. kvSvc and adminSvc may
// be nil when only non-KV services (barrier, cert, auth) are needed; protocol
// servers propagate their nil-service behaviour to callers as Unimplemented
// responses. The server does not start until Start() is called.
func New(cfg *config.Config, kvSvc service.KVServicer, adminSvc service.AdminServicer, opts ...ServerOption) *Server {
	s := &Server{
		config:   cfg,
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		logger:   slog.Default(),
	}

	for _, opt := range opts {
		opt(&s.opts)
	}
	s.opts.applyClusterSeal()

	// Option-based kvSvc/adminSvc override the positional parameters, allowing
	// nil to be passed as a positional param when using option functions instead.
	if s.opts.kvSvc != nil {
		s.kvSvc = s.opts.kvSvc
	}
	if s.opts.adminSvc != nil {
		s.adminSvc = s.opts.adminSvc
	}

	if s.opts.sealedWindowListener {
		s.sealedListener = NewSealedWindowListenerProbe(s.opts.sealedWindowProbe, s.opts.ceremonySvc)
	}

	s.wireStoreResolver()
	s.wireTenantDataProbe()
	s.wireReclaimStoreResolver()
	s.wireCompactionPolicies()
	s.wireProvisioningAuthorization()

	return s
}

// wireProvisioningAuthorization gives the database service every network
// transport and both SQL wire listeners create through the provisioning
// check: databases:create_raft and databases:create_volume, in addition to
// the databases:create their entry check makes. Which one a create needs
// depends on the request's backend and the tenant's isolation tier, which no
// entry check can see. Without authentication there is no entry check to
// add to, and the service is left as it is.
func (s *Server) wireProvisioningAuthorization() {
	if s.opts.databaseSvc == nil || s.opts.authMW == nil {
		return
	}
	var tenants service.TenantInfoLookup
	if s.opts.tenantSvc != nil {
		tenants = s.opts.tenantSvc
	}
	s.opts.databaseSvc = service.NewProvisioningAuthorizedDatabaseService(s.opts.databaseSvc, s.opts.authMW, tenants)
}

// wireTenantDataProbe gives the tenant service the data probe its
// isolation-change precondition consults (tenant.IsolationChangeWiring), over
// the same service registry the transports resolve tenant data through. It is
// wired here, once, so every transport -- including the embedded one, which
// shares this constructor -- reaches the same check. With no registry the
// probe stays unset and an isolation change fails closed.
func (s *Server) wireTenantDataProbe() {
	w, ok := s.opts.tenantSvc.(tenant.IsolationChangeWiring)
	if !ok {
		return
	}
	if reg := s.buildServiceRegistry(); reg != nil {
		w.SetTenantDataProbe(service.NewTenantDataProbe(reg))
	}
}

// wireCompactionPolicies gives the database service the port it sets and
// reads a database's scheduled-compaction policy through: the database's own
// view, over the same service registry the transports resolve every other
// write to the database through. The policy is a replicated record in the
// database's own store, never catalog state. It is wired before the
// provisioning decorator wraps the service, which does not carry the wiring
// method.
func (s *Server) wireCompactionPolicies() {
	w, ok := s.opts.databaseSvc.(service.CompactionPolicyWiring)
	if !ok {
		return
	}
	if reg := s.buildServiceRegistry(); reg != nil {
		w.SetCompactionPolicies(service.NewCompactionPolicies(reg))
	}
}

// wireStoreResolver completes the DROP DATABASE seam for the isolation modes
// whose data is not in the system store.
//
// lifecycle.BuildServices already wired a service.KeyspaceReclaimService into
// the database manager over the system store, which is where every
// shared-store database's keys are. Two modes put a database's data
// elsewhere, and the registries that resolve those stores only exist here:
//
//   - a volume-isolated tenant's databases share a dedicated per-tenant Host,
//     so both the reclamation (go-qrdb#412) and the drop fence go there;
//   - a raft-isolated tenant's database has a shard of its own, so its drop
//     fence goes into that shard -- the Raft group its writes are guarded
//     in.
//
// It is a no-op without the reclaim service, because then there is nothing to
// attach it to, and without either registry, because then every database's
// data is in the system store the reclaim service already targets.
func (s *Server) wireStoreResolver() {
	if s.opts.keyspaceReclaim == nil || (s.opts.tenantHosts == nil && s.opts.raftBackend == nil) {
		return
	}
	resolver := &storeResolver{
		tenantSvc:    s.opts.tenantSvc,
		systemTenant: tenantid.System,
	}
	if s.opts.tenantHosts != nil {
		resolver.hosts = s.opts.tenantHosts
	}
	if s.opts.raftBackend != nil {
		resolver.raft = s.opts.raftBackend
	}
	s.opts.keyspaceReclaim.SetStoreResolver(resolver)
}

// ErrAuthFactorTenantRoutingRequired is returned by Start when a caller
// supplied WithFactorRegistry but no WithTenantStoreResolver.
//
// WithFactorRegistry's own contract promises that the server "automatically"
// exposes the enrollment/challenge/verify surface on every transport once a
// registry is supplied. Building the auth-factor service requires a
// tenant.StoreResolver — there is no unrouted construction to fall back to —
// so a composition that made that promise but supplied no way to keep it
// fails Start() outright rather than booting successfully with every
// transport silently answering Unimplemented for a capability
// WithFactorRegistry said would be there.
var ErrAuthFactorTenantRoutingRequired = errors.New("server: WithFactorRegistry requires WithTenantStoreResolver")

// wireReclaimStoreResolver installs the resolver that routes each pending
// reclaim job (go-qrdb#547) to the node that sweeps it and to its store.
// It resolves through a registry built from the SAME options the transports'
// registries are (restRegistryOptions), so a job is swept through exactly
// the (tenant, database) view it was published through. The registry is
// held for the server's lifetime rather than rebuilt per sweep: the
// per-request construction buildServiceRegistry documents exists to keep
// collection ID generators from being shared across transports, and the
// sweeper touches no collection. It is a no-op without a sweeper.
func (s *Server) wireReclaimStoreResolver() {
	if s.opts.reclaimSweeper == nil || s.opts.dbRegistry == nil {
		return
	}
	reg := newRESTRegistry(s.opts.dbRegistry, s.opts.barrierRegistry, tenantid.System, s.restRegistryOptions()...)
	var shards reclaimShardLeader
	if s.opts.raftBackend != nil {
		shards = s.opts.raftBackend
	}
	s.opts.reclaimSweeper.SetStoreResolver(newReclaimStoreResolver(reg, shards, s.reclaimOrigin()))
}

// reclaimOrigin is this node's identity on reclaim-job index entries: its
// system-shard replica ID, unique within a cluster and stable across
// restarts. The registry that publishes entries and the resolver that
// matches them both take it from here, so they cannot disagree.
func (s *Server) reclaimOrigin() string {
	if s.config == nil {
		return ""
	}
	return strconv.FormatUint(s.config.Raft.ReplicaID, 10)
}

// ensureAuthFactorService completes the WithFactorRegistry contract: when
// a caller injected a FactorRegistry via WithFactorRegistry but did not
// supply an explicit WithAuthFactorService, this method constructs the
// tenant-routed AuthFactorService from the registry and assigns it so every
// transport that checks s.opts.authFactorSvc sees a valid instance.
//
// The service it constructs is the TENANT-ROUTED one
// (service.NewRoutingAuthFactorService), built over the resolver supplied by
// WithTenantStoreResolver — mirroring ensureUserAuthFactorService immediately
// below, for the same reason (go-qrdb#464): this ceremony-time custodian
// surface used to be built over the bare KV servicer, one flat keyspace, so a
// custodian factor enrolled under one tenant resolved a call made under
// another.
//
// It is a no-op when authFactorSvc is already set or when factorRegistry is
// nil (no factors to serve, so WithFactorRegistry was never called and there
// is no promise to keep). It is a hard failure — ErrAuthFactorTenantRoutingRequired
// — when factorRegistry is set but NO tenant store resolver was supplied:
// there is no unrouted fallback, and a composition that cannot say which
// tenant a request belongs to must not silently omit the surface it
// advertised.
func (s *Server) ensureAuthFactorService() error {
	if s.opts.authFactorSvc != nil || s.opts.factorRegistry == nil {
		return nil
	}
	if s.opts.tenantStoreRes == nil {
		return ErrAuthFactorTenantRoutingRequired
	}
	svc, err := service.NewRoutingAuthFactorService(service.RoutingAuthFactorServiceConfig{
		Factors:  s.opts.factorRegistry.All(),
		Resolver: s.opts.tenantStoreRes,
	})
	if err != nil {
		return err
	}
	s.opts.authFactorSvc = svc
	return nil
}

// ensureUserAuthFactorService is the daemon-side analogue of
// sdk/go/node.go's buildLoginOpts: when a User service is configured
// (WithUserService) but no explicit UserAuthFactorServicer was supplied via
// WithUserAuthFactorService, this method constructs the M14 user-side
// auth-factor service and assigns it so every transport that checks
// s.opts.userAuthFactorSvc sees a valid instance. This gives the CLI's
// "auth login" / "auth factor" family (cmd/qrdb/auth.go) the same working
// surface against the standalone daemon that it already has against an
// embedded node.
//
// The service it constructs is the TENANT-ROUTED one
// (service.NewRoutingUserAuthFactorService), built over the resolver supplied
// by WithTenantStoreResolver. It used to be built over the bare KV servicer,
// and that is precisely the defect go-qrdb#393 records: one flat keyspace, so
// a credential enrolled under one tenant resolved a login begun under another.
//
// It is a no-op when userAuthFactorSvc is already set, when no User service is
// configured (mirrors sdk/go/node.go's buildEmbeddedOpts, which only wires the
// login ceremony when svcNode.User != nil — no user model means no principal
// to resolve logins against), and — the fail-closed case — when NO tenant
// store resolver was supplied. There is no unrouted fallback: a composition
// that cannot say which tenant a request belongs to cannot serve a surface
// whose whole isolation boundary is that answer. A composer that wants the
// surface supplies a resolver; the daemon supplies the service outright.
func (s *Server) ensureUserAuthFactorService() error {
	if s.opts.userAuthFactorSvc != nil || s.opts.userSvc == nil || s.opts.tenantStoreRes == nil {
		return nil
	}
	svc, err := service.NewRoutingUserAuthFactorService(service.RoutingUserAuthFactorServiceConfig{
		Resolver: s.opts.tenantStoreRes,
	})
	if err != nil {
		return err
	}
	s.opts.userAuthFactorSvc = svc
	return nil
}

// Start launches the configured API servers. Servers are started sequentially
// in order: REST, gRPC, QUIC, Unix, MCP, MySQL, PostgreSQL. If any server
// fails to start, all previously started servers are rolled back and the
// error is returned.
func (s *Server) Start() error {
	if err := s.start(); err != nil {
		// A server that refuses to serve does not hand its pre-bound
		// sockets back half-used: they are closed, as a caller that gave
		// them up would otherwise leak them.
		s.closePreboundSockets()
		return err
	}
	return nil
}

// start is Start without the pre-bound socket cleanup.
func (s *Server) start() error {
	if err := s.checkPreboundSockets(); err != nil {
		return err
	}
	// Refused before any transport binds: /v1/ca/bootstrap has one owner and
	// serves one bundle, so a cluster CA and a host bundle together is a
	// configuration error, not a choice for qrdb to make.
	if s.opts.caBootstrapBundle != nil && len(clustertls.CertificatesOnly(s.opts.caBootstrapCert)) > 0 {
		return &rest.CABootstrapConflictError{}
	}
	if err := s.ensureAuthFactorService(); err != nil {
		return err
	}
	if err := s.ensureUserAuthFactorService(); err != nil {
		return err
	}

	// Hand every module its tenant-target authorizer BEFORE any transport
	// registers it (startREST/startGRPC/startQUIC/startUnix/startMCP all
	// call the module's Register* hooks below), so a handler the module
	// registers on any transport can call AuthorizeTenantTarget from the
	// moment it is reachable.
	s.wireModuleTenantTargetAuthorizers()

	// The RAG pipeline resolves every record through the same
	// per-(tenant, database) registry the transports use. A node with no
	// database registry leaves it unbound, and RAG then refuses rather than
	// serve records outside any database's seal, ownership and isolation.
	//
	// With request authentication every transport binds the caller's tenant,
	// and a call without one is refused. Without it no call carries a tenant
	// and the operator owns every call, so they act for the system tenant.
	if binder, ok := s.opts.ragSvc.(service.RAGRegistryBinder); ok {
		var opts []service.RAGBindOption
		if s.opts.authMW == nil {
			opts = append(opts, service.RAGUnauthenticatedTenant(tenantid.System))
		}
		binder.BindRegistry(s.buildServiceRegistry(), opts...)
	}

	if s.config.Protocols.REST {
		if err := s.startREST(); err != nil {
			return err
		}
	}

	if s.config.Protocols.GRPC {
		if err := s.startGRPC(); err != nil {
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.QUIC {
		if err := s.startQUIC(); err != nil {
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.Unix {
		if err := s.startUnix(); err != nil {
			s.stopQUIC()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.MCP {
		if err := s.startMCP(); err != nil {
			s.stopUnix()
			s.stopQUIC()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	// The SQL wire protocols carry no sealed-window verb, so the boot-time
	// sealed-window listener has nothing to serve on them. It does not
	// start them: a listener that refuses everything is still an
	// unauthenticated surface.
	if s.config.Protocols.MySQL && startMySQLFn != nil && !s.opts.sealedWindowListener {
		if err := startMySQLFn(s); err != nil {
			s.stopMCP()
			s.stopUnix()
			s.stopQUIC()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	if s.config.Protocols.PostgreSQL && startPgSQLFn != nil && !s.opts.sealedWindowListener {
		if err := startPgSQLFn(s); err != nil {
			s.stopMySQL()
			s.stopMCP()
			s.stopUnix()
			s.stopQUIC()
			s.stopGRPC()
			s.stopREST()
			return err
		}
	}

	// Start capability modules' background resources after every transport has
	// registered them and is serving. A module Start failure rolls the whole
	// server back, exactly like a transport start failure.
	if err := s.startModules(); err != nil {
		s.stopModules()
		s.stopPgSQL()
		s.stopMySQL()
		s.stopMCP()
		s.stopUnix()
		s.stopQUIC()
		s.stopGRPC()
		s.stopREST()
		return err
	}

	// Collections an interrupted create or drop left CREATING or DROPPING
	// are finished in the background, so nobody has to re-issue them.
	s.startCollectionSweeper()

	// Databases whose owning tenant configured a compaction policy are
	// compacted on it. A database with none is never compacted implicitly.
	s.startCompactionScheduler()

	// Metrics whose series configured a retention policy have their
	// expired points pruned on it. A metric with none is never pruned
	// implicitly.
	s.startRetentionScheduler()

	return nil
}

// startModules runs each registered module's Start hook in registration order.
// It stops at the first error and returns it (the caller rolls back). Modules
// already started are stopped by the caller's s.stopModules().
// wireModuleTenantTargetAuthorizers hands every registered module its
// tenant-target authorizer (go-qrdb#587), for a module implementing the
// optional apimodule.ModuleTenantTargetAware extension. s.opts.authMW == nil
// means auth is disabled, and every built-in tenant route treats that as
// "admit unconditionally" (see e.g. rest.authorizeTenantRequest) rather than
// a refusal; SetTenantTargetAuthorizer(nil) is called with a literal nil, as
// a true nil interface, specifically so a module's own
// "if authz == nil { admit }" check sees the identical nil a direct
// comparison would -- assigning the (possibly nil) *authmw.AuthMiddleware
// pointer to the interface parameter instead would box a nil pointer into a
// NON-nil interface value, the classic Go pitfall, and silently invert the
// module's own nil check.
func (s *Server) wireModuleTenantTargetAuthorizers() {
	for _, m := range s.opts.modules {
		aware, ok := m.(apimodule.ModuleTenantTargetAware)
		if !ok {
			continue
		}
		if s.opts.authMW == nil {
			aware.SetTenantTargetAuthorizer(nil)
			continue
		}
		aware.SetTenantTargetAuthorizer(s.opts.authMW)
	}
}

func (s *Server) startModules() error {
	for _, m := range s.opts.modules {
		if err := m.Start(context.Background()); err != nil {
			return fmt.Errorf("server: module %q failed to start: %w", m.Name(), err)
		}
	}
	return nil
}

// stopModules runs each module's Stop hook in reverse registration order (LIFO),
// after the transports have stopped so no new traffic reaches a module while its
// resources are released. Stop errors are logged, not fatal.
func (s *Server) stopModules() {
	for i := len(s.opts.modules) - 1; i >= 0; i-- {
		m := s.opts.modules[i]
		if err := m.Stop(context.Background()); err != nil {
			s.logger.Error("module stop failed", "module", m.Name(), "err", err)
		}
	}
}

// Stop gracefully shuts down all running API servers in reverse startup
// order (etcd, PostgreSQL, MySQL, MCP, Unix, QUIC, gRPC, REST) and waits
// for all background goroutines to complete.
func (s *Server) Stop() {
	s.stopCompactionScheduler()
	s.stopRetentionScheduler()
	s.stopCollectionSweeper()
	s.stopAllEtcd()
	s.stopPgSQL()
	s.stopMySQL()
	s.stopMCP()
	s.stopUnix()
	s.stopQUIC()
	s.stopGRPC()
	s.stopREST()
	s.stopModules()
	s.stopReplicaForwarder()
	s.wg.Wait()
	s.logger.Info("server orchestrator stopped")
}

// RESTAddr returns the REST server's listener address, or empty string
// if REST is not running.
func (s *Server) RESTAddr() string {
	if s.restSrv != nil {
		return s.restSrv.Addr()
	}
	return ""
}

// GRPCAddr returns the gRPC server's listener address, or empty string
// if gRPC is not running.
func (s *Server) GRPCAddr() string {
	if s.grpcSrv != nil {
		return s.grpcSrv.Addr()
	}
	return ""
}

// QUICAddr returns the native QUIC server's listener address, or empty
// string if QUIC is not running.
func (s *Server) QUICAddr() string {
	if s.quicSrv != nil {
		return s.quicSrv.Addr()
	}
	return ""
}

// UnixAddr returns the Unix socket server's listener address, or empty
// string if the Unix server is not running.
func (s *Server) UnixAddr() string {
	if s.unixSrv != nil {
		return s.unixSrv.Addr()
	}
	return ""
}

// MCPAddr returns the MCP server's listener address, or empty string
// if MCP is not running.
func (s *Server) MCPAddr() string {
	if s.mcpSrv != nil {
		return s.mcpSrv.Addr()
	}
	return ""
}

// startREST creates and starts the REST server with configured options.
// soDualFactorEnabled is the ONE predicate for whether the SO dual-factor
// gate is on: auth.so_dual_factor set and an SO authenticator wired. REST's
// SOMiddleware and the gate the gRPC, Unix, QUIC and MCP transports install
// both follow it, so the transports cannot disagree about whether the SO
// surface needs the second factor. It does not depend on an auth middleware:
// a gate with nothing to authenticate the caller fails closed on every
// transport, which is what REST always did.
func (s *Server) soDualFactorEnabled() bool {
	return s.config.Auth.SODualFactor && s.opts.soAuthenticator != nil
}

// soGate returns the SO dual-factor gate the gRPC, Unix, QUIC and MCP
// transports install, or nil when soDualFactorEnabled is false. It is built over the same role service,
// audit logger and audit failure policy REST uses. An unrecognised policy
// fails the start, as it does for REST.
func (s *Server) soGate() (*authmw.SOGate, error) {
	if !s.soDualFactorEnabled() {
		return nil, nil
	}
	policy, err := audit.ParseFailurePolicy(s.config.Auth.AuditFailurePolicy)
	if err != nil {
		return nil, err
	}
	var roles role.NameResolver
	if s.opts.roleSvc != nil {
		roles = s.opts.roleSvc
	}
	return authmw.NewSOGate(roles, s.opts.soAuthenticator, s.opts.auditLogger, policy, slog.Default()), nil
}

func (s *Server) startREST() error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.Server.AdminPort))

	var restOpts []rest.ServerOption

	tlsCfg, err := s.resolveTransportTLS("REST", s.config.Server.Host)
	if err != nil {
		return err
	}
	restOpts = append(restOpts, rest.WithTLS(tlsCfg))
	restOpts = append(restOpts,
		rest.WithMaxBodyBytes(int64(s.config.Transport.ResolvedMaxMessageBytes())),
		rest.WithMaxConnsPerPeer(s.config.Transport.ResolvedMaxConnsPerPeer()),
	)
	if s.opts.restListener != nil {
		ln, err := newLifetimeListener("rest", s.opts.restListener)
		if err != nil {
			return err
		}
		restOpts = append(restOpts, rest.WithListener(ln))
	}
	if s.opts.restPacketConn != nil {
		restOpts = append(restOpts, rest.WithPacketConn(newLifetimePacketConn(s.opts.restPacketConn)))
	}
	if s.opts.caCertPool != nil {
		restOpts = append(restOpts, rest.WithCACertPool(s.opts.caCertPool))
	}
	if fwd, ok := s.replicaForwarding(); ok {
		restOpts = append(restOpts, rest.WithReplicaForwarding(fwd))
	}
	if s.opts.gatherer != nil {
		restOpts = append(restOpts, rest.WithGatherer(s.opts.gatherer))
	}
	if s.opts.resolver != nil {
		restOpts = append(restOpts, rest.WithLeaderForwarding(s.opts.resolver))
	}
	if s.opts.host != nil {
		restOpts = append(restOpts, rest.WithHost(s.opts.host))
	}
	if s.opts.userSvc != nil {
		restOpts = append(restOpts, rest.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		restOpts = append(restOpts, rest.WithRoleService(s.opts.roleSvc))
	}
	if s.opts.certProv != nil {
		restOpts = append(restOpts, rest.WithCertProvider(s.opts.certProv))
	}
	if s.opts.certEnroll != nil {
		restOpts = append(restOpts, rest.WithCertEnrollment(s.opts.certEnroll))
	}
	if s.opts.invitations != nil {
		restOpts = append(restOpts, rest.WithInvitations(s.opts.invitations))
	}
	if s.opts.onboarding != nil {
		restOpts = append(restOpts, rest.WithTenantOnboarding(s.opts.onboarding))
	}
	if s.opts.authMW != nil {
		restOpts = append(restOpts, rest.WithAuthMiddleware(s.opts.authMW))
	}
	if s.opts.sealedWindowProbe != nil && !s.opts.sealedWindowListener {
		// The boot-time listener's gate is installed by
		// rest.NewSealedWindowServer below.
		restOpts = append(restOpts, rest.WithSealedWindowProbe(s.opts.sealedWindowProbe))
	}
	if s.opts.awaitingAdminCredentialProbe != nil {
		restOpts = append(restOpts, rest.WithAwaitingAdminCredentialProbe(s.opts.awaitingAdminCredentialProbe))
	}
	if s.opts.batchSvc != nil {
		restOpts = append(restOpts, rest.WithBatch(s.opts.batchSvc))
	}
	if s.opts.txSvc != nil {
		restOpts = append(restOpts, rest.WithTransaction(s.opts.txSvc))
	}
	if s.opts.mvccSvc != nil {
		restOpts = append(restOpts, rest.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.leaseSvc != nil {
		restOpts = append(restOpts, rest.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		restOpts = append(restOpts, rest.WithLock(s.opts.lockSvc))
	}
	if s.opts.tsSvc != nil {
		restOpts = append(restOpts, rest.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.indexSvc != nil {
		restOpts = append(restOpts, rest.WithIndex(s.opts.indexSvc))
	}
	if s.opts.vectorSvc != nil {
		restOpts = append(restOpts, rest.WithVector(s.opts.vectorSvc))
	}
	if s.opts.embeddingSvc != nil {
		restOpts = append(restOpts, rest.WithEmbedding(s.opts.embeddingSvc))
	}
	if s.opts.ragSvc != nil {
		restOpts = append(restOpts, rest.WithRAG(s.opts.ragSvc))
	}
	if s.opts.watchSvc != nil {
		restOpts = append(restOpts, rest.WithWatch(s.opts.watchSvc))
	}
	if s.opts.databaseSvc != nil {
		restOpts = append(restOpts, rest.WithDatabaseService(s.opts.databaseSvc))
		if s.kvSvc != nil && newSQLServiceFn != nil {
			restOpts = append(restOpts, rest.WithSQLService(newSQLServiceFn(s)))
		}
	}
	if s.opts.barrierSvc != nil {
		restOpts = append(restOpts, rest.WithBarrierService(s.opts.barrierSvc))
	}
	if m := s.effectiveEncryptionMigrator(); m != nil {
		restOpts = append(restOpts, rest.WithEncryptionMigrator(m))
	}
	if s.opts.pinBackend != nil {
		restOpts = append(restOpts, rest.WithPINBackend(s.opts.pinBackend))
	}
	if s.opts.ceremonySvc != nil {
		restOpts = append(restOpts, rest.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.credentialSvc != nil {
		restOpts = append(restOpts, rest.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.dekSvc != nil {
		restOpts = append(restOpts, rest.WithDEKService(s.opts.dekSvc))
	}
	if s.opts.ceremonyCA != nil {
		restOpts = append(restOpts, rest.WithCeremonyCA(s.opts.ceremonyCA))
	}
	if s.opts.collectionSvc != nil {
		restOpts = append(restOpts, rest.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.tenantSvc != nil {
		restOpts = append(restOpts, rest.WithTenantService(s.opts.tenantSvc))
	}
	if s.opts.barrierRegistry != nil {
		if volumeSvc, ok := s.opts.barrierRegistry.VolumeService(); ok {
			restOpts = append(restOpts, rest.WithVolumeService(volumeSvc))
		}
	}
	if s.opts.backupSvc != nil {
		restOpts = append(restOpts, rest.WithBackupService(s.opts.backupSvc))
	}
	if s.opts.rebalanceSvc != nil {
		restOpts = append(restOpts, rest.WithRebalanceService(s.opts.rebalanceSvc))
	}
	if s.opts.caBootstrapBundle != nil {
		restOpts = append(restOpts, rest.WithCABootstrapProvider(s.opts.caBootstrapBundle))
	}
	if len(s.opts.caBootstrapCert) > 0 {
		restOpts = append(restOpts, rest.WithCABootstrap(s.opts.caBootstrapCert))
	}
	restOpts = append(restOpts, rest.WithOnEtcdUpdated(func(dbName string, cfg *database.EtcdConfig) error {
		return s.StartEtcd(dbName, cfg)
	}))
	if s.opts.webUI != nil {
		restOpts = append(restOpts, rest.WithWebUI(s.opts.webUI))
	}
	if s.opts.auditLogger != nil {
		restOpts = append(restOpts, rest.WithAuditLogger(s.opts.auditLogger))
	}
	if s.opts.replicatedAudit != nil {
		restOpts = append(restOpts, rest.WithReplicatedAuditLogger(s.opts.replicatedAudit))
	}
	if s.opts.authFactorSvc != nil {
		restOpts = append(restOpts, rest.WithAuthFactorService(s.opts.authFactorSvc))
	}
	if s.opts.userAuthFactorSvc != nil {
		restOpts = append(restOpts, rest.WithUserAuthFactorService(s.opts.userAuthFactorSvc))
	}
	if s.opts.soAuthenticator != nil {
		restOpts = append(restOpts, rest.WithSOAuthenticator(s.opts.soAuthenticator))
	}
	if s.soDualFactorEnabled() {
		restOpts = append(restOpts, rest.WithSODualFactor())
	}
	if s.opts.clusterJoinProvider != nil {
		restOpts = append(restOpts, rest.WithClusterJoin(s.opts.clusterJoinProvider, s.opts.clusterJoinStore, s.opts.clusterJoinTrustDomain))
		if a := joinadvertise.New(s.opts.leaseSvc, s.opts.mvccSvc); a != nil {
			restOpts = append(restOpts, rest.WithClusterJoinAdvertiser(a))
		}
	}
	// The SO dual-factor gate reads the operator's audit failure posture from
	// the SAME key the authorization middleware does (go-qrdb#445). It is
	// resolved from config here rather than taken as a ServerOption so that an
	// embedder driving pkg/server directly cannot end up with the two gates on
	// different postures by setting the YAML key and forgetting the option --
	// which is the split the ruling exists to remove. An unrecognised value
	// fails the start for the same reason it fails the daemon's boot: a node
	// that cannot tell which posture was asked for must not choose one.
	auditFailure, err := audit.ParseFailurePolicy(s.config.Auth.AuditFailurePolicy)
	if err != nil {
		return err
	}
	restOpts = append(restOpts, rest.WithAuditFailurePolicy(auditFailure))
	if reg := s.buildServiceRegistry(); reg != nil {
		restOpts = append(restOpts, rest.WithServiceRegistry(reg))
	}
	if s.opts.clusterKeyReceiver != nil {
		restOpts = append(restOpts, rest.WithClusterKeyReceiver(s.opts.clusterKeyReceiver))
	}

	if len(s.opts.modules) > 0 {
		restOpts = append(restOpts, rest.WithServiceModules(s.opts.modules))
	}

	if s.opts.sealedWindowListener {
		// One sealed REST surface: the listener serves exactly what a host
		// mounts through rest.NewSealedWindowHandler.
		s.restSrv = rest.NewSealedWindowServer(addr, s.opts.barrierSvc, s.opts.ceremonySvc, s.opts.credentialSvc, s.listenerProbe(), restOpts...)
	} else {
		s.restSrv = rest.NewServer(s.kvSvc, s.adminSvc, addr, restOpts...)
	}

	if err := s.restSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("REST server started", "addr", s.restSrv.Addr())
	return nil
}

// ServiceRegistry returns the transport-neutral per-(tenant, database)
// service.Registry this server resolves data-plane requests through -- the
// same object it hands to the REST, gRPC, Unix, MySQL, and PostgreSQL
// listeners. It returns nil when no database registry is configured (see
// WithDatabaseServiceRegistry), exactly as the wire listeners observe.
//
// It exists so an in-process embedder -- e.g. sdk/go/driver/node, which
// speaks no wire protocol at all -- can reach the registry the same way the
// listeners do, rather than reassembling an equivalent one from the
// individual service fields and inevitably drifting from the tenant/barrier/
// quota/isolation resolution the listeners enforce.
//
// A fresh registry is returned per call, exactly like every internal call
// site; see buildServiceRegistry for why it is not memoized. Callers that
// resolve repeatedly should hold on to one registry rather than calling this
// per request.
func (s *Server) ServiceRegistry() service.Registry {
	return s.buildServiceRegistry()
}

// buildServiceRegistry constructs the transport-neutral per-(tenant, database)
// service.Registry shared by every protocol (REST, gRPC, Unix). It returns nil
// when no database registry is configured, in which case transports fall back
// to their singleton service fields (single-database / embedded behaviour).
//
// A fresh restServiceRegistry is deliberately built per call rather than
// memoized on the Server. Every input it closes over is fixed at server.New
// time, so a memoized instance would be built from identical inputs -- but it
// would also SHARE its two collection-service caches across transports:
// dedicatedStoreServiceCache, whose *DatabaseServices bundles carry the
// per-collection, process-local increment ID generators of
// service.CollectionService (see buildDedicatedStoreServices), and
// compositeCollectionCache, which holds the same state for the namespaced
// shared-shard path. Sharing them across transports would change observable ID
// allocation, so memoization is not a pure optimization and is not applied
// here.
func (s *Server) buildServiceRegistry() service.Registry {
	if s.opts.dbRegistry == nil {
		return nil
	}
	return NewRESTServiceRegistry(s.opts.dbRegistry, s.opts.barrierRegistry, tenantid.System, s.restRegistryOptions()...)
}

// restRegistryOptions is the one option list every restServiceRegistry this
// server builds is configured with -- the transports' per-call registries
// and the reclaim-store resolver's long-lived one alike.
func (s *Server) restRegistryOptions() []RESTRegistryOption {
	regOpts := []RESTRegistryOption{withReclaimOrigin(s.reclaimOrigin())}
	if s.opts.tenantSvc != nil {
		regOpts = append(regOpts, WithTenantServicer(s.opts.tenantSvc))
	}
	if s.opts.dbManager != nil {
		regOpts = append(regOpts, WithDatabaseCatalog(newManagerCatalog(s.opts.dbManager)))
		regOpts = append(regOpts, withDatabaseManager(s.opts.dbManager))
	}
	if s.opts.tenantHosts != nil {
		regOpts = append(regOpts, withTenantHostRegistry(s.opts.tenantHosts))
	}
	if s.opts.raftBackend != nil {
		regOpts = append(regOpts, withRaftBackendFactory(s.opts.raftBackend))
	}
	if s.opts.quotaAccountant != nil {
		regOpts = append(regOpts, withQuotaAccountant(s.opts.quotaAccountant))
	}
	if s.config.SQL.DefaultDatabase != "" {
		regOpts = append(regOpts, WithSharedSQLDefaultDatabase(s.config.SQL.DefaultDatabase))
	}
	if s.opts.clusterSeal != nil {
		regOpts = append(regOpts, withClusterSealRegistry(s.opts.clusterSeal))
	}
	return regOpts
}

// startGRPC creates and starts the gRPC server with configured options.
func (s *Server) startGRPC() error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.Server.GRPCPort))

	var grpcOpts []grpcapi.ServerOption

	tlsCfg, err := s.resolveTransportTLS("gRPC", s.config.Server.Host)
	if err != nil {
		return err
	}
	grpcOpts = append(grpcOpts, grpcapi.WithTLS(tlsCfg))
	grpcOpts = append(grpcOpts,
		grpcapi.WithMaxMessageBytes(s.config.Transport.ResolvedMaxMessageBytes()),
		grpcapi.WithMaxConnsPerPeer(s.config.Transport.ResolvedMaxConnsPerPeer()),
	)
	if s.opts.grpcListener != nil {
		ln, err := newLifetimeListener("grpc", s.opts.grpcListener)
		if err != nil {
			return err
		}
		grpcOpts = append(grpcOpts, grpcapi.WithListener(ln))
	}
	if s.opts.grpcMaxConcurrentStreams != 0 {
		grpcOpts = append(grpcOpts, grpcapi.WithMaxConcurrentStreams(s.opts.grpcMaxConcurrentStreams))
	}
	if s.opts.userSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithRoleService(s.opts.roleSvc))
	}
	if gate, err := s.soGate(); err != nil {
		return err
	} else if gate != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithSOGate(gate))
	}
	if s.opts.certProv != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCertProvider(s.opts.certProv))
	}
	if s.opts.certEnroll != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCertEnrollment(s.opts.certEnroll))
	}
	if s.opts.invitations != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithInvitations(s.opts.invitations))
	}
	if s.opts.onboarding != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithTenantOnboarding(s.opts.onboarding))
	}
	if s.opts.authMW != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithAuthMiddleware(s.opts.authMW))
	}
	if fwd, ok := s.replicaForwarding(); ok {
		grpcOpts = append(grpcOpts, grpcapi.WithReplicaForwarding(fwd))
	}
	if s.opts.sealedWindowProbe != nil {
		if s.opts.sealedWindowListener {
			grpcOpts = append(grpcOpts, grpcapi.WithSealedWindowListener(s.listenerProbe()))
		} else {
			grpcOpts = append(grpcOpts, grpcapi.WithSealedWindowProbe(s.opts.sealedWindowProbe))
		}
	}
	if s.opts.awaitingAdminCredentialProbe != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithAwaitingAdminCredentialProbe(s.opts.awaitingAdminCredentialProbe))
	}
	if s.opts.barrierSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithBarrierService(s.opts.barrierSvc))
	}
	if m := s.effectiveEncryptionMigrator(); m != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithEncryptionMigrator(m))
	}
	if s.opts.pinBackend != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithPINBackend(s.opts.pinBackend))
	}
	if s.opts.ceremonySvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.ceremonyCA != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCeremonyCA(s.opts.ceremonyCA))
	}
	if s.opts.credentialSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.dekSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithDEKService(s.opts.dekSvc))
	}
	if s.opts.auditLogger != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithAuditLogger(s.opts.auditLogger))
	}
	if s.opts.replicatedAudit != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithReplicatedAuditLogger(s.opts.replicatedAudit))
	}
	if s.opts.collectionSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.mvccSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.txSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithTransaction(s.opts.txSvc))
	}
	if s.opts.batchSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithBatch(s.opts.batchSvc))
	}
	if s.opts.watchSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithWatch(s.opts.watchSvc))
	}
	if s.opts.leaseSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithLock(s.opts.lockSvc))
	}
	if s.opts.indexSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithIndex(s.opts.indexSvc))
	}
	if s.opts.tsSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.vectorSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithVector(s.opts.vectorSvc))
	}
	if s.opts.embeddingSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithEmbedding(s.opts.embeddingSvc))
	}
	if s.opts.ragSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithRAGService(s.opts.ragSvc))
	}
	if s.opts.databaseSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithDatabaseService(s.opts.databaseSvc))
		if s.kvSvc != nil && newSQLServiceFn != nil {
			grpcOpts = append(grpcOpts, grpcapi.WithSQLService(newSQLServiceFn(s)))
		}
	}
	if s.opts.tenantSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithTenantService(s.opts.tenantSvc))
	}
	if s.opts.barrierRegistry != nil {
		if volumeSvc, ok := s.opts.barrierRegistry.VolumeService(); ok {
			grpcOpts = append(grpcOpts, grpcapi.WithVolumeService(volumeSvc))
		}
	}
	if s.opts.backupSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithBackupService(s.opts.backupSvc))
	}
	if s.opts.rebalanceSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithRebalanceService(s.opts.rebalanceSvc))
	}
	if s.opts.authFactorSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithAuthFactorService(s.opts.authFactorSvc))
	}
	if s.opts.userAuthFactorSvc != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithUserAuthFactorService(s.opts.userAuthFactorSvc))
	}
	if reg := s.buildServiceRegistry(); reg != nil {
		grpcOpts = append(grpcOpts, grpcapi.WithServiceRegistry(reg))
	}

	if len(s.opts.modules) > 0 {
		grpcOpts = append(grpcOpts, grpcapi.WithServiceModules(s.opts.modules))
	}

	s.grpcSrv = grpcapi.NewServer(s.kvSvc, s.adminSvc, addr, grpcOpts...)

	if err := s.grpcSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("gRPC server started", "addr", s.grpcSrv.Addr())
	return nil
}

// startQUIC creates and starts the native QUIC transport server.
func (s *Server) startQUIC() error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.Server.QUICPort))

	var quicOpts []quicapi.ServerOption

	tlsCfg, err := s.resolveTransportTLS("QUIC", s.config.Server.Host)
	if err != nil {
		return err
	}
	quicOpts = append(quicOpts, quicapi.WithTLS(tlsCfg))
	quicOpts = append(quicOpts,
		quicapi.WithMaxPayloadBytes(s.config.Transport.ResolvedMaxMessageBytes()),
		quicapi.WithMaxStreamsPerConn(s.config.Transport.ResolvedMaxStreamsPerConn()),
		quicapi.WithMaxConnsPerPeer(s.config.Transport.ResolvedMaxConnsPerPeer()),
	)
	if s.opts.quicPacketConn != nil {
		quicOpts = append(quicOpts, quicapi.WithPacketConn(newLifetimePacketConn(s.opts.quicPacketConn)))
	}
	if s.opts.userSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithRoleService(s.opts.roleSvc))
	}
	if gate, err := s.soGate(); err != nil {
		return err
	} else if gate != nil {
		quicOpts = append(quicOpts, quicapi.WithSOGate(gate))
	}
	if s.opts.certProv != nil {
		quicOpts = append(quicOpts, quicapi.WithCertProvider(s.opts.certProv))
	}
	if s.opts.certEnroll != nil {
		quicOpts = append(quicOpts, quicapi.WithCertEnrollment(s.opts.certEnroll))
	}
	if s.opts.invitations != nil {
		quicOpts = append(quicOpts, quicapi.WithInvitations(s.opts.invitations))
	}
	if s.opts.onboarding != nil {
		quicOpts = append(quicOpts, quicapi.WithTenantOnboarding(s.opts.onboarding))
	}
	if s.opts.authMW != nil {
		quicOpts = append(quicOpts, quicapi.WithAuthMiddleware(s.opts.authMW))
	}
	if fwd, ok := s.replicaForwarding(); ok {
		quicOpts = append(quicOpts, quicapi.WithReplicaForwarding(fwd))
	}
	if s.opts.sealedWindowProbe != nil {
		if s.opts.sealedWindowListener {
			quicOpts = append(quicOpts, quicapi.WithSealedWindowListener(s.listenerProbe()))
		} else {
			quicOpts = append(quicOpts, quicapi.WithSealedWindowProbe(s.opts.sealedWindowProbe))
		}
	}
	if s.opts.awaitingAdminCredentialProbe != nil {
		quicOpts = append(quicOpts, quicapi.WithAwaitingAdminCredentialProbe(s.opts.awaitingAdminCredentialProbe))
	}
	if s.opts.collectionSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.batchSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithBatch(s.opts.batchSvc))
	}
	if s.opts.txSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithTransaction(s.opts.txSvc))
	}
	if s.opts.mvccSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.leaseSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithLock(s.opts.lockSvc))
	}
	if s.opts.tsSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.indexSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithIndex(s.opts.indexSvc))
	}
	if s.opts.vectorSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithVector(s.opts.vectorSvc))
	}
	if s.opts.watchSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithWatch(s.opts.watchSvc))
	}
	if s.opts.databaseSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithDatabaseService(s.opts.databaseSvc))
		if s.kvSvc != nil && newSQLServiceFn != nil {
			quicOpts = append(quicOpts, quicapi.WithSQLService(newSQLServiceFn(s)))
		}
	}
	if s.opts.barrierSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithBarrierService(s.opts.barrierSvc))
	}
	if m := s.effectiveEncryptionMigrator(); m != nil {
		quicOpts = append(quicOpts, quicapi.WithEncryptionMigrator(m))
	}
	if s.opts.pinBackend != nil {
		quicOpts = append(quicOpts, quicapi.WithPINBackend(s.opts.pinBackend))
	}
	if s.opts.ceremonySvc != nil {
		quicOpts = append(quicOpts, quicapi.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.ceremonyCA != nil {
		quicOpts = append(quicOpts, quicapi.WithCeremonyCA(s.opts.ceremonyCA))
	}
	if s.opts.auditLogger != nil {
		quicOpts = append(quicOpts, quicapi.WithAuditLogger(s.opts.auditLogger))
	}
	if s.opts.replicatedAudit != nil {
		quicOpts = append(quicOpts, quicapi.WithReplicatedAuditLogger(s.opts.replicatedAudit))
	}
	if s.opts.tenantSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithTenantService(s.opts.tenantSvc))
	}
	if s.opts.barrierRegistry != nil {
		if volumeSvc, ok := s.opts.barrierRegistry.VolumeService(); ok {
			quicOpts = append(quicOpts, quicapi.WithVolumeService(volumeSvc))
		}
	}
	if s.opts.backupSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithBackupService(s.opts.backupSvc))
	}
	if s.opts.rebalanceSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithRebalanceService(s.opts.rebalanceSvc))
	}
	if s.opts.embeddingSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithEmbedding(s.opts.embeddingSvc))
	}
	if s.opts.ragSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithRAGService(s.opts.ragSvc))
	}
	if s.opts.authFactorSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithAuthFactorService(s.opts.authFactorSvc))
	}
	if s.opts.userAuthFactorSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithUserAuthFactorService(s.opts.userAuthFactorSvc))
	}
	if s.opts.credentialSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.dekSvc != nil {
		quicOpts = append(quicOpts, quicapi.WithDEKService(s.opts.dekSvc))
	}
	if reg := s.buildServiceRegistry(); reg != nil {
		quicOpts = append(quicOpts, quicapi.WithServiceRegistry(reg))
	}

	if len(s.opts.modules) > 0 {
		quicOpts = append(quicOpts, quicapi.WithServiceModules(s.opts.modules))
	}

	s.quicSrv = quicapi.NewServer(s.kvSvc, s.adminSvc, addr, quicOpts...)

	if err := s.quicSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("QUIC server started", "addr", s.quicSrv.Addr())
	return nil
}

// startUnix creates and starts the Unix domain socket server.
func (s *Server) startUnix() error {
	var unixOpts []unixapi.ServerOption
	tlsCfg, err := s.resolveTransportTLS("unix", s.config.Server.Host)
	if err != nil {
		return err
	}
	unixOpts = append(unixOpts, unixapi.WithTLS(tlsCfg))
	if s.opts.grpcMaxConcurrentStreams != 0 {
		unixOpts = append(unixOpts, unixapi.WithMaxConcurrentStreams(s.opts.grpcMaxConcurrentStreams))
	}
	unixOpts = append(unixOpts, unixapi.WithMaxMessageBytes(s.config.Transport.ResolvedMaxMessageBytes()))
	if s.opts.userSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithRoleService(s.opts.roleSvc))
	}
	if gate, err := s.soGate(); err != nil {
		return err
	} else if gate != nil {
		unixOpts = append(unixOpts, unixapi.WithSOGate(gate))
	}
	if s.opts.certProv != nil {
		unixOpts = append(unixOpts, unixapi.WithCertProvider(s.opts.certProv))
	}
	if s.opts.certEnroll != nil {
		unixOpts = append(unixOpts, unixapi.WithCertEnrollment(s.opts.certEnroll))
	}
	if s.opts.invitations != nil {
		unixOpts = append(unixOpts, unixapi.WithInvitations(s.opts.invitations))
	}
	if s.opts.onboarding != nil {
		unixOpts = append(unixOpts, unixapi.WithTenantOnboarding(s.opts.onboarding))
	}
	if s.opts.authMW != nil {
		unixOpts = append(unixOpts, unixapi.WithAuthMiddleware(s.opts.authMW))
	}
	if fwd, ok := s.replicaForwarding(); ok {
		unixOpts = append(unixOpts, unixapi.WithReplicaForwarding(fwd))
	}
	if s.opts.sealedWindowProbe != nil {
		if s.opts.sealedWindowListener {
			unixOpts = append(unixOpts, unixapi.WithSealedWindowListener(s.listenerProbe()))
		} else {
			unixOpts = append(unixOpts, unixapi.WithSealedWindowProbe(s.opts.sealedWindowProbe))
		}
	}
	if s.opts.awaitingAdminCredentialProbe != nil {
		unixOpts = append(unixOpts, unixapi.WithAwaitingAdminCredentialProbe(s.opts.awaitingAdminCredentialProbe))
	}
	if s.opts.barrierSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithBarrierService(s.opts.barrierSvc))
	}
	if m := s.effectiveEncryptionMigrator(); m != nil {
		unixOpts = append(unixOpts, unixapi.WithEncryptionMigrator(m))
	}
	if s.opts.pinBackend != nil {
		unixOpts = append(unixOpts, unixapi.WithPINBackend(s.opts.pinBackend))
	}
	if s.opts.ceremonySvc != nil {
		unixOpts = append(unixOpts, unixapi.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.ceremonyCA != nil {
		unixOpts = append(unixOpts, unixapi.WithCeremonyCA(s.opts.ceremonyCA))
	}
	if s.opts.credentialSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.dekSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithDEKService(s.opts.dekSvc))
	}
	if s.opts.collectionSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.mvccSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.txSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithTransaction(s.opts.txSvc))
	}
	if s.opts.batchSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithBatch(s.opts.batchSvc))
	}
	if s.opts.watchSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithWatch(s.opts.watchSvc))
	}
	if s.opts.leaseSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithLock(s.opts.lockSvc))
	}
	if s.opts.indexSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithIndex(s.opts.indexSvc))
	}
	if s.opts.tsSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.vectorSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithVector(s.opts.vectorSvc))
	}
	if s.opts.embeddingSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithEmbedding(s.opts.embeddingSvc))
	}
	if s.opts.ragSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithRAGService(s.opts.ragSvc))
	}
	if s.opts.databaseSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithDatabaseService(s.opts.databaseSvc))
		if s.kvSvc != nil && newSQLServiceFn != nil {
			unixOpts = append(unixOpts, unixapi.WithSQLService(newSQLServiceFn(s)))
		}
	}
	if s.opts.auditLogger != nil {
		unixOpts = append(unixOpts, unixapi.WithAuditLogger(s.opts.auditLogger))
	}
	if s.opts.replicatedAudit != nil {
		unixOpts = append(unixOpts, unixapi.WithReplicatedAuditLogger(s.opts.replicatedAudit))
	}
	if s.opts.barrierRegistry != nil {
		if volumeSvc, ok := s.opts.barrierRegistry.VolumeService(); ok {
			unixOpts = append(unixOpts, unixapi.WithVolumeService(volumeSvc))
		}
	}
	if s.opts.tenantSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithTenantService(s.opts.tenantSvc))
		// The same TenantServicer backs both tenant CRUD (WithTenantService)
		// and the per-tenant barrier lifecycle RPCs (WithTenantBarrierService)
		// — mirrors the TCP gRPC server, which always registers
		// TenantBarrierService alongside QRDBService using s.tenantSvc.
		unixOpts = append(unixOpts, unixapi.WithTenantBarrierService(s.opts.tenantSvc))
	}
	if s.opts.backupSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithBackupService(s.opts.backupSvc))
	}
	if s.opts.rebalanceSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithRebalanceService(s.opts.rebalanceSvc))
	}
	if s.opts.authFactorSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithAuthFactorService(s.opts.authFactorSvc))
	}
	if s.opts.userAuthFactorSvc != nil {
		unixOpts = append(unixOpts, unixapi.WithUserAuthFactorService(s.opts.userAuthFactorSvc))
	}
	if reg := s.buildServiceRegistry(); reg != nil {
		unixOpts = append(unixOpts, unixapi.WithServiceRegistry(reg))
	}

	if len(s.opts.modules) > 0 {
		unixOpts = append(unixOpts, unixapi.WithServiceModules(s.opts.modules))
	}

	s.unixSrv = unixapi.NewServer(s.kvSvc, s.adminSvc, s.config.Unix.SocketPath, unixOpts...)

	if err := s.unixSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("Unix socket server started", "path", s.unixSrv.Addr())
	return nil
}

// startMCP creates and starts the MCP server with configured options.
func (s *Server) startMCP() error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.MCP.Port))

	var mcpOpts []mcpapi.ServerOption

	tlsCfg, err := s.resolveTransportTLS("MCP", s.config.Server.Host)
	if err != nil {
		return err
	}
	mcpOpts = append(mcpOpts, mcpapi.WithTLS(tlsCfg))
	if s.opts.mcpMaxConnections != 0 {
		mcpOpts = append(mcpOpts, mcpapi.WithMaxConnections(s.opts.mcpMaxConnections))
	}
	mcpOpts = append(mcpOpts,
		mcpapi.WithMaxBodyBytes(int64(s.config.Transport.ResolvedMaxMessageBytes())),
		mcpapi.WithMaxConnsPerPeer(s.config.Transport.ResolvedMaxConnsPerPeer()),
	)
	if s.opts.mcpListener != nil {
		ln, err := newLifetimeListener("mcp", s.opts.mcpListener)
		if err != nil {
			return err
		}
		mcpOpts = append(mcpOpts, mcpapi.WithListener(ln))
	}
	if s.opts.authMW != nil {
		// Authenticate the mTLS peer and authorize every tool call
		// (default-deny). Parity with REST/gRPC/QUIC.
		mcpOpts = append(mcpOpts, mcpapi.WithAuthMiddleware(s.opts.authMW))
	}
	if fwd, ok := s.replicaForwarding(); ok {
		mcpOpts = append(mcpOpts, mcpapi.WithReplicaForwarding(fwd))
	}
	if s.opts.userSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithUserService(s.opts.userSvc))
	}
	if s.opts.roleSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithRoleService(s.opts.roleSvc))
	}
	if gate, err := s.soGate(); err != nil {
		return err
	} else if gate != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithSOGate(gate))
	}
	if s.opts.certProv != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCertProvider(s.opts.certProv))
	}
	if s.opts.certEnroll != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCertEnrollment(s.opts.certEnroll))
	}
	if s.opts.invitations != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithInvitations(s.opts.invitations))
	}
	if s.opts.onboarding != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithTenantOnboarding(s.opts.onboarding))
	}
	if s.opts.sealedWindowProbe != nil && !s.opts.sealedWindowListener {
		// The boot-time listener's gate is installed by
		// mcpapi.NewSealedWindowServer below.
		mcpOpts = append(mcpOpts, mcpapi.WithSealedWindowProbe(s.opts.sealedWindowProbe))
	}
	if s.opts.awaitingAdminCredentialProbe != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithAwaitingAdminCredentialProbe(s.opts.awaitingAdminCredentialProbe))
	}
	if s.opts.barrierSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithBarrierService(s.opts.barrierSvc))
	}
	if m := s.effectiveEncryptionMigrator(); m != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithEncryptionMigrator(m))
	}
	if s.opts.pinBackend != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithPINBackend(s.opts.pinBackend))
	}
	if s.opts.ceremonySvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCeremonyService(s.opts.ceremonySvc))
	}
	if s.opts.credentialSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCredentialService(s.opts.credentialSvc))
	}
	if s.opts.dekSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithDEKService(s.opts.dekSvc))
	}
	if s.opts.ceremonyCA != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCeremonyCA(s.opts.ceremonyCA))
	}
	if s.opts.collectionSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.batchSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithBatch(s.opts.batchSvc))
	}
	if s.opts.txSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithTransaction(s.opts.txSvc))
	}
	if s.opts.mvccSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.leaseSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithLease(s.opts.leaseSvc))
	}
	if s.opts.lockSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithLock(s.opts.lockSvc))
	}
	if s.opts.tsSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithTimeSeries(s.opts.tsSvc))
	}
	if s.opts.indexSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithIndex(s.opts.indexSvc))
	}
	if s.opts.vectorSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithVector(s.opts.vectorSvc))
	}
	if s.opts.embeddingSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithEmbedding(s.opts.embeddingSvc))
	}
	if s.opts.ragSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithRAG(s.opts.ragSvc))
	}
	if s.opts.watchSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithWatch(s.opts.watchSvc))
	}
	if s.opts.databaseSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithDatabaseService(s.opts.databaseSvc))
		if s.kvSvc != nil && newSQLServiceFn != nil {
			mcpOpts = append(mcpOpts, mcpapi.WithSQLService(newSQLServiceFn(s)))
		}
	}
	if s.opts.auditLogger != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithAuditLogger(s.opts.auditLogger))
	}
	if s.opts.replicatedAudit != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithReplicatedAuditLogger(s.opts.replicatedAudit))
	}
	if s.opts.tenantSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithTenantService(s.opts.tenantSvc))
	}
	if s.opts.barrierRegistry != nil {
		if volumeSvc, ok := s.opts.barrierRegistry.VolumeService(); ok {
			mcpOpts = append(mcpOpts, mcpapi.WithVolumeService(volumeSvc))
		}
	}
	if s.opts.backupSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithBackupService(s.opts.backupSvc))
	}
	if s.opts.rebalanceSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithRebalanceService(s.opts.rebalanceSvc))
	}
	if s.opts.authFactorSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithAuthFactorService(s.opts.authFactorSvc))
	}
	if s.opts.userAuthFactorSvc != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithUserAuthFactorService(s.opts.userAuthFactorSvc))
	}
	if reg := s.buildServiceRegistry(); reg != nil {
		mcpOpts = append(mcpOpts, mcpapi.WithServiceRegistry(reg))
	}

	if len(s.opts.modules) > 0 {
		mcpOpts = append(mcpOpts, mcpapi.WithServiceModules(s.opts.modules))
	}
	mcpOpts = append(mcpOpts, s.opts.mcpServerOpts...)

	if s.opts.sealedWindowListener {
		// One sealed MCP surface: the listener serves exactly the tools a
		// host adds through mcpapi.SealedWindowTools.
		s.mcpSrv = mcpapi.NewSealedWindowServer(addr, s.opts.barrierSvc, s.opts.ceremonySvc, s.opts.credentialSvc, s.listenerProbe(), mcpOpts...)
	} else {
		s.mcpSrv = mcpapi.NewServer(s.kvSvc, s.adminSvc, addr, mcpOpts...)
	}

	if err := s.mcpSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("MCP server started", "addr", s.mcpSrv.Addr())
	return nil
}

// stopREST stops the REST server if it was started.
func (s *Server) stopREST() {
	if s.restSrv != nil {
		s.restSrv.Stop()
	}
}

// stopGRPC stops the gRPC server if it was started.
func (s *Server) stopGRPC() {
	if s.grpcSrv != nil {
		s.grpcSrv.Stop()
	}
}

// stopQUIC stops the native QUIC server if it was started.
func (s *Server) stopQUIC() {
	if s.quicSrv != nil {
		s.quicSrv.Stop()
	}
}

// stopUnix stops the Unix socket server if it was started.
func (s *Server) stopUnix() {
	if s.unixSrv != nil {
		s.unixSrv.Stop()
	}
}

// stopMCP stops the MCP server if it was started.
func (s *Server) stopMCP() {
	if s.mcpSrv != nil {
		s.mcpSrv.Stop()
	}
}

// MySQLAddr returns the MySQL server's listener address, or empty string
// if MySQL is not running.
func (s *Server) MySQLAddr() string {
	if s.mysqlSrv != nil {
		return s.mysqlSrv.Addr()
	}
	return ""
}

// PgSQLAddr returns the PostgreSQL server's listener address, or empty
// string if PostgreSQL is not running.
func (s *Server) PgSQLAddr() string {
	if s.pgsqlSrv != nil {
		return s.pgsqlSrv.Addr()
	}
	return ""
}

// StartEtcd starts an etcd protocol server for the named database. If etcd
// support is not compiled into this binary, it refuses with the same
// service.CapabilityUnsupportedError every transport already maps to
// Unimplemented, naming the missing build tag, rather than silently
// accepting a configuration it can never serve.
func (s *Server) StartEtcd(dbName string, cfg *database.EtcdConfig) error {
	if startEtcdServerFn == nil {
		return notImplemented("etcd", "requires build tag qrdb_etcd")
	}
	_, err := startEtcdServerFn(s, dbName, cfg)
	return err
}

// StopEtcd stops the etcd protocol server for the named database if one
// is running.
func (s *Server) StopEtcd(dbName string) {
	if stopEtcdServerFn == nil {
		return
	}
	val, ok := s.etcdServers.LoadAndDelete(dbName)
	if ok {
		stopEtcdServerFn(val.(EtcdProtocolServer)) //nolint:errcheck // type assertion on a value this container only ever holds
	}
}

// EtcdAddr returns the etcd server's listener address for the given
// database, or empty string if etcd is not running for that database.
func (s *Server) EtcdAddr(dbName string) string {
	val, ok := s.etcdServers.Load(dbName)
	if !ok {
		return ""
	}
	return val.(EtcdProtocolServer).Addr() //nolint:errcheck // type assertion on a value this container only ever holds
}

// stopMySQL stops the MySQL server if it was started.
func (s *Server) stopMySQL() {
	if stopMySQLFn != nil {
		stopMySQLFn(s)
	}
}

// stopPgSQL stops the PostgreSQL server if it was started.
func (s *Server) stopPgSQL() {
	if stopPgSQLFn != nil {
		stopPgSQLFn(s)
	}
}

// stopAllEtcd stops all running etcd protocol servers.
func (s *Server) stopAllEtcd() {
	if stopEtcdServerFn == nil {
		return
	}
	s.etcdServers.Range(func(key, value any) bool {
		stopEtcdServerFn(value.(EtcdProtocolServer)) //nolint:errcheck // type assertion on a value this container only ever holds
		s.etcdServers.Delete(key)
		return true
	})
}
