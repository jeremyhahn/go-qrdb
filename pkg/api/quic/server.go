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

// Package quic provides a native binary QUIC transport server for QRDB.
// It uses raw QUIC streams with a binary wire protocol for maximum
// throughput, bypassing HTTP and gRPC framing overhead. All service
// operations are dispatched via a map-based handler registry.
package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/connlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
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
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// methodHandler is the function signature for QUIC method dispatch.
// It receives the request context and raw protobuf payload, returning
// the serialized response payload or an error.
type methodHandler func(ctx context.Context, payload []byte) ([]byte, error)

// Server is the native QUIC transport server for QRDB. It listens for
// QUIC connections, accepts bidirectional streams, and dispatches
// requests to the service layer using a binary wire protocol.
type Server struct {
	kvSvc             service.KVServicer
	adminSvc          service.AdminServicer
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
	userSvc           user.UserServicer
	roleSvc           role.RoleServicer
	certProv          cert.CertProvider
	certEnroll        service.CertEnrollmentServicer
	invitations       service.InvitationServicer
	onboarding        service.TenantOnboardingServicer
	collectionSvc     service.CollectionServicer
	databaseSvc       service.DatabaseServicer
	barrierSvc        service.BarrierServicer
	encMigrator       service.EncryptionMigrator
	pinBackend        pin.PINBackend
	ceremonySvc       service.CeremonyServicer
	ceremonyCA        ceremony.CeremonyCA
	tenantSvc         tenant.TenantServicer
	volumeSvc         volume.Service
	backupSvc         service.BackupServicer
	rebalanceSvc      service.RebalanceServicer
	embeddingSvc      service.EmbeddingServicer
	ragSvc            service.RAGServicer
	credentialSvc     service.CredentialServicer
	dekSvc            service.DEKServicer
	policySvc         service.PolicyServicer
	authFactorSvc     service.AuthFactorServicer
	userAuthFactorSvc service.UserAuthFactorServicer
	sqlSvc            service.SQLServicer
	authMW            *authmw.AuthMiddleware
	auditLogger       audit.AuditLogger
	replicatedAudit   *service.OptInAuditLogger

	// soGate is the SO dual-factor gate; nil leaves it off. See WithSOGate.
	soGate            *authmw.SOGate
	sealedWindowProbe sealedWindowProbe
	// sealedWindowListener is the boot-time sealed-window listener's probe,
	// and selects that listener's gate. Nil for a regular listener.
	sealedWindowListener sealedwindow.ListenerProbe
	// sealedWindowDispatch, on a regular listener, sends every request that
	// arrives while it reports the node barrier sealed down the boot-time
	// listener's path. Nil installs no dispatch.
	sealedWindowDispatch sealedwindow.ListenerProbe

	// peerChainVerifier verifies a client certificate this listener's TLS
	// configuration accepted without building a chain (the client trust
	// pool's widened handshake). Nil falls back to authMW's.
	peerChainVerifier auth.IssuerVerifier

	// awaitingAdminCredentialProbe controls the awaiting-admin-credential
	// gate (go-qrdb#548 review item 3). See WithAwaitingAdminCredentialProbe.
	awaitingAdminCredentialProbe awaitingAdminCredentialProbe

	// registry, when set, resolves data-plane services per (tenant, database)
	// scope. The tenant comes from the authenticated identity; the database
	// from the request frame's database field. When nil, the singleton service
	// fields are used, preserving single-database and embedded deployments.
	registry service.Registry

	// modules holds capability modules whose gRPC service(s) are projected onto
	// the QUIC frame dispatch table by buildHandlers via each module's
	// RegisterQUIC hook. Set via WithServiceModules by the server orchestrator.
	modules []apimodule.ServiceModule

	// connObservers are the modules observing the connection lifecycle,
	// collected from modules at Start. handleConnection reports each accepted
	// connection to them for as long as it runs.
	connObservers apimodule.ConnObservers

	addr      string
	tlsConfig *tls.Config
	// listener is connlimit.QUICAcceptor rather than *quicgo.Listener so
	// that Start can wrap the raw listener in a connlimit.QUICListener
	// when maxConnsPerPeer is enabled (go-qrdb#576); every call site below
	// only needs Accept/Addr/Close, the same subset that interface
	// declares.
	listener connlimit.QUICAcceptor
	handlers map[string]methodHandler

	// udpConn and transport are the socket and the quic-go Transport the
	// listener runs on. Start creates them itself rather than through
	// quicgo.ListenAddr so that Stop can release the port deterministically:
	// a ListenAddr socket stays bound until quic-go retires the IDs of every
	// connection Stop closed (about three probe timeouts), and the server that
	// replaces this one on the same address -- a system-scope encryption
	// migration's recycle -- failed to bind in that window. udpConn is the
	// pre-bound socket when WithPacketConn handed one over.
	udpConn   net.PacketConn
	transport *quicgo.Transport

	// preboundPacketConn is the socket the embedder bound and handed over
	// (WithPacketConn). When set, Start serves on it instead of binding addr.
	preboundPacketConn net.PacketConn

	// methodPerms is the single source of truth for per-method authorization.
	// buildHandlers populates it through the s.handle/s.handleIf registration
	// helpers, so every dispatched method declares its permission at the
	// registration site; AuthorizeQUIC consults it with DEFAULT-DENY
	// semantics, and the startup assertion walks the dispatch map against it.
	methodPerms *authmw.QUICMethodRegistry

	// replicaForwarding, when set, makes the listener forward what it
	// refuses as a non-replica (WithReplicaForwarding); forwarder is built
	// from it at Start, over the listener's TLS identity, and closed at Stop.
	replicaForwarding *replicaforward.Config
	forwarder         *FrameForwarder

	closed atomic.Bool
	wg     sync.WaitGroup

	// streamWG tracks in-flight handleStream calls ONLY -- not the long-lived
	// acceptLoop/handleConnection goroutines wg also carries. Stop waits on it,
	// bounded by quicStopGracePeriod, BEFORE actively closing any connection
	// (see Stop's doc comment): a handler that has not yet returned still owns
	// its connection, and closing out from under it is what let a fast
	// system-scope encryption migration's Quiesce phase -- which recycles this
	// very server from within the migration goroutine the enable-encryption
	// handler is waiting on -- sever the connection before the handler could
	// write its "migration_started" response, which the caller then saw as
	// "server shutting down" instead of its answer (go-qrdb#550). Waiting first
	// is the same discipline grpc.Server.GracefulStop and http.Server.Shutdown
	// give their own in-flight requests; it does not need to be unbounded to
	// get that property, because every handler in this package answers within
	// its own bounded accept window (750ms) plus the time to write the reply.
	streamWG sync.WaitGroup

	// acceptClosingMu guards acceptClosing. handleConnection checks
	// acceptClosing and, if false, calls streamWG.Add(1) -- ALL under this one
	// mutex -- before spawning handleStream; Stop takes the same mutex to set
	// acceptClosing to true before it ever calls streamWG.Wait. This is not
	// optional bookkeeping: sync.WaitGroup requires that every Add happen
	// before the Wait call it is meant to be counted by, and an atomic flag
	// checked separately from the Add would leave a window where a goroutine
	// reads "not yet closing", Stop observes the counter at zero and returns
	// from Wait, and only THEN does the goroutine call Add -- a stream Stop
	// never waited for, whose connection the very next line force-closes out
	// from under it, and a WaitGroup Add-after-Wait besides. The mutex removes
	// that window: Stop cannot proceed past setting acceptClosing until any
	// handleConnection currently inside the check-then-Add section has left
	// it, and every handleConnection call afterward reads acceptClosing as
	// true and refuses the stream instead of admitting it uncounted.
	acceptClosingMu sync.Mutex
	acceptClosing   bool

	// stopping is closed when Stop begins (once, via stopOnce), and ends every
	// watch stream then in progress. A watch is the one handler that does not
	// finish on its own -- it runs until its client goes away -- so without
	// this each open watch held Stop for the whole quicStopGracePeriod before
	// the connection-close pass ended it anyway. Created by Start; nil on a
	// server that was never started, where it never fires.
	stopping chan struct{}
	stopOnce sync.Once

	// testHookAccepted and testHookConnsClosed are nil outside tests. They
	// let a test hold acceptLoop between Accept and storing the connection
	// until Stop's close pass over s.conns has finished -- the interleaving
	// that left a connection nobody closed.
	testHookAccepted    func()
	testHookConnsClosed func()

	// conns tracks every live *quicgo.Conn (key) accepted by acceptLoop,
	// mapped to struct{}{}, so Stop can actively close them instead of
	// merely stopping new accepts. QUIC rides UDP: closing the listener
	// only stops future Accept calls, it does NOT touch connections already
	// established, and a client that exits its process without sending an
	// explicit CONNECTION_CLOSE frame (the qrdb CLI does this today -- each
	// invocation dials, runs one RPC, and exits without closing the SDK
	// transport) leaves handleConnection blocked in AcceptStream until the
	// server's own MaxIdleTimeout (30s) elapses. Without this, Stop's
	// s.wg.Wait() -- and therefore the whole sealed-window-to-full-boot
	// listener swap in pkg/server.ServeSealedWindow -- would stall for up
	// to 30 seconds behind a single idle connection.
	conns sync.Map

	// maxPayloadBytes bounds a single request frame's payload this server
	// will allocate a buffer for, checked against the frame's declared
	// length BEFORE that buffer is allocated. Zero or negative resolves to
	// DefaultMaxPayloadBytes. See WithMaxPayloadBytes.
	maxPayloadBytes int

	// maxStreamsPerConn bounds the number of concurrent streams a single
	// QUIC connection may have open, passed to quic-go as
	// quic.Config.MaxIncomingStreams. Zero or negative resolves to
	// DefaultMaxStreamsPerConn. See WithMaxStreamsPerConn.
	maxStreamsPerConn int

	// maxConnsPerPeer bounds the number of concurrent QUIC connections a
	// single remote IP may hold open against this listener (go-qrdb#576).
	// Zero means unlimited. See WithMaxConnsPerPeer.
	maxConnsPerPeer int

	// clientLimit admits each request by client address before anything
	// else runs on its behalf (go-qrdb#1197); nil admits every request.
	clientLimit *clientlimit.Limiter
}

