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

// Package node provides turnkey builders for embedding QRDB as either a
// standalone (engine-direct, no Raft state machine) or a clustered (full
// Raft consensus) node.
//
// # Standalone mode — NewStandaloneNode
//
// The standalone builder creates a storage backend and MVCC/KV engine
// directly under a LocalBackend. There is no quicraft host, no state
// machine, no StartShard. This honours the architecture rule that standalone
// nodes are engine-direct (service.Host is pkg/backend.LocalBackend).
//
//	n, err := node.NewStandaloneNode(ctx, cfg,
//	    server.WithStrategyRegistry(reg),
//	    server.WithPINBackend(pin),
//	)
//
// # Clustered mode — NewClusteredNode
//
// The clustered builder creates a quicraft.Host, starts a Raft shard with
// a subsystem-aware MVCC state machine factory, wraps the host with
// pkg/backend/raft.HostAdapter to satisfy service.Host, and proceeds with
// the same lifecycle.BuildServices + server.New tail as the standalone path.
//
//	n, err := node.NewClusteredNode(ctx, cfg, members,
//	    server.WithStrategyRegistry(reg),
//	)
//
// Both builders return a *Node that exposes:
//
//   - Server() *server.Server  — the started multi-protocol server.
//   - Client() *sdk.Client     — an in-process SDK client connected to the node.
//   - Close() error             — tears everything down in reverse order.
//
// # Seal lifecycle — barrier → unseal → BuildServices
//
// When cfg.Seal.Enabled is true the builders follow the daemon's ordering:
//
//  1. Build storage + seal subsystem (barrier, credential service, barrier
//     registry) BEFORE BuildServices. This mirrors the daemon's
//     initSealSubsystem phase.
//  2. Attempt auto-unseal:
//     - StrategyNone: no credentials required; barrier immediately clear.
//     - Passphrase strategy + WithSealPassphrase supplied: unseal with creds.
//     - Hardware strategies (KMS, Vault, TPM2, PKCS#11): HardwareBacked()
//     returns true; attempt unseal with empty credentials (the strategy
//     retrieves the key internally from hardware).
//  3. If unsealed → run lifecycle.BuildServices + server.New as usual
//     (full node returned).
//  4. If STILL sealed (Shamir, or passphrase without WithSealPassphrase):
//     return a node in sealed state.  Seal-only services (BarrierService,
//     CeremonyService, CredentialService) are reachable via Services() so
//     callers can drive the unseal API.  Call node.Unseal(ctx, creds) to
//     complete the boot once the barrier is unsealed by the operator.
//
// Passphrase injection — use WithSealPassphrase when cfg.Seal.Enabled is
// true and you want the barrier auto-unsealed on startup:
//
//	n, err := node.NewStandaloneNode(ctx, cfg,
//	    node.WithSealPassphrase([]byte("my-secret")),
//	)
//
// Hardware crypto options (WithStrategyRegistry, WithPINBackend,
// WithFactorRegistry) are passed as server.ServerOption and forwarded to
// server.New so embedders retain full composition control.
package node

