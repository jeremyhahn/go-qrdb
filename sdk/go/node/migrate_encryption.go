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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stagingRootKeySuffix is appended to the canonical root-key path to hold the
// keyed target barrier's sealed root key during an in-process migration. It is
// removed once the migration commits (or rolls back).
const stagingRootKeySuffix = ".migrating"

// MigrationNotSupportedError is returned when an encryption migration is
// requested on a node whose backend cannot be migrated (memory/file backends
// have no encfs at-rest store) or whose data-plane boot params were not
// retained (clustered nodes use the cluster coordinator, not this path).
type MigrationNotSupportedError struct {
	Reason string
}

func (e *MigrationNotSupportedError) Error() string {
	return "node: encryption migration not supported: " + e.Reason
}

// EnableEncryption migrates a previously-unencrypted barrier to a keyed
// strategy in place on this running node, with no process restart. It is the
// node-level entrypoint behind the BarrierService enable-encryption operation.
//
// Scope routing is handled by the cached n.encMigrator (a
// server.scopedEncryptionMigrator built in wireClientAndServer):
//   - ScopeSystem  → nodeSystemMigrator.EnableEncryption (holds migrateMu)
//   - ScopeTenant  → server.TenantHostRegistry.MigrateEncryption (per-tenant guard)
//
// Both the direct in-process call here and the transport handlers on every
// protocol server flow through the same scoped-router semantics, guaranteeing
// identical routing, authority, and entitlement checks.
func (n *Node) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	mig := n.encMigrator.Load()
	if mig == nil {
		if n.barrierRegistry.Load() == nil {
			return &MigrationNotSupportedError{Reason: "no barrier registry (seal not enabled)"}
		}
		return &MigrationNotSupportedError{Reason: "node not yet fully booted; call Unseal first"}
	}
	return (*mig).EnableEncryption(ctx, scope, opts)
}

// nodeSystemMigrator adapts *Node to service.EncryptionMigrator for the system
// scope only. It holds n.migrateMu for the entire duration of each system
// migration so the data-plane tear-down and rebuild are serialized. Tenant
// scope is NOT handled here; the scoped router in n.encMigrator (built by
// server.NewScopedEncryptionMigrator) intercepts tenant scope and routes it to
// TenantHostRegistry.MigrateEncryption before this adapter is ever consulted.
type nodeSystemMigrator struct {
	node *Node
}

// EnableEncryption handles system-scope migration by building an
// EncryptionMigrationService over the node's barrier registry and the
// nodeMigrationProvider, then calling EnableSystemEncryption. The migrateMu
// lock is held for the entire operation so concurrent system migrations are
// serialized and the data-plane recycle (tear-down → re-encrypt → rebuild) is
// atomic from the caller's perspective.
func (m *nodeSystemMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	switch scope.Kind {
	case service.ScopeSystem:
		m.node.migrateMu.Lock()
		defer m.node.migrateMu.Unlock()

		barrierRegistry := m.node.barrierRegistry.Load()
		if barrierRegistry == nil {
			return &MigrationNotSupportedError{Reason: "no barrier registry (seal not enabled)"}
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

// nodeMigrationProvider implements service.BarrierMigrationProvider against a
// live Node, performing the online in-process data-plane recycle. It is
// single-flight (the caller holds n.migrateMu), so its per-migration fields are
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
	// correct scheme before the first UnsealWithShare(s) quorum.
	shamirThreshold int
	shamirTotal     int
}

// StoreDirs returns the encfs-wrapped store directories owned by the scoped
// barrier on this node, filtered to those that exist. Only system scope is
// handled here; tenant scope is routed by the scoped router in Node.encMigrator
// to TenantHostRegistry.MigrateEncryption before nodeMigrationProvider is
// consulted, so the tenant branch below is defensive dead code.
func (p *nodeMigrationProvider) StoreDirs(_ context.Context, scope service.BarrierScope) ([]string, error) {
	n := p.node
	if scope.Kind != service.ScopeSystem {
		return nil, &MigrationNotSupportedError{Reason: "tenant scope is handled by TenantHostRegistry via the scoped router; nodeMigrationProvider is system-only"}
	}
	if n.retainedStandalone == nil {
		return nil, &MigrationNotSupportedError{Reason: "data-plane boot params not retained (clustered or sealed node)"}
	}
	dataDir := n.retainedStandalone.dataDir
	candidates := []string{
		filepath.Join(dataDir, "pebble"),
		filepath.Join(dataDir, "vector"),
	}
	var dirs []string
	for _, d := range candidates {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
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
// operator drives the barrier unsealed via UnsealWithShare(s). This mirrors
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
		return &MigrationNotSupportedError{Reason: "retained boot params lost during migration"}
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
		if rerr != nil {
			return rerr
		}
		n.barrier.Store(keyed)
		n.barrierRegistry.Load().SetSystemBarrier(keyed)
		// Drop the now-redundant staging root-key blob (best effort).
		_ = seal.NewStorageAdapter(n.sealStore).Delete(ctx, canonicalRootKeyPath(n)+stagingRootKeySuffix)

		if keyed.IsSealed() {
			sealNode, serr := lifecycle.BuildSealServicesOnly(ctx, lifecycle.Infra{
				DataDir:           pending.dataDir,
				SealBarrier:       keyed,
				BarrierRegistry:   n.barrierRegistry.Load(),
				CredentialCoreSvc: n.credCoreSvc,
				CeremonyDataDir:   filepath.Join(pending.dataDir, "seal"),
				Logger:            n.logger,
			})
			if serr != nil {
				return &MigrationNotSupportedError{Reason: "rebuild seal-window services after shamir migration: " + serr.Error()}
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
	if client := n.client.Swap(nil); client != nil {
		_ = client.Close()
	}
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
func canonicalRootKeyStrategy(ctx context.Context, n *Node) (seal.StrategyID, error) {
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

// persistedRootKeyStrategy reads the persisted sealed root-key blob at
// rootKeyPath and returns its sealing strategy, or "" when no blob exists or it
// cannot be decoded. It lets the boot sequence detect a barrier that was
// migrated to a keyed strategy after the fact (enable-encryption) so it can
// register the matching strategy even when the config still names the
// pre-migration one.
func persistedRootKeyStrategy(ctx context.Context, backend seal.StorageBackend, rootKeyPath string) seal.StrategyID {
	blob, err := backend.Get(ctx, rootKeyPath)
	if err != nil || len(blob) == 0 {
		return ""
	}
	var sealed struct {
		Strategy seal.StrategyID `json:"strategy"`
	}
	if json.Unmarshal(blob, &sealed) != nil {
		return ""
	}
	return sealed.Strategy
}

// strategyRegistered reports whether a strategy with the given ID is already in
// the slice.
func strategyRegistered(strategies []seal.SealingStrategy, id seal.StrategyID) bool {
	for _, s := range strategies {
		if s.ID() == id {
			return true
		}
	}
	return false
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
