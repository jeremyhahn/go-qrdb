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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// What the Manager remembers after a database is gone
//
// The catalog record of a dropped database is removed, but two facts about it
// must outlive it on this node:
//
//   - that the name was DROPPED here, so its store holds a tombstone. A
//     create of the name with no reclaimer attached cannot replace the
//     tombstone with a live incarnation, and must be refused rather than
//     produce a database that refuses every write (droppedHere).
//   - the shard IDs it used. A dedicated-raft database's shard stands in for
//     its incarnation, which holds only if a shard ID is never issued twice
//     -- across drops AND restarts. The allocator is rebuilt at boot from the
//     databases that still exist, so the highest ID ever issued is persisted
//     separately and OpenAll moves the allocator past it (recordShardID).
// ---------------------------------------------------------------------------

const (
	// droppedDir holds one empty file per database name this node dropped
	// and has not created again.
	droppedDir = "dropped"

	// shardHighWaterFile holds the highest shard ID this node ever issued.
	shardHighWaterFile = "shard-high-water"
)

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

// recordShardID raises the persisted shard high-water mark to id. It runs
// before the database that uses id exists, so an ID is never issued without
// being recorded.
func (m *Manager) recordShardID(id uint64) error {
	m.shardHWMu.Lock()
	defer m.shardHWMu.Unlock()
	current, err := m.readShardHighWater()
	if err != nil {
		return err
	}
	if id <= current {
		return nil
	}
	if err := os.MkdirAll(m.dataDir, 0o750); err != nil {
		return &MetaWriteError{Path: m.dataDir, Err: err}
	}
	return writeFileAtomic(m.dataDir, shardHighWaterFile, []byte(strconv.FormatUint(id, 10)))
}

// readShardHighWater returns the highest shard ID ever recorded, or zero
// when none is (a node whose databases predate the record).
func (m *Manager) readShardHighWater() (uint64, error) {
	path := filepath.Join(m.dataDir, shardHighWaterFile)
	data, err := os.ReadFile(path) // #nosec G304 -- m.dataDir is the node's own configured data directory, shardHighWaterFile a constant
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, &MetaReadError{Path: path, Err: err}
	}
	id, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, &MetaReadError{Path: path, Err: err}
	}
	return id, nil
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
