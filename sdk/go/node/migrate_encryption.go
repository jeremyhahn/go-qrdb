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

package node

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// stagingRootKeySuffix is appended to the canonical root-key path to hold the
// keyed target barrier's sealed root key during an in-process migration. It is
// removed once the migration commits (or rolls back).
const stagingRootKeySuffix = ".migrating"

// MigrationNotSupportedError is returned when this node cannot perform an
// encryption migration at all: seal is not enabled, the node has not finished
// booting, it has no seal store to persist the migrated root key in, or its
// data-plane boot params were not retained (clustered nodes use the cluster
// coordinator, not this path). Reason says which.
//
// It is the node-level form of service.ErrMigrationProviderUnavailable, and
// errors.Is reports it as that sentinel, so every transport answers it the way
// it answers the sentinel -- 501 on REST, Unimplemented on gRPC, unix, QUIC
// and MCP -- while a direct caller of Node.EnableEncryption can still
// errors.As it for the reason. A failure partway through a migration the node
// did accept is a *MigrationReopenError, not this.
type MigrationNotSupportedError struct {
	Reason string
}

func (e *MigrationNotSupportedError) Error() string {
	return "node: encryption migration not supported: " + e.Reason
}

// Is reports whether target is service.ErrMigrationProviderUnavailable.
func (e *MigrationNotSupportedError) Is(target error) bool {
	return target == service.ErrMigrationProviderUnavailable
}

// MigrationReopenError is returned when a system-scope migration the node
// accepted fails while bringing its data plane back up on the migrated
// barrier. Unlike *MigrationNotSupportedError it is not a refusal: the
// migration ran, and Err, when set, is the cause.
type MigrationReopenError struct {
	Reason string
	Err    error
}

func (e *MigrationReopenError) Error() string {
	msg := "node: encryption migration reopen failed: " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause, if any.
func (e *MigrationReopenError) Unwrap() error {
	return e.Err
}

// EnableEncryption migrates a previously-unencrypted barrier to a keyed
// strategy in place on this running node, with no process restart. It is the
// node-level entrypoint behind the BarrierService enable-encryption operation.
//
// Scope routing is handled by the cached n.encMigrator (a
// server.scopedEncryptionMigrator built in wireClientAndServer):
//   - ScopeSystem  → nodeSystemMigrator.EnableEncryption (holds the lifecycle gate)
//   - ScopeTenant  → server.TenantHostRegistry.MigrateEncryption (per-tenant guard)
//
// Both the direct in-process call here and the transport handlers on every
// protocol server flow through the same scoped-router semantics, guaranteeing
// identical routing, authority, and entitlement checks.
func (n *Node) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	if n.lifeGate.isClosing() {
		return ErrNodeClosed
	}
	mig := n.encMigrator.Load()
	if mig == nil {
		// Close retracts the router, so a nil router read after the check
		// above may mean a Close that began in between: say so rather than
		// "not yet booted".
		if n.lifeGate.isClosing() {
			return ErrNodeClosed
		}
		if n.barrierRegistry.Load() == nil {
			return &MigrationNotSupportedError{Reason: "no barrier registry (seal not enabled)"}
		}
		return &MigrationNotSupportedError{Reason: "node not yet fully booted; call Unseal first"}
	}
	return (*mig).EnableEncryption(ctx, scope, opts)
}

// admitTenantMigration is the server.TenantMigrationAdmission for this node:
// a tenant-scope migration holds the lifecycle gate SHARED — alongside other
// tenant migrations and a system migration, which keep their own guards — so
// Close waits for it to finish, and one that arrives after Close has begun is
// refused with ErrNodeClosed (go-qrdb#226).
func (n *Node) admitTenantMigration(_ context.Context) (func(), error) {
	if err := n.lifeGate.enterShared(); err != nil {
		return nil, err
	}
	return n.lifeGate.leaveShared, nil
}

