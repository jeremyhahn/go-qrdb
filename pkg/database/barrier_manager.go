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
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// DatabaseBarrierManager manages per-database encryption barriers. Each
// database with barrier encryption enabled gets its own barrier, providing
// cryptographic isolation between databases.
//
// The barriers live in a registry of the manager's own, keyed by the
// database's tenant and name (barrierKey), never in the seal subsystem's
// tenant registry: a database named like a tenant would otherwise resolve to
// that tenant's barrier, so sealing or reading the status of one's own
// database would seal or describe another tenant's barrier. Two tenants may
// each hold a database of one name, and each has a barrier of its own.
type DatabaseBarrierManager struct {
	logger   *slog.Logger
	registry *seal.BarrierRegistry

	// holderMu guards holderDeps, which SetHolderDeps may set after
	// construction.
	holderMu   sync.Mutex
	holderDeps *ceremony.HolderDeps
	// holders is the holder authority of every Shamir database barrier,
	// keyed by barrierKey.
	holders *ceremony.ScopedCeremonyRegistry
	// auditTrail is the compliance trail every database barrier records its
	// events on, keyed by the database's tenant (WithBarrierAuditTrail).
	auditTrail audit.AuditLogger
	// strategies resolves a barrier mode to a sealing strategy before the
	// built-in passphrase default (WithBarrierStrategyRegistry). Nil resolves
	// only the built-in modes.
	strategies seal.StrategyRegistry
}

// BarrierManagerOption configures a DatabaseBarrierManager.
type BarrierManagerOption func(*DatabaseBarrierManager)

// WithBarrierStrategyRegistry makes the registry the place a database barrier's
// mode is resolved to a sealing strategy, the seam the system and tenant
// barriers already have: a composer registers a hardware strategy under a name
// (go-xkms: tpm2, pkcs11, a cloud KMS) and a database whose barrier mode names
// it is sealed by that device. A mode the registry does not hold falls back to
// the built-in strategy for passphrase, and is refused with
// UnsupportedBarrierStrategyError for any other. Shamir is not resolved here:
// its custody is the holder authority. Nil keeps the built-in modes only.
func WithBarrierStrategyRegistry(reg seal.StrategyRegistry) BarrierManagerOption {
	return func(m *DatabaseBarrierManager) { m.strategies = reg }
}

// strategyFor resolves mode, with the empty mode meaning passphrase, to the
// sealing strategy of a database barrier.
func (m *DatabaseBarrierManager) strategyFor(dbName, mode string) (seal.SealingStrategy, error) {
	if mode == "" {
		mode = BarrierModePassphrase
	}
	if m.strategies != nil {
		if factory, err := m.strategies.LookupFactory(seal.StrategyID(mode)); err == nil {
			strategy, ferr := factory()
			if ferr != nil {
				return nil, &BarrierCreateError{Name: dbName, Err: ferr}
			}
			return strategy, nil
		}
	}
	if mode == BarrierModePassphrase {
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
		return seal.NewDeferredSoftwareStrategy(), nil
	}
	return nil, &UnsupportedBarrierStrategyError{Mode: mode}
}

// SupportsMode reports whether a database barrier can be created in mode: one
// of the built-in modes, or a strategy the registry holds.
func (m *DatabaseBarrierManager) SupportsMode(mode string) bool {
	if mode == "" || mode == BarrierModePassphrase || mode == BarrierModeShamir {
		return true
	}
	if m.strategies == nil {
		return false
	}
	_, err := m.strategies.LookupFactory(seal.StrategyID(mode))
	return err == nil
}

// WithBarrierAuditTrail records the events of every database barrier the
// manager builds -- initialize, unseal, seal, quorum-complete and the rest --
// on sink, the node's compliance trail, keyed by the database's tenant and
// naming the database (seal.NewComplianceAuditLogger). Nil records nothing.
func WithBarrierAuditTrail(sink audit.AuditLogger) BarrierManagerOption {
	return func(m *DatabaseBarrierManager) { m.auditTrail = sink }
}

