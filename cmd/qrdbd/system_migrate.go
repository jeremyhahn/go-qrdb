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

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// systemMigrationRequest is an in-flight system-scope enable-encryption request
// handed from the transport handler (via daemonSystemMigrator) to the daemon's
// boot loop, which performs the in-process data-plane recycle.
type systemMigrationRequest struct {
	opts seal.MigrationOptions
	done chan error
}

// daemonSystemMigrator implements service.EncryptionMigrator for the standalone
// daemon's SYSTEM scope. It does not perform the migration itself: it enqueues
// the request onto the boot loop's channel, where the data plane is recycled
// in-process (the OS process never exits). Tenant scope is handled by the
// per-tenant Host registry via the server's scoped-migrator composite, so this
// type only ever receives ScopeSystem.
type daemonSystemMigrator struct {
	ch chan *systemMigrationRequest
	// sealEnabled mirrors f.sealConfig.Enabled at construction time. This
	// migrator is wired into gRPC/Unix unconditionally in clustered mode
	// (server.WithEncryptionMigrator does not itself gate on seal state, unlike
	// REST's barrierOn-gated route), so a seal-disabled daemon has no seal
	// subsystem (sealSub is nil in the boot loop) but can still receive an
	// EnableEncryption request over those transports. Rejecting it here, before
	// the request is ever enqueued, keeps handleSystemMigration's sealSub
	// dereference from ever running against a nil subsystem instead of
	// tearing the data plane down first and panicking on it (go-qrdb#550
	// review).
	sealEnabled bool
	// inProgress guards the ENTIRE lifetime of an accepted migration request,
	// from acceptance here to its terminal outcome inside the boot loop --
	// not channel occupancy, which only reflects whether a request is
	// sitting unread in the buffer. The boot loop removes a request from ch
	// the instant its select case fires, before handleSystemMigration does
	// any work, so a channel-occupancy check alone leaves the entire
	// migration (quiesce, re-encrypt, reboot) unguarded: a second request
	// could enqueue during that window and, after the reboot into the newly
	// keyed (but sealed) barrier, tear the barely-booted data plane down
	// again. CompareAndSwap makes acceptance atomic and exclusive; it is
	// cleared only when the accepted request's terminal outcome is known
	// (see EnableEncryption), never merely because a caller stopped waiting
	// on it (go-qrdb#550 review).
	inProgress atomic.Bool
}

// newDaemonSystemMigrator constructs the daemon system migrator over the boot
// loop's request channel. sealEnabled is f.sealConfig.Enabled: when false,
// EnableEncryption is rejected before enqueueing, since there is no seal
// subsystem for the boot loop to migrate.
func newDaemonSystemMigrator(ch chan *systemMigrationRequest, sealEnabled bool) *daemonSystemMigrator {
	return &daemonSystemMigrator{ch: ch, sealEnabled: sealEnabled}
}

// buildSysMigrator wires the daemon's seal.enabled config decision into the
// system migrator. It exists as a named, directly-testable seam between
// runStart's flags and newDaemonSystemMigrator specifically so a future edit
// to runStart that hardcodes sealEnabled (instead of reading it from f) fails
// a fast unit test instead of surfacing only as the go-qrdb#550 panic under
// gRPC/Unix load on a live seal-disabled daemon.
func buildSysMigrator(f *startFlags, ch chan *systemMigrationRequest) *daemonSystemMigrator {
	return newDaemonSystemMigrator(ch, f.sealConfig.Enabled)
}

// Compile-time check.
var _ service.EncryptionMigrator = (*daemonSystemMigrator)(nil)

