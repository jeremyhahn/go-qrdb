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

	"log/slog"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	objstore "github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

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

// WithRaftMTLS injects PEM-encoded mTLS certificate material into the
// quicraft Raft transport. It is only consulted for multi-node clustered
// nodes (len(members) > 1); single-node clusters use WithoutTransport and
// require no TLS material. Callers are responsible for loading the PEM
// bytes from the operator-supplied certificate files.
func WithRaftMTLS(mtls *quicraftconfig.MTLSConfig) NodeOption {
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
}

// close releases the seal store. Safe to call when sealStore is nil.
func (s *nodeSealSubsystem) close() error {
	if s == nil || s.sealStore == nil {
		return nil
	}
	return s.sealStore.Close()
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
type Node struct {
	srv     *server.Server
	client  *sdk.Client
	svcNode *lifecycle.Node
	host    quicraftCloser // nil for standalone
	stg     storage.Storage
	// pebbleDB is the main data-plane pebble instance; nil for memory/file
	// backends and for sealed nodes before Unseal completes.
	pebbleDB *pebble.DB
	// sealStore is the dedicated PebbleDB for the seal sub-store; it must
	// outlive the barrier and the data-plane PebbleDB.
	sealStore       *pebbleStorage.PebbleStorage
	barrier         *seal.StorageBarrier
	barrierRegistry *seal.BarrierRegistry
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
	loginFactorSvc service.UserAuthFactorServicer

	// pendingStandalone is non-nil when the node was constructed with a sealed
	// barrier from a passphrase-missing or Shamir strategy. Unseal consumes it
	// to complete the standalone boot.
	pendingStandalone *pendingStandaloneBoot

	// pendingClustered is non-nil when the clustered node was constructed with
	// a sealed barrier. Unseal consumes it to complete the clustered boot.
	pendingClustered *pendingClusteredBoot
}

// quicraftCloser is satisfied by *quicraft.Host and allows tests to inject
// a stub without importing the quicraft package at the call site.
type quicraftCloser interface {
	Close() error
}

// Server returns the started multi-protocol server. Returns nil when the
// node is in the sealed state (Unseal has not yet been called).
func (n *Node) Server() *server.Server { return n.srv }

// Client returns the in-process SDK client connected to this node. Returns
// nil when the node is in the sealed state.
func (n *Node) Client() *sdk.Client { return n.client }

// Services returns the lifecycle.Node that aggregates all service instances.
// Embedders that need direct service references (e.g. to wire additional
// protocol servers) use this accessor. When the node is in the sealed state
// this returns a Node that has only the seal-layer services populated (Barrier,
// Ceremony, Credential); data-plane services are nil.
func (n *Node) Services() *lifecycle.Node { return n.svcNode }

// IsSealed reports whether the node is in the sealed state, meaning the data
// plane has not yet been started because the seal barrier requires operator
// credentials to unseal. Call Unseal to complete the boot.
func (n *Node) IsSealed() bool {
	if n.barrier == nil {
		return false
	}
	return n.barrier.IsSealed()
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
	if n.barrier == nil {
		return nil // seal not enabled
	}
	if !n.barrier.IsSealed() {
		if n.pendingStandalone != nil || n.pendingClustered != nil {
			// Barrier already unsealed externally (e.g. via the ceremony API)
			// but completeBoot hasn't run yet. Fall through.
		} else {
			return nil // fully booted
		}
	}

	// Attempt to unseal the barrier if it is still sealed.
	if n.barrier.IsSealed() {
		if initErr := n.barrier.Initialize(ctx, creds); initErr != nil {
			if !errors.Is(initErr, seal.ErrAlreadyInitialized) {
				return &UnsealError{Err: initErr}
			}
			// Already initialized: attempt standard unseal.
			if unsealErr := n.barrier.Unseal(ctx, creds); unsealErr != nil {
				if !errors.Is(unsealErr, seal.ErrAlreadyUnsealed) {
					return &UnsealError{Err: unsealErr}
				}
			}
		}
	}

	if n.barrier.IsSealed() {
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

	var dataPlaneFS vfs.FS
	if n.barrier != nil && !n.barrier.IsSealed() && pending.backendType == database.BackendPebble {
		dataPlaneFS = encfs.New(vfs.Default, n.barrier)
	}

	stg, pdb, err := createNodeStorage(pending.backendType, pending.dataDir, dataPlaneFS, pending.cfg.Storage.Objstore)
	if err != nil {
		return &StorageCreateError{Backend: string(pending.backendType), Err: err}
	}
	n.stg = stg
	n.pebbleDB = pdb

	smAdapter, err := createNodeEngine(pending.backendType, stg, pdb)
	if err != nil {
		_ = n.stg.Close()
		n.stg = nil
		return &EngineCreateError{Engine: "mvcc", Err: err}
	}

	localHost := backend.NewLocalBackend(smAdapter)
	watchHub := watch.NewHub()

	var encryptor *atrest.Encryptor
	if n.barrier != nil {
		encryptor = atrest.New(n.barrier)
	}

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              localHost,
		WatchHub:          watchHub,
		DataDir:           pending.dataDir,
		ReplicaID:         pending.cfg.Raft.ReplicaID,
		Logger:            n.logger,
		Encryptor:         encryptor,
		SealBarrier:       n.barrier,
		BarrierRegistry:   n.barrierRegistry,
		CredentialCoreSvc: n.credCoreSvc,
		// Forward an injected (xkms) seal StrategyRegistry into the
		// TenantService so per-tenant hardware/cloud seal strategies resolve.
		// This must happen here (before BuildServices), not via server.New,
		// because the TenantService is constructed inside BuildServices.
		StrategyRegistry: server.ResolveCryptoOptions(pending.serverOpts...).StrategyRegistry,
	})
	if err != nil {
		_ = localHost.Close()
		_ = n.stg.Close()
		n.stg = nil
		return &ServicesCreateError{Err: err}
	}
	n.svcNode = svcNode

	if err := n.wireClientAndServer(ctx, svcNode, pending.cfg, pending.serverOpts); err != nil {
		_ = svcNode.Stop(context.Background())
		_ = localHost.Close()
		_ = n.stg.Close()
		n.stg = nil
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

// completeClusteredBoot finishes wiring the clustered node after the barrier
// is unsealed: creates the quicraft host, calls BuildServices, and wires the
// server.
func (n *Node) completeClusteredBoot(ctx context.Context, pending *pendingClusteredBoot) error {
	n.pendingClustered = nil

	var encryptor *atrest.Encryptor
	if n.barrier != nil {
		encryptor = atrest.New(n.barrier)
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

	hostAdapter := raftbackend.New(host)

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              hostAdapter,
		WatchHub:          watchHub,
		DataDir:           pending.cfg.Raft.DataDir,
		ReplicaID:         pending.cfg.Raft.ReplicaID,
		Logger:            n.logger,
		Encryptor:         encryptor,
		SealBarrier:       n.barrier,
		BarrierRegistry:   n.barrierRegistry,
		CredentialCoreSvc: n.credCoreSvc,
		// Forward an injected (xkms) seal StrategyRegistry into the
		// TenantService (constructed inside BuildServices) so per-tenant
		// hardware/cloud seal strategies resolve in clustered mode too.
		StrategyRegistry: server.ResolveCryptoOptions(pending.serverOpts...).StrategyRegistry,
	})
	if err != nil {
		_ = host.Close()
		return &ServicesCreateError{Err: err}
	}
	n.svcNode = svcNode

	if err := n.wireClientAndServer(ctx, svcNode, pending.cfg, pending.serverOpts); err != nil {
		_ = svcNode.Stop(context.Background())
		_ = host.Close()
		return err
	}
	return nil
}

// Close tears down all node resources in LIFO order. Nil fields are skipped.
func (n *Node) Close() error {
	var firstErr error

	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if n.client != nil {
		record(n.client.Close())
		n.client = nil
	}
	if n.srv != nil {
		n.srv.Stop()
		n.srv = nil
	}
	if n.svcNode != nil {
		record(n.svcNode.Stop(context.Background()))
		n.svcNode = nil
	}
	if n.host != nil {
		record(n.host.Close())
		n.host = nil
	}
	n.barrier = nil
	n.barrierRegistry = nil
	n.credCoreSvc = nil
	if n.sealStore != nil {
		record(n.sealStore.Close())
		n.sealStore = nil
	}
	if n.stg != nil {
		record(n.stg.Close())
		n.stg = nil
		n.pebbleDB = nil
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
		logger:               log,
		sessionIssuer:        nOpts.sessionIssuer,
		loginFactorVerifiers: nOpts.loginFactorVerifiers,
	}

	backendType, dataDir, err := resolveBackend(cfg)
	if err != nil {
		return nil, err
	}

	// Build the seal subsystem BEFORE data-plane storage so that, once
	// unsealed, the data-plane PebbleDB can be opened on top of encfs.
	if cfg.Seal.Enabled {
		sub, err := buildNodeSealSubsystem(ctx, cfg, dataDir, log, nOpts)
		if err != nil {
			return nil, err
		}
		n.barrier = sub.barrier
		n.barrierRegistry = sub.barrierRegistry
		n.sealStore = sub.sealStore
		n.credCoreSvc = sub.credCoreSvc

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
		n.svcNode = sealNode

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

	smAdapter, err := createNodeEngine(backendType, stg, pdb)
	if err != nil {
		_ = n.stg.Close()
		return nil, &EngineCreateError{Engine: "mvcc", Err: err}
	}

	localHost := backend.NewLocalBackend(smAdapter)
	watchHub := watch.NewHub()

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:      localHost,
		WatchHub:  watchHub,
		DataDir:   dataDir,
		ReplicaID: cfg.Raft.ReplicaID,
		Logger:    log,
		// Forward an injected (xkms) seal StrategyRegistry into the
		// TenantService even on the no-seal data path, so the option a
		// composer passes via server.WithStrategyRegistry is honored rather
		// than silently dropped.
		StrategyRegistry: server.ResolveCryptoOptions(serverOpts...).StrategyRegistry,
	})
	if err != nil {
		_ = localHost.Close()
		_ = n.stg.Close()
		return nil, &ServicesCreateError{Err: err}
	}
	n.svcNode = svcNode

	if err := n.wireClientAndServer(ctx, svcNode, cfg, serverOpts); err != nil {
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
		logger:               log,
		sessionIssuer:        nOpts.sessionIssuer,
		loginFactorVerifiers: nOpts.loginFactorVerifiers,
	}

	dataDir := cfg.Raft.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}

	// Build the seal subsystem BEFORE host construction so encfs wraps the
	// state machine's Pebble stores once the barrier is unsealed.
	if cfg.Seal.Enabled {
		sub, err := buildNodeSealSubsystem(ctx, cfg, dataDir, log, nOpts)
		if err != nil {
			return nil, err
		}
		n.barrier = sub.barrier
		n.barrierRegistry = sub.barrierRegistry
		n.sealStore = sub.sealStore
		n.credCoreSvc = sub.credCoreSvc

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
		n.svcNode = sealNode

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
	n.svcNode = svcNode

	if err := n.wireClientAndServer(ctx, svcNode, cfg, serverOpts); err != nil {
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
func buildNodeSealSubsystem(ctx context.Context, cfg *qrdbconfig.Config, dataDir string, log *slog.Logger, nOpts *nodeOptions) (*nodeSealSubsystem, error) {
	sealDataDir := filepath.Join(dataDir, "seal")
	sealStore, err := pebbleStorage.New(pebbleStorage.DefaultConfig(sealDataDir))
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

	return &nodeSealSubsystem{
		barrier:         barrier,
		credCoreSvc:     credCoreSvc,
		barrierRegistry: barrierRegistry,
		sealStore:       sealStore,
		sealDataDir:     sealDataDir,
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
		creds = seal.Credentials{Secret: string(passphrase)}

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
func (n *Node) wireClientAndServer(ctx context.Context, svcNode *lifecycle.Node, cfg *qrdbconfig.Config, callerOpts []server.ServerOption) error {
	// Resolve the caller-supplied audit logger so the same instance backs
	// both the multi-protocol server and the in-process client.Audit().
	n.auditLogger = server.ResolveCryptoOptions(callerOpts...).AuditLogger

	// Build in-process transport.
	// RBAC posture mirrors the standalone daemon EXACTLY
	// (cmd/qrdbd/start.go): config can only RAISE strictness and a
	// FIPS 140-3 build (crypto/fips140.Enabled()) is always strict
	// regardless of config, so embedded and standalone authorize
	// identically. The Auth.SeparationOfDuties flag is the same source
	// the daemon reads (cfg.Auth.SeparationOfDuties).
	strictSoD := fips140.Enabled() || cfg.Auth.SeparationOfDuties
	embeddedOpts, err := n.buildEmbeddedOpts(svcNode, strictSoD)
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
	n.client = client

	// Build multi-protocol server.
	serverCfg := buildNodeServerConfig(cfg)
	allOpts := buildServerOpts(svcNode, callerOpts)
	if n.barrier != nil {
		allOpts = append(allOpts, server.WithSealedWindowProbe(n.barrier))
	}
	srv := server.New(serverCfg, svcNode.KV, svcNode.Admin, allOpts...)
	if err := srv.Start(); err != nil {
		_ = client.Close()
		n.client = nil
		return &ServerCreateError{Err: err}
	}
	n.srv = srv
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
func (n *Node) buildEmbeddedOpts(svcNode *lifecycle.Node, strictSoD bool) ([]embedded.Option, error) {
	opts := []embedded.Option{
		embedded.WithMVCCService(svcNode.MVCC),
		embedded.WithTransactionService(svcNode.Transaction),
		embedded.WithBatchService(svcNode.Batch),
		embedded.WithLeaseService(svcNode.Lease),
		embedded.WithIndexService(svcNode.Index),
		embedded.WithTimeSeriesService(svcNode.TimeSeries),
		embedded.WithWatchService(svcNode.Watch),
		embedded.WithVectorService(svcNode.Vector),
		embedded.WithEmbeddingService(svcNode.Embedding),
		embedded.WithRAGService(svcNode.RAG),
		embedded.WithDatabaseService(svcNode.Database),
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
		opts = append(opts, embedded.WithRBACAuthorizer(rbacAuthz))
	}
	if svcNode.Barrier != nil {
		opts = append(opts, embedded.WithBarrierService(svcNode.Barrier))
	}
	if n.auditLogger != nil {
		opts = append(opts, embedded.WithAuditLogger(n.auditLogger))
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
	n.loginFactorSvc = factorSvc

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
func buildServerOpts(svcNode *lifecycle.Node, callerOpts []server.ServerOption) []server.ServerOption {
	opts := []server.ServerOption{
		server.WithBatch(svcNode.Batch),
		server.WithTransaction(svcNode.Transaction),
		server.WithMVCC(svcNode.MVCC),
		server.WithLease(svcNode.Lease),
		server.WithIndex(svcNode.Index),
		server.WithTimeSeries(svcNode.TimeSeries),
		server.WithWatch(svcNode.Watch),
		server.WithLockService(svcNode.Lock),
		server.WithCollectionService(svcNode.Collection),
		server.WithDatabaseService(svcNode.Database),
		server.WithVector(svcNode.Vector),
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

// createNodeStorage creates the appropriate storage backend, mirroring
// embedded/server.go createServerStorage.
func createNodeStorage(backendType database.BackendType, dataDir string, dataPlaneFS vfs.FS, objCfg qrdbconfig.ObjstoreConfig) (storage.Storage, *pebble.DB, error) {
	switch backendType {
	case database.BackendMemory:
		return memory.NewStorage(), nil, nil

	case database.BackendFile:
		stg, err := file.New(filepath.Join(dataDir, "file"))
		if err != nil {
			return nil, nil, err
		}
		return stg, nil, nil

	case database.BackendObjstore:
		provider, settings := resolveObjstoreSettings(objCfg, dataDir)
		stg, err := objstore.New(provider, settings)
		if err != nil {
			return nil, nil, err
		}
		return stg, nil, nil

	default: // BackendPebble
		pebbleDir := filepath.Join(dataDir, "pebble")
		nodeCfg := pebbleStorage.DefaultConfig(pebbleDir)
		if dataPlaneFS != nil {
			nodeCfg.FS = dataPlaneFS
		}
		stg, err := pebbleStorage.New(nodeCfg)
		if err != nil {
			return nil, nil, err
		}
		return stg, stg.DB(), nil
	}
}

// resolveObjstoreSettings derives the go-objstore provider and settings for an
// objstore-backed node. An empty provider defaults to "local"; for the local
// provider an empty "path" is rooted at <dataDir>/objstore so the backend works
// out of the box. The caller's settings map is never mutated.
func resolveObjstoreSettings(objCfg qrdbconfig.ObjstoreConfig, dataDir string) (string, map[string]string) {
	provider := objCfg.Provider
	if provider == "" {
		provider = "local"
	}
	settings := make(map[string]string, len(objCfg.Settings)+1)
	for k, v := range objCfg.Settings {
		settings[k] = v
	}
	if provider == "local" && settings["path"] == "" {
		settings["path"] = filepath.Join(dataDir, "objstore")
	}
	return provider, settings
}

// createNodeEngine creates the appropriate store engine, mirroring
// embedded/server.go createServerEngine.
func createNodeEngine(backendType database.BackendType, stg storage.Storage, pdb *pebble.DB) (backend.SMAdapter, error) {
	if backendType == database.BackendFile || backendType == database.BackendObjstore {
		return engine.NewKVEngine(stg), nil
	}

	mvccOpts := []engine.MVCCOption{
		engine.WithMVCCLessor(lease.NewLessor()),
	}
	if pdb != nil {
		mvccOpts = append(mvccOpts, engine.WithMVCCPebbleDB(pdb))
		if tsStorage, err := timeseries.New(pdb); err == nil {
			mvccOpts = append(mvccOpts, engine.WithMVCCTimeSeries(tsStorage))
		}
	}

	eng, err := engine.NewMVCCEngine(stg, mvccOpts...)
	if err != nil {
		return nil, err
	}
	return eng, nil
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

// closeSeal closes the seal store and clears the seal references.
func (n *Node) closeSeal() {
	n.barrier = nil
	n.barrierRegistry = nil
	n.credCoreSvc = nil
	if n.sealStore != nil {
		_ = n.sealStore.Close()
		n.sealStore = nil
	}
}

// KVService returns the node's KVServicer. Convenience accessor so
// embedders do not need to dig through Services().
func (n *Node) KVService() service.KVServicer {
	if n.svcNode == nil {
		return nil
	}
	return n.svcNode.KV
}

// AdminService returns the node's AdminServicer.
func (n *Node) AdminService() service.AdminServicer {
	if n.svcNode == nil {
		return nil
	}
	return n.svcNode.Admin
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
	return n.loginFactorSvc
}

// UserService returns the node's UserServicer. Convenience accessor that
// pairs with UserAuthFactorService so composers can resolve a subject
// (tenant + username) to a uint64 user id against the same user store the
// login ceremony uses.
func (n *Node) UserService() user.UserServicer {
	if n.svcNode == nil {
		return nil
	}
	return n.svcNode.User
}
