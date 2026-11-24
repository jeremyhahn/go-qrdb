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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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

// releasingDir holds one file per object-storage database this node dropped
// whose provider namespace it has not yet released: a directory at the
// database's (tenant, name) path (tenantScopedPath) holding one file per
// dropped incarnation (releaseEntryFile). A drop records the entry before
// its catalog delete and forgets it once the namespace is released (drop,
// steps 4 and 6), so a release that failed, or never ran because the node
// stopped between the two, is retried (retryNamespaceReleases).
const releasingDir = "releasing"

// releaseEntry is a releasingDir file: what reopening a dropped database's
// namespace as its owner needs. Objstore carries the credential file paths,
// never their contents (DatabaseObjstoreConfig).
type releaseEntry struct {
	Backend  BackendType             `json:"backend"`
	TenantID string                  `json:"tenant_id,omitempty"`
	Name     string                  `json:"name"`
	Objstore *DatabaseObjstoreConfig `json:"objstore,omitempty"`
	// Incarnation is the dropped database's incarnation (Database.Incarnation):
	// the ledger keeps one entry per incarnation, so a name dropped,
	// re-created and dropped again keeps every namespace it must release.
	Incarnation string `json:"incarnation,omitempty"`
	// ClaimOnly is set for the claim of a failed create onto a namespace that
	// already held data no database owned: its release removes only the
	// owner marker (backendReleaser.ReleaseOwnerMarker), since the records,
	// journal and format marker are not the failed create's. Unset, the
	// release reclaims the namespace whole (ReleaseOwner).
	ClaimOnly bool `json:"claim_only,omitempty"`

	// file is the entry's file name in its name's directory
	// (releaseEntryFile), set when the entry is read back.
	file string
}

// config is the DatabaseConfig the entry's namespace reopens under: as the
// dropped incarnation, so a namespace a successor owns is refused, not
// reclaimed (releaseDatabaseNamespace).
func (e releaseEntry) config() DatabaseConfig {
	return DatabaseConfig{TenantID: e.TenantID, Name: e.Name, Backend: e.Backend, Objstore: e.Objstore, Incarnation: e.Incarnation}
}

// recordRelease durably records that cfg's namespace awaits its release;
// claimOnly records the release of a claim on existing data, which removes
// only the owner marker (releaseEntry.ClaimOnly).
func (m *Manager) recordRelease(key dbKey, cfg DatabaseConfig, incarnation string, claimOnly bool) (string, error) {
	file, err := releaseEntryFile(incarnation)
	if err != nil {
		return "", err
	}
	// The configuration's own tenant and name, not the key's: the namespace
	// owner is spelled from the configuration (objstoredb).
	data, err := json.Marshal(releaseEntry{Backend: cfg.Backend, TenantID: cfg.TenantID, Name: cfg.Name, Objstore: cfg.Objstore, Incarnation: incarnation, ClaimOnly: claimOnly})
	if err != nil {
		return "", err
	}
	path := m.releaseEntryPath(key.tenant, key.name, file)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", &MetaWriteError{Path: dir, Err: err}
	}
	if err := writeFileAtomic(dir, file, data); err != nil {
		return "", err
	}
	return file, nil
}

// forgetRelease removes the release entry of tenantID's database name; an
// absent entry is not an error.
func (m *Manager) forgetRelease(tenantID, name, file string) error {
	if file == "" {
		// No entry was recorded (a database that is not object storage).
		return nil
	}
	path := m.releaseEntryPath(tenantID, name, file)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &MetaWriteError{Path: path, Err: err}
	}
	// The name's directory goes with its last entry; another incarnation's
	// entry keeps it (ENOTEMPTY), which is not an error.
	_ = os.Remove(filepath.Dir(path))
	return nil
}

