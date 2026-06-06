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
	"io"
	"strconv"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// snapshotExporter is the subset of the Raft host that exports and imports
// per-shard snapshot artifacts. The clustered host (raft.HostAdapter, which
// embeds *quicraft.Host) satisfies it via method promotion; the single-node
// LocalBackend does not, so backup over a non-Raft host is reported as
// unsupported rather than silently producing empty backups.
type snapshotExporter interface {
	ExportSnapshot(ctx context.Context, shardID uint64, w io.Writer) error
	ImportSnapshot(ctx context.Context, shardID uint64, r io.Reader) error
}

// hostShardSnapshotProvider implements ShardSnapshotProvider over a
// service.Host. Snapshots are self-describing artifacts produced by the host's
// ExportSnapshot (including the epoch/barrier metadata needed for at-rest
// encrypted shards); RestoreShard feeds them back through ImportSnapshot.
type hostShardSnapshotProvider struct {
	host Host
}

// NewHostShardSnapshotProvider returns a ShardSnapshotProvider backed by the
// given Raft host. It is the production provider wired into BackupService by
// lifecycle.BuildServices when a snapshot-capable (clustered) host is present.
func NewHostShardSnapshotProvider(host Host) ShardSnapshotProvider {
	return &hostShardSnapshotProvider{host: host}
}

// AvailableShards returns the IDs of every shard currently loaded on the host.
func (p *hostShardSnapshotProvider) AvailableShards(_ context.Context) ([]uint64, error) {
	info := p.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
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
func (p *hostShardSnapshotProvider) SnapshotShard(ctx context.Context, shardID uint64) ([]byte, error) {
	exporter, ok := p.host.(snapshotExporter)
	if !ok {
		return nil, &BackupUnsupportedError{Op: "snapshot", ShardID: shardID}
	}
	var buf bytes.Buffer
	if err := exporter.ExportSnapshot(ctx, shardID, &buf); err != nil {
		return nil, &ShardSnapshotError{Op: "export", ShardID: shardID, Err: err}
	}
	return buf.Bytes(), nil
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

// ShardSnapshotError wraps a failure from the host's snapshot export/import.
type ShardSnapshotError struct {
	Op      string
	ShardID uint64
	Err     error
}

func (e *ShardSnapshotError) Error() string {
	return "service: shard snapshot " + opShardString(e.Op, e.ShardID) + ": " + e.Err.Error()
}

func (e *ShardSnapshotError) Unwrap() error { return e.Err }

// opShardString renders "<op> for shard <id>" for the error messages above.
func opShardString(op string, shardID uint64) string {
	return op + " for shard " + strconv.FormatUint(shardID, 10)
}