// DefaultMaxPayloadBytes is the per-request-frame payload cap applied when
// no WithMaxPayloadBytes option is given. It is the same pre-auth cap
// every other transport applies by default
// (config.DefaultTransportMaxMessageBytes): wire.ReadRequestLimited checks
// a frame's declared payload length against it before allocating a buffer
// for that payload, so it bounds what an unauthenticated TLS peer can force
// this server to allocate per stream, and with up to maxStreamsPerConn
// concurrent streams, per connection (go-qrdb#576). A caller with a larger
// payload (bulk import) uses the chunked import session API instead of a
// single oversized message.
const DefaultMaxPayloadBytes = 4 << 20

// DefaultMaxStreamsPerConn is the per-connection concurrent QUIC stream cap
// applied when no WithMaxStreamsPerConn option is given.
const DefaultMaxStreamsPerConn = 128

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithPacketConn makes Start serve on pc instead of binding the server's
// address. The caller binds it and hands it over, so the port is never
// released between reservation and serving -- the sealed-window handover
// relies on this to hold the QUIC port from the first boot-time listener to
// the regular server (go-qrdb#693). The server owns pc from Start on and
// closes it in Stop, or in Start when it refuses to serve.
func WithPacketConn(pc net.PacketConn) ServerOption {
	return func(s *Server) { s.preboundPacketConn = pc }
}

// WithMaxPayloadBytes bounds a single request frame's payload this server
// will allocate a buffer for. n <= 0 resolves to DefaultMaxPayloadBytes at
// Start; there is no way to request an unbounded payload.
func WithMaxPayloadBytes(n int) ServerOption {
	return func(s *Server) { s.maxPayloadBytes = n }
}

// WithMaxStreamsPerConn bounds the number of concurrent streams a single
// QUIC connection may have open. n <= 0 resolves to
// DefaultMaxStreamsPerConn at Start.
func WithMaxStreamsPerConn(n int) ServerOption {
	return func(s *Server) { s.maxStreamsPerConn = n }
}

// WithMaxConnsPerPeer bounds the number of concurrent QUIC connections a
// single remote IP may hold open against this listener. n <= 0 means
// unlimited. The caller resolves the configured default (or disables the
// cap) before calling this, the same convention as REST, gRPC, and MCP's
// WithMaxConnsPerPeer.
func WithMaxConnsPerPeer(n int) ServerOption {
	return func(s *Server) { s.maxConnsPerPeer = n }
}

