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

// homeRecords is a standalone tenant's home keyspace as the migration sees
// it: IAM, CA and DEK records under one prefix.
var homeRecords = map[string]string{
	"__home/iam/users/alice": "alice-record",
	"__home/ca/root":         "ca-material",
	"__home/dek/keyring":     "dek-keyring",
}

// testStagedRecords is the keyspace pair the tests stage.
var testStagedRecords = StagedRecords{Prefix: "__home/", StagingPrefix: "root-key.migrating/records/"}

// newRecordsMigration returns an unsealed none barrier holding homeRecords
// and one record outside the home keyspace, a keyed sibling over its backend,
// and the backend.
func newRecordsMigration(t *testing.T) (*StorageBarrier, *StorageBarrier, StorageBackend) {
	t.Helper()
	ctx := context.Background()
	src, backend := newNoneBarrier(t)
	for k, v := range homeRecords {
		if err := src.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	if err := src.Put(ctx, "other/record", []byte("not-home")); err != nil {
		t.Fatalf("Put(other): %v", err)
	}
	target, err := src.MintKeyedSibling(ctx, ".migrating", NewDeferredSoftwareStrategy(), MustCredentials(ctx, []byte("pw")))
	if err != nil {
		t.Fatalf("MintKeyedSibling: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	return src, target, backend
}

func TestStageMigratedRecords_ReencryptsEveryHomeRecordUnderTarget(t *testing.T) {
	ctx := context.Background()
	src, target, backend := newRecordsMigration(t)

	n, err := src.StageMigratedRecords(ctx, target, testStagedRecords)
	if err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}
	if n != len(homeRecords) {
		t.Fatalf("staged %d records, want %d", n, len(homeRecords))
	}
	for k, v := range homeRecords {
		got, err := target.Get(ctx, testStagedRecords.StagingPrefix+k)
		if err != nil || string(got) != v {
			t.Fatalf("target.Get(staged %q) = %q, %v; want %q", k, got, err, v)
		}
		if _, err := src.Get(ctx, testStagedRecords.StagingPrefix+k); err == nil {
			t.Fatalf("staged %q decrypts under the source key; want the target key only", k)
		}
	}
	staged, _ := backend.List(ctx, testStagedRecords.StagingPrefix)
	if len(staged) != len(homeRecords) {
		t.Fatalf("staging keyspace holds %d records, want %d (other/record must not be staged)", len(staged), len(homeRecords))
	}
}

func TestStageMigratedRecords_DiscardsAPriorAttemptsLeftovers(t *testing.T) {
	ctx := context.Background()
	src, target, backend := newRecordsMigration(t)
	leftover := testStagedRecords.StagingPrefix + "__home/deleted-since"
	if err := backend.Put(ctx, leftover, []byte("stale")); err != nil {
		t.Fatalf("Put(leftover): %v", err)
	}

	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}
	if ok, _ := backend.Exists(ctx, leftover); ok {
		t.Fatal("a prior attempt's staged record survived a fresh staging pass")
	}
}

func TestStageMigratedRecords_SealedSourceFails(t *testing.T) {
	ctx := context.Background()
	src, target, _ := newRecordsMigration(t)
	_ = src.Seal()

	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); !errors.Is(err, ErrSealed) {
		t.Fatalf("StageMigratedRecords on a sealed source = %v, want ErrSealed", err)
	}
}

func TestVerifyMigratedRecords_AcceptsAFaithfulCopy(t *testing.T) {
	ctx := context.Background()
	src, target, _ := newRecordsMigration(t)
	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}
	if err := src.VerifyMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("VerifyMigratedRecords: %v", err)
	}
}

func TestVerifyMigratedRecords_RefusesAMissingCopy(t *testing.T) {
	ctx := context.Background()
	src, target, backend := newRecordsMigration(t)
	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}
	if err := backend.Delete(ctx, testStagedRecords.StagingPrefix+"__home/ca/root"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := src.VerifyMigratedRecords(ctx, target, testStagedRecords); !errors.Is(err, ErrMigrationRecordMismatch) {
		t.Fatalf("VerifyMigratedRecords with a missing copy = %v, want ErrMigrationRecordMismatch", err)
	}
}