// nodeEmbeddedMigrator adapts *Node to service.EncryptionMigrator for the
// embedded SDK transport (sdk/go/transport/embedded), wired in via
// embedded.WithEncryptionMigrator in buildEmbeddedOpts. It resolves
// n.encMigrator on every call rather than capturing a value once: the
// embedded transport (like the network transports) is built once per
// data-plane construction, and a system-scope migration replaces
// n.encMigrator's target in place (see wireClientAndServer) without
// necessarily rebuilding the transport around it, so a captured value could
// go stale across that recycle where a live re-resolve does not.
//
// Unlike Node.EnableEncryption, which is the direct Go API and returns a
// Node-specific *MigrationNotSupportedError with a reason, this adapter
// returns the shared service.ErrMigrationProviderUnavailable sentinel every
// other transport (REST/gRPC/Unix/QUIC/MCP) uses for "not available" --
// giving a caller checking for that sentinel through the embedded transport
// the same answer it would get anywhere else, regardless of the Node-level
// reason (seal not enabled vs. not yet unsealed).
type nodeEmbeddedMigrator struct {
	node *Node
}

// EnableEncryption implements service.EncryptionMigrator by delegating to the
// node's own live scoped migrator, or refusing with
// service.ErrMigrationProviderUnavailable when none is configured yet.
func (m *nodeEmbeddedMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	mig := m.node.encMigrator.Load()
	if mig == nil {
		return service.ErrMigrationProviderUnavailable
	}
	return (*mig).EnableEncryption(ctx, scope, opts)
}

// Compile-time check that nodeEmbeddedMigrator implements service.EncryptionMigrator.
var _ service.EncryptionMigrator = (*nodeEmbeddedMigrator)(nil)

// nodeSystemMigrator adapts *Node to service.EncryptionMigrator for the system
// scope only. It holds n.lifecycle for the entire duration of each system
// migration so the data-plane tear-down and rebuild are serialized. Tenant
// scope is NOT handled here; the scoped router in n.encMigrator (built by
// server.NewScopedEncryptionMigrator) intercepts tenant scope and routes it to
// TenantHostRegistry.MigrateEncryption before this adapter is ever consulted.
type nodeSystemMigrator struct {
	node *Node
}

// EnableEncryption handles system-scope migration by building an
// EncryptionMigrationService over the node's barrier registry and the
// nodeMigrationProvider, then calling EnableSystemEncryption. The lifecycle gate
// lock is held for the entire operation so concurrent system migrations are
// serialized and the data-plane recycle (tear-down → re-encrypt → rebuild) is
// atomic from the caller's perspective.
func (m *nodeSystemMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	switch scope.Kind {
	case service.ScopeSystem:
		// The node's lifecycle lock: Close waits for this migration to finish
		// rather than retracting the barrier underneath it (go-qrdb#226), and a
		// migration that arrives after Close is refused.
		if err := m.node.lifeGate.acquire(ctx); err != nil {
			return err
		}
		defer m.node.lifeGate.release()

		barrierRegistry := m.node.barrierRegistry.Load()
		if barrierRegistry == nil {
			return &MigrationNotSupportedError{Reason: "no barrier registry (seal not enabled)"}
		}
		// A node with a barrier registry but no seal store is not a state
		// this package's own construction paths produce today -- both build
		// the two together in buildNodeSealSubsystem -- but Reopen's
		// canonicalRootKeyStrategy call depends on the seal store existing,
		// and refusing HERE, before Quiesce ever tears the data plane down,
		// is strictly better than the same refusal surfacing deep inside a
		// migration already in progress. See canonicalRootKeyStrategy's doc
		// comment for the read this specifically guards.
		if m.node.sealStore == nil {
			return &MigrationNotSupportedError{Reason: "no seal store (this backend cannot hold a seal store)"}
		}
		prov := &nodeMigrationProvider{node: m.node}
		svc := service.NewEncryptionMigrationService(barrierRegistry, prov, m.node.logger)
		return svc.EnableSystemEncryption(ctx, opts)
	case service.ScopeTenant:
		// Tenant scope must be routed via TenantHostRegistry, which recycles
		// only the targeted tenant's Host (not the system data plane). The scoped
		// router in Node.encMigrator intercepts tenant scope before reaching this
		// adapter, so this branch is defensive dead code in normal operation.
		return &MigrationNotSupportedError{Reason: "tenant scope must be routed via TenantHostRegistry (scoped router intercepted before this adapter)"}
	default:
		return &MigrationNotSupportedError{Reason: "unknown scope " + string(scope.Kind)}
	}
}

