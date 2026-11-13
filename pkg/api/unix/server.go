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

// Package unix provides a gRPC server over Unix domain sockets for QRDB.
// It reuses the gRPC service implementation but listens on a local Unix
// socket. The socket file mode (0700 directory) is defense-in-depth;
// the transport is "verified TLS 1.3 only" with the same mTLS-derived
// authentication and RBAC authorization as the TCP gRPC server, applied
// via the identical interceptor chain.
package unix

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// Server is a gRPC server that listens on a Unix domain socket. It
// delegates all operations to the same service implementations used
// by the TCP gRPC server, communicating over a local socket path secured
// with the same optional TLS credentials and auth middleware (see WithTLS
// and WithAuthMiddleware).
type Server struct {
	kvSvc             service.KVServicer
	adminSvc          service.AdminServicer
	socketPath        string
	userSvc           user.UserServicer
	roleSvc           role.RoleServicer
	certProv          cert.CertProvider
	certEnroll        service.CertEnrollmentServicer
	invitationSvc     service.InvitationServicer
	onboarding        service.TenantOnboardingServicer
	barrierSvc        service.BarrierServicer
	encMigrator       service.EncryptionMigrator
	pinBackend        pin.PINBackend
	ceremonySvc       service.CeremonyServicer
	ceremonyCA        ceremony.CeremonyCA
	credentialSvc     service.CredentialServicer
	dekSvc            service.DEKServicer
	policySvc         service.PolicyServicer
	collectionSvc     service.CollectionServicer
	mvccSvc           service.MVCCKVServicer
	txSvc             service.TransactionServicer
	batchSvc          service.BatchServicer
	watchSvc          service.WatchServicer
	leaseSvc          lease.LeaseServicer
	lockSvc           service.LockServicer
	indexSvc          index.IndexServicer
	tsSvc             timeseries.TimeSeriesServicer
	vectorSvc         service.VectorServicer
	logSvc            service.LogService
	embeddingSvc      service.EmbeddingServicer
	ragSvc            service.RAGServicer
	databaseSvc       service.DatabaseServicer
	tenantSvc         tenant.TenantServicer
	volumeSvc         volume.Service
	backupSvc         service.BackupServicer
	rebalanceSvc      service.RebalanceServicer
	authFactorSvc     service.AuthFactorServicer
	userAuthFactorSvc service.UserAuthFactorServicer
	tenantBarrierSvc  tenant.TenantServicer
	auditLogger       audit.AuditLogger
	replicatedAudit   *service.OptInAuditLogger

	// soGate is the SO dual-factor gate; nil leaves it off. See WithSOGate.
	soGate   *authmw.SOGate
	sqlSvc   service.SQLServicer
	registry service.Registry
	modules  []apimodule.ServiceModule
	tls      *tls.Config
	tlsErr   error
	authMW   *authmw.AuthMiddleware

	// clientLimit admits each call by client address before anything
	// else runs on its behalf (go-qrdb#1197); nil admits every call.
	clientLimit *clientlimit.Limiter
	// peerChainVerifier verifies a client certificate the TLS
	// configuration accepted without building a chain.
	peerChainVerifier auth.IssuerVerifier

	// replicaForwarding, when set, makes the socket forward what it refuses
	// as a non-replica (WithReplicaForwarding), over gRPC to the replica's
	// gRPC endpoint; forwarder is built from it at Start and closed at Stop.
	replicaForwarding *replicaforward.Config
	forwarder         *grpcpkg.ReplicaForwarder
	sealedWindowProbe sealedWindowProbe
	// sealedWindowListener is the boot-time sealed-window listener's probe,
	// and selects that listener's interceptors. Nil for a regular listener.
	sealedWindowListener sealedwindow.ListenerProbe
	// sealedWindowDispatch, on a regular socket, sends every call that
	// arrives while it reports the node barrier sealed down the boot-time
	// listener's chain. Nil installs no dispatch.
	sealedWindowDispatch sealedwindow.ListenerProbe

	// awaitingAdminCredentialProbe controls the awaiting-admin-credential
	// gate (go-qrdb#548 review item 3). See the TCP gRPC server's
	// WithAwaitingAdminCredentialProbe for its admission rule.
	awaitingAdminCredentialProbe awaitingAdminCredentialProbe
	server                       *grpc.Server
	listener                     net.Listener
	serveWg                      sync.WaitGroup

	// maxConcurrentStreams is the server's configured per-connection stream
	// cap. Zero means grpcpkg.DefaultMaxConcurrentStreams; see
	// WithMaxConcurrentStreams.
	maxConcurrentStreams uint32

	// maxMessageBytes bounds a single gRPC message this server accepts or
	// produces. Zero or negative resolves to grpcpkg.DefaultMaxMessageBytes.
	// See WithMaxMessageBytes.
	maxMessageBytes int
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithMaxConcurrentStreams overrides grpcpkg.DefaultMaxConcurrentStreams, the
// number of simultaneously active RPCs grpc-go admits on one connection.
func WithMaxConcurrentStreams(n uint32) ServerOption {
	return func(s *Server) {
		s.maxConcurrentStreams = n
	}
}

// WithReplicaForwarding makes the socket forward a unary request it refuses
// because the node holds no replica of the database to a node that does, on
// the caller's behalf, over gRPC (docs/replica-forwarding). Without it the
// socket answers the refusal. Streams are never forwarded.
func WithReplicaForwarding(cfg replicaforward.Config) ServerOption {
	return func(s *Server) {
		c := cfg
		s.replicaForwarding = &c
	}
}

// WithMaxMessageBytes bounds a single gRPC message this server accepts or
// produces. n <= 0 resolves to grpcpkg.DefaultMaxMessageBytes at Start;
// there is no way to request an unbounded message. The Unix domain socket
// carries no remote-IP concept, so unlike the TCP gRPC server there is no
// corresponding per-peer connection cap here: the socket file's mode bits
// already bound who can dial it at all.
func WithMaxMessageBytes(n int) ServerOption {
	return func(s *Server) { s.maxMessageBytes = n }
}

// WithTLS configures the Unix socket gRPC server with TLS credentials.
// The Unix transport is "verified TLS 1.3 only": the socket-file mode
// (0700 directory) is defense-in-depth, but the TLS client certificate
// is the identity the auth middleware authenticates against, exactly as
// on the TCP gRPC server.
//
// cfg is cloned — the caller's copy is never mutated or retained — and its
// MinVersion is enforced: unset defaults to tls.VersionTLS13; an explicit
// value below TLS 1.3 fails Start with a typed *WeakTLSVersionError. A nil
// cfg leaves TLS unconfigured, identical to omitting WithTLS entirely: Start
// then fails closed with a *TLSNotConfiguredError, because the Unix transport
// is TLS-only and a caller must never end up with an unauthenticated plaintext
// listener simply by forgetting to wire TLS. The orchestrator (pkg/server)
// auto-provisions a certificate from the node's CeremonyCA so this is
// transparent for every normal deployment shape.
//
// The floor is applied through clienttrust.ReassertListenerTLS rather than
// a bare clone-and-mutate: when cfg already carries a GetConfigForClient
// hook (installed by a shared *clienttrust.Pool across every client-facing
// listener, to trust every tenant's Key B CA), a bare clone's MinVersion
// change is lost on any connection that takes the hook's dynamic path --
// tls.Config.Clone() copies the hook's closure, not its effect. Measured as
// a TLS 1.2 downgrade on exactly that path (go-qrdb#560 review, B2-3).
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) {
		if cfg == nil {
			return
		}
		if cfg.MinVersion != 0 && cfg.MinVersion < tls.VersionTLS13 {
			s.tlsErr = &WeakTLSVersionError{MinVersion: cfg.MinVersion}
			return
		}
		s.tls = clienttrust.ReassertListenerTLS(cfg, func(c *tls.Config) {
			if c.MinVersion < tls.VersionTLS13 {
				c.MinVersion = tls.VersionTLS13
			}
		})
	}
}

