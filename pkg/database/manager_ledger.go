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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
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

// droppedDir holds one empty file per database this node dropped and has not
// created again, at the database's (tenant, name) path (tenantScopedPath).
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
// It is the one way to set the claimer: no production composition has the
// replicated store when it constructs the Manager, so a construction option
// would only ever serve tests. The claimer is held in an atomic pointer, so a
// create racing the composition sees either no claimer (and refuses) or the
// one set, never a torn value. A nil c clears it.
func (m *Manager) SetShardIDClaimer(c ShardIDClaimer) {
	if c == nil {
		m.claimer.Store(nil)
		return
	}
	m.claimer.Store(&claimerRef{c})
}

// claimerRef boxes a ShardIDClaimer so the Manager can hold it in an
// atomic.Pointer.
type claimerRef struct{ ShardIDClaimer }

// issueShardID returns the shard ID for a raft database being created. The
// claimer is the only issuer: its record lives in the replicated store, so an
// ID is issued once in the cluster's lifetime, survives a restore into a fresh
// data directory, and behaves identically in standalone and raft mode.
func (m *Manager) issueShardID(ctx context.Context) (uint64, error) {
	ref := m.claimer.Load()
	if ref == nil {
		return 0, &ShardIssuerRequiredError{}
	}
	id, err := ref.ClaimShardID(ctx)
	if err != nil {
		return 0, &ShardIDClaimError{Err: err}
	}
	return id, nil
}

// ClaimShardID claims a cluster-unique shard ID from the replicated ledger
// for a Raft group that is not a catalog database -- an isolated tenant's home
// group. Catalog databases take theirs inside Create.
func (m *Manager) ClaimShardID(ctx context.Context) (uint64, error) {
	return m.issueShardID(ctx)
}

// recordDrop remembers that tenantID's database name was dropped on this node.
func (m *Manager) recordDrop(tenantID, name string) error {
	path := m.dropEntry(tenantID, name)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return &MetaWriteError{Path: dir, Err: err}
	}
	return writeFileAtomic(dir, filepath.Base(path), nil)
}

// WasDropped reports whether tenantID's database name was dropped on this
// node and not created again since: its store holds the dropped tombstone. A
// resolution that finds no catalog record uses it to tell "never a database
// here" from "dropped".
func (m *Manager) WasDropped(tenantID, name string) (bool, error) {
	return m.droppedHere(tenantID, name)
}

// droppedHere reports whether tenantID's database name was dropped on this
// node and not created again since.
func (m *Manager) droppedHere(tenantID, name string) (bool, error) {
	path := m.dropEntry(tenantID, name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, &MetaReadError{Path: path, Err: err}
	}
	return true, nil
}

// forgetDrop clears the record of an earlier drop of tenantID's database
// name, once a database of that name has been created again and marked live.
func (m *Manager) forgetDrop(tenantID, name string) error {
	path := m.dropEntry(tenantID, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &MetaWriteError{Path: path, Err: err}
	}
	return nil
}

// dropEntry is the ledger file for tenantID's database name. It is keyed by
// both, as the database is: one tenant's drop says nothing about another
// tenant's database of the same name.
func (m *Manager) dropEntry(tenantID, name string) string {
	return tenantScopedPath(filepath.Join(m.dataDir, droppedDir), tenantID, name)
}

// createUndo removes what a failed Create made, and nothing it did not.
type createUndo struct {
	m        *Manager
	tenantID string
	name     string

	// dirs are the directories this Create asked for and made itself, in the
	// order it made them (see mkdirAll).
	dirs []string

	// backends are the backends this Create opened.
	backends []Backend

	// barrier reports that this Create registered the database's barrier.
	barrier bool

	// catalog is the catalog this Create recorded the database in, or nil
	// while it has recorded nothing.
	catalog Catalog

	// incarnation is the incarnation of the record this Create made: the
	// undo removes that record and no other.
	incarnation string
}

// mkdirAll creates path and remembers it when this call's own os.Mkdir made
// it, so undo removes that directory and nothing else. A missing ancestor is
// created but never recorded: the ancestors are a namespace other creates
// share -- the catalog's databases directory above all -- and the moment one
// exists a concurrent create of another name can put its own database in it,
// so removing it would remove that database too. The Mkdir is atomic, so
// which call made path is never a guess: a directory that already existed, by
// whoever's hand, is not this create's to remove.
func (u *createUndo) mkdirAll(path string) error {
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	err := os.Mkdir(path, 0o750)
	if err == nil {
		u.dirs = append(u.dirs, path)
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			return nil
		}
	}
	return err
}

