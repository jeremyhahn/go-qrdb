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
	"sync/atomic"
	"time"

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
	// ClaimTenantShamirInit claims a Shamir statement, which must sign
	// exactly the split this node prepared.
	ClaimTenantShamirInit(ctx context.Context, tenantID string, prepared *seal.ShamirInitParams, statement []byte) error
	TenantInit(ctx context.Context, tenantID string) (terms BarrierInitTerms, found bool, err error)
	// PrepareTenantInit returns tenantID's unsigned epoch-1 statement naming
	// signerSPKI as its signing key, encoded, and the message that key signs:
	// the first half of TenantBarrierInitBegin. prepared is a Shamir init's
	// split, nil for every other strategy.
	PrepareTenantInit(tenantID string, strategyID seal.StrategyID, keyRef string, prepared *seal.ShamirInitParams, signerSPKI []byte, issuedAt time.Time) (statement, message []byte, err error)
	// AttachTenantInit returns the encoded statement PrepareTenantInit
	// returned, carrying signature, once it verifies under the key the
	// statement names.
	AttachTenantInit(statement, signature []byte) ([]byte, error)
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
	// platformSigner signs the platform's init statement for a tenant that
	// chose platform custody; see SetPlatformInitSigner.
	platformSigner atomic.Pointer[platformSignerBox]
	// initDelivery brings this node's barrier up to its latest claimed
	// statement on demand; see SetBarrierInitDelivery.
	initDelivery atomic.Pointer[initDeliveryBox]
	// initLocks holds one *sync.Mutex per tenant, serializing this node's
	// inits of that tenant's barrier: the owner's call and the watcher's.
	initLocks sync.Map
	// signedInits holds the statements TenantBarrierInitBegin issued and
	// TenantBarrierInitComplete has not yet redeemed.
	signedInits signedInitBook
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

// PlatformInitSigner signs, with this node's own key and certificate, the
// platform's epoch-1 init statement for a tenant that chose platform custody
// (barrierinit.SignPlatformInit). No owner signs for a barrier the platform
// keeps; every node serving it verifies the statement against the cluster CA
// and initializes its own key from it.
type PlatformInitSigner interface {
	SignPlatformInit(tenantID string) ([]byte, error)
}

// PlatformInitWiring is how a composition wires the platform signer, which
// holds this node's certificate and so exists only after the tenant service is
// built.
type PlatformInitWiring interface {
	SetPlatformInitSigner(PlatformInitSigner)
}

// Compile-time check.
var _ PlatformInitWiring = (*TenantService)(nil)

// SetPlatformInitSigner makes the create of an isolated tenant that chose
// platform custody claim the platform's statement signed by signer, so every
// node of its groups initializes its own key for the barrier. A node composed
// without one has no cluster identity to sign with and no peer that could
// verify it: a raft tenant's create is refused (InitStatementRequiredError),
// and a barrier or volume tenant's claims nothing (claimPlatformInit).
func (s *TenantService) SetPlatformInitSigner(signer PlatformInitSigner) {
	s.platformSigner.Store(&platformSignerBox{signer: signer})
}

// platformSignerBox lets the signer be swapped atomically: it is wired after
// the service is built, while creates may be reading it.
type platformSignerBox struct{ signer PlatformInitSigner }

// platformInitSigner returns the wired platform signer, or nil.
func (c *barrierInitClaims) platformInitSigner() PlatformInitSigner {
	if box := c.platformSigner.Load(); box != nil {
		return box.signer
	}
	return nil
}

// BarrierInitDelivery delivers a tenant barrier's latest claimed init
// statement to this node now, as the node's init watcher would on its next
// poll (barrierinit.Watcher.Deliver). A tenant with no statement, or one this
// node does not serve, delivers nothing.
type BarrierInitDelivery interface {
	DeliverTenantInit(ctx context.Context, tenantID string) error
}

// BarrierInitDeliveryWiring is how a composition wires the delivery, which
// is the init watcher and so exists only after the tenant service is built.
type BarrierInitDeliveryWiring interface {
	SetBarrierInitDelivery(BarrierInitDelivery)
}

