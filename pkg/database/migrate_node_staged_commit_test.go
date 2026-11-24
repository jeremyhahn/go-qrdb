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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// failStagedDeleteBackend is a seal.StorageBackend whose Delete of a key
// under prefix fails with errInjected while fail is set; every other call
// reaches the wrapped backend unchanged.
type failStagedDeleteBackend struct {
	seal.StorageBackend
	prefix  string
	fail    atomic.Bool
	getKey  string
	failGet atomic.Bool
}

func (b *failStagedDeleteBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if b.failGet.Load() && key == b.getKey {
		return nil, errInjected
	}
	return b.StorageBackend.Get(ctx, key)
}

func (b *failStagedDeleteBackend) Delete(ctx context.Context, key string) error {
	if b.fail.Load() && b.prefix != "" && strings.HasPrefix(key, b.prefix) {
		return errInjected
	}
	return b.StorageBackend.Delete(ctx, key)
}

// runStagedDeleteFaultMigration runs a full records migration whose
// staged-copy delete fails, returning the raw backend, the fault wrapper and
// the migration's error.
func runStagedDeleteFaultMigration(t *testing.T) (seal.StorageBackend, *failStagedDeleteBackend, seal.StagedRecords, error) {
	t.Helper()
	ctx := context.Background()
	raw := seal.NewMemoryBackend()
	fb := &failStagedDeleteBackend{StorageBackend: raw}
	source := initNoneBarrierBackend(t, fb, "root-key")
	for k, v := range recoveryHomeRecords {
		if err := source.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	creds := seal.MustCredentials(ctx, []byte(recoveryRecordsPassphrase))
	target, err := source.MintKeyedSibling(ctx, ".migrating", seal.NewDeferredSoftwareStrategy(), creds)
	if err != nil {
		t.Fatalf("MintKeyedSibling: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	rec := seal.StagedRecords{Prefix: "__home/", StagingPrefix: target.RootKeyPath() + "/records/"}
	fb.prefix = rec.StagingPrefix
	fb.fail.Store(true)

	m := &NodeStoreMigration{Source: source, Target: target, Records: []seal.StagedRecords{rec}}
	runErr := m.Run(ctx, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    creds,
	})
	return raw, fb, rec, runErr
}

// TestNodeStoreMigration_StagedDeleteFault_RecoveryCompletesBeforeServing
// injects a failure on the staged-record delete past the commit point: the
// migration reports ErrMigrationRecordsPending with the committed journal
// kept, an in-process finish under the same fault refuses the same way, and
// the boot's recovery then completes the commit (staged copies gone) so a
// write made after it survives a second recovery pass.
func TestNodeStoreMigration_StagedDeleteFault_RecoveryCompletesBeforeServing(t *testing.T) {
	ctx := context.Background()
	raw, fb, rec, runErr := runStagedDeleteFaultMigration(t)
	if !errors.Is(runErr, ErrMigrationRecordsPending) || !errors.Is(runErr, errInjected) {
		t.Fatalf("Run with a failing staged delete = %v, want ErrMigrationRecordsPending wrapping the fault", runErr)
	}
	j, err := seal.LoadMigrationJournalFrom(ctx, raw, "root-key")
	if err != nil || j == nil || !j.Committed() {
		t.Fatalf("journal after the failed commit = %+v, %v; want the committed journal kept", j, err)
	}

	keyedFaulty, err := seal.NewStorageBarrier(reencTestLogger(), fb,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if ferr := FinishCommittedMigration(ctx, keyedFaulty, nil); !errors.Is(ferr, ErrMigrationRecordsPending) {
		t.Fatalf("FinishCommittedMigration under the fault = %v, want ErrMigrationRecordsPending", ferr)
	}
	if j, _ := seal.LoadMigrationJournalFrom(ctx, raw, "root-key"); j == nil {
		t.Fatal("a failed finish must keep the journal for the boot's recovery")
	}

	// Reopen: the fault is gone and the boot's recovery runs first.
	fb.fail.Store(false)
	if err := RecoverInterruptedMigration(ctx, raw, "root-key", nil); err != nil {
		t.Fatalf("RecoverInterruptedMigration: %v", err)
	}
	if staged, _ := raw.List(ctx, rec.StagingPrefix); len(staged) != 0 {
		t.Fatalf("staging keyspace holds %d records after recovery, want 0", len(staged))
	}
	creds := func() seal.Credentials { return seal.MustCredentials(ctx, []byte(recoveryRecordsPassphrase)) }
	keyed := reopenRecoveredBarrier(t, raw, seal.NewDeferredSoftwareStrategy(), creds())
	for k, v := range recoveryHomeRecords {
		got, gerr := keyed.Get(ctx, k)
		if gerr != nil || string(got) != v {
			t.Fatalf("Get(%q) under the adopted key = %q, %v; want %q", k, got, gerr, v)
		}
	}
	const updated = "__home/ca/root"
	if err := keyed.Put(ctx, updated, []byte("rotated-after-recovery")); err != nil {
		t.Fatalf("Put after recovery: %v", err)
	}

	// A second boot: nothing staged is left to overwrite the update.
	if err := RecoverInterruptedMigration(ctx, raw, "root-key", nil); err != nil {
		t.Fatalf("second RecoverInterruptedMigration: %v", err)
	}
	again := reopenRecoveredBarrier(t, raw, seal.NewDeferredSoftwareStrategy(), creds())
	if got, gerr := again.Get(ctx, updated); gerr != nil || string(got) != "rotated-after-recovery" {
		t.Fatalf("Get(%q) after a second reopen = %q, %v; want the post-recovery update", updated, got, gerr)
	}
}

// TestFinishCommittedMigration_CompletesInProcess shows the in-process path
// the node's Reopen takes once the fault clears: the staged records commit,
// retire runs, and the journal is removed.
func TestFinishCommittedMigration_CompletesInProcess(t *testing.T) {
	ctx := context.Background()
	raw, fb, rec, runErr := runStagedDeleteFaultMigration(t)
	if !errors.Is(runErr, ErrMigrationRecordsPending) {
		t.Fatalf("Run = %v, want ErrMigrationRecordsPending", runErr)
	}
	fb.fail.Store(false)
	b, err := seal.NewStorageBarrier(reencTestLogger(), fb,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	retired := false
	if err := FinishCommittedMigration(ctx, b, func(context.Context) error { retired = true; return nil }); err != nil {
		t.Fatalf("FinishCommittedMigration: %v", err)
	}
	if !retired {
		t.Fatal("FinishCommittedMigration did not retire the replaced plaintext")
	}
	if staged, _ := raw.List(ctx, rec.StagingPrefix); len(staged) != 0 {
		t.Fatalf("staging keyspace holds %d records, want 0", len(staged))
	}
	if j, _ := seal.LoadMigrationJournalFrom(ctx, raw, "root-key"); j != nil {
		t.Fatal("journal must be removed once the commit completes")
	}
	keyed := reopenRecoveredBarrier(t, raw, seal.NewDeferredSoftwareStrategy(),
		seal.MustCredentials(ctx, []byte(recoveryRecordsPassphrase)))
	for k, v := range recoveryHomeRecords {
		if got, gerr := keyed.Get(ctx, k); gerr != nil || string(got) != v {
			t.Fatalf("Get(%q) = %q, %v; want %q", k, got, gerr, v)
		}
	}

	// No journal (or one before the commit point) is a no-op.
	if err := FinishCommittedMigration(ctx, b, func(context.Context) error { return errInjected }); err != nil {
		t.Fatalf("FinishCommittedMigration with no journal = %v, want nil", err)
	}
}

// TestFinishCommittedMigration_JournalReadFault_FailsClosed pins that a
// journal that cannot be read is ErrMigrationRecordsPending: nothing proves
// the staged records committed, so the caller must not serve.
func TestFinishCommittedMigration_JournalReadFault_FailsClosed(t *testing.T) {
	ctx := context.Background()
	raw, fb, _, runErr := runStagedDeleteFaultMigration(t)
	if !errors.Is(runErr, ErrMigrationRecordsPending) {
		t.Fatalf("Run = %v, want ErrMigrationRecordsPending", runErr)
	}
	keyedFaulty, err := seal.NewStorageBarrier(reencTestLogger(), fb,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	fb.getKey = "root-key.migration"
	fb.failGet.Store(true)
	ferr := FinishCommittedMigration(ctx, keyedFaulty, nil)
	if !errors.Is(ferr, ErrMigrationRecordsPending) || !errors.Is(ferr, errInjected) {
		t.Fatalf("FinishCommittedMigration with an unreadable journal = %v, want ErrMigrationRecordsPending wrapping the fault", ferr)
	}
	if errors.Is(ferr, ErrMigrationRetirePending) {
		t.Fatalf("FinishCommittedMigration with an unreadable journal = %v, must not be ErrMigrationRetirePending", ferr)
	}
	fb.failGet.Store(false)
	if j, err := seal.LoadMigrationJournalFrom(ctx, raw, "root-key"); err != nil || j == nil || !j.Committed() {
		t.Fatalf("journal after the read fault = %+v, %v; want the committed journal kept", j, err)
	}
}

// TestFinishCommittedMigration_RetireFault_IsRetirePending pins that a
// retirement failure after the records commit is ErrMigrationRetirePending,
// never ErrMigrationRecordsPending, with the staging keyspace empty.
func TestFinishCommittedMigration_RetireFault_IsRetirePending(t *testing.T) {
	ctx := context.Background()
	raw, fb, rec, runErr := runStagedDeleteFaultMigration(t)
	if !errors.Is(runErr, ErrMigrationRecordsPending) {
		t.Fatalf("Run = %v, want ErrMigrationRecordsPending", runErr)
	}
	fb.fail.Store(false)
	b, err := seal.NewStorageBarrier(reencTestLogger(), fb,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	ferr := FinishCommittedMigration(ctx, b, func(context.Context) error { return errInjected })
	if !errors.Is(ferr, ErrMigrationRetirePending) || !errors.Is(ferr, errInjected) {
		t.Fatalf("FinishCommittedMigration with a failing retire = %v, want ErrMigrationRetirePending wrapping the fault", ferr)
	}
	if errors.Is(ferr, ErrMigrationRecordsPending) {
		t.Fatalf("FinishCommittedMigration with a failing retire = %v, must not be ErrMigrationRecordsPending", ferr)
	}
	if staged, _ := raw.List(ctx, rec.StagingPrefix); len(staged) != 0 {
		t.Fatalf("staging keyspace holds %d records after the commit, want 0", len(staged))
	}
}
