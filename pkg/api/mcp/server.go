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

// Package mcp provides a Model Context Protocol server for QRDB.
// It exposes KV and admin operations as MCP tools, enabling AI agent
// interaction with the QRDB cluster.
package mcp

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"golang.org/x/net/netutil"

	"github.com/jeremyhahn/go-qrdb/pkg/api/connlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// Server is the MCP server for QRDB. It registers KV, admin, MVCC,
// transaction, batch, watch, lease, index, time-series, vector, user,
// role, and certificate operations as MCP tools and serves them over
// HTTP using the Streamable HTTP transport.
type Server struct {
	kvSvc        service.KVServicer
	adminSvc     service.AdminServicer
	mvccSvc      service.MVCCKVServicer
	txSvc        service.TransactionServicer
	batchSvc     service.BatchServicer
	watchSvc     service.WatchServicer
	leaseSvc     service.LeaseServicer
	lockSvc      service.LockServicer
	indexSvc     service.IndexServicer
	tsSvc        service.TimeSeriesServicer
	vectorSvc    service.VectorServicer
	embeddingSvc service.EmbeddingServicer
	ragSvc       service.RAGServicer
	userSvc      user.UserServicer
	roleSvc      role.RoleServicer
	certProv     cert.CertProvider
	certEnroll   service.CertEnrollmentServicer
	invitations  service.InvitationServicer
	// enrollSrv is the PUBLIC enrolment MCP server on EnrollPath; nil when
	// no invitation service is wired.
	enrollSrv         *mcpserver.MCPServer
	onboarding        service.TenantOnboardingServicer
	collectionSvc     service.CollectionServicer
	dbSvc             service.DatabaseServicer
	barrierSvc        service.BarrierServicer
	encMigrator       service.EncryptionMigrator
	sqlSvc            service.SQLServicer
	pinBackend        pin.PINBackend
	ceremonySvc       service.CeremonyServicer
	credentialSvc     service.CredentialServicer
	dekSvc            service.DEKServicer
	tenantSvc         tenant.TenantServicer
	volumeSvc         volume.Service
	backupSvc         service.BackupServicer
	rebalanceSvc      service.RebalanceServicer
	ceremonyCA        ceremony.CeremonyCA
	authFactorSvc     service.AuthFactorServicer
	userAuthFactorSvc service.UserAuthFactorServicer
	auditLogger       audit.AuditLogger
	replicatedAudit   *service.OptInAuditLogger

	// soGate is the SO dual-factor gate; nil leaves it off. See WithSOGate.
	soGate *authmw.SOGate
	// registry, when set, resolves data-plane services per (tenant, database)
	// scope. The tenant comes from the authenticated identity; the database
	// from the optional "database" tool argument. When nil, the singleton
	// service fields are used, preserving single-database and embedded
	// deployments.
	registry  service.Registry
	addr      string
	tlsConfig *tls.Config
	tlsErr    error
	authMW    *authmw.AuthMiddleware
	mcpSrv    *mcpserver.MCPServer
	httpSrv   *http.Server
	listener  net.Listener

	// toolPerms is the single source of truth for per-tool authorization.
	// addTools populates it from each tool's registration-site permission
	// declaration (the serverTool.Perm field), so every tool declares its
	// permission alongside its schema and handler; AuthorizeMCP consults it
	// with DEFAULT-DENY semantics.
	toolPerms *authmw.MCPToolRegistry

	// publicToolsErr is the refusal of a module's public-tool declaration
	// (registerPublicTools), surfaced by Start so the server never serves a
	// surface it refused.
	publicToolsErr error

	// toolMiddlewares is the tool handler middleware chain, outermost first,
	// installed on mcpSrv and applied by SealedWindowTools to the tools it
	// exports.
	toolMiddlewares []mcpserver.ToolHandlerMiddleware

	// replicaForwarding, when set, makes the listener forward what it
	// refuses as a non-replica (WithReplicaForwarding); forwarder is built
	// from it, over the listener's TLS identity, at construction.
	replicaForwarding *replicaforward.Config
	forwarder         *replicaForwarder

	// modules are ServiceModule instances (see pkg/apimodule) whose MCP tools
	// are registered on the shared mcpSrv after the built-in tool sets, giving
	// higher layers (e.g. go-xkms) the same MCP transport reach as REST, gRPC,
	// QUIC, and Unix without standing up a parallel server.
	modules []apimodule.ServiceModule

	// sealedWindowProbe controls the sealed-window tool-handler middleware.
	// While the probe reports sealed, every tool call not on
	// SealedWindowAllowlist is rejected. Wired identically to the REST,
	// gRPC, and native-QUIC sealed-window gates so operators see the same
	// envelope on every transport.
	sealedWindowProbe sealedWindowProbe

	// sealedWindowListener is the boot-time sealed-window listener's probe,
	// and selects that listener's gate. Nil for a regular listener.
	sealedWindowListener sealedwindow.ListenerProbe

	// awaitingAdminCredentialProbe controls the awaiting-admin-credential
	// gate (go-qrdb#548 review item 3). See WithAwaitingAdminCredentialProbe.
	awaitingAdminCredentialProbe awaitingAdminCredentialProbe

	// bearerChallenge, when non-empty, is the exact WWW-Authenticate header
	// value returned with 401 Unauthorized when a request to the MCP
	// endpoint carries no credential any configured authenticator accepts.
	// Per the MCP authorization specification the value points OAuth
	// clients at the Protected Resource Metadata document via its
	// resource_metadata parameter. The embedder composes the value; qrdb
	// only emits it (see WithBearerChallenge).
	bearerChallenge string

	// protectedResourceMetadata, when non-nil, is the RFC 9728 Protected
	// Resource Metadata JSON document served UNAUTHENTICATED at
	// ProtectedResourceMetadataPath. The embedder supplies the document;
	// qrdb performs no OAuth processing of its own.
	protectedResourceMetadata []byte

	// maxConnections bounds the number of simultaneously open TCP connections
	// to the /mcp listener. Zero means DefaultMaxConnections; see
	// WithMaxConnections.
	maxConnections int

	// maxBodyBytes bounds every authenticated request body, overriding
	// maxRequestBodyBytes when positive. Zero uses the package default.
	// See WithMaxBodyBytes.
	maxBodyBytes int64

	// maxConnsPerPeer bounds the number of concurrent TCP connections a
	// single remote IP may hold open against this server's listener.
	// Zero means unlimited. See WithMaxConnsPerPeer.
	maxConnsPerPeer int
}