// tlsVersionName renders a crypto/tls version constant (or 0 for "unset")
// as a human-readable string for error messages.
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	case 0:
		return "unset"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

// WithAuthMiddleware configures the Unix socket gRPC server with the same
// authentication and authorization middleware applied to the TCP gRPC
// server, so unix RPCs are gated by RBAC exactly like every other
// transport instead of bypassing it as a "local trust" shortcut.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) { s.authMW = mw }
}

// WithClientLimiter admits every call through l before any other
// interceptor -- authentication included -- runs (go-qrdb#1197). A Unix
// socket peer has no network address and the socket carries no forwarding
// headers, so every local caller of one socket shares one budget. Every
// transport of one server shares the same Limiter. Nil, the default,
// applies no limit.
func WithClientLimiter(l *clientlimit.Limiter) ServerOption {
	return func(s *Server) { s.clientLimit = l }
}

// WithPeerChainVerifier sets the verifier that resolves the caller's client
// certificate when the socket's TLS configuration accepted it without
// building a chain: the client trust pool the configuration is wired to. The
// socket binds it, with the peer's TLS state, to every call before
// admission, so a share claim binds only a verified certificate on the
// authenticated chain and in the sealed window alike. Unset, the socket uses
// its AuthMiddleware's issuer verifier.
func WithPeerChainVerifier(v auth.IssuerVerifier) ServerOption {
	return func(s *Server) { s.peerChainVerifier = v }
}

// WithUserService configures the Unix socket server with a user management service.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(s *Server) { s.userSvc = svc }
}

// WithRoleService configures the Unix socket server with a role management service.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(s *Server) { s.roleSvc = svc }
}

// WithSOGate installs the SO dual-factor gate (authmw.SOGate) on every RPC
// whose permission is in authmw.SOPermissions, after authentication: the
// caller must hold the SO role and present the SO PIN in the "x-qrdb-so-pin" metadata
// entry over TLS. It is the same gate the REST transport applies, built by the
// orchestrator when auth.so_dual_factor is on; nil leaves it off.
func WithSOGate(gate *authmw.SOGate) ServerOption {
	return func(s *Server) { s.soGate = gate }
}

// WithCertProvider configures the Unix socket server with a certificate provider.
func WithCertProvider(svc cert.CertProvider) ServerOption {
	return func(s *Server) { s.certProv = svc }
}

