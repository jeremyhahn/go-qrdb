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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// initClaim is one ClaimTenantInit call.
type initClaim struct {
	tenantID  string
	strategy  seal.StrategyID
	keyRef    string
	statement string
}

// bookDouble records each claim it is handed and answers with err. Parsing,
// matching and verifying the statement are barrierinit.Claimer's, tested
// there; the service's part is what it hands over and what it does with the
// answer.
//
// Like the real book, TenantInit reads back the tenant's first claimed
// statement as epoch 1. recorded stands in for a record this service did not
// claim (another node's, or a later epoch), and readErr for a record that
// cannot be read or no longer verifies.
type bookDouble struct {
	mu       sync.Mutex
	claims   []initClaim
	err      error
	recorded map[string]BarrierInitTerms
	readErr  error
	reads    int
}

func (b *bookDouble) TenantInit(_ context.Context, tenantID string) (BarrierInitTerms, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads++
	if b.readErr != nil {
		return BarrierInitTerms{}, false, b.readErr
	}
	if terms, ok := b.recorded[tenantID]; ok {
		return terms, true, nil
	}
	for _, c := range b.claims {
		if c.tenantID == tenantID {
			return BarrierInitTerms{StrategyID: c.strategy, KeyRef: c.keyRef, Epoch: 1}, true, nil
		}
	}
	return BarrierInitTerms{}, false, nil
}

func (b *bookDouble) ClaimTenantInit(_ context.Context, tenantID string, strategy seal.StrategyID, keyRef string, statement []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.claims = append(b.claims, initClaim{tenantID, strategy, keyRef, string(statement)})
	return nil
}

func (b *bookDouble) claimed() []initClaim {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]initClaim(nil), b.claims...)
}

var _ BarrierInitBook = (*bookDouble)(nil)

// newClaimService is a service whose barrier inits claim through book, with
// tenantID created and its pre-registered shell removed.
func newClaimService(t *testing.T, book *bookDouble, tenantID string) (*TenantService, *hookCapture) {
	t.Helper()
	svc, reg, hook := newHookService(t, WithBarrierInitBook(book))
	if _, err := svc.CreateTenant(context.Background(), tenantID, tenantID, ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}
	hook.reset()
	return svc, hook
}

func initialized(t *testing.T, svc *TenantService, tenantID string) bool {
	t.Helper()
	st, err := svc.TenantBarrierStatus(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	return st.Initialized
}

func TestTenantBarrierInit_ClaimsTheStatementBeforeTheKey(t *testing.T) {
	book := &bookDouble{}
	svc, hook := newClaimService(t, book, "acme")

	if _, err := svc.TenantBarrierInitWithStrategy(context.Background(), "acme", nil, 0, 0, seal.StrategyNone, nil,
		WithInitStatement([]byte("signed")), WithBarrierKeyRef("kms://owner-key")); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	want := initClaim{tenantID: "acme", strategy: seal.StrategyNone, keyRef: "kms://owner-key", statement: "signed"}
	if got := book.claimed(); len(got) != 1 || got[0] != want {
		t.Fatalf("claims = %+v, want [%+v]", got, want)
	}
	if hook.count() != 1 || !initialized(t, svc, "acme") {
		t.Fatal("the barrier was not initialized after its statement was claimed")
	}
}

// A refused claim -- one another init won, one that does not verify, one
// naming another init -- leaves this node with no key.
func TestTenantBarrierInit_ARefusedClaimGeneratesNoKey(t *testing.T) {
	refused := errors.New("claim refused")
	svc, hook := newClaimService(t, &bookDouble{err: refused}, "acme")
	_, err := svc.TenantBarrierInitWithStrategy(context.Background(), "acme", nil, 0, 0, seal.StrategyNone, nil,
		WithInitStatement([]byte("signed")))
	if !errors.Is(err, refused) {
		t.Fatalf("init = %v, want the refused claim", err)
	}
	if hook.count() != 0 || initialized(t, svc, "acme") {
		t.Fatal("a refused claim still initialized a key")
	}
}

// A claim is made only once the strategy resolves and its credential is
// present: a request this node would refuse records nothing.
func TestTenantBarrierInit_NoClaimForARequestRefusedEarlier(t *testing.T) {
	book := &bookDouble{}
	svc, _ := newClaimService(t, book, "acme")
	_, err := svc.TenantBarrierInitWithStrategy(context.Background(), "acme", nil, 0, 0, seal.StrategyPassphrase, nil,
		WithInitStatement([]byte("signed")))
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("init = %v, want ErrEmptyPassphrase", err)
	}
	if len(book.claimed()) != 0 {
		t.Fatal("a refused request claimed its statement")
	}
}

