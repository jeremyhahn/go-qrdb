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
	"os"
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// migrateStagingSuffix names the staging root-key path for a tenant migration.
const migrateStagingSuffix = ".migrating"

// MigrateEncryption migrates a single IsolationVolume tenant's dedicated barrier
// from none to a keyed strategy, re-encrypting both halves of its at-rest data
// — the state-machine Pebble and the Raft WAL — in place, then transitioning the
// live tenant barrier to the keyed strategy. The tenant Host is quiesced
// (closed) for the duration and reopened keyed at the end; only this tenant is
// affected.
//
// opts.TargetStrategy must be a keyed strategy. The migration is crash-safe via
// the tenant barrier's journal: copy -> verify -> atomic swap -> adopt key.
func (r *TenantHostRegistry) MigrateEncryption(ctx context.Context, tenantID string, opts seal.MigrationOptions) (err error) {
	if r == nil || r.barrierReg == nil || r.tenants == nil {
		return &TenantHostError{TenantID: tenantID, Op: "migrate", Err: ErrTenantHostUnavailable}
	}
	if verr := opts.Validate(); verr != nil {
		return verr
	}

	// Guard against a concurrent lazy Host (re)build for the whole recycle: while
	// set, Services returns ErrTenantMigrationInProgress instead of wrapping a new
	// host with the stale source barrier. Cleared explicitly before the final
	// Attach (once the keyed barrier is installed) and via defer on any error path.
	r.migrating.Store(tenantID, struct{}{})
	defer r.migrating.Delete(tenantID)

	tb, err := r.barrierReg.GetTenant(tenantID)
	if err != nil {
		return &TenantHostError{TenantID: tenantID, Op: "migrate-barrier-lookup", Err: err}
	}
	source := tb.Barrier()
	if source == nil {
		// Registered tenant whose inner barrier isn't built/attached yet — return a
		// typed error rather than nil-deref in MintKeyedSibling below.
		return &TenantHostError{TenantID: tenantID, Op: "migrate-barrier-lookup", Err: ErrTenantHostUnavailable}
	}

	dataRoot, err := r.tenants.dataRootOf(ctx, tenantID)
	if err != nil {
		return &TenantHostError{TenantID: tenantID, Op: "migrate-data-root", Err: err}
	}
	if dataRoot == "" {
		return &TenantHostError{TenantID: tenantID, Op: "migrate-data-root", Err: ErrTenantDataRootUnset}
	}

	// The IsolationVolume tenant Host roots its Raft state under
	// {dataRoot}/databases: the WAL at .../wal and the state-machine Pebble at
	// .../nodehost/snapshots/shard-{ShardKV} (see newTenantHost + host.go).
	hostRoot := filepath.Join(dataRoot, "databases")
	walDir := filepath.Join(hostRoot, "wal")
	smDir := filepath.Join(hostRoot, "nodehost", "snapshots", fmt.Sprintf("shard-%d", shard.ShardKV))

	// Mint the keyed target over the tenant barrier's exact backend + config.
	var target *seal.StorageBarrier
	if opts.TargetStrategy == seal.StrategyShamir {
		// The Shamir target requires the direct-mode ceremony (mint + M-of-N
		// split to enrolled Security Officers in one step); the generic
		// MintKeyedSibling path cannot represent it — see its doc comment.
		target, _, err = source.MintKeyedShamirSibling(ctx, migrateStagingSuffix, opts.ShamirThreshold, opts.ShamirTotal)
		if err != nil {
			return &TenantHostError{TenantID: tenantID, Op: "migrate-mint", Err: err}
		}
	} else {
		mintStrat, merr := keyedTenantStrategy(opts)
		if merr != nil {
			return merr
		}
		target, err = source.MintKeyedSibling(ctx, migrateStagingSuffix, mintStrat, opts.Credentials)
		if err != nil {
			return &TenantHostError{TenantID: tenantID, Op: "migrate-mint", Err: err}
		}
	}
	// Always remove the staging keyed root-key blob + zero its key enclave on
	// return (success: redundant after adopt; failure: rollback). Mint is itself
	// idempotent against a leftover blob; this keeps the backend clean.
	defer func() { _ = target.DeleteRootKey(ctx) }()
	defer func() { _ = target.Close() }()

	j, err := source.BeginMigration(ctx, opts)
	if err != nil {
		return err
	}
	j.StagingRootKeyPath = target.RootKeyPath()
	// Record the directories the commit swaps so a crash-recovery pass can roll
	// the swaps forward (or back) from the journal alone. The volume tenant's
	// snapshots ride the smDir swap, so no staged-snapshot record is needed.
	j.StoreDirs = []string{smDir, walDir}
	// Registered BEFORE the first CheckpointMigration so any post-begin failure
	// rolls the journal to terminal PhaseFailed — else a leftover non-terminal
	// journal wedges the next BeginMigration with ErrMigrationInProgress.
	defer func() {
		if err != nil && !j.Committed() {
			_ = os.RemoveAll(smDir + database.KeyedDirSuffix)
			_ = os.RemoveAll(walDir + database.KeyedDirSuffix)
			j.Phase = seal.PhaseFailed
			_ = source.CheckpointMigration(ctx, j)
			_ = source.FinishMigration(ctx)
		}
	}()
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}

	// Quiesce: close the tenant Host, releasing its locks on the SM Pebble + WAL.
	if cerr := r.Close(tenantID); cerr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-quiesce", Err: cerr}
		return err
	}

	// Closing the Host sealed the quicraft barrier underlying `source`: the Host
	// owns the lifecycle of the barrier it was given via WithBarrier and zeroes
	// its keys on Close. That leaves the source StorageBarrier wrapper's state
	// desynced from its now-sealed inner barrier, so re-sync (Seal) and re-unseal
	// it before the at-rest re-encryption reads the source data. The source is
	// either StrategyNone (plaintext) or StrategySystemBarrier (auto-unseal via
	// the system barrier). Both accept empty Credentials: StrategyNone ignores them
	// entirely; StrategySystemBarrier derives unseal authority from the system
	// barrier (always unsealed at this point), not from user-supplied credentials.
	_ = source.Seal()
	if uerr := source.Unseal(ctx, seal.Credentials{}); uerr != nil && !errors.Is(uerr, seal.ErrAlreadyUnsealed) {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-reunseal-source", Err: uerr}
		return err
	}

	srcEnc := atrest.New(source)
	dstEnc := atrest.New(target)

	// Copy phase: re-encrypt the SM Pebble + the WAL into keyed siblings.
	j.Phase = seal.PhaseCopying
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	smKeyed := smDir + database.KeyedDirSuffix
	_ = os.RemoveAll(smKeyed)
	recode := database.ReencryptPebbleStore
	if opts.Mechanism == seal.MechanismInPlace {
		recode = database.ReencryptStoreFiles
	}
	if _, cerr := recode(ctx, smDir, srcEnc, smKeyed, dstEnc); cerr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-recode-sm", Err: cerr}
		return err
	}
	walKeyed := walDir + database.KeyedDirSuffix
	_ = os.RemoveAll(walKeyed)
	// The volume tenant's snapshot directories sit UNDER smDir (the host's
	// nodehost/snapshots/shard tree), so re-encrypt each snapshot's data file
	// into the matching path inside smKeyed. The SwapStoreDir(smDir) below then
	// makes the keyed snapshot live, and a rolled-back migration discards it with
	// smKeyed — no separate snapshot commit is required, so the staged slice is
	// intentionally ignored here.
	snapDest := func(snapDir string) string {
		return filepath.Join(strings.Replace(snapDir, smDir, smKeyed, 1), database.SnapshotDataFile)
	}
	if _, _, cerr := database.ReencryptWALStore(walDir, source, walKeyed, target, snapDest); cerr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-recode-wal", Err: cerr}
		return err
	}

	// Verify the SM Pebble round-trip (no data loss). The WAL is verified
	// implicitly by the host reopen below, which replays it into the keyed SM.
	j.Phase = seal.PhaseVerifying
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	if verr := database.VerifyReencryptedStore(ctx, smDir, srcEnc, smKeyed, dstEnc); verr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-verify-sm", Err: verr}
		return err
	}

	// Commit (point of no return): swap dirs, adopt the keyed root key.
	j.Phase = seal.PhaseCommitting
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	if serr := database.SwapStoreDir(smDir); serr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-swap-sm", Err: serr}
		return err
	}
	if serr := database.SwapStoreDir(walDir); serr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-swap-wal", Err: serr}
		return err
	}
	if aerr := source.AdoptMigratedRootKey(ctx, target); aerr != nil {
		err = aerr
		return err
	}
	database.RemoveStoreBackup(smDir)
	database.RemoveStoreBackup(walDir)

	// Replace the live (none) tenant barrier with a keyed one reopened from the
	// now-keyed canonical root key, then drop the staging blob.
	reopenStrat, serr := reopenStrategyFor(opts)
	if serr != nil {
		err = serr
		return err
	}
	keyed, rerr := source.ReopenKeyed(ctx, reopenStrat, opts.Credentials)
	if rerr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-reopen-barrier", Err: rerr}
		return err
	}
	if perr := r.barrierReg.ReplaceTenantBarrier(tenantID, seal.WrapTenantBarrier(tenantID, keyed)); perr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-replace-barrier", Err: perr}
		return err
	}

	j.Phase = seal.PhaseComplete
	_ = source.CheckpointMigration(ctx, j)
	_ = source.FinishMigration(ctx)

	// The keyed barrier is now installed; allow Host (re)builds again before the
	// eager Attach so the rebuild — and any concurrent Services — uses the keyed
	// barrier. (defer also deletes it; double-delete is harmless.)
	r.migrating.Delete(tenantID)

	// A Shamir target reopens SEALED (see ReopenKeyed): by design it never
	// auto-unseals from disk, migration or not. The migration itself is
	// complete and the keyed barrier is installed; the tenant Host stays
	// detached until a Security Officer quorum unseals it via
	// BarrierUnsealWithShare(s) (the same mechanism as a fresh Shamir init) —
	// the next Services()/Attach call (request-driven or operator-driven)
	// naturally retries and succeeds once unsealed. Skipping the eager Attach
	// here is expected, not a failure.
	if keyed.IsSealed() {
		r.logger.Info("tenant encryption migration complete; barrier sealed pending Shamir quorum",
			"tenant", tenantID, "strategy", string(opts.TargetStrategy))
		return nil
	}

	// Reopen the tenant Host eagerly under the keyed barrier; this also verifies
	// the WAL re-encryption by replaying it into the keyed state machine.
	if aerr := r.Attach(ctx, tenantID); aerr != nil {
		err = &TenantHostError{TenantID: tenantID, Op: "migrate-reopen-host", Err: aerr}
		return err
	}
	r.logger.Info("tenant encryption migration complete",
		"tenant", tenantID, "strategy", string(opts.TargetStrategy))
	return nil
}