// WithCertEnrollment configures the Unix socket server with the certificate
// issue/enrol and trust-anchor service.
func WithCertEnrollment(svc service.CertEnrollmentServicer) ServerOption {
	return func(s *Server) { s.certEnroll = svc }
}

// WithInvitations configures the Unix socket server with the invitation
// self-enrolment service behind InviteUser and the PUBLIC RedeemInvitation.
func WithInvitations(svc service.InvitationServicer) ServerOption {
	return func(s *Server) { s.invitationSvc = svc }
}

// WithTenantOnboarding configures the Unix socket server with the tenant-create
// service.
func WithTenantOnboarding(svc service.TenantOnboardingServicer) ServerOption {
	return func(s *Server) { s.onboarding = svc }
}

// WithBarrierService configures the Unix socket server with a barrier service.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(s *Server) { s.barrierSvc = svc }
}

// WithEncryptionMigrator configures the Unix socket server with the
// enable-encryption migrator, exposing BarrierEnableEncryption over the
// Unix socket the same way it is exposed over REST and TCP gRPC.
func WithEncryptionMigrator(m service.EncryptionMigrator) ServerOption {
	return func(s *Server) { s.encMigrator = m }
}

// WithSQLService configures the Unix socket server with a SQL query
// service for ad-hoc SQL execution against a named database.
func WithSQLService(svc service.SQLServicer) ServerOption {
	return func(s *Server) { s.sqlSvc = svc }
}

// sealedWindowProbe is the abstraction the sealed-window interceptor needs
// to know whether the storage barrier is currently sealed. Mirrors
// grpcpkg's unexported interface of the same name; kept as a local type so
// this package can declare a field of the right shape without importing an
// unexported grpc-package type.
type sealedWindowProbe interface {
	IsSealed() bool
}

// awaitingAdminCredentialProbe is the abstraction the awaiting-admin-
// credential interceptor needs (go-qrdb#548 review item 3). Mirrors
// grpcpkg's unexported interface of the same name, for the same reason
// sealedWindowProbe above does.
type awaitingAdminCredentialProbe interface {
	AwaitingAdminCredential() bool
}

// WithSealedWindowProbe installs the sealed-window unary and streaming
// interceptors on the Unix socket gRPC server, using the exact same
// allowlist and semantics as the TCP gRPC server (grpcpkg.SealedWindowAllowlist).
// While probe.IsSealed() returns true, every method whose trailing
// component is not on the allowlist is rejected with codes.Unavailable.
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) {
		s.sealedWindowProbe = probe
		s.sealedWindowListener = nil
	}
}

// WithSealedWindowListener installs the boot-time sealed-window listener's
// interceptors (grpcpkg.SealedWindowListenerUnaryInterceptor and
// grpcpkg.SealedWindowListenerStreamInterceptor), exactly as the TCP gRPC
// server's WithSealedWindowListener does: the allowlist while the barrier is
// sealed, and codes.Unavailable for every method once it is unsealed. It
// replaces WithSealedWindowProbe; the last of the two applied wins.
func WithSealedWindowListener(probe sealedwindow.ListenerProbe) ServerOption {
	return func(s *Server) {
		probe = sealedwindow.OrFailClosed(probe)
		s.sealedWindowProbe = probe
		s.sealedWindowListener = probe
	}
}

// WithSealedWindowDispatch makes a regular socket behave, while probe
// reports the node barrier sealed, exactly as the boot-time sealed-window
// listener does, as the TCP gRPC server's WithSealedWindowDispatch does:
// such a call runs the chain WithSealedWindowListener builds instead of the
// socket's own.
func WithSealedWindowDispatch(probe sealedwindow.ListenerProbe) ServerOption {
	return func(s *Server) {
		s.sealedWindowDispatch = probe
	}
}

// WithAwaitingAdminCredentialProbe installs the awaiting-admin-credential
// gate (go-qrdb#548 review item 3), exactly as the TCP gRPC server's
// WithAwaitingAdminCredentialProbe does.
func WithAwaitingAdminCredentialProbe(probe awaitingAdminCredentialProbe) ServerOption {
	return func(s *Server) { s.awaitingAdminCredentialProbe = probe }
}

// WithPINBackend configures the Unix socket server with the canonical
// pin.PINBackend that verifies the SO PIN on SO-PIN-gated barrier
// operations and provisions PINs supplied at initialization. Without one
// every SO-PIN-gated operation, and every request carrying a PIN to
// provision, is refused: PIN checks fail closed.
//
// The backend is decorated with the SO PIN lockout (pin.NewLockoutBackend,
// idempotent: a backend the composer already decorated keeps its one set of
// counters), so a raw backend handed straight to this transport is still
// throttled.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	backend = pin.NewLockoutBackend(backend)
	return func(s *Server) { s.pinBackend = backend }
}

// WithCeremonyService configures the Unix socket server with a ceremony service.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(s *Server) { s.ceremonySvc = svc }
}