// EnableEncryption enqueues a system-scope migration onto the boot loop and
// blocks until the recycle reports its outcome. A non-system scope is a routing
// error (the scoped composite should never deliver one here). A second request
// while one is pending returns seal.ErrMigrationInProgress. A seal-disabled
// node (no seal subsystem to migrate) returns ErrMigrationProviderUnavailable.
func (m *daemonSystemMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	// On every path that does NOT hand the request off to the boot loop, destroy
	// the unlock credential here — the recycler built it for the async system
	// path and only handleSystemMigration (the enqueued path) destroys it.
	if !m.sealEnabled {
		seal.DestroyCredentials(opts.Credentials)
		return service.ErrMigrationProviderUnavailable
	}
	if scope.Kind != service.ScopeSystem {
		seal.DestroyCredentials(opts.Credentials)
		return service.ErrMigrationProviderUnavailable
	}
	if err := opts.Validate(); err != nil {
		seal.DestroyCredentials(opts.Credentials)
		return err
	}
	if !m.inProgress.CompareAndSwap(false, true) {
		// seal.ErrMigrationInProgress, not a command-local sentinel: REST
		// (pkg/api/rest/handlers_barrier.go) and gRPC/Unix
		// (pkg/api/grpc/service_barrier.go) already map it to 409/
		// FailedPrecondition for the tenant-scope path, and a private
		// errors.New here would fall through both mappers' defaults to a
		// misleading 500/Internal for an entirely routine, expected
		// condition (go-qrdb#550 review).
		seal.DestroyCredentials(opts.Credentials)
		return seal.ErrMigrationInProgress
	}
	req := &systemMigrationRequest{opts: opts, done: make(chan error, 1)}
	select {
	case m.ch <- req:
		// Enqueued; the boot loop (handleSystemMigration) owns + destroys the
		// credential. Wait for the recycle outcome.
		select {
		case err := <-req.done:
			m.inProgress.Store(false)
			return err
		case <-ctx.Done():
			// The migration keeps running server-side after this caller stops
			// waiting on it -- releasing inProgress here (instead of on the
			// migration's own terminal outcome) would let a second request
			// enqueue while the first is still tearing down and re-encrypting
			// the data plane. Hand the release off to a goroutine that waits
			// for the SAME terminal signal a non-cancelled caller would have.
			go func() {
				<-req.done
				m.inProgress.Store(false)
			}()
			return ctx.Err()
		}
	default:
		// Unreachable in practice: inProgress already serializes entry, and
		// ch has capacity for exactly the one request CompareAndSwap just
		// admitted. Kept as defense in depth against a CAS/channel desync
		// rather than blocking forever on a full buffered channel; release
		// the slot this call reserved since nothing was actually enqueued.
		m.inProgress.Store(false)
		seal.DestroyCredentials(opts.Credentials)
		return seal.ErrMigrationInProgress
	}
}