// WithClientLimiter admits every request stream by client address through l
// before anything else -- authentication included -- runs on its behalf
// (go-qrdb#1197). Native QUIC carries no forwarding headers, so the key is
// always the connection's remote address. Every transport of one server
// shares the same Limiter. Nil, the default, applies no limit.
func WithClientLimiter(l *clientlimit.Limiter) ServerOption {
	return func(s *Server) { s.clientLimit = l }
}

// resolveMaxPayloadBytes returns n if positive, else DefaultMaxPayloadBytes.
func resolveMaxPayloadBytes(n int) int {
	if n <= 0 {
		return DefaultMaxPayloadBytes
	}
	return n
}

// resolveMaxStreamsPerConn returns n if positive, else
// DefaultMaxStreamsPerConn.
func resolveMaxStreamsPerConn(n int) int {
	if n <= 0 {
		return DefaultMaxStreamsPerConn
	}
	return n
}

// WithTLS configures the QUIC server with TLS credentials.
// TLS is mandatory for QUIC; this option allows overriding the default.
func WithTLS(cfg *tls.Config) ServerOption {
	return func(s *Server) { s.tlsConfig = cfg }
}

// WithMVCC configures the QUIC server with an MVCC key-value service.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(s *Server) { s.mvccSvc = svc }
}

// WithTransaction configures the QUIC server with a transaction service.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(s *Server) { s.txSvc = svc }
}

// WithBatch configures the QUIC server with a batch service.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(s *Server) { s.batchSvc = svc }
}

// WithWatch configures the QUIC server with a watch service.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(s *Server) { s.watchSvc = svc }
}

// WithLease configures the QUIC server with a lease service.
func WithLease(svc lease.LeaseServicer) ServerOption {
	return func(s *Server) { s.leaseSvc = svc }
}

// WithLock configures the QUIC server with a distributed lock service.
//
// This is the REGISTRY-LESS fallback, not the primary path. When a
// service.Registry is configured the lock servicer is resolved per
// (tenant, database) and this singleton is never consulted: a lock key
// carries no tenant segment, so a process-wide servicer built over the raw
// engines gives every tenant the same lock (go-qrdb#455).
func WithLock(svc service.LockServicer) ServerOption {
	return func(s *Server) { s.lockSvc = svc }
}

// WithIndex configures the QUIC server with an index service.
func WithIndex(svc index.IndexServicer) ServerOption {
	return func(s *Server) { s.indexSvc = svc }
}

// WithTimeSeries configures the QUIC server with a time-series service.
func WithTimeSeries(svc timeseries.TimeSeriesServicer) ServerOption {
	return func(s *Server) { s.tsSvc = svc }
}

// WithVector configures the QUIC server with a vector service.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(s *Server) { s.vectorSvc = svc }
}

// WithLogService configures the QUIC server with the verifiable-log
// service. The vlog.* methods are served only when it is set.
func WithLogService(svc service.LogService) ServerOption {
	return func(s *Server) { s.logSvc = svc }
}

// WithUserService configures the QUIC server with a user management service.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(s *Server) { s.userSvc = svc }
}

// WithRoleService configures the QUIC server with a role management service.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(s *Server) { s.roleSvc = svc }
}

// WithSOGate installs the SO dual-factor gate (authmw.SOGate) on every method
// whose permission is in authmw.SOPermissions, after authorization: the caller
// must hold the SO role and present the SO PIN in the request message's
// so_pin field (see soPINCarriers: a QUIC frame has no header section). QUIC
// is TLS-only, so the factor always arrives over TLS. It is the same gate the REST transport
// applies, built by the orchestrator when auth.so_dual_factor is on; nil
// leaves it off.
func WithSOGate(gate *authmw.SOGate) ServerOption {
	return func(s *Server) { s.soGate = gate }
}

// WithCertProvider configures the QUIC server with a certificate provider.
func WithCertProvider(prov cert.CertProvider) ServerOption {
	return func(s *Server) { s.certProv = prov }
}

// WithCertEnrollment configures the certificate issue/enrol and trust-anchor
// service behind cert.Issue, cert.Enroll and tenant.TrustAnchor*.
func WithCertEnrollment(svc service.CertEnrollmentServicer) ServerOption {
	return func(s *Server) { s.certEnroll = svc }
}

// WithInvitations configures the invitation self-enrolment service behind
// user.Invite and the PUBLIC invitation.Redeem.
func WithInvitations(svc service.InvitationServicer) ServerOption {
	return func(s *Server) { s.invitations = svc }
}

// WithTenantOnboarding configures the service behind tenant.Create.
func WithTenantOnboarding(svc service.TenantOnboardingServicer) ServerOption {
	return func(s *Server) { s.onboarding = svc }
}

// WithCollectionService configures the QUIC server with a collection service.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) { s.collectionSvc = svc }
}

// WithDatabaseService configures the QUIC server with a database management service.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) { s.databaseSvc = svc }
}

// WithBarrierService configures the QUIC server with a barrier management service.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(s *Server) { s.barrierSvc = svc }
}

// WithEncryptionMigrator configures the QUIC server with the
// enable-encryption migrator, exposing BarrierEnableEncryption over the
// native QUIC transport the same way it is exposed over REST, gRPC, and Unix.
func WithEncryptionMigrator(m service.EncryptionMigrator) ServerOption {
	return func(s *Server) { s.encMigrator = m }
}

// WithSQLService configures the QUIC server with a SQL query service for
// ad-hoc SQL execution against a named database.
func WithSQLService(svc service.SQLServicer) ServerOption {
	return func(s *Server) { s.sqlSvc = svc }
}

// sealedWindowProbe is the abstraction the sealed-window gate needs to know
// whether the storage barrier is currently sealed. Mirrors grpcpkg's
// unexported interface of the same name.
type sealedWindowProbe interface {
	IsSealed() bool
}

// WithSealedWindowProbe installs the sealed-window gate on the native QUIC
// transport. While probe.IsSealed() returns true, every method not on
// SealedWindowAllowlist is rejected with wire.StatusUnavailable carrying the
// SEALED ErrorInfo, mirroring the gRPC/REST servers.
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) {
		s.sealedWindowProbe = probe
		s.sealedWindowListener = nil
	}
}