// Compile-time check.
var _ BarrierInitDeliveryWiring = (*TenantService)(nil)

// SetBarrierInitDelivery makes a holder quorum request bring this node's
// barrier up to its latest claimed statement before answering it, so every
// node serving the tenant gives the answer its creator gives rather than the
// one its last watcher poll left. A node composed without one answers from
// its registry as it stands.
func (s *TenantService) SetBarrierInitDelivery(d BarrierInitDelivery) {
	s.initDelivery.Store(&initDeliveryBox{delivery: d})
}

// initDeliveryBox lets the delivery be swapped atomically: it is wired after
// the service is built, while requests may be reading it.
type initDeliveryBox struct{ delivery BarrierInitDelivery }

// deliverInit brings tenantID's barrier on this node up to its latest claimed
// statement; it is a no-op on a node with no delivery wired. The caller must
// not hold tenantID's init lock: the delivery arms the barrier under it.
func (c *barrierInitClaims) deliverInit(ctx context.Context, tenantID string) error {
	box := c.initDelivery.Load()
	if box == nil || box.delivery == nil {
		return nil
	}
	if err := box.delivery.DeliverTenantInit(ctx, tenantID); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "init delivery", Err: err}
	}
	return nil
}

// homeAcrossNodes reports whether info's tenant has a home group, which
// placement spreads across every node of the cluster
// (defaultStoreResolver.EnsureHome): each isolated type does, and each node
// of that group needs its own key for the tenant's barrier.
func homeAcrossNodes(info *TenantInfo) bool {
	_, routed := barrierRoutedIsolation[info.EffectiveIsolationType()]
	return routed
}

// claimPlatformInit claims the platform's init statement for info, an
// isolated tenant created with platform custody whose record has just
// committed: that record is where every node, this one included, reads the
// custody choice its anchors trust the platform's signature by. Any other
// tenant, and a composition without a book, claim nothing. A node without a
// platform signer (SetPlatformInitSigner) claims an empty statement, which
// claimInit refuses for a raft tenant, whose groups exist only across a
// cluster's nodes; a standalone node composes no signer and serves a barrier
// or volume tenant's barrier on itself alone.
func (s *TenantService) claimPlatformInit(ctx context.Context, info *TenantInfo) error {
	if s.initBook == nil || !homeAcrossNodes(info) || !info.BarrierAutoUnseal {
		return nil
	}
	var statement []byte
	if signer := s.platformInitSigner(); signer != nil {
		var err error
		if statement, err = signer.SignPlatformInit(info.ID); err != nil {
			return &BarrierError{TenantID: info.ID, Op: "init statement", Err: err}
		}
	}
	return s.claimInit(ctx, info, statement, seal.StrategySystemBarrier, "")
}

// platformSelfInit initializes this node's own key for the barrier of info, a
// tenant that chose platform custody, exactly as the node that created it did:
// registered on the tenant's volume layout with its posture recorded, so a
// restart unseals it unattended (seal.BarrierRegistry.RestoreVolumeTenants),
// and sealed under this node's own system barrier. A tenant whose owner keeps
// custody is refused (ErrNotPlatformCustody) whatever statement names it.
func (s *TenantService) platformSelfInit(ctx context.Context, info *TenantInfo) error {
	if !info.BarrierAutoUnseal {
		return &BarrierError{TenantID: info.ID, Op: "initialize", Err: ErrNotPlatformCustody}
	}
	defer s.lockInit(info.ID)()
	if _, err := s.registry.GetTenant(info.ID); err != nil {
		if _, _, regErr := s.registry.RegisterVolumeTenant(ctx, info.ID, true); regErr != nil {
			return &BarrierError{TenantID: info.ID, Op: "register", Err: regErr}
		}
	}
	err := s.registry.InitializeTenant(ctx, info.ID, seal.Credentials{})
	switch {
	case errors.Is(err, seal.ErrAlreadyInitialized):
		return nil
	case err != nil:
		return &BarrierError{TenantID: info.ID, Op: "initialize", Err: err}
	}
	s.logger.Info("tenant barrier initialized from the platform's init statement",
		slog.String("tenant_id", info.ID))
	return nil
}