// WithCeremonyCA sets the CeremonyCA used by the ceremony initialization
// handler to issue the TLS and SO certificates, mirroring the TCP gRPC, QUIC,
// REST, and MCP transports. The CA cannot be serialized over the wire, so it
// must be injected at server creation time and is held as a server-side
// reference. When unset, CeremonyInitialize fails closed with a
// *grpcpkg.CeremonyCANotConfiguredError instead of dereferencing a nil CA.
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption {
	return func(s *Server) { s.ceremonyCA = ca }
}

// WithCredentialService configures the Unix socket server with a credential service.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(s *Server) { s.credentialSvc = svc }
}

// WithDEKService configures the Unix socket server with the tenant DEK
// keyring service.
func WithDEKService(svc service.DEKServicer) ServerOption {
	return func(s *Server) { s.dekSvc = svc }
}

// WithPolicyService configures the Unix socket server with the tenant ABAC
// policy store.
func WithPolicyService(svc service.PolicyServicer) ServerOption {
	return func(s *Server) { s.policySvc = svc }
}

// WithCollectionService configures the Unix socket server with a collection service.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) { s.collectionSvc = svc }
}

// WithMVCC configures the Unix socket server with an MVCC service.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(s *Server) { s.mvccSvc = svc }
}

// WithTransaction configures the Unix socket server with a transaction service.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(s *Server) { s.txSvc = svc }
}

// WithBatch configures the Unix socket server with a batch service.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(s *Server) { s.batchSvc = svc }
}

// WithWatch configures the Unix socket server with a watch service.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(s *Server) { s.watchSvc = svc }
}

// WithLease configures the Unix socket server with a lease service.
func WithLease(svc lease.LeaseServicer) ServerOption {
	return func(s *Server) { s.leaseSvc = svc }
}

// WithLock configures the Unix socket server with a distributed lock service.
//
// This is the REGISTRY-LESS fallback, not the primary path. When a
// service.Registry is configured the lock servicer is resolved per
// (tenant, database) and this singleton is never consulted: a lock key
// carries no tenant segment, so a process-wide servicer built over the raw
// engines gives every tenant the same lock (go-qrdb#455).
func WithLock(svc service.LockServicer) ServerOption {
	return func(s *Server) { s.lockSvc = svc }
}

// WithIndex configures the Unix socket server with an index service.
func WithIndex(svc index.IndexServicer) ServerOption {
	return func(s *Server) { s.indexSvc = svc }
}

// WithTimeSeries configures the Unix socket server with a time-series service.
func WithTimeSeries(svc timeseries.TimeSeriesServicer) ServerOption {
	return func(s *Server) { s.tsSvc = svc }
}

// WithVector configures the Unix socket server with a vector service.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(s *Server) { s.vectorSvc = svc }
}

// WithLogService sets the verifiable-log service served over the Unix socket.
func WithLogService(svc service.LogService) ServerOption {
	return func(s *Server) { s.logSvc = svc }
}

// WithEmbedding configures the Unix socket server with a text embedding service.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(s *Server) { s.embeddingSvc = svc }
}

// WithRAGService configures the Unix socket server with a RAG service.
func WithRAGService(svc service.RAGServicer) ServerOption {
	return func(s *Server) { s.ragSvc = svc }
}

// WithDatabaseService configures the Unix socket server with a database service.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) { s.databaseSvc = svc }
}

// WithTenantService configures the Unix socket server with a tenant management service.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantSvc = svc }
}

// WithVolumeService configures the Unix socket server with the volume pool
// management surface (docs/volume-pools/api.md).
func WithVolumeService(svc volume.Service) ServerOption {
	return func(s *Server) { s.volumeSvc = svc }
}

// WithBackupService configures the Unix socket server with a backup management service.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(s *Server) { s.backupSvc = svc }
}

// WithRebalanceService configures the Unix socket server with a cluster rebalance service.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(s *Server) { s.rebalanceSvc = svc }
}

// WithAuthFactorService configures the Unix socket server with an auth factor
// service for custodian enrollment, challenge, and verification.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(s *Server) { s.authFactorSvc = svc }
}

// WithUserAuthFactorService configures the Unix socket server with the
// user-side N-factor service (M14) backing the SDK client's
// UserAuthFactors() surface (Add / List / Get / Remove / Rotate / Relabel /
// Login). Distinct from WithAuthFactorService (the ceremony-time enrollment
// surface). The service is not registered on the gRPC server when nil.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) ServerOption {
	return func(s *Server) { s.userAuthFactorSvc = svc }
}

// WithTenantBarrierService configures the Unix socket server with a tenant
// servicer used to expose the per-tenant barrier RPCs (Init, Unseal, Seal,
// Status). When set, the TenantBarrierService is registered on the gRPC
// server alongside the main QRDBService — same gating pattern as
// WithAuthFactorService and WithAuditLogger.
func WithTenantBarrierService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantBarrierSvc = svc }
}

// WithServiceRegistry configures the Unix socket server with a per-(tenant,
// database) service registry. When set, data-plane RPCs resolve their servicer
// per scope using the x-qrdb-database metadata value (default system); when nil
// the singleton service fields are used.
func WithServiceRegistry(reg service.Registry) ServerOption {
	return func(s *Server) { s.registry = reg }
}