// WithSealedWindowListener installs the boot-time sealed-window listener's
// gate backed by probe: the allowlist while the barrier is sealed, and
// wire.StatusUnavailable for every method once it is unsealed, sampled per
// request. That listener runs without authentication, so it must not serve
// anything against an unsealed barrier, even in the moment before it is
// stopped. It replaces WithSealedWindowProbe; the last of the two applied
// wins.
func WithSealedWindowListener(probe sealedwindow.ListenerProbe) ServerOption {
	return func(s *Server) {
		probe = sealedwindow.OrFailClosed(probe)
		s.sealedWindowProbe = probe
		s.sealedWindowListener = probe
	}
}

// WithSealedWindowDispatch makes a regular listener behave, while probe
// reports the node barrier sealed, exactly as the boot-time sealed-window
// listener does: such a request skips the awaiting-admin-credential gate,
// authentication (and so any user-store lookup), the SO gate and replica
// forwarding, and passes the gate WithSealedWindowListener installs -- the
// allowlist, anonymously, and wire.StatusUnavailable for everything else.
// Requests that arrive while it reports the barrier unsealed take the regular
// path unchanged. Build probe with server.NewSealedWindowListenerProbe over
// the bare node barrier, not the cluster-seal probe: a node a cluster seal
// holds keeps its barrier unsealed and its authenticated cluster-unseal verb.
func WithSealedWindowDispatch(probe sealedwindow.ListenerProbe) ServerOption {
	return func(s *Server) {
		s.sealedWindowDispatch = probe
	}
}

// WithPeerChainVerifier sets the verifier that resolves the caller's client
// certificate when this listener's TLS configuration accepted it without
// building a chain: the client trust pool the configuration is wired to. The
// listener binds it, with the connection's TLS state, to every request before
// admission, so a share claim binds only a verified certificate on the
// authenticated path and in the sealed window alike. Unset, the listener
// uses its AuthMiddleware's issuer verifier.
func WithPeerChainVerifier(v auth.IssuerVerifier) ServerOption {
	return func(s *Server) {
		s.peerChainVerifier = v
	}
}

// chainVerifier is the verifier the listener binds to every request:
// WithPeerChainVerifier's, else the AuthMiddleware's issuer verifier.
func (s *Server) chainVerifier() auth.IssuerVerifier {
	if s.peerChainVerifier != nil {
		return s.peerChainVerifier
	}
	return s.authMW.IssuerVerifier()
}

// WithAwaitingAdminCredentialProbe installs the awaiting-admin-credential
// gate (go-qrdb#548 review item 3): while probe reports awaiting, only
// ceremony status and ceremony-initialize are dispatched, skipping auth,
// the SO gate and the sealed-window gate entirely -- there is no bound
// identity to authenticate as -- and every other method is refused with
// wire.StatusUnavailable + *middleware.AwaitingAdminCredentialError.
func WithAwaitingAdminCredentialProbe(probe awaitingAdminCredentialProbe) ServerOption {
	return func(s *Server) { s.awaitingAdminCredentialProbe = probe }
}

// WithPINBackend configures the QUIC server with the canonical
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

// WithCeremonyService configures the QUIC server with a ceremony management service.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(s *Server) { s.ceremonySvc = svc }
}

// WithCeremonyCA sets the CeremonyCA used by the ceremony initialization
// handler to issue the TLS and SO certificates. The CA cannot be serialized
// over the wire, so it must be injected at server creation time and is held
// as a server-side reference, mirroring the REST, MCP, and gRPC transports.
// When unset, CeremonyInitialize fails closed with a
// *CeremonyCANotConfiguredError instead of dereferencing a nil CA.
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption {
	return func(s *Server) { s.ceremonyCA = ca }
}

// WithTenantService configures the QUIC server with a tenant management service.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantSvc = svc }
}

// WithVolumeService configures the QUIC server with the volume pool
// management surface (docs/volume-pools/api.md).
func WithVolumeService(svc volume.Service) ServerOption {
	return func(s *Server) { s.volumeSvc = svc }
}

// WithBackupService configures the QUIC server with a backup management service.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(s *Server) { s.backupSvc = svc }
}

// WithRebalanceService configures the QUIC server with a cluster rebalance service.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(s *Server) { s.rebalanceSvc = svc }
}

// WithEmbedding configures the QUIC server with a text embedding service.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(s *Server) { s.embeddingSvc = svc }
}

// WithRAGService configures the QUIC server with a RAG service.
func WithRAGService(svc service.RAGServicer) ServerOption {
	return func(s *Server) { s.ragSvc = svc }
}

// WithCredentialService configures the QUIC server with a credential
// management service for credential storage, rotation, and versioning.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(s *Server) { s.credentialSvc = svc }
}

// WithDEKService configures the QUIC server with the tenant DEK keyring
// service. Without it the dek.* methods are not served.
func WithDEKService(svc service.DEKServicer) ServerOption {
	return func(s *Server) { s.dekSvc = svc }
}

// WithPolicyService configures the QUIC server with the tenant ABAC policy
// store, registering the abacpolicy.* methods.
func WithPolicyService(svc service.PolicyServicer) ServerOption {
	return func(s *Server) { s.policySvc = svc }
}

// WithAuthFactorService configures the QUIC server with an auth factor service
// for custodian enrollment, challenge, and verification.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(s *Server) { s.authFactorSvc = svc }
}

// WithUserAuthFactorService configures the QUIC server with the user-side
// auth factor service (M14) used for the 1-to-many user-factor model.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) ServerOption {
	return func(s *Server) { s.userAuthFactorSvc = svc }
}

// WithAuditLogger configures the QUIC server with a compliance-grade audit logger.
// When set, the "audit.Query" frame method is registered.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
}

// WithReplicatedAuditLogger configures the QUIC server with the
// cluster-wide, per-tenant-opt-in replicated audit sink
// (docs/audit/architecture.md#cluster-wide-replicated-trail). When set, the
// "audit.Query" frame method's replicated payload field and the
// "audit.VerifyReplicatedChain" frame method are registered.
func WithReplicatedAuditLogger(l *service.OptInAuditLogger) ServerOption {
	return func(s *Server) { s.replicatedAudit = l }
}

// WithAuthMiddleware configures the QUIC server with authentication and
// authorization middleware.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) { s.authMW = mw }
}

