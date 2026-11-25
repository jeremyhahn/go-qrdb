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

package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// SnapshotRequester takes a snapshot of a started shard and proposes to it.
type SnapshotRequester interface {
	SyncRequestSnapshot(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (uint64, error)
	SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error)
}

// SnapshotRecords reads the newest snapshot record of a replica.
type SnapshotRecords interface {
	GetSnapshot(shardID, replicaID uint64) (logdb.Snapshot, error)
}

// SnapshotShard is one started shard of a tenant whose snapshot record a
// settle checks against the settled root.
type SnapshotShard struct {
	ShardID   uint64
	ReplicaID uint64
	Records   SnapshotRecords
	Host      SnapshotRequester
}

// MoveSnapshotShards lists the started shards of a tenant.
type MoveSnapshotShards interface {
	SnapshotShards(tenantID string) []SnapshotShard
}

// WithMoveSnapshotShards sets the source of the shards RelocateSnapshots
// re-snapshots onto a settled root.
func WithMoveSnapshotShards(s MoveSnapshotShards) MoveStoreCloserOption {
	return func(c *MoveStoreCloser) { c.snapshots = s }
}

// RelocateSnapshots takes a fresh snapshot of every started shard of
// tenantID whose newest snapshot record names a file outside root, so no
// record names the copy a move is about to remove.
func (c *MoveStoreCloser) RelocateSnapshots(ctx context.Context, tenantID, root string) error {
	if c.snapshots == nil || root == "" {
		return nil
	}
	var errs []error
	for _, s := range c.snapshots.SnapshotShards(tenantID) {
		if err := relocateShardSnapshot(ctx, s, root); err != nil {
			errs = append(errs, fmt.Errorf("move: relocate the snapshot of shard %d of tenant %s: %w", s.ShardID, tenantID, err))
		}
	}
	return errors.Join(errs...)
}

// relocateShardSnapshot re-snapshots s when its newest record lies outside
// root, first proposing an empty command when nothing was applied since.
func relocateShardSnapshot(ctx context.Context, s SnapshotShard, root string) error {
	rec, err := s.Records.GetSnapshot(s.ShardID, s.ReplicaID)
	if errors.Is(err, logdb.ErrNoSnapshot) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.Filepath == "" {
		return nil
	}
	if _, under := pathUnder(rec.Filepath, root); under {
		return nil
	}
	_, err = s.Host.SyncRequestSnapshot(ctx, s.ShardID, quicraft.SnapshotOption{})
	if errors.Is(err, logdb.ErrSnapshotOutOfDate) {
		if _, perr := s.Host.SyncPropose(ctx, s.ShardID, nil); perr != nil {
			return perr
		}
		_, err = s.Host.SyncRequestSnapshot(ctx, s.ShardID, quicraft.SnapshotOption{})
	}
	return err
}

// pathUnder returns path relative to root, and whether path lies under it.
func pathUnder(path, root string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// SnapshotShards lists the shard of tenantID's started volume Host.
func (r *TenantHostRegistry) SnapshotShards(tenantID string) []SnapshotShard {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	th, ok := r.hosts[tenantID]
	r.mu.Unlock()
	if !ok || th.logDB == nil {
		return nil
	}
	return []SnapshotShard{{ShardID: tenantHostShardID, ReplicaID: 1, Records: th.logDB, Host: th.host}}
}
