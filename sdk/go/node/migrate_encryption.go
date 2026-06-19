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
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
// The node implements service.BarrierMigrationProvider via nodeMigrationProvider
// and drives the migration through service.EncryptionMigrationService, which
// enforces the scope/authority model (system = admin; tenant = the tenant's own
// dedicated barrier only; shared-barrier tenants are rejected).
func (n *Node) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	n.migrateMu.Lock()
	defer n.migrateMu.Unlock()

	if n.barrierRegistry == nil {
		return &MigrationNotSupportedError{Reason: "no barrier registry (seal not enabled)"}
	}
	prov := &nodeMigrationProvider{node: n}
	svc := service.NewEncryptionMigrationService(n.barrierRegistry, prov, n.logger)
	switch scope.Kind {
	case service.ScopeSystem:
		return svc.EnableSystemEncryption(ctx, opts)
	case service.ScopeTenant:
		return svc.EnableTenantEncryption(ctx, scope.TenantID, opts)
	default:
		return &MigrationNotSupportedError{Reason: "unknown scope " + string(scope.Kind)}
	}
}

// nodeMigrationProvider implements service.BarrierMigrationProvider against a
// live Node, performing the online in-process data-plane recycle. It is
// single-flight (the caller holds n.migrateMu), so its per-migration fields are
// race-free for the duration of one migration.
type nodeMigrationProvider struct {
	node *Node
	// creds carries the target keyed credentials from MintKeyedTarget to Reopen
	// so Reopen can re-establish the keyed barrier from the committed root key.
	creds seal.Credentials
}

// StoreDirs returns the encfs-wrapped store directories owned by the scoped
// barrier on this node, filtered to those that exist.
func (p *nodeMigrationProvider) StoreDirs(_ context.Context, scope service.BarrierScope) ([]string, error) {
	n := p.node
	if scope.Kind != service.ScopeSystem {
		return nil, &MigrationNotSupportedError{Reason: "tenant-scoped migration is not yet wired on this node"}
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
// store at a staging root-key path, for the system scope.
func (p *nodeMigrationProvider) MintKeyedTarget(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) (*seal.StorageBarrier, error) {
	n := p.node
	if scope.Kind != service.ScopeSystem {
		return nil, &MigrationNotSupportedError{Reason: "tenant-scoped migration is not yet wired on this node"}
	}
	strat, err := keyedStrategyFor(opts)
	if err != nil {
		return nil, err
	}
	cfg := seal.BarrierConfig{
		RootKeyPath:  canonicalRootKeyPath(n) + stagingRootKeySuffix,
		DeploymentID: n.retainedStandalone.cfg.Raft.DeploymentID,
	}
	target, err := seal.NewStorageBarrier(n.logger, seal.NewStorageAdapter(n.sealStore), cfg, strat)
	if err != nil {
		return nil, err
	}
	if err := target.Initialize(ctx, opts.Credentials); err != nil {
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
func (p *nodeMigrationProvider) Reopen(ctx context.Context, scope service.BarrierScope) error {
	n := p.node
	if scope.Kind != service.ScopeSystem {
		return &MigrationNotSupportedError{Reason: "tenant-scoped migration is not yet wired on this node"}
	}
	committedStrategy, err := canonicalRootKeyStrategy(ctx, n)
	if err != nil {
		return err
	}
	switch committedStrategy {
	case seal.StrategyPassphrase, seal.StrategyShamir:
		// Committed: replace the live none barrier with the keyed one unsealed
		// from the committed canonical root key.
		strat, serr := keyedStrategyForStrategy(committedStrategy, p.creds)
		if serr != nil {
			return serr
		}
		backend := seal.NewStorageAdapter(n.sealStore)
		keyed, berr := seal.NewStorageBarrier(n.logger, backend,
			seal.BarrierConfig{RootKeyPath: canonicalRootKeyPath(n), DeploymentID: n.retainedStandalone.cfg.Raft.DeploymentID}, strat)
		if berr != nil {
			return berr
		}
		if uerr := keyed.Unseal(ctx, p.creds); uerr != nil && !errors.Is(uerr, seal.ErrAlreadyUnsealed) {
			return uerr
		}
		n.barrier = keyed
		n.barrierRegistry.SetSystemBarrier(keyed)
		// Drop the now-redundant staging root-key blob (best effort).
		_ = backend.Delete(ctx, canonicalRootKeyPath(n)+stagingRootKeySuffix)
	default:
		// Not committed: the original barrier is still valid and unsealed.
	}

	// Rebuild the data plane (storage, engine, services, server) under the
	// now-current barrier. retainedStandalone is re-consumed by
	// completeStandaloneBoot, which re-sets it.
	pending := n.retainedStandalone
	if pending == nil {
		return &MigrationNotSupportedError{Reason: "retained boot params lost during migration"}
	}
	return n.completeStandaloneBoot(ctx, pending)
}

// teardownDataPlane closes the server, services, host, and data-plane store,
// leaving the seal layer (barrier, registry, seal store, volume pool) intact.
// It mirrors the data-plane portion of Close.
func (n *Node) teardownDataPlane() {
	if n.client != nil {
		_ = n.client.Close()
		n.client = nil
	}
	if n.srv != nil {
		n.srv.Stop()
		n.srv = nil
	}
	if n.svcNode != nil {
		_ = n.svcNode.Stop(context.Background())
		n.svcNode = nil
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

// keyedStrategyFor builds the keyed sealing strategy for the migration options.
func keyedStrategyFor(opts seal.MigrationOptions) (seal.SealingStrategy, error) {
	return keyedStrategyForStrategy(opts.TargetStrategy, opts.Credentials)
}

func keyedStrategyForStrategy(id seal.StrategyID, creds seal.Credentials) (seal.SealingStrategy, error) {
	switch id {
	case seal.StrategyPassphrase:
		return seal.NewSoftwareStrategy(creds.Secret)
	case seal.StrategyShamir:
		// Shamir target: the share quorum drives unseal; a deferred software
		// strategy initialized with the secret seeds the keyed root key.
		return seal.NewSoftwareStrategy(creds.Secret)
	default:
		return nil, seal.ErrMigrationInvalidTarget
	}
}

var _ service.BarrierMigrationProvider = (*nodeMigrationProvider)(nil)

// compile-time reference so slog import is retained even if logging is trimmed.
var _ = slog.LevelInfo