// Compile-time check that nodeSystemMigrator implements service.EncryptionMigrator.
var _ service.EncryptionMigrator = (*nodeSystemMigrator)(nil)

// nodeTenantResolver adapts Node to server.TenantServiceResolver by resolving
// n.svcNode.Load().Tenant on EVERY call instead of once. It is the value
// wireClientAndServer passes to server.NewScopedEncryptionMigrator so the
// resulting router's entitlement (CheckBarrierMode) and isolation-routing
// (GetTenant) checks always see whichever *tenant.Service is currently live.
//
// This matters because wireClientAndServer runs once per data-plane build,
// but the router it produces is deliberately kept alive across a
// system-scope migration's teardownDataPlane→rebuild window (see
// Node.retractDataPlaneServices): TenantHostRegistry and RaftBackendFactory,
// which perform the actual tenant-scope recycle, survive that window by
// design, so a concurrent tenant-scope migration proceeds independently of
// the system one. svcNode does NOT survive the window, though — it is
// swapped to nil in teardownDataPlane and only reappears once Reopen
// rebuilds it, which for a committed Shamir target does not happen until a
// Security Officer quorum unseals the barrier (nodeMigrationProvider.Reopen
// returns after storing a seal-window-only svcNode and leaves the full
// rebuild pending). A resolver captured once at construction would keep
// pointing at the STOPPED pre-recycle *tenant.Service for that whole window;
// resolving through n.svcNode on every call means there is no captured
// handle to go stale, and no lifecycle hook that has to remember to refresh
// one (go-qrdb#222).
type nodeTenantResolver struct {
	node *Node
}

// CheckBarrierMode delegates to the tenant service on the CURRENTLY live
// svcNode. It returns *service.DataPlaneRecyclingError -- temporary, so every
// transport answers it Unavailable -- when no tenant service is live right now — which is only reachable during the narrow synchronous
// window inside teardownDataPlane where svcNode has been swapped to nil and
// the rebuild has not yet stored a replacement (the router's earlier checks
// in EnableEncryption already require server.TenantHostRegistry or
// server.RaftBackendFactory to be wired before reaching here, and both
// require a tenant service to have existed at construction time, so an
// otherwise tenant-disabled node never reaches this branch).
func (r *nodeTenantResolver) CheckBarrierMode(ctx context.Context, tenantID string, mode seal.StrategyID) error {
	sv := r.node.svcNode.Load()
	if sv == nil || sv.Tenant == nil {
		return &service.DataPlaneRecyclingError{Service: "tenant"}
	}
	return sv.Tenant.CheckBarrierMode(ctx, tenantID, mode)
}

// GetTenant delegates to the tenant service on the CURRENTLY live svcNode;
// see CheckBarrierMode for why this resolves on every call rather than
// capturing a handle once.
func (r *nodeTenantResolver) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	sv := r.node.svcNode.Load()
	if sv == nil || sv.Tenant == nil {
		return nil, &service.DataPlaneRecyclingError{Service: "tenant"}
	}
	return sv.Tenant.GetTenant(ctx, tenantID)
}

// nodeMigrationProvider implements service.BarrierMigrationProvider against a
// live Node, performing the online in-process data-plane recycle. It is
// single-flight (the caller holds n.lifecycle), so its per-migration fields are
// race-free for the duration of one migration.
type nodeMigrationProvider struct {
	node *Node
	// creds carries the target keyed credentials from MintKeyedTarget to Reopen
	// so Reopen can re-establish the keyed barrier from the committed root key.
	// Left zero for a Shamir target: ReopenKeyed never attempts Unseal for
	// Shamir (see its doc comment) and instead reopens sealed, pending a
	// Security Officer quorum.
	creds seal.Credentials
	// shamirThreshold / shamirTotal carry the M-of-N scheme from MintKeyedTarget
	// to Reopen (via reopenStrategy) so the reopened Shamir barrier reports the
	// correct scheme before the first share-holder unseal quorum.
	shamirThreshold int
	shamirTotal     int
}

