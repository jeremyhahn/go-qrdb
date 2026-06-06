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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// DatabaseBarrierManager wraps a seal.BarrierRegistry and manages
// per-database encryption barriers. Each database with barrier encryption
// enabled gets its own tenant barrier in the registry, providing
// cryptographic isolation between databases.
type DatabaseBarrierManager struct {
	logger   *slog.Logger
	registry *seal.BarrierRegistry
}

// NewDatabaseBarrierManager creates a new DatabaseBarrierManager that
// delegates barrier lifecycle operations to the given registry.
func NewDatabaseBarrierManager(logger *slog.Logger, registry *seal.BarrierRegistry) *DatabaseBarrierManager {
	return &DatabaseBarrierManager{
		logger:   logger,
		registry: registry,
	}
}

// EnsureBarrier creates and initializes an encryption barrier for the
// named database when cfg.Enabled is true. The barrier is registered as
// a tenant in the underlying registry and immediately initialized with
// the provided credentials.
//
// If cfg is nil or cfg.Enabled is false, the method returns nil without
// creating a barrier.
func (m *DatabaseBarrierManager) EnsureBarrier(
	ctx context.Context,
	dbName string,
	cfg *DatabaseBarrierConfig,
	storage seal.StorageBackend,
	creds seal.Credentials,
) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}

	barrierCfg := seal.BarrierConfig{
		RootKeyPath: "db/" + dbName + "/root-key",
	}

	strategy, err := seal.NewSoftwareStrategy([]byte(creds.Secret))
	if err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}

	_, err = m.registry.RegisterTenant(ctx, dbName, storage, barrierCfg, strategy)
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
	dbName string,
	cfg *DatabaseBarrierConfig,
	storage seal.StorageBackend,
) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}

	barrierCfg := seal.BarrierConfig{
		RootKeyPath: "db/" + dbName + "/root-key",
	}

	// Use a zero-length secret strategy as a placeholder — the real
	// credentials are provided during UnsealDatabase.
	strategy, err := seal.NewSoftwareStrategy(make([]byte, 32))
	if err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}

	// RegisterBarrier always supplies a real backend, so the pool path
	// is never engaged here. context.Background is sufficient because
	// RegisterTenant only consults ctx on the pool path.
	_, err = m.registry.RegisterTenant(context.Background(), dbName, storage, barrierCfg, strategy)
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