// DefaultMaxConnections bounds simultaneously open connections to the MCP
// listener when WithMaxConnections is not used. Each accepted connection can
// read a full request body up to requestBodyLimit before authenticating
// (go-qrdb#576), so an unbounded connection count turns a request-body bound
// into only a per-connection one; this caps the aggregate.
const DefaultMaxConnections = 1024

// ProtectedResourceMetadataPath is the well-known path at which the
// embedder-supplied RFC 9728 Protected Resource Metadata document is served
// when WithProtectedResourceMetadata is configured.
const ProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource"

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithMaxBodyBytes bounds every authenticated request body this server
// reads. n <= 0 leaves the package default (maxRequestBodyBytes) in
// force; there is no way to request an unbounded body.
func WithMaxBodyBytes(n int64) ServerOption {
	return func(s *Server) { s.maxBodyBytes = n }
}

// WithMaxConnsPerPeer bounds the number of concurrent TCP connections a
// single remote IP may hold open against this server's listener. n <= 0
// means unlimited.
func WithMaxConnsPerPeer(n int) ServerOption {
	return func(s *Server) { s.maxConnsPerPeer = n }
}

// WithTLS configures the server with TLS. The config is cloned and pinned to
// a TLS 1.3 floor: an unset MinVersion is raised to TLS 1.3, and an explicitly
// weaker MinVersion is rejected (surfaced by Start as *WeakTLSVersionError)
// rather than silently negotiating a weaker handshake. This mirrors the gRPC
// and unix transports so the "TLS 1.3 only" posture holds on every transport.
// A nil cfg leaves TLS unconfigured; because the transport is TLS-only, Start
// then fails closed with *TLSNotConfiguredError.
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
		s.tlsConfig = clienttrust.ReassertListenerTLS(cfg, func(c *tls.Config) {
			if c.MinVersion < tls.VersionTLS13 {
				c.MinVersion = tls.VersionTLS13
			}
		})
	}
}

// WithMaxConnections overrides DefaultMaxConnections, the number of
// simultaneously open connections the /mcp listener admits.
func WithMaxConnections(n int) ServerOption {
	return func(s *Server) {
		s.maxConnections = n
	}
}

// WithAuthMiddleware configures authentication and per-tool authorization for
// the MCP server. When set, every tool call is authenticated (mTLS peer
// identity, injected by the HTTP context func) and authorized against the
// tool's required permission (declared with the tool at registration time in
// the toolPerms registry, DEFAULT-DENY). When nil, the server runs
// unauthenticated — matching the other transports' behavior when no
// middleware is supplied.
func WithAuthMiddleware(mw *authmw.AuthMiddleware) ServerOption {
	return func(s *Server) {
		s.authMW = mw
	}
}

// WithBearerChallenge configures the exact WWW-Authenticate header value the
// MCP endpoint returns with 401 Unauthorized when a request carries no
// credential any configured authenticator accepts — the discovery entry
// point of the MCP authorization flow. Per the specification the embedder
// composes the value to point clients at its Protected Resource Metadata
// document, e.g.:
//
//	Bearer realm="mcp", resource_metadata="https://host/.well-known/oauth-protected-resource"
//
// qrdb implements NO OAuth flow of its own: the challenge value, the
// metadata document (WithProtectedResourceMetadata), and the bearer-token
// validator (an auth.Authenticator passed through
// wiring.AuthMiddlewareConfig.ExtraAuthenticators, reading the raw header
// via auth.HTTPAuthorizationFromContext) are all supplied by the embedder.
// Without this option an unauthenticated request proceeds with no identity
// and is denied per-tool by the DEFAULT-DENY authorization middleware,
// preserving the pre-OAuth behavior for mTLS-only deployments.
func WithBearerChallenge(challenge string) ServerOption {
	return func(s *Server) {
		s.bearerChallenge = challenge
	}
}