import (
	"context"
	"crypto/fips140"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// WithAuthenticators appends embedder-supplied authenticators to the node's
// auth middleware chain, AFTER the built-in mTLS authenticator
// (first-success-wins, so mTLS peers keep their existing behavior and
// requests without a client certificate fall through to these). This is the
// extension seam for credential schemes qrdb deliberately does not
// implement itself — go-quicbaas passes its OAuth 2.1 bearer-token
// validator here when embedding qrdb, per the MCP authorization
// specification; the raw Authorization header reaches the authenticator
// via auth.HTTPAuthorizationFromContext on the HTTP transports (REST, MCP).
func WithAuthenticators(authenticators ...auth.Authenticator) NodeOption {
	return func(o *nodeOptions) {
		o.extraAuthenticators = append(o.extraAuthenticators, authenticators...)
	}
}

// WithAuthorizerWrapper decorates the node's fully built RBAC[+ABAC]
// authorizer with embedder-supplied authorization logic. The wrapper
// receives the inner authorizer and returns the one every transport will
// use — the SAME instance reaches the embedded transport and the network
// transports' auth middleware, so custom logic (OAuth scope checks,
// external policy engines) composes over, and can defer to, the built-in
// stack everywhere at once. A wrapper returning nil aborts node
// construction (fail closed).
func WithAuthorizerWrapper(wrap func(inner auth.Authorizer) auth.Authorizer) NodeOption {
	return func(o *nodeOptions) { o.authorizerWrapper = wrap }
}

// ---------------------------------------------------------------------------
// Node options
// ---------------------------------------------------------------------------

// nodeOptions holds configuration knobs that are not expressed in
// *qrdbconfig.Config (e.g. runtime secrets that must never be persisted).
type nodeOptions struct {
	// sealPassphrase is the passphrase used to auto-unseal the software
	// barrier on startup. Only consulted when cfg.Seal.Enabled is true and
	// the strategy is passphrase-based.
	sealPassphrase []byte

	// sealStrategy is a pre-built hardware/cloud sealing strategy injected by
	// the caller (e.g. go-quicbaas XKMSCryptoOptions). When non-nil it is
	// used by buildNodeSealSubsystem instead of the built-in deferred-software
	// fallback, enabling cloud KMS and HSM strategies (awskms, vault, tpm2,
	// pkcs11) to seal and auto-unseal the barrier without operator interaction.
	// The caller is responsible for constructing the strategy from its config
	// before calling NewStandaloneNode / NewClusteredNode.
	sealStrategy seal.SealingStrategy

	// logger overrides the default slog.Default() logger.
	logger *slog.Logger

	// raftMTLS provides the PEM-encoded certificate material for mutual TLS
	// on the Raft transport. When non-nil, it is injected into the
	// quicraft HostConfig.TransportConfig.MTLSConfig so inter-node
	// connections use the caller-supplied CA and per-node certificate.
	// Ignored for single-node clusters that use WithoutTransport.
	raftMTLS *quicraftconfig.MTLSConfig

	// noSyncWrites disables fsync on PebbleDB batch commits in the MVCC
	// state machine. When true, Update() does not wait for OS-level write
	// synchronisation. Use only in testing environments where overlay2/Docker
	// fsync latency causes Raft proposal timeouts. Never use in production.
	noSyncWrites bool

	// extraAuthenticators are embedder-supplied authenticators appended to
	// the auth middleware's chain after the built-in mTLS authenticator.
	// See WithAuthenticators.
	extraAuthenticators []auth.Authenticator

	// authorizerWrapper decorates the node's fully built RBAC[+ABAC]
	// authorizer with embedder-supplied logic. See WithAuthorizerWrapper.
	authorizerWrapper func(inner auth.Authorizer) auth.Authorizer

	// sessionIssuer mints/resolves/revokes the opaque bearer tokens issued
	// by the unified-login ceremony. When non-nil it is forwarded to the
	// embedded transport via embedded.WithSessionIssuer so successful logins
	// produce real session bearers. qrdb has no native session store; the
	// issuer is owned by the surrounding composer (e.g. go-quicbaas).
	sessionIssuer embedded.SessionIssuer

	// loginFactorVerifiers holds the per-type hardware verification adapters
	// (PIV, FIDO2, PKCS#11, ...) injected by the composer. Each entry is
	// forwarded to the embedded transport via embedded.WithFactorVerifier,
	// which keys the verifier by its Type() (last-write-wins per type).
	loginFactorVerifiers []embedded.FactorVerifier

	// tenantIsolation enables the IsolationVolume tenant stack. When true,
	// completeStandaloneBoot calls server.BuildTenantInfra to restore volume
	// tenants and wire the TenantHostRegistry into the multi-protocol server and
	// the scoped encryption migrator. Default false (no behavior change for
	// existing embedders).
	tenantIsolation bool

	// defaultTenantEntitlements are the operator-configured default feature
	// entitlements injected into lifecycle.Infra so the built-in TenantService
	// allows the isolation modes and features needed by the embedder. When nil
	// the TenantService uses quota.DefaultEntitlements (prefix isolation only).
	defaultTenantEntitlements *quota.Entitlements

	// abacPolicies are caller-supplied ABAC policies that decorate the RBAC
	// authorizer built in buildEmbeddedOpts. When both abacPolicies and
	// abacPolicyDir are empty the RBAC authorizer is used undecorated.
	abacPolicies []*abac.Policy

	// abacPolicyDir is a directory of YAML ABAC policy files loaded when the
	// embedded RBAC authorizer is constructed. Load or compile failures
	// abort the boot (fail closed).
	abacPolicyDir string

	// abacLiveReload forces the ABAC decorator to be constructed (and its
	// Reload seam exposed via Node.ABACPolicyReloader) even when neither
	// abacPolicies nor abacPolicyDir is set, so an embedder can add its
	// first policy after boot. See WithABACLiveReload.
	abacLiveReload bool
}

// NodeOption is a functional option for NewStandaloneNode / NewClusteredNode.
type NodeOption func(*nodeOptions)

// WithSealPassphrase injects the passphrase used to auto-unseal the software
// seal barrier during node construction. The caller is responsible for
// wiping the slice after calling the constructor. This option is only
// consulted when cfg.Seal.Enabled is true.
func WithSealPassphrase(passphrase []byte) NodeOption {
	return func(o *nodeOptions) { o.sealPassphrase = passphrase }
}

// WithSealStrategy injects a pre-built hardware or cloud sealing strategy
// into the node builder so buildNodeSealSubsystem uses it instead of the
// default deferred-software (passphrase) fallback. This is the injection
// point for cloud KMS strategies (awskms, gcpkms, azurekv, vault) and
// hardware strategies (tpm2, pkcs11) constructed by higher-level composers
// such as go-quicbaas/go-xkms. The strategy is registered as the sole
// entry in the barrier's preference order; HardwareBacked strategies
// auto-unseal on startup. The option is only consulted when
// cfg.Seal.Enabled is true; when nil the built-in strategy selection
// (none → NoneStrategy, shamir → ShamirStrategy, default → deferred-
// software) applies unchanged.
func WithSealStrategy(strategy seal.SealingStrategy) NodeOption {
	return func(o *nodeOptions) { o.sealStrategy = strategy }
}

// WithLogger overrides the node's structured logger.
func WithLogger(log *slog.Logger) NodeOption {
	return func(o *nodeOptions) { o.logger = log }
}

// RaftMTLSConfig is the mTLS material consumed by WithRaftMTLS. It is a
// re-export of the underlying quicraft transport type so SDK consumers
// (go-xkms, go-quicbaas, go-trusted-platform) never import
// go-quicraft/pkg/config directly — the qrdb SDK is the single boundary.
type RaftMTLSConfig = quicraftconfig.MTLSConfig

// WithRaftMTLS injects PEM-encoded mTLS certificate material into the
// quicraft Raft transport. It is only consulted for multi-node clustered
// nodes (len(members) > 1); single-node clusters use WithoutTransport and
// require no TLS material. Callers are responsible for loading the PEM
// bytes from the operator-supplied certificate files.
func WithRaftMTLS(mtls *RaftMTLSConfig) NodeOption {
	return func(o *nodeOptions) { o.raftMTLS = mtls }
}

// WithNoSyncWrites disables fsync on PebbleDB batch commits in the MVCC
// state machine. When set, the state machine's Update does not wait for
// OS-level write synchronisation, significantly reducing commit latency.
// Use only in testing environments where filesystem fsync latency would
// otherwise cause Raft proposal timeouts. Never use in production.
func WithNoSyncWrites() NodeOption {
	return func(o *nodeOptions) { o.noSyncWrites = true }
}

// WithSessionIssuer injects the SessionIssuer that the unified-login
// ceremony uses to mint, resolve, and revoke opaque bearer tokens after a
// factor proof succeeds. It is forwarded to the in-process embedded
// transport via embedded.WithSessionIssuer. qrdb owns no session store, so
// without an issuer the login ceremony can resolve the principal/factor but
// cannot return a bearer; supply one when the node must hand out sessions
// (production composers such as go-quicbaas always do). The option is
// nil-safe: a nil issuer is ignored.
func WithSessionIssuer(s embedded.SessionIssuer) NodeOption {
	return func(o *nodeOptions) { o.sessionIssuer = s }
}

// WithLoginFactorVerifier registers a hardware FactorVerifier (PIV, FIDO2,
// PKCS#11, ...) with the unified-login ceremony. The verifier is forwarded
// to the embedded transport via embedded.WithFactorVerifier, which keys it
// by its Type(); registering two verifiers for the same type is
// last-write-wins. Call the option once per factor type. A nil verifier is
// ignored.
func WithLoginFactorVerifier(v embedded.FactorVerifier) NodeOption {
	return func(o *nodeOptions) {
		if v == nil {
			return
		}
		o.loginFactorVerifiers = append(o.loginFactorVerifiers, v)
	}
}

// WithTenantIsolation enables IsolationVolume tenant support on the node.
// When set, completeStandaloneBoot calls server.BuildTenantInfra to restore
// persisted volume tenants and wires the TenantHostRegistry into the
// multi-protocol server and the scoped encryption migrator so per-tenant
// enable-encryption reaches the right recycle path. Default false: no
// behavior change for existing embedders. Requires cfg.Seal.Enabled and at
// least one configured volume pool.
func WithTenantIsolation() NodeOption {
	return func(o *nodeOptions) { o.tenantIsolation = true }
}

// WithDefaultTenantEntitlements injects the operator-configured default
// feature entitlements into the node's built-in TenantService. This
// overrides the conservative quota.DefaultEntitlements (prefix isolation
// only) so the embedder can permit IsolationVolume, dedicated volume
// storage, system-managed barriers, and other enterprise features without
// standing up a separate TenantService. A nil argument is accepted (no-op).
func WithDefaultTenantEntitlements(ent *quota.Entitlements) NodeOption {
	return func(o *nodeOptions) { o.defaultTenantEntitlements = ent }
}

// WithABACPolicies injects programmatically constructed ABAC policies that
// decorate the node's RBAC authorizer (abac.Decorate). Explicit denies
// override RBAC allows, explicit allows grant beyond RBAC, and unmatched
// requests delegate to RBAC. May be combined with WithABACPolicyDir.
func WithABACPolicies(policies []*abac.Policy) NodeOption {
	return func(o *nodeOptions) { o.abacPolicies = append(o.abacPolicies, policies...) }
}

// WithABACPolicyDir loads ABAC policies from a directory of YAML files
// (*.yaml, *.yml) when the node's RBAC authorizer is constructed. Load or
// compile failures abort the boot. May be combined with WithABACPolicies.
func WithABACPolicyDir(dir string) NodeOption {
	return func(o *nodeOptions) { o.abacPolicyDir = dir }
}

// WithABACLiveReload guarantees the node's ABAC decorator supports
// Node.ABACPolicyReloader / Decorator.Reload even when the node is
// constructed with neither WithABACPolicies nor WithABACPolicyDir. Without
// this option, a node built with zero initial ABAC policies runs the RBAC
// authorizer undecorated (matching prior behavior) and
// ABACPolicyReloader returns nil, since there is nothing to reload into.
// Set this when a downstream policy store (e.g. a policy CRUD API) will
// call Node.ABACPolicyReloader().Reload after boot, including to add the
// very first policy.
func WithABACLiveReload() NodeOption {
	return func(o *nodeOptions) { o.abacLiveReload = true }
}

// ---------------------------------------------------------------------------
// nodeSealSubsystem
// ---------------------------------------------------------------------------

// nodeSealSubsystem holds the three seal-layer objects that must be built
// before data-plane storage when cfg.Seal.Enabled is true. Mirrors the
// daemon's sealSubsystem (cmd/qrdbd/seal_subsystem.go) but lives in the
// node package for use by the node builders.
type nodeSealSubsystem struct {
	barrier         *seal.StorageBarrier
	credCoreSvc     *credentials.CredentialService
	barrierRegistry *seal.BarrierRegistry
	sealStore       *pebbleStorage.PebbleStorage
	sealDataDir     string
	// volumePool is non-nil when the config declares at least one volume pool.
	// It is closed during node shutdown after the per-tenant registry is
	// drained, mirroring the daemon's shutdown ordering.
	volumePool *qrdbpool.VolumePoolSubsystem
}

// close releases the seal store and the volume-pool state store (in that
// order, matching the daemon's shutdown path). The per-tenant registry is
// closed separately via closeVolumePool so callers can log progress.
// Safe to call when sealStore is nil.
func (s *nodeSealSubsystem) close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	if s.sealStore != nil {
		firstErr = s.sealStore.Close()
		s.sealStore = nil
	}
	// Guard the call site for parity with Node.Close: volumePool is nil when no
	// volume pools are configured. VolumePoolSubsystem.Close already nil-guards
	// its receiver, so this is defensive consistency, not a correctness fix.
	if s.volumePool != nil {
		if err := s.volumePool.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ---------------------------------------------------------------------------
// pendingStandaloneboot / pendingClusteredBoot
// ---------------------------------------------------------------------------

// pendingStandaloneBoot holds the partially-constructed standalone infra
// that completeBoot needs to finish wiring after the barrier is unsealed.
type pendingStandaloneBoot struct {
	backendType database.BackendType
	dataDir     string
	serverOpts  []server.ServerOption
	cfg         *qrdbconfig.Config
}

// pendingClusteredBoot holds the partially-constructed clustered infra that
// completeClusteredBoot needs to finish wiring after the barrier is unsealed.
type pendingClusteredBoot struct {
	members      map[uint64]string
	serverOpts   []server.ServerOption
	cfg          *qrdbconfig.Config
	raftMTLS     *quicraftconfig.MTLSConfig
	noSyncWrites bool
}

// ---------------------------------------------------------------------------
// Node
// ---------------------------------------------------------------------------

// Node holds a fully initialised and started QRDB node. Call Close to
// release all resources in reverse order.
//
// When the seal barrier is enabled but the strategy requires operator input
// (Shamir or passphrase without WithSealPassphrase), the node is returned in
// a sealed state: IsSealed returns true, the data-plane services are absent,
// and only the seal-layer services (BarrierService, CeremonyService,
// CredentialService) are reachable via Services(). Call Unseal to complete
// the boot once the operator has driven the barrier unsealed.
//
// # Accessor concurrency
//
// # Accessor reads are race-free; node LIFECYCLE is not synchronised
//
// Read this before relying on either half. The two are deliberately separated
// because only the first is a guarantee.
//
// What is guaranteed. Every no-argument exported accessor — Client, Server,
// Services, KVService, AdminService, RebalanceService, UserService,
// AuthFactorService, UserAuthFactorService, ABACPolicyReloader,
// BarrierRegistry, IsSealed — performs a RACE-FREE READ from any goroutine at
// any point. It returns a fully constructed value or nil, never a torn or
// unsynchronised one. That is what the atomic.Pointer fields below buy.
//
// This matters because a sealed node is handed to the embedder BEFORE its data
// plane exists, and each data-plane accessor documents a nil return for exactly
// that window (Services is the near-exception — it returns the seal-only
// lifecycle.Node until the boot republishes it with the full graph), which is
// an invitation to poll it while a second goroutine is inside Unseal: the
// sealed-window shape every embedder uses. go-qrdb#176 measured the
// alternative: `Read at Client()` vs `Previous write at
// wireClientAndServer()` under -race, twice, from independent consumer runs,
// the second reproduced at a clean base.
//
// What is NOT guaranteed. Node has no lifecycle state, so CLOSE IS NOT
// SYNCHRONISED AGAINST BOOT OR MIGRATION:
//
//   - Close concurrent with Unseal is undefined. Close retracts the handles,
//     then the in-flight boot's Store calls republish them, and the node looks
//     open again after Close returned.
//   - Close concurrent with EnableEncryption is undefined. The migration
//     reloads barrier and barrierRegistry after Close has nilled them, and
//     Reopen can rebuild the data plane after shutdown.
//
// Neither is a torn read — the reads are still atomic — so this is an ordering
// gap, not the defect #176 fixed. Callers must not overlap Close with Unseal or
// EnableEncryption. Closing that gap needs an explicit lifecycle state and a
// ruling on what Close means for an embedded Node (does it block on an in-flight
// Unseal, or does Unseal start failing, and with which typed error). That is
// tracked as go-qrdb#226, NOT fixed here.
//
// Retraction, within those limits. Close retracts every handle it published
// before stopping what backs them, and teardownDataPlane (the in-process
// encryption-migration recycle) retracts the ones the stopped service graph
// owns. So a caller that does NOT overlap Close with a boot sees nil from every
// accessor afterwards rather than a live-looking handle onto a stopped graph.
//
// The tests enumerate the accessors by REFLECTION over this type rather than by
// hand, so an accessor added later is covered by construction — a hand-list is
// how IsSealed and BarrierRegistry were missed on the first pass at #176.
//
// atomic.Pointer and not a mutex because each handle is an independent
// single-word publication with no invariant spanning two of them, and reads
// outnumber the single write by orders of magnitude — the same reasoning
// pkg/database, pkg/secure and pkg/storage/pool apply to their swappable
// handles. A mutex is what the LIFECYCLE gap above will need; it is not what a
// per-handle publication needs. Interface-typed handles are boxed, so the
// pointer holds *Iface.
//
// Finally, all of this is about ACQUIRING a handle, never about USING one.
// Calling through a handle you already hold, concurrently with Close, is a
// use-after-close; the node has never promised to survive that and still does
// not.
type Node struct {
	srv     atomic.Pointer[server.Server]
	client  atomic.Pointer[sdk.Client]
	svcNode atomic.Pointer[lifecycle.Node]
	host    quicraftCloser // nil for standalone
	stg     storage.Storage
	// pebbleDB is the main data-plane pebble instance; nil for memory/file
	// backends and for sealed nodes before Unseal completes.
	pebbleDB *pebble.DB
	// sealStore is the dedicated PebbleDB for the seal sub-store; it must
	// outlive the barrier and the data-plane PebbleDB.
	sealStore       *pebbleStorage.PebbleStorage
	barrier         atomic.Pointer[seal.StorageBarrier]
	barrierRegistry atomic.Pointer[seal.BarrierRegistry]
	// volumePool is non-nil when the config declares at least one volume pool.
	// Its PerTenantRegistry is drained before the sealStore is closed on
	// shutdown, mirroring cmd/qrdbd/start.go's shutdown ordering.
	volumePool *qrdbpool.VolumePoolSubsystem
	// credCoreSvc is the *credentials.CredentialService built during the seal
	// subsystem phase. It is kept here so completeBoot can thread it into
	// lifecycle.BuildServices.Infra.CredentialCoreSvc without needing to
	// pierce the service.CredentialServicer interface.
	credCoreSvc *credentials.CredentialService
	logger      *slog.Logger

	// sessionIssuer and loginFactorVerifiers carry the unified-login
	// ceremony wiring from the node constructor through to
	// wireClientAndServer. They survive the sealed window because
	// wireClientAndServer runs after Unseal completes the boot. Both are
	// optional: the ceremony resolver is still wired (when a User service is
	// present) even when these are absent.
	sessionIssuer        embedded.SessionIssuer
	loginFactorVerifiers []embedded.FactorVerifier

	// abacPolicies / abacPolicyDir / abacLiveReload carry the ABAC decorator
	// configuration from the node constructor to buildEmbeddedOpts, which
	// wraps the RBAC authorizer via abac.Decorate. All empty/false means
	// RBAC only.
	abacPolicies   []*abac.Policy
	abacPolicyDir  string
	abacLiveReload bool

	// extraAuthenticators / authorizerWrapper carry the embedder's custom
	// authn/authz hooks (WithAuthenticators / WithAuthorizerWrapper) from
	// the node constructor to buildEmbeddedOpts (wrapper) and
	// wireClientAndServer (authenticator chain).
	extraAuthenticators []auth.Authenticator
	authorizerWrapper   func(inner auth.Authorizer) auth.Authorizer

	// authorizer is the composed RBAC[+ABAC] auth.Authorizer built by
	// decorateABAC in buildEmbeddedOpts (nil when svcNode.Role is nil, i.e.
	// no Role service). wireClientAndServer forwards this SAME instance to
	// wiring.BuildAuthMiddleware (AuthMiddlewareConfig.Authorizer) so the
	// embedded in-process transport and every network transport (REST,
	// gRPC, unix, QUIC, MCP, MySQL, PostgreSQL) share one authorizer — an
	// ABAC policy Reload on it reaches every transport identically.
	authorizer auth.Authorizer

	// abacDecorator is the Reload seam for the node's live ABAC policy set,
	// set by decorateABAC when the composed authorizer supports it
	// (abac.PolicyReloader). Nil when ABAC was not configured with
	// WithABACLiveReload and no initial policies were supplied. Exposed via
	// ABACPolicyReloader.
	abacDecorator atomic.Pointer[abac.PolicyReloader]

	// auditLogger is the caller-supplied compliance audit logger resolved
	// from the server.ServerOptions. It is shared with the multi-protocol
	// server (via server.WithAuditLogger) and injected into the embedded
	// transport so client.Audit() emits and queries against the same logger.
	auditLogger audit.AuditLogger

	// loginFactorSvc is the UserAuthFactor service instance the unified-login
	// ceremony reads from. It is built in buildLoginOpts and retained here so
	// that out-of-process composers (e.g. quicbaas) can enroll login factors
	// into the SAME store instance the LoginResolver resolves against,
	// guaranteeing enroll-writes are visible to login-reads. Nil until
	// buildLoginOpts runs (only when a User service is present).
	loginFactorSvc atomic.Pointer[service.UserAuthFactorServicer]

	// authFactorSvc is the custodian enrollment/challenge/verify service shared
	// by the embedded transport and the multi-protocol server so both see the
	// same in-memory challenge cache and the same persisted enrollment records.
	// Populated in wireClientAndServer from the caller-supplied
	// server.WithAuthFactorService or constructed automatically from
	// server.WithFactorRegistry when no explicit service is supplied.
	authFactorSvc atomic.Pointer[service.AuthFactorServicer]

	// pendingStandalone is non-nil when the node was constructed with a sealed
	// barrier from a passphrase-missing or Shamir strategy. Unseal consumes it
	// to complete the standalone boot.
	pendingStandalone *pendingStandaloneBoot

	// pendingClustered is non-nil when the clustered node was constructed with
	// a sealed barrier. Unseal consumes it to complete the clustered boot.
	pendingClustered *pendingClusteredBoot

	// retainedStandalone preserves the standalone boot parameters AFTER boot
	// completes (completeStandaloneBoot nils pendingStandalone). It lets the
	// in-process encryption-migration recycle rebuild the data plane under a
	// keyed barrier without a process restart. Nil for clustered/memory nodes.
	retainedStandalone *pendingStandaloneBoot

	// migrateMu serializes in-process system-scope encryption migrations so only
	// one system migration runs at a time (each tears down and rebuilds the data
	// plane). Per-tenant migrations use the TenantHostRegistry's per-tenant
	// migrating guard and do not contend on this mutex.
	migrateMu sync.Mutex

	// tenantIsolation records whether WithTenantIsolation() was supplied at
	// construction. Copied from nodeOptions so completeStandaloneBoot can
	// consult it after Unseal completes (nodeOptions is a local variable in the
	// constructor).
	tenantIsolation bool

	// defaultTenantEntitlements are forwarded to lifecycle.Infra so the built-in
	// TenantService permits the isolation modes and features the embedder needs.
	defaultTenantEntitlements *quota.Entitlements

	// tenantHosts is the IsolationVolume per-tenant Host registry built by
	// server.BuildTenantInfra when tenantIsolation is true. It is nil for nodes
	// that do not opt in to tenant isolation.
	tenantHosts *server.TenantHostRegistry

	// raftBackend is the IsolationRaft factory. Non-nil on a clustered node
	// that has WithTenantIsolation() set and a configured barrier registry.
	// It is shared with wireClientAndServer (which adds
	// server.WithRaftBackendFactory) and with the scoped encryption migrator
	// so tenant-scope enable-encryption reaches per-raft-shard recyclers.
	raftBackend *server.RaftBackendFactory

	// dbManager tracks the per-database metadata and shard-ID allocation for
	// dedicated IsolationRaft databases. Created by completeClusteredBoot and
	// closed in LIFO order after all raft shards are stopped and the main host
	// is closed, mirroring the daemon's deferred manager.Close() ordering.
	dbManager *database.Manager

	// bgCtx is the node-lifetime background context for long-running goroutines
	// started during completeClusteredBoot (currently the TenantShardReconciler).
	// It is cancelled by bgCancel in Close() so those goroutines exit promptly
	// rather than blocking on a placement watch or poll timer. Nil on standalone
	// nodes and on clustered nodes where no reconciler was started (no
	// PlacementStore configured).
	bgCtx    context.Context //nolint:containedctx
	bgCancel context.CancelFunc

	// encMigrator is the scoped encryption migrator built in wireClientAndServer.
	// It routes system scope to the nodeSystemMigrator (which holds migrateMu)
	// and tenant scope to tenantHosts.MigrateEncryption. Both Node.EnableEncryption
	// and the multi-protocol server's transport handlers route through the same
	// underlying scoped-router semantics.
	encMigrator atomic.Pointer[service.EncryptionMigrator]
}

// quicraftCloser is satisfied by *quicraft.Host and allows tests to inject
// a stub without importing the quicraft package at the call site.
type quicraftCloser interface {
	Close() error
}

// Server returns the started multi-protocol server. Returns nil when the
// node is in the sealed state (Unseal has not yet been called).
func (n *Node) Server() *server.Server { return n.srv.Load() }

// Client returns the in-process SDK client connected to this node. Returns
// nil when the node is in the sealed state.
func (n *Node) Client() *sdk.Client { return n.client.Load() }

// Services returns the lifecycle.Node that aggregates all service instances.
// Embedders that need direct service references (e.g. to wire additional
// protocol servers) use this accessor. When the node is in the sealed state
// this returns a Node that has only the seal-layer services populated (Barrier,
// Ceremony, Credential); data-plane services are nil.
func (n *Node) Services() *lifecycle.Node { return n.svcNode.Load() }

// BarrierRegistry returns the per-tenant barrier registry built during the
// seal subsystem phase. Embedders use this to construct tenant-scoped
// credential resolvers via the sdk/go/secret subpackage's
// NewTenantScopedCredentialRetrieve so that
// credential:// lookups route to the correct per-tenant barrier store.
// Returns nil when cfg.Seal.Enabled is false (no seal subsystem) or when
// the node is in the sealed state before Unseal has been called.
func (n *Node) BarrierRegistry() *seal.BarrierRegistry { return n.barrierRegistry.Load() }

// IsSealed reports whether the node is in the sealed state, meaning the data
// plane has not yet been started because the seal barrier requires operator
// credentials to unseal. Call Unseal to complete the boot.
func (n *Node) IsSealed() bool {
	barrier := n.barrier.Load()
	if barrier == nil {
		return false
	}
	return barrier.IsSealed()
}

// Unseal drives the barrier to the unsealed state using the supplied
// credentials and, on success, completes the node's boot (BuildServices +
// server.New). It is a no-op when the node is already unsealed.
//
// For the passphrase strategy, creds.Secret must contain the passphrase.
// For Shamir, the caller must first accumulate quorum shares via the
// ceremony API; Unseal with empty creds completes the final boot once the
// barrier has been driven unsealed externally.
//
// On success IsSealed returns false and Server() / Client() return
// non-nil values. On failure the node remains in the sealed state and the
// caller may retry.
func (n *Node) Unseal(ctx context.Context, creds seal.Credentials) error {
	// One snapshot for the whole call. Loading per-use would let a concurrent
	// Close turn a nil check into a nil dereference two lines later.
	barrier := n.barrier.Load()
	if barrier == nil {
		return nil // seal not enabled
	}
	if !barrier.IsSealed() {
		if n.pendingStandalone != nil || n.pendingClustered != nil {
			// Barrier already unsealed externally (e.g. via the ceremony API)
			// but completeBoot hasn't run yet. Fall through.
		} else {
			return nil // fully booted
		}
	}

	// Attempt to unseal the barrier if it is still sealed.
	if barrier.IsSealed() {
		if initErr := barrier.Initialize(ctx, creds); initErr != nil {
			if !errors.Is(initErr, seal.ErrAlreadyInitialized) {
				return &UnsealError{Err: initErr}
			}
			// Already initialized: attempt standard unseal.
			if unsealErr := barrier.Unseal(ctx, creds); unsealErr != nil {
				if !errors.Is(unsealErr, seal.ErrAlreadyUnsealed) {
					return &UnsealError{Err: unsealErr}
				}
			}
		}
	}

	if barrier.IsSealed() {
		return &UnsealError{Err: seal.ErrSealed}
	}

	return n.completeBoot(ctx)
}

// completeBoot finishes the node's boot sequence after the barrier is
// unsealed. It is called by both Unseal and (for auto-unseal strategies)
// directly from the constructors.
func (n *Node) completeBoot(ctx context.Context) error {
	if n.pendingStandalone != nil {
		return n.completeStandaloneBoot(ctx, n.pendingStandalone)
	}
	if n.pendingClustered != nil {
		return n.completeClusteredBoot(ctx, n.pendingClustered)
	}
	return nil
}

// completeStandaloneBoot finishes wiring the standalone node after the
// barrier is unsealed: opens data-plane storage, creates the engine, calls
// BuildServices, and wires the server.
func (n *Node) completeStandaloneBoot(ctx context.Context, pending *pendingStandaloneBoot) error {
	n.pendingStandalone = nil
	// Retain the boot parameters so an in-process encryption migration can
	// rebuild the data plane under a keyed barrier without a process restart.
	n.retainedStandalone = pending

	// One snapshot of the seal layer for the whole boot: it is written by the
	// constructor before the node escapes and by Close, never by the boot
	// itself, so re-loading per use would only widen the window for a
	// concurrent Close to turn a nil check into a nil dereference.
	barrier := n.barrier.Load()
	barrierRegistry := n.barrierRegistry.Load()

	var dataPlaneFS vfs.FS
	if barrier != nil && !barrier.IsSealed() && pending.backendType == database.BackendPebble {
		dataPlaneFS = encfs.New(vfs.Default, barrier)
	}

	stg, pdb, err := createNodeStorage(pending.backendType, pending.dataDir, dataPlaneFS, pending.cfg.Storage.Objstore)
	if err != nil {
		return &StorageCreateError{Backend: string(pending.backendType), Err: err}
	}
	n.stg = stg
	n.pebbleDB = pdb

	watchHub := watch.NewHub()
	smAdapter, err := createNodeEngine(pending.backendType, stg, pdb, watchHub)
	if err != nil {
		_ = n.stg.Close()
		n.stg = nil
		return &EngineCreateError{Engine: "mvcc", Err: err}
	}

	localHost := backend.NewLocalBackend(smAdapter)

	var encryptor *atrest.Encryptor
	if barrier != nil {
		encryptor = atrest.New(barrier)
	}

	// The effective capability profile of THIS node: NewStandaloneNode is
	// the raft-free composition by construction, so the profile is computed
	// with the standalone mode semantics regardless of cfg.Mode. It is
	// pinned per-instance (Infra.Profile -> AdminService) rather than
	// sealed process-globally, because one process may host several nodes.
	profileCfg := *pending.cfg
	profileCfg.Mode = capabilities.ModeStandalone
	profile := profileCfg.EffectiveCapabilities()

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              localHost,
		WatchHub:          watchHub,
		DataDir:           pending.dataDir,
		ReplicaID:         pending.cfg.Raft.ReplicaID,
		Profile:           &profile,
		Logger:            n.logger,
		Encryptor:         encryptor,
		SealBarrier:       barrier,
		BarrierRegistry:   barrierRegistry,
		CredentialCoreSvc: n.credCoreSvc,
		// Forward an injected (xkms) seal StrategyRegistry into the
		// TenantService so per-tenant hardware/cloud seal strategies resolve.
		// This must happen here (before BuildServices), not via server.New,
		// because the TenantService is constructed inside BuildServices.
		StrategyRegistry:          server.ResolveCryptoOptions(pending.serverOpts...).StrategyRegistry,
		DefaultTenantEntitlements: n.defaultTenantEntitlements,
	})
	if err != nil {
		_ = localHost.Close()
		_ = n.stg.Close()
		n.stg = nil
		return &ServicesCreateError{Err: err}
	}
	n.svcNode.Store(svcNode)

	// Build the IsolationVolume tenant stack when the caller opted in via
	// WithTenantIsolation and the prerequisites are all present. The guard
	// n.tenantHosts == nil ensures this runs only on the first boot, not on
	// the in-process recycle triggered by an encryption migration (where
	// n.tenantHosts is already set and the tenant Hosts keep running through
	// the system data-plane tear-down and rebuild).
	if n.tenantIsolation && barrierRegistry != nil && n.volumePool != nil && svcNode.Tenant != nil && n.tenantHosts == nil {
		ti, tiErr := server.BuildTenantInfra(server.TenantInfraConfig{
			Ctx:        ctx,
			BarrierReg: barrierRegistry,
			TenantSvc:  svcNode.Tenant,
			VolumePool: n.volumePool,
			Logger:     n.logger,
			// Phase 1: Host, Manager, ReplicaID, RaftAddr, SystemTenant,
			// PlacementStore, MultiplexLogDB are all left zero — IsolationRaft
			// is not wired in the embedded standalone node yet (Phase 2).
		})
		if tiErr != nil {
			_ = svcNode.Stop(context.Background())
			_ = localHost.Close()
			_ = n.stg.Close()
			n.stg = nil
			return &TenantInfraError{Err: tiErr}
		}
		n.tenantHosts = ti.Hosts
	}

	if err := n.wireClientAndServer(ctx, svcNode, pending.cfg, pending.serverOpts, true); err != nil {
		_ = svcNode.Stop(context.Background())
		_ = localHost.Close()
		_ = n.stg.Close()
		n.stg = nil
		// Close any per-tenant IsolationVolume Hosts that were assembled by
		// BuildTenantInfra before wireClientAndServer was attempted.
		if n.tenantHosts != nil {
			_ = n.tenantHosts.CloseAll()
			n.tenantHosts = nil
		}
		return err
	}
	return nil
}

