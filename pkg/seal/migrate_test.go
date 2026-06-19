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
		Credentials:    Credentials{Secret: []byte("pw")},
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
	if err := keyed.Initialize(ctx, Credentials{Secret: []byte("pw")}); err != nil {
		t.Fatalf("Initialize keyed: %v", err)
	}
	good := MigrationOptions{TargetStrategy: StrategyShamir, Mechanism: MechanismSwap, Availability: AvailabilityQuiesced, ShamirThreshold: 2, ShamirTotal: 3}
	if _, err := keyed.beginMigrationJournal(ctx, good); !errors.Is(err, ErrMigrationSourceNotNone) {
		t.Fatalf("keyed-source begin = %v, want ErrMigrationSourceNotNone", err)
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