// NewDatabaseBarrierManager creates a DatabaseBarrierManager whose barriers
// live in a registry of its own over system, the node's system barrier.
func NewDatabaseBarrierManager(logger *slog.Logger, system *seal.StorageBarrier, opts ...BarrierManagerOption) (*DatabaseBarrierManager, error) {
	registry, err := seal.NewBarrierRegistry(logger, system)
	if err != nil {
		return nil, err
	}
	m := &DatabaseBarrierManager{
		logger:   logger,
		registry: registry,
		holders:  ceremony.NewScopedCeremonyRegistry(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m, nil
}

// barrierKey is the registry and holder key of dbName's barrier in tenantID:
// the database's tenant, then its name, so two tenants holding one name hold
// two barriers. A tenant ID never contains a '/' (the tenant package refuses
// one), so the first '/' splits the key back (splitBarrierKey).
func barrierKey(dbName, tenantID string) string {
	return tenantid.Normalize(tenantID) + "/" + dbName
}

// splitBarrierKey returns the database name and tenant barrierKey joined.
func splitBarrierKey(key string) (dbName, tenantID string) {
	tenantID, dbName, _ = strings.Cut(key, "/")
	return dbName, tenantID
}

// barrierConfig is the BarrierConfig of dbName's barrier in tenantID: its
// root-key path, and its trail on the compliance audit.
func (m *DatabaseBarrierManager) barrierConfig(dbName, tenantID string) seal.BarrierConfig {
	cfg := seal.BarrierConfig{
		RootKeyPath: "db/" + dbName + "/root-key",
		AuditLogger: seal.NewComplianceAuditLogger(m.auditTrail, tenantID, seal.DatabaseBarrierAuditName(tenantID, dbName)),
	}
	// A database barrier drains its stores within the node's
	// seal.drain_timeout: the system barrier's resolved bound.
	if system := m.registry.SystemBarrier(); system != nil {
		cfg.SealDrainTimeout = system.Config().SealDrainTimeout
	}
	return cfg
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
	strategy, err := m.strategyFor(dbName, cfg.Mode)
	if err != nil {
		return err
	}

	barrierCfg := m.barrierConfig(dbName, tenantID)
	key := barrierKey(dbName, tenantID)

	_, err = m.registry.RegisterTenant(ctx, key, storage, barrierCfg, strategy)
	if err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}

	if err := m.registry.InitializeTenant(ctx, key, creds); err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}

	m.logger.Info("database barrier initialized",
		slog.String("tenant", tenantid.Normalize(tenantID)),
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

	// RegisterBarrier never has the real passphrase (the database is only
	// being re-opened, not created), so a passphrase strategy must read
	// Credentials.Secret from the later UnsealDatabase call rather than binding
	// one now; strategyFor returns that deferred strategy.
	strategy, err := m.strategyFor(dbName, cfg.Mode)
	if err != nil {
		return err
	}

	barrierCfg := m.barrierConfig(dbName, tenantID)

	// RegisterBarrier always supplies a real backend, so the pool path
	// is never engaged here. context.Background is sufficient because
	// RegisterTenant only consults ctx on the pool path.
	_, err = m.registry.RegisterTenant(context.Background(), barrierKey(dbName, tenantID), storage, barrierCfg, strategy)
	if err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}

	m.logger.Info("database barrier registered (sealed)",
		slog.String("tenant", tenantid.Normalize(tenantID)),
		slog.String("database", dbName))

	return nil
}

// GetBarrier returns the BarrierEncryptor for the named database of tenantID.
// The returned encryptor wraps the EpochBarrier from the StorageBarrier,
// providing Encrypt/Decrypt/IsSealed for transparent data encryption, and
// exposes the StorageBarrier's quiesce gate, so a BarrierBackend over it
// holds a lease the database seal drains.
// Returns nil if the database is not registered in the barrier registry.
func (m *DatabaseBarrierManager) GetBarrier(dbName, tenantID string) BarrierEncryptor {
	tb, err := m.registry.GetTenant(barrierKey(dbName, tenantID))
	if err != nil {
		return nil
	}
	sb := tb.Barrier()
	return gatedEncryptor{BarrierEncryptor: sb.EpochBarrier(), gate: sb.Quiesce()}
}

// gatedEncryptor is a database barrier's encryptor together with its
// StorageBarrier's quiesce gate.
type gatedEncryptor struct {
	BarrierEncryptor
	gate *quiesce.Gate
}

// Quiesce returns the gate the database barrier's seal closes and drains.
func (g gatedEncryptor) Quiesce() *quiesce.Gate { return g.gate }

// SealDatabase seals the encryption barrier for the named database of
// tenantID, blocking all further encrypted storage operations until the
// barrier is unsealed.
func (m *DatabaseBarrierManager) SealDatabase(dbName, tenantID string) error {
	// A Shamir barrier is sealed by a quorum of its holders, never by one
	// caller, as the system barrier is.
	if m.RequiresQuorum(dbName, tenantID) {
		return &BarrierSealError{Name: dbName, Err: seal.ErrShamirBarrierRequiresQuorum}
	}
	return m.SealAuthorized(dbName, tenantID)
}