// WithServiceModules configures the Unix socket server with the capability
// modules (pkg/apimodule.ServiceModule) registered on the shared server via
// server.WithModule. The Unix transport serves the same gRPC wire as the TCP
// gRPC server over a UDS, so each module's RegisterGRPC hook is invoked
// against this server's own *grpc.Server in Start, exactly as it is against
// the TCP gRPC server's, giving every module gRPC parity on both transports.
func WithServiceModules(mods []apimodule.ServiceModule) ServerOption {
	return func(s *Server) { s.modules = mods }
}

// WithKVService configures the Unix socket server with a KV service. This is
// an alternative to passing kvSvc as a positional parameter and is useful when
// constructing a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(s *Server) { s.kvSvc = svc }
}

// WithAdminService configures the Unix socket server with an admin service.
// This is an alternative to passing adminSvc as a positional parameter and is
// useful when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(s *Server) { s.adminSvc = svc }
}

// NewServer creates a new Unix domain socket gRPC server that listens on the
// given socket path and delegates to the provided service implementations.
// kvSvc and adminSvc may be nil when only non-KV services (barrier, cert, auth)
// are needed; their RPC handlers return codes.Unimplemented when nil.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, socketPath string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:      kvSvc,
		adminSvc:   adminSvc,
		socketPath: socketPath,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start creates the parent directory for the socket path, removes any
