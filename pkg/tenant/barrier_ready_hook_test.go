// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// hookCapture is a BarrierReadyHook double that records every tenant ID it
// was called with, in order, so a test can assert both WHETHER and HOW MANY
// times notifyBarrierReady fired.
type hookCapture struct {
	mu    sync.Mutex
	calls []string
	modes []AdminCompletion
	err   error // returned by every call
}

func (h *hookCapture) hook(_ context.Context, tenantID string, mode AdminCompletion) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, tenantID)
	h.modes = append(h.modes, mode)
	return h.err
}

// lastMode returns the AdminCompletion of the latest call.
func (h *hookCapture) lastMode() AdminCompletion {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.modes[len(h.modes)-1]
}

// failWith makes every later call return err.
func (h *hookCapture) failWith(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
}

var _ BarrierReadyHook = (&hookCapture{}).hook

// count returns the number of times the hook has fired so far.
func (h *hookCapture) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// reset clears recorded calls, so a test can isolate the call(s) made by the
// operation under test from the ones made by its setup.
func (h *hookCapture) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = nil
}

// newHookService builds a TenantService wired with a capturing
// BarrierReadyHook (and, optionally, holder deps for the Shamir path) over a
// fresh in-memory-backed barrier registry.
func newHookService(t *testing.T, extra ...TenantServiceOption) (*TenantService, *seal.BarrierRegistry, *hookCapture) {
	t.Helper()
	hook := &hookCapture{}
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	opts := append([]TenantServiceOption{WithBarrierReadyHook(hook.hook)}, extra...)
	svc, err := NewTenantService(store, reg, testLogger(), opts...)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg, hook
}

// ---------------------------------------------------------------------------
// A: called after barrier init (none / passphrase / shamir).
// ---------------------------------------------------------------------------

// TestBarrierReadyHook_CalledAfterInit_None proves the hook fires once the
// "none" strategy finishes initializing (which also unseals the barrier).
func TestBarrierReadyHook_CalledAfterInit_None(t *testing.T) {
	svc, reg, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-init-none"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Init None", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(none): %v", err)
	}
	if got := hook.count(); got != 1 {
		t.Fatalf("hook fired %d times after none-strategy init, want 1 (calls: %v)", got, hook.calls)
	}
}

// TestBarrierReadyHook_CalledAfterInit_Passphrase proves the same for the
// passphrase strategy.
func TestBarrierReadyHook_CalledAfterInit_Passphrase(t *testing.T) {
	svc, reg, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-init-passphrase"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Init Passphrase", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte("correct-horse-battery"), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(passphrase): %v", err)
	}
	if got := hook.count(); got != 1 {
		t.Fatalf("hook fired %d times after passphrase-strategy init, want 1 (calls: %v)", got, hook.calls)
	}
}

