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
	"encoding/json"
	"log/slog"
	"os"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// keyedSuffix / backupSuffix name the transient staging directories created
// during a swap migration. A re-encrypted copy is built at <dir><keyedSuffix>,
// then on commit the original is moved to <dir><backupSuffix> and the keyed
// copy renamed into place; the backup is removed only after the commit fully
// succeeds.
const (
	keyedSuffix  = ".keyed"
	backupSuffix = ".premigrate"
)

// MigrateStoreDirError wraps a per-store-directory failure during a node
// encryption migration, naming the directory at fault.
type MigrateStoreDirError struct {
	Dir string
	Err error
}

func (e *MigrateStoreDirError) Error() string {
	return "database: migrate node encryption: store " + e.Dir + ": " + e.Err.Error()
}

func (e *MigrateStoreDirError) Unwrap() error { return e.Err }

// NodeStoreMigration drives an at-rest encryption migration for a single node's
// data stores, using the swap mechanism: each store is logically copied into a
// freshly-keyed sibling, verified, and atomically swapped into place; the
// barrier's root key is then transitioned to the keyed strategy.
//
// The caller is responsible for QUIESCING the node first: every store directory
// in StoreDirs must be CLOSED before Run is invoked (this is the quiesced
// availability mode — a maintenance window). The caller reopens the stores
// under the keyed barrier afterwards (typically by restarting the node).
//
// Source is the live none-strategy barrier (owner of the journal and the
// canonical root-key path). Target is a freshly-minted keyed barrier
// (initialized + unsealed over a staging root-key path in the same seal store);
// its Encryptor encrypts the migrated data and its sealed root key is adopted
// at commit.
type NodeStoreMigration struct {
	Source    *seal.StorageBarrier
	Target    *seal.StorageBarrier
	StoreDirs []string
	Logger    *slog.Logger
}

