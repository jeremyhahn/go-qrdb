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
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"log/slog"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
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
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// WithAuthenticators appends embedder-supplied authenticators to the node's
// auth middleware chain, AFTER the built-in mTLS authenticator
// (first-success-wins, so mTLS peers keep their existing behavior and
// requests without a client certificate fall through to these). This is the
// extension seam for credential schemes qrdb deliberately does not
// implement itself — go-quicbaas passes its OAuth 2.1 bearer-token
// validator here when embedding qrdb, per the MCP authorization
// specification. The caller's Authorization value reaches the authenticator
// via auth.HTTPAuthorizationFromContext on REST and MCP (the Authorization
// header) and on gRPC and Unix (the "authorization" metadata). QUIC's request
// frame carries no credential, so a bearer cannot authenticate there.
//
// A bearer minted by the host's own login endpoints carries no login method
// the chain can check against the account's permitted methods; such an
// authenticator implements auth.UnobservableMethodAuthenticator and the host
// enforces the policy when it issues the token.
func WithAuthenticators(authenticators ...auth.Authenticator) NodeOption {
	return func(o *nodeOptions) {
		o.extraAuthenticators = append(o.extraAuthenticators, authenticators...)
	}
}

// WithPeerAttestationGate installs the attested-caller gate on the node's
// network transports (REST, gRPC, unix, QUIC, MCP): for the gate's
// permissions, a request RBAC granted is admitted only when the gate's
// verifier reports the peer on the connection as currently attested
// (go-qrdb#278). qrdb owns only this seam; the verifier is go-xkms's. Build
// the gate with middleware.NewPeerAttestationGate (sdk.NewPeerAttestationGate).
// The in-process embedded transport has no peer and is not gated.
func WithPeerAttestationGate(gate *middleware.PeerAttestationGate) NodeOption {
	return func(o *nodeOptions) { o.peerAttestation = gate }
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

	// peerAttestation is the attested-caller gate installed on every network
	// transport's auth middleware. See WithPeerAttestationGate.
	peerAttestation *middleware.PeerAttestationGate

	// clusterCAKeyProvider, clusterCACertPEM, clusterCAKeyPEM mirror the
	// Node fields of the same name. See WithClusterCAKeyProvider and
	// WithClusterCAMaterial.
	clusterCAKeyProvider cakey.Provider
	clusterCACertPEM     []byte
	clusterCAKeyPEM      []byte

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

// WithClusterCAKeyProvider injects a cakey.Provider that replaces the
// built-in cluster CA (Key A) entirely: the node never constructs a
// cakey.SoftwareProvider and never reads or writes its replicated-store
// record. This is the seam an embedder (go-xkms) uses to back Key A with an
// HSM, TPM or cloud KMS, via cakey.Adapter. When set, WithClusterCAMaterial
// is ignored. See docs/certificate-authority/architecture.md, "The CA key
// provider seam".
func WithClusterCAKeyProvider(p cakey.Provider) NodeOption {
	return func(o *nodeOptions) { o.clusterCAKeyProvider = p }
}

// WithClusterCAMaterial supplies an in-memory cluster CA (Key A) generated
// before any store existed -- the bootstrap crash window. It is how an
// embedder that drives its own cluster-TLS bootstrap (sdk/go/clusterenroll)
// hands the first node's freshly generated CA to the built-in
// cakey.SoftwareProvider for adoption into the replicated store. Both
// arguments are typically empty on every boot after the embedder's local
// cache of the key is confirmed persisted and deleted: see
// cakey.SoftwareProvider.AdoptIfAbsent. Ignored when
// WithClusterCAKeyProvider is set.
func WithClusterCAMaterial(caCertPEM, caKeyPEM []byte) NodeOption {
	return func(o *nodeOptions) {
		o.clusterCACertPEM = caCertPEM
		o.clusterCAKeyPEM = caKeyPEM
	}
}

// MemberIdentityFromPEM returns the Raft member identity of the node whose
// transport certificate (the Cert of its RaftMTLSConfig) is certPEM. A
// multi-node cluster lists every OTHER replica's identity in
// cfg.Raft.MemberIdentities as "replicaID=identity"; the local replica's own
// entry is derived from its RaftMTLSConfig and may be omitted. See
// cluster.MemberIdentityFromPEM, which this delegates to.
func MemberIdentityFromPEM(certPEM []byte) (string, error) {
	return cluster.MemberIdentityFromPEM(certPEM)
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
	// sealStore is typed as the interface Node.sealStore holds, not as the
	// concrete store buildNodeSealSubsystem constructs: adoptSealSubsystem
	// copies it across, and copying a nil CONCRETE pointer into an interface
	// field would produce a non-nil interface wrapping a nil receiver -- the
	// exact shape Node.sealStore's nil checks exist to rule out.
	sealStore   seal.LegacyStorage
	sealDataDir string
	// pinBackend is the software PIN backend over the node-local seal store
	// (lifecycle.NewSealStorePINBackend). It is the node's PIN backend
	// unless the caller injects one through server.WithPINBackend.
	pinBackend pin.PINBackend
	// volumePool is non-nil when the config declares at least one volume pool.
	// It is closed during node shutdown after the per-tenant registry is
	// drained, mirroring the daemon's shutdown ordering.
	volumePool *qrdbpool.VolumePoolSubsystem
}

// adoptSealSubsystem installs sub's seal-layer objects on n. Both node
// builders go through it, so there is one assignment of each field and in
// particular one place where the seal store crosses into Node.sealStore.
func (n *Node) adoptSealSubsystem(sub *nodeSealSubsystem) {
	n.barrier.Store(sub.barrier)
	n.barrierRegistry.Store(sub.barrierRegistry)
	n.sealStore = sub.sealStore
	n.credCoreSvc = sub.credCoreSvc
	n.volumePool = sub.volumePool
	if sub.pinBackend != nil {
		n.sealPINBackend = sub.pinBackend
	}
}

// pinBackend returns the node's PIN backend: the one the caller injected
// through server.WithPINBackend, else the software backend over the
// node-local seal store, else nil — in which case every PIN-gated operation
// fails closed.
func (n *Node) pinBackend(serverOpts []server.ServerOption) pin.PINBackend {
	if injected := server.ResolveCryptoOptions(serverOpts...).PINBackend; injected != nil {
		return injected
	}
	return n.sealPINBackend
}

// sealedWindowCeremonyCore returns the core ceremony service of the node's
// sealed-window service graph, or nil when there is none. The full boot reuses
// it rather than building a second instance, exactly as the daemon does.
//
// Across an in-process data-plane restart there is no service graph between
// the teardown and the rebuild; the ceremony service the teardown carried is
// returned instead, so the envelopes an encryption migration to Shamir has
// just distributed survive into the rebuilt graph.
func (n *Node) sealedWindowCeremonyCore() *ceremony.CeremonyService {
	prior := n.svcNode.Load()
	if prior == nil || prior.Ceremony == nil {
		return n.carriedCeremony.Load()
	}
	core, _ := service.CeremonyServiceCore(prior.Ceremony)
	return core
}

// withHolderInfra fills infra's share-holder dependencies: the PIN backend,
// and the caller's factor registry, ceremony CA and compliance audit trail.
// Without the CA the ceremony service cannot issue a holder certificate, so
// no share holder could be enrolled on an SDK-built node. The audit trail is
// the caller's own (server.WithAuditLogger), which exists before the node
// does, so the holder events of the sealed window are recorded on it too.
func (n *Node) withHolderInfra(infra lifecycle.Infra, cfg *qrdbconfig.Config, serverOpts []server.ServerOption) (lifecycle.Infra, error) {
	crypto := server.ResolveCryptoOptions(serverOpts...)
	infra.PINBackend = n.pinBackend(serverOpts)
	infra.FactorRegistry = crypto.FactorRegistry
	infra.CeremonyCA = crypto.CeremonyCA
	infra.AuditLogger = crypto.AuditLogger
	// The holder events recorded before they take effect follow the node's
	// one audit failure posture. The sealed window's graph is built before the
	// authorization middleware parses it, so an unrecognised value refuses the
	// graph here -- as it refuses qrdbd's start -- rather than resolving to
	// either posture.
	if cfg != nil {
		policy, err := audit.ParseFailurePolicy(cfg.Auth.AuditFailurePolicy)
		if err != nil {
			return infra, err
		}
		infra.AuditFailurePolicy = policy
	}
	return infra, nil
}

// buildServices builds the full service graph over infra with the node's
// holder infra (withHolderInfra).
func (n *Node) buildServices(ctx context.Context, infra lifecycle.Infra, cfg *qrdbconfig.Config, serverOpts []server.ServerOption) (*lifecycle.Node, error) {
	infra, err := n.withHolderInfra(infra, cfg, serverOpts)
	if err != nil {
		return nil, err
	}
	return lifecycle.BuildServices(ctx, infra)
}

// buildSealServices builds the sealed window's service graph over infra with
// the node's holder infra (withHolderInfra).
func (n *Node) buildSealServices(ctx context.Context, infra lifecycle.Infra, cfg *qrdbconfig.Config, serverOpts []server.ServerOption) (*lifecycle.Node, error) {
	infra, err := n.withHolderInfra(infra, cfg, serverOpts)
	if err != nil {
		return nil, err
	}
	return lifecycle.BuildSealServicesOnly(ctx, infra)
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
// # Lifecycle: Close is ordered against boot and migration
//
// Every data-plane transition runs under one lifecycle gate (lifecycleGate): the
// boot Unseal completes, a system-scope EnableEncryption (tear down, re-key,
// rebuild), and Close. The owner's ruling (go-qrdb#226) is that CLOSE BLOCKS on
// an in-flight boot or migration and then tears down what it built, so:
//
//   - Close concurrent with Unseal: either the boot finishes first and Close
//     tears it all down, or Close runs first and Unseal returns ErrNodeClosed.
//     No accessor reads non-nil after Close returns.
//   - Close concurrent with a system EnableEncryption: the migration either
//     completes and is then torn down, or is refused with ErrNodeClosed. It
//     never runs against a barrier Close has retracted.
//   - Close concurrent with a tenant-scope EnableEncryption: the same. A
//     tenant migration holds the gate SHARED — alongside other tenant
//     migrations and a system migration, each still serialised by its own
//     guard — through the transports' router and the direct call alike
//     (server.TenantMigrationAdmission), and Close waits for every one.
//   - A boot or migration still WAITING for the gate when Close begins is
//     refused with ErrNodeClosed, not queued. Close signals before it waits,
//     which is what keeps a graceful server stop inside Close from waiting on
//     a request that is itself waiting on Close.
//   - Close is idempotent: every call after the first returns nil once the
//     first has finished.
//
// Close can therefore take as long as the boot or migrations it waits for — a
// KMS-backed Unseal included.
//
// Retraction. Close retracts every handle it published before stopping what
// backs them, and teardownDataPlane (the in-process encryption-migration
// recycle) retracts the ones the stopped service graph owns. So every accessor
// reads nil after Close rather than a live-looking handle onto a stopped graph.
//
// The tests enumerate the accessors by REFLECTION over this type rather than by
// hand, so an accessor added later is covered by construction — a hand-list is
// how IsSealed and BarrierRegistry were missed on the first pass at #176.
//
// atomic.Pointer and not a mutex because each handle is an independent
// single-word publication with no invariant spanning two of them, and reads
// outnumber the single write by orders of magnitude — the same reasoning
// pkg/database, pkg/secure and pkg/storage/pool apply to their swappable
// handles. Interface-typed handles are boxed, so the pointer holds *Iface. The
// lifecycle gate above orders the WRITERS; it is never taken by an accessor.
//
// Finally, all of this is about ACQUIRING a handle, never about USING one.
// Calling through a handle you already hold, concurrently with Close, is a
// use-after-close; the node has never promised to survive that and still does
// not.
type Node struct {
	// bindingPolicy is the auth.MTLSBindingPolicy the node's middleware
	// enforces, recorded when it is wired so Node.FirstBoot holds first-boot
	// to the same posture.
	bindingPolicy atomic.Int32

	srv    atomic.Pointer[server.Server]
	client atomic.Pointer[sdk.Client]
	// featureClient is the node's own in-process client for its feature
	// gate. It is not gated on the node's seal probe: the gate's
	// advertisement is node machinery, kept up while the node refuses
	// requests, as cmd/qrdbd keeps it.
	featureClient atomic.Pointer[sdk.Client]
	svcNode       atomic.Pointer[lifecycle.Node]

	// carriedCeremony is the ceremony service a data-plane teardown carried
	// out of the stopped service graph, for the rebuild to reuse. See
	// sealedWindowCeremonyCore.
	carriedCeremony atomic.Pointer[ceremony.CeremonyService]
	host            quicraftCloser // nil for standalone
	stg             storage.Storage
	// pebbleDB is the main data-plane pebble instance; nil for memory/file
	// backends and for sealed nodes before Unseal completes.
	pebbleDB *pebble.DB
	// sealStore is the dedicated store backing the seal sub-store; it must
	// outlive the barrier and the data-plane PebbleDB. Typed as
	// seal.LegacyStorage -- the interface seal.NewStorageAdapter already
	// consumes -- rather than the concrete *pebbleStorage.PebbleStorage every
	// current construction path happens to produce it with: a field typed as
	// a concrete pointer lets a genuinely-absent seal store be assigned as a
	// TYPED nil (a nil *pebbleStorage.PebbleStorage boxed into a NON-nil
	// interface at the NewStorageAdapter call site), so `!= nil` checks lie
	// and the read reaches a nil-receiver method on the concrete type
	// instead of refusing cleanly. Declaring the field as the interface
	// itself means a genuinely-absent seal store is nil at the type the
	// field actually holds, so canonicalRootKeyStrategy's nil check (see its
	// doc comment) means what it says.
	sealStore seal.LegacyStorage
	// sealPINBackend is the software PIN backend over the node-local seal
	// store, set when the seal subsystem is adopted. It stays the interface
	// type so a node without one holds a true nil.
	sealPINBackend  pin.PINBackend
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

	// clusterCAKeyProvider, when non-nil (WithClusterCAKeyProvider), replaces
	// the built-in cluster CA (Key A) entirely -- the HSM/TPM/KMS injection
	// seam, threaded into lifecycle.BuildServices.Infra.ClusterCAKeyProvider.
	clusterCAKeyProvider cakey.Provider

	// clusterCACertPEM / clusterCAKeyPEM carry an in-memory cluster CA
	// generated before any store existed -- the bootstrap crash window (see
	// docs/certificate-authority/architecture.md). Set via
	// WithClusterCAMaterial by an embedder driving its own cluster-TLS
	// bootstrap (sdk/go/clusterenroll.Resolve) ahead of calling
	// NewClusteredNode; both are empty once the key is confirmed persisted
	// and the embedder's own local cache of it is gone.
	clusterCACertPEM []byte
	clusterCAKeyPEM  []byte

	// preboundSockets are the listeners and packet conns the caller handed
	// over through server.WithRESTListener / WithRESTPacketConn /
	// WithGRPCListener. Each server lifetime serves on them without closing
	// them, so a data-plane rebuild keeps its ports; Close closes them.
	preboundSockets []io.Closer

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

	// peerAttestation carries WithPeerAttestationGate from the node
	// constructor to wireClientAndServer.
	peerAttestation *middleware.PeerAttestationGate

	// authMethods is THIS node's permitted-method vocabulary, handed to every
	// user store BuildServices creates and bound to the node's own auth
	// middleware once wireClientAndServer builds it. Per node, never
	// process-wide: two nodes in one process have two authenticator chains.
	authMethods middleware.AuthMethodVocabulary

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
	// by the embedded transport and the multi-protocol server. It holds no
	// in-process state: enrollments and pending challenges are persisted in
	// the caller's tenant store, so a challenge issued over one surface
	// verifies over the other.
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

	// lifeGate serializes every data-plane transition: the boot Unseal
	// completes, an in-process system-scope encryption migration (which tears
	// down and rebuilds the data plane), and Close. Close signals closing and
	// then holds its slot for the whole teardown, so it waits out an in-flight
	// boot or migration, neither can republish a handle after it, and one
	// still WAITING is refused with ErrNodeClosed rather than queued
	// (go-qrdb#226). A tenant-scope migration holds it shared (see
	// admitTenantMigration): tenant migrations keep their per-tenant guard
	// and do not wait for each other or for a system migration, but Close
	// waits for them. No accessor takes it. See lifecycleGate.
	lifeGate lifecycleGate

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
	// started during completeClusteredBoot (the lease-expiry sweeper, the
	// reclaim sweeper and, when tenant isolation is wired, the
	// TenantShardReconciler -- see bgWG). It is cancelled by bgCancel in
	// Close() so those goroutines exit promptly rather than blocking on a
	// placement watch or poll timer. Nil on standalone nodes, on clustered
	// nodes where no reconciler was started (no PlacementStore configured),
	// and -- after a failed boot's defer clears it alongside bgCancel -- on a
	// node pending a retried Unseal. background() makes a fresh one only when
	// bgCtx is nil, so clearing both together is what lets a retry get a live
	// context instead of silently reusing the cancelled one every goroutine
	// above would see as already-done.
	bgCtx    context.Context //nolint:containedctx
	bgCancel context.CancelFunc
	// bgWG tracks every goroutine started on bgCtx (lease-expiry sweep,
	// reclaim sweep, tenant shard reconciler) so a failure path can wait for
	// them to actually exit after cancelling bgCtx and before stopping the
	// services they call into -- cancelling alone races the goroutine's next
	// iteration against svcNode.Stop.
	bgWG sync.WaitGroup

	// stopLeaseExpiry ends the lease-expiry sweeper of the current
	// standalone data plane (startLeaseExpiryStandalone) and waits for it to
	// exit; nil on a clustered node, whose sweeper runs on bgCtx. Written
	// only by the lifecycle-gated boot, teardown and Close paths.
	stopLeaseExpiry func()

	// featureGate decides when the clustered host's Raft groups may be sent
	// guarded writes; nil for a host with no transport. featureAdv is its
	// lease-backed advertisement once startFeatureGate has brought it up, and
	// featureWG tracks that goroutine so Close can wait it out.
	featureGate *featuregate.Gate

	// clusterSeal makes seals reach every node of the cluster once the cluster
	// has activated cluster-seal; nil on a node with no barrier or no cluster.
	clusterSeal *clusterseal.Deferred
	featureAdv  atomic.Pointer[cluster.Advertiser]
	featureWG   sync.WaitGroup

	// encMigrator is the scoped encryption migrator built in wireClientAndServer.
	// It routes system scope to the nodeSystemMigrator (which holds the lifecycle gate)
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
	// The boot publishes the data plane; Close must not interleave with it
	// (go-qrdb#226). A Close already under way wins, and this call refuses.
	if err := n.lifeGate.acquire(ctx); err != nil {
		return err
	}
	defer n.lifeGate.release()

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
	lr := lease.NewLessor()
	smAdapter, err := createNodeEngine(pending.backendType, stg, pdb, watchHub, lr)
	if err != nil {
		_ = n.stg.Close()
		n.stg = nil
		return &EngineCreateError{Engine: "mvcc", Err: err}
	}

	localHost := backend.NewLocalBackend(smAdapter)

	dbManager, err := n.openStandaloneDatabaseManager(pending.dataDir)
	if err != nil {
		_ = localHost.Close()
		_ = n.stg.Close()
		n.stg = nil
		return err
	}

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
	profile.WarnUnencryptedAtRest(n.logger)

	svcNode, err := n.buildServices(ctx, lifecycle.Infra{
		Host:              localHost,
		WatchHub:          watchHub,
		DataDir:           pending.dataDir,
		DatabaseManager:   dbManager,
		ReplicaID:         pending.cfg.Raft.ReplicaID,
		Profile:           &profile,
		Logger:            n.logger,
		Encryptor:         encryptor,
		SealBarrier:       barrier,
		SealEnabled:       pending.cfg.Seal.Enabled,
		BarrierRegistry:   barrierRegistry,
		CredentialCoreSvc: n.credCoreSvc,
		// Key A: either an injected HSM/TPM/KMS provider, or in-memory
		// bootstrap material for the built-in cakey.SoftwareProvider to
		// adopt. See WithClusterCAKeyProvider / WithClusterCAMaterial.
		ClusterCAKeyProvider: n.clusterCAKeyProvider,
		ClusterCACertPEM:     n.clusterCACertPEM,
		ClusterCAKeyPEM:      n.clusterCAKeyPEM,
		// The CA mints its client certificates under the same trust domain
		// the mTLS authenticator pins, so the pin is exercised by the
		// certificates this node issues (go-qrdb#519).
		SPIFFETrustDomain: pending.cfg.Auth.SPIFFETrustDomain,
		// Under "required" the user store refuses to withdraw the bootstrap
		// admin's last live binding (go-qrdb#517).
		MTLSBindingPolicy: pending.cfg.Auth.MTLSBindingPolicy,
		// The node's own auth-chain vocabulary (see Node.authMethods).
		AuthMethodVocabulary: n.authMethods.IsAuthMethod,
		// Reuse the sealed-window ceremony service: holder registrations made
		// while sealed are cache-only and distributed shares live in its
		// in-memory enclaves, so a fresh instance would orphan both.
		CeremonyCoreSvc: n.sealedWindowCeremonyCore(),
		// Forward an injected (xkms) seal StrategyRegistry into the
		// TenantService so per-tenant hardware/cloud seal strategies resolve.
		// This must happen here (before BuildServices), not via server.New,
		// because the TenantService is constructed inside BuildServices.
		StrategyRegistry:          server.ResolveCryptoOptions(pending.serverOpts...).StrategyRegistry,
		DefaultTenantEntitlements: n.defaultTenantEntitlements,
	}, pending.cfg, pending.serverOpts)
	if err != nil {
		_ = localHost.Close()
		_ = n.stg.Close()
		n.stg = nil
		n.closeDatabaseManager()
		return &ServicesCreateError{Err: err}
	}
	n.svcNode.Store(svcNode)
	n.startReclaimSweepStandalone(svcNode.ReclaimSweeper)

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
			// The registry outlives this svcNode: a system-scope encryption
			// migration rebuilds the service graph in place and keeps the tenant
			// Hosts running, so the registry resolves the tenant service live
			// rather than capturing this one (go-qrdb#383).
			TenantLookup: &nodeTenantResolver{node: n},
			VolumePool:   n.volumePool,
			Logger:       n.logger,
			// Phase 1: Host, Manager, ReplicaID, RaftAddr, SystemTenant,
			// PlacementStore, MultiplexLogDB are all left zero — IsolationRaft
			// is not wired in the embedded standalone node yet (Phase 2).
		})
		if tiErr != nil {
			_ = svcNode.Stop(context.Background())
			_ = localHost.Close()
			_ = n.stg.Close()
			n.stg = nil
			n.closeDatabaseManager()
			return &TenantInfraError{Err: tiErr}
		}
		n.tenantHosts = ti.Hosts
	} else if n.tenantHosts != nil && svcNode.Tenant != nil {
		// A system-scope recycle rebuilt the tenant service; the runtimes
		// survived it. Hand the new service the closer BuildTenantInfra gave
		// the first one, so its deleted-tenant reconcile still stops a retired
		// tenant's host before removing its directories.
		server.WireTenantRuntimeCloser(svcNode.Tenant, n.tenantHosts, n.raftBackend)
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
		n.closeDatabaseManager()
		return err
	}
	n.startLeaseExpiryStandalone(lr, svcNode.Lease)
	return nil
}