// clusteredCreateFunc selects the Raft state-machine factory for a clustered
// node. The default (empty, "pebble", or "mvcc") is the durable MVCC-over-Pebble
// store wired with the supplied lessor / watch-hub / time-series / encryptor
// options — the only state machine that carries full MVCC semantics. Any other
// cfg.Raft.SMType (e.g. "memory", "concurrent", "fast") selects an alternate
// factory from the statemachine registry, giving a replicated in-memory cache
// that does NOT provide watch / lease / time-series. Memory-class state machines
// are rejected when the seal barrier is enabled, since there is no durable
// at-rest store to encrypt.
func clusteredCreateFunc(cfg *qrdbconfig.Config, pebbleOpts []store.Option, mvccOpts []store.MVCCOption, sealed bool) (sm.CreateFunc, error) {
	switch cfg.Raft.SMType {
	case "", "pebble", "mvcc":
		return store.NewMVCCCreateFuncWith(pebbleOpts, mvccOpts...), nil
	default:
		if sealed {
			return nil, &ConfigError{
				Field:   "raft.sm_type",
				Message: "state machine " + cfg.Raft.SMType + " is incompatible with an enabled seal barrier; use the default pebble store",
			}
		}
		fn, err := statemachine.ResolveTyped(cfg.Raft.SMType, cfg.Raft.SMBackend)
		if err != nil {
			return nil, &ConfigError{Field: "raft.sm_type", Message: err.Error()}
		}
		return fn, nil
	}
}

// background returns the node-lifetime context for long-running goroutines,
// creating it on first use. Goroutines started from a boot path must NOT use
// the boot request's ctx: that ctx is cancelled when the caller's Unseal or
// New returns, which would stop the goroutine moments after the node came up.
// Node.Close cancels this one.
func (n *Node) background() context.Context {
	if n.bgCtx == nil {
		n.bgCtx, n.bgCancel = context.WithCancel(context.Background())
	}
	return n.bgCtx
}

// startLeaseExpiry starts the leader-side lease-expiry sweeper for a clustered
// node, mirroring the daemon's wiring in cmd/qrdbd/start.go.
//
// Without it a lease with a TTL expires in the Lessor -- the holder's Done
// channel closes on time -- but NOTHING deletes the keys attached to it,
// because the delete happens in the apply path of a revoke that no node ever
// proposes (go-qrdb#174). For a consumer whose HA design makes the bound key's
// existence the ownership signal, that is the difference between "the leader
// died" and "a replacement can take over": the dead leader is fenced, every
// contender receives LeaseHeldError, and no election ever completes.
//
// Only the Raft leader proposes the revoke, routed through consensus via
// leaseSvc.Revoke; every replica applies it. The leadership gate is
// re-evaluated on each tick, so the loop is correct across elections and is a
// no-op on followers. It is skipped when the lease service is absent
// (embeddable deployments built without lease support), which is the same
// condition the daemon skips on.
func (n *Node) startLeaseExpiry(host *quicraft.Host, lr *lease.Lessor, leaseSvc service.LeaseServicer, replicaID uint64) {
	if host == nil || lr == nil || leaseSvc == nil {
		return
	}
	go lr.RunExpiry(n.background(), lease.DefaultExpiryInterval,
		func() bool {
			leaderID, ok, err := host.GetLeaderID(shard.ShardKV)
			return err == nil && ok && leaderID == replicaID
		},
		leaseSvc.Revoke,
		n.logger,
	)
}