// stale socket file, binds a Unix domain socket listener, registers the
// QRDB gRPC service, and starts serving in the background. The method
// returns after the listener is bound.
func (s *Server) Start() error {
	// A WithTLS config with an explicitly weak MinVersion was rejected at
	// option-application time; surface it now.
	if s.tlsErr != nil {
		return s.tlsErr
	}
	// The Unix transport is TLS-only: the mTLS client certificate is the
	// identity the auth middleware authenticates against, so an absent TLS
	// config would silently strip RBAC and identity checks from every caller
	// that can reach the socket. Fail closed rather than bind a plaintext
	// listener. The orchestrator (pkg/server) auto-provisions a certificate
	// from the node's CeremonyCA, so a normal deployment never reaches here.
	if s.tls == nil {
		return &TLSNotConfiguredError{}
	}

	// Ensure the parent directory exists.
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &MkdirError{Dir: dir, Err: err}
	}

	// Remove stale socket file from a previous run.
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return &StartError{SocketPath: s.socketPath, Err: err}
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return &StartError{SocketPath: s.socketPath, Err: err}
	}
	s.listener = ln

	// Feed module-declared public gRPC methods into the auth middleware
	// before building the interceptor chain below: a module implementing
	// apimodule.GRPCPublicMethodLister declares RPCs that must be reachable
	// without authentication, symmetric with HTTPRegistrar.HandlePublic on
	// REST. s.authMW is the SAME *authmw.AuthMiddleware instance the TCP
	// gRPC server is configured with (see pkg/server wiring), so in the
	// common case where both transports are enabled this registration is
	// idempotent with the TCP gRPC server's own; registering here too keeps
	// the Unix transport correct standalone (Unix enabled without TCP gRPC).
	if s.authMW != nil {
		for _, m := range s.modules {
			lister, ok := m.(apimodule.GRPCPublicMethodLister)
			if !ok {
				continue
			}
			if err := s.authMW.RegisterPublicGRPCMethods(lister.PublicGRPCMethods()); err != nil {
				return err
			}
		}
		// The built-in no-login RPCs are publicverbs.Table's: see
		// pkg/api/grpc/server.go Start's identical registration. s.authMW may
		// be the SAME instance the TCP gRPC server registered them on, but
		// this keeps the Unix transport correct standalone.
		if err := s.authMW.RegisterPublicGRPCMethods(publicverbs.GRPCFullMethods(pb.QRDBService_ServiceDesc.ServiceName)); err != nil {
			return err
		}
	}

	if s.replicaForwarding != nil {
		s.forwarder = grpcpkg.NewReplicaForwarder(*s.replicaForwarding, s.tls)
	}
	unaryChain, streamChain := s.admissionChains()
	maxStreams := s.maxConcurrentStreams
	if maxStreams == 0 {
		maxStreams = grpcpkg.DefaultMaxConcurrentStreams
	}
	maxMsgBytes := grpcpkg.ResolveMaxMessageBytes(s.maxMessageBytes)
	serverOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(unaryChain),
		grpc.StreamInterceptor(streamChain),
		// MaxRecvMsgSize only -- see pkg/api/grpc.Server's identical option
		// list for why a response this server chooses to send is not
		// capped at the same pre-auth receive-side bound.
		grpc.MaxRecvMsgSize(maxMsgBytes),
		grpc.MaxConcurrentStreams(maxStreams),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             grpcpkg.DefaultKeepaliveMinTime,
			PermitWithoutStream: false,
		}),
	}

	// TLS credentials, unconditionally: the Unix transport is TLS-only, and
	// Start returned TLSNotConfiguredError above when s.tls is nil, so this
	// server never serves plaintext and there is no plaintext connection for
	// an observer to miss. Connections are reported to observing modules
	// through the credentials, never by wrapping the listener, so grpc-go
	// receives the accepted socket unchanged.
	creds := apimodule.ConnObserversOf(s.modules).Credentials(credentials.NewTLS(s.tls), apimodule.TransportUnix)
	serverOpts = append(serverOpts, grpc.Creds(creds))

	s.server = grpc.NewServer(serverOpts...)

	// Build register options for optional services.
	var regOpts []grpcpkg.RegisterOption
	if s.userSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterUserService(s.userSvc))
	}
	// WithRegisterUserAuthFactorService wires the SAME instance the dedicated
	// UserAuthFactorService RPCs below are registered with into the shared
	// qrdbServiceServer's GetUser/ListUsers/UpdateUser projection, so Factors
	// metadata and the derived MFA state are computed from the real
	// enrollment records on this transport too, not silently empty.
	if s.userAuthFactorSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterUserAuthFactorService(s.userAuthFactorSvc))
	}
	if s.roleSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterRoleService(s.roleSvc))
	}
	if s.certProv != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCertProvider(s.certProv))
	}
	if s.certEnroll != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCertEnrollment(s.certEnroll))
	}
	if s.invitationSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterInvitationService(s.invitationSvc))
	}
	if s.onboarding != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterTenantOnboarding(s.onboarding))
	}
	if s.barrierSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterBarrierService(s.barrierSvc))
	}
	if s.encMigrator != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterEncryptionMigrator(s.encMigrator))
	}
	if s.pinBackend != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterPINBackend(s.pinBackend))
	}
	if s.ceremonySvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCeremonyService(s.ceremonySvc))
	}
	// The SAME forwarder the interceptor chain above installs for the
	// generic per-database NotAReplicaError path: a handler refused with a
	// leader-only error (CeremonyInitialize's admin-credential step) uses it
	// to forward to the system shard's leader directly, exactly as the TCP
	// gRPC server wires its own forwarder into qrdbServiceServer.
	if s.forwarder != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterReplicaForwarding(s.forwarder))
	}
	if s.ceremonyCA != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCeremonyCA(s.ceremonyCA))
	}
	if s.credentialSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCredentialService(s.credentialSvc))
	}
	if s.dekSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterDEKService(s.dekSvc))
	}
	if s.policySvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterPolicyService(s.policySvc))
	}
	if s.collectionSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterCollectionService(s.collectionSvc))
	}
	if s.mvccSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterMVCC(s.mvccSvc))
	}
	if s.txSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterTransaction(s.txSvc))
	}
	if s.batchSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterBatch(s.batchSvc))
	}
	if s.watchSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterWatch(s.watchSvc))
	}
	if s.leaseSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterLease(s.leaseSvc))
	}
	if s.lockSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterLockService(s.lockSvc))
	}
	if s.indexSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterIndex(s.indexSvc))
	}
	if s.tsSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterTimeSeries(s.tsSvc))
	}
	if s.vectorSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterVector(s.vectorSvc))
	}
	if s.logSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterLogService(s.logSvc))
	}
	if s.embeddingSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterEmbedding(s.embeddingSvc))
	}
	if s.ragSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterRAGService(s.ragSvc))
	}
	if s.databaseSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterDatabaseService(s.databaseSvc))
	}
	if s.tenantSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterTenantService(s.tenantSvc))
	}
	if s.volumeSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterVolumeService(s.volumeSvc))
	}
	if s.backupSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterBackupService(s.backupSvc))
	}
	if s.rebalanceSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterRebalanceService(s.rebalanceSvc))
	}
	if s.sqlSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterSQLService(s.sqlSvc))
	}
	if s.registry != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterServiceRegistry(s.registry))
	}
	// The SAME middleware instance the interceptors above use. The handler-
	// level certs gates need it: the interceptor authorizes against the
	// caller's tenant, and the certs RPCs name their target in the request.
	// Omitting it here would leave the Unix transport reaching the same
	// process-wide CertProvider with the gate switched off.
	if s.authMW != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterAuthMiddleware(s.authMW))
	}
	// RegisterService registers TenantBarrierService alongside QRDBService
	// when configured. The same TenantServicer can be passed to both
	// WithTenantService (for CRUD) and WithTenantBarrierService (for the
	// per-tenant barrier lifecycle); they expose disjoint gRPC methods on
	// disjoint service descriptors. s.authMW (which may be nil) supplies the
	// tenant-scoped authorization gate, exactly as on the TCP gRPC server.
	if s.tenantBarrierSvc != nil {
		regOpts = append(regOpts, grpcpkg.WithRegisterTenantBarrierService(s.tenantBarrierSvc))
	}

	// Reuse the gRPC package's service registration.
	grpcpkg.RegisterService(s.server, s.kvSvc, s.adminSvc, regOpts...)

	// Register AuditService alongside QRDBService when an audit logger is set.
	// Authorization: requires "audit:query" permission.
	// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
	if s.auditLogger != nil || s.replicatedAudit != nil {
		grpcpkg.RegisterAuditService(s.server, s.auditLogger, s.replicatedAudit)
	}

	// Register AuthFactorService alongside QRDBService when configured.
	if s.authFactorSvc != nil {
		grpcpkg.RegisterAuthFactorService(s.server, s.authFactorSvc)
	}

	// UserAuthFactorService (M14) is registered by RegisterService above, via
	// the WithRegisterUserAuthFactorService regOpt: that same call also wires
	// s.userAuthFactorSvc into the shared qrdbServiceServer's GetUser/
	// ListUsers/UpdateUser projection, so a second, direct
	// RegisterUserAuthFactorService call here would register the
	// qrdb.v1.UserAuthFactorService proto service on s.server twice.

	// Register capability modules' gRPC service(s) alongside QRDBService. The
	// Unix transport serves the same gRPC wire over a UDS, so modules gain
	// parity here identically to the TCP gRPC server; s.server (*grpc.Server)
	// implements grpc.ServiceRegistrar directly.
	for _, m := range s.modules {
		m.RegisterGRPC(s.server)
	}

	proto := "unix"
	if s.tls != nil {
		proto = "unix+TLS"
	}
	slog.Info("unix gRPC server started", "socket", s.socketPath, "proto", proto)

	s.serveWg.Add(1)
	go func() {
		defer s.serveWg.Done()
		if serveErr := s.server.Serve(ln); serveErr != nil {
			slog.Error("unix gRPC server error", "error", serveErr)
		}
	}()

	return nil
}