// WithServiceRegistry configures the QUIC server with a per-(tenant, database)
// service registry. When set, data-plane handlers resolve their servicer from
// the registry using the authenticated tenant and the database field carried
// in the request frame; when nil they use the singleton service fields. This
// gives the native QUIC transport the same per-database keyspace isolation as
// the REST, gRPC, and Unix transports.
func WithServiceRegistry(reg service.Registry) ServerOption {
	return func(s *Server) { s.registry = reg }
}

// WithKVService configures the QUIC server with a KV service. This is an
// alternative to passing kvSvc as a positional parameter and is useful when
// constructing a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(s *Server) { s.kvSvc = svc }
}

// WithAdminService configures the QUIC server with an admin service. This is
// an alternative to passing adminSvc as a positional parameter and is useful
// when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(s *Server) { s.adminSvc = svc }
}

// WithServiceModules registers capability modules whose gRPC service(s) are
// projected onto the QUIC frame dispatch table at construction, via each
// module's RegisterQUIC hook.
func WithServiceModules(mods []apimodule.ServiceModule) ServerOption {
	return func(s *Server) { s.modules = mods }
}

// NewServer creates a new native QUIC server that listens on the given
// address and delegates to the provided service implementations. kvSvc and
// adminSvc may be nil when only non-KV services (barrier, cert, auth) are
// needed; their handlers are registered only when the respective service is
// non-nil. The server is not started until Start() is called.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
	}

	for _, opt := range opts {
		opt(s)
	}

	s.buildHandlers()

	return s
}

// quicOnlyTLSConfig clones base and pins its ALPN list to exactly
// wire.ProtocolV3 -- nothing a TLS config shared with the REST listener
// carries ("h3", "h2"): this listener speaks one protocol, so a client that
// does not offer it fails the handshake here rather than connecting under a
// label it will not be served by.
//
// The reassertion is delegated to clienttrust.ReassertListenerTLS -- the ONE
// shared helper every listener-specific TLS override (this ALPN pin, and
// REST/gRPC/Unix/MCP's TLS 1.3 MinVersion floor) composes with, rather than
// each hand-rolling the same clone-and-rewrap: a dynamic GetConfigForClient
// hook shared with the other listeners (pkg/clienttrust's trust-pool
// widening) may be installed on base BEFORE this clone runs, and a bare
// Clone copies the hook's closure, not its effect -- the closure still
// clones from the ORIGINAL base it closed over, whose NextProtos is
// whatever the shared config carries for REST/gRPC/Unix/MCP, not
// wire.ProtocolV3. Left unwrapped, a connection that exercises the dynamic
// path (any Key B certificate login) would be served the WRONG ALPN list
// and fail the handshake with "tls: server did not select an ALPN protocol"
// (go-qrdb#560 review, B2-3).
func quicOnlyTLSConfig(base *tls.Config) *tls.Config {
	return clienttrust.ReassertListenerTLS(base, func(c *tls.Config) {
		c.NextProtos = []string{wire.ProtocolV3}
	})
}