// WithProtectedResourceMetadata serves the supplied RFC 9728 Protected
// Resource Metadata JSON document, unauthenticated, at
// ProtectedResourceMetadataPath. The document is the embedder's: it names
// the authorization servers, supported scopes, and resource identifier of
// the deployment embedding qrdb. Serving it unauthenticated is required by
// the discovery flow — it is public metadata, not a protected resource.
func WithProtectedResourceMetadata(doc []byte) ServerOption {
	return func(s *Server) {
		if len(doc) == 0 {
			return
		}
		s.protectedResourceMetadata = append([]byte(nil), doc...)
	}
}

// WithMVCC registers MVCC key-value tools on the MCP server.
func WithMVCC(svc service.MVCCKVServicer) ServerOption {
	return func(s *Server) {
		s.mvccSvc = svc
	}
}

// WithTransaction registers transaction tools on the MCP server.
func WithTransaction(svc service.TransactionServicer) ServerOption {
	return func(s *Server) {
		s.txSvc = svc
	}
}

// WithBatch registers batch operation tools on the MCP server.
func WithBatch(svc service.BatchServicer) ServerOption {
	return func(s *Server) {
		s.batchSvc = svc
	}
}

// WithWatch registers watch tools on the MCP server.
func WithWatch(svc service.WatchServicer) ServerOption {
	return func(s *Server) {
		s.watchSvc = svc
	}
}

// WithLease registers lease management tools on the MCP server.
func WithLease(svc service.LeaseServicer) ServerOption {
	return func(s *Server) {
		s.leaseSvc = svc
	}
}

// WithLock registers distributed lock tools on the MCP server.
//
// This is the REGISTRY-LESS fallback, not the primary path. When a
// service.Registry is configured the lock servicer is resolved per
// (tenant, database) and this singleton is never consulted: a lock key
// carries no tenant segment, so a process-wide servicer built over the raw
// engines gives every tenant the same lock (go-qrdb#455).
func WithLock(svc service.LockServicer) ServerOption {
	return func(s *Server) {
		s.lockSvc = svc
	}
}

// WithIndex registers secondary index tools on the MCP server.
func WithIndex(svc service.IndexServicer) ServerOption {
	return func(s *Server) {
		s.indexSvc = svc
	}
}

// WithTimeSeries registers time-series tools on the MCP server.
func WithTimeSeries(svc service.TimeSeriesServicer) ServerOption {
	return func(s *Server) {
		s.tsSvc = svc
	}
}

// WithVector registers vector database tools on the MCP server.
func WithVector(svc service.VectorServicer) ServerOption {
	return func(s *Server) {
		s.vectorSvc = svc
	}
}

// WithEmbedding registers embedding tools on the MCP server.
func WithEmbedding(svc service.EmbeddingServicer) ServerOption {
	return func(s *Server) {
		s.embeddingSvc = svc
	}
}

// WithRAG registers RAG (Retrieval-Augmented Generation) tools on the MCP
// server. When nil, no RAG tools are registered.
func WithRAG(svc service.RAGServicer) ServerOption {
	return func(s *Server) {
		s.ragSvc = svc
	}
}

// WithUserService registers user management tools on the MCP server.
func WithUserService(svc user.UserServicer) ServerOption {
	return func(s *Server) {
		s.userSvc = svc
	}
}

// WithRoleService registers role management tools on the MCP server.
func WithRoleService(svc role.RoleServicer) ServerOption {
	return func(s *Server) {
		s.roleSvc = svc
	}
}

// WithCertProvider registers certificate management tools on the MCP server.
func WithCertProvider(prov cert.CertProvider) ServerOption {
	return func(s *Server) {
		s.certProv = prov
	}
}

// WithCertEnrollment registers the certificate issue/enrol and trust-anchor
// tools (qrdb_cert_issue, qrdb_cert_enroll, qrdb_trust_anchor_*).
func WithCertEnrollment(svc service.CertEnrollmentServicer) ServerOption {
	return func(s *Server) { s.certEnroll = svc }
}

// WithInvitations registers qrdb_user_invite on /mcp and serves the PUBLIC
// enrolment endpoint (EnrollPath) whose one tool redeems an invitation.
func WithInvitations(svc service.InvitationServicer) ServerOption {
	return func(s *Server) { s.invitations = svc }
}

// WithTenantOnboarding wires the service behind qrdb_tenant_create.
func WithTenantOnboarding(svc service.TenantOnboardingServicer) ServerOption {
	return func(s *Server) { s.onboarding = svc }
}