// completeClusteredBoot finishes wiring the clustered node after the barrier
// is unsealed: creates the quicraft host, calls BuildServices, and wires the
// server.
func (n *Node) completeClusteredBoot(ctx context.Context, pending *pendingClusteredBoot) error {
	n.pendingClustered = nil

	// One snapshot of the seal layer for the whole boot, as in
	// completeStandaloneBoot: written by the constructor and by Close, never
	// by the boot itself.
	barrier := n.barrier.Load()
	barrierRegistry := n.barrierRegistry.Load()

	var encryptor *atrest.Encryptor
	if barrier != nil {
		encryptor = atrest.New(barrier)
	}

	watchHub := watch.NewHub()
	lr := lease.NewLessor()

	pebbleOpts := []store.Option{store.WithEncryptor(encryptor)}
	mvccOpts := []store.MVCCOption{
		store.WithLessor(lr),
		store.WithWatchHub(watchHub),
		store.WithTimeSeriesEnabled(),
	}
	if pending.noSyncWrites {
		mvccOpts = append(mvccOpts, store.WithNoSync())
	}
	createFn, err := clusteredCreateFunc(pending.cfg, pebbleOpts, mvccOpts, true)
	if err != nil {
		return err
	}

	host, err := buildClusteredHost(ctx, pending.cfg, pending.members, createFn, pending.raftMTLS)
	if err != nil {
		return &HostCreateError{Err: err}
	}
	n.host = host

	// Build the database manager with a shard allocator so dedicated-raft
	// database shard IDs are tracked and auto-incremented. OpenAll re-scans
	// the databases directory, loads persisted meta.json records, and advances
	// the allocator past the highest known shard ID — the same sequence the
	// daemon performs before BuildServices. The manager is shared with
	// lifecycle.BuildServices (which skips creating its own when non-nil) and
	// with BuildTenantInfra (reattachRaftTenants walks manager.List()). The
	// node owns the manager's lifetime; lifecycle does NOT add a closer for an
	// externally-supplied manager.
	shardAlloc := shard.NewAllocator()
	dbManager := database.NewManager(n.logger, pending.cfg.Raft.DataDir, database.WithAllocator(shardAlloc))
	if err := dbManager.OpenAll(); err != nil {
		_ = host.Close()
		_ = dbManager.Close()
		return &StorageCreateError{Backend: "database-manager", Err: err}
	}
	n.dbManager = dbManager

	hostAdapter := raftbackend.New(host)

	// Per-instance effective capability profile: the clustered node is the
	// raft composition, so raft-mode semantics apply (config toggles can
	// still narrow mvcc/watch/lease).
	profileCfg := *pending.cfg
	profileCfg.Mode = capabilities.ModeRaft
	profile := profileCfg.EffectiveCapabilities()

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              hostAdapter,
		WatchHub:          watchHub,
		DataDir:           pending.cfg.Raft.DataDir,
		ReplicaID:         pending.cfg.Raft.ReplicaID,
		Profile:           &profile,
		Logger:            n.logger,
		Encryptor:         encryptor,
		SealBarrier:       barrier,
		BarrierRegistry:   barrierRegistry,
		CredentialCoreSvc: n.credCoreSvc,
		// DatabaseManager is shared so the shard allocator is not reset
		// mid-flight and lifecycle does not open a second manager on the same
		// data directory.
		DatabaseManager: dbManager,
		// Forward an injected (xkms) seal StrategyRegistry into the
		// TenantService (constructed inside BuildServices) so per-tenant
		// hardware/cloud seal strategies resolve in clustered mode too.
		StrategyRegistry:          server.ResolveCryptoOptions(pending.serverOpts...).StrategyRegistry,
		DefaultTenantEntitlements: n.defaultTenantEntitlements,
	})
	if err != nil {
		_ = host.Close()
		_ = dbManager.Close()
		n.dbManager = nil
		return &ServicesCreateError{Err: err}
	}
	n.svcNode.Store(svcNode)

	// Phase 2: bind the cluster-wide replicated volume-pool state now that the
	// replicated system KV is available. Mirrors cmd/qrdbd/start.go.
	n.bindReplicatedVolumePool(svcNode)

	// Wire the IsolationRaft + IsolationVolume tenant infrastructure when the
	// caller opted in via WithTenantIsolation and the prerequisites are present.
	// The guard n.tenantHosts == nil prevents a double-build on the in-process
	// encryption-migration recycle (where the clustered boot does not rebuild;
	// only the standalone path rebuilds via completeStandaloneBoot). In the
	// clustered path there is no in-process system-migration recycle today, but
	// the guard matches the standalone pattern for consistency.
	if n.tenantIsolation && barrierRegistry != nil && n.volumePool != nil && svcNode.Tenant != nil && n.tenantHosts == nil {
		// Wait for the system shard to elect a leader before building tenant
		// infrastructure. reattachRaftTenants calls ListTenants which requires
		// a linearizable read; that read fails if the shard has no leader yet.
		// The daemon relies on other boot steps (REST/gRPC/QUIC setup) to
		// provide this window; the embedded node must wait explicitly.
		if readyErr := waitSystemShardReady(host, shard.ShardKV); readyErr != nil {
			_ = svcNode.Stop(context.Background())
			_ = host.Close()
			_ = dbManager.Close()
			n.dbManager = nil
			return readyErr
		}
		ti, tiErr := server.BuildTenantInfra(server.TenantInfraConfig{
			Ctx:        ctx,
			BarrierReg: barrierRegistry,
			TenantSvc:  svcNode.Tenant,
			VolumePool: n.volumePool,
			Logger:     n.logger,
			// Host is the shared quicraft.Host that carries IsolationRaft shards.
			// Passing it here enables NewRaftBackendFactoryFromManager and
			// reattachRaftTenants so clustered embedded nodes gain full
			// IsolationRaft parity with the daemon. PlacementStore comes from
			// the just-built svcNode so shard placement is replicated into the
			// system KV. MultiplexLogDB is intentionally nil: the embedded node
			// has no WAL mux; dedicated-raft DBs fall back to the shared system
			// WAL (Option A from 4d Option C).
			Host:           host,
			Manager:        dbManager,
			ReplicaID:      pending.cfg.Raft.ReplicaID,
			RaftAddr:       pending.cfg.Raft.RaftAddress,
			SystemTenant:   tenant.SystemTenantID,
			PlacementStore: svcNode.PlacementStore,
		})
		if tiErr != nil {
			_ = svcNode.Stop(context.Background())
			_ = host.Close()
			_ = dbManager.Close()
			n.dbManager = nil
			return &TenantInfraError{Err: tiErr}
		}
		n.tenantHosts = ti.Hosts
		n.raftBackend = ti.Raft
		// Wire the IsolationRaft factory into the already-built backup
		// service as its ShardRestarter, enabling ONLINE restore for
		// dedicated per-database Raft shards (see
		// service.RestoreShardOnline). Mirrors cmd/qrdbd/start.go's wiring.
		if n.raftBackend != nil && svcNode.Backup != nil {
			service.SetShardRestarter(svcNode.Backup, n.raftBackend)
		}
		if ti.Reconciler != nil {
			// Use a node-lifetime context so the reconciler keeps running after
			// the unseal/boot request ctx is cancelled. The background context is
			// cancelled by bgCancel in Node.Close() (and in the wireClientAndServer
			// error branch below).
			go ti.Reconciler.Run(n.background())
		}
	}

	// Leader-side lease-expiry sweeper. See startLeaseExpiry: without it an
	// expired lease's attached keys are never reaped on the embedded path
	// (go-qrdb#174).
	//
	// Started HERE, after every other fallible step, rather than beside the
	// Lessor it sweeps. Placed earlier it sits above waitSystemShardReady and
	// BuildTenantInfra, whose error branches tear down svcNode, host and
	// dbManager and return WITHOUT cancelling the background context -- so a
	// failed boot would return an error while the sweeper kept ticking against
	// closed services. Adding a cancel to each of those branches would work and
	// would leave three places to forget; one start point with one error path
	// below it cannot be forgotten. The sweeper needs nothing that boot
	// produces after this line, and nothing is serving traffic yet.
	n.startLeaseExpiry(host, lr, svcNode.Lease, pending.cfg.Raft.ReplicaID)

	if err := n.wireClientAndServer(ctx, svcNode, pending.cfg, pending.serverOpts, false); err != nil {
		// Stop the reconciler goroutine before tearing down the resources it
		// accesses so it does not race against CloseAll/Close below.
		if n.bgCancel != nil {
			n.bgCancel()
			n.bgCancel = nil
		}
		_ = svcNode.Stop(context.Background())
		n.svcNode.Store(nil)
		// Stop IsolationRaft shards BEFORE closing the shared host — mirrors
		// the ordering in Node.Close (raftBackend.CloseAll before host.Close).
		// Inverting this order would close the host while Pebble shard locks are
		// still held, risking data corruption or a deadlock on host shutdown.
		if n.raftBackend != nil {
			_ = n.raftBackend.CloseAll()
			n.raftBackend = nil
		}
		if n.host != nil {
			_ = n.host.Close()
			n.host = nil
		}
		// Close per-tenant IsolationVolume Hosts after the main host — mirrors
		// Node.Close ordering (tenantHosts.CloseAll after host.Close).
		if n.tenantHosts != nil {
			_ = n.tenantHosts.CloseAll()
			n.tenantHosts = nil
		}
		// Close the database manager last — mirrors Node.Close ordering
		// (dbManager.Close after all raft shards, host, and tenantHosts).
		if n.dbManager != nil {
			_ = n.dbManager.Close()
			n.dbManager = nil
		}
		return err
	}
	return nil
}

// bindReplicatedVolumePool wires the cluster-wide volume-pool state (Phase 2)
// once the replicated system KV is available: it binds the pool's round-robin
// cursor store to the replicated KV and attaches the replicated tenant→slot
// assignment store to the barrier registry. It is a no-op when the node has no
// volume pool, runs standalone (ReplicatedState is nil), or has no KV service.
func (n *Node) bindReplicatedVolumePool(svcNode *lifecycle.Node) {
	if n.volumePool == nil || svcNode == nil || svcNode.KV == nil {
		return
	}
	if n.volumePool.ReplicatedState != nil {
		if err := n.volumePool.ReplicatedState.Bind(lifecycle.NewKVStoreAdapter(svcNode.KV)); err != nil && n.logger != nil {
			n.logger.Warn("node: failed to bind replicated volume-pool cursor; falling back to node-local pick",
				slog.Any("error", err))
		}
	}
	if barrierRegistry := n.barrierRegistry.Load(); barrierRegistry != nil {
		barrierRegistry.Attach(seal.WithReplicatedSlotStore(
			lifecycle.NewReplicatedSealBackend(svcNode.KV, svcNode.Transaction)))
	}
}

// retractDataPlaneServices clears the service handles that belong to the
// service graph the caller is about to stop: the custodian factor service, the
// unified-login factor service, and the ABAC reload seam. Both Close and
// teardownDataPlane (the in-process encryption-migration recycle) call it, so
// neither leaves an accessor handing out a live-looking handle onto stopped
// backing services. wireClientAndServer republishes all three on the rebuild.
//
// encMigrator is deliberately NOT retracted here. It is the scoped router, and
// its tenant leg reaches TenantHostRegistry and RaftBackendFactory, which
// SURVIVE a system-scope recycle by design (tenant migrations use a per-tenant
// guard and do not contend on migrateMu). Clearing it during teardownDataPlane
// would turn a concurrent tenant-scope migration — which today proceeds
// independently of the system migration — into a typed "not yet fully booted"
// refusal. Close clears it directly instead, where there is no rebuild to come.
// The router does hold the OLD svcNode.Tenant across a recycle; that staleness
// is tracked separately as go-qrdb#222, because fixing it means rebuilding or
// lazily resolving the router, not nilling a pointer.
func (n *Node) retractDataPlaneServices() {
	n.authFactorSvc.Store(nil)
	n.loginFactorSvc.Store(nil)
	n.abacDecorator.Store(nil)
}

// Close tears down all node resources in LIFO order. Nil fields are skipped.
//
// All barriers (system and per-tenant) are sealed before their references
// are dropped so unsealed DEKs and root keys held in memguard enclaves are
// wiped deterministically rather than waiting on GC finalizers. Embedded
// composers that want mlocked memory purged synchronously at process exit
// must additionally arm secure.EnablePurge at startup and invoke
// secure.Purge on shutdown, mirroring cmd/qrdbd/main.go.
func (n *Node) Close() error {
	var firstErr error

	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Cancel the node-lifetime background context before stopping any service
	// so long-running goroutines (TenantShardReconciler) exit promptly rather
	// than blocking on a placement watch or poll timer during teardown.
	if n.bgCancel != nil {
		n.bgCancel()
		n.bgCancel = nil
	}

	// Retract every handle this node published BEFORE stopping what backs
	// them. A caller that loads one after Close gets nil rather than a
	// live-looking handle onto a stopped service graph — the accessors promise
	// "a value or nil", and after Close the honest answer is nil for all of
	// them, not just the three with an obvious owner.
	if client := n.client.Swap(nil); client != nil {
		record(client.Close())
	}
	if srv := n.srv.Swap(nil); srv != nil {
		srv.Stop()
	}
	n.retractDataPlaneServices()
	if svcNode := n.svcNode.Swap(nil); svcNode != nil {
		record(svcNode.Stop(context.Background()))
	}
	// The scoped encryption router is retracted only here, never in
	// teardownDataPlane: see retractDataPlaneServices for why the migration
	// recycle keeps it.
	n.encMigrator.Store(nil)
	// Stop dedicated IsolationRaft shards on the shared host BEFORE closing the
	// host itself — each shard holds an active raft group registered with the
	// host's quicraft state machine and its own Pebble instance rooted in the
	// tenant DataRoot. Stopping them first allows quicraft to flush in-flight
	// entries and release the shard's Pebble locks. Mirrors the daemon's
	// raftBackendReg.CloseAll() call before host.Close() in start.go.
	if n.raftBackend != nil {
		record(n.raftBackend.CloseAll())
		n.raftBackend = nil
	}
	if n.host != nil {
		record(n.host.Close())
		n.host = nil
	}
	// Close per-tenant IsolationVolume Hosts BEFORE sealing barriers: each
	// tenant Host holds an open reference to its per-tenant barrier (via the
	// per-tenant quicraft EpochBarrier) and its underlying Pebble instance.
	// Closing them first ensures quicraft can shut down cleanly and that the
	// per-tenant barriers' key material is wiped by the Host's own Close path
	// before the registry-level SealAll below zeroes the root-key enclaves.
	// Mirrors cmd/qrdbd/start.go shutdown ordering (hostReg.CloseAll before
	// barrierRegistry.SealAll).
	if n.tenantHosts != nil {
		record(n.tenantHosts.CloseAll())
		n.tenantHosts = nil
	}
	// Seal before dropping references: Seal zeroes the EpochBarrier root
	// key and DEKs and is idempotent, so a still-sealed barrier is a no-op.
	// SealAll covers the system barrier registered in the registry; the
	// direct barrier seal handles registry-less configurations.
	// Retract the seal handles BEFORE sealing, so an accessor racing Close
	// cannot hand out a barrier that is about to have its key material wiped.
	barrierRegistry := n.barrierRegistry.Swap(nil)
	barrier := n.barrier.Swap(nil)
	if barrierRegistry != nil {
		record(barrierRegistry.SealAll())
	} else if barrier != nil {
		record(barrier.Seal())
	}
	n.credCoreSvc = nil
	// Drain per-tenant Pebble instances before closing the volume-pool state
	// store. Mirrors cmd/qrdbd/start.go shutdown ordering.
	if n.volumePool != nil && n.volumePool.PerTenantRegistry != nil {
		closed := n.volumePool.PerTenantRegistry.List()
		if err := n.volumePool.PerTenantRegistry.CloseAll(context.Background()); err != nil {
			n.logger.Error("per-tenant pebble close-all failed",
				slog.Any("error", err),
				slog.Int("count", len(closed)))
		} else if len(closed) > 0 {
			n.logger.Info("closed per-tenant pebbles on shutdown",
				slog.Int("count", len(closed)))
		}
	}
	if n.volumePool != nil {
		record(n.volumePool.Close())
		n.volumePool = nil
	}
	if n.sealStore != nil {
		record(n.sealStore.Close())
		n.sealStore = nil
	}
	if n.stg != nil {
		record(n.stg.Close())
		n.stg = nil
		n.pebbleDB = nil
	}
	// Close the database manager last — after all raft shards, host, and
	// storage are down — mirroring the daemon's deferred dbManager.Close()
	// at the top of its run function (last to run, LIFO). The manager holds
	// only metadata bookkeeping and in-memory placeholder backends for raft
	// databases; closing it is always safe at this point.
	if n.dbManager != nil {
		record(n.dbManager.Close())
		n.dbManager = nil
	}
	return firstErr
}

