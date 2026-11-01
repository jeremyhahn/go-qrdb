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
	"log/slog"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Shamir custody of a database barrier. A database is one more holder scope
// of the one share-holder model (docs/databases/architecture.md): its barrier
// is registered with a Shamir strategy at the M-of-N the database was created
// with, and the database's ScopedCeremonyService is installed on it as its
// distributor and holder verifier, exactly as a tenant barrier's is.

// ShamirSecretNotAllowedError is returned when a database barrier using the
// shamir strategy is created with a secret. Its key is split to its share
// holders; there is no passphrase to give.
type ShamirSecretNotAllowedError struct {
	Name string
}

// Error returns a human-readable description.
func (e *ShamirSecretNotAllowedError) Error() string {
	return "database: the shamir barrier of database " + strconv.Quote(e.Name) +
		" takes no secret: its key is split to its share holders"
}

// InvalidShamirSchemeError is returned when a database is created with a
// Shamir barrier whose M-of-N the seal cannot split: it needs
// 2 <= SharesRequired <= SharesTotal <= MaxShamirShares.
type InvalidShamirSchemeError struct {
	SharesRequired int
	SharesTotal    int
}

// Error returns a human-readable description.
func (e *InvalidShamirSchemeError) Error() string {
	return "database: a shamir barrier of " + strconv.Itoa(e.SharesRequired) + "-of-" + strconv.Itoa(e.SharesTotal) +
		" cannot be split: it needs 2 <= shares required <= shares total <= " + strconv.Itoa(MaxShamirShares)
}

// ShamirCustodyNotConfiguredError is returned when a database's barrier is
// a Shamir barrier but the node has no holder authority to give it: no
// ceremony CA is wired (SetHolderDeps).
type ShamirCustodyNotConfiguredError struct {
	Name string
}

// Error returns a human-readable description.
func (e *ShamirCustodyNotConfiguredError) Error() string {
	return "database: no share-holder custody is configured for the shamir barrier of database " +
		strconv.Quote(e.Name)
}

// Unwrap reports the seal subsystem's sentinel for a barrier with no
// distributor.
func (e *ShamirCustodyNotConfiguredError) Unwrap() error {
	return seal.ErrCeremonyDistributorNotConfigured
}

// NotShamirBarrierError is returned when a share-holder operation names a
// database whose barrier is not a Shamir barrier, or that has no barrier.
type NotShamirBarrierError struct {
	Name string
}

// Error returns a human-readable description.
func (e *NotShamirBarrierError) Error() string {
	return "database: database " + strconv.Quote(e.Name) + " has no shamir barrier"
}

// SetHolderDeps gives the manager what a Shamir database barrier's holder
// authority needs: the ceremony CA and the factor registry. Without it a Shamir database barrier cannot be created or
// reopened.
func (m *DatabaseBarrierManager) SetHolderDeps(deps ceremony.HolderDeps) {
	m.holderMu.Lock()
	defer m.holderMu.Unlock()
	d := deps
	m.holderDeps = &d
}

// isShamir reports whether cfg selects the shamir strategy.
func isShamir(cfg *DatabaseBarrierConfig) bool {
	return cfg != nil && cfg.Mode == BarrierModeShamir
}

// registerShamir registers dbName's barrier under the shamir strategy at
// cfg's M-of-N and installs its holder authority. The barrier is left as it
// is on storage: uninitialized on a create, initialized and sealed on a
// reopen.
func (m *DatabaseBarrierManager) registerShamir(ctx context.Context, dbName, tenantID string, cfg *DatabaseBarrierConfig, storage seal.StorageBackend) error {
	m.holderMu.Lock()
	deps := m.holderDeps
	m.holderMu.Unlock()
	if deps == nil {
		return &ShamirCustodyNotConfiguredError{Name: dbName}
	}
	strategy, err := seal.NewShamirStrategy(cfg.SharesRequired, cfg.SharesTotal)
	if err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}
	barrierCfg := m.barrierConfig(dbName, tenantID)
	barrierCfg.Shamir = &seal.ShamirConfig{Threshold: cfg.SharesRequired, TotalShares: cfg.SharesTotal}
	key := barrierKey(dbName, tenantID)
	tb, err := m.registry.RegisterTenant(ctx, key, storage, barrierCfg, strategy)
	if err != nil {
		return &BarrierCreateError{Name: dbName, Err: err}
	}
	svc, err := ceremony.NewDatabaseCeremonyService(tenantid.Normalize(tenantID), dbName, tb.Barrier(), *deps, m.logger)
	if err != nil {
		_ = m.registry.RemoveTenant(key)
		return &BarrierCreateError{Name: dbName, Err: err}
	}
	tb.Barrier().SetCeremonyDistributor(svc)
	tb.Barrier().SetHolderVerifier(svc)
	m.holders.Set(key, svc)
	m.logger.Info("database shamir barrier registered",
		slog.String("tenant", tenantid.Normalize(tenantID)),
		slog.String("database", dbName),
		slog.Int("threshold", cfg.SharesRequired),
		slog.Int("total", cfg.SharesTotal))
	return nil
}

// Holders returns the holder authority and the barrier of the Shamir barrier
// of tenantID's dbName, for the share-holder operations the database service
// serves.
func (m *DatabaseBarrierManager) Holders(dbName, tenantID string) (*ceremony.ScopedCeremonyService, *seal.StorageBarrier, error) {
	key := barrierKey(dbName, tenantID)
	svc, ok := m.holders.Get(key)
	if !ok {
		return nil, nil, &NotShamirBarrierError{Name: dbName}
	}
	tb, err := m.registry.GetTenant(key)
	if err != nil {
		return nil, nil, &NotShamirBarrierError{Name: dbName}
	}
	return svc, tb.Barrier(), nil
}

// SubmitHolderShare counts one authenticated holder's share toward the
// Shamir barrier round of tenantID's dbName; a completed seal round runs seal
// (seal.BarrierRegistry.SubmitTenantHolderShare). An unseal submission is
// ordered against the node seal, and an unseal it completes clears the
// database's pending node unseal.
func (m *DatabaseBarrierManager) SubmitHolderShare(ctx context.Context, dbName, tenantID string, sub *seal.HolderSubmission, sealFn func() error) (*seal.QuorumOutcome, error) {
	key := barrierKey(dbName, tenantID)
	if _, ok := m.holders.Get(key); !ok {
		if sub != nil {
			secure.Wipe(sub.Share)
		}
		return nil, &NotShamirBarrierError{Name: dbName}
	}
	return m.registry.SubmitTenantHolderShare(ctx, key, sub, sealFn)
}

// InitializeShamir initializes the Shamir barrier of tenantID's dbName: it
// generates the root key and splits it to the database's enrolled holders
// under policy, sealing each share to its holder. The number of enrolled
// holders must equal the barrier's share count.
func (m *DatabaseBarrierManager) InitializeShamir(ctx context.Context, dbName, tenantID string, policy seal.FactorPolicy) (*seal.ShamirInitParams, error) {
	holders, barrier, err := m.Holders(dbName, tenantID)
	if err != nil {
		return nil, err
	}
	// Through the holder authority, which marks the initialization in flight
	// so no holder is unenrolled between the barrier's two reads of the set.
	params, err := holders.InitializeShamir(ctx, barrier, policy)
	if err != nil {
		return nil, err
	}
	m.logger.Info("database shamir barrier initialized",
		slog.String("tenant", tenantid.Normalize(tenantID)),
		slog.String("database", dbName),
		slog.Int("threshold", params.Threshold),
		slog.Int("total", params.TotalShares))
	return params, nil
}
