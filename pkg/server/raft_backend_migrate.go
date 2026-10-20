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

// tenantShardRecode is the share of a tenant barrier's encryption migration
// that covers the tenant's locally started dedicated raft shards: each shard's
// state-machine Pebble and, when it opted into one, its dedicated WAL, whose
// snapshots live in the shared host tree and are staged beside their
// originals. All of a tenant's shards share its one barrier, so they recode
// together -- an IsolationRaft tenant's databases, and the home group of an
// IsolationVolume tenant, which runs beside the tenant's Host. The migration
// that begins one commits its stores with the same journaled directory swap it
// commits its own with (StoreDirs), so a crash anywhere is reconciled from the
// journal alone.
//
// Every method of a nil tenantShardRecode is a no-op, so a migration on a node
// without a raft backend factory needs no special case.
type tenantShardRecode struct {
	f        *RaftBackendFactory
	tenantID string
	shards   []shardMigrTarget
	quiesced bool
	staged   []database.StagedSnapshot
}

// beginTenantShardRecode blocks lazy (re)starts of tenantID's shards and
// returns the recode of every one started here. The guard goes up BEFORE the
// started set is snapshotted, so a shard that starts concurrently cannot slip
// between the two and escape the recode. Release lowers it again. It returns
// nil on a nil receiver.
func (f *RaftBackendFactory) beginTenantShardRecode(tenantID string) *tenantShardRecode {
	if f == nil {
		return nil
	}
	f.migrating.Store(tenantID, struct{}{})
	return &tenantShardRecode{f: f, tenantID: tenantID, shards: f.tenantStartedShards(tenantID)}
}

// Empty reports whether no shard of the tenant is started here.
func (r *tenantShardRecode) Empty() bool {
	return r == nil || len(r.shards) == 0
}

// StoreDirs returns every store directory the recode stages a keyed copy of,
// for the migration journal and its swap.
func (r *tenantShardRecode) StoreDirs() []string {
	if r == nil {
		return nil
	}
	var dirs []string
	for _, s := range r.shards {
		dirs = append(dirs, s.smDir)
		if s.walDir != "" {
			dirs = append(dirs, s.walDir)
		}
	}
	return dirs
}

// Quiesce stops every shard, releasing its state-machine Pebble lock and
// closing its dedicated WAL. The tenant barrier is untouched. A shard stopped
// here starts again in Release, whether or not every stop succeeded.
func (r *tenantShardRecode) Quiesce() error {
	if r == nil {
		return nil
	}
	r.quiesced = true
	for _, s := range r.shards {
		if err := r.f.Close(s.key.tenantID, s.key.database); err != nil {
			return &RaftBackendError{TenantID: s.key.tenantID, Database: s.key.database, Op: "migrate-quiesce", Err: err}
		}
	}
	return nil
}

// Copy stages, beside each shard's stores, a copy re-encrypted from source's
// key to target's: the state-machine Pebble through recode, the dedicated WAL
// record by record with each of its snapshots' data files staged beside the
// original. checkpoint is handed the staged snapshot set each time it grows, so
// the journal knows every sibling written so far: recovery commits them on a
// roll-forward and discards them on a roll-back.
func (r *tenantShardRecode) Copy(ctx context.Context, source, target *seal.StorageBarrier, recode recodeFunc, checkpoint func([]database.StagedSnapshot) error) error {
	if r == nil {
		return nil
	}
	srcEnc, dstEnc := atrest.New(source), atrest.New(target)
	snapDest := func(snapDir string) string {
		return filepath.Join(snapDir, database.SnapshotDataFile) + database.SnapshotKeyedSuffix
	}
	for _, s := range r.shards {
		smKeyed := s.smDir + database.KeyedDirSuffix
		_ = os.RemoveAll(smKeyed)
		if _, err := recode(ctx, s.smDir, srcEnc, smKeyed, dstEnc); err != nil {
			return &RaftBackendError{TenantID: r.tenantID, Database: s.key.database, Op: "migrate-recode-sm", Err: err}
		}
		if s.walDir == "" {
			continue
		}
		walKeyed := s.walDir + database.KeyedDirSuffix
		_ = os.RemoveAll(walKeyed)
		_, staged, err := database.ReencryptWALStore(s.walDir, source, walKeyed, target, snapDest)
		if err != nil {
			return &RaftBackendError{TenantID: r.tenantID, Database: s.key.database, Op: "migrate-recode-wal", Err: err}
		}
		r.staged = append(r.staged, staged...)
		if err := checkpoint(r.staged); err != nil {
			return err
		}
	}
	return nil
}

// Verify proves every staged state machine holds exactly its source's records.
// A WAL is proven by the shard's start in Release, which replays it into the
// keyed state machine.
func (r *tenantShardRecode) Verify(ctx context.Context, source, target *seal.StorageBarrier) error {
	if r == nil {
		return nil
	}
	srcEnc, dstEnc := atrest.New(source), atrest.New(target)
	for _, s := range r.shards {
		if err := database.VerifyReencryptedStore(ctx, s.smDir, srcEnc, s.smDir+database.KeyedDirSuffix, dstEnc); err != nil {
			return &RaftBackendError{TenantID: r.tenantID, Database: s.key.database, Op: "migrate-verify-sm", Err: err}
		}
	}
	return nil
}

