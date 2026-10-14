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

package tenant

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// BarrierInitBook claims and reads a tenant barrier's signed init statement
// (barrierinit.Claimer).
//
// ClaimTenantInit parses statement, refuses one naming any other barrier,
// strategy or key, verifies it against the barrier's anchors and records it
// with a guarded create-if-absent, so a concurrent init with other parameters
// loses before any key exists.
//
// TenantInit returns what the barrier's current recorded statement says it
// unseals with, once that statement verifies against the barrier's anchors;
// found is false when the barrier has no recorded statement. A statement that
// no longer verifies, or a record that cannot be read, is an error.
type BarrierInitBook interface {
	ClaimTenantInit(ctx context.Context, tenantID string, strategyID seal.StrategyID, keyRef string, statement []byte) error
	TenantInit(ctx context.Context, tenantID string) (terms BarrierInitTerms, found bool, err error)
}

// BarrierInitTerms is what a tenant barrier's verified init statement says
// the barrier is sealed with: the strategy, the key reference the node's
// BarrierKeyProvider resolves it from, and the statement's epoch.
type BarrierInitTerms struct {
	StrategyID seal.StrategyID
	KeyRef     string
	Epoch      uint64
}

// InitStatementMissingError refuses to unseal a raft-isolated tenant's
// barrier that has no recorded init statement. Such a barrier cannot have
// been initialized through this service, which claims the statement before
// any key exists, so nothing signed says which strategy and key it unseals
// with, and the node's default strategies are never guessed at instead.
type InitStatementMissingError struct {
	TenantID string
}

// Error returns a human-readable description.
func (e *InitStatementMissingError) Error() string {
	return "tenant: barrier of " + strconv.Quote(e.TenantID) +
		" has no recorded init statement to unseal from"
}

// barrierInitClaims is the book a tenant barrier's init statement is claimed
// through; see WithBarrierInitBook.
type barrierInitClaims struct {
	initBook BarrierInitBook
	// initLocks holds one *sync.Mutex per tenant, serializing this node's
	// inits of that tenant's barrier: the owner's call and the watcher's.
	initLocks sync.Map
}

// lockInit takes tenantID's init lock and returns its release.
func (c *barrierInitClaims) lockInit(tenantID string) func() {
	v, _ := c.initLocks.LoadOrStore(tenantID, new(sync.Mutex))
	mu := v.(*sync.Mutex) //nolint:errcheck // the map only ever holds *sync.Mutex
	mu.Lock()
	return mu.Unlock
}

// WithBarrierInitBook makes tenant barrier initialization claim the owner's
// signed init statement in book before this node generates its key: every
// other node serving the barrier initializes its own key from that record
// (pkg/barrierinit.Watcher, TenantBarrierSelfInit). A raft-isolated tenant,
// whose barrier every node of its groups serves, is refused without a
// statement (*InitStatementRequiredError). Without the option no statement is
// claimed, which is right only for a composition that serves each barrier on
// one node.
func WithBarrierInitBook(book BarrierInitBook) TenantServiceOption {
	return func(s *TenantService) { s.initBook = book }
}

// WithInitStatement carries the barrier owner's signed init statement, as
// barrierinit.InitStatement.Marshal encodes it, into one init. Empty is no
// statement.
func WithInitStatement(raw []byte) TenantBarrierInitOption {
	return func(o *tenantBarrierInitOptions) { o.statement = raw }
}

// InitStatementRequiredError reports a tenant barrier init made without the
// owner's signed init statement on a tenant whose barrier more than one node
// serves: the other nodes could never initialize their own keys.
type InitStatementRequiredError struct {
	TenantID string
}

// Error returns a human-readable description.
func (e *InitStatementRequiredError) Error() string {
	return "tenant: barrier init for " + strconv.Quote(e.TenantID) +
		" needs the barrier owner's signed init statement"
}

// claimInit claims statement as info's epoch-1 init statement for exactly the
// strategy and key this init resolved. A composition without a book claims
// nothing; with one, a raft-isolated tenant gets no further than here without
// a statement.
func (s *TenantService) claimInit(ctx context.Context, info *TenantInfo, statement []byte, strategyID seal.StrategyID, keyRef string) error {
	if s.initBook == nil {
		return nil
	}
	if len(statement) == 0 {
		if info.IsolationType == IsolationRaft {
			return &InitStatementRequiredError{TenantID: info.ID}
		}
		return nil
	}
	if err := s.initBook.ClaimTenantInit(ctx, info.ID, strategyID, keyRef, statement); err != nil {
		return err
	}
	s.logger.Info("tenant barrier init statement claimed",
		slog.String("tenant_id", info.ID),
		slog.String("strategy", string(strategyID)))
	return nil
}