// SealAll seals every database barrier this manager holds -- the database
// half of a whole-node seal -- leaving the shared system barrier to the
// node's own registry. It records, under the registry's lock, the
// system-managed database barriers it moves from unsealed to sealed as the
// node seal's to bring back when the system barrier unseals; an
// owner-managed (passphrase) or Shamir barrier is never brought back, and a
// barrier already sealed -- by its owner, its quorum or its cluster seal
// record -- stays the way it was sealed (seal.BarrierRegistry.SealTenants).
func (m *DatabaseBarrierManager) SealAll() error {
	return m.registry.SealTenants()
}

// SetUnsealGate makes the node unseal leave sealed every database for which
// refuses reports true -- the composer passes the cluster seal's verdict on
// the database's record -- as seal.WithTenantUnsealGate does for tenants.
// refuses is asked with the database's name and tenant.
func (m *DatabaseBarrierManager) SetUnsealGate(refuses func(dbName, tenantID string) bool) {
	m.registry.Attach(seal.WithTenantUnsealGate(func(key string) bool {
		return refuses(splitBarrierKey(key))
	}))
}

// UnsealRefused reports whether the node unseal would leave the named
// database of tenantID sealed because the unseal gate refuses it
// (SetUnsealGate). Without a gate it refuses none.
func (m *DatabaseBarrierManager) UnsealRefused(dbName, tenantID string) bool {
	return m.registry.TenantUnsealRefused(barrierKey(dbName, tenantID))
}

// PendingUnseals returns the databases the last node seal sealed whose
// unseal with the node barrier has not succeeded, each with the error of its
// last attempt (seal.BarrierRegistry.PendingTenantUnseals), keyed by
// "<tenant>/<database>".
func (m *DatabaseBarrierManager) PendingUnseals() map[string]error {
	return m.registry.PendingTenantUnseals()
}

// RequiresQuorum reports whether the barrier of tenantID's dbName is sealed
// only by a quorum of its share holders (a Shamir barrier), not by one
// caller.
func (m *DatabaseBarrierManager) RequiresQuorum(dbName, tenantID string) bool {
	_, ok := m.holders.Get(barrierKey(dbName, tenantID))
	return ok
}

// SealAuthorized seals the barrier of tenantID's dbName whatever its
// strategy. Its callers carry the authority themselves: a completed holder
// quorum, or the cluster seal record a node enforces. A single caller seals
// through SealDatabase.
func (m *DatabaseBarrierManager) SealAuthorized(dbName, tenantID string) error {
	if err := m.registry.SealTenant(barrierKey(dbName, tenantID)); err != nil {
		return &BarrierSealError{Name: dbName, Err: err}
	}
	m.logger.Info("database barrier sealed",
		slog.String("tenant", tenantid.Normalize(tenantID)),
		slog.String("database", dbName))
	return nil
}

// UnsealDatabase unseals the encryption barrier for the named database of
// tenantID, restoring access to its encrypted storage.
func (m *DatabaseBarrierManager) UnsealDatabase(
	ctx context.Context,
	dbName, tenantID string,
	creds seal.Credentials,
) error {
	if err := m.registry.UnsealTenant(ctx, barrierKey(dbName, tenantID), creds); err != nil {
		return &BarrierUnsealError{Name: dbName, Err: err}
	}
	m.logger.Info("database barrier unsealed",
		slog.String("tenant", tenantid.Normalize(tenantID)),
		slog.String("database", dbName))
	return nil
}

// BarrierStatus returns the current status of the encryption barrier
// for the named database of tenantID.
func (m *DatabaseBarrierManager) BarrierStatus(dbName, tenantID string) (*seal.BarrierStatus, error) {
	return m.registry.TenantStatus(barrierKey(dbName, tenantID))
}

// RemoveBarrier seals and removes the encryption barrier for the named
// database of tenantID. The barrier is closed and unregistered from the
// registry.
func (m *DatabaseBarrierManager) RemoveBarrier(dbName, tenantID string) error {
	key := barrierKey(dbName, tenantID)
	// A Shamir barrier's holder authority goes with it: every envelope still
	// held for its holders and every pending challenge is destroyed.
	m.holders.Delete(key)
	if err := m.registry.RemoveTenant(key); err != nil {
		return err
	}
	m.logger.Info("database barrier removed",
		slog.String("tenant", tenantid.Normalize(tenantID)),
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
