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

package seal

import (
	"context"
	"errors"
	"testing"
)

// newNoneBarrier returns an initialized none-strategy barrier over a fresh
// memory backend — the canonical "encryption not configured" starting state
// for an enable-encryption migration.
func newNoneBarrier(t *testing.T) (*StorageBarrier, StorageBackend) {
	t.Helper()
	backend := NewMemoryBackend()
	b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{RootKeyPath: "root-key"}, NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), Credentials{}); err != nil {
		t.Fatalf("Initialize none barrier: %v", err)
	}
	return b, backend
}

// TestMintKeyedSibling_Idempotent proves a second mint over the same staging
// suffix succeeds even when a prior mint left a staging blob behind (an
// interrupted/rolled-back migration). Without the self-healing DeleteRootKey in
// MintKeyedSibling, the second Initialize would fail with ErrAlreadyInitialized
// and permanently wedge every migration retry.
func TestMintKeyedSibling_Idempotent(t *testing.T) {
	ctx := context.Background()
	src, _ := newNoneBarrier(t)

	strategy := NewDeferredSoftwareStrategy()
	first, err := src.MintKeyedSibling(ctx, ".migrating", strategy, MustCredentials([]byte("pw")))
	if err != nil {
		t.Fatalf("first MintKeyedSibling: %v", err)
	}
	_ = first.Close()
	// The staging blob from the first mint is still on the backend (simulating a
	// rolled-back / interrupted migration that didn't delete it). A second mint
	// must still succeed.
	second, err := src.MintKeyedSibling(ctx, ".migrating", NewDeferredSoftwareStrategy(), MustCredentials([]byte("pw")))
	if err != nil {
		t.Fatalf("second MintKeyedSibling over leftover staging blob: %v (mint not idempotent — retries would wedge)", err)
	}
	_ = second.Close()
}

// ---------------------------------------------------------------------------
// Shamir-target migration: MintKeyedShamirSibling / AdoptMigratedRootKey /
// ReopenKeyed.
// ---------------------------------------------------------------------------

// TestMintKeyedShamirSibling_Success proves the sibling is minted via the
// real direct-mode Shamir ceremony (not the generic Initialize path, which
// cannot represent Shamir — see the method's doc comment): the returned
// sibling is unsealed, the params report the requested M-of-N, and the
// distributor captured exactly `total` sealed shares.
func TestMintKeyedShamirSibling_Success(t *testing.T) {
	ctx := context.Background()
	src, _ := newNoneBarrier(t)
	dist := newCaptureDistributor()
	src.SetCeremonyDistributor(dist)

	sib, params, err := src.MintKeyedShamirSibling(ctx, ".migrating", 2, 3)
	if err != nil {
		t.Fatalf("MintKeyedShamirSibling: %v", err)
	}
	defer func() { _ = sib.Close() }()

	if params.Threshold != 2 || params.TotalShares != 3 || params.ShareCount != 3 {
		t.Fatalf("params = %+v, want threshold=2 total=3 shareCount=3", params)
	}
	if len(dist.Shares()) != 3 {
		t.Fatalf("captured %d shares, want 3", len(dist.Shares()))
	}
	if sib.IsSealed() {
		t.Fatal("minted shamir sibling must be unsealed (direct-mode Initialize unseals it)")
	}
	if got := sib.Status().Strategy; got != StrategyShamir {
		t.Fatalf("sibling strategy = %q, want shamir", got)
	}
}

// TestMintKeyedShamirSibling_NoDistributor proves the operational precondition
// is enforced with the same typed error a fresh BarrierShamirInitialize would
// give: enabling encryption to Shamir requires a CeremonyDistributor already
// wired on the source barrier, exactly like initializing a new Shamir barrier.
func TestMintKeyedShamirSibling_NoDistributor(t *testing.T) {
	ctx := context.Background()
	src, _ := newNoneBarrier(t)
	// No SetCeremonyDistributor call.
	if _, _, err := src.MintKeyedShamirSibling(ctx, ".migrating", 2, 3); !errors.Is(err, ErrCeremonyDistributorNotConfigured) {
		t.Fatalf("MintKeyedShamirSibling without a distributor = %v, want ErrCeremonyDistributorNotConfigured", err)
	}
}

