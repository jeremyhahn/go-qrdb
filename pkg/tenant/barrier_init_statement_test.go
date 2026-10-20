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
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// errStatementMismatch is the double's refusal of a Shamir statement that
// does not sign the prepared split, as barrierinit's ReasonMismatch.
var errStatementMismatch = errors.New("statement does not sign the prepared split")

// shamirStatement is the double's encoding of a Shamir statement signing p:
// the real one is barrierinit's, which this package cannot import.
func shamirStatement(p *seal.ShamirInitParams) []byte {
	sum := sha256.Sum256(p.Roster)
	return []byte(fmt.Sprintf("shamir:%d:%x:%x", p.Threshold, sum, p.UnsealKeyCheck))
}

// initShamir runs both requests of a Shamir tenant barrier init, as the
// owner's SDK does: the first prepares the split, the second carries the
// statement signing it. It returns the first request's result, which carries
// the roster's holder certificates.
func initShamir(ctx context.Context, svc *TenantService, tenantID string, threshold, total int, roster *TenantBarrierShamirRoster) (*TenantBarrierInitResult, error) {
	first, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, nil, threshold, total, seal.StrategyShamir, roster)
	if err != nil {
		return nil, err
	}
	statement := shamirStatement(&seal.ShamirInitParams{Threshold: first.Threshold, Roster: first.Roster, UnsealKeyCheck: first.UnsealKeyCheck})
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, nil, threshold, total, seal.StrategyShamir, nil,
		WithInitStatement(statement)); err != nil {
		return nil, err
	}
	return first, nil
}

func (b *bookDouble) ClaimTenantShamirInit(_ context.Context, tenantID string, prepared *seal.ShamirInitParams, statement []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	if !bytes.Equal(statement, shamirStatement(prepared)) {
		return errStatementMismatch
	}
	b.claims = append(b.claims, initClaim{tenantID, seal.StrategyShamir, "", string(statement)})
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
	first, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, shamirClaimRoster(t))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if initialized(t, svc, "acme") || len(book.claimed()) != 0 {
		t.Fatal("the first request keyed the barrier or claimed a statement")
	}
	if len(first.Holders) != 2 || len(first.UnsealKeyCheck) != seal.UnsealKeyCheckSize || len(first.Roster) == 0 {
		t.Fatalf("first request = %+v, want both holders, the check value and the roster", first)
	}
	statement := shamirStatement(&seal.ShamirInitParams{Threshold: 2, Roster: first.Roster, UnsealKeyCheck: first.UnsealKeyCheck})
	second, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
		WithInitStatement(statement))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	want := initClaim{tenantID: "acme", strategy: seal.StrategyShamir, statement: string(statement)}
	if got := book.claimed(); len(got) != 1 || got[0] != want {
		t.Fatalf("claims = %+v, want [%+v]", got, want)
	}
	if !initialized(t, svc, "acme") {
		t.Fatal("the barrier was not initialized after its statement was claimed")
	}
	if !bytes.Equal(second.UnsealKeyCheck, first.UnsealKeyCheck) || !bytes.Equal(second.Roster, first.Roster) {
		t.Fatal("the second request returned another split than the first")
	}
}

// A refused claim -- a statement naming another strategy, one another init
// won -- leaves a Shamir barrier uninitialized and its roster unregistered.
func TestTenantBarrierInit_ARefusedShamirClaimGeneratesNoKey(t *testing.T) {
	book := &bookDouble{}
	svc, ctx := newShamirClaimService(t, book, "acme")
	first, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, shamirClaimRoster(t))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	prepared := seal.ShamirInitParams{Threshold: 2, Roster: first.Roster, UnsealKeyCheck: first.UnsealKeyCheck}
	otherCheck := prepared
	otherCheck.UnsealKeyCheck = bytes.Repeat([]byte{0xa5}, seal.UnsealKeyCheckSize)
	otherRoster := prepared
	otherRoster.Roster = []byte("another roster")
	otherThreshold := prepared
	otherThreshold.Threshold = 1
	for name, p := range map[string]seal.ShamirInitParams{
		"check": otherCheck, "roster": otherRoster, "threshold": otherThreshold,
	} {
		_, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
			WithInitStatement(shamirStatement(&p)))
		if !errors.Is(err, errStatementMismatch) {
			t.Fatalf("%s: second request = %v, want the mismatch refusal", name, err)
		}
		if initialized(t, svc, "acme") || len(book.claimed()) != 0 {
			t.Fatalf("%s: a refused statement keyed the barrier or was claimed", name)
		}
	}
	// The node kept its prepared split: the right statement still completes.
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
		WithInitStatement(shamirStatement(&prepared))); err != nil || !initialized(t, svc, "acme") {
		t.Fatalf("the prepared statement after refusals = %v", err)
	}
}

func TestTenantBarrierInit_ShamirFirstRequestReturnsTheKeysCheckValue(t *testing.T) {
	svc, ctx := newShamirClaimService(t, &bookDouble{}, "acme")
	first, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, shamirClaimRoster(t))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	tb, err := svc.registry.GetTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	prepared, ok := tb.Barrier().PreparedShamir()
	if !ok || !tb.Barrier().IsSealed() {
		t.Fatalf("after the first request prepared=%v sealed=%v", ok, tb.Barrier().IsSealed())
	}
	if !bytes.Equal(prepared.UnsealKeyCheck, first.UnsealKeyCheck) || !bytes.Equal(prepared.Roster, first.Roster) {
		t.Fatal("the first request returned another check value or roster than the barrier prepared")
	}
}