// TenantBarrierSelfInit initializes this node's own key for tenantID's
// barrier, through this node's BarrierKeyProvider, from the strategy and key
// reference of the owner's init statement: one the watcher has already
// verified against the barrier's anchors, and which carries no key. Only a
// strategy whose key needs no caller credential is initialized here: a key
// the provider resolves, or -- for a tenant that chose platform custody, whose
// statement the platform signed -- this node's system barrier
// (platformSelfInit). A passphrase key needs the owner's passphrase on every node, which the owner's
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
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	if strategyID == seal.StrategySystemBarrier {
		return s.platformSelfInit(ctx, info)
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

// TenantBarrierArmShamir arms this node's barrier for a Shamir tenant from the
// owner's init statement (pkg/barrierinit.Watcher): roster is the share
// holders' roster the first node split the unseal key to, and check that
// key's seal.UnsealKeyCheck. The node holds no key until its holders' first
// unseal quorum reaches it; that quorum's key must match check, and the node
// then generates and seals its own root key (seal.StorageBarrier.ArmShamir).
// The unseal key is never relayed between nodes.
//
// A statement that carries neither a roster nor a check value arms nothing.
// Arming a node that already holds its key is a no-op.
func (s *TenantService) TenantBarrierArmShamir(ctx context.Context, tenantID string, roster, check []byte) error {
	if len(roster) == 0 && len(check) == 0 {
		return nil
	}
	if _, err := s.GetTenant(ctx, tenantID); err != nil {
		return err
	}
	defer s.lockInit(tenantID)()
	if err := s.registry.ArmTenantShamir(ctx, tenantID, roster, check); err != nil {
		return &BarrierError{TenantID: tenantID, Op: "arm", Err: err}
	}
	return nil
}

// RegisterTenantShell registers tenantID's barrier on this node as an
// uninitialized, sealed shell, once placement assigns the node to one of the
// tenant's groups and the node has no barrier for it yet. No key exists: the
// shell is what lets status, init and unseal reach the node, and this node's
// own init -- self-init from the tenant's init statement, the owner's init, or
// a key import -- then initializes it. Only an isolated tenant has a home group
// placed across nodes (homeAcrossNodes), so any other tenant is left alone, as
// is one this node has no record of or already holds a barrier for.
func (s *TenantService) RegisterTenantShell(ctx context.Context, tenantID string) error {
	info, err := s.GetTenant(ctx, tenantID)
	switch {
	case errors.Is(err, ErrTenantNotFound):
		return nil
	case err != nil:
		return err
	}
	if !homeAcrossNodes(info) {
		return nil
	}
	defer s.lockInit(tenantID)()
	if _, err := s.registry.GetTenant(tenantID); err == nil {
		return nil
	}
	if _, _, err := s.registry.RegisterVolumeTenant(ctx, tenantID, info.BarrierAutoUnseal); err != nil && !errors.Is(err, seal.ErrTenantAlreadyExists) {
		return &BarrierError{TenantID: tenantID, Op: "register", Err: err}
	}
	s.logger.Info("tenant barrier shell registered for its placement",
		slog.String("tenant_id", tenantID))
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
// enable-encryption has since migrated (see migratedOffNone). A platform
// statement (seal.StrategySystemBarrier) unseals with the platform strategy
// the barrier was registered and initialized with (platformSelfInit), which
// no provider resolves, and only for a tenant in platform custody
// (ErrNotPlatformCustody otherwise). A barrier with
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
	case terms.StrategyID == seal.StrategySystemBarrier:
		if !info.BarrierAutoUnseal {
			return &BarrierError{TenantID: info.ID, Op: "unseal", Err: ErrNotPlatformCustody}
		}
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