// Run executes the migration end to end with crash-safe journalling. On
// success every store directory holds keyed (encrypted) data and the barrier's
// persisted root key is the keyed one. On failure before the commit point the
// staging copies are removed and the originals are untouched.
func (m *NodeStoreMigration) Run(ctx context.Context, opts seal.MigrationOptions) (err error) {
	// Select the per-store recodec by mechanism. Swap performs a logical
	// key/value copy (Pebble rebuilds its layout); in-place performs a raw,
	// structure-preserving file-by-file recodec. Both write to a keyed sibling
	// directory and share the crash-safe verify + atomic-swap commit below.
	var recode func(context.Context, string, *atrest.Encryptor, string, *atrest.Encryptor) (int64, error)
	switch opts.Mechanism {
	case seal.MechanismSwap:
		recode = ReencryptPebbleStore
	case seal.MechanismInPlace:
		recode = ReencryptStoreFiles
	default:
		return seal.ErrMigrationInvalidMechanism
	}
	src := atrest.New(m.Source)
	dst := atrest.New(m.Target)

	j, err := m.Source.BeginMigration(ctx, opts)
	if err != nil {
		return err
	}
	// Record the keyed target's root-key path so crash recovery can adopt its
	// sealed blob if the process dies mid-commit (after the data dirs are
	// swapped but before the canonical root key is updated).
	if m.Target != nil {
		j.StagingRootKeyPath = m.Target.RootKeyPath()
		if cerr := m.Source.CheckpointMigration(ctx, j); cerr != nil {
			err = cerr
			return err
		}
	}
	// Roll back staging copies if we fail before reaching the commit point.
	defer func() {
		if err != nil && !j.Committed() {
			m.rollback(ctx, j)
		}
	}()

	// --- Copy phase: re-encrypt each store into a keyed sibling. ----------
	j.Phase = seal.PhaseCopying
	if err = m.Source.CheckpointMigration(ctx, j); err != nil {
		return err
	}
	for _, dir := range m.StoreDirs {
		if j.Stores[dir] {
			continue // already copied (resume)
		}
		keyedDir := dir + keyedSuffix
		_ = os.RemoveAll(keyedDir) // clear any stale partial copy
		if _, cerr := recode(ctx, dir, src, keyedDir, dst); cerr != nil {
			err = &MigrateStoreDirError{Dir: dir, Err: cerr}
			return err
		}
		j.Stores[dir] = true
		if err = m.Source.CheckpointMigration(ctx, j); err != nil {
			return err
		}
	}

	// --- Verify phase: prove no data loss before the point of no return. --
	j.Phase = seal.PhaseVerifying
	if err = m.Source.CheckpointMigration(ctx, j); err != nil {
		return err
	}
	for _, dir := range m.StoreDirs {
		if verr := VerifyReencryptedStore(ctx, dir, src, dir+keyedSuffix, dst); verr != nil {
			err = &MigrateStoreDirError{Dir: dir, Err: verr}
			return err
		}
	}

	// --- Commit phase: point of no return; recovery rolls FORWARD. --------
	j.Phase = seal.PhaseCommitting
	if err = m.Source.CheckpointMigration(ctx, j); err != nil {
		return err
	}
	// 1. Swap each keyed copy into place, keeping a backup of the original.
	for _, dir := range m.StoreDirs {
		if serr := swapDir(dir); serr != nil {
			err = &MigrateStoreDirError{Dir: dir, Err: serr}
			return err
		}
	}
	// 2. Adopt the keyed root key as the canonical root key.
	if err = m.Source.AdoptMigratedRootKey(ctx, m.Target); err != nil {
		return err
	}
	// 3. Remove the pre-migration backups now that the commit is durable.
	for _, dir := range m.StoreDirs {
		_ = os.RemoveAll(dir + backupSuffix)
	}

	// --- Done. -----------------------------------------------------------
	j.Phase = seal.PhaseComplete
	if err = m.Source.CheckpointMigration(ctx, j); err != nil {
		return err
	}
	if err = m.Source.FinishMigration(ctx); err != nil {
		return err
	}
	if m.Logger != nil {
		m.Logger.Info("node encryption migration complete",
			slog.Int("stores", len(m.StoreDirs)),
			slog.String("strategy", string(opts.TargetStrategy)))
	}
	return nil
}

// swapDir atomically replaces dir with dir+keyedSuffix, moving the original to
// dir+backupSuffix. If a prior partial commit already moved the original away,
// the backup is preserved and only the rename-in is performed (idempotent for
// roll-forward recovery).
func swapDir(dir string) error {
	keyedDir := dir + keyedSuffix
	backupDir := dir + backupSuffix
	// Idempotent for recovery: if the keyed copy is absent the swap already
	// completed for this dir (dir holds the keyed data), so there is nothing to
	// do. Without this guard a second call would move the keyed dir aside with
	// no replacement, destroying it.
	if _, err := os.Stat(keyedDir); os.IsNotExist(err) {
		return nil
	}
	// Move original aside (if still present and not already backed up).
	if _, err := os.Stat(dir); err == nil {
		if _, berr := os.Stat(backupDir); berr == nil {
			// A backup already exists from a prior attempt; discard the stale
			// original so the rename target is free.
			if rerr := os.RemoveAll(dir); rerr != nil {
				return rerr
			}
		} else if rerr := os.Rename(dir, backupDir); rerr != nil {
			return rerr
		}
	}
	// Move the keyed copy into place.
	if _, err := os.Stat(keyedDir); err == nil {
		if rerr := os.Rename(keyedDir, dir); rerr != nil {
			return rerr
		}
	}
	return nil
}

// rollback removes staging copies and marks the journal failed. Best-effort:
// it never returns an error because it runs on a failure path.
func (m *NodeStoreMigration) rollback(ctx context.Context, j *MigrationJournalRef) {
	for _, dir := range m.StoreDirs {
		_ = os.RemoveAll(dir + keyedSuffix)
	}
	j.Phase = seal.PhaseFailed
	_ = m.Source.CheckpointMigration(ctx, j)
}