// TestBarrierReadyHook_CalledAfterInit_Shamir proves the hook also fires
// after a Shamir init -- which, like the software strategies, leaves the
// barrier unsealed until it is explicitly sealed again.
func TestBarrierReadyHook_CalledAfterInit_Shamir(t *testing.T) {
	svc, _, hook := newHookService(t, WithTenantHolderDeps(ceremony.HolderDeps{CA: newTenantHolderCA(t), TrustDomain: holderTrustDomain}))
	const tenantID = "hook-init-shamir"

	if _, err := svc.CreateTenant(context.Background(), tenantID, "Hook Init Shamir", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	ctx := tenantAdminCtx(tenantID)
	holders := []*tenantHolder{newTenantHolder(t, "alice"), newTenantHolder(t, "bob"), newTenantHolder(t, "carol")}
	for _, h := range holders {
		if _, err := svc.TenantBarrierOfficerRegister(ctx, tenantID, &ceremony.RegisterOfficerRequest{Username: h.name, Role: seal.RoleCustodian, CSRPEM: h.csr}); err != nil {
			t.Fatalf("register %s: %v", h.name, err)
		}
	}

	if _, err := initShamir(ctx, svc, tenantID, len(holders), len(holders), nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(shamir): %v", err)
	}
	if got := hook.count(); got != 1 {
		t.Fatalf("hook fired %d times after shamir init, want 1 (calls: %v)", got, hook.calls)
	}
}

// ---------------------------------------------------------------------------
// B: called after unseal, and NOT merely because the barrier is sealed.
// ---------------------------------------------------------------------------

// TestBarrierReadyHook_CalledAfterUnseal proves the hook fires again on an
// explicit TenantBarrierUnseal call.
func TestBarrierReadyHook_CalledAfterUnseal(t *testing.T) {
	svc, reg, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-unseal"
	const passphrase = "hook-unseal-pw"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Unseal", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(passphrase), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if err := reg.SealTenant(tenantID); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	hook.reset()

	if err := svc.TenantBarrierUnseal(WithTenantID(ctx, tenantID), tenantID, []byte(passphrase)); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}
	if got := hook.count(); got != 1 {
		t.Fatalf("hook fired %d times after unseal, want 1 (calls: %v)", got, hook.calls)
	}
	if got := hook.lastMode(); got != AdminCompletionWithin {
		t.Fatalf("an unseal asked the hook for mode %d; want AdminCompletionWithin", got)
	}
}

// errHookFailed is what a failing barrier-ready hook returns.
var errHookFailed = errors.New("barrier-ready hook failed")

// TestBarrierReadyHook_InitFailsWithTheHookError proves a barrier
// initialization fails with the hook's own error: its caller must not
// proceed on an init whose follow-up work -- a pending admin's account --
// is missing.
func TestBarrierReadyHook_InitFailsWithTheHookError(t *testing.T) {
	svc, _, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-init-fails"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Init Fails", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	hook.failWith(errHookFailed)

	res, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte("hook-init-fails-pw"), 0, 0, seal.StrategyPassphrase, nil)
	if !errors.Is(err, errHookFailed) || res != nil {
		t.Fatalf("TenantBarrierInitWithStrategy = %+v, %v; want nil and the hook's error", res, err)
	}
	if got := hook.count(); got != 1 {
		t.Fatalf("hook fired %d times, want 1 (calls: %v)", got, hook.calls)
	}
}