// admissionPath is one path a call takes through the socket's interceptors,
// as on TCP gRPC: the authentication, SO gate, sealed-window gate and replica
// forwarder it passes. A nil field installs nothing; listener, when set,
// selects the boot-time sealed-window listener's gate over probe's.
// admissionChains composes the server's unary and stream interceptors.
func (s *Server) admissionChains() (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unaryChain, streamChain := s.interceptorChains(admissionPath{
		authMW:    s.authMW,
		soGate:    s.soGate,
		probe:     s.sealedWindowProbe,
		listener:  s.sealedWindowListener,
		forwarder: s.forwarder,
	})
	// The awaiting-admin-credential gate (go-qrdb#548 review item 3) is
	// composed exactly as the TCP gRPC server composes it: wrapping the
	// chain's own combined form, not appended as a chain member, since it
	// must be able to skip the whole chain -- auth included -- for the two
	// methods it allows.
	if s.awaitingAdminCredentialProbe != nil {
		unaryChain = grpcpkg.AwaitingAdminCredentialUnaryInterceptor(s.awaitingAdminCredentialProbe, grpcpkg.AwaitingAdminCredentialAllowlist, unaryChain)
		streamChain = grpcpkg.AwaitingAdminCredentialStreamInterceptor(s.awaitingAdminCredentialProbe, grpcpkg.AwaitingAdminCredentialAllowlist, streamChain)
	}
	// A call that arrives while the node barrier is sealed takes the
	// boot-time sealed-window listener's chain instead, exactly as on TCP
	// gRPC (see grpcpkg.WithSealedWindowDispatch).
	if s.sealedWindowDispatch != nil {
		sealedUnary, sealedStream := s.interceptorChains(admissionPath{listener: s.sealedWindowDispatch})
		unaryChain = grpcpkg.SealedWindowDispatchUnaryInterceptor(s.sealedWindowDispatch, sealedUnary, unaryChain)
		streamChain = grpcpkg.SealedWindowDispatchStreamInterceptor(s.sealedWindowDispatch, sealedStream, streamChain)
	}
	// Outermost, independent of authentication: every path above -- the
	// authenticated chain, the awaiting-admin gate and the sealed window --
	// sees the peer's TLS state and this socket's chain verifier.
	// Ahead of even that, the client-address limiter (go-qrdb#1197), exactly
	// as on TCP gRPC.
	v := s.chainVerifier()
	return grpcpkg.ChainUnaryServerInterceptors(grpcpkg.ClientLimitUnaryInterceptor(s.clientLimit), authmw.PeerTLSUnaryInterceptor(v), unaryChain),
		grpcpkg.ChainStreamServerInterceptors(grpcpkg.ClientLimitStreamInterceptor(s.clientLimit), authmw.PeerTLSStreamInterceptor(v), streamChain)
}

// chainVerifier is the verifier the socket binds to every call:
// WithPeerChainVerifier's, else the AuthMiddleware's issuer verifier.
func (s *Server) chainVerifier() auth.IssuerVerifier {
	if s.peerChainVerifier != nil {
		return s.peerChainVerifier
	}
	return s.authMW.IssuerVerifier()
}

type admissionPath struct {
	authMW    *authmw.AuthMiddleware
	soGate    *authmw.SOGate
	probe     sealedWindowProbe
	listener  sealedwindow.ListenerProbe
	forwarder *grpcpkg.ReplicaForwarder
}