// MigrationJournalRef is a local alias so the rollback signature reads clearly;
// the journal type itself is owned by pkg/seal.
type MigrationJournalRef = seal.MigrationJournal

// RecoverInterruptedMigration reconciles a migration that was interrupted by a
// crash, using the persisted journal, BEFORE the data stores are opened. It is
// idempotent and credential-free (it only moves directories and the sealed
// root-key blob, never deriving keys), so it is safe to run on every boot.
//
//   - No journal: nothing to do.
//   - Committed (PhaseCommitting/Complete): ROLL FORWARD — finish any pending
//     directory swaps and ensure the canonical root key is the keyed one
//     (adopting the staging blob if the crash struck between the swaps and the
//     root-key update). This rescues the dangerous mid-commit window where the
//     data dirs are keyed but the root key is still "none" (which would
//     otherwise wedge the node).
//   - Not committed (pending/copying/verifying/failed): ROLL BACK — discard the
//     keyed staging copies and restore any pre-migration backups.
//
// In both terminal cases the journal and transient artifacts are removed.
func RecoverInterruptedMigration(ctx context.Context, backend seal.StorageBackend, canonicalRootKeyPath string, storeDirs []string) error {
	j, err := seal.LoadMigrationJournalFrom(ctx, backend, canonicalRootKeyPath)
	if err != nil {
		return err
	}
	if j == nil {
		return nil
	}

	if j.Committed() {
		// Roll forward: ensure every store dir holds its keyed copy.
		for _, dir := range storeDirs {
			if serr := swapDir(dir); serr != nil {
				return &MigrateStoreDirError{Dir: dir, Err: serr}
			}
		}
		// Ensure the canonical root key is the keyed one. If the crash struck
		// before the root-key update, the staging blob still holds it.
		if persisted := persistedStrategy(ctx, backend, canonicalRootKeyPath); persisted == seal.StrategyNone || persisted == "" {
			if j.StagingRootKeyPath != "" {
				if blob, gerr := backend.Get(ctx, j.StagingRootKeyPath); gerr == nil && len(blob) > 0 {
					if perr := backend.Put(ctx, canonicalRootKeyPath, blob); perr != nil {
						return perr
					}
				}
			}
		}
		// Cleanup: drop backups + staging blob + journal.
		for _, dir := range storeDirs {
			_ = os.RemoveAll(dir + backupSuffix)
		}
		if j.StagingRootKeyPath != "" {
			_ = backend.Delete(ctx, j.StagingRootKeyPath)
		}
		return seal.ClearMigrationJournalFrom(ctx, backend, canonicalRootKeyPath)
	}

	// Roll back: discard keyed staging copies, restore any swapped originals.
	for _, dir := range storeDirs {
		_ = os.RemoveAll(dir + keyedSuffix)
		if _, berr := os.Stat(dir + backupSuffix); berr == nil {
			// A backup exists (a swap began): restore it over any partial dir.
			_ = os.RemoveAll(dir)
			_ = os.Rename(dir+backupSuffix, dir)
		}
	}
	if j.StagingRootKeyPath != "" {
		_ = backend.Delete(ctx, j.StagingRootKeyPath)
	}
	return seal.ClearMigrationJournalFrom(ctx, backend, canonicalRootKeyPath)
}

// persistedStrategy reads the sealing strategy from a persisted sealed root-key
// blob, or "" when absent/undecodable.
func persistedStrategy(ctx context.Context, backend seal.StorageBackend, rootKeyPath string) seal.StrategyID {
	blob, err := backend.Get(ctx, rootKeyPath)
	if err != nil || len(blob) == 0 {
		return ""
	}
	var sealed struct {
		Strategy seal.StrategyID `json:"strategy"`
	}
	if json.Unmarshal(blob, &sealed) != nil {
		return ""
	}
	return sealed.Strategy
}