// WithCollectionService registers collection management tools on the MCP server.
func WithCollectionService(svc service.CollectionServicer) ServerOption {
	return func(s *Server) {
		s.collectionSvc = svc
	}
}

// WithDatabaseService registers database management tools on the MCP server.
func WithDatabaseService(svc service.DatabaseServicer) ServerOption {
	return func(s *Server) {
		s.dbSvc = svc
	}
}

// WithBarrierService registers barrier lifecycle tools on the MCP server.
func WithBarrierService(svc service.BarrierServicer) ServerOption {
	return func(s *Server) {
		s.barrierSvc = svc
	}
}

// WithEncryptionMigrator registers the qrdb_barrier_enable_encryption tool
// on the MCP server, exposing BarrierEnableEncryption the same way it is
// exposed over REST, gRPC, QUIC, and Unix.
func WithEncryptionMigrator(m service.EncryptionMigrator) ServerOption {
	return func(s *Server) {
		s.encMigrator = m
	}
}

// WithSQLService registers the qrdb_sql_query tool on the MCP server for
// ad-hoc SQL execution against a named database.
func WithSQLService(svc service.SQLServicer) ServerOption {
	return func(s *Server) {
		s.sqlSvc = svc
	}
}

// WithPINBackend attaches the canonical pin.PINBackend used for the
// optional SO PIN / User PIN compliance-gating layer on barrier
// administrative operations.
//
// The backend is decorated with the SO PIN lockout (pin.NewLockoutBackend,
// idempotent: a backend the composer already decorated keeps its one set of
// counters), so a raw backend handed straight to this transport is still
// throttled.
func WithPINBackend(backend pin.PINBackend) ServerOption {
	backend = pin.NewLockoutBackend(backend)
	return func(s *Server) {
		s.pinBackend = backend
	}
}

// WithCeremonyService registers ceremony lifecycle tools on the MCP server.
func WithCeremonyService(svc service.CeremonyServicer) ServerOption {
	return func(s *Server) {
		s.ceremonySvc = svc
	}
}

// WithCredentialService registers credential management tools on the MCP server.
func WithCredentialService(svc service.CredentialServicer) ServerOption {
	return func(s *Server) {
		s.credentialSvc = svc
	}
}

// WithDEKService configures the tenant DEK keyring service. Without it the
// qrdb_dek_* tools are not registered.
func WithDEKService(svc service.DEKServicer) ServerOption {
	return func(s *Server) {
		s.dekSvc = svc
	}
}

// WithCeremonyCA configures the ceremony CA used for ceremony initialization.
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption {
	return func(s *Server) {
		s.ceremonyCA = ca
	}
}

// WithTenantService registers tenant management tools on the MCP server.
func WithTenantService(svc tenant.TenantServicer) ServerOption {
	return func(s *Server) { s.tenantSvc = svc }
}

// WithVolumeService registers the volume pool management tools
// (docs/volume-pools/api.md) on the MCP server.
func WithVolumeService(svc volume.Service) ServerOption {
	return func(s *Server) { s.volumeSvc = svc }
}

// WithBackupService registers backup management tools on the MCP server.
func WithBackupService(svc service.BackupServicer) ServerOption {
	return func(s *Server) { s.backupSvc = svc }
}

// WithRebalanceService registers cluster rebalance tools on the MCP server.
func WithRebalanceService(svc service.RebalanceServicer) ServerOption {
	return func(s *Server) { s.rebalanceSvc = svc }
}

// WithAuthFactorService registers auth factor tools on the MCP server.
// When set, the five qrdb_authfactor_* tools are available.
func WithAuthFactorService(svc service.AuthFactorServicer) ServerOption {
	return func(s *Server) { s.authFactorSvc = svc }
}

// WithUserAuthFactorService registers user-side auth factor tools on the
// MCP server (M14). When set, the qrdb_user_authfactor_* tools are
// available.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) ServerOption {
	return func(s *Server) { s.userAuthFactorSvc = svc }
}

// WithAuditLogger registers audit query tools on the MCP server.
// When set, the qrdb_audit_query tool is available.
func WithAuditLogger(l audit.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
}

// WithReplicatedAuditLogger registers the cluster-wide, per-tenant-opt-in
// replicated audit tools on the MCP server
// (docs/audit/architecture.md#cluster-wide-replicated-trail): the
// qrdb_audit_query tool's "replicated" parameter and the
// qrdb_audit_verify_replicated tool.
func WithReplicatedAuditLogger(l *service.OptInAuditLogger) ServerOption {
	return func(s *Server) { s.replicatedAudit = l }
}

// WithSOGate installs the SO dual-factor gate (authmw.SOGate) on every tool
// whose permission is in authmw.SOPermissions, after authorization: the caller
// must hold the SO role and present the SO PIN in the X-QRDB-SO-PIN header of
// the /mcp request (authmw.SOPINHeader), the same header REST reads. It is the
// same gate the REST transport applies, built by the orchestrator when
// auth.so_dual_factor is on; nil leaves it off.
func WithSOGate(gate *authmw.SOGate) ServerOption {
	return func(s *Server) { s.soGate = gate }
}

