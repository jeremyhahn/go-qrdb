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
	// SnapshotShards returns an error for a started shard whose snapshot
	// record cannot be read.
	SnapshotShards(tenantID string) ([]SnapshotShard, error)
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
	if root == "" {
		return nil
	}
	// A volume tenant Host lists its own shard; in raft mode the tenant's
	// dedicated shards run on the shared node Host and the raft factory
	// lists them.
	var sources []MoveSnapshotShards
	if c.snapshots != nil {
		sources = append(sources, c.snapshots)
	}
	if raft, ok := c.shards.(MoveSnapshotShards); ok && raft != nil {
		sources = append(sources, raft)
	}
	var errs []error
	for _, src := range sources {
		shards, err := src.SnapshotShards(tenantID)
		if err != nil {
			errs = append(errs, err)
		}
		for _, s := range shards {
			if err := relocateShardSnapshot(ctx, s, root); err != nil {
				errs = append(errs, fmt.Errorf("move: relocate the snapshot of shard %d of tenant %s: %w", s.ShardID, tenantID, err))
			}
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

// SnapshotShards lists the dedicated shards of tenantID started on this
// node's shared Host, with the snapshot records the Host's multiplex LogDB
// keeps for them. A started shard whose records the factory cannot read (no
// multiplex LogDB) is an error: skipping it could leave a record naming the
// file a move removes.
func (f *RaftBackendFactory) SnapshotShards(tenantID string) ([]SnapshotShard, error) {
	if f == nil || f.host == nil {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []SnapshotShard
	var errs []error
	for k, rs := range f.shards {
		if k.tenantID != tenantID {
			continue
		}
		if f.walMux == nil {
			errs = append(errs, fmt.Errorf("move: shard %d of tenant %s has no multiplex LogDB to read its snapshot record from", rs.shardID, tenantID))
			continue
		}
		out = append(out, SnapshotShard{ShardID: rs.shardID, ReplicaID: rs.replicaID, Records: f.walMux, Host: f.host})
	}
	return out, errors.Join(errs...)
}

// SnapshotShards lists the shard of tenantID's started volume Host. A
// started Host with no LogDB to read its snapshot record from is an error.
func (r *TenantHostRegistry) SnapshotShards(tenantID string) ([]SnapshotShard, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	th, ok := r.hosts[tenantID]
	r.mu.Unlock()
	if !ok || th == nil || th.host == nil {
		return nil, nil
	}
	if th.logDB == nil {
		return nil, fmt.Errorf("move: shard %d of tenant %s has no LogDB to read its snapshot record from", tenantHostShardID, tenantID)
	}
	return []SnapshotShard{{ShardID: tenantHostShardID, ReplicaID: 1, Records: th.logDB, Host: th.host}}, nil
}