// TestBarrierReadyHook_ShamirInitFailsWithTheHookError proves the same for
// the Shamir init.
func TestBarrierReadyHook_ShamirInitFailsWithTheHookError(t *testing.T) {
	svc, _, hook := newHookService(t, WithTenantHolderDeps(ceremony.HolderDeps{CA: newTenantHolderCA(t), TrustDomain: holderTrustDomain}))
	const tenantID = "hook-shamir-init-fails"

	if _, err := svc.CreateTenant(context.Background(), tenantID, "Hook Shamir Init Fails", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	ctx := tenantAdminCtx(tenantID)
	holders := []*tenantHolder{newTenantHolder(t, "alice"), newTenantHolder(t, "bob"), newTenantHolder(t, "carol")}
	for _, h := range holders {
		if _, err := svc.TenantBarrierOfficerRegister(ctx, tenantID, &ceremony.RegisterOfficerRequest{Username: h.name, Role: seal.RoleCustodian, CSRPEM: h.csr}); err != nil {
			t.Fatalf("register %s: %v", h.name, err)
		}
	}
	hook.failWith(errHookFailed)

	res, err := initShamir(ctx, svc, tenantID, len(holders), len(holders), nil)
	if !errors.Is(err, errHookFailed) || res != nil {
		t.Fatalf("shamir init = %+v, %v; want nil and the hook's error", res, err)
	}
}

// registerHookHolders creates tenantID and enrols three custodians for a
// Shamir init, returning the tenant admin's context.
func registerHookHolders(t *testing.T, svc *TenantService, tenantID string) (context.Context, int) {
	t.Helper()
	if _, err := svc.CreateTenant(context.Background(), tenantID, tenantID, ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	ctx := tenantAdminCtx(tenantID)
	holders := []*tenantHolder{newTenantHolder(t, "alice"), newTenantHolder(t, "bob"), newTenantHolder(t, "carol")}
	for _, h := range holders {
		if _, err := svc.TenantBarrierOfficerRegister(ctx, tenantID, &ceremony.RegisterOfficerRequest{Username: h.name, Role: seal.RoleCustodian, CSRPEM: h.csr}); err != nil {
			t.Fatalf("register %s: %v", h.name, err)
		}
	}
	return ctx, len(holders)
}

// completeShamir runs both requests of a Shamir init and returns the
// second's result, the one the barrier-ready hook shapes.
func completeShamir(ctx context.Context, svc *TenantService, tenantID string, n int) (*TenantBarrierInitResult, error) {
	first, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, nil, n, n, seal.StrategyShamir, nil)
	if err != nil {
		return nil, err
	}
	statement := shamirStatement(&seal.ShamirInitParams{Threshold: first.Threshold, Roster: first.Roster, UnsealKeyCheck: first.UnsealKeyCheck})
	return svc.TenantBarrierInitWithStrategy(ctx, tenantID, nil, n, n, seal.StrategyShamir, nil, WithInitStatement(statement))
}

// TestBarrierReadyHook_ShamirInitPendingAdminIsASuccess proves a Shamir init
// whose hook reports ErrTenantAdminPending -- the tenant's home group has no
// leader yet -- answers the committed init with AdminPending set rather than
// an error that would cost the caller the init's holder certificates, and
// asks the hook for one attempt.
func TestBarrierReadyHook_ShamirInitPendingAdminIsASuccess(t *testing.T) {
	svc, _, hook := newHookService(t, WithTenantHolderDeps(ceremony.HolderDeps{CA: newTenantHolderCA(t), TrustDomain: holderTrustDomain}))
	const tenantID = "hook-shamir-admin-pending"
	ctx, n := registerHookHolders(t, svc, tenantID)
	hook.failWith(ErrTenantAdminPending)

	res, err := completeShamir(ctx, svc, tenantID, n)
	if err != nil {
		t.Fatalf("shamir init = %v; want the committed init", err)
	}
	if !res.AdminPending || res.Threshold != n {
		t.Fatalf("shamir init = %+v; want AdminPending and the split", res)
	}
	if got := hook.lastMode(); got != AdminCompletionOnce {
		t.Fatalf("the shamir init asked the hook for mode %d; want AdminCompletionOnce", got)
	}
	st, err := svc.TenantBarrierStatus(ctx, tenantID)
	if err != nil || !st.Initialized || st.Sealed {
		t.Fatalf("status after a pending-admin init = %+v, %v; want initialized and unsealed", st, err)
	}
}

// TestBarrierReadyHook_ShamirInitCompletedAdminIsNotPending proves a Shamir
// init whose hook completes the admin reports nothing pending.
func TestBarrierReadyHook_ShamirInitCompletedAdminIsNotPending(t *testing.T) {
	svc, _, hook := newHookService(t, WithTenantHolderDeps(ceremony.HolderDeps{CA: newTenantHolderCA(t), TrustDomain: holderTrustDomain}))
	const tenantID = "hook-shamir-admin-complete"
	ctx, n := registerHookHolders(t, svc, tenantID)

	res, err := completeShamir(ctx, svc, tenantID, n)
	if err != nil || res.AdminPending {
		t.Fatalf("shamir init = %+v, %v; want success with no admin pending", res, err)
	}
	if got := hook.lastMode(); got != AdminCompletionOnce {
		t.Fatalf("the shamir init asked the hook for mode %d; want AdminCompletionOnce", got)
	}
}

// TestBarrierReadyHook_OtherTriggersWaitWithinTheHooksBounds proves only the
// Shamir init asks for one attempt: a passphrase init, an unseal and a status
// read let the hook complete the admin within its own bounds.
func TestBarrierReadyHook_OtherTriggersWaitWithinTheHooksBounds(t *testing.T) {
	svc, _, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-modes"
	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Modes", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte("hook-modes-pw"), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("passphrase init: %v", err)
	}
	if got := hook.lastMode(); got != AdminCompletionWithin {
		t.Fatalf("a passphrase init asked the hook for mode %d; want AdminCompletionWithin", got)
	}
	if _, err := svc.TenantBarrierStatus(ctx, tenantID); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got := hook.lastMode(); got != AdminCompletionWithin {
		t.Fatalf("a status read asked the hook for mode %d; want AdminCompletionWithin", got)
	}
}