// WithSealedWindowProbe installs the sealed-window tool-handler middleware
// on the MCP server. While probe.IsSealed() returns true, every tool call
// not on SealedWindowAllowlist is rejected with a tool-result error, giving
// operators the same sealed-window envelope on MCP as on REST, gRPC, and
// native QUIC.
func WithSealedWindowProbe(probe sealedWindowProbe) ServerOption {
	return func(s *Server) {
		s.sealedWindowProbe = probe
		s.sealedWindowListener = nil
	}
}

// WithSealedWindowListener installs the boot-time sealed-window listener's
// gate backed by probe: the allowlist while the barrier is sealed, and a
// refusal of every request once it is unsealed -- a 503 at the HTTP layer
// and CodeUnavailable for a tool call -- sampled per request. That listener
// runs without authentication, so it must not serve anything against an
// unsealed barrier, even in the moment before it is stopped. It replaces
// WithSealedWindowProbe; the last of the two applied wins.
func WithSealedWindowListener(probe sealedwindow.ListenerProbe) ServerOption {
	return func(s *Server) {
		probe = sealedwindow.OrFailClosed(probe)
		s.sealedWindowProbe = probe
		s.sealedWindowListener = probe
	}
}

// WithAwaitingAdminCredentialProbe installs the awaiting-admin-credential
// gate (go-qrdb#548 review item 3): while probe reports awaiting, only the
// ceremony status and ceremony-initialize tools are served -- admitted with
// no authorization check at all, since there is no bound identity to
// authorize -- and every other tool call is refused with
// wire.CodeUnavailable.
func WithAwaitingAdminCredentialProbe(probe awaitingAdminCredentialProbe) ServerOption {
	return func(s *Server) { s.awaitingAdminCredentialProbe = probe }
}

// WithServiceRegistry configures the MCP server with a per-(tenant, database)
// service registry. When set, data-plane tool handlers resolve their servicer
// from the registry using the authenticated tenant and the optional "database"
// tool argument; when nil they use the singleton service fields. This gives the
// MCP transport the same per-database keyspace isolation as the REST, gRPC,
// QUIC, and Unix transports.
func WithServiceRegistry(reg service.Registry) ServerOption {
	return func(s *Server) { s.registry = reg }
}

// WithServiceModules registers ServiceModule instances (see pkg/apimodule)
// whose MCP tools are added to the shared mcp-go server after the built-in
// tool sets. This lets higher layers inject capabilities that reach the MCP
// transport the same way they already reach gRPC, unix, REST, and QUIC.
func WithServiceModules(mods []apimodule.ServiceModule) ServerOption {
	return func(s *Server) { s.modules = mods }
}

// WithKVService registers KV tools on the MCP server. This is an alternative
// to passing kvSvc as a positional parameter and is useful when constructing
// a server that handles only barrier, cert, or auth services.
func WithKVService(svc service.KVServicer) ServerOption {
	return func(s *Server) { s.kvSvc = svc }
}

// WithAdminService registers admin tools on the MCP server. This is an
// alternative to passing adminSvc as a positional parameter and is useful
// when constructing a server without a KV engine.
func WithAdminService(svc service.AdminServicer) ServerOption {
	return func(s *Server) { s.adminSvc = svc }
}