// interceptorChains builds the combined unary and stream interceptors of p
// with the SAME ordering as the TCP gRPC server (pkg/api/grpc/server.go
// interceptorChains): recovery, then logging, then the database-scope
// interceptor, then -- when configured -- the auth middleware, the SO gate,
// the sealed-window gate and the replica forwarder. Unix previously omitted
// the auth middleware entirely, letting RBAC-gated RPCs bypass authorization
// on this transport; it is now applied identically to TCP gRPC.
func (s *Server) interceptorChains(p admissionPath) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		// Unconditional (go-qrdb#560 review C): see pkg/api/grpc/server.go's
		// identical entry -- a node with no AuthMiddleware still runs the SO
		// PIN gate on ceremony-initialize's admin-credential retry.
		authmw.RemoteAddrUnaryInterceptor,
		recoveryInterceptor,
		loggingInterceptor,
		grpcpkg.DatabaseScopeUnaryInterceptor,
	}
	if p.authMW != nil {
		unaryInterceptors = append(unaryInterceptors, grpcpkg.PublicRateLimitUnaryInterceptor(p.authMW), p.authMW.UnaryInterceptor())
	}
	// The SO gate installs whenever there is one, auth middleware or not:
	// with nothing to authenticate the caller there is no SO to admit, so it
	// fails closed rather than being skipped.
	if p.soGate != nil {
		unaryInterceptors = append(unaryInterceptors, authmw.SOGateUnaryInterceptor(p.soGate))
	}
	var sealedUnary grpc.UnaryServerInterceptor
	var sealedStream grpc.StreamServerInterceptor
	switch {
	case p.listener != nil:
		sealedUnary = grpcpkg.SealedWindowListenerUnaryInterceptor(p.listener, nil)
		sealedStream = grpcpkg.SealedWindowListenerStreamInterceptor(p.listener, nil)
	case p.probe != nil:
		sealedUnary = grpcpkg.SealedWindowUnaryInterceptor(p.probe, nil)
		sealedStream = grpcpkg.SealedWindowStreamInterceptor(p.probe, nil)
	}
	if sealedUnary != nil {
		unaryInterceptors = append(unaryInterceptors, sealedUnary)
	}
	// Replica forwarding sits innermost, as on TCP gRPC: the caller is
	// authenticated and admitted here before a refusal is forwarded.
	if p.forwarder != nil {
		unaryInterceptors = append(unaryInterceptors, p.forwarder.UnaryInterceptor())
	}
	streamInterceptors := []grpc.StreamServerInterceptor{
		streamRecoveryInterceptor,
		streamLoggingInterceptor,
		grpcpkg.DatabaseScopeStreamInterceptor,
	}
	if p.authMW != nil {
		streamInterceptors = append(streamInterceptors, p.authMW.StreamInterceptor())
	}
	if p.soGate != nil {
		streamInterceptors = append(streamInterceptors, authmw.SOGateStreamInterceptor(p.soGate))
	}
	if sealedStream != nil {
		streamInterceptors = append(streamInterceptors, sealedStream)
	}
	return grpcpkg.ChainUnaryServerInterceptors(unaryInterceptors...), grpcpkg.ChainStreamServerInterceptors(streamInterceptors...)
}

// Stop gracefully shuts down the gRPC server, allowing in-flight RPCs
// to complete, and removes the socket file.
func (s *Server) Stop() {
	if s.server == nil {
		return
	}
	s.server.GracefulStop()
	if s.forwarder != nil {
		s.forwarder.Close()
	}

	// Wait for the serve goroutine to exit before removing the socket
	// to prevent a race where a subsequent Start recreates the socket
	// file and the exiting goroutine removes it.
	s.serveWg.Wait()

	// Clean up the socket file.
	_ = os.Remove(s.socketPath)

	slog.Info("unix gRPC server stopped", "socket", s.socketPath)
}

// Addr returns the socket path. This satisfies the same convention as
// the TCP gRPC server's Addr method.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.socketPath
}

// recoveryInterceptor catches panics in gRPC handlers and returns an
// Internal error instead of crashing the server.
func recoveryInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("unix grpc: panic recovered",
				"method", info.FullMethod,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			err = status.Errorf(codes.Internal, "internal server error")
		}
	}()
	return handler(ctx, req)
}

// loggingInterceptor logs each gRPC call with method, duration, and any
// error code.
func loggingInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	duration := time.Since(start)

	code := codes.OK
	if err != nil {
		if st, ok := status.FromError(err); ok {
			code = st.Code()
		}
	}

	level := slog.LevelDebug
	if code != codes.OK {
		level = slog.LevelWarn
	}

	slog.Log(ctx, level, "unix grpc: request",
		"method", info.FullMethod,
		"code", code.String(),
		"duration", duration,
	)

	return resp, err
}

// streamRecoveryInterceptor catches panics in streaming gRPC handlers
// and returns an Internal error instead of crashing the server. Mirrors
// pkg/api/grpc's streamRecoveryInterceptor.
func streamRecoveryInterceptor(
	srv any,
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("unix grpc: stream panic recovered",
				"method", info.FullMethod,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			err = status.Errorf(codes.Internal, "internal server error")
		}
	}()
	return handler(srv, ss)
}

// streamLoggingInterceptor logs each streaming gRPC call with method,
// duration, and any error code. Mirrors pkg/api/grpc's
// streamLoggingInterceptor.
func streamLoggingInterceptor(
	srv any,
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	start := time.Now()
	err := handler(srv, ss)
	duration := time.Since(start)

	code := codes.OK
	if err != nil {
		if st, ok := status.FromError(err); ok {
			code = st.Code()
		}
	}

	level := slog.LevelDebug
	if code != codes.OK {
		level = slog.LevelWarn
	}

	slog.Log(ss.Context(), level, "unix grpc: stream",
		"method", info.FullMethod,
		"code", code.String(),
		"duration", duration,
	)

	return err
}