// TestMintKeyedShamirSibling_InvalidThreshold proves an out-of-range M-of-N is
// rejected before any ceremony work happens.
func TestMintKeyedShamirSibling_InvalidThreshold(t *testing.T) {
	ctx := context.Background()
	src, _ := newNoneBarrier(t)
	src.SetCeremonyDistributor(newCaptureDistributor())
	if _, _, err := src.MintKeyedShamirSibling(ctx, ".migrating", 1, 3); !errors.Is(err, ErrShamirThresholdInvalid) {
		t.Fatalf("MintKeyedShamirSibling(threshold=1) = %v, want ErrShamirThresholdInvalid", err)
	}
}

// TestAdoptMigratedRootKey_Shamir_AdoptsWrappedKeyAndScheme proves adopting a
// Shamir target does more than the generic passphrase-shaped copy: it also
// copies the wrapped-root-key blob the Shamir indirection depends on (without
// it, the migrated barrier could never again be unsealed — see the method's
// doc comment) and registers the M-of-N scheme so Status() reports it
// immediately.
func TestAdoptMigratedRootKey_Shamir_AdoptsWrappedKeyAndScheme(t *testing.T) {
	ctx := context.Background()
	src, backend := newNoneBarrier(t)
	dist := newCaptureDistributor()
	src.SetCeremonyDistributor(dist)

	sib, _, err := src.MintKeyedShamirSibling(ctx, ".migrating", 2, 3)
	if err != nil {
		t.Fatalf("MintKeyedShamirSibling: %v", err)
	}
	defer func() { _ = sib.Close() }()

	if err := src.AdoptMigratedRootKey(ctx, sib); err != nil {
		t.Fatalf("AdoptMigratedRootKey: %v", err)
	}

	status := src.Status()
	if status.Strategy != StrategyShamir {
		t.Fatalf("adopted strategy = %q, want shamir", status.Strategy)
	}
	if status.ShamirThreshold != 2 || status.ShamirTotal != 3 {
		t.Fatalf("adopted scheme = %d/%d, want 2/3", status.ShamirThreshold, status.ShamirTotal)
	}

	// The wrapped-root-key blob must have been copied onto the CANONICAL path
	// (not left stranded at the staging sibling's path) — this is exactly what
	// UnsealWithShare(s) reads on a future unseal.
	canonicalWrapped, gerr := backend.Get(ctx, src.wrappedRootKeyPath())
	if gerr != nil || len(canonicalWrapped) == 0 {
		t.Fatalf("canonical wrapped-root-key blob missing after adopt: %v", gerr)
	}
	stagingWrapped, _ := backend.Get(ctx, sib.wrappedRootKeyPath())
	if string(canonicalWrapped) != string(stagingWrapped) {
		t.Fatal("adopted wrapped-root-key blob does not match the staging sibling's")
	}
}

// TestAdoptMigratedRootKey_InvalidTargetStrategy proves a target whose active
// strategy is neither passphrase nor shamir is rejected (defense in depth: the
// caller's opts.Validate should already have rejected this earlier).
func TestAdoptMigratedRootKey_InvalidTargetStrategy(t *testing.T) {
	ctx := context.Background()
	src, _ := newNoneBarrier(t)
	none, _ := newNoneBarrier(t)
	if err := src.AdoptMigratedRootKey(ctx, none); !errors.Is(err, ErrMigrationInvalidTarget) {
		t.Fatalf("AdoptMigratedRootKey(none target) = %v, want ErrMigrationInvalidTarget", err)
	}
}

