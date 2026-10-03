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

package database

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// DatabaseBarrierManager manages per-database encryption barriers. Each
// database with barrier encryption enabled gets its own barrier, providing
// cryptographic isolation between databases.
//
// The barriers live in a registry of the manager's own, keyed by database
// name, never in the seal subsystem's tenant registry: a database named like
// a tenant would otherwise resolve to that tenant's barrier, so sealing or
// reading the status of one's own database would seal or describe another
// tenant's barrier.
type DatabaseBarrierManager struct {
	logger   *slog.Logger
	registry *seal.BarrierRegistry

	// holderMu guards holderDeps, which SetHolderDeps may set after
	// construction.
	holderMu   sync.Mutex
	holderDeps *ceremony.HolderDeps
	// holders is the holder authority of every Shamir database barrier,
	// keyed by database name.
	holders *ceremony.ScopedCeremonyRegistry
}

// NewDatabaseBarrierManager creates a DatabaseBarrierManager whose barriers
// live in a registry of its own over system, the node's system barrier.
func NewDatabaseBarrierManager(logger *slog.Logger, system *seal.StorageBarrier) (*DatabaseBarrierManager, error) {
	registry, err := seal.NewBarrierRegistry(logger, system)
	if err != nil {
		return nil, err
	}
	return &DatabaseBarrierManager{
		logger:   logger,
		registry: registry,
		holders:  ceremony.NewScopedCeremonyRegistry(),
	}, nil
}

// EnsureBarrier creates and initializes an encryption barrier for the
// named database when cfg.Enabled is true. The barrier is registered as
// a tenant in the underlying registry and immediately initialized with
// the provided credentials.
//
// If cfg is nil or cfg.Enabled is false, the method returns nil without
// creating a barrier.
//
// A shamir barrier is registered but not initialized: it takes no
// credentials, and its share holders are enrolled and its custody
// initialized afterwards (InitializeShamir). tenantID is the database's
// tenant, in which its holders' certificates are issued.
func (m *DatabaseBarrierManager) EnsureBarrier(
	ctx context.Context,
	dbName, tenantID string,
	cfg *DatabaseBarrierConfig,
	storage seal.StorageBackend,
	creds seal.Credentials,
) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	if isShamir(cfg) {
		if creds.Secret != nil {
			return &ShamirSecretNotAllowedError{Name: dbName}
		}
		return m.registerShamir(ctx, dbName, tenantID, cfg, storage)
	}
	if cfg.Mode != "" && cfg.Mode != BarrierModePassphrase {
		return &UnsupportedBarrierStrategyError{Mode: cfg.Mode}
	}

	barrierCfg := seal.BarrierConfig{
		RootKeyPath: "db/" + dbName + "/root-key",
	}

	// NewDeferredSoftwareStrategy defers passphrase binding to each
	// Seal/Unseal call (it reads Credentials.Secret at call time instead of
	// baking a passphrase into the strategy at construction), matching the
	// canonical software/passphrase factory in pkg/seal/strategy_registry.go.
	// A bound strategy (seal.NewSoftwareStrategy(creds.Secret)) would ignore
	// the creds argument passed to every later UnsealDatabase call in favor
	// of whatever passphrase happened to be given here at create time, which
	// would make wrong-secret unseal attempts succeed for the lifetime of the
	// process and RegisterBarrier's re-open path unable to unseal at all
	// (it has no real passphrase to bind at Open time).
	strategy := seal.NewDeferredSoftwareStrategy()

	_, err := m.registry.RegisterTenant(ctx, dbName, storage, barrierCfg, strategy)
	if err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}

	if err := m.registry.InitializeTenant(ctx, dbName, creds); err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}

	m.logger.Info("database barrier initialized",
		slog.String("database", dbName))

	return nil
}

// RegisterBarrier registers a barrier for the named database without
// initializing it. This is used by Manager.Open for databases that
// already have barrier metadata on disk — the barrier starts sealed
// and must be explicitly unsealed via UnsealDatabase.
//
// Returns nil if cfg is nil or not enabled.
func (m *DatabaseBarrierManager) RegisterBarrier(
	dbName, tenantID string,
	cfg *DatabaseBarrierConfig,
	storage seal.StorageBackend,
) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	if isShamir(cfg) {
		// A shamir barrier reopens under its holders' authority, sealed;
		// a quorum of them unseals it.
		return m.registerShamir(context.Background(), dbName, tenantID, cfg, storage)
	}

	barrierCfg := seal.BarrierConfig{
		RootKeyPath: "db/" + dbName + "/root-key",
	}

	// Deferred strategy: RegisterBarrier never has the real passphrase (the
	// database is only being re-opened, not created), so the strategy must
	// read Credentials.Secret from the later UnsealDatabase call rather than
	// binding a passphrase now. See the matching comment in EnsureBarrier.
	strategy := seal.NewDeferredSoftwareStrategy()

	// RegisterBarrier always supplies a real backend, so the pool path
	// is never engaged here. context.Background is sufficient because
	// RegisterTenant only consults ctx on the pool path.
	_, err := m.registry.RegisterTenant(context.Background(), dbName, storage, barrierCfg, strategy)
	if err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}

	m.logger.Info("database barrier registered (sealed)",
		slog.String("database", dbName))

	return nil
}