// StoreDirs returns the encfs-wrapped store directories owned by the scoped
// barrier on this node, filtered to those that exist. Only system scope is
// handled here; tenant scope is routed by the scoped router in Node.encMigrator
// to TenantHostRegistry.MigrateEncryption before nodeMigrationProvider is
// consulted, so the tenant branch below is defensive dead code.
func (p *nodeMigrationProvider) StoreDirs(_ context.Context, scope service.BarrierScope) (service.MigrationStores, error) {
	n := p.node
	if scope.Kind != service.ScopeSystem {
		return service.MigrationStores{}, &MigrationNotSupportedError{Reason: "tenant scope is handled by TenantHostRegistry via the scoped router; nodeMigrationProvider is system-only"}
	}
	if n.retainedStandalone == nil {
		return service.MigrationStores{}, &MigrationNotSupportedError{Reason: "data-plane boot params not retained (clustered or sealed node)"}
	}
	dataDir := n.retainedStandalone.dataDir
	pebbleDir := filepath.Join(dataDir, "pebble")
	// The swap and in-place mechanisms copy, verify and swap a store
	// directory, so a Pebble store whose WAL lives elsewhere would lose its
	// WAL-only records and leave that WAL under the old encryption. Refuse
	// it before anything is minted or quiesced.
	if externalPebbleWAL(n.retainedStandalone.cfg, dataDir) != "" && len(existingDirs(pebbleDir)) > 0 {
		return service.MigrationStores{}, &MigrationNotSupportedError{Reason: "the pebble store keeps its write-ahead log outside its directory (storage.wal_dir); encryption migration moves the store directory alone"}
	}
	return service.MigrationStores{
		Pebble: existingDirs(pebbleDir, filepath.Join(dataDir, "vector")),
		File:   existingDirs(filepath.Join(dataDir, "file")),
	}, nil
}

