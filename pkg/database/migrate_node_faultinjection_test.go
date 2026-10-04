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
	"errors"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// errInjected is the sentinel returned by the fault-injecting journal so tests
// can assert it propagates verbatim out of Run.
var errInjected = errors.New("injected journal fault")

// faultJournal embeds a real *seal.StorageBarrier so every non-overridden method
// (and atrest.New, which requires the concrete type via the migration's Source
// field) keeps delegating to the live barrier. Each journal operation Run drives
// can be made to fail on demand, exercising Run's defensive error branches that
// the real barrier never reaches on a happy path.
type faultJournal struct {
	*seal.StorageBarrier

	failBegin  error
	failFinish error
	failAdopt  error

	// CheckpointMigration fails with failCheckErr on the (failCheckSkip+1)th call
	// whose journal phase equals failCheckAt; earlier matches and other phases
	// delegate to the real barrier.
	failCheckAt   seal.MigrationPhase
	failCheckErr  error
	failCheckSkip int
	checkSeen     int
}

func (f *faultJournal) BeginMigration(ctx context.Context, opts seal.MigrationOptions) (*seal.MigrationJournal, error) {
	if f.failBegin != nil {
		return nil, f.failBegin
	}
	return f.StorageBarrier.BeginMigration(ctx, opts)
}

func (f *faultJournal) CheckpointMigration(ctx context.Context, j *seal.MigrationJournal) error {
	if f.failCheckErr != nil && j.Phase == f.failCheckAt {
		if f.checkSeen == f.failCheckSkip {
			f.checkSeen++
			return f.failCheckErr
		}
		f.checkSeen++
	}
	return f.StorageBarrier.CheckpointMigration(ctx, j)
}

func (f *faultJournal) FinishMigration(ctx context.Context) error {
	if f.failFinish != nil {
		return f.failFinish
	}
	return f.StorageBarrier.FinishMigration(ctx)
}

func (f *faultJournal) AdoptMigratedRootKey(ctx context.Context, target *seal.StorageBarrier) error {
	if f.failAdopt != nil {
		return f.failAdopt
	}
	return f.StorageBarrier.AdoptMigratedRootKey(ctx, target)
}

// faultMigration wires a fault-injecting journal over two freshly-written none
// stores and returns the migration plus the pieces needed to assert the
// post-Run filesystem and root-key state.
type faultMigration struct {
	mig         *NodeStoreMigration
	backend     seal.StorageBackend
	rootKeyPath string
	dirs        []string
}

func newFaultMigration(t *testing.T, fake *faultJournal) faultMigration {
	t.Helper()
	const passphrase = "fault-injection-pass"
	const rootKeyPath = "root-key"

	backend := seal.NewMemoryBackend()
	source := initNoneBarrier(t, backend, rootKeyPath)
	target := initKeyedBarrier(t, backend, rootKeyPath+".migrating", passphrase)

	fake.StorageBarrier = source

	noneEnc := atrest.New(source)
	dirA := t.TempDir() + "/pebble"
	dirB := t.TempDir() + "/vector"
	writeStore(t, dirA, noneEnc, map[string]string{"a/1": "alpha", "a/2": "alpha2"})
	writeStore(t, dirB, noneEnc, map[string]string{"b/1": "bravo", "b/2": "bravo2"})

	return faultMigration{
		mig: &NodeStoreMigration{
			Source:    source,
			Target:    target,
			StoreDirs: []string{dirA, dirB},
			Logger:    reencTestLogger(),
			journal:   fake,
		},
		backend:     backend,
		rootKeyPath: rootKeyPath,
		dirs:        []string{dirA, dirB},
	}
}

func (fm faultMigration) run(t *testing.T) error {
	t.Helper()
	return fm.mig.Run(context.Background(), seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("fault-injection-pass")),
	})
}

func (fm faultMigration) keyedDirsExist() bool {
	for _, dir := range fm.dirs {
		if _, err := os.Stat(dir + keyedSuffix); err == nil {
			return true
		}
	}
	return false
}

func (fm faultMigration) anyBackupExists() bool {
	for _, dir := range fm.dirs {
		if _, err := os.Stat(dir + backupSuffix); err == nil {
			return true
		}
	}
	return false
}

func (fm faultMigration) persistedStrategy() seal.StrategyID {
	return persistedStrategy(context.Background(), fm.backend, fm.rootKeyPath)
}

// TestNodeStoreMigration_InvalidMechanism covers Run's mechanism dispatch
// default branch: an unrecognized mechanism is rejected before any work.
func TestNodeStoreMigration_InvalidMechanism(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{})
	err := fm.mig.Run(context.Background(), seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MigrationMechanism("bogus"),
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("fault-injection-pass")),
	})
	if !errors.Is(err, seal.ErrMigrationInvalidMechanism) {
		t.Fatalf("Run err = %v, want ErrMigrationInvalidMechanism", err)
	}
	if fm.keyedDirsExist() {
		t.Fatal("no staging work should occur for an invalid mechanism")
	}
}

// TestNodeStoreMigration_BeginMigrationFault: BeginMigration fails before the
// rollback defer is registered and before any staging copy is made. Run returns
// the error and leaves the source none with no staging artifacts.
func TestNodeStoreMigration_BeginMigrationFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{failBegin: errInjected})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if fm.keyedDirsExist() {
		t.Fatal("no keyed staging dir should exist after a BeginMigration fault")
	}
	if got := fm.persistedStrategy(); got != seal.StrategyNone {
		t.Fatalf("source strategy = %q, want none", got)
	}
}