func TestTenantBarrierInit_ShamirSecondRequestWithoutAPreparedSplit(t *testing.T) {
	svc, ctx := newShamirClaimService(t, &bookDouble{}, "acme")
	_, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
		WithInitStatement([]byte("signed")))
	if !errors.Is(err, seal.ErrShamirNotPrepared) {
		t.Fatalf("a statement before any first request = %v, want ErrShamirNotPrepared", err)
	}
}

func TestTenantBarrierInit_ARepeatedShamirFirstRequestLeavesOneSplit(t *testing.T) {
	book := &bookDouble{}
	svc, ctx := newShamirClaimService(t, book, "acme")
	first, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, shamirClaimRoster(t))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	// The officers are enrolled now: the repeat is the pre-enrolled path.
	again, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil)
	if err != nil {
		t.Fatalf("repeated first request: %v", err)
	}
	if bytes.Equal(again.UnsealKeyCheck, first.UnsealKeyCheck) {
		t.Fatal("the repeated first request returned the replaced split")
	}
	_, err = svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
		WithInitStatement(shamirStatement(&seal.ShamirInitParams{Threshold: 2, Roster: first.Roster, UnsealKeyCheck: first.UnsealKeyCheck})))
	if !errors.Is(err, errStatementMismatch) || initialized(t, svc, "acme") {
		t.Fatalf("the replaced split's statement = %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 2, 2, seal.StrategyShamir, nil,
		WithInitStatement(shamirStatement(&seal.ShamirInitParams{Threshold: 2, Roster: again.Roster, UnsealKeyCheck: again.UnsealKeyCheck}))); err != nil {
		t.Fatalf("the live split's statement = %v", err)
	}
	if !initialized(t, svc, "acme") || len(book.claimed()) != 1 {
		t.Fatalf("after completion: initialized=%v claims=%d", initialized(t, svc, "acme"), len(book.claimed()))
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

// firstNodeShamir initializes a 2-of-3 Shamir barrier, as the node that took
// the owner's init does, and returns its custody and the roster and check
// value its statement carries.
func firstNodeShamir(t *testing.T) (*sealtest.Custody, *seal.ShamirInitParams) {
	t.Helper()
	strategy, err := seal.NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", Shamir: &seal.ShamirConfig{Threshold: 2, TotalShares: 3}}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	custody := sealtest.NewCustody("h1", "h2", "h3")
	custody.Install(b)
	params, err := b.InitializeShamir(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return custody, params
}

func TestTenantBarrierArmShamir_KeysTheNodeAtItsHoldersFirstQuorum(t *testing.T) {
	svc, hook := newClaimService(t, &bookDouble{}, "acme")
	ctx := context.Background()
	custody, params := firstNodeShamir(t)

	if err := svc.TenantBarrierArmShamir(ctx, "acme", params.Roster, params.UnsealKeyCheck); err != nil {
		t.Fatalf("TenantBarrierArmShamir: %v", err)
	}
	tb, err := svc.registry.GetTenant("acme")
	if err != nil || !tb.Barrier().ShamirArmed() || !tb.Barrier().IsSealed() {
		t.Fatalf("acme after arming: %v", err)
	}
	// The next poll re-delivers the statement.
	if err := svc.TenantBarrierArmShamir(ctx, "acme", params.Roster, params.UnsealKeyCheck); err != nil {
		t.Fatalf("re-arming: %v", err)
	}

	custody.Install(tb.Barrier())
	if o, err := custody.Quorum(ctx, tb.Barrier(), seal.DomainUnseal, seal.OpUnseal, "h1", "h3"); err != nil || !o.Complete {
		t.Fatalf("first quorum = %+v, %v", o, err)
	}
	if hook.count() != 0 || tb.Barrier().IsSealed() || tb.Barrier().ShamirArmed() {
		t.Fatalf("after the quorum: hook=%d sealed=%v armed=%v", hook.count(), tb.Barrier().IsSealed(), tb.Barrier().ShamirArmed())
	}
	// Once keyed, the statement arms nothing.
	if err := svc.TenantBarrierArmShamir(ctx, "acme", params.Roster, params.UnsealKeyCheck); err != nil || tb.Barrier().ShamirArmed() {
		t.Fatalf("arming a keyed node = %v", err)
	}
}

func TestTenantBarrierArmShamir_Refusals(t *testing.T) {
	svc, _ := newClaimService(t, &bookDouble{}, "acme")
	ctx := context.Background()
	_, params := firstNodeShamir(t)

	if err := svc.TenantBarrierArmShamir(ctx, "acme", nil, nil); err != nil {
		t.Fatalf("a statement with nothing to arm = %v", err)
	}
	if _, err := svc.registry.GetTenant("acme"); err == nil {
		t.Fatal("a statement with nothing to arm registered a barrier")
	}
	if err := svc.TenantBarrierArmShamir(ctx, "globex", params.Roster, params.UnsealKeyCheck); err == nil {
		t.Fatal("armed a tenant that does not exist")
	}
	var barrierErr *BarrierError
	if err := svc.TenantBarrierArmShamir(ctx, "acme", params.Roster, []byte("short")); !errors.As(err, &barrierErr) || barrierErr.Op != "arm" {
		t.Fatalf("a short check value = %v, want BarrierError{Op: arm}", err)
	}
}