// releaseEntries returns every release entry this node holds. A file that
// does not parse is skipped and logged, never fatal.
func (m *Manager) releaseEntries() ([]releaseEntry, error) {
	root := filepath.Join(m.dataDir, releasingDir)
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	fsys := os.DirFS(root)
	var entries []releaseEntry
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return &MetaReadError{Path: filepath.Join(root, path), Err: err}
		}
		// writeFileAtomic's temporary files start with a dot.
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return &MetaReadError{Path: filepath.Join(root, path), Err: err}
		}
		var entry releaseEntry
		if err := json.Unmarshal(data, &entry); err != nil || entry.Name == "" {
			m.logger.Warn("skipping an unreadable object-storage namespace release entry", slog.String("path", filepath.Join(root, path)))
			return nil
		}
		entry.file = d.Name()
		entries = append(entries, entry)
		return nil
	})
	return entries, err
}

// releaseEntryPath is where tenantID's database name has its release entry.
func (m *Manager) releaseEntryPath(tenantID, name, file string) string {
	return filepath.Join(tenantScopedPath(filepath.Join(m.dataDir, releasingDir), tenantID, name), file)
}

// releaseEntryFile names a drop's release entry inside its name's directory,
// so the ledger keeps one entry per dropped database incarnation and a name
// dropped, re-created and dropped again keeps every namespace it must
// release. An incarnation is NewIncarnation's lowercase hex, a single safe
// path element, and names its entry (a retried drop of the same incarnation
// rewrites it); any other value is named by its hex SHA-256. A database
// without one -- a provisioned database -- shares the empty incarnation with every re-creation of its name,
// so each of its drops gets a fresh token instead, prefixed "-" so it can
// never equal an incarnation's name.
func releaseEntryFile(incarnation string) (string, error) {
	if incarnation == "" {
		token, err := NewIncarnation()
		if err != nil {
			return "", err
		}
		return "-" + token, nil
	}
	for _, c := range incarnation {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			sum := sha256.Sum256([]byte(incarnation))
			return "sha256-" + hex.EncodeToString(sum[:]), nil
		}
	}
	return incarnation, nil
}

// retryNamespaceReleases releases the namespace of every release entry this
// node holds whose database is gone from the catalog, and forgets each one
// released. It never fails: a release that fails again keeps its entry for
// the next retry and is logged. An entry whose name a drop is still working
// on here, or whose catalog record still carries the drop's fence, is left
// to that drop. An entry whose name has a live record again was re-created:
// when that database holds the entry's namespace the entry is forgotten (a
// release would be refused anyway: the owner names the incarnation); when it provably holds another one
// (ObjectStorageNamespacer) the old namespace is released; when that cannot
// be told the entry is kept and logged. Each entry is handled under its name's claim,
// so no create or open of the name runs meanwhile.
func (m *Manager) retryNamespaceReleases(ctx context.Context) {
	cat, err := m.catalog("retry object-storage namespace releases")
	if err != nil {
		return
	}
	entries, err := m.releaseEntries()
	if err != nil {
		m.logger.Warn("failed to read the object-storage namespace release entries", slog.String("error", err.Error()))
		return
	}
	for _, entry := range entries {
		m.retryNamespaceRelease(ctx, cat, entry)
	}
}

