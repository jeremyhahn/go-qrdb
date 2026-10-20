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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// recoveryHomeRecords are a standalone tenant's home records: IAM, CA and
// DEK material in the barrier's own backend.
var recoveryHomeRecords = map[string]string{
	"__home/iam/users/alice": "alice-record",
	"__home/ca/root":         "ca-material",
	"__home/dek/keyring":     "dek-keyring",
}

const recoveryRecordsPassphrase = "records-recovery-pw"

// stageHomeRecordsMigration seeds the home records under a none barrier,
// stages them under a keyed sibling, and leaves the journal at phase: the
// state a crash after the recode leaves behind. It returns the backend and
// the staged keyspace.
func stageHomeRecordsMigration(t *testing.T, phase seal.MigrationPhase) (seal.StorageBackend, seal.StagedRecords) {
	t.Helper()
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	source := initNoneBarrierBackend(t, backend, "root-key")
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
	j, err := source.BeginMigration(ctx, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    creds,
	})
	if err != nil {
		t.Fatalf("BeginMigration: %v", err)
	}
	rec := seal.StagedRecords{Prefix: "__home/", StagingPrefix: target.RootKeyPath() + "/records/"}
	j.StagingRootKeyPath = target.RootKeyPath()
	j.StagedRecords = []seal.StagedRecords{rec}
	if _, err := source.StageMigratedRecords(ctx, target, rec); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}
	j.Phase = phase
	if err := source.CheckpointMigration(ctx, j); err != nil {
		t.Fatalf("CheckpointMigration: %v", err)
	}
	return backend, rec
}

// reopenRecoveredBarrier unseals the canonical root key with strategy.
func reopenRecoveredBarrier(t *testing.T, backend seal.StorageBackend, strategy seal.SealingStrategy, creds seal.Credentials) *seal.StorageBarrier {
	t.Helper()
	b, err := seal.NewStorageBarrier(reencTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	return b
}

func TestRecoverInterruptedMigration_StagedRecords_RollForward(t *testing.T) {
	ctx := context.Background()
	backend, rec := stageHomeRecordsMigration(t, seal.PhaseCommitting)
	// The crash struck mid-commit: one record's staged copy is already over
	// its live key, the others are not.
	const done = "__home/ca/root"
	blob, err := backend.Get(ctx, rec.StagingPrefix+done)
	if err != nil {
		t.Fatalf("Get(staged): %v", err)
	}
	if err := backend.Put(ctx, done, blob); err != nil {
		t.Fatalf("Put(live): %v", err)
	}
	if err := backend.Delete(ctx, rec.StagingPrefix+done); err != nil {
		t.Fatalf("Delete(staged): %v", err)
	}

	if err := RecoverInterruptedMigration(ctx, backend, "root-key", nil); err != nil {
		t.Fatalf("RecoverInterruptedMigration: %v", err)
	}

	keyed := reopenRecoveredBarrier(t, backend, seal.NewDeferredSoftwareStrategy(),
		seal.MustCredentials(ctx, []byte(recoveryRecordsPassphrase)))
	for k, v := range recoveryHomeRecords {
		got, err := keyed.Get(ctx, k)
		if err != nil || string(got) != v {
			t.Fatalf("Get(%q) under the adopted key = %q, %v; want %q", k, got, err, v)
		}
	}
	if staged, _ := backend.List(ctx, rec.StagingPrefix); len(staged) != 0 {
		t.Fatalf("staging keyspace holds %d records after roll-forward, want 0", len(staged))
	}
	if j, _ := seal.LoadMigrationJournalFrom(ctx, backend, "root-key"); j != nil {
		t.Fatal("journal must be cleared after roll-forward")
	}
}

func TestRecoverInterruptedMigration_StagedRecords_RollBack(t *testing.T) {
	ctx := context.Background()
	backend, rec := stageHomeRecordsMigration(t, seal.PhaseVerifying)

	if err := RecoverInterruptedMigration(ctx, backend, "root-key", nil); err != nil {
		t.Fatalf("RecoverInterruptedMigration: %v", err)
	}

	if staged, _ := backend.List(ctx, rec.StagingPrefix); len(staged) != 0 {
		t.Fatalf("staging keyspace holds %d records after roll-back, want 0", len(staged))
	}
	none := reopenRecoveredBarrier(t, backend, seal.NewNoneStrategy(), seal.Credentials{})
	for k, v := range recoveryHomeRecords {
		got, err := none.Get(ctx, k)
		if err != nil || string(got) != v {
			t.Fatalf("Get(%q) under the original key = %q, %v; want %q", k, got, err, v)
		}
	}
}