// Start begins listening for QUIC connections and serving requests.
// The method returns after the listener is bound; connections are
// served in the background.
func (s *Server) Start() error {
	if s.tlsConfig == nil {
		s.closePrebound()
		return &StartError{Addr: s.addr, Err: &tlsRequiredError{}}
	}

	if s.replicaForwarding != nil {
		s.forwarder = NewFrameForwarder(*s.replicaForwarding, s.tlsConfig)
	}

	tlsCfg := quicOnlyTLSConfig(s.tlsConfig)

	// 0-RTT stays off (Allow0RTT is false). Early data is replayable: an
	// on-path attacker can resend a captured flight and the server processes
	// it again. Every frame this listener serves is a request, and many of
	// them change state. A resumed client completes a full handshake first.
	quicCfg := &quicgo.Config{
		MaxIdleTimeout:        30 * time.Second,
		KeepAlivePeriod:       10 * time.Second,
		MaxIncomingStreams:    int64(resolveMaxStreamsPerConn(s.maxStreamsPerConn)),
		MaxIncomingUniStreams: 0,
	}

	udpConn, err := s.packetConn()
	if err != nil {
		return &StartError{Addr: s.addr, Err: err}
	}
	tr := &quicgo.Transport{Conn: udpConn}
	ln, err := tr.Listen(tlsCfg, quicCfg)
	if err != nil {
		_ = tr.Close()
		_ = udpConn.Close()
		return &StartError{Addr: s.addr, Err: err}
	}
	s.udpConn = udpConn
	s.transport = tr

	// Per-peer connection cap: without it, a single remote IP can open
	// unlimited concurrent QUIC connections against this listener
	// (go-qrdb#576). Wrapped around the raw quic-go listener, beneath
	// everything else, so a refused connection is closed before any
	// stream -- and therefore any request -- exists on it.
	if s.maxConnsPerPeer > 0 {
		s.listener = connlimit.NewQUICListener(ln, s.maxConnsPerPeer)
	} else {
		s.listener = ln
	}

	s.connObservers = apimodule.ConnObserversOf(s.modules)
	s.stopping = make(chan struct{})

	slog.Info("QUIC server started", "addr", ln.Addr().String())

	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// quicStopGracePeriod bounds how long Stop waits for in-flight handleStream
// calls to finish naturally before it force-closes their connections. It is
// deliberately the same order of magnitude as REST's Shutdown(ctx) timeout
// (pkg/api/rest/server.go) rather than tied to any one handler's own accept
// window (750ms for the enable-encryption family): Stop is generic across
// every method this server dispatches, and the grace period only needs to
// comfortably outlast the slowest of them plus the time to write a reply, not
// match any single one exactly.
const quicStopGracePeriod = 5 * time.Second

// quicResponseAckTimeout bounds how long a handler waits, after writing its
// response, for the client's acknowledgement before giving up and returning
// anyway. See awaitStreamAck.
const quicResponseAckTimeout = 2 * time.Second

// awaitStreamAck blocks, up to quicResponseAckTimeout, until the client closes
// its write side of stream -- io.EOF on Read -- or the stream fails.
//
// quic-go v0.62 has no public API for "has the peer received this data":
// SendStream.Close only schedules the FIN for the next outgoing packet, and
// Stream.Context is canceled on LOCAL close, not on peer acknowledgement
// (quic-go issue #3291 tracks the absence of a send-completion primitive). A
// handler returning therefore proves its response was queued, not that it
// reached the wire, which is what let CloseWithError abort a connection
// carrying an already-computed "migration_started" reply before the client
// ever saw it (go-qrdb#550): the caller saw "server shutting down" instead of
// its answer.
//
// The acknowledgement is the client's FIN (see wire.ProtocolV3): the client
// closes its write side only after it has read the response, so the EOF this
// function waits for cannot arrive before the response did.
//
// Any bytes the client sends after its request frame are not part of the
// protocol and are discarded; only end-of-stream or a stream error ends the
// wait. Bounded rather than indefinite: a client that never closes its stream
// (a hung process, or a hostile one) must not be able to stall a handler, and
// by extension Stop's streamWG drain, forever.
func awaitStreamAck(stream *quicgo.Stream) {
	_ = stream.SetReadDeadline(time.Now().Add(quicResponseAckTimeout))
	var buf [16]byte
	for {
		if _, err := stream.Read(buf[:]); err != nil {
			return
		}
	}
}

// writeResponse writes a response frame, closes this side of the stream, and
// waits for the client's acknowledgement (awaitStreamAck) before returning.
// Every exit path out of handleStream answers through here, so every reply --
// not only the successful-dispatch one -- is confirmed delivered before
// Stop's connection-close pass can run.
//
// The stream is closed BEFORE the wait so the response's FIN travels with it:
// a client that reads until end-of-stream rather than one frame sees that end
// immediately instead of after the wait. The handler's deferred Close is then
// a no-op. A write failure is returned unchanged and skips the wait, since
// nothing was sent for a peer to acknowledge.
func (s *Server) writeResponse(stream *quicgo.Stream, requestID uint64, status uint16, payload []byte) error {
	if err := wire.WriteResponse(stream, requestID, status, payload); err != nil {
		return err
	}
	_ = stream.Close()
	awaitStreamAck(stream)
	return nil
}

// packetConn returns the socket Start serves on: the pre-bound one when the
// embedder handed one over, otherwise a UDP socket bound on the server's
// address.
func (s *Server) packetConn() (net.PacketConn, error) {
	if s.preboundPacketConn != nil {
		return s.preboundPacketConn, nil
	}
	udpAddr, err := net.ResolveUDPAddr("udp", s.addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// closePrebound closes a pre-bound socket Start refused to serve on, so the
// socket handed over is not leaked.
func (s *Server) closePrebound() {
	if s.preboundPacketConn != nil {
		_ = s.preboundPacketConn.Close()
	}
}

// Stop gracefully shuts down the QUIC server. It closes the listener (so no
// new connections are accepted), then gives in-flight handleStream calls up
// to quicStopGracePeriod to finish -- which includes each one's own
// awaitStreamAck wait for its response to be confirmed delivered, not merely
// written -- before actively closing every still-live connection tracked in
// s.conns. It then waits for every tracked goroutine to drain and closes the
// transport and the UDP socket, so the address can be bound again the moment
// Stop returns.
//
// Waiting on in-flight handlers BEFORE closing their connections -- rather
// than closing every connection immediately and letting handlers fail out --
// is required for the same reason grpc.Server.GracefulStop and
// http.Server.Shutdown(ctx) give their own in-flight requests this treatment:
// a system-scope encryption migration's Quiesce phase recycles this very
// server from within the migration goroutine the enable-encryption handler is
// waiting on (pkg/api/quic/handlers_barrier.go's accept-window doc), and a
// migration whose synchronous phase (mint the keyed target) finishes faster
// than the handler's own 750ms accept window reaches Quiesce -> this Stop
// call WHILE that handler is still waiting to hear back. Before this fix,
// closing every connection first -- including the one the still-pending
// handler owned -- severed it out from under the handler: by the time the
// handler's accept window elapsed and it tried to write "migration_started",
// the connection was already gone, and the caller saw "server shutting down"
// instead of its answer (go-qrdb#550). Waiting first gives that handler the
// SAME chance to finish and reply that grpc.Server.GracefulStop already gives
// the identical request on the gRPC transport (confirmed: gRPC's handler
// reaches this exact interleaving and its GracefulStop call blocks until the
// pending RPC completes, rather than aborting it).
//
// The wait is bounded rather than indefinite so this cannot reintroduce the
// up-to-30s stall active-closing was written to avoid (see conns' doc
// comment): a connection with no in-flight handler contributes nothing to
// streamWG and is force-closed immediately once the grace period -- which
// elapses at once when streamWG is already empty -- is reached. New stream
// admission is closed atomically with this wait (see acceptClosingMu and
// handleConnection) so a stream accepted after draining has begun can never
// escape both streamWG AND the close pass that follows it.
func (s *Server) Stop() {
	if s.listener == nil {
		return
	}
	s.closed.Store(true)
	_ = s.listener.Close()
	if s.forwarder != nil {
		s.forwarder.Close()
	}

	// Close stream admission BEFORE waiting: handleConnection's Add-then-spawn
	// for a new stream must not be able to happen after streamWG.Wait has
	// already observed zero, which would let that stream's connection be
	// force-closed out from under a handler streamWG never counted. Both this
	// flag's read (in handleConnection, before Add) and its write here take
	// acceptClosingMu, so there is no window where a stream is admitted
	// without being counted first.
	s.acceptClosingMu.Lock()
	s.acceptClosing = true
	s.acceptClosingMu.Unlock()

	// End watch streams now rather than letting each one run out the grace
	// period below: they are waiting on events, not finishing a reply.
	s.stopOnce.Do(func() { close(s.stopping) })

	drained := make(chan struct{})
	go func() {
		s.streamWG.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(quicStopGracePeriod):
	}

	s.conns.Range(func(key, _ any) bool {
		_ = key.(*quicgo.Conn).CloseWithError(0, "server shutting down") //nolint:errcheck // type assertion on a value this container only ever holds
		return true
	})
	if s.testHookConnsClosed != nil {
		s.testHookConnsClosed()
	}
	s.wg.Wait()

	// Every connection has been closed above, each CONNECTION_CLOSE already
	// sent (CloseWithError returns once the connection has shut down), so
	// nothing is lost by dropping the IDs quic-go would otherwise keep
	// registered to repeat it. Closing the transport and then the socket this
	// server owns is what frees the port before Stop returns (see udpConn).
	_ = s.transport.Close()
	_ = s.udpConn.Close()
	slog.Info("QUIC server stopped")
}

// Addr returns the listener address. Useful for tests that use port 0.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// acceptLoop accepts new QUIC connections until the listener is closed.
func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept(context.Background())
		if err != nil {
			if s.closed.Load() {
				return
			}
			slog.Error("quic: accept error", "error", err)
			continue
		}

		if s.testHookAccepted != nil {
			s.testHookAccepted()
		}
		s.conns.Store(conn, struct{}{})
		// Accept keeps handing out connections already queued when the
		// listener closed, so one can be stored after Stop's close pass has
		// ranged over s.conns. Nothing would ever close it, its
		// handleConnection would sit in AcceptStream, and Stop's wg.Wait would
		// hang behind it. Stop sets s.closed before that pass, so a connection
		// stored after it always sees s.closed here and is closed on the spot.
		if s.closed.Load() {
			s.conns.Delete(conn)
			_ = conn.CloseWithError(0, "server shutting down")
			continue
		}
		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// handleConnection processes streams on a single QUIC connection. It
// deregisters the connection from s.conns on exit so Stop's active-close
// pass (see Stop's doc comment) never touches an already-finished
// connection.
func (s *Server) handleConnection(conn *quicgo.Conn) {
	defer s.wg.Done()
	defer s.conns.Delete(conn)
	// handleConnection runs for exactly the connection's life -- it returns
	// only once AcceptStream fails because the connection closed -- so its
	// span is the span reported to connection observers. Deferred after
	// wg.Done so the close is reported before Stop's wg.Wait returns.
	defer s.connObservers.Open(apimodule.TransportQUIC)()

	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			if s.closed.Load() {
				return
			}
			// Connection closed by client or network error.
			return
		}

		// Checking acceptClosing and adding to streamWG happen under the same
		// lock Stop takes before it ever calls streamWG.Wait -- see
		// acceptClosingMu's doc comment for why that atomicity, not just the
		// ordering, is what closes the race.
		s.acceptClosingMu.Lock()
		if s.acceptClosing {
			s.acceptClosingMu.Unlock()
			// Refuse rather than admit: Stop has already started draining and
			// will never wait for this stream. Resetting both directions tells
			// a well-behaved client to retry rather than hang reading a
			// response that will never come.
			stream.CancelWrite(0)
			stream.CancelRead(0)
			continue
		}
		s.wg.Add(1)
		s.streamWG.Add(1)
		s.acceptClosingMu.Unlock()

		go s.handleStream(conn, stream)
	}
}

// handleStream reads a request frame from the stream, dispatches to the
// handler, writes the response frame, and closes the stream.
func (s *Server) handleStream(conn *quicgo.Conn, stream *quicgo.Stream) {
	defer s.wg.Done()
	defer s.streamWG.Done()
	defer func() { _ = stream.Close() }()

	// Recover from panics in handlers.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("quic: panic recovered",
				"panic", r,
				"stack", string(debug.Stack()),
			)
			st, body := errorFrameBody(wire.StatusError, wire.ErrorPayload{Message: internalErrorMessage})
			_ = s.writeResponse(stream, 0, st, body)
		}
	}()

	method, database, requestID, payload, err := wire.ReadRequestLimited(stream, resolveMaxPayloadBytes(s.maxPayloadBytes))
	if err != nil {
		slog.Debug("quic: failed to read request", "error", err)
		return
	}

	start := time.Now()

	// The client-address limiter (go-qrdb#1197) admits the request before
	// anything -- the OnBehalf decode, the awaiting-admin and sealed-window
	// gates, authentication -- does work on its behalf. Native QUIC carries
	// no forwarding headers: the key is the connection's own remote address.
	if err := s.clientLimit.Admit(conn.RemoteAddr(), nil); err != nil {
		st, body := errorFrame(wire.StatusResourceExhausted, err)
		_ = s.writeResponse(stream, requestID, st, body)
		s.logRequest(method, requestID, st, start)
		return
	}

	// A request a cluster peer forwarded on a caller's behalf carries the
	// caller's method and payload in an envelope. It is served as that
	// method, as that caller once the assertion verifies -- and, being a
	// forward already, it is never forwarded again.
	var onBehalf *wire.OnBehalf
	if method == wire.MethodOnBehalf {
		env, derr := wire.DecodeOnBehalf(payload)
		if derr != nil {
			st, body := errorFrame(wire.StatusInvalidArgument, derr)
			_ = s.writeResponse(stream, requestID, st, body)
			s.logRequest(method, requestID, st, start)
			return
		}
		onBehalf, method, payload = &env, env.Method, env.Payload
	}

	// Build context with connection state for auth and the database scope for
	// per-(tenant, database) service resolution. The tenant dimension flows
	// separately from the authenticated identity (set during authenticate);
	// only the database is sourced from the request frame. An empty database
	// resolves to the implicit bootstrap database.
	ctx := context.Background()
	ctx = withConnectionState(ctx, conn, s.chainVerifier())
	ctx = tenant.WithDatabase(ctx, database)

	// The awaiting-admin-credential gate (go-qrdb#548 review item 3) runs
	// BEFORE auth, the SO gate and the sealed-window gate, and -- when it
	// admits a method -- skips all three of them entirely: there is no
	// bound identity to authenticate as, which is the whole reason the
	// node is in this state. A refused method never reaches any gate
	// below, or any handler. This check is on the already-unwrapped
	// method (see the OnBehalf decode above), so a cluster peer forwarding
	// a call on a caller's behalf gets exactly the same allowlist
	// treatment as a direct caller: it cannot smuggle a non-allowlisted
	// method past the gate by wrapping it in an OnBehalf envelope, and it
	// cannot be made to skip real authentication via this gate unless the
	// forwarded method is itself genuinely allowlisted.
	//
	// A request that arrives while the node barrier is sealed takes the
	// boot-time sealed-window listener's path instead (see
	// WithSealedWindowDispatch): none of the gates above its own, and no
	// forwarding.
	sealedPath := s.sealedWindowDispatch != nil && s.sealedWindowDispatch.IsSealed()
	gateProbe, gateListener := s.sealedWindowProbe, s.sealedWindowListener
	if sealedPath {
		gateProbe, gateListener = s.sealedWindowDispatch, s.sealedWindowDispatch
	}
	awaiting := !sealedPath && s.awaitingAdminCredentialProbe != nil && s.awaitingAdminCredentialProbe.AwaitingAdminCredential()
	if awaiting && !awaitingAdminCredentialAllowed(method) {
		st, body := errorFrame(wire.StatusUnavailable, &authmw.AwaitingAdminCredentialError{})
		_ = s.writeResponse(stream, requestID, st, body)
		s.logRequest(method, requestID, st, start)
		return
	}
	if !awaiting {
		// Auth: authenticate and authorize if middleware is configured. A
		// method declared with RegisterPublic (invitation.Redeem, and a
		// module's GRPCPublicMethodLister methods) skips both -- its caller
		// holds no credential yet -- exactly as REST's RESTRoutePublic and gRPC's
		// RegisterPublicGRPCMethods already skip auth for their own public
		// routes.
		if !sealedPath && s.authMW != nil && !s.methodPerms.IsPublic(method) {
			if onBehalf != nil {
				ctx, err = s.authenticateOnBehalf(ctx, conn, method, payload, onBehalf)
			} else {
				ctx, err = s.authenticate(ctx, conn)
			}
			if err != nil {
				st, body := errorFrame(authnStatus(err), err)
				_ = s.writeResponse(stream, requestID, st, body)
				s.logRequest(method, requestID, st, start)
				return
			}

			if authErr := s.authorize(ctx, method); authErr != nil {
				// The status follows the CLASS of the failure, not the call site.
				// Hard-coding PermissionDenied told a caller whose request this
				// node merely could not RECORD that it was not permitted, and
				// permission-denied is terminal in every client.
				authStatus, body := errorFrame(authzStatus(authErr), authErr)
				_ = s.writeResponse(stream, requestID, authStatus, body)
				s.logRequest(method, requestID, authStatus, start)
				return
			}
		}

		// The SO dual-factor gate, after authorization and before any handler,
		// exactly where the REST and gRPC transports apply it. It runs whenever
		// there is a gate, auth middleware or not: with nothing to authenticate
		// the caller there is no SO to admit, so it fails closed rather than
		// being skipped.
		if !sealedPath && s.soGate != nil {
			if soErr := s.enforceSOGate(ctx, conn, method, payload); soErr != nil {
				soStatus, body := errorFrame(authzStatus(soErr), soErr)
				_ = s.writeResponse(stream, requestID, soStatus, body)
				s.logRequest(method, requestID, soStatus, start)
				return
			}
		}

		// Sealed-window gate: while the barrier is sealed, every method not on
		// SealedWindowAllowlist -- registered or not -- is refused with the
		// typed sealed Unavailable, so a sealed node serves the unseal call
		// and nothing else over the native QUIC transport. On the boot-time
		// sealed-window listener every method is rejected once the barrier is
		// unsealed. Mirrors the REST/gRPC sealed-window middleware/interceptors.
		if gateListener != nil {
			// Held until this request completes; see sealedwindow.Admission.
			_, transition := sealedWindowTransition[method]
			defer gateListener.Admit(transition)()
		}
		if refusal, refused := sealedWindowGateRefusal(ctx, method, gateProbe, gateListener); refused {
			st, body := errorFrameBody(refusal.status, refusal.payload)
			_ = s.writeResponse(stream, requestID, st, body)
			s.logRequest(method, requestID, st, start)
			return
		}
		if gateListener != nil {
			ctx = sealedwindow.WithAnonymous(ctx)
		}
	}

	// Watch is server-streaming: it writes a stream of response frames on this
	// same stream rather than a single unary response, so it bypasses the
	// request/response dispatch table.
	if isWatchMethod(method) {
		s.handleWatchStream(ctx, stream, requestID, method == watchPrefixMethodKey, payload)
		s.logRequest(method, requestID, wire.StatusOK, start)
		return
	}

	// Dispatch to handler.
	handler, ok := s.handlers[method]
	if !ok {
		st, body := errorFrame(wire.StatusUnimplemented, &DispatchError{Method: method})
		_ = s.writeResponse(stream, requestID, st, body)
		s.logRequest(method, requestID, st, start)
		return
	}

	resp, handlerErr := handler(ctx, payload)
	if handlerErr != nil {
		if s.forwarder != nil && onBehalf == nil && !sealedPath {
			if refusal, ok := errors.AsType[*placement.NotAReplicaError](handlerErr); ok {
				if st, body, forwarded := s.forwarder.Forward(ctx, auth.IdentityFromContext(ctx), method, database, payload, refusal); forwarded {
					_ = s.writeResponse(stream, requestID, st, body)
					s.logRequest(method, requestID, st, start)
					return
				}
			}
			// What only a shard's leader can commit -- a membership change,
			// a write, the admin-credential step of CeremonyInitialize
			// (service.CeremonyServiceWithLeaderGate) -- a follower refuses
			// as not the leader. It forwards the identical request to the
			// leader that refusal names through the SAME FrameForwarder the
			// per-database path above uses, rather than answering its own
			// refusal (node parity, owner ruling 2026-10-06).
			if leaderAddr, ok := service.LeaderAddress(handlerErr); ok {
				if st, body, forwarded := s.forwarder.ForwardToLeader(ctx, auth.IdentityFromContext(ctx), method, database, payload, leaderAddr); forwarded {
					_ = s.writeResponse(stream, requestID, st, body)
					s.logRequest(method, requestID, st, start)
					return
				}
			}
		}
		statusCode, errPayload := mapServiceError(handlerErr)
		_ = s.writeResponse(stream, requestID, statusCode, errPayload)
		s.logRequest(method, requestID, statusCode, start)
		return
	}

	_ = s.writeResponse(stream, requestID, wire.StatusOK, resp)
	s.logRequest(method, requestID, wire.StatusOK, start)
}

// logRequest logs a QUIC request at the appropriate level.
func (s *Server) logRequest(method string, requestID uint64, status uint16, start time.Time) {
	duration := time.Since(start)
	level := slog.LevelDebug
	if status != wire.StatusOK {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "quic: request",
		"method", method,
		"request_id", requestID,
		"status", status,
		"duration", duration,
	)
}

// tlsRequiredError is returned when TLS is not configured for the QUIC server.
type tlsRequiredError struct{}

func (e *tlsRequiredError) Error() string {
	return "quic: TLS is required for QUIC transport"
}