// TestBarrierReadyHook_UnsealAndStatusSucceedWhateverTheHookReturns proves an
// unseal and a status read are not failed by the hook: each has served its
// purpose, and the next one runs the hook again.
func TestBarrierReadyHook_UnsealAndStatusSucceedWhateverTheHookReturns(t *testing.T) {
	svc, reg, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-unseal-ignores"
	const passphrase = "hook-unseal-ignores-pw"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Unseal Ignores", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(passphrase), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if err := reg.SealTenant(tenantID); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	hook.reset()
	hook.failWith(errHookFailed)

	if err := svc.TenantBarrierUnseal(WithTenantID(ctx, tenantID), tenantID, []byte(passphrase)); err != nil {
		t.Fatalf("TenantBarrierUnseal = %v; want nil whatever the hook returns", err)
	}
	if _, err := svc.TenantBarrierStatus(ctx, tenantID); err != nil {
		t.Fatalf("TenantBarrierStatus = %v; want nil whatever the hook returns", err)
	}
	if got := hook.count(); got != 2 {
		t.Fatalf("hook fired %d times, want 2: the unseal and the status read (calls: %v)", got, hook.calls)
	}
}

// TestBarrierReadyHook_NotCalledWhileSealed proves sealing alone -- with no
// subsequent unseal -- never fires the hook: it is unseal that makes the
// barrier ready, not the seal operation that precedes it.
func TestBarrierReadyHook_NotCalledWhileSealed(t *testing.T) {
	svc, _, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-sealed-only"
	const passphrase = "hook-sealed-pw"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Sealed Only", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(passphrase), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	hook.reset()

	if err := svc.TenantBarrierSeal(WithTenantID(ctx, tenantID), tenantID); err != nil {
		t.Fatalf("TenantBarrierSeal: %v", err)
	}
	if got := hook.count(); got != 0 {
		t.Fatalf("hook fired %d times on seal alone, want 0 (calls: %v)", got, hook.calls)
	}
}

// ---------------------------------------------------------------------------
// B2: a holder quorum fires the hook only when it completes an UNSEAL.
// ---------------------------------------------------------------------------

// newHookQuorumTenant builds a hook-capturing service with a 2-of-2 Shamir
// tenant barrier whose holders have claimed their envelopes, and clears the
// hook calls its init made.
func newHookQuorumTenant(t *testing.T, tenantID string, extra ...TenantServiceOption) (*TenantService, *seal.BarrierRegistry, *hookCapture, []*tenantHolder) {
	t.Helper()
	opts := append([]TenantServiceOption{WithTenantHolderDeps(ceremony.HolderDeps{CA: newTenantHolderCA(t), TrustDomain: holderTrustDomain})}, extra...)
	svc, reg, hook := newHookService(t, opts...)
	if _, err := svc.CreateTenant(context.Background(), tenantID, "Hook Quorum", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	holders := []*tenantHolder{newTenantHolder(t, "alice"), newTenantHolder(t, "bob")}
	shamirTenantWithClaimedHolders(t, svc, tenantAdminCtx(tenantID), tenantID, holders)
	hook.reset()
	return svc, reg, hook, holders
}

// TestBarrierReadyHook_QuorumUnseal_CalledOnceOnCompletion proves the hook
// fires exactly once, on the holder submission that completes an unseal
// quorum, and not on the share below the threshold.
func TestBarrierReadyHook_QuorumUnseal_CalledOnceOnCompletion(t *testing.T) {
	const tenantID = "hook-quorum-unseal"
	svc, _, hook, holders := newHookQuorumTenant(t, tenantID)
	ctx := tenantAdminCtx(tenantID)

	for _, h := range holders {
		if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpSeal, h); err != nil {
			t.Fatalf("seal share %s: %v", h.name, err)
		}
	}
	hook.reset()

	out, err := submitTenant(t, svc, ctx, tenantID, seal.OpUnseal, holders[0])
	if err != nil || out.Complete {
		t.Fatalf("first unseal share = %+v, %v; want an incomplete round", out, err)
	}
	if got := hook.count(); got != 0 {
		t.Fatalf("hook fired %d times below the unseal quorum, want 0 (calls: %v)", got, hook.calls)
	}

	out, err = submitTenant(t, svc, ctx, tenantID, seal.OpUnseal, holders[1])
	if err != nil || !out.Complete {
		t.Fatalf("second unseal share = %+v, %v; want a complete quorum", out, err)
	}
	if got := hook.count(); got != 1 || hook.calls[0] != tenantID {
		t.Fatalf("hook calls after the unseal quorum = %v, want [%s]", hook.calls, tenantID)
	}
}

