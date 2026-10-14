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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// providerUnsealFixture is a tenant whose barrier was initialized with a
// strategy only the key provider can resolve, holding one value, then
// restarted: its shell is re-registered with the system barrier's strategies,
// as the boot path re-registers it.
type providerUnsealFixture struct {
	svc      *TenantService
	book     *bookDouble
	provider *recordingKeyProvider
}

const providerUnsealKeyRef = "kms://owner-key"

func newProviderUnsealFixture(t *testing.T, strategy seal.SealingStrategy) providerUnsealFixture {
	t.Helper()
	ctx := WithTenantID(context.Background(), "acme")
	f := providerUnsealFixture{
		book: &bookDouble{},
		provider: &recordingKeyProvider{result: &seal.BarrierKeyStrategy{
			Strategy:   strategy,
			Credential: seal.CredentialNone,
			Custody:    seal.CustodyOwner,
		}},
	}
	f.svc = newKeyProviderService(t, "acme", f.provider, WithNodeID("node-a"), WithBarrierInitBook(f.book))
	if _, err := f.svc.UpdateEntitlements(context.Background(), "acme", &quota.Entitlements{
		AllowedBarrierModes: []string{string(strategy.ID())},
	}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}
	if _, err := f.svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 0, 0, strategy.ID(), nil,
		WithInitStatement([]byte("signed")), WithBarrierKeyRef(providerUnsealKeyRef)); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	tb, err := f.svc.registry.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if err := tb.Barrier().Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	f.restart(t)
	return f
}

// restart drops the tenant's in-memory barrier and re-registers its shell
// over the same durable store with the system barrier's strategies.
func (f providerUnsealFixture) restart(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := f.svc.registry.ReleaseTenant(ctx, "acme"); err != nil {
		t.Fatalf("ReleaseTenant: %v", err)
	}
	if _, err := f.svc.registry.RegisterTenantPooled(ctx, "acme"); err != nil {
		t.Fatalf("RegisterTenantPooled(restart): %v", err)
	}
	f.provider.mu.Lock()
	f.provider.requests = nil
	f.provider.mu.Unlock()
}

func (f providerUnsealFixture) unseal() error {
	return f.svc.TenantBarrierUnseal(WithTenantID(context.Background(), "acme"), "acme", []byte("unused"))
}

func (f providerUnsealFixture) barrier(t *testing.T) *seal.TenantBarrier {
	t.Helper()
	tb, err := f.svc.registry.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	return tb
}

// After a restart the shell holds only the system barrier's strategies, so the
// registry alone cannot open the key; the unseal resolves the strategy the
// barrier's init statement names through the provider, with the statement's
// key reference and epoch, and serves the data written before the restart.
func TestTenantBarrierUnseal_ReattachesAProviderBackedBarrierFromItsStatement(t *testing.T) {
	device, err := sealtest.NewDeviceStrategy("test-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	f := newProviderUnsealFixture(t, device)

	if err := f.svc.registry.UnsealTenant(context.Background(), "acme", seal.Credentials{}); !errors.Is(err, seal.ErrStrategyNotFound) {
		t.Fatalf("registry UnsealTenant on the restarted shell = %v, want ErrStrategyNotFound", err)
	}
	if err := f.unseal(); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}

	want := seal.BarrierKeyRequest{
		Barrier:    string(clusterseal.TenantBarrier("acme")),
		NodeID:     "node-a",
		StrategyID: "test-kms",
		KeyRef:     providerUnsealKeyRef,
		Epoch:      1,
	}
	if len(f.provider.requests) != 1 || f.provider.requests[0] != want {
		t.Fatalf("provider requests = %+v, want [%+v]", f.provider.requests, want)
	}
	if device.Unwraps() == 0 {
		t.Fatal("the device never unwrapped the root key")
	}
	got, err := f.barrier(t).Barrier().Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get after unseal: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get = %q, want %q", got, "v")
	}
}