// startLeaseExpiryStandalone starts the lease-expiry sweeper of a standalone
// data plane (lifecycle.StartLocalLeaseExpiry): the node is the only replica,
// so it always sweeps. It runs until this data plane is torn down -- by an
// in-process encryption migration (teardownDataPlane), which builds a new
// engine and lessor -- or the node closes.
func (n *Node) startLeaseExpiryStandalone(lr *lease.Lessor, leaseSvc service.LeaseServicer) {
	n.stopLeaseExpiry = lifecycle.StartLocalLeaseExpiry(n.background(), lr, leaseSvc, n.logger)
}

// openStandaloneDatabaseManager opens the standalone node's database catalog
// under dataDir, reopening every database a previous run created, and records
// it on the node, which owns its lifetime. It is the standalone counterpart of
// the clustered boot's manager: the manager is handed to BuildServices and to
// the server, so a named database resolves through the server's registry, is
// confined to its owning tenant, and is refused while sealed. It used to be
// built inside BuildServices, which never reopened a previous run's databases
// and which the server never saw.
func (n *Node) openStandaloneDatabaseManager(dataDir string) (*database.Manager, error) {
	dbManager := database.NewManager(n.logger, dataDir)
	if err := dbManager.OpenAll(); err != nil {
		_ = dbManager.Close()
		return nil, &StorageCreateError{Backend: "database-manager", Err: err}
	}
	n.dbManager = dbManager
	return dbManager, nil
}