// TestBarrierReadyHook_QuorumUnsealAsksForOneAttempt pins the mode a holder
// quorum unseal runs the hook in: one attempt on the caller's context, as a
// Shamir init does. The quorum unseals only the node that served it, so the
// bounded wait would hold the reply while the home group cannot complete the
// admin from here (main run 73226: a QUIC quorum unseal on a follower gave no
// reply inside the CLI's 30 s deadline).
func TestBarrierReadyHook_QuorumUnsealAsksForOneAttempt(t *testing.T) {
	const tenantID = "hook-quorum-mode"
	svc, _, hook, holders := newHookQuorumTenant(t, tenantID)
	ctx := tenantAdminCtx(tenantID)

	for _, h := range holders {
		if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpSeal, h); err != nil {
			t.Fatalf("seal share %s: %v", h.name, err)
		}
	}
	hook.reset()
	hook.failWith(ErrTenantAdminPending)

	if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpUnseal, holders[0]); err != nil {
		t.Fatalf("first unseal share: %v", err)
	}
	out, err := submitTenant(t, svc, ctx, tenantID, seal.OpUnseal, holders[1])
	if err != nil || !out.Complete {
		t.Fatalf("second unseal share = %+v, %v; want a complete quorum whatever the hook returns", out, err)
	}
	if got := hook.count(); got != 1 {
		t.Fatalf("hook fired %d times on the unseal quorum, want 1", got)
	}
	if got := hook.lastMode(); got != AdminCompletionOnce {
		t.Fatalf("a quorum unseal asked the hook for mode %d; want AdminCompletionOnce", got)
	}
}

// TestBarrierReadyHook_QuorumNonUnseal_NotCalled proves a quorum that
// completes any other operation never fires the hook: a seal quorum leaves
// the barrier unusable, and a rekey quorum leaves its seal state unchanged.
func TestBarrierReadyHook_QuorumNonUnseal_NotCalled(t *testing.T) {
	tests := map[seal.QuorumOp]func(t *testing.T, svc *TenantService, ctx context.Context, tenantID string){
		seal.OpSeal: func(*testing.T, *TenantService, context.Context, string) {},
		seal.OpRekey: func(t *testing.T, svc *TenantService, ctx context.Context, tenantID string) {
			t.Helper()
			if _, err := svc.TenantBarrierRekeyStart(ctx, tenantID, 2, seal.FactorPolicyAnyEnrolled); err != nil {
				t.Fatalf("rekey start: %v", err)
			}
		},
	}
	for op, start := range tests {
		t.Run(string(op), func(t *testing.T) {
			tenantID := "hook-quorum-" + string(op)
			svc, _, hook, holders := newHookQuorumTenant(t, tenantID)
			ctx := tenantAdminCtx(tenantID)
			start(t, svc, ctx, tenantID)

			var out *seal.QuorumOutcome
			for _, h := range holders {
				var err error
				if out, err = submitTenant(t, svc, ctx, tenantID, op, h); err != nil {
					t.Fatalf("%s share %s: %v", op, h.name, err)
				}
			}
			if !out.Complete || out.Op != op {
				t.Fatalf("outcome = %+v, want a complete %s quorum", out, op)
			}
			if got := hook.count(); got != 0 {
				t.Fatalf("hook fired %d times on a %s quorum, want 0 (calls: %v)", got, op, hook.calls)
			}
		})
	}
}

