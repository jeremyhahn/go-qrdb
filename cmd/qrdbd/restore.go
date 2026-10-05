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
	"log/slog"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// restoreShardFromBackup performs an OFFLINE, one-shot disaster-recovery
// restore of shardCfg.ShardID from a durable backup, before the shard is
// ever exposed to client traffic this boot.
//
// # Why offline
//
// *quicraft.Host.ImportSnapshot refuses two things, both deliberate Raft
// safety invariants (go-quicraft/pkg/host.go): it refuses to import into a
// shard that is currently RUNNING, and it refuses an artifact whose index
// does not ADVANCE the shard's recorded snapshot. A live, in-place restore
// therefore requires stopping the target shard first — but on this daemon's
// default single-shard deployment the same shard hosts the system tenant's
// auth/user data, so stopping it while the server is serving requests takes
// down authentication for every subsequent admin call, including the one
// that would restart it (see test/integration/backup_cli_test.go's header
// for the empirically confirmed deadlock). Performing the restore BEFORE any
// API listener opens — while nothing has authenticated and nothing is
// mid-request — sidesteps that deadlock entirely without weakening either
// quicraft invariant.
//
// # Sequence
//
//  1. Verify the backup's checksum (fail closed on a corrupt/incomplete
//     artifact before touching the shard at all).
//  2. Bootstrap the shard normally (StartShard). A shard the host has never
//     started has no LogDB bootstrap record, and ImportSnapshot refuses an
//     unrecorded shard (ErrShardNotFound) exactly like a running one — this
//     also covers the true "brand new / totally lost node" disaster-recovery
//     case, where the on-disk LogDB is itself empty.
//  3. Stop it (ImportSnapshot's "not running" precondition).
//  4. Import the backup artifact for this shard. An artifact that does not
//     advance the shard's already-recorded snapshot (logdb.ErrSnapshotOutOfDate,
//     e.g. a repeated invocation of this same flag against an
//     already-restored shard) is treated as an idempotent no-op — the shard
//     already carries the data this restore would have produced — rather
//     than a fatal error, so a stale --restore-backup-id left on an operator
//     script does not wedge the next boot. Any other import failure aborts
//     the boot rather than start the shard from a half-imported or corrupt
//     state.
//  5. Start the shard again, now recovering the imported (or, in the
//     idempotent-skip case, already-restored) state, and hand control back to
//     the normal boot sequence.
func restoreShardFromBackup(ctx context.Context, f *startFlags, host *quicraft.Host, createFn sm.CreateFunc, shardCfg quicraftconfig.Config, complianceAudit audit.AuditLogger, auditPolicy audit.FailurePolicy) error {
	if f.backupDir == "" {
		return &RestoreBackupSourceRequiredError{}
	}

	backupStore, err := file.New(f.backupDir)
	if err != nil {
		return &RestoreBackupError{Phase: "open-backup-store", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: err}
	}
	defer func() { _ = backupStore.Close() }()

	sink := backupsink.New(backupStore)
	provider := service.NewHostShardSnapshotProvider(newHostAdapter(host))
	// The compliance audit trail is already open by the time boot reaches
	// here (start.go opens it before the seal subsystem); threading it
	// and this node's audit failure posture through means a plaintext
	// restore on THIS path is recorded exactly like the live daemon's own
	// RestoreBackup/DecryptBackupShard calls, instead of silently having
	// no audit logger at all (go-qrdb security review,
	// backup-encrypt-default #B5).
	backupOpts := []service.BackupServiceOption{
		service.WithBackupSink(sink),
		service.WithKeyDir(f.backupKeyDir),
		service.WithAuditLogger(complianceAudit),
		service.WithAuditFailurePolicy(auditPolicy),
	}
	// Shared with this daemon's live BuildServices call (start.go) so an
	// embedder's injected key-wrap provider is honored on the offline
	// restore path too, not only the live one (go-qrdb security review,
	// backup-encrypt-default #B6).
	if keyWrapInjection.registry != nil {
		backupOpts = append(backupOpts, service.WithKeyWrapRegistry(keyWrapInjection.registry))
	}
	if keyWrapInjection.provider != nil {
		backupOpts = append(backupOpts, service.WithKeyWrapProvider(keyWrapInjection.provider))
	}
	backupSvc, err := service.NewBackupService(provider, backupOpts...)
	if err != nil {
		return &RestoreBackupError{Phase: "open-backup-store", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: err}
	}
	// NewBackupService always returns *service.BackupService; the type
	// assertion only reaches DecryptBackupShard, a capability outside the
	// BackupServicer interface (see that method's own doc comment for why).
	bs, ok := backupSvc.(*service.BackupService)
	if !ok {
		return &RestoreBackupError{Phase: "open-backup-store", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: &RestoreBackupServiceTypeError{}}
	}
	// This one-shot service is discarded once this function returns;
	// release its cached "software" key-wrap provider's *os.Root (if the
	// default registry ever resolved one) rather than leaking it for the
	// remaining life of this boot.
	defer func() { _ = bs.Close() }()

	slog.Info("offline restore: verifying backup", "backup_id", f.restoreBackupID, "shard_id", shardCfg.ShardID)
	verifyResult, err := backupSvc.VerifyBackup(ctx, f.restoreBackupID)
	if err != nil {
		return &RestoreBackupError{Phase: "verify", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: err}
	}
	if !verifyResult.Valid {
		return &RestoreBackupVerifyError{BackupID: f.restoreBackupID, Result: *verifyResult}
	}

	// DecryptBackupShard resolves and unwraps the backup's envelope (a
	// no-op for an EncryptionNone backup) and returns this shard's
	// plaintext -- reading shard bytes any other way here would import
	// ciphertext directly for an encrypted backup.
	data, err := bs.DecryptBackupShard(ctx, f.restoreBackupID, shardCfg.ShardID, f.restoreDecryptionKeyRef)
	if err != nil {
		// Only a shard genuinely absent from the artifact is reported as
		// "shard not in artifact" -- every other failure (a wrong key, a
		// tampered shard or manifest, a metadata/artifact mismatch, an
		// unregistered provider) is a decrypt-phase failure and is
		// reported as such, not collapsed into the same generic message
		// (go-qrdb security review, backup-encrypt-default; previously
		// every non-*BackupDecryptionFailedError here, including these,
		// was mapped to "shard not in artifact").
		var notFound *service.BackupShardNotFoundError
		if errors.As(err, &notFound) {
			return &RestoreBackupShardNotInArtifactError{BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID}
		}
		return &RestoreBackupError{Phase: "decrypt", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: err}
	}

	slog.Info("offline restore: bootstrapping shard before import", "backup_id", f.restoreBackupID, "shard_id", shardCfg.ShardID)
	if err := startShard(ctx, f, host, createFn, shardCfg); err != nil {
		return &RestoreBackupError{Phase: "bootstrap", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: err}
	}
	if err := host.StopShard(shardCfg.ShardID); err != nil {
		return &RestoreBackupError{Phase: "stop", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: err}
	}

	slog.Info("offline restore: importing backup artifact", "backup_id", f.restoreBackupID, "shard_id", shardCfg.ShardID, "bytes", len(data))
	if err := provider.RestoreShard(ctx, shardCfg.ShardID, data); err != nil {
		if errors.Is(err, logdb.ErrSnapshotOutOfDate) {
			slog.Warn("offline restore: backup does not advance the shard's already-recorded snapshot; "+
				"treating as already applied and starting from the existing state",
				"backup_id", f.restoreBackupID, "shard_id", shardCfg.ShardID)
		} else {
			return &RestoreBackupError{Phase: "import", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: err}
		}
	}

	slog.Info("offline restore: starting shard from restored state", "backup_id", f.restoreBackupID, "shard_id", shardCfg.ShardID)
	if err := startShard(ctx, f, host, createFn, shardCfg); err != nil {
		return &RestoreBackupError{Phase: "restart", BackupID: f.restoreBackupID, ShardID: shardCfg.ShardID, Err: err}
	}

	slog.Info("offline restore: complete", "backup_id", f.restoreBackupID, "shard_id", shardCfg.ShardID)
	return nil
}