// closeDatabaseManager closes the node's database catalog, if open.
func (n *Node) closeDatabaseManager() {
	if n.dbManager != nil {
		_ = n.dbManager.Close()
		n.dbManager = nil
	}
}

// clusteredCreateFunc selects the Raft state-machine factory for a clustered
// node. The default (empty, "pebble", or "mvcc") is the durable MVCC-over-Pebble
// store wired with the supplied lessor / watch-hub / time-series / encryptor
// options — the only state machine that keeps revisions. Any other
// cfg.Raft.SMType (e.g. "memory", "concurrent", "fast", "disk") selects that
// factory from the statemachine registry: KV, batches, the guarded verbs,
// transactions (go-qrdb#136) and durable leases kept in lr -- the lessor the
// node's expiry sweeper watches -- without watch or time-series. A
// storage-generic one ("disk", "fast") composes the storage raft.sm_storage
// selects under raft.data_dir, exactly as the daemon does, carrying the
// node's at-rest encryptor: with the seal barrier enabled, pebble storage is
// opened on the barrier's encfs and file storage — which has no encrypting
// seam — is refused rather than left in plaintext. A non-pebble sm_storage for
// any other type is refused rather than ignored.
func clusteredCreateFunc(cfg *qrdbconfig.Config, pebbleOpts []store.Option, mvccOpts []store.MVCCOption, encryptor *atrest.Encryptor, lr *lease.Lessor) (sm.CreateFunc, error) {
	if usesClusteredMVCCStore(cfg) {
		if cfg.Raft.SMStorage != "" && cfg.Raft.SMStorage != "pebble" {
			return nil, &ConfigError{
				Field:   "raft.sm_storage",
				Message: "sm_storage " + cfg.Raft.SMStorage + " applies to the storage-generic state machines only; set raft.sm_type to disk or fast",
			}
		}
		return store.NewMVCCCreateFuncWith(pebbleOpts, mvccOpts...), nil
	}
	fn, err := statemachine.ResolveTypedWithStorage(cfg.Raft.SMType, cfg.Raft.SMBackend,
		statemachine.StorageConfig{Root: cfg.Raft.DataDir, Kind: cfg.Raft.SMStorage, Encryptor: encryptor},
		statemachine.WithKVLessor(lr))
	if err != nil {
		field := "raft.sm_type"
		var unencrypted *statemachine.SMStorageUnencryptedError
		if errors.As(err, &unencrypted) {
			field = "raft.sm_storage"
		}
		return nil, &ConfigError{Field: field, Message: err.Error()}
	}
	return fn, nil
}

// usesClusteredMVCCStore reports whether a clustered node runs the MVCC store
// rather than a registry state machine.
func usesClusteredMVCCStore(cfg *qrdbconfig.Config) bool {
	switch cfg.Raft.SMType {
	case "", "pebble", "mvcc":
		return true
	default:
		return false
	}
}

// clusteredProfile is a clustered node's effective capability profile: the
// raft composition's build-tag x config profile, narrowed to what the state
// machine clusteredCreateFunc selects actually serves, so the node reports its
// real sm_type rather than every feature the binary could serve on another.
func clusteredProfile(cfg *qrdbconfig.Config) (capabilities.Capabilities, error) {
	profileCfg := *cfg
	profileCfg.Mode = capabilities.ModeRaft
	profile := profileCfg.EffectiveCapabilities()
	if usesClusteredMVCCStore(cfg) {
		return store.MVCCDescriptor().Narrow(profile), nil
	}
	narrowed, err := statemachine.NarrowCapabilities(profile, cfg.Raft.SMType, cfg.Raft.SMBackend)
	if err != nil {
		return capabilities.Capabilities{}, &ConfigError{Field: "raft.sm_type", Message: err.Error()}
	}
	return narrowed, nil
}