// run closes and removes everything the Create made, and returns cause, the
// error that failed the Create. When the Create recorded the database and
// the record cannot be removed, the record may outlive the rollback and
// still name the database's storage and barrier metadata, so the
// directories are kept for the drop that removes the record, as
// settleCatalog keeps them, and run returns *CatalogOrphanError wrapping
// cause. Otherwise a backend whose data lives outside its directory (see
// storeReclaimer) has that data reclaimed; a reclaim that fails is joined to
// cause.
func (u *createUndo) run(ctx context.Context, cause error) error {
	for i := len(u.backends) - 1; i >= 0; i-- {
		_ = u.backends[i].Close()
	}
	if u.barrier && u.m.barrierMgr != nil {
		_ = u.m.barrierMgr.RemoveBarrier(u.name, u.tenantID)
	}
	if u.catalog != nil {
		// The create may have failed because ctx ended; the record it made
		// is removed regardless, within the rollback bound.
		undoCtx, cancel := DropRollbackContext(ctx)
		err := u.catalog.Delete(undoCtx, u.tenantID, u.name, u.incarnation, "")
		cancel()
		var notFound *NotFoundError
		if err != nil && !errors.As(err, &notFound) {
			return &CatalogOrphanError{Name: u.name, TenantID: u.tenantID, Err: cause, Cleanup: err}
		}
	}
	for i := len(u.dirs) - 1; i >= 0; i-- {
		_ = os.RemoveAll(u.dirs[i])
	}
	// The record is gone, so no drop will ever reclaim a backend whose data
	// lives outside the directories removed above: it is reclaimed here.
	var reclaimErr error
	for i := len(u.backends) - 1; i >= 0; i-- {
		if r, ok := u.backends[i].(storeReclaimer); ok {
			reclaimErr = errors.Join(reclaimErr, r.ReclaimStore())
		}
	}
	if reclaimErr != nil {
		return errors.Join(cause, reclaimErr)
	}
	return cause
}

// settleCatalog resolves a catalog create whose outcome is indeterminate
// (see createOutcomeUnknown): the write may have committed though the
// create was told it failed, so the record is read back and, when it is
// this create's own, removed before the rest of the create is rolled back.
// Rolling back without it would leave a record with no database behind it,
// and every later create or open of the name would refuse.
//
// Only this create's incarnation is removed, by a delete conditional on it:
// a record another node created meanwhile is left alone. A record with an
// empty incarnation cannot be told apart from another create's, so it is
// not removed either. On any failure to settle it returns
// *CatalogOrphanError, and the caller keeps its local directories.
func (u *createUndo) settleCatalog(ctx context.Context, cat Catalog, incarnation string, writeErr error) error {
	// The write may have failed because ctx ended; the read-back and the
	// delete get the rollback bound regardless.
	settleCtx, cancel := DropRollbackContext(ctx)
	defer cancel()
	orphan := func(cleanup error) error {
		return &CatalogOrphanError{Name: u.name, TenantID: u.tenantID, Err: writeErr, Cleanup: cleanup}
	}
	meta, err := cat.Get(settleCtx, u.tenantID, u.name)
	var notFound *NotFoundError
	switch {
	case errors.As(err, &notFound):
		return nil
	case err != nil:
		return orphan(err)
	case meta.Incarnation != incarnation:
		return nil
	case incarnation == "":
		return orphan(nil)
	}
	if err := cat.Delete(settleCtx, u.tenantID, u.name, incarnation, ""); err != nil && !errors.As(err, &notFound) {
		return orphan(err)
	}
	return nil
}

// createOutcomeUnknown reports whether a failed catalog create may still
// have committed: its write reached the store and then failed in a way that
// leaves the outcome open -- a Raft proposal delivered before leadership
// moved or its wait timed out (quicraft.MaybeApplied), or the caller's
// context ending while the write was in flight. A refusal such as
// *AlreadyExistsError, or a failure before the write, is determinate.
func createOutcomeUnknown(err error) bool {
	var write *CatalogWriteError
	if !errors.As(err, &write) {
		return false
	}
	return quicraft.MaybeApplied(err) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)
}
