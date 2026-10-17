// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// snapshotCommitAttempts / snapshotCommitBackoff bound the retry of the staged
// snapshot commit performed past the migration point of no return. Transient
// filesystem errors there must not abort the migration (the directory swaps are
// already durable), so the idempotent commit is retried with exponential
// backoff before giving up and leaving the staged siblings on disk for recovery.
const (
	snapshotCommitAttempts = 5
	snapshotCommitBackoff  = 50 * time.Millisecond
)

// recodeFunc re-encrypts a store directory from srcEnc into dstDir under dstEnc,
// returning the bytes processed. database.ReencryptPebbleStore /
// ReencryptStoreFiles satisfy it; a test seam may substitute a failing impl to
// exercise the migration rollback path.
type recodeFunc func(ctx context.Context, srcDir string, srcEnc *atrest.Encryptor, dstDir string, dstEnc *atrest.Encryptor) (int64, error)

// shardMigrTarget is one local dedicated-raft shard to recycle during a tenant's
// encryption migration: its on-disk state-machine Pebble dir and (when the
// shard opted into a per-tenant-KEY WAL, 4d Option C) its dedicated WAL dir.
type shardMigrTarget struct {
	key    raftShardKey
	smDir  string // {dataDir}/shard-{shardID} — encfs Pebble under the tenant barrier
	walDir string // {dataDir}/wal — dedicated WAL, or "" for a shared-WAL (Route A) shard
}

// tenantStartedShards snapshots every locally-started dedicated raft shard owned
// by tenantID, with the on-disk dirs each recycle must re-encrypt. All of a
// tenant's dedicated-raft databases share ONE per-tenant barrier, so enabling
// encryption on that barrier must recode every one of the tenant's local shards
// together — recoding a subset would strand the rest behind a keyed barrier they
// can no longer open.
func (f *RaftBackendFactory) tenantStartedShards(tenantID string) []shardMigrTarget {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A start of the tenant's shard that began before the migration did is
	// opened under the old barrier: it is listed, and migrated, once it ends.
	f.awaitStartsLocked(func(k raftShardKey, _ *startingShard) bool { return k.tenantID == tenantID })
	var out []shardMigrTarget
	for k, rs := range f.shards {
		if k.tenantID != tenantID {
			continue
		}
		t := shardMigrTarget{
			key:   k,
			smDir: filepath.Join(rs.dataDir, fmt.Sprintf("shard-%d", rs.shardID)),
		}
		if rs.dedicatedWAL != nil {
			t.walDir = filepath.Join(rs.dataDir, dedicatedWALSubdir)
		}
		out = append(out, t)
	}
	return out
}