// handleSystemMigration performs the in-process system data-plane recycle for an
// enqueued migration request. It quiesces everything that holds a lock on the
// system Raft host's WAL + state-machine Pebble (the API server, dedicated-db
// raft shards, per-tenant Hosts, and the host itself), cancels the boot
// context so this iteration's long-lived goroutines stop, then re-encrypts the
// host's stores in place and adopts the keyed root key. On success it returns
// reboot=true so the caller re-runs the boot body, which rebuilds the seal
// subsystem from the now-keyed root key (the barrier comes up sealed, pending
// operator unseal with the newly-set credential).
//
// The seal subsystem is intentionally NOT closed here: recodeSystemBarrier needs
// sealSub.SealBarrier open to read the source data. The caller's deferred
// sealSub.Close fires after the boot closure returns (LIFO, registered first),
// i.e. after the recode completes.
func handleSystemMigration(
	req *systemMigrationRequest,
	f *startFlags,
	srv *server.Server,
	svcNode *lifecycle.Node,
	host *quicraft.Host,
	raftBackendReg *server.RaftBackendFactory,
	tenantHostReg *server.TenantHostRegistry,
	memberRecord memberRecordCloser,
	bootCancel context.CancelFunc,
	sealSub *sealSubsystem,
) (reboot bool, err error) {
	// Defensive: daemonSystemMigrator.EnableEncryption already rejects a
	// seal-disabled node with ErrMigrationProviderUnavailable before ever
	// enqueueing a request, so sealSub should never be nil here. This guard
	// exists so a future caller of handleSystemMigration that bypasses that
	// check gets a typed error instead of the nil-pointer panic this function
	// used to hit at sealSub.SealBarrier below (go-qrdb#550 review).
	if sealSub == nil {
		seal.DestroyCredentials(req.opts.Credentials)
		rerr := &SystemMigrationSealDisabledError{}
		req.done <- rerr
		return false, rerr
	}

	slog.Info("system encryption migration requested; quiescing data plane in-process",
		slog.String("strategy", string(req.opts.TargetStrategy)))

	// Hand the system shard to a peer first, while this host still serves it:
	// a leader that just closes leaves the peers' system-shard reads -- the
	// TLS trust resolution and revocation check behind every authenticated
	// request -- stalled until they elect one themselves.
	if host != nil {
		if herr := handOffLeadership(context.Background(), host, shard.ShardKV, f.replicaID); herr != nil {
			slog.Warn("system shard leadership handoff before migration failed; closing anyway",
				slog.Any("error", herr))
		}
	}

	// Stop this iteration's long-lived goroutines (lease sweeper, quota
	// reconciler, tenant reconciler, dynamic membership) before tearing down the
	// services they call into.
	if bootCancel != nil {
		bootCancel()
	}
	if memberRecord != nil {
		if cerr := memberRecord.Close(); cerr != nil {
			slog.Error("member record close during migration failed", slog.Any("error", cerr))
		}
	}
	if srv != nil {
		srv.Stop()
	}
	// Stop the service layer to release the Pebble locks it holds on the
	// data-plane stores that live OUTSIDE the Raft host — notably the vector
	// store — so they can be re-encrypted. Without this the recode fails with
	// "lock held by current process".
	if svcNode != nil {
		if cerr := svcNode.Stop(context.Background()); cerr != nil {
			slog.Error("service node stop during migration failed", slog.Any("error", cerr))
		}
	}
	// Release locks on the system host's stores: dedicated-db raft shards and
	// per-tenant Hosts first (they may root under the same data tree), then the
	// host itself (this seals the shared barrier underlying sealSub.SealBarrier).
	if raftBackendReg != nil {
		if cerr := raftBackendReg.CloseAll(); cerr != nil {
			slog.Error("dedicated raft shard close during migration failed", slog.Any("error", cerr))
		}
	}
	if tenantHostReg != nil {
		if cerr := tenantHostReg.CloseAll(); cerr != nil {
			slog.Error("tenant host close during migration failed", slog.Any("error", cerr))
		}
	}
	if host != nil {
		_ = shutdownNode(host)
	}

	// The stores wrapped by the system barrier OUTSIDE the Raft WAL. Every one
	// must be re-encrypted or the keyed re-init fails to open it ("decryption
	// failed").
	stores := systemMigrationStores(f.dataDir, f.smType, f.smStorage)
	walDir := filepath.Join(f.dataDir, "wal")

	rerr := recodeSystemBarrier(context.Background(), sealSub.SealBarrier, stores, walDir, req.opts)
	// The recycle has consumed the unlock credential; destroy the credential the
	// reconciler handed off (the system-scope path owns it — the reconciler can't
	// destroy it because this recycle runs async across the in-process reboot).
	seal.DestroyCredentials(req.opts.Credentials)
	req.done <- rerr
	if rerr != nil {
		// Do NOT reboot into a half-migrated state. The crash-safe journal plus
		// RecoverInterruptedMigration on the next manual restart reconcile the
		// stores; surface the error so the operator restarts deliberately.
		slog.Error("system encryption migration failed; daemon will exit (restart to reconcile via the migration journal)",
			slog.Any("error", rerr))
		return false, rerr
	}
	slog.Info("system encryption migration committed; re-initializing data plane in-process",
		slog.String("strategy", string(req.opts.TargetStrategy)))
	return true, nil
}

// existingDirs returns the subset of the given paths that exist as directories.
// selfStoredSMTypes are the --sm types that keep their state in a Pebble
// store of their own inside the host's snapshot directory for the shard. Every
// other type is either in memory or storage-generic, keeping its state under
// <data-dir>/sm in the --sm-storage backend.
var selfStoredSMTypes = map[string]bool{
	"pebble": true,
	"mvcc":   true,
}