// clusteredKeepsRevisionHistory reports whether the state machine
// clusteredCreateFunc selects for cfg keeps MVCC revision history: the
// default ("", "pebble", "mvcc") is the MVCC store; any other type is judged
// by its registered descriptor.
func clusteredKeepsRevisionHistory(cfg *qrdbconfig.Config) bool {
	switch cfg.Raft.SMType {
	case "", "pebble", "mvcc":
		return true
	default:
		return lifecycle.StateMachineKeepsRevisionHistory(cfg.Raft.SMType)
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
	ctx := n.background()
	n.bgWG.Add(1)
	go func() {
		defer n.bgWG.Done()
		lr.RunExpiry(ctx, lease.DefaultExpiryInterval,
			func() bool {
				leaderID, ok, err := host.GetLeaderID(shard.ShardKV)
				return err == nil && ok && leaderID == replicaID
			},
			leaseSvc.Revoke,
			n.logger,
		)
	}()
}

// startReclaimSweep starts the reclaim-job sweeper
// (lifecycle.Node.ReclaimSweeper, go-qrdb#547) for a clustered node,
// mirroring startLeaseExpiry's wiring immediately above -- and the daemon's
// equivalent in cmd/qrdbd -- so the two can never drift onto different
// composition paths.
//
// Unlike the lease sweeper, it runs on EVERY clustered node, not only the
// system shard's leader: that leadership decides ownership of shared-store
// jobs only, while a node-local store's jobs belong to the node that holds
// it and a dedicated Raft group's to that group's leader (see
// service.ReclaimStoreResolver.Owns). The server installs the resolver once
// it is built; until then a sweep resolves nothing. sweeper is always
// non-nil (see lifecycle.BuildServices); host nil is the only disabling
// condition, matching startLeaseExpiry.
func (n *Node) startReclaimSweep(host *quicraft.Host, sweeper *service.ReclaimSweeper, replicaID uint64) {
	if host == nil || sweeper == nil {
		return
	}
	ctx := n.background()
	n.bgWG.Add(1)
	go func() {
		defer n.bgWG.Done()
		sweeper.Run(ctx, service.DefaultReclaimInterval,
			func() bool {
				leaderID, ok, err := host.GetLeaderID(shard.ShardKV)
				return err == nil && ok && leaderID == replicaID
			},
			n.logger,
		)
	}()
}

// startReclaimSweepStandalone starts svcNode.ReclaimSweeper for a standalone
// (backend.LocalBackend, no Raft) node -- completeStandaloneBoot and
// NewStandaloneNode's seal-disabled path. There is no host to ask for
// leadership: a LocalBackend node is a single process with no peers, so it
// owns every shared-store job itself. sweeper is always non-nil (see
// lifecycle.BuildServices).
func (n *Node) startReclaimSweepStandalone(sweeper *service.ReclaimSweeper) {
	if sweeper == nil {
		return
	}
	go sweeper.Run(n.background(), service.DefaultReclaimInterval, nil, n.logger)
}

// completeClusteredBoot finishes wiring the clustered node after the barrier
// is unsealed: creates the quicraft host, calls BuildServices, and wires the
// server.
func (n *Node) completeClusteredBoot(ctx context.Context, pending *pendingClusteredBoot) (err error) {
	n.pendingClustered = nil
	// Snapshot the seal-only services node the constructor built (Barrier,
	// Ceremony, Credential, a seal-backed Tenant service -- see
	// lifecycle.BuildSealServicesOnly) before this attempt may overwrite it
	// with the full post-boot node. A failure restores this snapshot rather
	// than clearing svcNode outright, so the ceremony/credential API a retry
	// caller may still need stays reachable through Services() across a
	// failed attempt, exactly as it was before this attempt ran.
	preBootSvcNode := n.svcNode.Load()
	// A boot that fails -- its ctx cancelled while the system shard waits
	// for a quorum, say -- tears down what it built and leaves the node
	// SEALED and bootable. Closing the host has already wiped the system
	// barrier's key; sealing the barrier makes its state say so, and
	// restoring pending makes the next Unseal re-derive the key and run this
	// again, rather than find an "unsealed" barrier with no key and nothing
	// pending and report the node booted.
	//
	// On the tenant-isolation path, BuildTenantInfra unseals every
	// auto-unseal tenant barrier (via RestoreVolumeTenants) before this
	// function can fail, so sealing only the system barrier here would leave
	// those tenant DEKs live in memory while IsSealed() reports true. Seal
	// the whole node (service.SealWholeNode, as Close and closeSeal do), so
	// every database and tenant barrier is sealed alongside the system one.
	defer func() {
		if err != nil {
			if serr := service.SealWholeNode(n.barrierRegistry.Load(), n.barrier.Load(), n.wholeNodeDatabases()); serr != nil {
				n.logger.Error("node: failed to seal every barrier after a cancelled or failed clustered boot",
					slog.Any("error", serr))
			}
			n.pendingClustered = pending
			// Clear the components this attempt built so a retried Unseal's
			// accessors cannot hand out a host, feature gate, or data-plane
			// service (AuthFactorService, UserAuthFactorService,
			// ABACPolicyReloader, the scoped encryption migrator) built over
			// this failed attempt's now-stopped svcNode and host -- mirroring
			// what Close does for the same handles. svcNode itself is
			// restored to its pre-attempt (seal-only, or an earlier failed
			// attempt's) snapshot rather than the full node this attempt
			// stopped.
			n.host = nil
			n.featureGate = nil
			n.svcNode.Store(preBootSvcNode)
			n.retractDataPlaneServices()
			n.encMigrator.Store(nil)
			// Unbind the auth-chain vocabulary too: it was bound to the
			// AuthMiddleware wireClientAndServer built over this attempt's
			// svcNode (n.authMethods.Bind(authMW)), and a nil binding falls
			// back to the built-in tokens rather than consulting a
			// middleware whose RoleService/UserService are now stopped.
			n.authMethods.Bind(nil)
			// Cancel and clear the background context together with the
			// goroutines it drives, defensively: the wireClientAndServer
			// failure branch above already does this before svcNode.Stop
			// when THAT is the failing step, but every other fallible step
			// in this function runs before bgCtx is ever created, so this is
			// a no-op for them. Clearing bgCancel without also clearing
			// bgCtx would leave the next background() call reusing this
			// now-permanently-cancelled context -- background() only makes
			// a fresh one when bgCtx is nil -- so every goroutine a retried
			// boot starts on it would see it already Done and exit at once:
			// no lease reaping, no reclaim sweep, no reconcile.
			if n.bgCancel != nil {
				n.bgCancel()
				n.bgCancel = nil
			}
			n.bgWG.Wait()
			n.bgCtx = nil
			// The replicated volume-pool cursor and the cluster-wide slot
			// store were, if this attempt got that far, bound/attached over
			// the svcNode just stopped above; detach them so neither holds a
			// dangling reference to it. This is defensive hygiene, not a
			// fallback that serves a request: a failed boot leaves the node
			// in its pre-boot SEALED state, and the seal-only Services()
			// this defer restores is backed by the system barrier itself,
			// which fails every read and write while sealed (seal.ErrSealed)
			// -- CreateTenant's tombstone check is the first such read, so
			// provisioning a tenant through it fails closed before any pool
			// slot is picked or any tenant barrier is opened, local or not.
			if n.volumePool != nil && n.volumePool.ReplicatedState != nil {
				n.volumePool.ReplicatedState.Unbind()
			}
			if barrierRegistry := n.barrierRegistry.Load(); barrierRegistry != nil {
				barrierRegistry.DetachReplicatedSlotStore()
			}
		}
	}()

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
	createFn, err := clusteredCreateFunc(pending.cfg, pebbleOpts, mvccOpts, encryptor, lr)
	if err != nil {
		return err
	}

	var systemBarrier *seal.EpochBarrier
	if barrier != nil {
		systemBarrier = barrier.EpochBarrier()
	}
	host, walMux, err := buildClusteredHost(ctx, pending.cfg, pending.members, createFn, pending.raftMTLS, systemBarrier, n.logger)
	if err != nil {
		return &HostCreateError{Err: err}
	}
	n.host = host

	// Build the database manager. OpenAll re-scans the databases directory and
	// loads persisted meta.json records -- the same sequence the daemon
	// performs before BuildServices. Shard IDs are claimed from the replicated
	// store (lifecycle.BuildServices wires the claimer), not from this node. The manager is shared with
	// lifecycle.BuildServices (which skips creating its own when non-nil) and
	// with BuildTenantInfra (reattachRaftTenants walks manager.List()). The
	// node owns the manager's lifetime; lifecycle does NOT add a closer for an
	// externally-supplied manager.
	dbManager := database.NewManager(n.logger, pending.cfg.Raft.DataDir)
	if err := dbManager.OpenAll(); err != nil {
		_ = host.Close()
		_ = dbManager.Close()
		return &StorageCreateError{Backend: "database-manager", Err: err}
	}
	n.dbManager = dbManager

	gate, err := raftbackend.NewFeatureGate(host, pending.cfg.Raft.ReplicaID, shard.ShardKV)
	if err != nil {
		_ = host.Close()
		_ = dbManager.Close()
		n.dbManager = nil
		return &HostCreateError{Err: err}
	}
	n.featureGate = gate
	hostAdapter := raftbackend.New(host, raftbackend.WithFeatureGate(gate))

	// Per-instance effective capability profile: the raft composition,
	// narrowed to the state machine this node runs.
	profile, err := clusteredProfile(pending.cfg)
	if err != nil {
		_ = host.Close()
		_ = dbManager.Close()
		n.dbManager = nil
		return err
	}
	profile.WarnUnencryptedAtRest(n.logger)

	// Cluster sealing: the services seal through n.clusterSeal from the
	// start, node-locally until the monitor is set once the host, the
	// services and the tenant infrastructure exist.
	n.clusterSeal = clusterseal.NewDeferred()

	svcNode, err := n.buildServices(ctx, lifecycle.Infra{
		ClusterSealer:     n.clusterSeal,
		Host:              hostAdapter,
		NoRevisionHistory: !clusteredKeepsRevisionHistory(pending.cfg),
		WatchHub:          watchHub,
		DataDir:           pending.cfg.Raft.DataDir,
		ReplicaID:         pending.cfg.Raft.ReplicaID,
		Profile:           &profile,
		Logger:            n.logger,
		Encryptor:         encryptor,
		SealBarrier:       barrier,
		SealEnabled:       pending.cfg.Seal.Enabled,
		BarrierRegistry:   barrierRegistry,
		CredentialCoreSvc: n.credCoreSvc,
		// Key A: either an injected HSM/TPM/KMS provider, or in-memory
		// bootstrap material for the built-in cakey.SoftwareProvider to
		// adopt. See WithClusterCAKeyProvider / WithClusterCAMaterial.
		ClusterCAKeyProvider: n.clusterCAKeyProvider,
		ClusterCACertPEM:     n.clusterCACertPEM,
		ClusterCAKeyPEM:      n.clusterCAKeyPEM,
		// The CA mints its client certificates under the same trust domain
		// the mTLS authenticator pins, so the pin is exercised by the
		// certificates this node issues (go-qrdb#519).
		SPIFFETrustDomain: pending.cfg.Auth.SPIFFETrustDomain,
		// Under "required" the user store refuses to withdraw the bootstrap
		// admin's last live binding (go-qrdb#517).
		MTLSBindingPolicy: pending.cfg.Auth.MTLSBindingPolicy,
		// The node's own auth-chain vocabulary (see Node.authMethods).
		AuthMethodVocabulary: n.authMethods.IsAuthMethod,
		// Reuse the sealed-window ceremony service: holder registrations made
		// while sealed are cache-only and distributed shares live in its
		// in-memory enclaves, so a fresh instance would orphan both.
		CeremonyCoreSvc: n.sealedWindowCeremonyCore(),
		// DatabaseManager is shared so lifecycle wires its shard-ID claimer
		// into the one manager the node serves, and does not open a second
		// manager on the same data directory.
		DatabaseManager: dbManager,
		// Forward an injected (xkms) seal StrategyRegistry into the
		// TenantService (constructed inside BuildServices) so per-tenant
		// hardware/cloud seal strategies resolve in clustered mode too.
		StrategyRegistry:          server.ResolveCryptoOptions(pending.serverOpts...).StrategyRegistry,
		DefaultTenantEntitlements: n.defaultTenantEntitlements,
	}, pending.cfg, pending.serverOpts)
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

	// reconciler is started only once the cluster seal monitor is installed;
	// see below.
	var reconciler *server.TenantShardReconciler

	// Wire the IsolationRaft + IsolationVolume tenant infrastructure when the
	// caller opted in via WithTenantIsolation and the prerequisites are present.
	// The guard n.tenantHosts == nil prevents a double-build on the in-process
	// encryption-migration recycle (where the clustered boot does not rebuild;
	// only the standalone path rebuilds via completeStandaloneBoot). In the
	// clustered path there is no in-process system-migration recycle today, but
	// the guard matches the standalone pattern for consistency.
	if n.tenantIsolation && barrierRegistry != nil && n.volumePool != nil && svcNode.Tenant != nil && n.tenantHosts == nil {
		ti, tiErr := server.BuildTenantInfra(server.TenantInfraConfig{
			Ctx:        ctx,
			BarrierReg: barrierRegistry,
			TenantSvc:  svcNode.Tenant,
			// Resolved live for the same reason as the standalone path; see
			// completeStandaloneBoot (go-qrdb#383).
			TenantLookup: &nodeTenantResolver{node: n},
			VolumePool:   n.volumePool,
			Logger:       n.logger,
			// Host is the shared quicraft.Host that carries IsolationRaft shards.
			// Passing it here enables NewRaftBackendFactoryFromManager and
			// reattachRaftTenants so clustered embedded nodes gain full
			// IsolationRaft parity with the daemon. PlacementStore comes from
			// the just-built svcNode so shard placement is replicated into the
			// system KV. MultiplexLogDB is the host's WAL multiplex when it
			// has one (a cluster whose system barrier encrypts; see
			// buildClusteredHost), so dedicated-raft databases get their own
			// tenant-barrier WAL as on qrdbd; otherwise nil and they use the
			// shared system WAL.
			Host:           host,
			MultiplexLogDB: walMux,
			Manager:        dbManager,
			ReplicaID:      pending.cfg.Raft.ReplicaID,
			RaftAddr:       pending.cfg.Raft.RaftAddress,
			SystemTenant:   tenantid.System,
			PlacementStore: svcNode.PlacementStore,
			FeatureGate:    n.featureGate,
			ClusterSealer:  n.clusterSeal,
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
		reconciler = ti.Reconciler
		tenantInfraBuiltHook()
	}

	// Leader-side lease-expiry sweeper. See startLeaseExpiry: without it an
	// expired lease's attached keys are never reaped on the embedded path
	// (go-qrdb#174).
	//
	// Started HERE, after every other fallible step, rather than beside the
	// Lessor it sweeps. Placed earlier it sits above BuildTenantInfra, whose
	// error branch tears down svcNode, host and dbManager and returns WITHOUT
	// cancelling the background context -- so a failed boot would return an
	// error while the sweeper kept ticking against closed services. Adding a
	// cancel to each such branch would work and would leave places to forget;
	// one start point with one error path below it cannot be forgotten. The sweeper needs nothing that boot
	// produces after this line, and nothing is serving traffic yet.
	n.startLeaseExpiry(host, lr, svcNode.Lease, pending.cfg.Raft.ReplicaID)
	n.startReclaimSweep(host, svcNode.ReclaimSweeper, pending.cfg.Raft.ReplicaID)
	if err := n.startClusterSeal(ctx, host, svcNode, barrierRegistry, pending.cfg); err != nil {
		// Stop the sweepers and WAIT for them to exit before tearing down
		// what they access, as the wireClientAndServer branch below does.
		if n.bgCancel != nil {
			n.bgCancel()
			n.bgCancel = nil
		}
		n.bgWG.Wait()
		_ = svcNode.Stop(context.Background())
		n.svcNode.Store(nil)
		if n.raftBackend != nil {
			_ = n.raftBackend.CloseAll()
			n.raftBackend = nil
		}
		_ = host.Close()
		n.host = nil
		if n.tenantHosts != nil {
			_ = n.tenantHosts.CloseAll()
			n.tenantHosts = nil
		}
		_ = dbManager.Close()
		n.dbManager = nil
		return err
	}
	// The reconciler starts tenant shards, holding each whose record is
	// sealed. It asks through n.clusterSeal, which refuses nothing until the
	// monitor is Set, so it starts only now -- and the monitor refuses every
	// barrier until its first read. A node-lifetime context keeps it running
	// after the unseal/boot request ctx is cancelled; bgCancel in Node.Close()
	// (and in the wireClientAndServer error branch below) stops it, and bgWG
	// lets a failure path wait for it to actually exit before stopping what
	// it calls into.
	if reconciler != nil {
		reconcileCtx := n.background()
		n.bgWG.Add(1)
		go func() {
			defer n.bgWG.Done()
			reconciler.Run(reconcileCtx)
		}()
	}

	if err := n.wireClientAndServer(ctx, svcNode, pending.cfg, pending.serverOpts, false); err != nil {
		// Stop the lease-expiry sweeper, the reclaim sweeper and (if tenant
		// isolation wired one) the reconciler goroutine, and WAIT for them to
		// actually exit, before tearing down the resources they access:
		// cancelling bgCancel alone does not block until the goroutine's
		// current select wakes on ctx.Done, so a bare cancel races the
		// goroutine's next tick against svcNode.Stop below.
		if n.bgCancel != nil {
			n.bgCancel()
			n.bgCancel = nil
		}
		n.bgWG.Wait()
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
	n.startFeatureGate(host, svcNode.KV)
	return nil
}

// tenantInfraBuiltHook runs in completeClusteredBoot once BuildTenantInfra
// has restored -- and, for an auto-unseal tenant, unsealed -- every tenant
// barrier, before the rest of the boot. It is a no-op in production -- a
// direct assignment, not a conditional -- and exists so a test can cancel the
// boot exactly where tenant keys are live and the node is not yet serving.
var tenantInfraBuiltHook = func() {}

// wholeNodeDatabases is the database half of this node's whole-node seal
// (service.SealWholeNode): its database manager, or nil when none is open.
func (n *Node) wholeNodeDatabases() service.BarrierDatabases {
	if n.dbManager == nil {
		return nil
	}
	return n.dbManager
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
// guard and do not contend on the lifecycle gate). Clearing it during teardownDataPlane
// would turn a concurrent tenant-scope migration — which today proceeds
// independently of the system migration — into a typed "not yet fully booted"
// refusal. Close clears it directly instead, where there is no rebuild to come.
// The router's tenant leg is wired with a nodeTenantResolver (go-qrdb#222),
// which resolves n.svcNode.Load().Tenant on every call instead of capturing a
// value once, so it never hands a concurrent tenant-scope migration a handle
// onto the OLD, stopped tenant service across this window.
func (n *Node) retractDataPlaneServices() {
	n.authFactorSvc.Store(nil)
	n.loginFactorSvc.Store(nil)
	n.abacDecorator.Store(nil)
}

// Close tears down all node resources in LIFO order. Nil fields are skipped.
//
// Close blocks until an in-flight Unseal boot or system-scope EnableEncryption
// has finished, and until every in-flight tenant-scope migration has, then
// tears down whatever they built; see Node's doc comment. It is idempotent: a
// second Close returns nil.
//
// Why it must wait rather than race. A system migration a transport handler
// has already answered "migration_started" for (the accept-window pattern
// every handler uses) keeps running after the handler returns, through
// teardownDataPlane, re-encryption and Reopen — the same fields Close tears
// down (client, srv, services, barrier, sealStore). Unsynchronised, Close
// nilled n.sealStore under nodeMigrationProvider.Reopen's
// canonicalRootKeyStrategy read (go-qrdb#550). Close raises the lifecycle
// gate's closing signal first, so a boot or migration still WAITING is
// refused with ErrNodeClosed instead of queueing behind it, and one that
// arrives after Close has begun is refused the same way.
//
// All barriers (system and per-tenant) are sealed before their references
// are dropped so unsealed DEKs and root keys held in go-enclave enclaves are
// wiped deterministically rather than waiting on GC finalizers. Close does
// not sweep the process: an embedded composer that wants every remaining
// mlocked enclave swept at exit calls secure.WipeAll itself on shutdown, after
// Close, and one that wants its protected memory bounded installs a budget
// with secure.SetAllocator at startup, before anything allocates. Both belong
// to the binary that owns the process; cmd/qrdbd/start.go's runStart is the
// reference wiring.
func (n *Node) Close() error {
	first := n.lifeGate.beginClose()
	defer n.lifeGate.release()
	if !first {
		return nil
	}

	var firstErr error

	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Cancel the node-lifetime background context before stopping any service
	// so long-running goroutines (the lease-expiry sweeper, the reclaim
	// sweeper, and -- when tenant isolation wired one -- the
	// TenantShardReconciler) exit promptly rather than blocking on a
	// placement watch or poll timer during teardown. bgWG.Wait blocks until
	// they actually have: cancelling alone does not -- it only makes their
	// current select wake on ctx.Done -- so without the wait, svcNode.Stop
	// below could race one of them calling back into the very service graph
	// it is tearing down. Mirrors the wireClientAndServer failure branch in
	// completeClusteredBoot, which waits on the same bgWG before its own
	// svcNode.Stop for the identical reason.
	if n.bgCancel != nil {
		n.bgCancel()
		n.bgCancel = nil
	}
	n.bgWG.Wait()
	// Wait for the standalone lease-expiry sweeper to exit before anything it
	// revokes through is stopped: cancelling alone leaves a revoke in flight.
	if n.stopLeaseExpiry != nil {
		n.stopLeaseExpiry()
		n.stopLeaseExpiry = nil
	}
	record(n.stopFeatureGate())

	// Retract every handle this node published BEFORE stopping what backs
	// them. A caller that loads one after Close gets nil rather than a
	// live-looking handle onto a stopped service graph — the accessors promise
	// "a value or nil", and after Close the honest answer is nil for all of
	// them, not just the three with an obvious owner.
	if client := n.client.Swap(nil); client != nil {
		record(client.Close())
	}
	if client := n.featureClient.Swap(nil); client != nil {
		record(client.Close())
	}
	if srv := n.srv.Swap(nil); srv != nil {
		srv.Stop()
	}
	// The node owns the sockets its caller pre-bound across every server
	// lifetime (a migration rebuilds the server on them), so it — not a
	// server's Stop — closes them, once no server is left to use them.
	for _, sock := range n.preboundSockets {
		_ = sock.Close()
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
	// service.SealWholeNode seals every database barrier, every tenant
	// barrier and the system barrier, or the bare barrier without a registry.
	// Retract the seal handles BEFORE sealing, so an accessor racing Close
	// cannot hand out a barrier that is about to have its key material wiped.
	barrierRegistry := n.barrierRegistry.Swap(nil)
	barrier := n.barrier.Swap(nil)
	record(service.SealWholeNode(barrierRegistry, barrier, n.wholeNodeDatabases()))
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

// closePreboundOnError closes the sockets handed over through serverOpts
// (server.WithRESTListener and friends) when a constructor returns *errp
// non-nil.
func closePreboundOnError(errp *error, serverOpts []server.ServerOption) {
	if *errp == nil {
		return
	}
	for _, sock := range server.PreboundSockets(serverOpts...) {
		_ = sock.Close()
	}
}

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
func NewStandaloneNode(ctx context.Context, cfg *qrdbconfig.Config, serverOpts []server.ServerOption, nodeOpts ...NodeOption) (_ *Node, err error) {
	// A constructor that fails owns the sockets its caller handed over and
	// closes them: the caller gave them up, and no Node exists to close them.
	defer closePreboundOnError(&err, serverOpts)

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
		preboundSockets:           server.PreboundSockets(serverOpts...),
		sessionIssuer:             nOpts.sessionIssuer,
		loginFactorVerifiers:      nOpts.loginFactorVerifiers,
		tenantIsolation:           nOpts.tenantIsolation,
		defaultTenantEntitlements: nOpts.defaultTenantEntitlements,
		abacPolicies:              nOpts.abacPolicies,
		abacPolicyDir:             nOpts.abacPolicyDir,
		abacLiveReload:            nOpts.abacLiveReload,
		extraAuthenticators:       nOpts.extraAuthenticators,
		authorizerWrapper:         nOpts.authorizerWrapper,
		peerAttestation:           nOpts.peerAttestation,
		clusterCAKeyProvider:      nOpts.clusterCAKeyProvider,
		clusterCACertPEM:          nOpts.clusterCACertPEM,
		clusterCAKeyPEM:           nOpts.clusterCAKeyPEM,
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
		sub, err := buildNodeSealSubsystem(ctx, cfg, dataDir, log, nOpts, false, server.ResolveCryptoOptions(serverOpts...).AuditLogger)
		if err != nil {
			return nil, err
		}
		n.adoptSealSubsystem(sub)

		// Build the sealed-window service graph (barrier + ceremony +
		// credential only) so they are accessible even before full boot.
		sealNode, sealNodeErr := n.buildSealServices(ctx, lifecycle.Infra{
			DataDir:           dataDir,
			SealBarrier:       sub.barrier,
			BarrierRegistry:   sub.barrierRegistry,
			CredentialCoreSvc: sub.credCoreSvc,
			CeremonyDataDir:   sub.sealDataDir,
			Logger:            log,
		}, cfg, serverOpts)
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
	lr := lease.NewLessor()
	smAdapter, err := createNodeEngine(backendType, stg, pdb, watchHub, lr)
	if err != nil {
		_ = n.stg.Close()
		return nil, &EngineCreateError{Engine: "mvcc", Err: err}
	}

	localHost := backend.NewLocalBackend(smAdapter)

	dbManager, err := n.openStandaloneDatabaseManager(dataDir)
	if err != nil {
		_ = localHost.Close()
		_ = n.stg.Close()
		return nil, err
	}

	// Per-instance effective capability profile: standalone semantics by
	// construction (see completeStandaloneBoot for the rationale).
	profileCfg := *cfg
	profileCfg.Mode = capabilities.ModeStandalone
	profile := profileCfg.EffectiveCapabilities()
	profile.WarnUnencryptedAtRest(n.logger)

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:            localHost,
		WatchHub:        watchHub,
		DataDir:         dataDir,
		DatabaseManager: dbManager,
		ReplicaID:       cfg.Raft.ReplicaID,
		Profile:         &profile,
		Logger:          log,
		SealEnabled:     cfg.Seal.Enabled,
		// Under "required" the user store refuses to withdraw the bootstrap
		// admin's last live binding (go-qrdb#517).
		MTLSBindingPolicy: cfg.Auth.MTLSBindingPolicy,
		// The node's own auth-chain vocabulary (see Node.authMethods).
		AuthMethodVocabulary: n.authMethods.IsAuthMethod,
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
		n.closeDatabaseManager()
		return nil, &ServicesCreateError{Err: err}
	}
	n.svcNode.Store(svcNode)
	n.startReclaimSweepStandalone(svcNode.ReclaimSweeper)

	if err := n.wireClientAndServer(ctx, svcNode, cfg, serverOpts, true); err != nil {
		_ = svcNode.Stop(context.Background())
		_ = localHost.Close()
		_ = n.stg.Close()
		n.closeDatabaseManager()
		return nil, err
	}
	n.startLeaseExpiryStandalone(lr, svcNode.Lease)

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
func NewClusteredNode(ctx context.Context, cfg *qrdbconfig.Config, members map[uint64]string, serverOpts []server.ServerOption, nodeOpts ...NodeOption) (_ *Node, err error) {
	// A constructor that fails owns the sockets its caller handed over and
	// closes them: the caller gave them up, and no Node exists to close them.
	defer closePreboundOnError(&err, serverOpts)

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
		preboundSockets:           server.PreboundSockets(serverOpts...),
		sessionIssuer:             nOpts.sessionIssuer,
		loginFactorVerifiers:      nOpts.loginFactorVerifiers,
		tenantIsolation:           nOpts.tenantIsolation,
		defaultTenantEntitlements: nOpts.defaultTenantEntitlements,
		abacPolicies:              nOpts.abacPolicies,
		abacPolicyDir:             nOpts.abacPolicyDir,
		abacLiveReload:            nOpts.abacLiveReload,
		extraAuthenticators:       nOpts.extraAuthenticators,
		authorizerWrapper:         nOpts.authorizerWrapper,
		peerAttestation:           nOpts.peerAttestation,
		clusterCAKeyProvider:      nOpts.clusterCAKeyProvider,
		clusterCACertPEM:          nOpts.clusterCACertPEM,
		clusterCAKeyPEM:           nOpts.clusterCAKeyPEM,
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
		sub, err := buildNodeSealSubsystem(ctx, cfg, dataDir, log, nOpts, true, server.ResolveCryptoOptions(serverOpts...).AuditLogger)
		if err != nil {
			return nil, err
		}
		n.adoptSealSubsystem(sub)

		sealNode, sealNodeErr := n.buildSealServices(ctx, lifecycle.Infra{
			DataDir:           dataDir,
			SealBarrier:       sub.barrier,
			BarrierRegistry:   sub.barrierRegistry,
			CredentialCoreSvc: sub.credCoreSvc,
			CeremonyDataDir:   sub.sealDataDir,
			Logger:            log,
		}, cfg, serverOpts)
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
	createFn, err := clusteredCreateFunc(cfg, pebbleOpts, mvccOptsNoSeal, nil, lr)
	if err != nil {
		return nil, err
	}

	// Seal disabled: no system barrier, so the WAL is written
	// WithoutEncryption and there is no multiplex.
	host, _, err := buildClusteredHost(ctx, cfg, members, createFn, nOpts.raftMTLS, nil, n.logger)
	if err != nil {
		return nil, &HostCreateError{Err: err}
	}
	n.host = host

	gate, err := raftbackend.NewFeatureGate(host, cfg.Raft.ReplicaID, shard.ShardKV)
	if err != nil {
		_ = host.Close()
		return nil, &HostCreateError{Err: err}
	}
	n.featureGate = gate
	hostAdapter := raftbackend.New(host, raftbackend.WithFeatureGate(gate))

	// Pinned per instance, as the sealed path pins it: without it the node
	// would report the process-global profile, not its own state machine's.
	profile, err := clusteredProfile(cfg)
	if err != nil {
		_ = host.Close()
		return nil, err
	}

	svcNode, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              hostAdapter,
		NoRevisionHistory: !clusteredKeepsRevisionHistory(cfg),
		WatchHub:          watchHub,
		DataDir:           dataDir,
		ReplicaID:         cfg.Raft.ReplicaID,
		Profile:           &profile,
		Logger:            log,
		SealEnabled:       cfg.Seal.Enabled,
		// Under "required" the user store refuses to withdraw the bootstrap
		// admin's last live binding (go-qrdb#517).
		MTLSBindingPolicy: cfg.Auth.MTLSBindingPolicy,
		// The node's own auth-chain vocabulary (see Node.authMethods).
		AuthMethodVocabulary: n.authMethods.IsAuthMethod,
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
	n.startReclaimSweep(host, svcNode.ReclaimSweeper, cfg.Raft.ReplicaID)

	if err := n.wireClientAndServer(ctx, svcNode, cfg, serverOpts, false); err != nil {
		if n.bgCancel != nil {
			n.bgCancel()
			n.bgCancel = nil
		}
		_ = svcNode.Stop(context.Background())
		_ = host.Close()
		return nil, err
	}
	n.startFeatureGate(host, svcNode.KV)

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
func buildNodeSealSubsystem(ctx context.Context, cfg *qrdbconfig.Config, dataDir string, log *slog.Logger, nOpts *nodeOptions, clustered bool, trail audit.AuditLogger) (*nodeSealSubsystem, error) {
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
	// The system barrier, and every tenant barrier the registry builds,
	// record their events on the caller's compliance trail
	// (server.WithAuditLogger), which exists before the node does.
	barrierCfg := seal.BarrierConfig{
		RootKeyPath: cfg.Seal.RootKeyPath,
		AuditLogger: seal.NewComplianceAuditLogger(trail, tenantid.System, seal.SystemBarrierAuditName),
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

	barrierRegistry, regErr := seal.NewBarrierRegistry(log, barrier, seal.WithAuditTrail(trail))
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

	pinBackend, pinErr := lifecycle.NewSealStorePINBackend(sealBackend)
	if pinErr != nil {
		_ = sealStore.Close()
		return nil, &StorageCreateError{Backend: "seal-pin", Err: pinErr}
	}

	return &nodeSealSubsystem{
		barrier:         barrier,
		credCoreSvc:     credCoreSvc,
		barrierRegistry: barrierRegistry,
		sealStore:       sealStore,
		sealDataDir:     sealDataDir,
		pinBackend:      pinBackend,
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
		// The option's slice stays the embedder's (WithSealPassphrase says
		// the caller wipes it): CredentialsFor copies it into protected
		// memory, and the credential is destroyed once this attempt has
		// used it.
		var err error
		if creds, err = seal.CredentialsFor(ctx, passphrase); err != nil {
			log.Warn("node: barrier credentials failed", "error", err)
			return false
		}
		defer creds.Secret.Destroy()

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
	//
	// The service built here is the TENANT-ROUTED one
	// (service.NewRoutingAuthFactorService), over svcNode.StoreResolver — the
	// same resolver buildLoginOpts requires for the user-side service, and
	// for the same reason (#464, mirroring #393): building it over the bare
	// svcNode.KV resolves every tenant's custodian enrollments out of one
	// keyspace. There is no unrouted fallback; see ErrNoAuthFactorTenantRouting.
	if crypto.AuthFactorService != nil {
		authFactorSvc := crypto.AuthFactorService
		n.authFactorSvc.Store(&authFactorSvc)
	} else if crypto.FactorRegistry != nil {
		if svcNode.StoreResolver == nil {
			return &AuthFactorWiringError{Err: ErrNoAuthFactorTenantRouting}
		}
		svc, err := service.NewRoutingAuthFactorService(service.RoutingAuthFactorServiceConfig{
			Factors:  crypto.FactorRegistry.All(),
			Resolver: svcNode.StoreResolver,
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
	// It is built BEFORE the embedded transport, and the same instance is
	// handed to it (embedded.WithAuthMiddleware), so a gated write on the
	// in-process transport -- a role assignment -- is authorized by exactly
	// the middleware the network transports use, and by nothing when auth
	// is disabled, as they are.
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
		return &AuthMiddlewareWiringError{Err: err}
	}
	// The audit failure posture: fail-closed unless the embedder's config says
	// otherwise, and an unrecognised value fails the node's start rather than
	// selecting a posture nobody asked for. The fail-open warning is emitted
	// by the middleware option, so an embedded node warns identically to the
	// daemon.
	auditFailure, err := audit.ParseFailurePolicy(cfg.Auth.AuditFailurePolicy)
	if err != nil {
		return &AuthMiddlewareWiringError{Err: err}
	}
	// The mTLS enrolment posture: "enrolled" unless the embedder's config says
	// otherwise, and an unrecognised value fails the node's start. Under
	// "required" the node refuses to start when an EXISTING bootstrap admin
	// could not log in by certificate, and Node.FirstBoot holds first-boot to
	// the same policy -- so an embedder cannot boot with the admin locked out
	// (the owner's refuse-to-boot ruling, go-qrdb#517).
	bindingPolicy, err := auth.ParseMTLSBindingPolicy(cfg.Auth.MTLSBindingPolicy)
	if err != nil {
		return &AuthMiddlewareWiringError{Err: err}
	}
	n.bindingPolicy.Store(int32(bindingPolicy))
	if bindingPolicy == auth.MTLSBindingRequired && cfg.Auth.IsEnabled() {
		if verr := verifyExistingAdminBinding(ctx, svcNode.User, firstboot.AdminBindingLookupTimeout); verr != nil {
			return &AuthMiddlewareWiringError{Err: verr}
		}
	}
	authMW, err := wiring.BuildAuthMiddleware(wiring.AuthMiddlewareConfig{
		Enabled:             cfg.Auth.IsEnabled(),
		Authorizer:          n.authorizer,
		RoleService:         svcNode.Role,
		UserService:         svcNode.User,
		StrictSeparation:    strictSoD,
		SPIFFETrustDomain:   cfg.Auth.SPIFFETrustDomain,
		AuditLogger:         decisionAudit,
		AuditFailurePolicy:  auditFailure,
		ExtraAuthenticators: n.extraAuthenticators,
		// Certificate revocation is enforced on the authentication path, so
		// the certificate authority reaches the middleware and not only the
		// cert API handlers. An embedded node without this enforces nothing:
		// it records revocations, lists them, and lets the revoked
		// certificate keep authenticating until it expires.
		CertProvider:       svcNode.CertProv,
		RevocationCacheTTL: revocationTTL,
		MTLSBindingPolicy:  bindingPolicy,
		PeerAttestation:    n.peerAttestation,
	})
	if err != nil {
		return &AuthMiddlewareWiringError{Err: err}
	}
	// This node's user stores now validate policies against THIS chain's
	// methods (built-ins plus WithAuthenticators' names). A nil middleware
	// (auth disabled) leaves the built-in tokens.
	n.authMethods.Bind(authMW)

	if authMW != nil {
		embeddedOpts = append(embeddedOpts, embedded.WithAuthMiddleware(authMW))
	}

	// The embedded transport creates through the same provisioning check the
	// server composes over its database service: databases:create_raft and
	// databases:create_volume are checked against the identity the call
	// carries, so "every create path" includes the in-process one. Without
	// request authentication there is no identity to check, exactly as on
	// the network transports.
	if authMW != nil {
		embeddedOpts = append(embeddedOpts, embedded.WithDatabaseService(
			service.NewProvisioningAuthorizedDatabaseService(svcNode.Database, authMW, svcNode.Tenant)))
	}
	if pinBackend := n.pinBackend(callerOpts); pinBackend != nil {
		embeddedOpts = append(embeddedOpts, embedded.WithPINBackend(pinBackend))
	}

	featureClient, err := newConnectedEmbeddedClient(ctx, svcNode, embeddedOpts)
	if err != nil {
		return err
	}
	n.featureClient.Store(featureClient)

	// The caller's client refuses what the node's network listeners refuse:
	// every request outside the sealed-window allowlist while the node
	// barrier is sealed, or a cluster seal holds the node, cuts it off, or
	// is not yet confirmed.
	if barrier != nil {
		var cs server.ClusterSeal
		if n.clusterSeal != nil {
			cs = n.clusterSeal
		}
		embeddedOpts = append(embeddedOpts, embedded.WithSealProbe(server.NodeSealProbe(barrier, cs)))
	}
	// The gate refuses a watch as it opens; a watch opened before a seal is
	// a stream the seal must end, as it ends a network listener's.
	if n.clusterSeal != nil {
		embeddedOpts = append(embeddedOpts, embedded.WithSealStreams(n.clusterSeal.Streams()))
	}

	tp, err := embedded.New(svcNode.KV, svcNode.Admin, embeddedOpts...)
	if err != nil {
		n.closeFeatureClient()
		return &ClientCreateError{Err: err}
	}

	// Create SDK client.
	client, err := sdk.NewClient(tp)
	if err != nil {
		n.closeFeatureClient()
		return &ClientCreateError{Err: err}
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		n.closeFeatureClient()
		return &ClientCreateError{Err: err}
	}
	n.client.Store(client)

	// Build multi-protocol server.
	serverCfg := buildNodeServerConfig(cfg)
	allOpts := buildServerOpts(svcNode, n.dbManager, callerOpts, standalone)
	if crypto.PINBackend == nil && n.sealPINBackend != nil {
		allOpts = append(allOpts, server.WithPINBackend(n.sealPINBackend))
	}
	if authMW != nil {
		allOpts = append(allOpts, server.WithAuthMiddleware(authMW))
	}
	if barrier != nil {
		allOpts = append(allOpts, server.WithSealedWindowProbe(barrier))
	}
	if n.clusterSeal != nil {
		allOpts = append(allOpts, server.WithClusterSeal(n.clusterSeal))
	}
	if p := n.authFactorSvc.Load(); p != nil {
		allOpts = append(allOpts, server.WithAuthFactorService(*p))
	}

	// Build the system-scope migrator adapter that holds the lifecycle gate for the
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
			// Tenant-scope migrations through every transport are admitted
			// by the node's lifecycle gate, like the direct call below.
			server.WithTenantMigrationAdmission(n.admitTenantMigration),
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
	// The seam that routes a volume-isolated tenant's DROP DATABASE
	// reclamation to its dedicated per-tenant Host rather than the system
	// store (go-qrdb#412). The reclaim service is already wired into the
	// database manager by lifecycle.BuildServices; only the tenant routing is
	// the server's to supply, and it is a no-op without a host registry.
	if svcNode.KeyspaceReclaim != nil {
		allOpts = append(allOpts, server.WithKeyspaceReclaimService(svcNode.KeyspaceReclaim))
	}
	// The reclaim sweeper (go-qrdb#547), already constructed by
	// lifecycle.BuildServices and started by startReclaimSweep: the server
	// installs the resolver that routes each pending job to its owner and
	// store, through the same registry its transports resolve requests
	// with. A server built without a DatabaseServiceRegistry serves no
	// relational DDL, publishes no jobs, and installs none.
	if svcNode.ReclaimSweeper != nil {
		allOpts = append(allOpts, server.WithReclaimSweeper(svcNode.ReclaimSweeper))
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
	// routing semantics (system → sysMig → lifecycle gate; tenant → tenantHosts).
	//
	// The tenant leg is wired with &nodeTenantResolver{node: n}, NOT svcNode.Tenant
	// directly: this router is kept alive across a system-scope migration's
	// teardownDataPlane→rebuild recycle (see retractDataPlaneServices), during
	// which svcNode — and the *tenant.Service captured off it — is swapped out
	// from under any handle taken here. nodeTenantResolver resolves
	// n.svcNode.Load().Tenant on every call instead, so a concurrent tenant-scope
	// migration during that window always reaches the currently-live tenant
	// service rather than the one that existed when this line ran (go-qrdb#222).
	var encMigrator service.EncryptionMigrator = server.NewScopedEncryptionMigrator(sysMig, n.tenantHosts, n.raftBackend, barrierRegistry, &nodeTenantResolver{node: n},
		server.WithTenantAdmission(n.admitTenantMigration))
	n.encMigrator.Store(&encMigrator)

	srv := server.New(serverCfg, svcNode.KV, svcNode.Admin, allOpts...)
	if err := srv.Start(); err != nil {
		_ = client.Close()
		n.client.Store(nil)
		n.closeFeatureClient()
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
		// Wired unconditionally, matching the network transports
		// (server.WithEncryptionMigrator is set whenever the node's own
		// scoped router exists, independent of barrier/seal state): the
		// adapter resolves n.encMigrator live on every call and answers
		// with the shared service.ErrMigrationProviderUnavailable when it
		// is not yet set, so BarrierEnableEncryption through this transport
		// behaves the same as through REST/gRPC/Unix/QUIC/MCP instead of
		// the transport-specific stub it used to be (go-qrdb#558).
		embedded.WithEncryptionMigrator(&nodeEmbeddedMigrator{node: n}),
		// Database scoping (Client.DatabaseScoped) resolves through the
		// running server's per-(tenant, database) registry, the same one the
		// network transports use; see nodeServiceRegistry.
		embedded.WithServiceRegistry(&nodeServiceRegistry{node: n}),
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
	// MVCC, lease, watch and lock are served in both modes too
	// (go-qrdb#582): a standalone node composes them over the same engine,
	// lessor and watch hub as raft mode's MVCC state machine.
	opts = append(opts,
		embedded.WithMVCCService(svcNode.MVCC),
		embedded.WithLeaseService(svcNode.Lease),
		embedded.WithWatchService(svcNode.Watch),
		embedded.WithLockService(svcNode.Lock),
	)
	if svcNode.DEK != nil {
		opts = append(opts, embedded.WithDEKService(svcNode.DEK))
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
	// The tenant surface was never wired here, so every TenantClient verb on
	// node.Client() answered "TenantServicer must not be nil" while the same
	// verbs worked on every network transport of the same node.
	if svcNode.Tenant != nil {
		opts = append(opts, embedded.WithTenantService(svcNode.Tenant))
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
//
// The factor service is the TENANT-ROUTED one the node lifecycle built
// alongside the routed user and role services, taken rather than rebuilt: one
// construction site means one resolver, one per-tenant service cache, and one
// process-local mutex serialising a tenant's factor mutations. Rebuilding it
// here over svcNode.KV is what go-qrdb#393 measured — a single flat keyspace
// in which a credential enrolled under one tenant resolved a login begun under
// another — so its absence is a wiring refusal, not a reason to fall back.
func (n *Node) buildLoginOpts(svcNode *lifecycle.Node) ([]embedded.Option, error) {
	factorSvc := svcNode.UserAuthFactor
	if factorSvc == nil {
		return nil, &LoginWiringError{Err: ErrNoTenantRouting}
	}
	loginFactorSvc := factorSvc
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
			Active:   u.CanAuthenticate(),
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
//
// The database catalog and a database service registry are wired as the
// daemon wires them: the registry's default entry is the node's own service
// graph (lifecycle.SystemDatabase), and every other database resolves from
// the catalog as a namespace over it.
// Without them no named database resolved on the node's server, so a
// database-scoped client was refused outright and RAG, which is bound to the
// registry, refused every call.
func buildServerOpts(svcNode *lifecycle.Node, dbManager *database.Manager, callerOpts []server.ServerOption, standalone bool) []server.ServerOption {
	var shardID uint64
	if !standalone {
		shardID = shard.ShardKV
	}
	dbRegistry := server.NewDatabaseServiceRegistry(lifecycle.SystemDatabase)
	dbRegistry.Register(lifecycle.SystemDatabase, svcNode.DatabaseServices(lifecycle.SystemDatabase, shardID))
	opts := []server.ServerOption{
		server.WithDatabaseServiceRegistry(dbRegistry),
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

	// MVCC, lease, watch and lock, likewise in both modes (go-qrdb#582):
	// every transport serves them on a standalone node exactly as the
	// in-process transport does. server.WithStandaloneMode now refuses only
	// the etcd compatibility listener, which stays raft-only.
	opts = append(opts,
		server.WithMVCC(svcNode.MVCC),
		server.WithLease(svcNode.Lease),
		server.WithWatch(svcNode.Watch),
		server.WithLockService(svcNode.Lock),
	)
	if standalone {
		opts = append(opts, server.WithStandaloneMode())
	}
	if svcNode.User != nil {
		opts = append(opts, server.WithUserService(svcNode.User))
	}
	// StoreResolver and UserAuthFactor travel together (lifecycle.Node's own
	// invariant: UserAuthFactor is non-nil exactly when StoreResolver is). The
	// resolver lets Server.ensureUserAuthFactorService build the tenant-routed
	// service itself, but WithUserAuthFactorService below wires the SAME
	// instance this node already built, so every SDK-managed transport (REST,
	// gRPC, QUIC, Unix, MCP) computes Factors metadata and MFA state from the
	// identical enrollment records the embedded transport and this node's own
	// login ceremony already read from — one source of truth, not a second
	// one constructed lazily per server.
	if svcNode.StoreResolver != nil {
		opts = append(opts, server.WithTenantStoreResolver(svcNode.StoreResolver))
	}
	if svcNode.UserAuthFactor != nil {
		opts = append(opts, server.WithUserAuthFactorService(svcNode.UserAuthFactor))
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
	if svcNode.DEK != nil {
		opts = append(opts, server.WithDEKService(svcNode.DEK))
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
	if dbManager != nil {
		opts = append(opts, server.WithDatabaseManager(dbManager))
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
// lifecycle.NewLocalEngine for the engine-boundary, watch-hub and lessor
// contracts).
func createNodeEngine(backendType database.BackendType, stg storage.Storage, pdb *pebble.DB, hub *watch.Hub, lr *lease.Lessor) (backend.SMAdapter, error) {
	return lifecycle.NewLocalEngine(backendType, stg, pdb, hub, lr)
}

// buildClusteredHost creates the quicraft.Host, starts the KV shard and waits
// until that shard can serve a linearizable read.
// A single-entry members map uses WithoutTransport (no network required).
// members is passed directly to host.StartShard as the initial member set.
// mtls provides the PEM-encoded mTLS certificate material for multi-node
// clusters; it is ignored for single-node (WithoutTransport) clusters.
//
// systemBarrier is the unsealed system barrier, or nil when seal is disabled.
// The Raft WAL is configured from it by database.NewRaftWAL, exactly as qrdbd
// configures its own: encrypted under the barrier, the multiplex on a cluster
// whose barrier encrypts, and WithoutEncryption only with seal disabled. The
// returned multiplex is nil unless the host uses it. Before this node shared
// that path it always wrote its WAL WithoutEncryption, so an unmarked WAL is
// judged plaintext, and one found under a barrier is refused with
// *database.RaftWALFormatError rather than replayed as ciphertext.
func buildClusteredHost(
	ctx context.Context,
	cfg *qrdbconfig.Config,
	members map[uint64]string,
	createFn sm.CreateFunc,
	mtls *quicraftconfig.MTLSConfig,
	systemBarrier *seal.EpochBarrier,
	log *slog.Logger,
) (*quicraft.Host, *database.MultiplexLogDB, error) {
	hostCfg, shardCfg, hostOpts, err := buildClusteredRaftConfigs(cfg, members, mtls)
	if err != nil {
		return nil, nil, err
	}

	wal, err := database.NewRaftWAL(database.RaftWALConfig{
		Dir:            hostCfg.WALDir,
		SystemBarrier:  systemBarrier,
		SingleNode:     len(members) == 1,
		UnmarkedFormat: database.WALFormatPlaintext,
	})
	if err != nil {
		return nil, nil, err
	}
	hostOpts = append(hostOpts, wal.Options...)

	host, err := quicraft.NewHost(hostCfg, hostOpts...)
	if err != nil {
		_ = wal.Close()
		return nil, nil, err
	}
	if identity := host.NodeIdentity(); identity != "" {
		// Safe to log: a hash of a public key. It is the value every other
		// replica needs in its raft.member_identities for this one.
		log.Info("raft member identity",
			slog.Uint64("replica_id", shardCfg.ReplicaID),
			slog.String("identity", identity))
	}

	if err := host.StartShard(members, cfg.Raft.Join, createFn, shardCfg); err != nil {
		_ = host.Close()
		return nil, nil, err
	}

	// The host is handed out only once the system shard can serve a
	// linearizable read: a leader is known AND has committed an entry in its
	// term. Until then every ReadIndex is refused -- "leader unknown", then
	// quicraft.ErrReadIndexNotReady -- and the first read is the boot's own
	// (BuildTenantInfra lists the replicated tenant store) or the caller's (a
	// replica add reads the committed membership). Both clustered branches,
	// sealed and seal-disabled, build their host here, so neither can return a
	// node that cannot serve. The wait and its budget are
	// server.WaitForShardServing, the one every host-open path shares. It
	// honours ctx: a caller that gives up is not held for the budget.
	if err := server.WaitForShardServing(ctx, host, shard.ShardKV); err != nil {
		_ = host.Close()
		return nil, nil, err
	}

	return host, wal.Multiplex, nil
}

// buildClusteredRaftConfigs derives the quicraft host configuration, KV shard
// configuration and host options from the qrdb config and the initial member
// set. It performs no I/O.
//
// A single-entry members map means the host is built WithoutTransport and has
// no network at all, which is the precondition for both transport-less Raft
// tunings — see cluster.TransportlessRTTMillisecond and
// cluster.SingleNodeElectionRTT (go-qrdb#250).
//
// A multi-entry members map means the host has a Raft transport, and
// go-quicraft then admits a member by its node identity and by nothing else
// (go-qrdb#556). The shard configuration's MemberIdentities is resolved from
// cfg.Raft.MemberIdentities plus this replica's own identity, derived from the
// certificate in mtls; see cluster.ResolveMemberIdentities.
func buildClusteredRaftConfigs(
	cfg *qrdbconfig.Config,
	members map[uint64]string,
	mtls *quicraftconfig.MTLSConfig,
) (quicraftconfig.HostConfig, quicraftconfig.Config, []quicraft.HostOption, error) {
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

	// The Raft log's at-rest options are not decided here: buildClusteredHost
	// adds them from database.NewRaftWAL, the path qrdbd shares, because they
	// depend on the system barrier.
	var hostOpts []quicraft.HostOption
	transportless := len(members) == 1
	if transportless {
		hostOpts = append(hostOpts, quicraft.WithoutTransport())

		// A host with no network inherits go-quicraft's WAN RTT estimate
		// (200 ms), sizing an election budget for a link it does not have.
		// See cluster.TransportlessRTTMillisecond (go-qrdb#250).
		cluster.ApplyTransportlessHostTuning(&hostCfg)
	} else if mtls != nil {
		// Multi-node: inject mTLS material into the transport config so all
		// inter-node QUIC connections are mutually authenticated.
		hostCfg.TransportConfig.MTLSConfig = mtls
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
	if transportless && !cfg.Raft.Join {
		// No peer can reach a transport-less host, so this shard's voting
		// membership can never grow and its quorum is permanently itself.
		// See cluster.SingleNodeElectionRTT. Join is excluded even though it
		// cannot succeed without a transport: the shorter election timeout is
		// sound only for a shard that is single-node by intent, and a
		// misconfiguration must not be the thing that grants it.
		cluster.ApplyTransportlessShardTuning(&shardCfg)
	}

	if !transportless {
		// The Raft transport is the one socket an embedder cannot pre-bind:
		// go-quicraft binds it itself, and every peer dials the advertised
		// address, so a kernel-assigned port would be advertised as ":0".
		// Refuse it rather than start a replica no peer can reach
		// (go-qrdb#374).
		for _, a := range [...]struct{ field, addr string }{
			{"raft.raft_address", raftAddress},
			{"raft.listen_address", listenAddress},
		} {
			if err := cluster.CheckRaftTransportPort(a.field, a.addr); err != nil {
				return quicraftconfig.HostConfig{}, quicraftconfig.Config{}, nil,
					&ConfigError{Field: a.field, Message: err.Error(), Err: err}
			}
		}

		var selfCert []byte
		if mtls != nil {
			selfCert = mtls.Cert
		}
		identities, err := cluster.ResolveMemberIdentities(
			splitMemberIdentities(cfg.Raft.MemberIdentities), shardCfg.ReplicaID, selfCert)
		if err != nil {
			return quicraftconfig.HostConfig{}, quicraftconfig.Config{}, nil,
				&ConfigError{Field: "raft.member_identities", Message: err.Error(), Err: err}
		}
		shardCfg.MemberIdentities = identities
	}

	return hostCfg, shardCfg, hostOpts, nil
}

// splitMemberIdentities splits the comma-separated raft.member_identities
// value into the specifications cluster.ParseMemberIdentities takes. An empty
// value yields no specifications.
func splitMemberIdentities(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
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
// go-enclave locked-memory enclaves, and Seal wipes it deterministically
// (idempotent when the barrier never unsealed).
func (n *Node) closeSeal() {
	barrierRegistry := n.barrierRegistry.Swap(nil)
	barrier := n.barrier.Swap(nil)
	_ = service.SealWholeNode(barrierRegistry, barrier, n.wholeNodeDatabases())
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