// TestReopenKeyed_Shamir_StaysSealedPendingQuorum proves ReopenKeyed does NOT
// attempt to auto-unseal a Shamir target (ShamirStrategy.UnsealRootKey always
// fails closed by design): it returns a sealed barrier with the M-of-N scheme
// + distributor already registered, ready for the normal
// UnsealWithShare(s)/Status() flow, rather than propagating the doomed unseal
// error.
func TestReopenKeyed_Shamir_StaysSealedPendingQuorum(t *testing.T) {
	ctx := context.Background()
	src, _ := newNoneBarrier(t)
	dist := newCaptureDistributor()
	src.SetCeremonyDistributor(dist)

	sib, _, err := src.MintKeyedShamirSibling(ctx, ".migrating", 2, 3)
	if err != nil {
		t.Fatalf("MintKeyedShamirSibling: %v", err)
	}
	defer func() { _ = sib.Close() }()
	if err := src.AdoptMigratedRootKey(ctx, sib); err != nil {
		t.Fatalf("AdoptMigratedRootKey: %v", err)
	}

	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	keyed, err := src.ReopenKeyed(ctx, shamirStrat, Credentials{})
	if err != nil {
		t.Fatalf("ReopenKeyed(shamir) must not error (it must not attempt unseal): %v", err)
	}
	if !keyed.IsSealed() {
		t.Fatal("a reopened Shamir barrier must stay SEALED pending Security Officer quorum")
	}
	status := keyed.Status()
	if status.ShamirThreshold != 2 || status.ShamirTotal != 3 {
		t.Fatalf("reopened sealed barrier scheme = %d/%d, want 2/3 (from config, since sealed)", status.ShamirThreshold, status.ShamirTotal)
	}
}

// TestReopenKeyed_Passphrase_Unseals proves the pre-existing (non-Shamir)
// behavior is unchanged: ReopenKeyed still unseals a passphrase target with
// the supplied credential.
func TestReopenKeyed_Passphrase_Unseals(t *testing.T) {
	ctx := context.Background()
	src, _ := newNoneBarrier(t)
	sib, err := src.MintKeyedSibling(ctx, ".migrating", NewDeferredSoftwareStrategy(), MustCredentials([]byte("pw")))
	if err != nil {
		t.Fatalf("MintKeyedSibling: %v", err)
	}
	defer func() { _ = sib.Close() }()
	if err := src.AdoptMigratedRootKey(ctx, sib); err != nil {
		t.Fatalf("AdoptMigratedRootKey: %v", err)
	}

	keyed, err := src.ReopenKeyed(ctx, NewDeferredSoftwareStrategy(), MustCredentials([]byte("pw")))
	if err != nil {
		t.Fatalf("ReopenKeyed(passphrase): %v", err)
	}
	if keyed.IsSealed() {
		t.Fatal("a reopened passphrase barrier must unseal immediately with the correct credential")
	}
}