// systemMigrationStores names the system host's at-rest stores outside the
// Raft WAL, by the kind of store each is: the KV state machine's own Pebble
// store (pebble and mvcc) or the storage-generic state machines' per-replica
// stores under <data-dir>/sm, plus the vector store, which
// lifecycle.BuildServices opens through the same atrest Encryptor. Only
// directories that exist are named: vector is absent when the qrdb_vector
// build tag is off or no vector was ever written, and --sm-storage memory
// writes nothing. Any other snapshot directory is not a store; its snapshot
// data files are re-encrypted with the WAL.
func systemMigrationStores(dataDir, smType, smStorage string) service.MigrationStores {
	var stores service.MigrationStores
	if selfStoredSMTypes[smType] {
		stores.Pebble = existingDirs(filepath.Join(dataDir, "nodehost", "snapshots", fmt.Sprintf("shard-%d", shard.ShardKV)))
	} else {
		// The pattern is constant and well-formed, so Glob cannot fail.
		replicas, _ := filepath.Glob(filepath.Join(dataDir, "sm", "shard-*-replica-*"))
		if smStorage == "file" {
			stores.File = existingDirs(replicas...)
		} else {
			stores.Pebble = existingDirs(replicas...)
		}
	}
	stores.Pebble = append(stores.Pebble, existingDirs(filepath.Join(dataDir, "vector"))...)
	return stores
}