// newShamirClaimService is a service whose barrier inits claim through book
// and whose holder authority can run a Shamir init, with tenantID created.
func newShamirClaimService(t *testing.T, book *bookDouble, tenantID string) (*TenantService, context.Context) {
	t.Helper()
	svc, err := NewTenantService(seal.NewMemoryBackend(), newTestBarrierRegistry(t), testLogger(),
		WithTenantHolderDeps(ceremony.HolderDeps{CA: newTenantHolderCA(t), TrustDomain: holderTrustDomain}), WithBarrierInitBook(book))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := tenantAdminCtx(tenantID)
	if _, err := svc.CreateTenant(ctx, tenantID, tenantID, ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	return svc, ctx
}

func shamirClaimRoster(t *testing.T) *TenantBarrierShamirRoster {
	t.Helper()
	return &TenantBarrierShamirRoster{Officers: []ceremony.OfficerConfig{
		shamirRosterOfficer(newTenantHolder(t, "alice")), shamirRosterOfficer(newTenantHolder(t, "bob")),
	}}
}

// A Shamir init is admitted like every other strategy: its statement is
// claimed before the holders' shares exist.
func TestTenantBarrierInit_ShamirClaimsTheStatementBeforeTheKey(t *testing.T) {
	book := &bookDouble{}
	svc, ctx := newShamirClaimService(t, book, "acme")
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, shamirClaimRoster(t),
		WithInitStatement([]byte("signed"))); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(shamir): %v", err)
	}
	want := initClaim{tenantID: "acme", strategy: seal.StrategyShamir, statement: "signed"}
	if got := book.claimed(); len(got) != 1 || got[0] != want {
		t.Fatalf("claims = %+v, want [%+v]", got, want)
	}
	if !initialized(t, svc, "acme") {
		t.Fatal("the barrier was not initialized after its statement was claimed")
	}
}

// A refused claim -- a statement naming another strategy, one another init
// won -- leaves a Shamir barrier uninitialized and its roster unregistered.
func TestTenantBarrierInit_ARefusedShamirClaimGeneratesNoKey(t *testing.T) {
	refused := errors.New("claim refused")
	svc, ctx := newShamirClaimService(t, &bookDouble{err: refused}, "acme")
	_, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, shamirClaimRoster(t),
		WithInitStatement([]byte("signed")))
	if !errors.Is(err, refused) {
		t.Fatalf("init(shamir) = %v, want the refused claim", err)
	}
	if initialized(t, svc, "acme") {
		t.Fatal("a refused claim still initialized a Shamir barrier")
	}
	officers, err := svc.TenantBarrierOfficerList(ctx, "acme")
	if err != nil {
		t.Fatalf("TenantBarrierOfficerList: %v", err)
	}
	if len(officers) != 0 {
		t.Fatalf("a refused claim registered %d officers, want 0", len(officers))
	}
}

// A Shamir request this node would refuse records nothing.
func TestTenantBarrierInit_NoShamirClaimForARequestRefusedEarlier(t *testing.T) {
	book := &bookDouble{}
	svc, ctx := newShamirClaimService(t, book, "acme")
	roster := shamirClaimRoster(t)
	roster.Policy = "no-such-policy"
	_, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, roster,
		WithInitStatement([]byte("signed")))
	var invalid *InvalidFactorPolicyError
	if !errors.As(err, &invalid) {
		t.Fatalf("init(shamir) = %v, want *InvalidFactorPolicyError", err)
	}
	if len(book.claimed()) != 0 {
		t.Fatal("a refused Shamir request claimed its statement")
	}
}

// A barrier one node serves needs no statement; one every node of a raft
// group serves cannot be initialized without one.
func TestClaimInit_StatementRequiredOnlyForRaftTenants(t *testing.T) {
	book := &bookDouble{}
	svc, _, _ := newHookService(t, WithBarrierInitBook(book))
	ctx := context.Background()

	if err := svc.claimInit(ctx, &TenantInfo{ID: "acme", IsolationType: IsolationBarrier}, nil, seal.StrategyNone, ""); err != nil {
		t.Fatalf("claimInit(barrier tenant, no statement) = %v, want nil", err)
	}
	var req *InitStatementRequiredError
	err := svc.claimInit(ctx, &TenantInfo{ID: "acme", IsolationType: IsolationRaft}, nil, seal.StrategyNone, "")
	if !errors.As(err, &req) || req.TenantID != "acme" {
		t.Fatalf("claimInit(raft tenant, no statement) = %v, want *InitStatementRequiredError", err)
	}
	if req.Error() == "" {
		t.Fatal("InitStatementRequiredError has no message")
	}
	if len(book.claimed()) != 0 {
		t.Fatal("nothing should have been claimed")
	}
}

// Without a book nothing is claimed and no statement is required: the
// composition serves each barrier on one node.
func TestClaimInit_NoBookClaimsNothing(t *testing.T) {
	svc, _, _ := newHookService(t)
	if err := svc.claimInit(context.Background(), &TenantInfo{ID: "acme", IsolationType: IsolationRaft}, []byte("signed"), seal.StrategyNone, ""); err != nil {
		t.Fatalf("claimInit without a book = %v, want nil", err)
	}
}

