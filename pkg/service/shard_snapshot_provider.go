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

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// Default bounds for the ExportSnapshot poll loop in exportSavedSnapshot.
// SyncRequestSnapshot normally makes the snapshot LogDB-durable before it
// returns (see the snapshotExporter doc comment below), so under normal
// operation the first export attempt succeeds and these bounds are never
// exercised. They exist only to cover the residual race where a
// CONCURRENT snapshot request already held the shard's snapshot slot when
// SnapshotShard called SyncRequestSnapshot, so our own request could not
// run and returned ErrSnapshotInProgress instead of a fresh index.
const (
	defaultSnapshotPollInterval = 100 * time.Millisecond
	defaultSnapshotPollTimeout  = 10 * time.Second
)

// snapshotExporter is the subset of the Raft host that requests, exports,
// and imports per-shard snapshot artifacts. The clustered host
// (raft.HostAdapter, which embeds *quicraft.Host) satisfies it via method
// promotion; the single-node LocalBackend does not, so backup over a
// non-Raft host is reported as unsupported rather than silently producing
// empty backups.
type snapshotExporter interface {
	// SyncRequestSnapshot MUST match *quicraft.Host.SyncRequestSnapshot's
	// signature exactly — including the SnapshotOption parameter — or the
	// clustered raft.HostAdapter (which promotes the embedded Host's
	// method) fails to satisfy this interface, making the p.host type
	// assertion silently false and every backup return
	// BackupUnsupportedError. SnapshotShard calls this BEFORE
	// ExportSnapshot: a freshly created shard, or one with too few
	// applies to have crossed Raft's own auto-snapshot threshold, has no
	// saved snapshot yet, and ExportSnapshot on such a shard fails with
	// quicraft.ErrSnapshotNotFound. quicraft's snapshot worker persists
	// the snapshot to LogDB (logdb.SaveSnapshot) BEFORE resolving the
	// RequestState that SyncRequestSnapshot blocks on, so a successful
	// return here guarantees the snapshot ExportSnapshot needs already
	// exists on disk — no race, no poll required in the common case. A
	// second back-to-back call with no writes in between returns
	// logdb.ErrSnapshotOutOfDate (nothing new to save); ensureSnapshot
	// treats that as success too, since the earlier call's snapshot is
	// still current and exportable.
	SyncRequestSnapshot(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (uint64, error)

	// ExportSnapshot exports the shard's most recently SAVED snapshot.
	ExportSnapshot(ctx context.Context, shardID uint64, w io.Writer) error

	// ImportSnapshot MUST match *quicraft.Host.ImportSnapshot's signature
	// exactly — including the trailing variadic ...quicraft.ImportOption —
	// or the clustered raft.HostAdapter (which promotes the embedded Host's
	// method) fails to satisfy this interface, making the p.host type
	// assertion silently false and every backup/restore return
	// BackupUnsupportedError. Callers pass no options (default import).
	ImportSnapshot(ctx context.Context, shardID uint64, r io.Reader, opts ...quicraft.ImportOption) error
}

// ShardRestarter is implemented by a component that owns a dedicated shard's
// full lifecycle — its start configuration (voting membership, state-machine
// factory, on-disk paths, any dedicated WAL registration) as well as its
// stop/cleanup — enabling provider-driven Stop -> Import -> Restart
// sequencing for ONLINE, single-replica backup restore. It is satisfied by
// server.RaftBackendFactory for the dedicated per-database Raft shards it
// manages on the shared host; the system shard is never in scope (see
// RestoreShardOnline's system-shard guard) and is never routed here.
//
// A shard restarter is wired into a host-backed ShardSnapshotProvider via
// SetShardRestarter, typically AFTER the provider itself was constructed:
// the daemon and SDK node builders construct the IsolationRaft factory only
// after the core service graph (which includes the backup service) already
// exists.
type ShardRestarter interface {
	// StopForRestore stops shardID in preparation for an in-place snapshot
	// import, releasing any lifecycle-owned resources (e.g. a dedicated
	// per-tenant WAL registration) that a bare Host.StopShard would leave
	// attached. It returns a typed "not managed" error when shardID is not
	// one this restarter owns.
	StopForRestore(shardID uint64) error

	// RestartAfterRestore restarts shardID using the exact start
	// configuration (voting membership, state-machine factory, on-disk
	// paths) it was originally launched with. The caller MUST have called
	// StopForRestore for the same shardID first, whether or not the import
	// in between succeeded — RestartAfterRestore is also the rollback path
	// for a failed import.
	RestartAfterRestore(ctx context.Context, shardID uint64) error
}

// OnlineShardRestorer is optionally implemented by a ShardSnapshotProvider to
// support restoring a shard that is still RUNNING. BackupService.RestoreBackup
// prefers it over the plain RestoreShard when the configured provider
// implements it; providers that don't (e.g. simple test doubles, or a
// host-backed provider with no ShardRestarter configured) fall back to
// RestoreShard, which requires the shard to already be stopped (the OFFLINE
// restore pattern in cmd/qrdbd/restore.go).
type OnlineShardRestorer interface {
	// RestoreShardOnline performs a SAFETY-GUARDED online restore of shardID.
	// See hostShardSnapshotProvider.RestoreShardOnline for the full safety
	// rationale and guard behavior.
	RestoreShardOnline(ctx context.Context, shardID uint64, data []byte) error
}

// hostShardSnapshotProvider implements ShardSnapshotProvider over a
// service.Host. Snapshots are self-describing artifacts produced by the host's
// ExportSnapshot (including the epoch/barrier metadata needed for at-rest
// encrypted shards); RestoreShard feeds them back through ImportSnapshot.
type hostShardSnapshotProvider struct {
	host         AdminHost
	pollInterval time.Duration
	pollTimeout  time.Duration

	// mu guards restarter, which may be wired in after construction (see
	// SetShardRestarter) once the caller's IsolationRaft infrastructure
	// exists.
	mu        sync.RWMutex
	restarter ShardRestarter
}

// HostShardSnapshotProviderOption configures optional
// hostShardSnapshotProvider behavior.
type HostShardSnapshotProviderOption func(*hostShardSnapshotProvider)

// WithSnapshotPollInterval overrides the interval between ExportSnapshot
// retries while SnapshotShard waits for a concurrently requested snapshot
// to land in LogDB (see snapshotExporter). Production callers should use
// the default; this exists primarily so tests do not have to wait out the
// full default poll timeout.
func WithSnapshotPollInterval(d time.Duration) HostShardSnapshotProviderOption {
	return func(p *hostShardSnapshotProvider) { p.pollInterval = d }
}

// WithSnapshotPollTimeout overrides the total time SnapshotShard waits for
// a requested snapshot to become exportable before failing with
// ShardSnapshotTimeoutError. Production callers should use the default;
// this exists primarily so tests do not have to wait out the full default
// poll timeout.
func WithSnapshotPollTimeout(d time.Duration) HostShardSnapshotProviderOption {
	return func(p *hostShardSnapshotProvider) { p.pollTimeout = d }
}

// WithShardRestarter wires a ShardRestarter at construction time, enabling
// ONLINE restore (see RestoreShardOnline) for the shards it manages. Most
// production callers cannot use this option — their IsolationRaft factory is
// built after the backup service — and must call the package-level
// SetShardRestarter (backup_service.go) once it exists instead. This option
// exists for tests and for any future caller that has the restarter
// available up front.
func WithShardRestarter(r ShardRestarter) HostShardSnapshotProviderOption {
	return func(p *hostShardSnapshotProvider) { p.restarter = r }
}

// NewHostShardSnapshotProvider returns a ShardSnapshotProvider backed by the
// given Raft host. It is the production provider wired into BackupService by
// lifecycle.BuildServices when a snapshot-capable (clustered) host is present.
func NewHostShardSnapshotProvider(host AdminHost, opts ...HostShardSnapshotProviderOption) ShardSnapshotProvider {
	p := &hostShardSnapshotProvider{
		host:         host,
		pollInterval: defaultSnapshotPollInterval,
		pollTimeout:  defaultSnapshotPollTimeout,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// AvailableShards returns the IDs of every shard currently loaded on the host.
func (p *hostShardSnapshotProvider) AvailableShards(_ context.Context) ([]uint64, error) {
	info := p.host.NodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
	if info == nil {
		return nil, nil
	}
	shards := make([]uint64, 0, len(info.ShardInfoList))
	for _, si := range info.ShardInfoList {
		shards = append(shards, si.ShardID)
	}
	return shards, nil
}

// SnapshotShard captures a self-describing snapshot artifact for the shard.
// It first ensures a Raft snapshot exists — a freshly created shard, or one
// below Raft's own auto-snapshot threshold, has nothing saved yet — then
// exports it. See snapshotExporter for why the request/export pairing is
// race-free under normal operation.
func (p *hostShardSnapshotProvider) SnapshotShard(ctx context.Context, shardID uint64) ([]byte, error) {
	exporter, ok := p.host.(snapshotExporter)
	if !ok {
		return nil, &BackupUnsupportedError{Op: "snapshot", ShardID: shardID}
	}

	if err := p.ensureSnapshot(ctx, exporter, shardID); err != nil {
		return nil, err
	}

	return p.exportSavedSnapshot(ctx, exporter, shardID)
}

// ensureSnapshot triggers a Raft snapshot for shardID and waits for it to
// be saved to LogDB. Two outcomes are tolerated as "a usable snapshot
// already exists, proceed to export" rather than failures:
//
//   - quicraft.ErrSnapshotInProgress: a concurrent snapshot request (e.g.
//     Raft's own log-size-triggered auto-snapshot) already holds the
//     shard's snapshot slot, so a snapshot is being produced regardless of
//     this call; exportSavedSnapshot's poll loop covers the residual wait
//     for it to land.
//   - logdb.ErrSnapshotOutOfDate: nothing has been applied to the shard
//     since its last saved snapshot, so the save attempt this call
//     triggered did not advance the recorded index and LogDB correctly
//     refused it — the EXISTING snapshot (from an earlier SnapshotShard
//     call, e.g. a back-to-back backup with no writes in between) is still
//     current and exportable as-is.
func (p *hostShardSnapshotProvider) ensureSnapshot(ctx context.Context, exporter snapshotExporter, shardID uint64) error {
	_, err := exporter.SyncRequestSnapshot(ctx, shardID, quicraft.SnapshotOption{})
	if err == nil || errors.Is(err, quicraft.ErrSnapshotInProgress) || errors.Is(err, logdb.ErrSnapshotOutOfDate) {
		return nil
	}
	return &ShardSnapshotError{Op: "request", ShardID: shardID, Err: err}
}

// exportSavedSnapshot exports shardID's most recently saved snapshot,
// polling on quicraft.ErrSnapshotNotFound up to p.pollTimeout. Under normal
// operation ensureSnapshot's SyncRequestSnapshot call has already made the
// snapshot durable before returning, so the first attempt succeeds; the
// poll loop exists only for the concurrent-snapshot case ensureSnapshot
// tolerated above. Each attempt exports into a fresh buffer so a
// not-found retry can never see a partially written prior attempt.
func (p *hostShardSnapshotProvider) exportSavedSnapshot(ctx context.Context, exporter snapshotExporter, shardID uint64) ([]byte, error) {
	deadline := time.Now().Add(p.pollTimeout)
	for {
		var buf bytes.Buffer
		err := exporter.ExportSnapshot(ctx, shardID, &buf)
		if err == nil {
			return buf.Bytes(), nil
		}
		if !errors.Is(err, quicraft.ErrSnapshotNotFound) {
			return nil, &ShardSnapshotError{Op: "export", ShardID: shardID, Err: err}
		}
		if !time.Now().Before(deadline) {
			return nil, &ShardSnapshotTimeoutError{ShardID: shardID, Timeout: p.pollTimeout}
		}
		select {
		case <-ctx.Done():
			return nil, &ShardSnapshotError{Op: "export", ShardID: shardID, Err: ctx.Err()}
		case <-time.After(p.pollInterval):
		}
	}
}

// RestoreShard restores a shard from a previously captured snapshot artifact.
func (p *hostShardSnapshotProvider) RestoreShard(ctx context.Context, shardID uint64, data []byte) error {
	exporter, ok := p.host.(snapshotExporter)
	if !ok {
		return &BackupUnsupportedError{Op: "restore", ShardID: shardID}
	}
	if err := exporter.ImportSnapshot(ctx, shardID, bytes.NewReader(data)); err != nil {
		return &ShardSnapshotError{Op: "import", ShardID: shardID, Err: err}
	}
	return nil
}

// Compile-time interface assertion: hostShardSnapshotProvider supports the
// guarded online restore path.
var _ OnlineShardRestorer = (*hostShardSnapshotProvider)(nil)

// RestoreShardOnline performs a SAFETY-GUARDED restore of a shard that is
// still RUNNING. *quicraft.Host.ImportSnapshot unconditionally refuses to
// import into a running shard (a deliberate Raft safety invariant — see
// RestoreShard/cmd/qrdbd/restore.go), so an online restore must stop the
// shard, import, and restart it in place. That stop-import-restart sequence
// is only SAFE and EFFECTIVE for a non-system shard where this node holds
// the sole voting replica:
//
//   - The SYSTEM shard (shard.ShardKV) hosts the system tenant's auth/user
//     data. Stopping it while the server is serving requests takes down the
//     data plane that authenticates and would restart it — including this
//     very admin call — so this is refused unconditionally in favor of the
//     OFFLINE `qrdbd start --restore-backup-id` boot flow.
//   - A shard with more than one voting replica (RF>1) would have this
//     node's freshly imported state immediately overwritten by the
//     existing majority on the next heartbeat/append — an online per-replica
//     import is silently ineffective, not merely risky. A safe multi-node
//     restore requires an offline whole-cluster procedure instead.
//
// For an eligible (non-system, RF=1) shard: StopForRestore -> ImportSnapshot
// -> RestartAfterRestore. If ImportSnapshot fails for any reason OTHER than
// the idempotent "already applied" case (logdb.ErrSnapshotOutOfDate — the
// artifact does not advance the shard's already-recorded snapshot, so the
// shard's existing state is exactly what the restore would have produced),
// the shard is restarted before the error is returned, so a failed online
// restore never leaves the shard down.
//
// # What "restored" means for a shard's OWN prior backup
//
// *quicraft.Host.ImportSnapshot only ever installs different data when the
// artifact ADVANCES the target replica's own recorded snapshot index, and a
// successful import compacts local log entries only AT OR BELOW that index
// — it never discards entries above it (go-quicraft never rewrites Raft
// history out from under committed entries; that would violate the log's
// durability guarantee for anything that already read them). Consequently,
// re-importing a shard's OWN earlier backup onto ITSELF cannot roll back
// writes committed after that backup was taken: the recorded snapshot never
// exceeds the backup's own index, ImportSnapshot correctly reports the
// idempotent "already applied" case, and normal recovery replays whatever
// the shard's log already holds — the CURRENT state, not the backup's. This
// is a deliberate safety property, not a limitation of this function: an
// online restore must never silently discard committed data. RestoreShardOnline
// genuinely installs different data in the disaster-recovery case it is
// designed for — restoring a backup onto a shard (or a replacement replica
// for the same shard ID) that has no snapshot of its own newer than the
// artifact, e.g. after real data loss.
func (p *hostShardSnapshotProvider) RestoreShardOnline(ctx context.Context, shardID uint64, data []byte) error {
	exporter, ok := p.host.(snapshotExporter)
	if !ok {
		return &BackupUnsupportedError{Op: "restore", ShardID: shardID}
	}

	if shardID == shard.ShardKV {
		return &OnlineRestoreSystemShardError{ShardID: shardID}
	}

	// The RF=1 guard: an in-place, per-replica snapshot import on a shard with
	// more than one voting replica is silently overwritten by the majority on
	// the next heartbeat/append, so it must never be attempted. The
	// membership read is linearizable, and a failed one fails closed.
	membership, err := p.host.SyncGetShardMembership(ctx, shardID)
	if err != nil {
		return &ShardSnapshotError{Op: "membership", ShardID: shardID, Err: err}
	}
	// Voting members are full replicas (Nodes) plus witnesses (Witnesses,
	// which count toward quorum without carrying a state machine).
	// Observers do not vote and are excluded.
	votingReplicas := len(membership.Nodes) + len(membership.Witnesses)
	if votingReplicas > 1 {
		return &OnlineRestoreReplicatedShardError{ShardID: shardID, VotingReplicas: votingReplicas}
	}

	p.mu.RLock()
	restarter := p.restarter
	p.mu.RUnlock()
	if restarter == nil {
		return &OnlineRestoreUnsupportedError{
			ShardID: shardID,
			Reason:  "no shard restarter is configured for this host",
		}
	}

	if err := restarter.StopForRestore(shardID); err != nil {
		return &ShardSnapshotError{Op: "stop", ShardID: shardID, Err: err}
	}

	importErr := exporter.ImportSnapshot(ctx, shardID, bytes.NewReader(data))
	if importErr != nil && !errors.Is(importErr, logdb.ErrSnapshotOutOfDate) {
		// Roll back: the shard must never be left stopped because a restore
		// attempt failed.
		if restartErr := restarter.RestartAfterRestore(ctx, shardID); restartErr != nil {
			return &OnlineRestoreRollbackError{ShardID: shardID, ImportErr: importErr, RestartErr: restartErr}
		}
		return &ShardSnapshotError{Op: "import", ShardID: shardID, Err: importErr}
	}

	if err := restarter.RestartAfterRestore(ctx, shardID); err != nil {
		return &ShardSnapshotError{Op: "restart", ShardID: shardID, Err: err}
	}
	return nil
}

// BackupUnsupportedError is returned when a backup/restore operation targets a
// host that cannot produce shard snapshots — e.g. the single-node LocalBackend,
// which has no Raft log to snapshot. Backup/restore requires the clustered
// (Raft) host.
type BackupUnsupportedError struct {
	Op      string
	ShardID uint64
}

func (e *BackupUnsupportedError) Error() string {
	return "service: shard " + opShardString(e.Op, e.ShardID) +
		" not supported on this host (backup/restore requires a clustered Raft host)"
}

// ShardSnapshotError wraps a failure from the host's snapshot
// request/export/import.
type ShardSnapshotError struct {
	Op      string
	ShardID uint64
	Err     error
}

func (e *ShardSnapshotError) Error() string {
	return "service: shard snapshot " + opShardString(e.Op, e.ShardID) + ": " + e.Err.Error()
}

func (e *ShardSnapshotError) Unwrap() error { return e.Err }

// ShardSnapshotTimeoutError is returned when SnapshotShard cannot find an
// exportable snapshot within the configured poll timeout, after a
// concurrent snapshot request (ErrSnapshotInProgress) prevented it from
// requesting its own.
type ShardSnapshotTimeoutError struct {
	ShardID uint64
	Timeout time.Duration
}

func (e *ShardSnapshotTimeoutError) Error() string {
	return "service: shard snapshot export for shard " + strconv.FormatUint(e.ShardID, 10) +
		" timed out after " + e.Timeout.String() + " waiting for a concurrently requested snapshot to become available"
}

// OnlineRestoreSystemShardError is returned by RestoreShardOnline when the
// target shard hosts the system tenant. Stopping the system shard while the
// server is serving requests would take down the auth data plane, including
// the admin call that would restart it, so online restore is refused
// unconditionally for this shard in favor of the offline boot-time flow.
type OnlineRestoreSystemShardError struct {
	ShardID uint64
}

func (e *OnlineRestoreSystemShardError) Error() string {
	return "service: online restore of shard " + strconv.FormatUint(e.ShardID, 10) +
		" refused: it hosts the system tenant; stopping it while serving would take down auth for every " +
		"subsequent admin call, including the one that would restart it — use the offline " +
		"'qrdbd start --restore-backup-id' boot flow instead"
}

// OnlineRestoreReplicatedShardError is returned by RestoreShardOnline when
// the target shard has more than one voting replica. A single replica's
// in-place snapshot import is silently overwritten by the existing majority
// on the next heartbeat/append, making an online per-replica restore
// ineffective rather than merely risky; a safe multi-node restore requires
// an offline whole-cluster procedure.
type OnlineRestoreReplicatedShardError struct {
	ShardID        uint64
	VotingReplicas int
}

func (e *OnlineRestoreReplicatedShardError) Error() string {
	return "service: online restore of shard " + strconv.FormatUint(e.ShardID, 10) +
		" refused: it has " + strconv.Itoa(e.VotingReplicas) + " voting replicas; an online per-replica " +
		"import would be silently overwritten by the majority — restoring a replicated shard requires " +
		"an offline whole-cluster procedure"
}

// OnlineRestoreUnsupportedError is returned by RestoreShardOnline when this
// host/deployment cannot safely perform an online restore of shardID — e.g.
// the host does not expose linearizable membership queries, or no
// ShardRestarter has been configured (see SetShardRestarter) to safely
// restart the shard after import. This is distinct from
// BackupUnsupportedError, which means the host cannot snapshot/import at
// all; here snapshot/import work, but the safety guards cannot be evaluated
// or enforced, so online restore fails closed instead of running unguarded.
type OnlineRestoreUnsupportedError struct {
	ShardID uint64
	Reason  string
}

func (e *OnlineRestoreUnsupportedError) Error() string {
	return "service: online restore of shard " + strconv.FormatUint(e.ShardID, 10) +
		" not supported: " + e.Reason + " — use the offline 'qrdbd start --restore-backup-id' boot flow instead"
}

// OnlineRestoreRollbackError is returned by RestoreShardOnline when an
// ImportSnapshot failure is compounded by a failure to restart the shard
// afterward. Unlike every other online-restore error, this one CANNOT
// guarantee the shard came back up — it always requires immediate operator
// attention.
type OnlineRestoreRollbackError struct {
	ShardID    uint64
	ImportErr  error
	RestartErr error
}

func (e *OnlineRestoreRollbackError) Error() string {
	return "service: online restore of shard " + strconv.FormatUint(e.ShardID, 10) +
		" failed to import (" + e.ImportErr.Error() + ") AND failed to restart the shard afterward (" +
		e.RestartErr.Error() + ") — the shard may be DOWN; operator intervention required"
}

// Unwrap returns the import failure so errors.Is/As can match it; the
// restart failure is still available via the RestartErr field.
func (e *OnlineRestoreRollbackError) Unwrap() error { return e.ImportErr }

// opShardString renders "<op> for shard <id>" for the error messages above.
func opShardString(op string, shardID uint64) string {
	return op + " for shard " + strconv.FormatUint(shardID, 10)
}