func existingDirs(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// recodeSystemBarrier re-encrypts the system barrier's at-rest stores from the
// source (none) barrier into a freshly-minted keyed barrier, then atomically
// swaps the keyed copies into place and adopts the keyed root key as canonical.
// stores are the encfs stores outside the WAL, named by systemMigrationStores;
// walDir is the Raft write-ahead log. The host
// MUST already be closed by the caller (its Pebble/WAL locks released). It
// mirrors the per-tenant recycle in pkg/server/tenant_migrate.go.
func recodeSystemBarrier(ctx context.Context, source *seal.StorageBarrier, stores service.MigrationStores, walDir string, opts seal.MigrationOptions) (err error) {
	if source == nil {
		return service.ErrMigrationProviderUnavailable
	}
	if verr := opts.Validate(); verr != nil {
		return verr
	}

	// Closing the host sealed the quicraft barrier underlying `source` (the host
	// owns the lifecycle of the barrier given via WithBarrier and zeroes its keys
	// on Close), desyncing the StorageBarrier wrapper. Re-sync (Seal) then
	// re-unseal so the at-rest re-encryption can read the unencrypted source. The
	// source is always StrategyNone (Validate guarantees a keyed target from a
	// none source), so it unseals with empty credentials.
	_ = source.Seal()
	if uerr := source.Unseal(ctx, seal.Credentials{}); uerr != nil && !errors.Is(uerr, seal.ErrAlreadyUnsealed) {
		return fmt.Errorf("re-unseal source barrier: %w", uerr)
	}

	var target *seal.StorageBarrier
	if opts.TargetStrategy == seal.StrategyShamir {
		// The Shamir target cannot go through the generic MintKeyedSibling path
		// (see its doc comment): it needs the direct-mode ceremony, which mints
		// AND splits the root key to the enrolled Security Officers in one step.
		target, _, err = source.MintKeyedShamirSibling(ctx, systemMigrateStagingSuffix, opts.ShamirThreshold, opts.ShamirTotal)
		if err != nil {
			return fmt.Errorf("mint keyed shamir sibling: %w", err)
		}
	} else {
		mintStrat, serr := keyedSystemStrategy(opts)
		if serr != nil {
			return serr
		}
		target, err = source.MintKeyedSibling(ctx, systemMigrateStagingSuffix, mintStrat, opts.Credentials)
		if err != nil {
			return fmt.Errorf("mint keyed sibling: %w", err)
		}
	}
	// Always remove the staging keyed root-key blob + zero its key enclave on
	// return (success: redundant after adopt; failure: rollback). Mint is itself
	// idempotent against a leftover blob; this keeps the backend clean.
	defer func() { _ = target.DeleteRootKey(ctx) }()
	defer func() { _ = target.Close() }()

	storeDirs := stores.Dirs()
	j, err := source.BeginMigration(ctx, opts)
	if err != nil {
		return err
	}
	j.StagingRootKeyPath = target.RootKeyPath()
	// Record the directories the commit swaps so the boot-time crash-recovery
	// pass can roll the swaps forward (or back) from the journal alone, even if
	// the configured store layout changes before the interrupted migration is
	// reconciled.
	j.StoreDirs = append(append([]string(nil), storeDirs...), walDir)
	// stagedSnaps accumulates re-encrypted snapshot data files staged beside
	// their originals in a snapshot directory that no store swap below moves.
	// They are renamed into place after the swaps commit, or discarded on any
	// pre-commit failure.
	var stagedSnaps []database.StagedSnapshot
	// Registered BEFORE the first CheckpointMigration so any post-begin failure
	// rolls the journal to terminal PhaseFailed — else a leftover non-terminal
	// journal wedges the next BeginMigration (manual restart) with
	// ErrMigrationInProgress.
	defer func() {
		if err != nil && !j.Committed() {
			database.RollbackStagedSnapshots(stagedSnaps)
			for _, dir := range storeDirs {
				_ = os.RemoveAll(dir + database.KeyedDirSuffix)
			}
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

	srcEnc := atrest.New(source)
	dstEnc := atrest.New(target)
	recode := database.ReencryptPebbleStore
	if opts.Mechanism == seal.MechanismInPlace {
		recode = database.ReencryptStoreFiles
	}

	// Copy phase: re-encrypt every store + the WAL into keyed siblings. A file
	// store is copied through the file engine, record by record, under either
	// mechanism.
	j.Phase = seal.PhaseCopying
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	for _, dir := range stores.Pebble {
		keyed := dir + database.KeyedDirSuffix
		_ = os.RemoveAll(keyed)
		if _, cerr := recode(ctx, dir, srcEnc, keyed, dstEnc); cerr != nil {
			err = fmt.Errorf("re-encrypt store %q: %w", dir, cerr)
			return err
		}
	}
	for _, dir := range stores.File {
		keyed := dir + database.KeyedDirSuffix
		_ = os.RemoveAll(keyed)
		if _, cerr := database.ReencryptFileStore(ctx, dir, srcEnc, keyed, dstEnc); cerr != nil {
			err = fmt.Errorf("re-encrypt file store %q: %w", dir, cerr)
			return err
		}
	}
	walKeyed := walDir + database.KeyedDirSuffix
	_ = os.RemoveAll(walKeyed)
	// A snapshot directory under a recoded store (the pebble and mvcc state
	// machines keep theirs under nodehost/snapshots/shard-N) has its data file
	// re-encrypted into the matching path inside that store's keyed staging
	// dir; the SwapStoreDir below makes it live and a rollback discards it with
	// the staging dir. Any other snapshot (an in-memory or storage-generic state
	// machine's) is staged as a keyed sibling and committed after the swaps.
	swappedDir := func(snapDir string) (string, bool) {
		for _, d := range storeDirs {
			if snapDir == d || strings.HasPrefix(snapDir, d+string(filepath.Separator)) {
				return d, true
			}
		}
		return "", false
	}
	snapDest := func(snapDir string) string {
		if d, ok := swappedDir(snapDir); ok {
			return filepath.Join(strings.Replace(snapDir, d, d+database.KeyedDirSuffix, 1), database.SnapshotDataFile)
		}
		return filepath.Join(snapDir, database.SnapshotDataFile) + database.SnapshotKeyedSuffix
	}
	_, staged, cerr := database.ReencryptWALStore(walDir, source, walKeyed, target, snapDest)
	if cerr != nil {
		err = fmt.Errorf("re-encrypt write-ahead log: %w", cerr)
		return err
	}
	for _, s := range staged {
		if _, ok := swappedDir(filepath.Dir(s.FinalPath)); !ok {
			stagedSnaps = append(stagedSnaps, s)
		}
	}
	// Checkpoint the staged snapshot set so a crash from here on leaves the
	// journal aware of every sibling: recovery commits them on roll-forward and
	// discards them on roll-back.
	j.StagedSnapshots = stagedSnaps
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}

	// Verify each store round-trip (no data loss). The WAL is verified
	// implicitly by the host reopen on the next boot iteration, which replays it
	// into the keyed state machine.
	j.Phase = seal.PhaseVerifying
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	for _, dir := range stores.Pebble {
		if verr := database.VerifyReencryptedStore(ctx, dir, srcEnc, dir+database.KeyedDirSuffix, dstEnc); verr != nil {
			err = fmt.Errorf("verify re-encrypted store %q: %w", dir, verr)
			return err
		}
	}
	for _, dir := range stores.File {
		if verr := database.VerifyReencryptedFileStore(ctx, dir, srcEnc, dir+database.KeyedDirSuffix, dstEnc); verr != nil {
			err = fmt.Errorf("verify re-encrypted file store %q: %w", dir, verr)
			return err
		}
	}

	// Commit (point of no return): swap dirs, commit the staged snapshots,
	// adopt the keyed root key.
	j.Phase = seal.PhaseCommitting
	if cerr := source.CheckpointMigration(ctx, j); cerr != nil {
		err = cerr
		return err
	}
	for _, dir := range storeDirs {
		if serr := database.SwapStoreDir(dir); serr != nil {
			err = fmt.Errorf("swap store %q: %w", dir, serr)
			return err
		}
	}
	if serr := database.SwapStoreDir(walDir); serr != nil {
		err = fmt.Errorf("swap write-ahead log: %w", serr)
		return err
	}
	// The swapped WAL metadata now expects the keyed snapshots, so a transient
	// rename failure is retried rather than left with the live snapshots under
	// the old key.
	if cerr := database.CommitStagedSnapshotsWithRetry(ctx, stagedSnaps, systemSnapshotCommitAttempts, systemSnapshotCommitBackoff); cerr != nil {
		err = fmt.Errorf("commit re-encrypted snapshots: %w", cerr)
		return err
	}
	if aerr := source.AdoptMigratedRootKey(ctx, target); aerr != nil {
		err = aerr
		return err
	}
	for _, dir := range storeDirs {
		database.RemoveStoreBackup(dir)
	}
	database.RemoveStoreBackup(walDir)

	j.Phase = seal.PhaseComplete
	_ = source.CheckpointMigration(ctx, j)
	_ = source.FinishMigration(ctx)
	return nil
}

// systemMigrateStagingSuffix names the staging root-key path for a system
// migration's keyed target barrier.
const systemMigrateStagingSuffix = ".migrating"

// systemSnapshotCommitAttempts and systemSnapshotCommitBackoff bound the retry
// of committing a system migration's staged snapshots once the WAL is swapped.
const (
	systemSnapshotCommitAttempts = 5
	systemSnapshotCommitBackoff  = 50 * time.Millisecond
)

// keyedSystemStrategy builds the keyed sealing strategy for a PASSPHRASE-target
// system migration, using the same deferred passphrase strategy the boot path
// registers so the sealed root-key blob is unsealable after the in-process
// re-init (and after any later process restart). A Shamir target does not go
// through this helper — see recodeSystemBarrier's MintKeyedShamirSibling
// branch and the doc comment on MintKeyedSibling for why a self-contained
// SealingStrategy cannot represent Shamir's ceremony.
func keyedSystemStrategy(opts seal.MigrationOptions) (seal.SealingStrategy, error) {
	switch opts.TargetStrategy {
	case seal.StrategyPassphrase:
		return seal.NewDeferredSoftwareStrategy(), nil
	default:
		return nil, seal.ErrMigrationInvalidTarget
	}
}