// existingDirs returns those of candidates that are existing directories.
func existingDirs(candidates ...string) []string {
	var dirs []string
	for _, d := range candidates {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// externalPebbleWAL returns the storage.wal_dir the system Pebble store under
// dataDir was opened with (lifecycle.NewLocalStorage), or "" when its WAL
// lives in the store directory: wal_dir unset, or naming that directory.
func externalPebbleWAL(cfg *qrdbconfig.Config, dataDir string) string {
	if cfg == nil || cfg.Storage.WALDir == "" {
		return ""
	}
	if filepath.Clean(cfg.Storage.WALDir) == filepath.Clean(filepath.Join(dataDir, "pebble")) {
		return ""
	}
	return cfg.Storage.WALDir
}

// MintKeyedTarget creates an initialized+unsealed keyed barrier over the seal
// store at a staging root-key path, for the system scope. Tenant scope is
// routed by the scoped router to TenantHostRegistry before reaching here.
//
// A Shamir target cannot go through the generic MintKeyedSibling path below
// (see its doc comment): a plain Initialize against a ShamirStrategy persists
// no key material by design, so it would silently "succeed" while leaving the
// root key unrecoverable. It requires the direct-mode ceremony —
// MintKeyedShamirSibling — which mints the sibling AND splits its root key
// M-of-N to the enrolled Security Officers in one step. This mirrors
// cmd/qrdbd/system_migrate.go's recodeSystemBarrier and
// pkg/server/tenant_migrate.go's MigrateEncryption, the daemon-side twins of
// this method: routing a Shamir target through the generic passphrase-shaped
// path there (as this method previously did) mints a mislabeled sealed root
// key with no officer shares — the exact security downgrade this closes.
func (p *nodeMigrationProvider) MintKeyedTarget(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) (*seal.StorageBarrier, error) {
	n := p.node
	if scope.Kind != service.ScopeSystem {
		return nil, &MigrationNotSupportedError{Reason: "tenant scope is handled by TenantHostRegistry via the scoped router; nodeMigrationProvider is system-only"}
	}

	if opts.TargetStrategy == seal.StrategyShamir {
		target, _, err := n.barrier.Load().MintKeyedShamirSibling(ctx, stagingRootKeySuffix, opts.ShamirThreshold, opts.ShamirTotal)
		if err != nil {
			return nil, err
		}
		p.shamirThreshold = opts.ShamirThreshold
		p.shamirTotal = opts.ShamirTotal
		return target, nil
	}

	strategy, err := keyedStrategyFor(opts)
	if err != nil {
		return nil, err
	}
	// Mint over the system barrier's exact backend + config (same DeploymentID,
	// same RootKeyPath base). DeploymentID participates in DEK derivation, so
	// minting via the source barrier guarantees the migrated data is reproducible
	// by the normal boot path (which constructs the system barrier identically).
	target, err := n.barrier.Load().MintKeyedSibling(ctx, stagingRootKeySuffix, strategy, opts.Credentials)
	if err != nil {
		return nil, err
	}
	p.creds = opts.Credentials
	return target, nil
}

// Quiesce tears down the data plane (server, services, host, data-plane store)
// while keeping the seal layer (barrier, registry, seal store) alive, so the
// migration can re-encrypt the now-closed store directories.
func (p *nodeMigrationProvider) Quiesce(_ context.Context, _ service.BarrierScope, _ []string) error {
	p.node.teardownDataPlane()
	return nil
}

// Reopen rebuilds the data plane. It detects whether the migration committed by
// reading the canonical root key's strategy: if it is now keyed, the in-memory
// none barrier is replaced with a freshly-unsealed keyed barrier before the
// rebuild; if it is still none (migration failed pre-commit), the data plane is
// rebuilt under the original barrier, which was never modified.
//
// A committed Shamir target is special-cased: ReopenKeyed never attempts
// Unseal for Shamir (by design, a Shamir barrier never auto-unseals from
// disk — see its doc comment), so the reopened barrier comes back SEALED,
// pending a Security Officer quorum. In that case the data plane cannot be
// rebuilt yet: only the seal-window services (Barrier/Ceremony/Credential)
// are reconstructed — mirroring the sealed branch of NewStandaloneNode — and
// n.pendingStandalone is left set so Node.Unseal's "already unsealed
// externally" fallthrough (see its doc comment) completes the boot once the
// share holders unseal the barrier through the quorum. This mirrors
// pkg/server/tenant_migrate.go's MigrateEncryption, which likewise skips its
// eager Host re-Attach when the reopened barrier is still sealed.
func (p *nodeMigrationProvider) Reopen(ctx context.Context, scope service.BarrierScope) error {
	n := p.node
	if scope.Kind != service.ScopeSystem {
		return &MigrationNotSupportedError{Reason: "tenant scope is handled by TenantHostRegistry via the scoped router; nodeMigrationProvider is system-only"}
	}
	committedStrategy, err := canonicalRootKeyStrategy(ctx, n)
	if err != nil {
		return err
	}

	pending := n.retainedStandalone
	if pending == nil {
		return &MigrationReopenError{Reason: "retained boot params lost during migration"}
	}

	switch committedStrategy {
	case seal.StrategyPassphrase, seal.StrategyShamir:
		// Committed: replace the live none barrier with the keyed one.
		// ReopenKeyed mints a fresh barrier over n.barrier's exact backend +
		// config (same RootKeyPath, same DeploymentID) and unseals it — except
		// for Shamir, which it deliberately leaves sealed (see the doc comment
		// above).
		reopenStrat, serr := p.reopenStrategy(committedStrategy)
		if serr != nil {
			return serr
		}
		keyed, rerr := n.barrier.Load().ReopenKeyed(ctx, reopenStrat, p.creds)
		// The caller of EnableEncryption owns the credential and destroys it
		// once the migration returns; this was its last reader, so the
		// reference is dropped rather than kept pointing at a dead enclave.
		p.creds = seal.Credentials{}
		if rerr != nil {
			return rerr
		}
		n.barrier.Store(keyed)
		n.barrierRegistry.Load().SetSystemBarrier(keyed)
		// Drop the now-redundant staging blob (best effort); nil-guarded like canonicalRootKeyStrategy.
		if n.sealStore != nil {
			_ = seal.NewStorageAdapter(n.sealStore).Delete(ctx, canonicalRootKeyPath(n)+stagingRootKeySuffix)
		}

		if keyed.IsSealed() {
			sealNode, serr := n.buildSealServices(ctx, lifecycle.Infra{
				DataDir:           pending.dataDir,
				SealBarrier:       keyed,
				BarrierRegistry:   n.barrierRegistry.Load(),
				CredentialCoreSvc: n.credCoreSvc,
				CeremonyDataDir:   filepath.Join(pending.dataDir, "seal"),
				CeremonyCoreSvc:   n.sealedWindowCeremonyCore(),
				Logger:            n.logger,
			}, pending.cfg, pending.serverOpts)
			if serr != nil {
				return &MigrationReopenError{Reason: "rebuild seal-window services after shamir migration", Err: serr}
			}
			n.svcNode.Store(sealNode)
			n.pendingStandalone = pending
			return nil
		}
	default:
		// Not committed: the original barrier is still valid and unsealed.
	}

	// Rebuild the data plane (storage, engine, services, server) under the
	// now-current barrier. retainedStandalone is re-consumed by
	// completeStandaloneBoot, which re-sets it.
	return n.completeStandaloneBoot(ctx, pending)
}

// reopenStrategy builds the keyed sealing strategy ReopenKeyed uses to
// reconstruct the live system barrier after a migration commits. A Shamir
// target reuses the threshold/total MintKeyedTarget recorded (ReopenKeyed
// special-cases the Shamir ID and does not attempt to unseal); every other
// keyed target uses keyedStrategyForStrategy's self-contained credential
// strategy. Mirrors pkg/server/tenant_migrate.go's reopenStrategyFor.
func (p *nodeMigrationProvider) reopenStrategy(id seal.StrategyID) (seal.SealingStrategy, error) {
	if id == seal.StrategyShamir {
		return seal.NewShamirStrategy(p.shamirThreshold, p.shamirTotal)
	}
	return keyedStrategyForStrategy(id, p.creds)
}

// teardownDataPlane closes the server, services, host, and data-plane store,
// leaving the seal layer (barrier, registry, seal store, volume pool) intact.
// It mirrors the data-plane portion of Close.
func (n *Node) teardownDataPlane() {
	// The sweepers revoke and reclaim through the data plane being stopped;
	// the rebuild starts new ones over the new engine and lessor.
	n.stopStandaloneSweepers()
	// So does the trust pool's refresh, reading the CA certificates through
	// this graph's storage; the rebuild starts a new one over the new graph.
	n.stopTrustPool()
	// So does the volume-pool Reporter: its refresh proposes through the
	// MVCC and lease services of this graph, and left running it would keep
	// proposing into an engine whose storage is closed below. Its Close
	// revokes its lease, so it goes while that graph still serves; the
	// rebuild starts a new Reporter over the new services.
	n.stopVolumeReporter()
	if client := n.client.Swap(nil); client != nil {
		_ = client.Close()
	}
	n.closeFeatureClient()
	if srv := n.srv.Swap(nil); srv != nil {
		srv.Stop()
	}
	// Retract the service handles that belong to the graph being stopped, so
	// the recycle window cannot hand out a live-looking factor service or ABAC
	// reloader onto stopped backing services. wireClientAndServer republishes
	// them on the rebuild; the Shamir-committed branch of Reopen never gets
	// there (it returns sealed, pending quorum), and leaving them published
	// through THAT window is exactly the stale handle this closes.
	n.retractDataPlaneServices()
	if svcNode := n.svcNode.Swap(nil); svcNode != nil {
		// The ceremony service holds the enrolled share holders and the
		// envelopes a Shamir migration has just distributed; the rebuild
		// reuses it rather than building a new one that would lose them.
		if svcNode.Ceremony != nil {
			if core, ok := service.CeremonyServiceCore(svcNode.Ceremony); ok {
				n.carriedCeremony.Store(core)
			}
		}
		_ = svcNode.Stop(context.Background())
	}
	if n.host != nil {
		_ = n.host.Close()
		n.host = nil
	}
	if n.stg != nil {
		_ = n.stg.Close()
		n.stg = nil
		n.pebbleDB = nil
	}
	// The standalone rebuild reopens the catalog (newStandaloneDatabaseManager)
	// against the rebuilt barrier.
	n.closeDatabaseManager()
}

// canonicalRootKeyPath returns the system barrier's root-key path.
func canonicalRootKeyPath(n *Node) string {
	if n.retainedStandalone != nil && n.retainedStandalone.cfg != nil {
		if p := n.retainedStandalone.cfg.Seal.RootKeyPath; p != "" {
			return p
		}
	}
	return "root-key"
}

// canonicalRootKeyStrategy reads the persisted canonical root-key blob and
// returns its sealing strategy, distinguishing a committed (keyed) migration
// from a rolled-back (still-none) one.
//
// It reads through n.sealStore's seal.LegacyStorage interface rather than
// assuming any concrete backend, and checks for nil explicitly before
// handing it to seal.NewStorageAdapter: passing a nil *CONCRETE* pointer
// into that call would satisfy the interface with a non-nil value wrapping
// a nil receiver, so the nil would survive past the point where an explicit
// check could still catch it and crash on the eventual read (go-qrdb#550) --
// a backend that cannot hold a seal store at all must refuse here, never
// segfault.
func canonicalRootKeyStrategy(ctx context.Context, n *Node) (seal.StrategyID, error) {
	if n.sealStore == nil {
		return "", &MigrationNotSupportedError{Reason: "no seal store (this backend cannot hold a seal store)"}
	}
	blob, err := seal.NewStorageAdapter(n.sealStore).Get(ctx, canonicalRootKeyPath(n))
	if err != nil {
		return "", err
	}
	var sealed struct {
		Strategy seal.StrategyID `json:"strategy"`
	}
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return "", err
	}
	return sealed.Strategy, nil
}

// keyedStrategyFor builds the keyed sealing strategy for the migration
// options. A Shamir target never reaches here: MintKeyedTarget special-cases
// it into the MintKeyedShamirSibling ceremony before this is called (see its
// doc comment) — routing it through this generic passphrase-shaped helper
// instead is exactly the security downgrade that fix closes.
func keyedStrategyFor(opts seal.MigrationOptions) (seal.SealingStrategy, error) {
	return keyedStrategyForStrategy(opts.TargetStrategy, opts.Credentials)
}

// keyedStrategyForStrategy builds the keyed sealing strategy for id. Like
// keyedStrategyFor, a Shamir id never reaches here: reopenStrategy
// special-cases StrategyShamir into seal.NewShamirStrategy before falling
// back to this helper, so only StrategyPassphrase is ever a valid input.
func keyedStrategyForStrategy(id seal.StrategyID, _ seal.Credentials) (seal.SealingStrategy, error) {
	switch id {
	case seal.StrategyPassphrase:
		// Use the SAME passphrase strategy the boot path uses for software
		// barriers (NewDeferredSoftwareStrategy). The sealed root-key blob must
		// be unsealable by the strategy the node registers on a subsequent boot;
		// minting the target with a different software strategy implementation
		// would seal a blob the restart path cannot decrypt. The passphrase is
		// supplied to Initialize/Unseal via Credentials, which the deferred
		// strategy captures at first use.
		return seal.NewDeferredSoftwareStrategy(), nil
	default:
		return nil, seal.ErrMigrationInvalidTarget
	}
}

var _ service.BarrierMigrationProvider = (*nodeMigrationProvider)(nil)

// compile-time reference so slog import is retained even if logging is trimmed.
var _ = slog.LevelInfo