// A system strategy registered under the same ID as the tenant's is never the
// one that opens the key: the provider's is, and it stays registered.
func TestTenantBarrierUnseal_NeverUsesASameIDSystemStrategy(t *testing.T) {
	tenantKey, err := seal.NewSoftwareStrategy(context.Background(), []byte("tenant-provider-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	f := newProviderUnsealFixture(t, tenantKey)
	shell, ok := f.barrier(t).Barrier().Strategies()[tenantKey.ID()]
	if !ok || shell == seal.SealingStrategy(tenantKey) {
		t.Fatalf("test precondition: the restarted shell must carry the system's own %q strategy", tenantKey.ID())
	}

	if err := f.unseal(); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}
	if got := f.barrier(t).Barrier().Strategies()[tenantKey.ID()]; got != seal.SealingStrategy(tenantKey) {
		t.Fatal("the barrier was unsealed with the system's same-ID strategy, not the provider's")
	}
	if len(f.provider.requests) != 1 || f.provider.requests[0].StrategyID != tenantKey.ID() {
		t.Fatalf("provider requests = %+v, want one for %q", f.provider.requests, tenantKey.ID())
	}
}

// A statement naming another strategy than the one that sealed the key is
// refused, and the barrier stays sealed.
func TestTenantBarrierUnseal_RefusesAStatementNamingAnotherStrategy(t *testing.T) {
	device, err := sealtest.NewDeviceStrategy("test-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	f := newProviderUnsealFixture(t, device)
	other, err := sealtest.NewDeviceStrategy("other-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	f.provider.result = &seal.BarrierKeyStrategy{Strategy: other, Credential: seal.CredentialNone, Custody: seal.CustodyOwner}
	f.book.recorded = map[string]BarrierInitTerms{"acme": {StrategyID: "other-kms", KeyRef: providerUnsealKeyRef, Epoch: 1}}

	if err := f.unseal(); !errors.Is(err, seal.ErrStrategyMismatch) {
		t.Fatalf("TenantBarrierUnseal = %v, want ErrStrategyMismatch", err)
	}
	if !f.barrier(t).IsSealed() {
		t.Fatal("the barrier was unsealed by a strategy its statement does not name")
	}
	if device.Unwraps() != 0 || other.Unwraps() != 0 {
		t.Fatal("a refused unseal reached a device")
	}
}

// A statement that cannot be read or no longer verifies fails closed, before
// the provider is asked for anything.
func TestTenantBarrierUnseal_AnUnreadableStatementFailsClosed(t *testing.T) {
	device, err := sealtest.NewDeviceStrategy("test-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	f := newProviderUnsealFixture(t, device)
	unverifiable := errors.New("statement no longer verifies")
	f.book.readErr = unverifiable

	err = f.unseal()
	var barrierErr *BarrierError
	if !errors.As(err, &barrierErr) || barrierErr.Op != "init statement" || !errors.Is(err, unverifiable) {
		t.Fatalf("TenantBarrierUnseal = %v, want a BarrierError for the init statement", err)
	}
	if len(f.provider.requests) != 0 || !f.barrier(t).IsSealed() {
		t.Fatal("an unreadable statement still reached the provider or unsealed the barrier")
	}
}

// A provider strategy that no longer resolves is the provider's error, and the
// barrier stays sealed.
func TestTenantBarrierUnseal_AProviderFailureStaysSealed(t *testing.T) {
	device, err := sealtest.NewDeviceStrategy("test-kms")
	if err != nil {
		t.Fatalf("NewDeviceStrategy: %v", err)
	}
	f := newProviderUnsealFixture(t, device)
	unreachable := errors.New("kms unreachable")
	f.provider.err = unreachable

	if err := f.unseal(); !errors.Is(err, unreachable) {
		t.Fatalf("TenantBarrierUnseal = %v, want the provider's error", err)
	}
	if !f.barrier(t).IsSealed() {
		t.Fatal("the barrier unsealed without its strategy")
	}
}

// A raft tenant's barrier is served by every node of its group and cannot
// have been initialized without a statement; one with none to unseal from is
// refused rather than opened with whatever the shell carries.
func TestUnsealBarrier_RaftTenantWithoutAStatementIsRefused(t *testing.T) {
	book := &bookDouble{}
	svc, _, _ := newHookService(t, WithBarrierInitBook(book))
	err := svc.unsealBarrier(context.Background(), &TenantInfo{ID: "acme", IsolationType: IsolationRaft}, seal.Credentials{})
	var missing *InitStatementMissingError
	if !errors.As(err, &missing) || missing.TenantID != "acme" {
		t.Fatalf("unsealBarrier(raft, no statement) = %v, want *InitStatementMissingError", err)
	}
	if !strings.Contains(missing.Error(), `"acme"`) {
		t.Fatalf("InitStatementMissingError = %q, want it to name the tenant", missing.Error())
	}
	if book.reads != 1 {
		t.Fatalf("book reads = %d, want 1", book.reads)
	}
}

// A barrier one node serves may have been initialized without a statement;
// it unseals with the strategy its own registration carries, as before.
func TestTenantBarrierUnseal_BarrierTenantWithoutAStatement(t *testing.T) {
	book := &bookDouble{}
	svc := newKeyProviderService(t, "acme", &recordingKeyProvider{}, WithBarrierInitBook(book))
	ctx := WithTenantID(context.Background(), "acme")
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if len(book.claimed()) != 0 {
		t.Fatal("test precondition: an init without a statement claimed one")
	}
	if err := svc.registry.SealTenant("acme"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := svc.TenantBarrierUnseal(ctx, "acme", []byte("unused")); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}
	if book.reads != 1 {
		t.Fatalf("book reads = %d, want 1", book.reads)
	}
}

// TestTenantBarrierUnseal_ANoneStatementAfterEnableEncryption pins the one
// sanctioned strategy change: enable-encryption moves a barrier off none and
// records no later-epoch statement, so the epoch-1 statement still names none
// while the root key is sealed with a keyed strategy. That barrier unseals
// with its registered strategies and the caller's credential, without asking
// the provider for the stale none strategy.
func TestTenantBarrierUnseal_ANoneStatementAfterEnableEncryption(t *testing.T) {
	const passphrase = "migrated-tenant-passphrase"
	f := providerUnsealFixture{
		book: &bookDouble{recorded: map[string]BarrierInitTerms{
			"acme": {StrategyID: seal.StrategyNone, Epoch: 1},
		}},
		provider: &recordingKeyProvider{},
	}
	f.svc = newKeyProviderService(t, "acme", f.provider, WithBarrierInitBook(f.book))
	ctx := WithTenantID(context.Background(), "acme")
	if _, err := f.svc.TenantBarrierInitWithStrategy(ctx, "acme", []byte(passphrase), 0, 0, seal.StrategyPassphrase, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if err := f.svc.registry.SealTenant("acme"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	f.provider.mu.Lock()
	f.provider.requests = nil
	f.provider.mu.Unlock()

	if err := f.svc.TenantBarrierUnseal(ctx, "acme", []byte(passphrase)); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}
	if f.barrier(t).IsSealed() {
		t.Fatal("migrated barrier still sealed")
	}
	if n := len(f.provider.requests); n != 0 {
		t.Fatalf("provider requests = %d, want 0 for a barrier migrated off its stated none", n)
	}
}

// TestMigratedOffNone covers the cases that keep a statement authoritative:
// a statement naming a keyed strategy, an unregistered barrier, and a
// barrier still sealed with none.
func TestMigratedOffNone(t *testing.T) {
	f := newProviderUnsealFixture(t, seal.NewNoneStrategy())
	if f.svc.migratedOffNone("acme", seal.StrategyPassphrase) {
		t.Fatal("a statement naming a keyed strategy reported as migrated off none")
	}
	if f.svc.migratedOffNone("acme", seal.StrategyNone) {
		t.Fatal("a barrier still sealed with none reported as migrated")
	}
	if f.svc.migratedOffNone("missing", seal.StrategyNone) {
		t.Fatal("an unregistered barrier reported as migrated")
	}
}

// A Shamir barrier's key is opened by its holders' shares, not a provider
// strategy: its statement routes the unseal to the registry's quorum path.
func TestUnsealBarrier_ShamirStatementUsesTheRegistry(t *testing.T) {
	provider := &recordingKeyProvider{}
	book := &bookDouble{recorded: map[string]BarrierInitTerms{"acme": {StrategyID: seal.StrategyShamir, Epoch: 1}}}
	svc := newKeyProviderService(t, "acme", provider, WithBarrierInitBook(book))
	ctx := WithTenantID(context.Background(), "acme")
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if err := svc.registry.SealTenant("acme"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	provider.requests = nil

	info, err := svc.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if err := svc.unsealBarrier(ctx, info, seal.Credentials{}); err != nil {
		t.Fatalf("unsealBarrier: %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("provider requests = %+v, want none for a Shamir statement", provider.requests)
	}
}

// Without a book the composition serves each barrier on one node and the
// registry's registration is the only record of its strategy.
func TestUnsealBarrier_NoBookUsesTheRegistry(t *testing.T) {
	provider := &recordingKeyProvider{}
	svc := newKeyProviderService(t, "acme", provider)
	ctx := WithTenantID(context.Background(), "acme")
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", nil, 0, 0, seal.StrategyNone, nil); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy: %v", err)
	}
	if err := svc.registry.SealTenant("acme"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	provider.requests = nil
	if err := svc.TenantBarrierUnseal(ctx, "acme", []byte("unused")); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("provider requests = %+v, want none without a book", provider.requests)
	}
}