// CommitSnapshots renames every staged snapshot data file over its original,
// once the swapped WALs (which carry the keyed snapshot epochs) are live. It
// runs past the point of no return, so it retries transient filesystem errors
// rather than aborting: an abort would leave the live snapshots under the
// retiring key while the swapped WALs expect the keyed copies. A sibling
// already committed is skipped.
func (r *tenantShardRecode) CommitSnapshots(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if err := database.CommitStagedSnapshotsWithRetry(ctx, r.staged, snapshotCommitAttempts, snapshotCommitBackoff); err != nil {
		return &RaftBackendError{TenantID: r.tenantID, Op: "migrate-commit-snapshots", Err: err}
	}
	return nil
}

// Discard removes every staged snapshot data file, for a migration that failed
// before its commit. The staged store copies go with the journal's StoreDirs.
func (r *tenantShardRecode) Discard() {
	if r != nil {
		database.RollbackStagedSnapshots(r.staged)
	}
}

// Release lowers the guard and starts every quiesced shard again, under
// whichever barrier the tenant holds by then: the keyed one after a commit,
// the untouched one after a failure, so the tenant is never left dark. When
// tolerateSealed is set the installed barrier is sealed pending its holders'
// quorum, and a shard that cannot start for that reason stays stopped until a
// later Attach finds it unsealed. Release is idempotent.
func (r *tenantShardRecode) Release(ctx context.Context, tolerateSealed bool) error {
	if r == nil {
		return nil
	}
	r.f.migrating.Delete(r.tenantID)
	if !r.quiesced {
		return nil
	}
	r.quiesced = false
	var first error
	for _, s := range r.shards {
		err := r.f.Attach(ctx, s.key.tenantID, s.key.database)
		if err == nil || (tolerateSealed && errors.Is(err, seal.ErrSealed)) {
			continue
		}
		if first == nil {
			first = &RaftBackendError{TenantID: s.key.tenantID, Database: s.key.database, Op: "migrate-reattach", Err: err}
		}
	}
	return first
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

	// Block lazy shard rebuilds against the stale barrier, then snapshot the
	// shard set. keyedSealed is set once a Shamir-target reopen installs a
	// SEALED keyed barrier (see the ReopenKeyed call below): the shards' start
	// then fails with seal.ErrSealed, the expected post-migration state pending
	// the holders' quorum, not a migration failure. The release runs after the
	// finalize defer below, so a failed migration's shards start under the
	// rolled-back barrier.
	shards := f.beginTenantShardRecode(tenantID)
	keyedSealed := false
	defer func() {
		if rerr := shards.Release(ctx, keyedSealed); rerr != nil && err == nil {
			err = rerr
		}
	}()

	if shards.Empty() {
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
	j.StoreDirs = shards.StoreDirs()

	// Finalize defer — registered immediately after BeginMigration, BEFORE the
	// first CheckpointMigration, so ANY post-begin failure rolls the journal to a
	// terminal PhaseFailed. Otherwise a leftover non-terminal journal makes the
	// next BeginMigration return ErrMigrationInProgress and wedge every retry.
	defer func() {
		if err != nil && !j.Committed() {
			shards.Discard()
			for _, dir := range j.StoreDirs {
				_ = os.RemoveAll(dir + database.KeyedDirSuffix)
			}
			j.Phase = seal.PhaseFailed
			_ = source.CheckpointMigration(ctx, j)
			_ = source.FinishMigration(ctx)
		}
	}()

	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}

	// Quiesce: stop each shard. The shared tenant barrier stays unsealed
	// throughout.
	if qerr := shards.Quiesce(); qerr != nil {
		err = qerr
		return err
	}

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
	if cerr := shards.Copy(ctx, source, target, recode, func(staged []database.StagedSnapshot) error {
		j.StagedSnapshots = staged
		return source.CheckpointMigration(ctx, j)
	}); cerr != nil {
		err = cerr
		return err
	}

	// Verify each SM round-trip (no data loss). WALs are verified implicitly by
	// the shard re-attach below, which replays them into the keyed state machine.
	j.Phase = seal.PhaseVerifying
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	if verr := shards.Verify(ctx, source, target); verr != nil {
		err = verr
		return err
	}

	// Commit (point of no return): swap every dir, commit the staged snapshots,
	// then adopt the keyed root key once for the whole tenant barrier.
	j.Phase = seal.PhaseCommitting
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	for _, dir := range j.StoreDirs {
		if serr := database.SwapStoreDir(dir); serr != nil {
			err = &RaftBackendError{TenantID: tenantID, Op: "migrate-swap-store", Err: serr}
			return err
		}
	}
	if cerr := shards.CommitSnapshots(ctx); cerr != nil {
		err = cerr
		return err
	}
	if aerr := source.AdoptMigratedRootKey(ctx, target); aerr != nil {
		err = aerr
		return err
	}
	for _, dir := range j.StoreDirs {
		database.RemoveStoreBackup(dir)
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
		slog.Int("shards", len(shards.shards)))
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