// ---------------------------------------------------------------------------
// NewStandaloneNode
// ---------------------------------------------------------------------------

// NewStandaloneNode builds a fully initialised standalone QRDB node backed
// by a LocalBackend (engine-direct, no Raft state machine). The storage
// backend is selected from cfg.Raft.DataDir and cfg.Databases; a memory
// backend is used when DataDir is empty or the first database is configured
// as BackendMemory.
//
// Seal lifecycle (when cfg.Seal.Enabled is true):
//  1. The seal subsystem (barrier, credential service, barrier registry) is
//     constructed before data-plane storage.
//  2. Auto-unseal is attempted: StrategyNone always succeeds; the passphrase
//     strategy succeeds when WithSealPassphrase is supplied; hardware
//     strategies (HardwareBacked() == true) attempt unseal with empty
//     credentials.
//  3. If the barrier is unsealed → full boot (BuildServices + server.New).
//  4. If the barrier is still sealed (Shamir or missing passphrase) → the
//     returned node is in sealed state. IsSealed() returns true; the seal
//     services are accessible via Services(). Call Unseal to complete the
//     boot once the operator has driven the barrier unsealed.
//
// serverOpts are forwarded verbatim to server.New so callers can inject
// WithStrategyRegistry, WithPINBackend, WithFactorRegistry, and any other
// server.ServerOption.
//
// nodeOpts configure node-level behaviour such as the seal passphrase and
// logger.
func NewStandaloneNode(ctx context.Context, cfg *qrdbconfig.Config, serverOpts []server.ServerOption, nodeOpts ...NodeOption) (*Node, error) {
	if cfg == nil {
		return nil, &ConfigError{Field: "cfg", Message: "must not be nil"}
	}

	nOpts := &nodeOptions{}
	for _, o := range nodeOpts {
		o(nOpts)
	}
	log := nOpts.logger
	if log == nil {
		log = slog.Default()
	}

	n := &Node{
		logger:                    log,
		sessionIssuer:             nOpts.sessionIssuer,
		loginFactorVerifiers:      nOpts.loginFactorVerifiers,
		tenantIsolation:           nOpts.tenantIsolation,
		defaultTenantEntitlements: nOpts.defaultTenantEntitlements,
		abacPolicies:              nOpts.abacPolicies,
		abacPolicyDir:             nOpts.abacPolicyDir,
		abacLiveReload:            nOpts.abacLiveReload,
		extraAuthenticators:       nOpts.extraAuthenticators,
		authorizerWrapper:         nOpts.authorizerWrapper,
	}

	backendType, dataDir, err := resolveBackend(cfg)
	if err != nil {
		return nil, err
	}

	// Build the seal subsystem BEFORE data-plane storage so that, once
	// unsealed, the data-plane PebbleDB can be opened on top of encfs.
	// Standalone nodes run on a LocalBackend (no replicated KV), so the volume
	// pool cursor stays node-local (clustered=false).
	if cfg.Seal.Enabled {
		sub, err := buildNodeSealSubsystem(ctx, cfg, dataDir, log, nOpts, false)
		if err != nil {
			return nil, err
		}
		n.barrier.Store(sub.barrier)
		n.barrierRegistry.Store(sub.barrierRegistry)
		n.sealStore = sub.sealStore
		n.credCoreSvc = sub.credCoreSvc
		n.volumePool = sub.volumePool

		// Build the sealed-window service graph (barrier + ceremony +
		// credential only) so they are accessible even before full boot.
		sealNode, sealNodeErr := lifecycle.BuildSealServicesOnly(ctx, lifecycle.Infra{
			DataDir:           dataDir,
			SealBarrier:       sub.barrier,
			BarrierRegistry:   sub.barrierRegistry,
			CredentialCoreSvc: sub.credCoreSvc,
			CeremonyDataDir:   sub.sealDataDir,
			Logger:            log,
		})
		if sealNodeErr != nil {
			_ = sub.close()
			return nil, &ServicesCreateError{Err: sealNodeErr}
		}
		n.svcNode.Store(sealNode)

		// Attempt auto-unseal. If it fails (operator-unseal strategy) record
		// the pending state and return the sealed node to the caller.
		if attemptAutoUnseal(ctx, sub.barrier, cfg.Seal, nOpts.sealPassphrase, log) {
			// Unsealed: proceed with full boot.
			if err := n.completeStandaloneBoot(ctx, &pendingStandaloneBoot{
				backendType: backendType,
				dataDir:     dataDir,
				serverOpts:  serverOpts,
				cfg:         cfg,
			}); err != nil {
				n.closeSeal()
				return nil, err
			}
			return n, nil
		}

		// Operator-unseal required: return the node in sealed state.
		n.pendingStandalone = &pendingStandaloneBoot{
			backendType: backendType,
			dataDir:     dataDir,
			serverOpts:  serverOpts,
			cfg:         cfg,
		}
		return n, nil
	}

	// Seal disabled: build storage + engine + services immediately.
	stg, pdb, err := createNodeStorage(backendType, dataDir, nil, cfg.Storage.Objstore)
	if err != nil {
		return nil, &StorageCreateError{Backend: string(backendType), Err: err}
	}
	n.stg = stg
	n.pebbleDB = pdb

	watchHub := watch.NewHub()
	smAdapter, err := createNodeEngine(backendType, stg, pdb, watchHub)
	if err != nil {
		_ = n.stg.Close()
		return nil, &EngineCreateError{Engine: "mvcc", Err: err}
	}

	localHost := backend.NewLocalBackend(smAdapter)

	// Per-instance effective capability profile: standalone semantics by
	// construction (see completeStandaloneBoot for the rationale).
	profileCfg := *cfg
	profileCfg.Mode = capabilities.ModeStandalone
	profile := profileCfg.EffectiveCapabilities()

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:      localHost,
		WatchHub:  watchHub,
		DataDir:   dataDir,
		ReplicaID: cfg.Raft.ReplicaID,
		Profile:   &profile,
		Logger:    log,
		// Forward an injected (xkms) seal StrategyRegistry into the
		// TenantService even on the no-seal data path, so the option a
		// composer passes via server.WithStrategyRegistry is honored rather
		// than silently dropped.
		StrategyRegistry:          server.ResolveCryptoOptions(serverOpts...).StrategyRegistry,
		DefaultTenantEntitlements: n.defaultTenantEntitlements,
	})
	if err != nil {
		_ = localHost.Close()
		_ = n.stg.Close()
		return nil, &ServicesCreateError{Err: err}
	}
	n.svcNode.Store(svcNode)

	if err := n.wireClientAndServer(ctx, svcNode, cfg, serverOpts, true); err != nil {
		_ = svcNode.Stop(context.Background())
		_ = localHost.Close()
		_ = n.stg.Close()
		return nil, err
	}

	return n, nil
}

// ---------------------------------------------------------------------------
// NewClusteredNode
// ---------------------------------------------------------------------------

// NewClusteredNode builds a fully initialised clustered QRDB node backed by
// a quicraft.Host running a subsystem-aware MVCC state machine. It starts the
// host, starts the KV shard using the supplied members map, wraps the host
// with pkg/backend/raft.HostAdapter to satisfy service.Host, and wires the
// full lifecycle.BuildServices + server.New pipeline.
//
// members maps replica ID → Raft address (e.g. {1: "127.0.0.1:63001"}).
// A single-entry map starts a single-node cluster using
// quicraft.WithoutTransport. Multi-node clusters require valid cfg.Raft
// address and port fields.
//
// Seal lifecycle follows the same rules as NewStandaloneNode: the subsystem
// is built first; auto-unseal is attempted; if still sealed a node in sealed
// state is returned and Unseal completes the clustered boot.
//
// serverOpts are forwarded verbatim to server.New.
func NewClusteredNode(ctx context.Context, cfg *qrdbconfig.Config, members map[uint64]string, serverOpts []server.ServerOption, nodeOpts ...NodeOption) (*Node, error) {
	if cfg == nil {
		return nil, &ConfigError{Field: "cfg", Message: "must not be nil"}
	}
	if len(members) == 0 {
		return nil, &ConfigError{Field: "members", Message: "must not be empty"}
	}

	nOpts := &nodeOptions{}
	for _, o := range nodeOpts {
		o(nOpts)
	}
	log := nOpts.logger
	if log == nil {
		log = slog.Default()
	}

	n := &Node{
		logger:                    log,
		sessionIssuer:             nOpts.sessionIssuer,
		loginFactorVerifiers:      nOpts.loginFactorVerifiers,
		tenantIsolation:           nOpts.tenantIsolation,
		defaultTenantEntitlements: nOpts.defaultTenantEntitlements,
		abacPolicies:              nOpts.abacPolicies,
		abacPolicyDir:             nOpts.abacPolicyDir,
		abacLiveReload:            nOpts.abacLiveReload,
		extraAuthenticators:       nOpts.extraAuthenticators,
		authorizerWrapper:         nOpts.authorizerWrapper,
	}

	dataDir := cfg.Raft.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}

	// Build the seal subsystem BEFORE host construction so encfs wraps the
	// state machine's Pebble stores once the barrier is unsealed. Clustered
	// nodes run on a Raft host, so the volume pool cursor lives in the
	// replicated system KV (clustered=true), bound once the data plane is up.
	if cfg.Seal.Enabled {
		sub, err := buildNodeSealSubsystem(ctx, cfg, dataDir, log, nOpts, true)
		if err != nil {
			return nil, err
		}
		n.barrier.Store(sub.barrier)
		n.barrierRegistry.Store(sub.barrierRegistry)
		n.sealStore = sub.sealStore
		n.credCoreSvc = sub.credCoreSvc
		n.volumePool = sub.volumePool

		sealNode, sealNodeErr := lifecycle.BuildSealServicesOnly(ctx, lifecycle.Infra{
			DataDir:           dataDir,
			SealBarrier:       sub.barrier,
			BarrierRegistry:   sub.barrierRegistry,
			CredentialCoreSvc: sub.credCoreSvc,
			CeremonyDataDir:   sub.sealDataDir,
			Logger:            log,
		})
		if sealNodeErr != nil {
			_ = sub.close()
			return nil, &ServicesCreateError{Err: sealNodeErr}
		}
		n.svcNode.Store(sealNode)

		if attemptAutoUnseal(ctx, sub.barrier, cfg.Seal, nOpts.sealPassphrase, log) {
			if err := n.completeClusteredBoot(ctx, &pendingClusteredBoot{
				members:      members,
				serverOpts:   serverOpts,
				cfg:          cfg,
				raftMTLS:     nOpts.raftMTLS,
				noSyncWrites: nOpts.noSyncWrites,
			}); err != nil {
				n.closeSeal()
				return nil, err
			}
			return n, nil
		}

		n.pendingClustered = &pendingClusteredBoot{
			members:      members,
			serverOpts:   serverOpts,
			cfg:          cfg,
			raftMTLS:     nOpts.raftMTLS,
			noSyncWrites: nOpts.noSyncWrites,
		}
		return n, nil
	}

	// Seal disabled: build the host and services immediately.
	watchHub := watch.NewHub()
	lr := lease.NewLessor()

	pebbleOpts := []store.Option{store.WithEncryptor(atrest.New(nil))}
	mvccOptsNoSeal := []store.MVCCOption{
		store.WithLessor(lr),
		store.WithWatchHub(watchHub),
		store.WithTimeSeriesEnabled(),
	}
	if nOpts.noSyncWrites {
		mvccOptsNoSeal = append(mvccOptsNoSeal, store.WithNoSync())
	}
	createFn, err := clusteredCreateFunc(cfg, pebbleOpts, mvccOptsNoSeal, false)
	if err != nil {
		return nil, err
	}

	host, err := buildClusteredHost(ctx, cfg, members, createFn, nOpts.raftMTLS)
	if err != nil {
		return nil, &HostCreateError{Err: err}
	}
	n.host = host

	hostAdapter := raftbackend.New(host)

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:      hostAdapter,
		WatchHub:  watchHub,
		DataDir:   dataDir,
		ReplicaID: cfg.Raft.ReplicaID,
		Logger:    log,
	})
	if err != nil {
		_ = host.Close()
		return nil, &ServicesCreateError{Err: err}
	}
	n.svcNode.Store(svcNode)

	// Leader-side lease-expiry sweeper, as in completeClusteredBoot. The
	// seal-disabled branch builds its host and services inline rather than
	// through that function, so it needs the same wiring; omitting it here is
	// how the unsealed path kept the defect after the sealed path was fixed.
	n.startLeaseExpiry(host, lr, svcNode.Lease, cfg.Raft.ReplicaID)

	if err := n.wireClientAndServer(ctx, svcNode, cfg, serverOpts, false); err != nil {
		if n.bgCancel != nil {
			n.bgCancel()
			n.bgCancel = nil
		}
		_ = svcNode.Stop(context.Background())
		_ = host.Close()
		return nil, err
	}

	return n, nil
}

// ---------------------------------------------------------------------------
// buildNodeSealSubsystem
// ---------------------------------------------------------------------------