func TestMigrationOptions_Validate(t *testing.T) {
	tests := []struct {
		name string
		opts MigrationOptions
		want error
	}{
		{
			name: "valid passphrase swap quiesced",
			opts: MigrationOptions{TargetStrategy: StrategyPassphrase, Mechanism: MechanismSwap, Availability: AvailabilityQuiesced},
			want: nil,
		},
		{
			name: "valid shamir in-place online",
			opts: MigrationOptions{TargetStrategy: StrategyShamir, Mechanism: MechanismInPlace, Availability: AvailabilityOnline, ShamirThreshold: 2, ShamirTotal: 3},
			want: nil,
		},
		{
			name: "target none is rejected",
			opts: MigrationOptions{TargetStrategy: StrategyNone, Mechanism: MechanismSwap, Availability: AvailabilityQuiesced},
			want: ErrMigrationInvalidTarget,
		},
		{
			name: "empty target is rejected",
			opts: MigrationOptions{Mechanism: MechanismSwap, Availability: AvailabilityQuiesced},
			want: ErrMigrationInvalidTarget,
		},
		{
			name: "unknown mechanism is rejected",
			opts: MigrationOptions{TargetStrategy: StrategyPassphrase, Mechanism: "bogus", Availability: AvailabilityQuiesced},
			want: ErrMigrationInvalidMechanism,
		},
		{
			name: "unknown availability is rejected",
			opts: MigrationOptions{TargetStrategy: StrategyPassphrase, Mechanism: MechanismSwap, Availability: "whenever"},
			want: ErrMigrationInvalidAvailability,
		},
		{
			name: "shamir threshold below 2 is rejected",
			opts: MigrationOptions{TargetStrategy: StrategyShamir, Mechanism: MechanismSwap, Availability: AvailabilityQuiesced, ShamirThreshold: 1, ShamirTotal: 3},
			want: ErrShamirThresholdInvalid,
		},
		{
			name: "shamir total below threshold is rejected",
			opts: MigrationOptions{TargetStrategy: StrategyShamir, Mechanism: MechanismSwap, Availability: AvailabilityQuiesced, ShamirThreshold: 3, ShamirTotal: 2},
			want: ErrShamirThresholdInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.opts.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMigrationJournal_RoundTrip(t *testing.T) {
	ctx := context.Background()
	b, _ := newNoneBarrier(t)
	defer func() { _ = b.Close() }()

	// No journal initially.
	if j, err := b.LoadMigrationJournal(ctx); err != nil || j != nil {
		t.Fatalf("LoadMigrationJournal (empty) = (%v, %v), want (nil, nil)", j, err)
	}

	opts := MigrationOptions{
		TargetStrategy: StrategyPassphrase,
		Mechanism:      MechanismSwap,
		Availability:   AvailabilityQuiesced,
		Credentials:    MustCredentials([]byte("pw")),
	}
	j, err := b.beginMigrationJournal(ctx, opts)
	if err != nil {
		t.Fatalf("beginMigrationJournal: %v", err)
	}
	if j.Phase != PhasePending {
		t.Fatalf("initial phase = %q, want %q", j.Phase, PhasePending)
	}
	if j.SourceStrategy != StrategyNone || j.TargetStrategy != StrategyPassphrase {
		t.Fatalf("journal strategies = %q->%q", j.SourceStrategy, j.TargetStrategy)
	}
	if j.StartedAt.IsZero() || j.UpdatedAt.IsZero() {
		t.Fatal("journal timestamps must be set")
	}

	// Reload from backend and confirm it persisted.
	loaded, err := b.LoadMigrationJournal(ctx)
	if err != nil {
		t.Fatalf("LoadMigrationJournal: %v", err)
	}
	if loaded == nil || loaded.Phase != PhasePending || loaded.Mechanism != MechanismSwap {
		t.Fatalf("reloaded journal = %+v", loaded)
	}

	// Advance phase and checkpoint.
	j.Phase = PhaseCopying
	j.Stores["mvcc"] = true
	if err := b.writeMigrationJournal(ctx, j); err != nil {
		t.Fatalf("writeMigrationJournal: %v", err)
	}
	loaded, _ = b.LoadMigrationJournal(ctx)
	if loaded.Phase != PhaseCopying || !loaded.Stores["mvcc"] {
		t.Fatalf("checkpoint not persisted: %+v", loaded)
	}

	// Clear after terminal.
	if err := b.clearMigrationJournal(ctx); err != nil {
		t.Fatalf("clearMigrationJournal: %v", err)
	}
	if j2, err := b.LoadMigrationJournal(ctx); err != nil || j2 != nil {
		t.Fatalf("after clear LoadMigrationJournal = (%v, %v), want (nil, nil)", j2, err)
	}
	// Clearing again is a no-op (not an error).
	if err := b.clearMigrationJournal(ctx); err != nil {
		t.Fatalf("clearMigrationJournal (idempotent) = %v", err)
	}
}

func TestBeginMigrationJournal_RejectsConcurrent(t *testing.T) {
	ctx := context.Background()
	b, _ := newNoneBarrier(t)
	defer func() { _ = b.Close() }()

	opts := MigrationOptions{TargetStrategy: StrategyPassphrase, Mechanism: MechanismSwap, Availability: AvailabilityQuiesced}
	if _, err := b.beginMigrationJournal(ctx, opts); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	// A second begin while the first is non-terminal must be rejected.
	if _, err := b.beginMigrationJournal(ctx, opts); !errors.Is(err, ErrMigrationInProgress) {
		t.Fatalf("second begin = %v, want ErrMigrationInProgress", err)
	}

	// After the prior journal reaches a terminal phase, a new migration is
	// allowed again.
	j, _ := b.LoadMigrationJournal(ctx)
	j.Phase = PhaseFailed
	if err := b.writeMigrationJournal(ctx, j); err != nil {
		t.Fatalf("write terminal: %v", err)
	}
	if _, err := b.beginMigrationJournal(ctx, opts); err != nil {
		t.Fatalf("begin after terminal: %v", err)
	}
}

func TestBeginMigrationJournal_RejectsInvalidOptionsAndNonNoneSource(t *testing.T) {
	ctx := context.Background()

	// Invalid options short-circuit before any state check.
	b, _ := newNoneBarrier(t)
	defer func() { _ = b.Close() }()
	bad := MigrationOptions{TargetStrategy: StrategyNone, Mechanism: MechanismSwap, Availability: AvailabilityQuiesced}
	if _, err := b.beginMigrationJournal(ctx, bad); !errors.Is(err, ErrMigrationInvalidTarget) {
		t.Fatalf("invalid options = %v, want ErrMigrationInvalidTarget", err)
	}

	// A keyed (software/passphrase) source barrier rejects enable-encryption.
	keyed, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "k"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier keyed: %v", err)
	}
	defer func() { _ = keyed.Close() }()
	if err := keyed.Initialize(ctx, MustCredentials([]byte("pw"))); err != nil {
		t.Fatalf("Initialize keyed: %v", err)
	}
	good := MigrationOptions{TargetStrategy: StrategyShamir, Mechanism: MechanismSwap, Availability: AvailabilityQuiesced, ShamirThreshold: 2, ShamirTotal: 3}
	if _, err := keyed.beginMigrationJournal(ctx, good); !errors.Is(err, ErrMigrationSourceNotNone) {
		t.Fatalf("keyed-source begin = %v, want ErrMigrationSourceNotNone", err)
	}
}

// TestBeginMigrationJournal_AcceptsSystemBarrierSource verifies that a tenant
// barrier sealed under the SystemBarrierStrategy (auto-unseal) is accepted as a
// valid enable-encryption migration source. The SourceStrategy in the written
// journal must reflect the actual active strategy (StrategySystemBarrier), not
// the legacy hardcoded StrategyNone.
func TestBeginMigrationJournal_AcceptsSystemBarrierSource(t *testing.T) {
	ctx := context.Background()

	// Build and initialise the system barrier that acts as the tenant KEK.
	sys, _ := newNoneBarrier(t)
	defer func() { _ = sys.Close() }()

	// Build a tenant barrier whose root key is wrapped by the system barrier.
	sysStrat := NewSystemBarrierStrategy(sys)
	tenant, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "tenant"}, sysStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier tenant: %v", err)
	}
	defer func() { _ = tenant.Close() }()
	if err := tenant.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("Initialize system-barrier tenant: %v", err)
	}

	// beginMigrationJournal must accept StrategySystemBarrier as a valid source.
	opts := MigrationOptions{
		TargetStrategy: StrategyPassphrase,
		Mechanism:      MechanismSwap,
		Availability:   AvailabilityQuiesced,
		Credentials:    MustCredentials([]byte("tenant-pw")),
	}
	j, err := tenant.beginMigrationJournal(ctx, opts)
	if err != nil {
		t.Fatalf("beginMigrationJournal system-barrier source: %v", err)
	}
	if j.SourceStrategy != StrategySystemBarrier {
		t.Fatalf("SourceStrategy = %q, want %q", j.SourceStrategy, StrategySystemBarrier)
	}
	if j.TargetStrategy != StrategyPassphrase {
		t.Fatalf("TargetStrategy = %q, want %q", j.TargetStrategy, StrategyPassphrase)
	}
	if j.Phase != PhasePending {
		t.Fatalf("Phase = %q, want %q", j.Phase, PhasePending)
	}
}

func TestMigrationPhase_Predicates(t *testing.T) {
	if !PhaseComplete.terminal() || !PhaseFailed.terminal() {
		t.Fatal("complete/failed must be terminal")
	}
	if PhaseCopying.terminal() || PhasePending.terminal() {
		t.Fatal("copying/pending must not be terminal")
	}
	if !PhaseCommitting.committed() || !PhaseComplete.committed() {
		t.Fatal("committing/complete must be committed")
	}
	if PhaseCopying.committed() || PhaseFailed.committed() {
		t.Fatal("copying/failed must not be committed")
	}
}
