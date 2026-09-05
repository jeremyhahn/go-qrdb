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

// go-qrdb#268: this package uses rename as a commit point in three places --
// the one-time layout migration, the node-encryption directory swap, and the
// staged-snapshot commit. Each one means "this step is done", and none of them
// were, until the containing directory was fsynced.
//
// Every test here observes the OPERATION rather than the result. The bytes and
// names on disk are identical whether or not the directory was synced, so the
// only way to assert the property is to fail the sync at its boundary and
// require the caller to surface it. An ordinary test of the post-state passes
// with the fix and without it.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// failSyncDir makes every directory fsync fail with the returned sentinel, and
// records the directories the code under test asked to sync.
func failSyncDir(t *testing.T) (sentinel error, synced *[]string) {
	t.Helper()
	sentinel = errors.New("directory sync refused")
	var seen []string
	t.Cleanup(fsync.SetForTest(func(dir string) error {
		seen = append(seen, dir)
		return sentinel
	}))
	return sentinel, &seen
}

// countSyncDir records the directories synced without failing them, for the
// paths that swallow errors and can only be observed by what they attempted.
func countSyncDir(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	t.Cleanup(fsync.SetForTest(func(dir string) error {
		seen = append(seen, dir)
		return nil
	}))
	return &seen
}

// seedPebbleDir builds the pre-migration flat layout MigrateIfNeeded expects.
func seedPebbleDir(t *testing.T, dataDir string) {
	t.Helper()
	pebbleDir := filepath.Join(dataDir, "pebble")
	if err := os.MkdirAll(pebbleDir, 0o750); err != nil {
		t.Fatalf("seed pebble dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pebbleDir, "CURRENT"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed pebble contents: %v", err)
	}
}

// TestMigrateIfNeeded_DoesNotReportSuccessBeforeTheMoveIsDurable.
//
// The move of pebble/ into databases/default/data IS the migration. It is
// guarded by "databases/ exists => already migrated", so a crash that keeps
// the destination's new entry while losing the source's removal leaves the
// data reachable under two names and the re-run returning early -- the old
// layout sitting beside the new one, both live.
func TestMigrateIfNeeded_DoesNotReportSuccessBeforeTheMoveIsDurable(t *testing.T) {
	dataDir := t.TempDir()
	seedPebbleDir(t, dataDir)
	sentinel, synced := failSyncDir(t)

	err := MigrateIfNeeded(newTestLogger(), dataDir)
	if !errors.Is(err, sentinel) {
		t.Fatalf("MigrateIfNeeded = %v; want the directory-sync failure. Reporting the "+
			"migration complete before its directory entries are durable lets a crash "+
			"recover the data under the old name, the new name, or neither", err)
	}
	if len(*synced) == 0 {
		t.Error("no directory sync was attempted; the move is not a commit point without one")
	}
}

// TestMigrateIfNeeded_SyncsBothSidesOfTheCrossDirectoryMove: the move crosses
// parents, so the source directory (losing "pebble") and the destination
// (gaining "data") are different directories and BOTH need syncing.
func TestMigrateIfNeeded_SyncsBothSidesOfTheCrossDirectoryMove(t *testing.T) {
	dataDir := t.TempDir()
	seedPebbleDir(t, dataDir)
	synced := countSyncDir(t)

	if err := MigrateIfNeeded(newTestLogger(), dataDir); err != nil {
		t.Fatalf("MigrateIfNeeded: %v", err)
	}

	wantDst := filepath.Join(dataDir, databasesDir, "default")
	got := map[string]bool{}
	for _, d := range *synced {
		got[d] = true
	}
	if !got[dataDir] {
		t.Errorf("source directory %s never synced (saw %v); the removal of the old "+
			"pebble/ entry is undurable, so a crash can recover it alongside the new layout",
			dataDir, *synced)
	}
	if !got[wantDst] {
		t.Errorf("destination directory %s never synced (saw %v)", wantDst, *synced)
	}
}

// TestSwapStoreDir_DoesNotReportSuccessBeforeTheSwapIsDurable.
//
// The swap is two renames with an intermediate state in which NO directory
// exists at dir. Recovery reads the post-crash names to decide whether to
// roll forward or restore the backup, so those names surviving the crash in
// the order they were written is the entire basis of that decision.
func TestSwapStoreDir_DoesNotReportSuccessBeforeTheSwapIsDurable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	for _, d := range []string{dir, dir + keyedSuffix} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("seed %s: %v", d, err)
		}
	}
	sentinel, synced := failSyncDir(t)

	if err := SwapStoreDir(dir); !errors.Is(err, sentinel) {
		t.Fatalf("SwapStoreDir = %v; want the directory-sync failure. A swap that reports "+
			"done before its entries are durable can be recovered mid-swap, with the "+
			"original moved aside and the keyed copy not yet in place", err)
	}
	if len(*synced) == 0 {
		t.Error("no directory sync was attempted during the swap")
	}
}