// TestBarrierReadyHook_QuorumUnseal_NotCalledOnError proves an unseal quorum
// whose cluster seal record cannot be cleared -- which seals the barrier
// again and fails the submission -- does not fire the hook.
func TestBarrierReadyHook_QuorumUnseal_NotCalledOnError(t *testing.T) {
	const tenantID = "hook-quorum-error"
	sealer := clustersealtest.New(true)
	svc, reg, hook, holders := newHookQuorumTenant(t, tenantID, WithClusterSealer(sealer))
	sealer.OnSeal(func(b clusterseal.Barrier) error { return reg.SealTenant(b.Tenant()) })
	ctx := tenantAdminCtx(tenantID)

	for _, h := range holders {
		if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpSeal, h); err != nil {
			t.Fatalf("seal share %s: %v", h.name, err)
		}
	}
	refused := errors.New("no quorum")
	sealer.SetClearErr(refused)
	hook.reset()

	if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpUnseal, holders[0]); err != nil {
		t.Fatalf("first unseal share: %v", err)
	}
	if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpUnseal, holders[1]); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the clear's error", err)
	}
	if got := hook.count(); got != 0 {
		t.Fatalf("hook fired %d times on a failed unseal quorum, want 0 (calls: %v)", got, hook.calls)
	}
}

// ---------------------------------------------------------------------------
// C: TenantBarrierStatus fires the hook only when Initialized && !Sealed.
// ---------------------------------------------------------------------------

// TestBarrierReadyHook_Status_CalledWhenInitializedAndUnsealed proves a
// status read on an initialized, unsealed barrier fires the hook -- the
// retry path for a hook (or its downstream effect) that failed earlier.
func TestBarrierReadyHook_Status_CalledWhenInitializedAndUnsealed(t *testing.T) {
	svc, _, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-status-ready"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Status Ready", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	hook.reset()

	st, err := svc.TenantBarrierStatus(ctx, tenantID)
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if !st.Initialized || st.Sealed {
		t.Fatalf("test precondition: expected initialized/unsealed status, got %+v", st)
	}
	if got := hook.count(); got != 1 {
		t.Fatalf("hook fired %d times on an initialized+unsealed status read, want 1 (calls: %v)", got, hook.calls)
	}
}

// TestBarrierReadyHook_Status_NotCalledWhenSealed proves a status read on a
// SEALED (even though initialized) barrier does not fire the hook: the
// identity records behind it are not readable, so the barrier is not
// "ready" by this hook's contract.
func TestBarrierReadyHook_Status_NotCalledWhenSealed(t *testing.T) {
	svc, reg, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-status-sealed"
	const passphrase = "hook-status-sealed-pw"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Status Sealed", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(passphrase), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if err := reg.SealTenant(tenantID); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	hook.reset()

	st, err := svc.TenantBarrierStatus(ctx, tenantID)
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if !st.Sealed {
		t.Fatal("test precondition: expected a sealed status")
	}
	if got := hook.count(); got != 0 {
		t.Fatalf("hook fired %d times on a sealed status read, want 0 (calls: %v)", got, hook.calls)
	}
}