// reopenStrategyFor builds the keyed sealing strategy ReopenKeyed uses to
// reconstruct the live tenant barrier after a migration commits. A Shamir
// target reuses the same threshold/total the mint step registered (ReopenKeyed
// special-cases the Shamir ID and does not attempt to unseal); every other
// keyed target uses keyedTenantStrategy's self-contained credential strategy.
func reopenStrategyFor(opts seal.MigrationOptions) (seal.SealingStrategy, error) {
	if opts.TargetStrategy == seal.StrategyShamir {
		return seal.NewShamirStrategy(opts.ShamirThreshold, opts.ShamirTotal)
	}
	return keyedTenantStrategy(opts)
}

// keyedTenantStrategy builds the keyed sealing strategy for a PASSPHRASE-target
// tenant migration, using the same passphrase strategy the boot path registers
// so the sealed blob is unsealable after a restart. A Shamir target does not
// go through this helper — see reopenStrategyFor and MigrateEncryption's
// MintKeyedShamirSibling branch.
func keyedTenantStrategy(opts seal.MigrationOptions) (seal.SealingStrategy, error) {
	switch opts.TargetStrategy {
	case seal.StrategyPassphrase:
		return seal.NewDeferredSoftwareStrategy(), nil
	default:
		return nil, seal.ErrMigrationInvalidTarget
	}
}