// NewServer creates a new MCP server that listens on the given address
// and delegates to the provided service implementations. kvSvc and adminSvc
// may be nil when only non-KV services (barrier, cert, auth) are needed;
// their tools are registered only when the respective service is non-nil.
func NewServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, addr string, opts ...ServerOption) *Server {
	s := &Server{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
		addr:     addr,
	}

	for _, opt := range opts {
		opt(s)
	}

	// Create the underlying MCP server with tool capabilities. When auth is
	// configured, install the per-tool authorization middleware so EVERY tool
	// call is authorized (default-deny) before its handler runs.
	// The tool middlewares, outermost first. mcp-go applies them in reverse
	// index order, so index 0 is the outermost. They are kept on the server
	// so SealedWindowTools can hand a host the same chain with each tool.
	//
	// FIRST, and therefore OUTERMOST: errorCodeMiddleware sees the errors
	// every middleware below it and every tool handler returns, and is what
	// gives each of them a JSON-RPC code (go-qrdb#438). It is registered
	// unconditionally — unlike the three gates below it, whose registration
	// depends on what the server was configured with — because a failure
	// classified on one deployment and opaque on another is the
	// inconsistency this transport has just stopped having.
	s.toolMiddlewares = []mcpserver.ToolHandlerMiddleware{errorCodeMiddleware()}
	// The awaiting-admin-credential gate (go-qrdb#548 review item 3) runs
	// BEFORE toolAuthzMiddleware so it can admit its two allowed tools with
	// NO authorization check at all -- there is no bound identity to
	// authorize while the node is in this boot state -- and refuse every
	// other tool before toolAuthzMiddleware, or the tool handler, ever runs.
	if s.awaitingAdminCredentialProbe != nil {
		s.toolMiddlewares = append(s.toolMiddlewares, s.awaitingAdminCredentialMiddleware())
	}
	if s.authMW != nil || s.soGate != nil {
		s.toolMiddlewares = append(s.toolMiddlewares, s.toolAuthzMiddleware())
	}
	if s.sealedWindowProbe != nil {
		s.toolMiddlewares = append(s.toolMiddlewares, s.sealedWindowMiddleware())
	}
	if s.registry != nil {
		// Bind the registry and the request's database scope into the per-call
		// context so data-plane tool handlers resolve per (tenant, database).
		s.toolMiddlewares = append(s.toolMiddlewares, s.databaseScopeMiddleware())
	}
	// Replica forwarding sits innermost: the caller is authenticated,
	// authorized and scoped before a refusal is forwarded on its behalf.
	if s.replicaForwarding != nil && s.tlsConfig != nil {
		s.forwarder = newReplicaForwarder(*s.replicaForwarding, s.tlsConfig)
		s.toolMiddlewares = append(s.toolMiddlewares, s.forwarder.middleware())
	}
	mcpOpts := []mcpserver.ServerOption{mcpserver.WithToolCapabilities(false)}
	for _, mw := range s.toolMiddlewares {
		mcpOpts = append(mcpOpts, mcpserver.WithToolHandlerMiddleware(mw))
	}
	// tools/list shows each caller what it may call (visibleTools).
	if s.authMW != nil {
		mcpOpts = append(mcpOpts, mcpserver.WithToolFilter(s.visibleTools))
	}
	s.mcpSrv = mcpserver.NewMCPServer(
		"qrdb",
		"1.0.0",
		mcpOpts...,
	)

	// toolPerms is populated by the addTools calls below: every built-in tool
	// declares its permission at the registration site, and AuthorizeMCP
	// consults the registry with DEFAULT-DENY semantics.
	s.toolPerms = authmw.NewMCPToolRegistry()

	// Register KV and admin tools only when the respective service is provided.
	if s.kvSvc != nil {
		s.addTools(kvTools(s.kvSvc)...)
	}
	if s.adminSvc != nil {
		s.addTools(adminTools(s.adminSvc)...)
	} else if s.sealedWindowProbe != nil {
		// A sealed window has no admin service, but health is on its
		// allowlist: answer "sealed", as the gRPC Health RPC does.
		s.addTools(sealedHealthTool())
	}

	// The raft-only families (MVCC, watch, lease, lock) are registered
	// UNCONDITIONALLY. A node composed without one -- every standalone node --
	// answers the call with the typed "not configured" refusal the other
	// transports give (Unimplemented, from which the SDK rebuilds
	// transport.CapabilityUnsupportedError) instead of "tool not found", so a
	// client sees one answer whichever transport it holds. The resolve*
	// helpers refuse when neither a registry nor a singleton is wired.
	s.addTools(mvccTools(s.mvccSvc)...)
	if s.txSvc != nil {
		s.addTools(transactionTools(s.txSvc)...)
	}
	if s.batchSvc != nil {
		s.addTools(batchTools(s.batchSvc)...)
	}
	s.addTools(watchTools(s.watchSvc)...)
	s.addTools(leaseTools(s.leaseSvc)...)
	// The lock servicer resolves per (tenant, database) at call time through
	// resolveLock (go-qrdb#455); unconditional for the reason above.
	s.addTools(lockTools(s.lockSvc)...)
	if s.indexSvc != nil {
		s.addTools(indexTools(s.indexSvc)...)
	}
	if s.tsSvc != nil {
		s.addTools(timeSeriesTools(s.tsSvc)...)
	}
	if s.vectorSvc != nil {
		s.addTools(vectorTools(s.vectorSvc)...)
	}
	if s.embeddingSvc != nil {
		s.addTools(embeddingTools(s.embeddingSvc)...)
	}
	if s.ragSvc != nil {
		s.addTools(ragTools(s.ragSvc)...)
	}
	if s.userSvc != nil {
		s.addTools(userTools(s.userSvc, s.userAuthFactorSvc, s.authMW, s.roleSvc)...)
	}
	if s.roleSvc != nil && s.userSvc != nil {
		s.addTools(roleTools(s.roleSvc, s.userSvc, s.authMW)...)
	}
	if s.certProv != nil {
		s.addTools(certTools(s.certProv, s.authMW)...)
	}
	if s.certEnroll != nil {
		s.addTools(enrollmentTools(s.certEnroll, s.authMW)...)
	}
	if s.invitations != nil {
		s.addTools(invitationTools(s.invitations, s.authMW, s.roleSvc)...)
		s.enrollSrv = s.newEnrollServer()
	}
	// Registered when either the singleton is wired or a registry is
	// configured: the per-(tenant, database) collection servicer resolves at
	// call time through resolveCollection, so gating on the singleton alone
	// would omit the tools entirely from a multi-tenant server that resolves
	// them per request.
	if s.collectionSvc != nil || s.registry != nil {
		s.addTools(collectionTools(s.collectionSvc)...)
	}
	if s.dbSvc != nil {
		s.addTools(databaseTools(s.dbSvc)...)
	}
	if s.barrierSvc != nil {
		s.addTools(barrierTools(s.barrierSvc, s.pinBackend, s.encMigrator)...)
	} else {
		// No barrier (seal disabled, or a composer that never wired one): the
		// rest of the barrier tool group has nothing to operate on, but
		// enable-encryption's own availability depends on encMigrator, not
		// barrierSvc, so register it independently instead of omitting it
		// along with the whole group (go-qrdb#558) -- matching
		// REST/gRPC/Unix/QUIC, which never depended on a barrier service
		// existing to expose this endpoint. Registered even when s.encMigrator
		// is ALSO nil (standalone mode with seal disabled): the handler's own
		// nil check is what answers with the typed
		// service.ErrMigrationProviderUnavailable refusal then, rather than
		// this tool being absent and the caller seeing a bare "tool not
		// found" instead of that typed error.
		s.addTools(serverTool{
			Tool:    toolBarrierEnableEncryption(),
			Perm:    permission.SealEnableEncryption,
			Handler: handleBarrierEnableEncryption(s.encMigrator),
		})
	}
	if s.ceremonySvc != nil {
		s.addTools(ceremonyTools(s.ceremonySvc, s.ceremonyCA)...)
	}
	if s.dekSvc != nil {
		s.addTools(dekTools(s.dekSvc)...)
	}
	if s.credentialSvc != nil {
		s.addTools(credentialTools(s.credentialSvc)...)
	}
	if s.tenantSvc != nil {
		s.addTools(tenantTools(s.tenantSvc, s.onboarding, s.authMW)...)
		s.addTools(tenantBarrierTools(s.tenantSvc, tenantBarrierAuthorizers{
			Op:        s.authorizeTenantBarrierOp,
			InitBegin: s.beginTenantBarrierInit,
			Init:      s.authorizeTenantBarrierInit,
			Sealed:    s.authorizeTenantBarrierSealedOp,
		})...)
	}
	if s.volumeSvc != nil {
		s.addTools(volumePoolTools(s.volumeSvc)...)
	}
	if s.backupSvc != nil {
		s.addTools(backupTools(s.backupSvc)...)
	}
	if s.rebalanceSvc != nil {
		s.addTools(rebalanceTools(s.rebalanceSvc)...)
	}
	if s.authFactorSvc != nil {
		s.addTools(authFactorTools(s.authFactorSvc)...)
	}
	if s.userAuthFactorSvc != nil {
		s.addTools(userAuthFactorTools(s.userAuthFactorSvc, s.userSvc)...)
	}
	// Audit query tool.
	// Authorization: requires "audit:query" permission.
	// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
	if s.auditLogger != nil || s.replicatedAudit != nil {
		s.addTools(auditTools(s.auditLogger, s.replicatedAudit)...)
	}
	if s.sqlSvc != nil {
		s.addTools(sqlTools(s.sqlSvc)...)
	}

	// Register module tools last so they layer on top of the built-in tool
	// sets on the same real mcp-go server. A module that implements
	// MCPToolPermissioner declares each tool's permission, which is fed into
	// the same toolPerms registry the built-in tools use — one authorization
	// path for every tool on the server. A module tool registered WITHOUT a
	// declaration stays fail-closed: when auth middleware is configured,
	// AuthorizeMCP default-denies any tool absent from the registry.
	for _, m := range s.modules {
		m.RegisterMCP(s.mcpSrv)
		if tp, ok := m.(apimodule.MCPToolPermissioner); ok {
			for name, perm := range tp.MCPToolPermissions() {
				s.toolPerms.Register(name, perm)
			}
		}
	}
	// Public tools are declared last, once every tool carries its
	// permission, so a declared tool that reaches data is refused.
	s.publicToolsErr = s.registerPublicTools()

	return s
}