// TestBarrierReadyHook_Status_NotCalledWhenNeverRegistered proves a status
// read for a tenant whose barrier was never registered at all -- reported
// as Sealed=true, Initialized=false -- does not fire the hook either.
func TestBarrierReadyHook_Status_NotCalledWhenNeverRegistered(t *testing.T) {
	svc, reg, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-status-unregistered"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Status Unregistered", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}
	hook.reset()

	st, err := svc.TenantBarrierStatus(ctx, tenantID)
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if st.Initialized {
		t.Fatal("test precondition: expected an unregistered barrier to report Initialized=false")
	}
	if got := hook.count(); got != 0 {
		t.Fatalf("hook fired %d times for a never-registered barrier, want 0 (calls: %v)", got, hook.calls)
	}
}

// ---------------------------------------------------------------------------
// D: a nil hook is a no-op.
// ---------------------------------------------------------------------------

// TestBarrierReadyHook_NilHookIsNoop proves a TenantService built with no
// WithBarrierReadyHook option at all runs every barrier operation without
// panicking or erroring because of the absent hook.
func TestBarrierReadyHook_NilHookIsNoop(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()
	const tenantID = "hook-nil-noop"
	const passphrase = "hook-nil-noop-pw"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Nil Noop", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(passphrase), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if _, err := svc.TenantBarrierStatus(ctx, tenantID); err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if err := reg.SealTenant(tenantID); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := svc.TenantBarrierUnseal(WithTenantID(ctx, tenantID), tenantID, []byte(passphrase)); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}

	// Also exercise notifyBarrierReady directly with no hook installed at
	// all, which is the precise no-op this test is named for.
	svc.notifyBarrierReady(ctx, tenantID)
}

// TestBarrierReadyHook_HomeLeadershipGained_CalledWhenInitializedAndUnsealed
// proves this node gaining a tenant's home-group leadership is a trigger: the
// hook runs once, in AdminCompletionWithin mode.
func TestBarrierReadyHook_HomeLeadershipGained_CalledWhenInitializedAndUnsealed(t *testing.T) {
	svc, _, hook := newHookService(t)
	ctx := context.Background()
	const tenantID = "hook-leader-ready"

	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Leader Ready", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	hook.reset()

	svc.HomeLeadershipGained(ctx, tenantID)
	if got := hook.count(); got != 1 {
		t.Fatalf("hook fired %d times on gaining home leadership, want 1 (calls: %v)", got, hook.calls)
	}
	if got := hook.lastMode(); got != AdminCompletionWithin {
		t.Fatalf("gaining home leadership asked the hook for mode %d; want AdminCompletionWithin", got)
	}
}

// TestBarrierReadyHook_HomeLeadershipGained_NotCalledWhenNotReady proves a
// leadership change on a tenant whose barrier is sealed, never initialized or
// unknown runs no hook: there is no usable barrier to complete an admin over.
func TestBarrierReadyHook_HomeLeadershipGained_NotCalledWhenNotReady(t *testing.T) {
	svc, reg, hook := newHookService(t)
	ctx := context.Background()
	const sealed, uninitialized = "hook-leader-sealed", "hook-leader-uninit"

	for _, id := range []string{sealed, uninitialized} {
		if _, err := svc.CreateTenant(ctx, id, id, ""); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, sealed, []byte("hook-leader-pw"), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if err := reg.SealTenant(sealed); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	hook.reset()

	for _, id := range []string{sealed, uninitialized, "hook-leader-unknown", ""} {
		svc.HomeLeadershipGained(ctx, id)
	}
	if got := hook.count(); got != 0 {
		t.Fatalf("hook fired %d times for tenants with no usable barrier, want 0 (calls: %v)", got, hook.calls)
	}
}

// TestBarrierReadyHook_HomeLeadershipGained_NilHookIsNoop proves a service
// built without a hook ignores a leadership change.
func TestBarrierReadyHook_HomeLeadershipGained_NilHookIsNoop(t *testing.T) {
	svc, err := NewTenantService(seal.NewMemoryBackend(), newTestBarrierRegistry(t), testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()
	const tenantID = "hook-leader-nil"
	if _, err := svc.CreateTenant(ctx, tenantID, "Hook Leader Nil", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	svc.HomeLeadershipGained(ctx, tenantID)
}