// buildNodeSealSubsystem initialises the seal storage barrier, sealing
// strategy, credential service, and barrier registry from the supplied
// configuration. It is the node package's equivalent of the daemon's
// initSealSubsystem (cmd/qrdbd/seal_subsystem.go).
//
// The subsystem must be built BEFORE host and data-plane storage construction
// so that encfs can wrap PebbleDB once the barrier is unsealed.
//
// When nOpts.sealStrategy is non-nil, that pre-built strategy is registered
// with the barrier instead of the built-in deferred-software fallback. This
// is the injection path for cloud KMS (awskms, vault, gcpkms, azurekv) and
// hardware (tpm2, pkcs11) strategies constructed by higher-level composers.
func buildNodeSealSubsystem(ctx context.Context, cfg *qrdbconfig.Config, dataDir string, log *slog.Logger, nOpts *nodeOptions, clustered bool) (*nodeSealSubsystem, error) {
	sealDataDir := filepath.Join(dataDir, "seal")
	// SyncWrites is forced on for the seal store: DefaultConfig defaults it
	// to false on the premise that the Raft WAL provides durability, but the
	// seal store is NOT replicated through Raft — it is the sole persisted
	// copy of the root-key blob and epoch/rotation state. An unsynced write
	// here (e.g. the PutIfAbsent that lands the root key during Initialize,
	// or the StatePersister write from an epoch rotation) can survive only
	// in the page cache; a crash before the next flush silently reverts the
	// on-disk blob, and the next boot's Initialize succeeds against a fresh
	// random root key instead of failing closed — permanently orphaning
	// every file the previous root key encrypted.
	sealCfg := pebbleStorage.DefaultConfig(sealDataDir)
	sealCfg.SyncWrites = true
	sealStore, err := pebbleStorage.New(sealCfg)
	if err != nil {
		return nil, &StorageCreateError{Backend: "seal", Err: err}
	}

	sealBackend := seal.NewStorageAdapter(sealStore)

	strategyID := seal.StrategyID(cfg.Seal.CanonicalStrategy())
	if strategyID == "" {
		strategyID = seal.StrategyPassphrase
	}

	var strategies []seal.SealingStrategy
	barrierCfg := seal.BarrierConfig{
		RootKeyPath: cfg.Seal.RootKeyPath,
	}
	if barrierCfg.RootKeyPath == "" {
		barrierCfg.RootKeyPath = "root-key"
	}

	// Reconcile an encryption migration interrupted by a crash BEFORE the
	// barrier is built or data stores are opened: roll a committed migration
	// forward (so the keyed data + keyed root key are both in place) or a
	// not-yet-committed one back. Idempotent and credential-free, so safe on
	// every boot; a no-op when no migration journal is present.
	if rerr := database.RecoverInterruptedMigration(ctx, sealBackend, barrierCfg.RootKeyPath,
		[]string{filepath.Join(dataDir, "pebble"), filepath.Join(dataDir, "vector")}); rerr != nil {
		log.Warn("node: encryption-migration crash recovery failed", "error", rerr)
	}

	switch {
	case strategyID == seal.StrategyShamir:
		shamirStrat, shamirErr := seal.NewShamirStrategy(
			cfg.Seal.ShamirThreshold,
			cfg.Seal.ShamirTotal,
		)
		if shamirErr != nil {
			_ = sealStore.Close()
			return nil, &StorageCreateError{Backend: "seal-shamir", Err: shamirErr}
		}
		strategies = append(strategies, shamirStrat)
		barrierCfg.Shamir = &seal.ShamirConfig{
			Threshold:   cfg.Seal.ShamirThreshold,
			TotalShares: cfg.Seal.ShamirTotal,
		}
	case strategyID == seal.StrategyNone:
		strategies = append(strategies, seal.NewNoneStrategy())
	case nOpts != nil && nOpts.sealStrategy != nil:
		// Caller-injected strategy: hardware/cloud KMS (awskms, vault, gcpkms,
		// azurekv, tpm2, pkcs11). The pre-built strategy is used directly so
		// the barrier can auto-unseal via hardware/KMS credentials without
		// operator interaction.
		strategies = append(strategies, nOpts.sealStrategy)
	default:
		// Passphrase / deferred software strategy.
		strategies = append(strategies, seal.NewDeferredSoftwareStrategy())
	}

	// Restart-survival for enable-encryption: if the persisted root-key blob was
	// migrated to a keyed strategy the config-derived set does not cover (e.g.
	// the barrier was migrated none -> passphrase but the config still names
	// "none"), register that strategy too so the node unseals correctly after a
	// migration + restart. The barrier is self-describing from its persisted
	// sealed root key.
	if persisted := persistedRootKeyStrategy(ctx, sealBackend, barrierCfg.RootKeyPath); persisted != "" && !strategyRegistered(strategies, persisted) {
		switch persisted {
		case seal.StrategyPassphrase:
			strategies = append(strategies, seal.NewDeferredSoftwareStrategy())
		case seal.StrategyShamir:
			if shamirStrat, e := seal.NewShamirStrategy(cfg.Seal.ShamirThreshold, cfg.Seal.ShamirTotal); e == nil {
				strategies = append(strategies, shamirStrat)
			}
		}
	}

	for _, s := range strategies {
		barrierCfg.PreferenceOrder = append(barrierCfg.PreferenceOrder, s.ID())
	}

	barrier, barrierErr := seal.NewStorageBarrier(log, sealBackend, barrierCfg, strategies...)
	if barrierErr != nil {
		_ = sealStore.Close()
		return nil, &StorageCreateError{Backend: "seal-barrier", Err: barrierErr}
	}

	credStrategy := cfg.Seal.CredentialStrategy
	if credStrategy == "" {
		credStrategy = credentials.StrategyBarrier
	}
	credCoreSvc, credErr := credentials.NewCredentialService(
		&credentials.Config{Strategy: credStrategy}, barrier, log)
	if credErr != nil {
		_ = sealStore.Close()
		return nil, &StorageCreateError{Backend: "seal-credentials", Err: credErr}
	}

	barrierRegistry, regErr := seal.NewBarrierRegistry(log, barrier)
	if regErr != nil {
		_ = sealStore.Close()
		return nil, &StorageCreateError{Backend: "seal-registry", Err: regErr}
	}

	// Build the volume-pool subsystem when the config declares pools. This
	// mirrors the daemon's initVolumePoolSubsystem path (cmd/qrdbd/start.go)
	// so embedders (go-quicbaas, go-xkms) get per-tenant barrier isolation
	// without requiring the daemon binary.
	vp, vpErr := qrdbpool.BuildVolumePoolSubsystem(dataDir, cfg.Storage.VolumePools, log, clustered)
	if vpErr != nil {
		_ = sealStore.Close()
		return nil, &StorageCreateError{Backend: "volume-pool", Err: vpErr}
	}
	if vp != nil {
		barrierRegistry.Attach(
			seal.WithVolumePool(vp.Manager, vp.StateStore, vp.DefaultPoolName),
			seal.WithPerTenantRegistry(vp.PerTenantRegistry),
		)
	}

	return &nodeSealSubsystem{
		barrier:         barrier,
		credCoreSvc:     credCoreSvc,
		barrierRegistry: barrierRegistry,
		sealStore:       sealStore,
		sealDataDir:     sealDataDir,
		volumePool:      vp,
	}, nil
}

// ---------------------------------------------------------------------------
// attemptAutoUnseal
// ---------------------------------------------------------------------------

// attemptAutoUnseal tries to unseal the barrier without operator interaction.
// It returns true when the barrier is unsealed after the attempt.
//
// Auto-unseal strategies:
//   - StrategyNone: no credentials required; Initialize with empty creds.
//   - StrategyPassphrase with passphrase supplied via WithSealPassphrase.
//   - Hardware-backed strategies (HardwareBacked() == true): attempt unseal
//     with empty credentials; the strategy retrieves the key from hardware.
//
// Operator-unseal strategies (returns false, leaving barrier sealed):
//   - StrategyShamir: shares are never persisted server-side; the operator
//     must submit quorum shares via the barrier API.
//   - StrategyPassphrase without a supplied passphrase.
func attemptAutoUnseal(ctx context.Context, barrier *seal.StorageBarrier, sealCfg qrdbconfig.SealConfig, passphrase []byte, log *slog.Logger) bool {
	strategyID := seal.StrategyID(sealCfg.CanonicalStrategy())
	if strategyID == "" {
		strategyID = seal.StrategyPassphrase
	}
	// Prefer the strategy recorded in the persisted root key: a barrier migrated
	// to a keyed strategy after the fact (enable-encryption) must auto-unseal
	// with the migrated strategy even when the static config still names the
	// pre-migration one (the config is not rewritten by the migration).
	if persisted := barrier.PersistedStrategy(ctx); persisted != "" {
		strategyID = persisted
	}

	var creds seal.Credentials

	switch strategyID {
	case seal.StrategyShamir:
		// Shamir always requires operator quorum. Never auto-unseal.
		log.Info("node: seal strategy requires operator unseal", "strategy", strategyID)
		return false

	case seal.StrategyNone:
		// No credentials required.

	case seal.StrategyPassphrase:
		if len(passphrase) == 0 {
			// No passphrase supplied: operator must drive unseal.
			log.Info("node: seal strategy requires operator unseal", "strategy", strategyID)
			return false
		}
		creds = seal.Credentials{Secret: passphrase}

	default:
		// Unknown / hardware strategy. Inspect the barrier's active strategy
		// to decide: if HardwareBacked(), attempt with empty creds; the
		// strategy retrieves the key from hardware. Otherwise require operator.
		//
		// The strategies slice is internal to the barrier so we probe via
		// Status(). HardwareBacked is only set on an unsealed barrier, so for
		// a freshly-constructed barrier that has not yet been initialized we
		// fall back to an empty-creds attempt and let the strategy reject if
		// it cannot auto-unseal.
		creds = seal.Credentials{}
	}

	// Attempt initialize (first boot) then unseal (subsequent boots).
	if initErr := barrier.Initialize(ctx, creds); initErr != nil {
		if !errors.Is(initErr, seal.ErrAlreadyInitialized) {
			log.Warn("node: barrier initialize failed", "error", initErr)
			return false
		}
		// Already initialized: unseal.
		if unsealErr := barrier.Unseal(ctx, creds); unsealErr != nil {
			if errors.Is(unsealErr, seal.ErrAlreadyUnsealed) {
				return true
			}
			log.Warn("node: barrier unseal failed", "error", unsealErr)
			return false
		}
	}
	return !barrier.IsSealed()
}

// ---------------------------------------------------------------------------
// Shared wiring helpers
// ---------------------------------------------------------------------------

// wireClientAndServer creates the in-process embedded transport, SDK client,
// and multi-protocol server. Used by both standalone and clustered builders
// after the service graph is ready.
func (n *Node) wireClientAndServer(ctx context.Context, svcNode *lifecycle.Node, cfg *qrdbconfig.Config, callerOpts []server.ServerOption, standalone bool) error {
	// Resolve caller-supplied crypto options once so the same instances back
	// both the multi-protocol server and the in-process transport.
	crypto := server.ResolveCryptoOptions(callerOpts...)

	// One snapshot of the seal layer for the whole wiring pass; see
	// completeStandaloneBoot for why it is loaded once rather than per use.
	barrier := n.barrier.Load()
	barrierRegistry := n.barrierRegistry.Load()
	n.auditLogger = crypto.AuditLogger

	// Wire the AuthFactorService. If the caller supplied one explicitly via
	// server.WithAuthFactorService it is already in crypto.AuthFactorService.
	// If they supplied only a FactorRegistry, construct the service now so the
	// embedded transport and all wire protocol servers share one instance.
	if crypto.AuthFactorService != nil {
		authFactorSvc := crypto.AuthFactorService
		n.authFactorSvc.Store(&authFactorSvc)
	} else if crypto.FactorRegistry != nil {
		svc, err := service.NewAuthFactorService(service.AuthFactorServiceConfig{
			Factors: crypto.FactorRegistry.All(),
			KV:      svcNode.KV,
		})
		if err != nil {
			return &AuthFactorWiringError{Err: err}
		}
		var authFactorSvc service.AuthFactorServicer = svc
		n.authFactorSvc.Store(&authFactorSvc)
	}

	// Build in-process transport.
	// RBAC posture mirrors the standalone daemon EXACTLY
	// (cmd/qrdbd/start.go): config can only RAISE strictness and a
	// FIPS 140-3 build (crypto/fips140.Enabled()) is always strict
	// regardless of config, so embedded and standalone authorize
	// identically. The Auth.SeparationOfDuties flag is the same source
	// the daemon reads (cfg.Auth.SeparationOfDuties).
	strictSoD := fips140.Enabled() || cfg.Auth.SeparationOfDuties
	embeddedOpts, err := n.buildEmbeddedOpts(svcNode, strictSoD, standalone)
	if err != nil {
		return err
	}
	tp, err := embedded.New(svcNode.KV, svcNode.Admin, embeddedOpts...)
	if err != nil {
		return &ClientCreateError{Err: err}
	}

	// Create SDK client.
	client, err := sdk.NewClient(tp)
	if err != nil {
		return &ClientCreateError{Err: err}
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		return &ClientCreateError{Err: err}
	}
	n.client.Store(client)

	// Build the canonical RBAC[+ABAC]-authorized, mTLS-authenticated auth
	// middleware and install it on the multi-protocol server so every
	// permission-gated route (REST, gRPC, unix, QUIC, MCP, MySQL,
	// PostgreSQL) enforces authn/authz exactly like the standalone daemon
	// (cmd/qrdbd/start.go bootstrapAuthEnforcement) — both call the SAME
	// pkg/auth/wiring.BuildAuthMiddleware constructor. Without this an
	// embedded node answered every RBAC-gated route unauthenticated (200
	// with no credential presented) because the multi-protocol server
	// never received server.WithAuthMiddleware, even though the
	// in-process embedded transport (buildEmbeddedOpts, above) already
	// enforced RBAC via node.Client().RBAC().
	//
	// cfg.Auth.Enabled mirrors the operator's --auth-enabled posture from
	// the same DaemonConfig re-export qrdbd itself loads; when false, auth
	// is explicitly disabled by configuration and BuildAuthMiddleware
	// returns (nil, nil) — no middleware is installed, matching qrdbd's
	// own posture. When true, RoleService/UserService MUST be present
	// (lifecycle.BuildServices always constructs both); their absence
	// aborts node construction with a typed AuthMiddlewareWiringError
	// rather than silently serving unauthenticated.
	//
	// n.authorizer is the EXACT SAME composed RBAC[+ABAC] authorizer
	// buildEmbeddedOpts/decorateABAC already built and handed to the
	// in-process embedded transport (embedded.WithRBACAuthorizer), passed
	// here via AuthMiddlewareConfig.Authorizer so BuildAuthMiddleware
	// reuses it instead of constructing an independent second instance.
	// Sharing one instance is what makes a live ABAC policy Reload
	// (Node.ABACPolicyReloader) reach every network transport identically
	// to the embedded transport — two independently-built decorators would
	// silently diverge the moment either one reloaded. n.authorizer is nil
	// only when svcNode.Role is nil (no Role service), in which case
	// cfg.RoleService below is also nil and BuildAuthMiddleware fails
	// closed exactly as it did before this authorizer was shared.
	// The middleware's per-decision audit trail (audit.Logger, Log/Close)
	// bridges onto the node's chained compliance sink (audit.AuditLogger,
	// Emit/Query) via audit.NewDecisionLogger, so authorization decisions
	// land in the SAME tamper-evident event log as every other audited
	// operation — one audit plane, matching qrdbd's posture without a
	// second store.
	var decisionAudit audit.Logger
	if n.auditLogger != nil {
		decisionAudit = audit.NewDecisionLogger(n.auditLogger)
	}
	// The revocation cache TTL is the window during which a revoked
	// certificate still authenticates, so an unparseable value fails the boot
	// rather than quietly reverting to a default the embedder did not choose.
	revocationTTL, err := qrdbconfig.ParseRevocationCacheTTL(cfg.Auth.RevocationCacheTTL)
	if err != nil {
		_ = client.Close()
		n.client.Store(nil)
		return &AuthMiddlewareWiringError{Err: err}
	}
	authMW, err := wiring.BuildAuthMiddleware(wiring.AuthMiddlewareConfig{
		Enabled:             cfg.Auth.IsEnabled(),
		Authorizer:          n.authorizer,
		RoleService:         svcNode.Role,
		UserService:         svcNode.User,
		StrictSeparation:    strictSoD,
		SPIFFETrustDomain:   cfg.Auth.SPIFFETrustDomain,
		AuditLogger:         decisionAudit,
		ExtraAuthenticators: n.extraAuthenticators,
		// Certificate revocation is enforced on the authentication path, so
		// the certificate authority reaches the middleware and not only the
		// cert API handlers. An embedded node without this enforces nothing:
		// it records revocations, lists them, and lets the revoked
		// certificate keep authenticating until it expires.
		CertProvider:       svcNode.CertProv,
		RevocationCacheTTL: revocationTTL,
	})
	if err != nil {
		_ = client.Close()
		n.client.Store(nil)
		return &AuthMiddlewareWiringError{Err: err}
	}

	// Build multi-protocol server.
	serverCfg := buildNodeServerConfig(cfg)
	allOpts := buildServerOpts(svcNode, callerOpts, standalone)
	if authMW != nil {
		allOpts = append(allOpts, server.WithAuthMiddleware(authMW))
	}
	if barrier != nil {
		allOpts = append(allOpts, server.WithSealedWindowProbe(barrier))
	}
	if p := n.authFactorSvc.Load(); p != nil {
		allOpts = append(allOpts, server.WithAuthFactorService(*p))
	}

	// Build the system-scope migrator adapter that holds migrateMu for the
	// duration of each in-process system data-plane recycle.
	sysMig := &nodeSystemMigrator{node: n}

	// Expose enable-encryption when a barrier registry is present. The
	// nodeSystemMigrator handles system scope; the server's
	// effectiveEncryptionMigrator auto-composes a scopedEncryptionMigrator
	// that routes tenant scope to TenantHostRegistry when tenantHosts is wired.
	if barrierRegistry != nil {
		allOpts = append(allOpts,
			server.WithEncryptionMigrator(sysMig),
			server.WithBarrierRegistry(barrierRegistry),
		)
	}
	// Wire the IsolationVolume tenant host registry into every transport so
	// per-tenant data-plane requests reach the right isolated shard. Together
	// with WithEncryptionMigrator(sysMig), this causes the server's internal
	// effectiveEncryptionMigrator to build a scopedEncryptionMigrator that
	// routes system scope to sysMig and tenant scope to tenantHosts.
	if n.tenantHosts != nil {
		allOpts = append(allOpts, server.WithTenantHostRegistry(n.tenantHosts))
	}
	// Wire the IsolationRaft backend factory into every transport so
	// dedicated-raft tenant database requests resolve to the right live shard
	// on the shared system host. Non-nil only on a clustered node that has
	// WithTenantIsolation() set; nil on standalone nodes (which use IsolationVolume
	// only in Phase 1).
	if n.raftBackend != nil {
		allOpts = append(allOpts, server.WithRaftBackendFactory(n.raftBackend))
	}

	// Build and cache the scoped migration router used by Node.EnableEncryption.
	// This mirrors what the server builds internally via effectiveEncryptionMigrator,
	// so the direct in-process call and every transport handler share the same
	// routing semantics (system → sysMig → migrateMu; tenant → tenantHosts).
	var encMigrator service.EncryptionMigrator = server.NewScopedEncryptionMigrator(sysMig, n.tenantHosts, n.raftBackend, barrierRegistry, svcNode.Tenant)
	n.encMigrator.Store(&encMigrator)

	srv := server.New(serverCfg, svcNode.KV, svcNode.Admin, allOpts...)
	if err := srv.Start(); err != nil {
		_ = client.Close()
		n.client.Store(nil)
		return &ServerCreateError{Err: err}
	}
	n.srv.Store(srv)
	return nil
}

