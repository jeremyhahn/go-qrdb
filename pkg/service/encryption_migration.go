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

package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrSharedBarrierImmutable is returned when a tenant on the shared system
	// barrier attempts to enable encryption: such a tenant inherits the
	// system/shared barrier's settings and has no authority to change it. Only
	// a tenant with its OWN dedicated (isolated) barrier may migrate it.
	ErrSharedBarrierImmutable = errors.New("service: tenant uses the shared system barrier and cannot change its encryption; isolate the tenant first")

	// ErrMigrationProviderUnavailable is returned when no BarrierMigrationProvider
	// has been wired (the node does not support online encryption migration in
	// this deployment, e.g. an embedded/no-barrier server).
	ErrMigrationProviderUnavailable = errors.New("service: encryption migration not available on this node")

	// ErrTenantBarrierNotFound is returned when a tenant-scoped migration names
	// a tenant that has no registered dedicated barrier.
	ErrTenantBarrierNotFound = errors.New("service: tenant has no dedicated barrier")
)

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

// BarrierScopeKind identifies which barrier an encryption migration targets.
// Migrations are ALWAYS scoped — there is no global "encrypt everything"
// operation. The system scope is admin-only; the tenant scope applies to a
// single tenant's dedicated barrier.
type BarrierScopeKind string

const (
	// ScopeSystem targets the node/system barrier (admin authority).
	ScopeSystem BarrierScopeKind = "system"
	// ScopeTenant targets a single tenant's dedicated barrier.
	ScopeTenant BarrierScopeKind = "tenant"
)

// BarrierScope names the barrier an encryption migration targets.
type BarrierScope struct {
	Kind     BarrierScopeKind
	TenantID string // required when Kind == ScopeTenant
}

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

// BarrierMigrationProvider supplies the node-specific mechanics an encryption
// migration needs, keeping the service decoupled from daemon/store internals.
// The daemon implements it: it owns store directories and their open/close
// lifecycle.
//
// The flow the service drives is: resolve the source barrier + its owned store
// dirs, mint a keyed target barrier, Quiesce (online: pause writes + close the
// stores), run the re-encryption, then Reopen the stores under the now-keyed
// barrier and resume writes.
type BarrierMigrationProvider interface {
	// StoreDirs returns the physical store directories owned by the scoped
	// barrier on THIS node. For a clustered tenant/db barrier this is the local
	// replica's copy; the cluster coordinator invokes the migration on every
	// owning node.
	StoreDirs(ctx context.Context, scope BarrierScope) ([]string, error)

	// MintKeyedTarget creates and initializes a fresh keyed target barrier for
	// the scope (over a staging root-key path in the same seal backend), per the
	// requested strategy and credentials. Its Encryptor encrypts the migrated
	// data and its sealed root key is adopted at commit.
	MintKeyedTarget(ctx context.Context, scope BarrierScope, opts seal.MigrationOptions) (*seal.StorageBarrier, error)

	// Quiesce pauses writes to the scoped barrier's stores and closes them so
	// the migration can re-encrypt their files. For the online availability
	// mode the provider buffers/blocks incoming writes; for quiesced it returns
	// a maintenance window.
	Quiesce(ctx context.Context, scope BarrierScope, dirs []string) error

	// Reopen reopens the scoped barrier's stores under the now-keyed barrier and
	// resumes writes (replaying any buffered delta for the online mode). It runs
	// even when the migration fails, reopening under the original barrier.
	Reopen(ctx context.Context, scope BarrierScope) error
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// EncryptionMigrationService enables at-rest encryption on a previously
// unencrypted barrier, scoped to either the system barrier or a single tenant's
// dedicated barrier. It enforces the authority model: system migrations are
// admin-only (gated at the transport layer by the seal:enable_encryption
// permission), and a tenant may only migrate its OWN dedicated barrier — a
// tenant on the shared system barrier is rejected with ErrSharedBarrierImmutable.
type EncryptionMigrationService struct {
	registry *seal.BarrierRegistry
	provider BarrierMigrationProvider
	logger   *slog.Logger
}

// NewEncryptionMigrationService constructs the service. provider may be nil on
// deployments that do not support online migration; the enable methods then
// return ErrMigrationProviderUnavailable.
func NewEncryptionMigrationService(registry *seal.BarrierRegistry, provider BarrierMigrationProvider, logger *slog.Logger) *EncryptionMigrationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &EncryptionMigrationService{registry: registry, provider: provider, logger: logger}
}