func TestVerifyMigratedRecords_RefusesADifferentCopy(t *testing.T) {
	ctx := context.Background()
	src, target, _ := newRecordsMigration(t)
	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}
	if err := target.Put(ctx, testStagedRecords.StagingPrefix+"__home/dek/keyring", []byte("forged")); err != nil {
		t.Fatalf("Put(forged): %v", err)
	}
	if err := src.VerifyMigratedRecords(ctx, target, testStagedRecords); !errors.Is(err, ErrMigrationRecordMismatch) {
		t.Fatalf("VerifyMigratedRecords with a differing copy = %v, want ErrMigrationRecordMismatch", err)
	}
}

func TestVerifyMigratedRecords_RefusesACopyUnderTheWrongKey(t *testing.T) {
	ctx := context.Background()
	src, target, backend := newRecordsMigration(t)
	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}
	// The live ciphertext copied raw into the staging keyspace is still
	// sealed under the source key.
	blob, _ := backend.Get(ctx, "__home/iam/users/alice")
	if err := backend.Put(ctx, testStagedRecords.StagingPrefix+"__home/iam/users/alice", blob); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := src.VerifyMigratedRecords(ctx, target, testStagedRecords); !errors.Is(err, ErrMigrationRecordMismatch) {
		t.Fatalf("VerifyMigratedRecords with a source-key copy = %v, want ErrMigrationRecordMismatch", err)
	}
}

func TestCommitStagedRecords_LiveRecordsReadUnderTheTarget(t *testing.T) {
	ctx := context.Background()
	src, target, backend := newRecordsMigration(t)
	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}

	if err := src.CommitStagedRecords(ctx, []StagedRecords{testStagedRecords}); err != nil {
		t.Fatalf("CommitStagedRecords: %v", err)
	}
	for k, v := range homeRecords {
		got, err := target.Get(ctx, k)
		if err != nil || string(got) != v {
			t.Fatalf("target.Get(%q) after commit = %q, %v; want %q", k, got, err, v)
		}
	}
	if staged, _ := backend.List(ctx, testStagedRecords.StagingPrefix); len(staged) != 0 {
		t.Fatalf("staging keyspace holds %d records after commit, want 0", len(staged))
	}
	if got, err := src.Get(ctx, "other/record"); err != nil || string(got) != "not-home" {
		t.Fatalf("a record outside the keyspace changed: %q, %v", got, err)
	}
}

func TestCommitStagedRecordsFrom_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	src, target, backend := newRecordsMigration(t)
	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}
	recs := []StagedRecords{testStagedRecords}
	for i := 0; i < 2; i++ {
		if err := CommitStagedRecordsFrom(ctx, backend, recs); err != nil {
			t.Fatalf("CommitStagedRecordsFrom pass %d: %v", i, err)
		}
	}
	if got, err := target.Get(ctx, "__home/ca/root"); err != nil || string(got) != "ca-material" {
		t.Fatalf("target.Get after a repeated commit = %q, %v", got, err)
	}
}

func TestRollbackStagedRecords_LeavesTheLiveRecords(t *testing.T) {
	ctx := context.Background()
	src, target, backend := newRecordsMigration(t)
	if _, err := src.StageMigratedRecords(ctx, target, testStagedRecords); err != nil {
		t.Fatalf("StageMigratedRecords: %v", err)
	}

	if err := src.RollbackStagedRecords(ctx, []StagedRecords{testStagedRecords}); err != nil {
		t.Fatalf("RollbackStagedRecords: %v", err)
	}
	if staged, _ := backend.List(ctx, testStagedRecords.StagingPrefix); len(staged) != 0 {
		t.Fatalf("staging keyspace holds %d records after rollback, want 0", len(staged))
	}
	for k, v := range homeRecords {
		got, err := src.Get(ctx, k)
		if err != nil || string(got) != v {
			t.Fatalf("src.Get(%q) after rollback = %q, %v; want %q", k, got, err, v)
		}
	}
}

func TestRollbackStagedRecordsFrom_NothingStagedIsANoOp(t *testing.T) {
	backend := NewMemoryBackend()
	if err := RollbackStagedRecordsFrom(context.Background(), backend, []StagedRecords{testStagedRecords}); err != nil {
		t.Fatalf("RollbackStagedRecordsFrom on an empty backend: %v", err)
	}
	if err := RollbackStagedRecordsFrom(context.Background(), backend, nil); err != nil {
		t.Fatalf("RollbackStagedRecordsFrom with no keyspaces: %v", err)
	}
}