// buildEmbeddedOpts constructs the embedded transport options from the
// lifecycle.Node's services. When the node carries a User service it also
// wires the unified-login ceremony: a UserAuthFactor service (backed by the
// node's KV servicer), a LoginResolver bridging factors to qrdb users, the
// optional SessionIssuer, and any injected hardware FactorVerifiers.
//
// When the node carries a Role service it also constructs the qrdb RBAC
// authorizer (rbac.New) with the same posture as the standalone daemon —
// WithStrictSeparation(strictSoD) — and wires it via
// embedded.WithRBACAuthorizer, so embedded consumers get a working PDP
// (node.Client().RBAC().Authorize) identical to the multi-protocol path.
//
// It returns a LoginWiringError when the UserAuthFactor service cannot be
// constructed; the caller aborts the boot so a misconfigured login surface
// never silently degrades to "login not supported".
func (n *Node) buildEmbeddedOpts(svcNode *lifecycle.Node, strictSoD bool, standalone bool) ([]embedded.Option, error) {
	opts := []embedded.Option{
		embedded.WithBatchService(svcNode.Batch),
		embedded.WithIndexService(svcNode.Index),
		embedded.WithTimeSeriesService(svcNode.TimeSeries),
		embedded.WithVectorService(svcNode.Vector),
		embedded.WithEmbeddingService(svcNode.Embedding),
		embedded.WithRAGService(svcNode.RAG),
		embedded.WithDatabaseService(svcNode.Database),
	}
	// The transaction/CAS family is served in EVERY mode (owner ruling
	// 2026-08-23, go-qrdb#62). A standalone node is engine-direct and
	// single-process, so a compare-and-swap there is already linearizable
	// without consensus — consensus is what makes it linearizable across
	// REPLICAS, and a standalone node has none. Withholding it left a
	// consumer holding node.Client() with no compare-and-swap primitive at
	// all, which is why go-xkms could not boot: its CA key store reaches
	// PutIfAbsent on the first write of a fresh node.
	opts = append(opts, embedded.WithTransactionService(svcNode.Transaction))
	// MVCC, lease and watch remain raft-only. Unlike CAS these are not
	// single-process-satisfiable: they are about observing and coordinating
	// state ACROSS replicas over time.
	if !standalone {
		opts = append(opts,
			embedded.WithMVCCService(svcNode.MVCC),
			embedded.WithLeaseService(svcNode.Lease),
			embedded.WithWatchService(svcNode.Watch),
		)
	}
	if svcNode.User != nil {
		opts = append(opts, embedded.WithUserService(svcNode.User))
	}
	if svcNode.Role != nil {
		opts = append(opts, embedded.WithRoleService(svcNode.Role))
		// Construct the qrdb RBAC authorizer the SAME way the standalone
		// daemon does (cmd/qrdbd/start.go bootstrapAuthEnforcement):
		// rbac.New(roleService, WithStrictSeparation(strictSoD)). Without
		// this the embedded RBACClient.Authorize returns
		// MissingRBACAuthorizerError. The daemon's optional StaleRead
		// WithRoleServiceFallback has no equivalent in the embedded path
		// (lifecycle.Node carries a single Role service) and only affects
		// transient-read failover, never which requests are admitted, so
		// authorize decisions remain identical.
		rbacAuthz := rbac.New(svcNode.Role, rbac.WithStrictSeparation(strictSoD))
		// Optionally decorate the RBAC authorizer with ABAC policy
		// evaluation, mirroring qrdbd's decorateWithABAC. The wrapped
		// authorizer is the SINGLE provider handed to the embedded
		// transport, so every embedded consumer sees the composed
		// decision. Load/compile failures abort the boot (fail closed).
		authorizer, err := n.decorateABAC(rbacAuthz)
		if err != nil {
			return nil, err
		}
		// Apply the embedder's authorizer wrapper HERE, at the single
		// construction site, so the embedded transport and the network
		// transports' auth middleware (which receives n.authorizer via
		// AuthMiddlewareConfig.Authorizer below) share one wrapped
		// instance — two independently wrapped instances would diverge.
		if n.authorizerWrapper != nil {
			authorizer = n.authorizerWrapper(authorizer)
			if authorizer == nil {
				return nil, &AuthMiddlewareWiringError{Err: ErrNilWrappedAuthorizer}
			}
		}
		n.authorizer = authorizer
		opts = append(opts, embedded.WithRBACAuthorizer(authorizer))
	}
	if svcNode.Barrier != nil {
		opts = append(opts, embedded.WithBarrierService(svcNode.Barrier))
	}
	if svcNode.CustodianGroup != nil {
		opts = append(opts, embedded.WithCustodianGroupService(svcNode.CustodianGroup))
	}
	if svcNode.Share != nil {
		opts = append(opts, embedded.WithShareService(svcNode.Share))
	}
	if n.auditLogger != nil {
		opts = append(opts, embedded.WithAuditLogger(n.auditLogger))
	}
	if p := n.authFactorSvc.Load(); p != nil {
		opts = append(opts, embedded.WithAuthFactorService(*p))
	}

	// Wire the unified-login ceremony only when a user model is present.
	if svcNode.User != nil {
		loginOpts, err := n.buildLoginOpts(svcNode)
		if err != nil {
			return nil, err
		}
		opts = append(opts, loginOpts...)
	}
	return opts, nil
}

// decorateABAC wraps the given authorizer with the ABAC policy decorator
// when the node was constructed with WithABACPolicies, WithABACPolicyDir,
// or WithABACLiveReload. With none of those configured the inner
// authorizer is returned unchanged. Any load or compilation failure is
// wrapped in *ABACWiringError so the caller aborts the boot rather than
// running with a partial policy set.
//
// When the returned authorizer supports live reload (abac.PolicyReloader —
// always true when WithABACLiveReload was set, since that forces
// abac.Decorate to wrap even with zero initial policies; also true when at
// least one policy was actually loaded from abacPolicies/abacPolicyDir),
// the reload seam is retained on n.abacDecorator for ABACPolicyReloader.
func (n *Node) decorateABAC(inner auth.Authorizer) (auth.Authorizer, error) {
	if len(n.abacPolicies) == 0 && n.abacPolicyDir == "" && !n.abacLiveReload {
		return inner, nil
	}
	decoratorOpts := make([]abac.Option, 0, 3)
	if n.abacPolicyDir != "" {
		decoratorOpts = append(decoratorOpts, abac.WithPolicyDir(n.abacPolicyDir))
	}
	if len(n.abacPolicies) > 0 {
		decoratorOpts = append(decoratorOpts, abac.WithPolicies(n.abacPolicies))
	}
	if n.abacLiveReload {
		decoratorOpts = append(decoratorOpts, abac.WithLiveReload())
	}
	authorizer, err := abac.Decorate(inner, decoratorOpts...)
	if err != nil {
		return nil, &ABACWiringError{Err: err}
	}
	if reloader, ok := authorizer.(abac.PolicyReloader); ok {
		n.abacDecorator.Store(&reloader)
	}
	return authorizer, nil
}

// ABACPolicyReloader returns the handle for replacing the node's active
// ABAC policy set at runtime, without restarting the process. It returns
// nil when the node's ABAC decorator does not support reload — i.e. the
// node was constructed with none of WithABACPolicies, WithABACPolicyDir,
// or WithABACLiveReload. Callers should nil-check before use, mirroring
// other optional runtime handles exposed by Node (e.g. BarrierRegistry).
//
// The returned reloader updates the SAME authorizer instance shared by the
// node's embedded in-process transport (node.Client().RBAC()) and, when
// auth is enabled, every network transport's AuthMiddleware (REST, gRPC,
// unix, QUIC, MCP, MySQL, PostgreSQL) — a Reload call is visible to every
// transport atomically and immediately.
func (n *Node) ABACPolicyReloader() abac.PolicyReloader {
	if p := n.abacDecorator.Load(); p != nil {
		return *p
	}
	return nil
}

// buildLoginOpts constructs the embedded options that wire the unified-login
// ceremony: the UserAuthFactor service, the LoginResolver, the optional
// SessionIssuer, and any injected hardware FactorVerifiers. Caller must have
// verified svcNode.User != nil.
func (n *Node) buildLoginOpts(svcNode *lifecycle.Node) ([]embedded.Option, error) {
	factorSvc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{
		KV: svcNode.KV,
	})
	if err != nil {
		return nil, &LoginWiringError{Err: err}
	}
	var loginFactorSvc service.UserAuthFactorServicer = factorSvc
	n.loginFactorSvc.Store(&loginFactorSvc)

	userLookup := auth.UserLookupFunc(func(ctx context.Context, id uint64) (*auth.Principal, error) {
		u, err := svcNode.User.GetUser(ctx, id)
		if err != nil {
			return nil, err
		}
		return &auth.Principal{
			ID:       u.ID,
			Username: u.Username,
			TenantID: u.TenantID,
			Roles:    principalRoles(u),
			Active:   u.Enabled,
		}, nil
	})

	resolver := auth.NewLoginResolver(factorSvc, userLookup)

	opts := []embedded.Option{
		embedded.WithUserAuthFactorService(factorSvc),
		embedded.WithLoginResolver(resolver),
	}
	if n.sessionIssuer != nil {
		opts = append(opts, embedded.WithSessionIssuer(n.sessionIssuer))
	}
	for _, v := range n.loginFactorVerifiers {
		opts = append(opts, embedded.WithFactorVerifier(v))
	}
	return opts, nil
}

// principalRoles flattens a user's system-scoped RoleIDs and its
// tenant-scoped RoleBinding role refs into the role-name set carried on the
// login Principal. Duplicates are collapsed while preserving first-seen
// order (RoleIDs first, then binding refs).
func principalRoles(u *user.User) []string {
	if len(u.RoleIDs) == 0 && len(u.RoleBindings) == 0 {
		return nil
	}
	roles := make([]string, 0, len(u.RoleIDs)+len(u.RoleBindings))
	seen := make(map[string]struct{}, cap(roles))
	add := func(r string) {
		if r == "" {
			return
		}
		if _, ok := seen[r]; ok {
			return
		}
		seen[r] = struct{}{}
		roles = append(roles, r)
	}
	for _, r := range u.RoleIDs {
		add(r)
	}
	for _, b := range u.RoleBindings {
		add(b.RoleID)
	}
	if len(roles) == 0 {
		return nil
	}
	return roles
}