// retryNamespaceRelease handles one release entry (retryNamespaceReleases).
func (m *Manager) retryNamespaceRelease(ctx context.Context, cat Catalog, entry releaseEntry) {
	key := keyFor(entry.TenantID, entry.Name)
	if _, dropping := m.dropping.Load(key); dropping {
		return
	}
	unclaim, claimed := m.tryClaim(key)
	if !claimed {
		return
	}
	defer unclaim()
	warn := func(msg string, err error) {
		m.logger.Warn(msg, slog.String("tenant", entry.TenantID), slog.String("name", entry.Name), slog.String("error", err.Error()))
	}
	meta, err := cat.Get(ctx, entry.TenantID, entry.Name)
	if err == nil {
		if meta.Incarnation == entry.Incarnation && meta.Dropping {
			// The drop that recorded the entry is still under way.
			return
		}
		// The name was re-created. The entry is forgotten only when the
		// live database holds the entry's namespace, and released only when
		// it provably holds another one; on any doubt the entry is kept,
		// since ReleaseOwner reclaims records the live database may share.
		live := DatabaseConfig{TenantID: entry.TenantID, Name: entry.Name, Backend: meta.Backend, Objstore: meta.Objstore}
		same, err := m.sameObjectStorageNamespace(entry.config(), live)
		if err != nil {
			warn("object-storage namespace release of a re-created name kept: namespace unknown", err)
			return
		}
		if same {
			if err := m.forgetRelease(key.tenant, key.name, entry.file); err != nil {
				warn("failed to forget the namespace release entry of a re-created database", err)
			}
			return
		}
	} else {
		var notFound *NotFoundError
		if !errors.As(err, &notFound) {
			warn("object-storage namespace release not retried", err)
			return
		}
	}
	if err := m.releaseDatabaseNamespace(key, entry.config(), entry.ClaimOnly); err != nil {
		warn("object-storage namespace release retry failed", err)
		return
	}
	if err := m.forgetRelease(key.tenant, key.name, entry.file); err != nil {
		warn("failed to forget a released object-storage namespace", err)
		return
	}
	m.logger.Info("object-storage namespace of a dropped database released",
		slog.String("tenant", entry.TenantID), slog.String("name", entry.Name))
}

// sameObjectStorageNamespace reports whether the live configuration holds
// the dropped one's provider namespace. A live database that is not object
// storage holds no namespace, so it is another one. An error -- no
// namespacer wired, a root that no longer resolves -- means it cannot be
// told.
func (m *Manager) sameObjectStorageNamespace(dropped, live DatabaseConfig) (bool, error) {
	droppedKey, err := objectStorageNamespace(m.factory, dropped)
	if err != nil {
		return false, err
	}
	if live.Backend != BackendObjstore || live.Objstore == nil {
		return false, nil
	}
	liveKey, err := objectStorageNamespace(m.factory, live)
	if err != nil {
		return false, err
	}
	return droppedKey == liveKey, nil
}

// createUndo removes what a failed Create made, and nothing it did not.
type createUndo struct {
	m        *Manager
	key      dbKey
	tenantID string
	name     string

	// cfg is the configuration the data backend opened under, as the new
	// incarnation: a claim give-back that fails records its release for
	// it (recordRelease).
	cfg DatabaseConfig

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
// cause.
// recordClaimRelease records the release a failed claim give-back (undoErr)
// could not make, for the incarnation this create opened as; claimOnly when
// the namespace held data before the claim (releaseEntry.ClaimOnly). A
// release that cannot be recorded is logged.
func (u *createUndo) recordClaimRelease(claimOnly bool, undoErr error) {
	if _, err := u.m.recordRelease(u.key, u.cfg, u.cfg.Incarnation, claimOnly); err != nil {
		u.m.logger.Warn("failed to record the namespace release of a failed create",
			slog.String("name", u.name), slog.Any("undo_error", undoErr), slog.Any("error", err))
	}
}

func (u *createUndo) run(ctx context.Context, cause error) error {
	for i := len(u.backends) - 1; i >= 0; i-- {
		// A backend whose open claimed its provider namespace (an
		// object-storage database) gives the claim back, so the failed
		// create reserves nothing: a namespace claimed new is reclaimed
		// and released; on a namespace that held data before, only the
		// owner marker this open wrote is removed and the data stays. A
		// namespace that was already this owner's is left as it was. A
		// give-back that fails leaves the namespace reserved by an
		// incarnation with no record, so its release is recorded for
		// retryNamespaceReleases, claim-only unless the claim was fresh.
		if r, ok := u.backends[i].(backendReleaser); ok {
			if err := r.ReleaseClaim(); err != nil {
				u.recordClaimRelease(!r.FreshlyClaimed(), err)
			}
		}
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