// Start begins listening and serving MCP requests over HTTP. The MCP
// Streamable HTTP handler is mounted at /mcp. When TLS is configured,
// the listener is wrapped with tls.NewListener.
func (s *Server) Start() error {
	// A WithTLS config with an explicitly weak MinVersion was rejected at
	// option-application time; surface it now.
	if s.tlsErr != nil {
		return s.tlsErr
	}
	if s.publicToolsErr != nil {
		return s.publicToolsErr
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

	// Bound the number of simultaneously open connections BEFORE anything
	// else touches them: an unauthenticated caller can hold a connection open
	// without ever completing a request, and the request-body bound below
	// only limits what ONE connection can cost (go-qrdb#576).
	maxConns := s.maxConnections
	if maxConns == 0 {
		maxConns = DefaultMaxConnections
	}
	ln = netutil.LimitListener(ln, maxConns)

	// Per-peer connection cap: previously the only thing bounding how many
	// connections a single remote IP could hold open against this
	// listener was the process's open-file-descriptor limit -- a GLOBAL,
	// not per-peer, ceiling that one abusive peer can exhaust on behalf of
	// every other caller (go-qrdb#576).
	if s.maxConnsPerPeer > 0 {
		ln = connlimit.New(ln, s.maxConnsPerPeer)
	}

	// Report every accepted connection to the modules that observe them.
	// Wrapped BENEATH the TLS listener, so http.Server still sees a *tls.Conn.
	ln = apimodule.ConnObserversOf(s.modules).Listener(ln, apimodule.TransportMCP)
	ln = tls.NewListener(ln, s.tlsConfig)
	s.listener = ln

	// Create the Streamable HTTP handler from the MCP server. The HTTP
	// context func is installed UNCONDITIONALLY (go-qrdb#560 review C):
	// authHTTPContext's remote-address injection must run even with NO
	// AuthMiddleware and NO SO gate configured, because a node with auth
	// entirely disabled still runs the SO PIN gate on ceremony-initialize's
	// admin-credential retry, which needs a per-source identity for
	// auth.CallerPrincipal's lockout regardless of whether anything else
	// here authenticates the caller. authHTTPContext itself remains a
	// no-op for authentication purposes when s.authMW is nil (its own
	// early return), so this adds no behavior beyond that injection.
	streamOpts := []mcpserver.StreamableHTTPOption{mcpserver.WithHTTPContextFunc(s.authHTTPContext)}
	streamHandler := mcpserver.NewStreamableHTTPServer(s.mcpSrv, streamOpts...)

	// The MCP endpoint handler, from the outside in:
	//
	//   - the boot-time sealed-window listener's gate, when this is one;
	//   - authentication, whenever it is configured: an unauthenticated
	//     request is answered 401 (with WWW-Authenticate when a bearer
	//     challenge is configured) BEFORE mcp-go reads anything, so not even
	//     initialize or tools/list is answered anonymously -- unless a module
	//     declares public tools, when exactly those calls, the initialize
	//     handshake and a tools/list filtered to them are. A request that
	//     authenticates proceeds with its identity bound into the request
	//     context, where authHTTPContext reuses it;
	//   - the request-body bound, on every request: 1 MiB when the caller is
	//     anonymous (no authentication, or the sealed-window listener), the
	//     batch-import bound otherwise.
	mcpHandler := boundedBodyHandler(s.requestBodyLimit(), streamHandler)
	if s.authMW != nil {
		mcpHandler = s.authenticateHandler(mcpHandler)
	}
	if s.sealedWindowListener != nil {
		mcpHandler = s.sealedWindowClosedHandler(mcpHandler)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	if s.enrollSrv != nil {
		// PUBLIC by design (EnrollPath): an invited user holds no credential
		// yet. One tool, the anonymous body bound, and the sealed-window gate
		// when this is a sealed listener.
		//
		// Redemption is throttled per source: the connection's own address.
		// A minimal, dedicated context func -- not s.authHTTPContext -- so
		// this public path never runs the main server's auth chain.
		peerAddr := mcpserver.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return auth.WithRemoteAddr(ctx, r.RemoteAddr)
		})
		enrollHandler := boundedBodyHandler(maxAnonymousRequestBodyBytes, mcpserver.NewStreamableHTTPServer(s.enrollSrv, peerAddr))
		if s.sealedWindowListener != nil {
			enrollHandler = s.sealedWindowClosedHandler(enrollHandler)
		}
		mux.Handle(EnrollPath, enrollHandler)
	}
	if len(s.protectedResourceMetadata) > 0 {
		// RFC 9728 discovery document: public by design (see
		// WithProtectedResourceMetadata).
		mux.HandleFunc(ProtectedResourceMetadataPath, s.protectedResourceMetadataHandler)
	}

	s.httpSrv = &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	proto := "HTTP"
	if s.tlsConfig != nil {
		proto = "HTTPS"
	}
	slog.Info("MCP server started", "addr", ln.Addr().String(), "proto", proto)

	go func() {
		if serveErr := s.httpSrv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("MCP server error", "error", serveErr)
		}
	}()

	return nil
}

// Stop gracefully shuts down the MCP server.
func (s *Server) Stop() {
	if s.httpSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		slog.Error("MCP server shutdown error", "error", err)
	}
	if s.forwarder != nil {
		s.forwarder.close()
	}
	slog.Info("MCP server stopped")
}

// Addr returns the listener address. Useful for tests that use port 0.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// MCPServer returns the underlying mcp-go MCPServer. This is useful for
// testing tool registration and for advanced integrations.
func (s *Server) MCPServer() *mcpserver.MCPServer {
	return s.mcpSrv
}