// MigrateTenantEncryption migrates an IsolationRaft tenant's dedicated barrier
// from none to a keyed strategy, re-encrypting every one of the tenant's local
// dedicated-raft shards (each shard's state-machine Pebble plus its dedicated WAL
// when present) in place under a single barrier recycle, then installing the
// keyed barrier and re-attaching the shards. Only the named tenant's shards are
// affected; the shared host, the system barrier, and every other tenant keep
// serving uninterrupted. It is the IsolationRaft analogue of
// TenantHostRegistry.MigrateEncryption (which serves IsolationVolume tenants).
//
// opts.TargetStrategy must be a keyed strategy. The migration is crash-safe via
// the tenant barrier's journal: copy -> verify -> atomic swap -> adopt key, with
// one mint/adopt covering all of the tenant's shards. On any pre-commit failure
// the staging copies are removed and the shards re-attach under the untouched
// (none) barrier, so the tenant is never stranded behind closed shards.
func (f *RaftBackendFactory) MigrateTenantEncryption(ctx context.Context, tenantID string, opts seal.MigrationOptions) (err error) {
	if f == nil || f.barrierReg == nil || f.host == nil {
		return &RaftBackendError{TenantID: tenantID, Op: "migrate", Err: ErrRaftBackendUnavailable}
	}
	if verr := opts.Validate(); verr != nil {
		return verr
	}

	tb, err := f.barrierReg.GetTenant(tenantID)
	if err != nil {
		return &RaftBackendError{TenantID: tenantID, Op: "migrate-barrier-lookup", Err: err}
	}
	source := tb.Barrier()
	if source == nil {
		return &RaftBackendError{TenantID: tenantID, Op: "migrate-barrier-lookup", Err: ErrRaftBackendUnavailable}
	}
	// The tenant barrier is shared across the tenant's shards (and stays
	// registry-owned across a per-shard stop), so it must already be unsealed —
	// closing one shard never seals it. A sealed source means the tenant key is
	// not present on this node; there is nothing to migrate locally.
	if source.IsSealed() {
		return &RaftBackendError{TenantID: tenantID, Op: "migrate", Err: seal.ErrSealed}
	}

	// Block lazy shard rebuilds against the stale barrier at TENANT granularity
	// BEFORE snapshotting the shard set — so a db that starts concurrently cannot
	// slip between the snapshot and the guard and escape the recode (the
	// IsolationVolume sibling guards first thing for the same reason). The early
	// defer is a safety net for the return paths below that precede the finalize
	// defer; the finalize defer clears the guard again (idempotent) before
	// re-attaching.
	f.migrating.Store(tenantID, struct{}{})
	defer f.migrating.Delete(tenantID)

	shards := f.tenantStartedShards(tenantID)
	if len(shards) == 0 {
		return &RaftBackendError{TenantID: tenantID, Op: "migrate", Err: ErrRaftDatabaseUnknown}
	}

	var target *seal.StorageBarrier
	if opts.TargetStrategy == seal.StrategyShamir {
		// The Shamir target requires the direct-mode ceremony (mint + M-of-N
		// split to enrolled Security Officers in one step); the generic
		// MintKeyedSibling path cannot represent it — see its doc comment.
		target, _, err = source.MintKeyedShamirSibling(ctx, migrateStagingSuffix, opts.ShamirThreshold, opts.ShamirTotal)
		if err != nil {
			return &RaftBackendError{TenantID: tenantID, Op: "migrate-mint", Err: err}
		}
	} else {
		mintStrat, merr := keyedTenantStrategy(opts)
		if merr != nil {
			return merr
		}
		target, err = source.MintKeyedSibling(ctx, migrateStagingSuffix, mintStrat, opts.Credentials)
		if err != nil {
			return &RaftBackendError{TenantID: tenantID, Op: "migrate-mint", Err: err}
		}
	}
	// Always remove the staging keyed root-key blob and zero its key enclave on
	// return — on success (the keyed root key is adopted to the canonical path, so
	// the staging copy is redundant) and on failure (rollback). MintKeyedSibling
	// is itself idempotent against a leftover blob; this keeps the backend clean.
	defer func() { _ = target.DeleteRootKey(ctx) }()
	defer func() { _ = target.Close() }()

	j, err := source.BeginMigration(ctx, opts)
	if err != nil {
		return err
	}
	j.StagingRootKeyPath = target.RootKeyPath()
	// Record every directory the commit swaps so a crash-recovery pass can roll
	// the swaps forward (or back) from the journal alone, without re-deriving
	// the tenant's shard layout.
	for _, s := range shards {
		j.StoreDirs = append(j.StoreDirs, s.smDir)
		if s.walDir != "" {
			j.StoreDirs = append(j.StoreDirs, s.walDir)
		}
	}

	quiesced := false
	// keyedSealed is set true once a Shamir-target reopen installs a SEALED
	// keyed barrier (see the ReopenKeyed call below): it tells the deferred
	// re-attach that a seal.ErrSealed from f.Attach is the expected, non-fatal
	// post-migration state (pending Security Officer quorum), not a migration
	// failure.
	keyedSealed := false
	// stagedSnaps accumulates re-encrypted snapshot data files staged beside their
	// originals in the shared host snapshot tree, which the per-shard dir swaps
	// below do NOT move (unlike IsolationVolume, whose snapshots ride the smDir
	// swap). They are renamed into place after the swaps commit, or discarded on
	// any pre-commit failure.
	var stagedSnaps []database.StagedSnapshot
	// Finalize defer — registered immediately after BeginMigration, BEFORE the
	// first CheckpointMigration, so ANY post-begin failure rolls the journal to a
	// terminal PhaseFailed. Otherwise a leftover non-terminal journal makes the
	// next BeginMigration return ErrMigrationInProgress and wedge every retry.
	defer func() {
		if err != nil && !j.Committed() {
			database.RollbackStagedSnapshots(stagedSnaps)
			for _, s := range shards {
				_ = os.RemoveAll(s.smDir + database.KeyedDirSuffix)
				if s.walDir != "" {
					_ = os.RemoveAll(s.walDir + database.KeyedDirSuffix)
				}
			}
			j.Phase = seal.PhaseFailed
			_ = source.CheckpointMigration(ctx, j)
			_ = source.FinishMigration(ctx)
		}
		// Clear the guard (idempotent), then re-attach every quiesced shard: after a
		// successful commit under the keyed barrier, after a failure under the
		// untouched none barrier. Either way the tenant is never left dark.
		f.migrating.Delete(tenantID)
		if quiesced {
			for _, s := range shards {
				if aerr := f.Attach(ctx, s.key.tenantID, s.key.database); aerr != nil && err == nil {
					if keyedSealed && errors.Is(aerr, seal.ErrSealed) {
						// Expected: a Shamir target reopens sealed, pending quorum. The
						// shard stays detached; a later Attach (request-driven or
						// operator-driven) retries and succeeds once unsealed.
						continue
					}
					err = &RaftBackendError{TenantID: s.key.tenantID, Database: s.key.database, Op: "migrate-reattach", Err: aerr}
				}
			}
		}
	}()

	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}

	// Quiesce: stop each shard (releasing its SM Pebble lock + closing its
	// dedicated WAL). The shared tenant barrier stays unsealed throughout.
	for _, s := range shards {
		if cerr := f.Close(s.key.tenantID, s.key.database); cerr != nil {
			err = &RaftBackendError{TenantID: s.key.tenantID, Database: s.key.database, Op: "migrate-quiesce", Err: cerr}
			return err
		}
	}
	quiesced = true

	srcEnc := atrest.New(source)
	dstEnc := atrest.New(target)
	recode := database.ReencryptPebbleStore
	if opts.Mechanism == seal.MechanismInPlace {
		recode = database.ReencryptStoreFiles
	}
	if f.recodeOverride != nil { // test-only: inject a recode failure (rollback path)
		recode = f.recodeOverride
	}

	// Copy: re-encrypt each shard's SM Pebble + dedicated WAL into keyed siblings.
	j.Phase = seal.PhaseCopying
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	for _, s := range shards {
		smKeyed := s.smDir + database.KeyedDirSuffix
		_ = os.RemoveAll(smKeyed)
		if _, cerr := recode(ctx, s.smDir, srcEnc, smKeyed, dstEnc); cerr != nil {
			err = &RaftBackendError{TenantID: tenantID, Database: s.key.database, Op: "migrate-recode-sm", Err: cerr}
			return err
		}
		if s.walDir != "" {
			walKeyed := s.walDir + database.KeyedDirSuffix
			_ = os.RemoveAll(walKeyed)
			// The dedicated-raft shard's snapshots live in the shared host tree,
			// not under s.smDir/s.walDir, so re-encrypt each snapshot's data file
			// into a staging sibling (committed by rename after the swaps).
			snapDest := func(snapDir string) string {
				return filepath.Join(snapDir, database.SnapshotDataFile) + database.SnapshotKeyedSuffix
			}
			_, staged, cerr := database.ReencryptWALStore(s.walDir, source, walKeyed, target, snapDest)
			if cerr != nil {
				err = &RaftBackendError{TenantID: tenantID, Database: s.key.database, Op: "migrate-recode-wal", Err: cerr}
				return err
			}
			stagedSnaps = append(stagedSnaps, staged...)
			// Checkpoint the staged snapshot set as it grows so a crash at any
			// point leaves the journal aware of every sibling written so far —
			// recovery commits them on roll-forward and discards them on
			// roll-back.
			j.StagedSnapshots = stagedSnaps
			if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
				err = cerr
				return err
			}
		}
	}

	// Verify each SM round-trip (no data loss). WALs are verified implicitly by
	// the shard re-attach below, which replays them into the keyed state machine.
	j.Phase = seal.PhaseVerifying
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	for _, s := range shards {
		if verr := database.VerifyReencryptedStore(ctx, s.smDir, srcEnc, s.smDir+database.KeyedDirSuffix, dstEnc); verr != nil {
			err = &RaftBackendError{TenantID: tenantID, Database: s.key.database, Op: "migrate-verify-sm", Err: verr}
			return err
		}
	}

	// Commit (point of no return): swap every dir, then adopt the keyed root key
	// once for the whole tenant barrier.
	j.Phase = seal.PhaseCommitting
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	for _, s := range shards {
		if serr := database.SwapStoreDir(s.smDir); serr != nil {
			err = &RaftBackendError{TenantID: tenantID, Database: s.key.database, Op: "migrate-swap-sm", Err: serr}
			return err
		}
		if s.walDir != "" {
			if serr := database.SwapStoreDir(s.walDir); serr != nil {
				err = &RaftBackendError{TenantID: tenantID, Database: s.key.database, Op: "migrate-swap-wal", Err: serr}
				return err
			}
		}
	}
	// Commit the staged snapshot data files now the WALs (which carry the keyed
	// snapshot epochs) are live: rename each keyed sibling over its original so
	// the shard recovers its snapshot under the new barrier. Idempotent for crash
	// recovery — a missing sibling is already committed.
	// Past the point of no return: roll forward, retrying transient FS errors,
	// rather than aborting — an abort here would leave the live snapshots
	// source-encrypted while the swapped WAL metadata expects the keyed copies.
	if cerr := database.CommitStagedSnapshotsWithRetry(ctx, stagedSnaps, snapshotCommitAttempts, snapshotCommitBackoff); cerr != nil {
		err = &RaftBackendError{TenantID: tenantID, Op: "migrate-commit-snapshots", Err: cerr}
		return err
	}
	if aerr := source.AdoptMigratedRootKey(ctx, target); aerr != nil {
		err = aerr
		return err
	}
	for _, s := range shards {
		database.RemoveStoreBackup(s.smDir)
		if s.walDir != "" {
			database.RemoveStoreBackup(s.walDir)
		}
	}

	// Install the keyed barrier: reopen from the now-keyed canonical root key and
	// replace the live (none) tenant barrier so re-attach and any concurrent
	// resolve use the keyed barrier.
	reopenStrat, serr := reopenStrategyFor(opts)
	if serr != nil {
		err = serr
		return err
	}
	keyed, rerr := source.ReopenKeyed(ctx, reopenStrat, opts.Credentials)
	if rerr != nil {
		err = &RaftBackendError{TenantID: tenantID, Op: "migrate-reopen-barrier", Err: rerr}
		return err
	}
	if perr := f.barrierReg.ReplaceTenantBarrier(tenantID, seal.WrapTenantBarrier(tenantID, keyed)); perr != nil {
		err = &RaftBackendError{TenantID: tenantID, Op: "migrate-replace-barrier", Err: perr}
		return err
	}

	j.Phase = seal.PhaseComplete
	_ = source.CheckpointMigration(ctx, j)
	_ = source.FinishMigration(ctx)

	f.logger.Info("tenant raft-shard encryption migration complete",
		slog.String("tenant", tenantID),
		slog.String("strategy", string(opts.TargetStrategy)),
		slog.Int("shards", len(shards)))
	// A Shamir target reopens SEALED (see ReopenKeyed): by design it never
	// auto-unseals from disk. The migration itself is complete and the keyed
	// barrier is installed; the deferred re-attach below would otherwise
	// report the shards' expected seal.ErrSealed as a migration FAILURE.
	// keyedSealed tells that defer to tolerate it — the shards stay detached
	// until a share-holder quorum unseals the barrier, at which point the
	// normal Services()/Attach retry
	// (request-driven or operator-driven) succeeds.
	keyedSealed = keyed.IsSealed()
	// The deferred re-attach reopens every shard under the keyed barrier.
	return nil
}