// buildServerOpts constructs the server.ServerOption slice that wires the
// lifecycle.Node's services into server.New. Caller opts are appended last
// so they can override any default wiring.
func buildServerOpts(svcNode *lifecycle.Node, callerOpts []server.ServerOption, standalone bool) []server.ServerOption {
	opts := []server.ServerOption{
		server.WithBatch(svcNode.Batch),
		server.WithIndex(svcNode.Index),
		server.WithTimeSeries(svcNode.TimeSeries),
		server.WithCollectionService(svcNode.Collection),
		server.WithDatabaseService(svcNode.Database),
		server.WithVector(svcNode.Vector),
	}
	// The transaction/CAS family is composed in BOTH modes. A standalone node
	// is engine-direct and single-process, so a compare-and-swap there is
	// already linearizable — consensus is what makes one linearizable across
	// REPLICAS, and a standalone node has none (owner ruling 2026-08-23,
	// go-qrdb#62). server.WithStandaloneMode deliberately does not gate this
	// family, and its doc comment names the hazard of omitting it here:
	// serving transactions on the in-process transport while the network
	// transports refuse them puts #62's contradiction back one seam over.
	//
	// It was omitted, and go-qrdb#121's conformance matrix measured the
	// result: transactions worked on standalone over the embedded transport
	// and failed over gRPC with "transaction service is not configured",
	// because s.opts.txSvc was nil so grpcapi.WithTransaction was never
	// applied. That is exactly the engine/mode/transport non-uniformity
	// go-qrdb#117 forbids — "CAS must NOT be reachable only via the embedded
	// server".
	opts = append(opts, server.WithTransaction(svcNode.Transaction))

	// The genuinely raft-only API surface (ratified run-modes split): composed
	// only on the raft path; the standalone composition refuses these families
	// with a typed "requires mode: raft" error via nil singletons plus the
	// gated per-database registry (server.WithStandaloneMode). These are about
	// observing and coordinating state ACROSS replicas, which is what a
	// standalone node genuinely cannot do — unlike a compare-and-swap. The
	// engine still serves MVCC operations internally for the SQL catalog and
	// collections.
	if standalone {
		opts = append(opts, server.WithStandaloneMode())
	} else {
		opts = append(opts,
			server.WithMVCC(svcNode.MVCC),
			server.WithLease(svcNode.Lease),
			server.WithWatch(svcNode.Watch),
			server.WithLockService(svcNode.Lock),
		)
	}
	if svcNode.User != nil {
		opts = append(opts, server.WithUserService(svcNode.User))
	}
	if svcNode.Role != nil {
		opts = append(opts, server.WithRoleService(svcNode.Role))
	}
	if svcNode.Barrier != nil {
		opts = append(opts, server.WithBarrierService(svcNode.Barrier))
	}
	if svcNode.Ceremony != nil {
		opts = append(opts, server.WithCeremonyService(svcNode.Ceremony))
	}
	if svcNode.Tenant != nil {
		opts = append(opts, server.WithTenantService(svcNode.Tenant))
	}
	if svcNode.Credential != nil {
		opts = append(opts, server.WithCredentialService(svcNode.Credential))
	}
	if svcNode.CertProv != nil {
		opts = append(opts, server.WithCertProvider(svcNode.CertProv))
	}
	if svcNode.Embedding != nil {
		opts = append(opts, server.WithEmbedding(svcNode.Embedding))
	}
	if svcNode.RAG != nil {
		opts = append(opts, server.WithRAG(svcNode.RAG))
	}
	// Cluster-operations services. Without these the node's own server exposes
	// cluster status/membership (admin) but not shard rebalance or backup, so an
	// embedder (or qrdbd) that hosts this server could not reach them. Wired here
	// so they are served across every transport like any other capability.
	if svcNode.Rebalance != nil {
		opts = append(opts, server.WithRebalanceService(svcNode.Rebalance))
	}
	if svcNode.Backup != nil {
		opts = append(opts, server.WithBackupService(svcNode.Backup))
	}
	// Caller opts last: allow overriding any of the above.
	return append(opts, callerOpts...)
}

// resolveBackend returns the storage backend type and data directory derived
// from the config. An explicitly configured backend (databases[0].backend)
// always wins; otherwise persistence defaults to PebbleDB. An in-memory store
// is never selected implicitly — callers must opt in via
// databases[0].backend="memory". Any persistent backend requires a data
// directory; its absence is a misconfiguration rather than a silent fallback
// to an ephemeral in-memory store.
func resolveBackend(cfg *qrdbconfig.Config) (database.BackendType, string, error) {
	dataDir := cfg.Raft.DataDir

	bt := database.BackendPebble
	if len(cfg.Databases) > 0 {
		if explicit := database.BackendType(cfg.Databases[0].Backend); explicit != "" {
			bt = explicit
		}
	}

	if bt != database.BackendMemory && dataDir == "" {
		return "", "", &ConfigError{
			Field:   "raft.data_dir",
			Message: `data directory is required for persistent storage; set databases[0].backend="memory" to opt into an ephemeral in-memory store`,
		}
	}
	return bt, dataDir, nil
}

// createNodeStorage creates the appropriate storage backend via the
// canonical raft-free composition in pkg/server/lifecycle, shared with the
// daemon's mode:standalone branch and the embedded transport. An
// unrecognized backend type falls back to pebble for backward
// compatibility with this constructor's historical behavior.
func createNodeStorage(backendType database.BackendType, dataDir string, dataPlaneFS vfs.FS, objCfg qrdbconfig.ObjstoreConfig) (storage.Storage, *pebble.DB, error) {
	switch backendType {
	case database.BackendMemory, database.BackendFile, database.BackendObjstore:
	default:
		backendType = database.BackendPebble
	}
	return lifecycle.NewLocalStorage(backendType, dataDir, dataPlaneFS, objCfg)
}

// createNodeEngine creates the appropriate store engine via the canonical
// raft-free composition in pkg/server/lifecycle (see
// lifecycle.NewLocalEngine for the engine-boundary and watch-hub
// contracts).
func createNodeEngine(backendType database.BackendType, stg storage.Storage, pdb *pebble.DB, hub *watch.Hub) (backend.SMAdapter, error) {
	return lifecycle.NewLocalEngine(backendType, stg, pdb, hub)
}

// systemShardReadyTimeout is the maximum duration completeClusteredBoot waits
// for the system shard to elect a leader before building tenant infrastructure.
// Mirrors tenantShardLeaderTimeout in pkg/server/tenant_host.go.
const systemShardReadyTimeout = 15 * time.Second

// waitSystemShardReady blocks until the system shard has a leader or the
// deadline elapses. It is called inside completeClusteredBoot before
// BuildTenantInfra so that reattachRaftTenants can safely issue a linearizable
// read against the replicated tenant store. The daemon relies on other
// initialization steps (REST/gRPC/QUIC setup) to provide this window; the
// embedded node must wait explicitly because boot completes without those.
func waitSystemShardReady(host *quicraft.Host, shardID uint64) error {
	deadline := time.Now().Add(systemShardReadyTimeout)
	for {
		if _, ok, err := host.GetLeaderID(shardID); err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			return &HostCreateError{Err: errors.New("system shard leader election timeout")}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// buildClusteredHost creates the quicraft.Host and starts the KV shard.
// A single-entry members map uses WithoutTransport (no network required).
// members is passed directly to host.StartShard as the initial member set.
// mtls provides the PEM-encoded mTLS certificate material for multi-node
// clusters; it is ignored for single-node (WithoutTransport) clusters.
func buildClusteredHost(
	_ context.Context,
	cfg *qrdbconfig.Config,
	members map[uint64]string,
	createFn sm.CreateFunc,
	mtls *quicraftconfig.MTLSConfig,
) (*quicraft.Host, error) {
	dataDir := cfg.Raft.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}

	walDir := filepath.Join(dataDir, "wal")
	nodeHostDir := filepath.Join(dataDir, "nodehost")

	raftAddress := cfg.Raft.RaftAddress
	listenAddress := cfg.Raft.ListenAddress
	// For single-node (WithoutTransport) clusters, quicraft still requires
	// a non-empty RaftAddress and ListenAddress for host config validation,
	// but they are never used for network I/O. Apply safe defaults so that
	// in-process single-node clusters (dev/test) need not configure them.
	if len(members) == 1 {
		if raftAddress == "" {
			raftAddress = "127.0.0.1:63901"
		}
		if listenAddress == "" {
			listenAddress = raftAddress
		}
	}

	deploymentID := cfg.Raft.DeploymentID
	if deploymentID == 0 {
		deploymentID = 1
	}

	hostCfg := quicraftconfig.HostConfig{
		WALDir:                walDir,
		NodeHostDir:           nodeHostDir,
		RaftAddress:           raftAddress,
		ListenAddress:         listenAddress,
		DeploymentID:          deploymentID,
		MaxTotalInMemLogSize:  cfg.Raft.MaxTotalInMemLogSize,
		AllowZeroDeploymentID: deploymentID == 0,
		// Disable WAL fdatasync when QRDB_NOSYNC_WRITES=1 to avoid
		// Docker overlay2 fsync latency issues in integration tests.
		NoSyncWAL: os.Getenv("QRDB_NOSYNC_WRITES") == "1",
	}

	var hostOpts []quicraft.HostOption
	if len(members) == 1 {
		hostOpts = append(hostOpts, quicraft.WithoutTransport())
	} else if mtls != nil {
		// Multi-node: inject mTLS material into the transport config so all
		// inter-node QUIC connections are mutually authenticated.
		hostCfg.TransportConfig.MTLSConfig = mtls
	}

	host, err := quicraft.NewHost(hostCfg, hostOpts...)
	if err != nil {
		return nil, err
	}

	shardCfg := quicraftconfig.Config{
		ShardID:            cfg.Raft.ShardID,
		ReplicaID:          cfg.Raft.ReplicaID,
		MaxInMemLogSize:    cfg.Raft.MaxInMemLogSize,
		SnapshotEntries:    cfg.Raft.SnapshotEntries,
		CompactionOverhead: cfg.Raft.CompactionOverhead,
	}
	if shardCfg.ShardID == 0 {
		shardCfg.ShardID = shard.ShardKV
	}
	if shardCfg.ReplicaID == 0 {
		shardCfg.ReplicaID = 1
	}

	if err := host.StartShard(members, cfg.Raft.Join, createFn, shardCfg); err != nil {
		_ = host.Close()
		return nil, err
	}

	return host, nil
}

// buildNodeServerConfig constructs a *qrdbconfig.Config suitable for
// server.New. Safe defaults are applied for any unset fields.
func buildNodeServerConfig(cfg *qrdbconfig.Config) *qrdbconfig.Config {
	serverCfg := *cfg // shallow copy; caller owns the original
	if serverCfg.Server.AdminPort == 0 {
		serverCfg.Server.AdminPort = 63001
	}
	if serverCfg.Server.GRPCPort == 0 {
		serverCfg.Server.GRPCPort = 63002
	}
	if serverCfg.Server.Host == "" {
		serverCfg.Server.Host = "127.0.0.1"
	}
	return &serverCfg
}

// closeSeal seals all barriers, closes the seal store and the volume-pool
// state store, and clears all seal-layer references. Called on error paths
// before the node is returned to the caller. Sealing first matters on the
// post-auto-unseal failure paths: the barrier may hold live key material in
// memguard enclaves, and Seal wipes it deterministically (idempotent when
// the barrier never unsealed).
func (n *Node) closeSeal() {
	barrierRegistry := n.barrierRegistry.Swap(nil)
	barrier := n.barrier.Swap(nil)
	if barrierRegistry != nil {
		_ = barrierRegistry.SealAll()
	} else if barrier != nil {
		_ = barrier.Seal()
	}
	n.credCoreSvc = nil
	if n.volumePool != nil {
		if n.volumePool.PerTenantRegistry != nil {
			_ = n.volumePool.PerTenantRegistry.CloseAll(context.Background())
		}
		_ = n.volumePool.Close()
		n.volumePool = nil
	}
	if n.sealStore != nil {
		_ = n.sealStore.Close()
		n.sealStore = nil
	}
}

// KVService returns the node's KVServicer. Convenience accessor so
// embedders do not need to dig through Services().
func (n *Node) KVService() service.KVServicer {
	svcNode := n.svcNode.Load()
	if svcNode == nil {
		return nil
	}
	return svcNode.KV
}

// AdminService returns the node's AdminServicer.
func (n *Node) AdminService() service.AdminServicer {
	svcNode := n.svcNode.Load()
	if svcNode == nil {
		return nil
	}
	return svcNode.Admin
}

// RebalanceService returns the node's RebalanceServicer (shard stats, plan,
// execute, imbalance report), or nil if the node has no service layer. It pairs
// with AdminService for composers that host qrdb's control plane directly.
func (n *Node) RebalanceService() service.RebalanceServicer {
	svcNode := n.svcNode.Load()
	if svcNode == nil {
		return nil
	}
	return svcNode.Rebalance
}

// UserAuthFactorService returns the UserAuthFactor service instance the
// unified-login ceremony resolves against. Out-of-process composers wire
// this into their own embedded transport so factor ENROLLMENT writes to the
// exact store instance login READS from — eliminating the split-store class
// of bug where an enrolled factor is never resolvable at login time.
//
// Returns nil when the node was built without a User service (the login
// ceremony is only wired when svcNode.User != nil) or before Unseal has
// completed the boot.
func (n *Node) UserAuthFactorService() service.UserAuthFactorServicer {
	if p := n.loginFactorSvc.Load(); p != nil {
		return *p
	}
	return nil
}

// AuthFactorService returns the AuthFactorService instance shared by the
// embedded transport and all wire protocol servers. Composers (e.g.
// go-quicbaas) use this to enroll hardware factors (FIDO2/PIV/PKCS#11)
// into the SAME service instance that the API surface verifies against,
// eliminating the class of bug where an enrollment is never visible to
// the challenge/verify path.
//
// Returns nil when the node was constructed without a FactorRegistry or
// explicit WithAuthFactorService option, or before wireClientAndServer
// completes (sealed state).
func (n *Node) AuthFactorService() service.AuthFactorServicer {
	if p := n.authFactorSvc.Load(); p != nil {
		return *p
	}
	return nil
}

// UserService returns the node's UserServicer. Convenience accessor that
// pairs with UserAuthFactorService so composers can resolve a subject
// (tenant + username) to a uint64 user id against the same user store the
// login ceremony uses.
func (n *Node) UserService() user.UserServicer {
	svcNode := n.svcNode.Load()
	if svcNode == nil {
		return nil
	}
	return svcNode.User
}