// TestSwapStoreDir_SyncsAfterBothRenames: the move-aside and the move-in are
// separately durable. Syncing only at the end would leave the intermediate
// state -- the one with no directory at dir -- reachable after a crash with no
// record of how it got there.
func TestSwapStoreDir_SyncsAfterBothRenames(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	for _, d := range []string{dir, dir + keyedSuffix} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("seed %s: %v", d, err)
		}
	}
	synced := countSyncDir(t)

	if err := SwapStoreDir(dir); err != nil {
		t.Fatalf("SwapStoreDir: %v", err)
	}
	// Both renames stay within root, so each contributes one sync of root.
	n := 0
	for _, d := range *synced {
		if d == root {
			n++
		}
	}
	if n != 2 {
		t.Errorf("root synced %d times (saw %v); want 2 — one per rename. A single sync "+
			"leaves the first rename undurable across the second", n, *synced)
	}
}

// TestCommitStagedSnapshots_DoesNotReportSuccessBeforeTheCommitIsDurable.
//
// This rename runs PAST the migration's point of no return: the store dirs are
// already keyed to the destination barrier. If its directory entry is lost, the
// source-encrypted original comes back under FinalPath -- unreadable to the
// keyed node -- and the idempotence guard then reads the absent KeyedPath as
// "already committed" and skips it on every retry.
func TestCommitStagedSnapshots_DoesNotReportSuccessBeforeTheCommitIsDurable(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "snapshot.dat")
	keyed := filepath.Join(dir, "snapshot.dat.keyed")
	for _, p := range []string{final, keyed} {
		if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	sentinel, synced := failSyncDir(t)

	staged := []StagedSnapshot{{FinalPath: final, KeyedPath: keyed}}
	err := CommitStagedSnapshots(staged)
	if !errors.Is(err, sentinel) {
		t.Fatalf("CommitStagedSnapshots = %v; want the directory-sync failure. Reporting "+
			"the keyed snapshot committed before its directory entry is durable leaves a "+
			"node keyed to one barrier reading a snapshot sealed under another", err)
	}
	var stageErr *SnapshotStageError
	if !errors.As(err, &stageErr) {
		t.Errorf("error = %T; want it wrapped as *SnapshotStageError so the caller still "+
			"learns which snapshot failed", err)
	}
	if len(*synced) != 1 || (*synced)[0] != dir {
		t.Errorf("synced dirs = %v; want exactly [%s]", *synced, dir)
	}
}

// TestCommitStagedSnapshotsWithRetry_SurfacesTheDurabilityFailure: the retry
// wrapper rolls forward past transient errors, and a directory-sync failure
// must not be swallowed by that policy — it is the one error that means the
// commit did not happen.
func TestCommitStagedSnapshotsWithRetry_SurfacesTheDurabilityFailure(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "snapshot.dat")
	keyed := filepath.Join(dir, "snapshot.dat.keyed")
	for _, p := range []string{final, keyed} {
		if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	sentinel, _ := failSyncDir(t)

	staged := []StagedSnapshot{{FinalPath: final, KeyedPath: keyed}}
	err := CommitStagedSnapshotsWithRetry(context.Background(), staged, 2, 0)
	if !errors.Is(err, sentinel) {
		t.Fatalf("CommitStagedSnapshotsWithRetry = %v; want the durability failure to "+
			"survive the retry policy", err)
	}
}

// TestRecoverInterruptedMigration_RollbackRestoreIsDurable.
//
// The rollback path discards its errors by design (it runs on a failure path
// and must not fail further), so the property cannot be observed in a return
// value. It is observed as an attempt instead: the restore of dir.premigrate
// over dir must ask for a directory sync, because a restore whose entry is
// lost leaves the node with neither the original nor the backup.
func TestRecoverInterruptedMigration_RollbackRestoreIsDurable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	if err := os.MkdirAll(dir+backupSuffix, 0o750); err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	synced := countSyncDir(t)

	ctx := context.Background()
	mem := seal.NewMemoryBackend()
	jblob, err := json.Marshal(&seal.MigrationJournal{
		Phase: seal.PhaseCopying, TargetStrategy: seal.StrategyPassphrase,
	})
	if err != nil {
		t.Fatalf("marshal journal: %v", err)
	}
	if err := mem.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if err := RecoverInterruptedMigration(ctx, mem, "root-key", []string{dir}); err != nil {
		t.Fatalf("RecoverInterruptedMigration: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("backup was not restored over dir: %v", err)
	}
	found := false
	for _, d := range *synced {
		if d == root {
			found = true
		}
	}
	if !found {
		t.Errorf("the rollback restore never synced %s (saw %v). It is best-effort about "+
			"ERRORS, not about durability: a restore whose directory entry is lost leaves "+
			"the node with neither the original nor the backup", root, *synced)
	}
}
