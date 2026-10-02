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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// What the Manager remembers after a database is gone
//
// The catalog record of a dropped database is removed, but one fact about it
// must outlive it on this node: that the name was DROPPED here, so its store
// holds a tombstone. A create of the name with no reclaimer attached cannot
// replace the tombstone with a live incarnation, and must be refused rather
// than produce a database that refuses every write (droppedHere).
//
// The shard IDs a dropped database used are NOT remembered here. A
// dedicated-raft database's shard stands in for its incarnation, which holds
// only if a shard ID is never issued twice; that is the replicated claimer's
// guarantee (shard.Claimer), recorded in the store rather than on this node's
// disk, so it survives a restart, a restore into a fresh data directory and
// a create through another node alike.
// ---------------------------------------------------------------------------

// droppedDir holds one empty file per database name this node dropped and
// has not created again.
const droppedDir = "dropped"

// ShardIDClaimer issues shard IDs that are unique across a cluster.
// *shard.Claimer is the implementation; the interface keeps this package
// independent of the store the claims are made in.
type ShardIDClaimer interface {
	ClaimShardID(ctx context.Context) (uint64, error)
}

// SetShardIDClaimer makes every raft database this Manager creates take its
// shard ID from c.
//
// It exists for the same ordering reason SetKeyspaceReclaimer does: the
// Manager is built before the replicated system store the claims are made in.
// Every composition that builds the data plane (lifecycle.BuildServices) wires
// one, in standalone and raft mode alike, so a shard ID's issue is recorded in
// the store and nowhere on the node's disk. A Manager with none refuses raft
// creates.
//
// Thread-safety: call it before serving create requests.
func (m *Manager) SetShardIDClaimer(c ShardIDClaimer) {
	m.claimer = c
}

// issueShardID returns the shard ID for a raft database being created. The
// claimer is the only issuer: its record lives in the replicated store, so an
// ID is issued once in the cluster's lifetime, survives a restore into a fresh
// data directory, and behaves identically in standalone and raft mode.
func (m *Manager) issueShardID(ctx context.Context) (uint64, error) {
	if m.claimer == nil {
		return 0, &ShardIssuerRequiredError{}
	}
	id, err := m.claimer.ClaimShardID(ctx)
	if err != nil {
		return 0, &ShardIDClaimError{Err: err}
	}
	return id, nil
}

// recordDrop remembers that name was dropped on this node.
func (m *Manager) recordDrop(name string) error {
	dir := filepath.Join(m.dataDir, droppedDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return &MetaWriteError{Path: dir, Err: err}
	}
	return writeFileAtomic(dir, name, nil)
}

// WasDropped reports whether name was dropped on this node and not created
// again since: its store holds the dropped tombstone. A resolution that finds
// no catalog record uses it to tell "never a database here" from "dropped".
func (m *Manager) WasDropped(name string) (bool, error) {
	return m.droppedHere(name)
}

// droppedHere reports whether name was dropped on this node and not created
// again since.
func (m *Manager) droppedHere(name string) (bool, error) {
	path := filepath.Join(m.dataDir, droppedDir, name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, &MetaReadError{Path: path, Err: err}
	}
	return true, nil
}

// forgetDrop clears the record of an earlier drop of name, once a database
// of that name has been created again and marked live.
func (m *Manager) forgetDrop(name string) error {
	path := filepath.Join(m.dataDir, droppedDir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &MetaWriteError{Path: path, Err: err}
	}
	return nil
}

// createUndo removes what a failed Create made, and nothing it did not.
type createUndo struct {
	m    *Manager
	name string

	// dirs are the directories this Create created, outermost first.
	dirs []string

	// backends are the backends this Create opened.
	backends []Backend

	// barrier reports that this Create registered the database's barrier.
	barrier bool
}

// mkdirAll creates path and remembers the outermost directory it created,
// so undo removes exactly that and leaves a pre-existing ancestor alone.
func (u *createUndo) mkdirAll(path string) error {
	created := ""
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			break
		}
		created = p
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}
	if created != "" {
		u.dirs = append(u.dirs, created)
	}
	return nil
}

// run closes and removes everything the Create made.
func (u *createUndo) run() {
	for i := len(u.backends) - 1; i >= 0; i-- {
		_ = u.backends[i].Close()
	}
	if u.barrier && u.m.barrierMgr != nil {
		_ = u.m.barrierMgr.RemoveBarrier(u.name)
	}
	for i := len(u.dirs) - 1; i >= 0; i-- {
		_ = os.RemoveAll(u.dirs[i])
	}
}