// TenantBarrierSelfInit initializes this node's own key for tenantID's
// barrier, through this node's BarrierKeyProvider, from the strategy and key
// reference of the owner's init statement: one the watcher has already
// verified against the barrier's anchors, and which carries no key. Only a
// strategy whose key needs no caller credential is initialized here. A
// passphrase key needs the owner's passphrase on every node, which the owner's
// client supplies to each one, and a Shamir barrier is initialized by its
// holders' ceremony; both are left alone and reported done. A barrier this
// node has already initialized is done too, so a redelivered or later-epoch
// statement changes nothing.
//
// The barrier-ready hook is not run: the owner's init already ran it, as the
// owner, on the node that took the init, and what it completes is replicated.
// This node has no caller to run it as; a status read or unseal here runs it
// as that caller should it still have work to do.
func (s *TenantService) TenantBarrierSelfInit(ctx context.Context, tenantID string, strategyID seal.StrategyID, keyRef string) error {
	if strategyID == seal.StrategyShamir {
		return nil
	}
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
	}
	resolved, err := s.resolveStrategy(ctx, tenantID, strategyID, keyRef, 1)
	if err != nil {
		return err
	}
	if resolved.Credential != seal.CredentialNone {
		return nil
	}

	defer s.lockInit(tenantID)()
	if _, err := s.registry.GetTenant(tenantID); err != nil {
		if _, regErr := s.registry.RegisterTenantPooledWithStrategy(ctx, tenantID, resolved.Strategy); regErr != nil {
			return &BarrierError{TenantID: tenantID, Op: "register", Err: regErr}
		}
	}
	err = s.registry.InitializeTenantWithStrategy(ctx, tenantID, resolved.Strategy, seal.Credentials{})
	switch {
	case errors.Is(err, seal.ErrAlreadyInitialized):
		return nil
	case err != nil:
		return &BarrierError{TenantID: tenantID, Op: "initialize", Err: err}
	}
	s.logger.Info("tenant barrier initialized from its init statement",
		slog.String("tenant_id", tenantID),
		slog.String("strategy", string(strategyID)))
	return nil
}

// unsealBarrier unseals info's registered barrier with creds.
//
// With a book, the strategy is the one the barrier's verified init statement
// names, resolved through this node's BarrierKeyProvider with the
// statement's key reference and epoch -- exactly the request that sealed this
// node's key -- and the barrier unseals with that strategy alone
// (seal.BarrierRegistry.UnsealTenantWithStrategy). The shell a node
// registers on restart carries the node's default strategy set, which may
// lack the provider's strategy or hold a differently configured one under
// the same ID; neither is ever tried for a barrier that has a statement.
//
// A Shamir statement unseals as before: its key is reconstructed from holder
// shares, not resolved by the provider. So does a none statement on a barrier
// enable-encryption has since migrated (see migratedOffNone). A barrier with
// no statement unseals
// with its registered strategies only where an init without one is allowed
// (see claimInit); a raft-isolated tenant's is refused with
// *InitStatementMissingError.
func (s *TenantService) unsealBarrier(ctx context.Context, info *TenantInfo, creds seal.Credentials) error {
	if s.initBook == nil {
		return s.unsealRegistered(ctx, info.ID, creds)
	}
	terms, found, err := s.initBook.TenantInit(ctx, info.ID)
	switch {
	case err != nil:
		return &BarrierError{TenantID: info.ID, Op: "init statement", Err: err}
	case !found && info.IsolationType == IsolationRaft:
		return &InitStatementMissingError{TenantID: info.ID}
	case !found, terms.StrategyID == seal.StrategyShamir, s.migratedOffNone(info.ID, terms.StrategyID):
		return s.unsealRegistered(ctx, info.ID, creds)
	}
	resolved, err := s.resolveStrategy(ctx, info.ID, terms.StrategyID, terms.KeyRef, terms.Epoch)
	if err != nil {
		return err
	}
	if err := s.registry.UnsealTenantWithStrategy(ctx, info.ID, resolved.Strategy, creds); err != nil {
		return &BarrierError{TenantID: info.ID, Op: "unseal", Err: err}
	}
	return nil
}

// migratedOffNone reports whether tenantID's statement names the none
// strategy while its root key is now sealed with another one. enable-encryption
// is the one sanctioned strategy change and records no later-epoch statement,
// so the epoch-1 statement no longer names the root key's strategy; that
// barrier unseals with its registered strategies, as it did before the
// statement was consulted.
func (s *TenantService) migratedOffNone(tenantID string, stated seal.StrategyID) bool {
	if stated != seal.StrategyNone {
		return false
	}
	st, err := s.registry.TenantStatus(tenantID)
	if err != nil {
		return false
	}
	return st.Strategy != "" && st.Strategy != seal.StrategyNone
}

// unsealRegistered unseals tenantID's barrier with the strategy registered
// under its persisted root key's strategy ID.
func (s *TenantService) unsealRegistered(ctx context.Context, tenantID string, creds seal.Credentials) error {
	if err := s.registry.UnsealTenant(ctx, tenantID, creds); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "unseal", Err: err}
	}
	return nil
}