func TestTenantBarrierSelfInit_InitializesAKeylessStrategy(t *testing.T) {
	svc, hook := newClaimService(t, &bookDouble{}, "acme")
	ctx := context.Background()
	if err := svc.TenantBarrierSelfInit(ctx, "acme", seal.StrategyNone, ""); err != nil {
		t.Fatalf("TenantBarrierSelfInit: %v", err)
	}
	// The barrier-ready hook is the owner's init's, run as the owner on the
	// node that took it; this node has no caller to run it as. (Checked before
	// the status read below, which runs the hook as its own caller.)
	if hook.count() != 0 {
		t.Fatalf("self-init ran the barrier-ready hook %d times, want 0", hook.count())
	}
	if !initialized(t, svc, "acme") {
		t.Fatal("the barrier is not initialized")
	}
	// A redelivery, or a statement for a barrier this node already holds a key
	// for, is done.
	if err := svc.TenantBarrierSelfInit(ctx, "acme", seal.StrategyNone, ""); err != nil {
		t.Fatalf("TenantBarrierSelfInit again: %v", err)
	}
	if !initialized(t, svc, "acme") {
		t.Fatal("the barrier is not initialized")
	}
}

// The owner's init on this node and the watcher's both end with one key, in
// either order.
func TestTenantBarrierSelfInit_AfterTheOwnersInitIsDone(t *testing.T) {
	svc, hook := newClaimService(t, &bookDouble{}, "acme")
	ctx := context.Background()
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 0, 0, seal.StrategyNone, nil,
		WithInitStatement([]byte("signed"))); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if hook.count() != 1 {
		t.Fatalf("the owner's init ran the barrier-ready hook %d times, want 1", hook.count())
	}
	if err := svc.TenantBarrierSelfInit(ctx, "acme", seal.StrategyNone, ""); err != nil {
		t.Fatalf("TenantBarrierSelfInit: %v", err)
	}
	if !initialized(t, svc, "acme") {
		t.Fatal("the barrier is not initialized")
	}
}

// A passphrase key needs the owner's passphrase, and a Shamir barrier its
// holders: the watcher leaves both to them.
func TestTenantBarrierSelfInit_LeavesCredentialStrategiesToTheOwner(t *testing.T) {
	for _, strategy := range []seal.StrategyID{seal.StrategyPassphrase, seal.StrategyShamir} {
		t.Run(string(strategy), func(t *testing.T) {
			svc, hook := newClaimService(t, &bookDouble{}, "acme")
			if err := svc.TenantBarrierSelfInit(context.Background(), "acme", strategy, ""); err != nil {
				t.Fatalf("TenantBarrierSelfInit: %v", err)
			}
			if hook.count() != 0 || initialized(t, svc, "acme") {
				t.Fatalf("a %s barrier was initialized without its credential", strategy)
			}
		})
	}
}

// The watcher's init reaches the key through the provider with the
// statement's key reference and this node's name, like the owner's does.
func TestTenantBarrierSelfInit_ResolvesThroughTheProvider(t *testing.T) {
	device, err := sealtest.NewDeviceStrategy("test-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	provider := &recordingKeyProvider{result: &seal.BarrierKeyStrategy{
		Strategy:   device,
		Credential: seal.CredentialNone,
		Custody:    seal.CustodyOwner,
	}}
	svc := newKeyProviderService(t, "acme", provider, WithNodeID("node-a"), WithBarrierInitBook(&bookDouble{}))
	if err := svc.TenantBarrierSelfInit(context.Background(), "acme", "test-kms", "kms://owner-key"); err != nil {
		t.Fatalf("TenantBarrierSelfInit: %v", err)
	}
	if len(provider.requests) != 1 || provider.requests[0].KeyRef != "kms://owner-key" || provider.requests[0].NodeID != "node-a" {
		t.Fatalf("provider requests = %+v, want the statement's key ref for node-a", provider.requests)
	}
	if !initialized(t, svc, "acme") {
		t.Fatal("the barrier is not initialized")
	}
}

func TestTenantBarrierSelfInit_Refusals(t *testing.T) {
	svc, _ := newClaimService(t, &bookDouble{}, "acme")
	ctx := context.Background()
	if err := svc.TenantBarrierSelfInit(ctx, "globex", seal.StrategyNone, ""); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("SelfInit(unknown tenant) = %v, want ErrTenantNotFound", err)
	}
	var invalid *InvalidStrategyError
	if err := svc.TenantBarrierSelfInit(ctx, "acme", "no-such-strategy", ""); !errors.As(err, &invalid) {
		t.Fatalf("SelfInit(unknown strategy) = %v, want *InvalidStrategyError", err)
	}
}