// GetBarrier returns the BarrierEncryptor for the named database.
// The returned encryptor wraps the EpochBarrier from the StorageBarrier,
// providing Encrypt/Decrypt/IsSealed for transparent data encryption.
// Returns nil if the database is not registered in the barrier registry.
func (m *DatabaseBarrierManager) GetBarrier(dbName string) BarrierEncryptor {
	tb, err := m.registry.GetTenant(dbName)
	if err != nil {
		return nil
	}
	return tb.Barrier().EpochBarrier()
}

// SealDatabase seals the encryption barrier for the named database,
// blocking all further encrypted storage operations until the barrier
// is unsealed.
func (m *DatabaseBarrierManager) SealDatabase(dbName string) error {
	// A Shamir barrier is sealed by a quorum of its holders, never by one
	// caller, as the system barrier is.
	if m.RequiresQuorum(dbName) {
		return &BarrierSealError{Name: dbName, Err: seal.ErrShamirBarrierRequiresQuorum}
	}
	return m.SealAuthorized(dbName)
}

// RequiresQuorum reports whether dbName's barrier is sealed only by a quorum
// of its share holders (a Shamir barrier), not by one caller.
func (m *DatabaseBarrierManager) RequiresQuorum(dbName string) bool {
	_, ok := m.holders.Get(dbName)
	return ok
}

// SealAuthorized seals dbName's barrier whatever its strategy. Its callers
// carry the authority themselves: a completed holder quorum, or the cluster
// seal record a node enforces. A single caller seals through SealDatabase.
func (m *DatabaseBarrierManager) SealAuthorized(dbName string) error {
	if err := m.registry.SealTenant(dbName); err != nil {
		return &BarrierSealError{Name: dbName, Err: err}
	}
	m.logger.Info("database barrier sealed",
		slog.String("database", dbName))
	return nil
}

// UnsealDatabase unseals the encryption barrier for the named database,
// restoring access to its encrypted storage.
func (m *DatabaseBarrierManager) UnsealDatabase(
	ctx context.Context,
	dbName string,
	creds seal.Credentials,
) error {
	if err := m.registry.UnsealTenant(ctx, dbName, creds); err != nil {
		return &BarrierUnsealError{Name: dbName, Err: err}
	}
	m.logger.Info("database barrier unsealed",
		slog.String("database", dbName))
	return nil
}

// BarrierStatus returns the current status of the encryption barrier
// for the named database.
func (m *DatabaseBarrierManager) BarrierStatus(dbName string) (*seal.BarrierStatus, error) {
	return m.registry.TenantStatus(dbName)
}

// RemoveBarrier seals and removes the encryption barrier for the named
// database. The barrier is closed and unregistered from the registry.
func (m *DatabaseBarrierManager) RemoveBarrier(dbName string) error {
	// A Shamir barrier's holder authority goes with it: every envelope still
	// held for its holders and every pending challenge is destroyed.
	m.holders.Delete(dbName)
	if err := m.registry.RemoveTenant(dbName); err != nil {
		return err
	}
	m.logger.Info("database barrier removed",
		slog.String("database", dbName))
	return nil
}

// ---------------------------------------------------------------------------
// Typed errors
// ---------------------------------------------------------------------------

// BarrierCreateError is returned when creating or initializing a database
// barrier fails.
type BarrierCreateError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the barrier creation failure.
func (e *BarrierCreateError) Error() string {
	return fmt.Sprintf("database: failed to create barrier for database %s: %s",
		strconv.Quote(e.Name), e.Err.Error())
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BarrierCreateError) Unwrap() error {
	return e.Err
}

// BarrierSealError is returned when sealing a database barrier fails.
type BarrierSealError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the barrier seal failure.
func (e *BarrierSealError) Error() string {
	return fmt.Sprintf("database: failed to seal barrier for database %s: %s",
		strconv.Quote(e.Name), e.Err.Error())
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BarrierSealError) Unwrap() error {
	return e.Err
}

// BarrierUnsealError is returned when unsealing a database barrier fails.
type BarrierUnsealError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the barrier unseal failure.
func (e *BarrierUnsealError) Error() string {
	return fmt.Sprintf("database: failed to unseal barrier for database %s: %s",
		strconv.Quote(e.Name), e.Err.Error())
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BarrierUnsealError) Unwrap() error {
	return e.Err
}