// TestNodeStoreMigration_CheckpointPendingFault: the very first checkpoint (which
// records the staging root-key path, at PhasePending) fails before the rollback
// defer is registered. No staging copy exists yet.
func TestNodeStoreMigration_CheckpointPendingFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{failCheckAt: seal.PhasePending, failCheckErr: errInjected})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if fm.keyedDirsExist() {
		t.Fatal("no keyed staging dir should exist after a PhasePending checkpoint fault")
	}
	if got := fm.persistedStrategy(); got != seal.StrategyNone {
		t.Fatalf("source strategy = %q, want none", got)
	}
}

// TestNodeStoreMigration_CheckpointCopyingFault: the first copy-phase checkpoint
// (before any store is copied) fails; the rollback defer is now registered, so
// rollback runs. No keyed dirs were created yet.
func TestNodeStoreMigration_CheckpointCopyingFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{failCheckAt: seal.PhaseCopying, failCheckErr: errInjected})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if fm.keyedDirsExist() {
		t.Fatal("rollback should have removed keyed staging dirs")
	}
	if got := fm.persistedStrategy(); got != seal.StrategyNone {
		t.Fatalf("source strategy = %q, want none", got)
	}
}

// TestNodeStoreMigration_CheckpointAfterStoreCopiedFault: the per-store checkpoint
// taken right after the first store is re-encoded (still PhaseCopying, second
// match) fails. A keyed staging dir now exists; rollback must remove it.
func TestNodeStoreMigration_CheckpointAfterStoreCopiedFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{
		failCheckAt:   seal.PhaseCopying,
		failCheckErr:  errInjected,
		failCheckSkip: 1,
	})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if fm.keyedDirsExist() {
		t.Fatal("rollback should have removed the keyed staging dir created during copy")
	}
	if got := fm.persistedStrategy(); got != seal.StrategyNone {
		t.Fatalf("source strategy = %q, want none", got)
	}
}

// TestNodeStoreMigration_CheckpointVerifyingFault: the verify-phase checkpoint
// fails after both stores have been copied. Rollback runs and removes every keyed
// staging dir; the originals are untouched (no swap occurred).
func TestNodeStoreMigration_CheckpointVerifyingFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{failCheckAt: seal.PhaseVerifying, failCheckErr: errInjected})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if fm.keyedDirsExist() {
		t.Fatal("rollback should have removed all keyed staging dirs")
	}
	if fm.anyBackupExists() {
		t.Fatal("no swap occurred, so no backup dir should exist")
	}
	if got := fm.persistedStrategy(); got != seal.StrategyNone {
		t.Fatalf("source strategy = %q, want none", got)
	}
}

// TestNodeStoreMigration_CheckpointCommittingFault: the commit-phase checkpoint
// (PhaseCommitting, taken before the swaps) fails. The journal is already in a
// committed phase, so the rollback defer does NOT fire — staging copies remain
// and the source root key is still none (adopt never ran).
func TestNodeStoreMigration_CheckpointCommittingFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{failCheckAt: seal.PhaseCommitting, failCheckErr: errInjected})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if !fm.keyedDirsExist() {
		t.Fatal("committed-phase failure must NOT roll back: keyed dirs should remain")
	}
	if got := fm.persistedStrategy(); got != seal.StrategyNone {
		t.Fatalf("source strategy = %q, want none (adopt never ran)", got)
	}
}

// TestNodeStoreMigration_AdoptMigratedRootKeyFault: AdoptMigratedRootKey fails
// after the directory swaps but before the canonical root key is updated. This is
// the post-swap commit window: no rollback, keyed data is in place with backups
// retained, and the canonical root key is still none (recovery would roll
// forward).
func TestNodeStoreMigration_AdoptMigratedRootKeyFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{failAdopt: errInjected})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if !fm.anyBackupExists() {
		t.Fatal("swaps ran, so a pre-migration backup dir should exist")
	}
	if fm.keyedDirsExist() {
		t.Fatal("keyed copies were swapped into place, so the .keyed staging dirs are gone")
	}
	if got := fm.persistedStrategy(); got != seal.StrategyNone {
		t.Fatalf("source strategy = %q, want none (adopt failed before persisting)", got)
	}
}

// TestNodeStoreMigration_CheckpointCompleteFault: the final PhaseComplete
// checkpoint fails after the migration is fully committed (swaps done, root key
// adopted, backups removed). No rollback; the source is now keyed.
func TestNodeStoreMigration_CheckpointCompleteFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{failCheckAt: seal.PhaseComplete, failCheckErr: errInjected})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if got := fm.persistedStrategy(); got != seal.StrategyPassphrase {
		t.Fatalf("source strategy = %q, want passphrase (commit succeeded)", got)
	}
}

// TestNodeStoreMigration_FinishMigrationFault: FinishMigration fails at the very
// end, after the migration is fully committed. Run returns the error but the
// commit stands (source is keyed).
func TestNodeStoreMigration_FinishMigrationFault(t *testing.T) {
	fm := newFaultMigration(t, &faultJournal{failFinish: errInjected})
	err := fm.run(t)
	if !errors.Is(err, errInjected) {
		t.Fatalf("Run err = %v, want %v", err, errInjected)
	}
	if got := fm.persistedStrategy(); got != seal.StrategyPassphrase {
		t.Fatalf("source strategy = %q, want passphrase (commit succeeded)", got)
	}
}