// EnableSystemEncryption migrates the node/system barrier from none to the
// keyed strategy in opts. Admin authority is enforced upstream (transport
// permission). It is a no-op-safe error if the system barrier is already keyed
// (opts.Validate + the barrier's none-source guard reject that).
func (s *EncryptionMigrationService) EnableSystemEncryption(ctx context.Context, opts seal.MigrationOptions) error {
	if s.provider == nil || s.registry == nil {
		return ErrMigrationProviderUnavailable
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	source := s.registry.SystemBarrier()
	if source == nil {
		return ErrMigrationProviderUnavailable
	}
	return s.run(ctx, BarrierScope{Kind: ScopeSystem}, source, opts)
}

// EnableTenantEncryption migrates a single tenant's DEDICATED barrier from none
// to the keyed strategy in opts. A tenant on the shared system barrier has no
// dedicated barrier to migrate and is rejected with ErrSharedBarrierImmutable —
// it inherits the system barrier's encryption and cannot change it.
func (s *EncryptionMigrationService) EnableTenantEncryption(ctx context.Context, tenantID string, opts seal.MigrationOptions) error {
	if s.provider == nil || s.registry == nil {
		return ErrMigrationProviderUnavailable
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	tb, err := s.registry.GetTenant(tenantID)
	if err != nil {
		// Not registered as a dedicated barrier → tenant is on the shared
		// system barrier and may not change its encryption.
		if errors.Is(err, seal.ErrTenantNotFound) {
			return ErrSharedBarrierImmutable
		}
		return err
	}
	source := tb.Barrier()
	if source == nil {
		return ErrTenantBarrierNotFound
	}
	return s.run(ctx, BarrierScope{Kind: ScopeTenant, TenantID: tenantID}, source, opts)
}

// run performs the online, in-process migration for a resolved scope+barrier:
// resolve owned dirs → mint keyed target → quiesce stores → re-encrypt → adopt
// keyed root key → reopen stores under the keyed barrier (always, even on
// failure, so the node never strands its data behind closed stores).
func (s *EncryptionMigrationService) run(ctx context.Context, scope BarrierScope, source *seal.StorageBarrier, opts seal.MigrationOptions) (err error) {
	dirs, err := s.provider.StoreDirs(ctx, scope)
	if err != nil {
		return err
	}
	target, err := s.provider.MintKeyedTarget(ctx, scope, opts)
	if err != nil {
		return err
	}

	if qerr := s.provider.Quiesce(ctx, scope, dirs); qerr != nil {
		return qerr
	}
	// Reopen the stores no matter what: on success they reopen under the keyed
	// barrier; on failure under the original none barrier (its root key is
	// untouched until the migration's commit point).
	defer func() {
		if rerr := s.provider.Reopen(ctx, scope); rerr != nil && err == nil {
			err = rerr
		}
	}()

	mig := &database.NodeStoreMigration{
		Source:    source,
		Target:    target,
		StoreDirs: dirs,
		Logger:    s.logger,
	}
	if merr := mig.Run(ctx, opts); merr != nil {
		err = merr
		return err
	}
	s.logger.Info("encryption migration complete",
		slog.String("scope", string(scope.Kind)),
		slog.String("tenant", scope.TenantID),
		slog.String("strategy", string(opts.TargetStrategy)),
		slog.Int("stores", len(dirs)))
	return nil
}
