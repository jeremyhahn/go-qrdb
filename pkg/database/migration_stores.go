package database

import (
	"context"
	"os"
	"path/filepath"
)

// StoreMigrationUnsupportedError refuses a node encryption migration for a
// database whose store the migration cannot re-encrypt in place.
type StoreMigrationUnsupportedError struct {
	TenantID string
	Database string
	Reason   string
}

func (e *StoreMigrationUnsupportedError) Error() string {
	return "database " + e.TenantID + "/" + e.Database + " " + e.Reason
}

// NodeEncryptedStoreDirs lists the at-rest store directories of every
// database of m the node barrier encrypts in a store of its own, split by
// store format, for a node encryption migration to re-encrypt beside the
// base store: a file or Pebble binding root (a system or prefix-tenant
// database, an explicit data dir included) and the state store of each
// node-encrypted database group (backend: raft; its pebble and file state
// storage both run a Pebble state machine, memory keeps nothing at rest).
// A database with its own barrier, or an isolated tenant's, is encrypted by
// another barrier and left alone. An objstore database cannot be encrypted
// at rest, and a Pebble binding whose write-ahead log lives under walDir
// (storage.wal_dir) would lose its WAL-only records: both are refused with
// a *StoreMigrationUnsupportedError. The daemon and the embedded node both
// enumerate through here.
func NodeEncryptedStoreDirs(ctx context.Context, m *Manager, walDir string) (pebbleDirs, fileDirs []string, err error) {
	if m == nil {
		return nil, nil, nil
	}
	bindings, err := m.migrationBindings(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range bindings {
		if b.Encryptor != EncryptorNode || b.DataRoot == "" {
			continue
		}
		switch b.Backend {
		case BackendObjstore:
			return nil, nil, &StoreMigrationUnsupportedError{TenantID: b.TenantID, Database: b.Database,
				Reason: "is stored in objstore, which cannot encrypt at rest"}
		case BackendPebble:
			db := existingStoreDirs(filepath.Join(b.DataRoot, "pebble"))
			if walDir != "" && len(db) > 0 {
				return nil, nil, &StoreMigrationUnsupportedError{TenantID: b.TenantID, Database: b.Database,
					Reason: "keeps its write-ahead log under storage.wal_dir; encryption migration moves the store directory alone"}
			}
			pebbleDirs = append(pebbleDirs, db...)
		case BackendFile:
			fileDirs = append(fileDirs, existingStoreDirs(filepath.Join(b.DataRoot, "file"))...)
		case BackendRaft:
			if b.Raft != nil && b.Raft.StateStorage == BackendMemory {
				continue
			}
			// The pattern is constant and well-formed, so Glob cannot fail.
			groups, _ := filepath.Glob(filepath.Join(b.DataRoot, "shard-*"))
			pebbleDirs = append(pebbleDirs, existingStoreDirs(groups...)...)
		}
	}
	return pebbleDirs, fileDirs, nil
}

// migrationBindings is RecordBindings with the binding of every backend:
// raft record the factory describes (BindingDescriber) added: a raft-mode
// factory materializes no group store of its own, yet a group's state store
// is encrypted at rest by its binding's encryptor all the same.
func (m *Manager) migrationBindings(ctx context.Context) ([]*Binding, error) {
	bindings, err := m.RecordBindings(ctx)
	if err != nil {
		return nil, err
	}
	describer, ok := m.factory.(BindingDescriber)
	if !ok {
		return bindings, nil
	}
	seen := make(map[dbKey]bool, len(bindings))
	for _, b := range bindings {
		seen[keyFor(b.TenantID, b.Database)] = true
	}
	metas, err := m.ListRecords(ctx)
	if err != nil {
		return nil, err
	}
	for _, meta := range metas {
		if meta.Backend != BackendRaft || seen[keyFor(meta.TenantID, meta.Name)] {
			continue
		}
		b, err := describer.DescribeBinding(configFromMeta(meta))
		if err != nil {
			return nil, &RecordRootUnresolvedError{Name: meta.Name, TenantID: meta.TenantID, With: meta.Name, Err: err}
		}
		if b != nil {
			bindings = append(bindings, b)
		}
	}
	return bindings, nil
}

// existingStoreDirs returns the directories of dirs that exist.
func existingStoreDirs(dirs ...string) []string {
	var out []string
	for _, d := range dirs {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			out = append(out, d)
		}
	}
	return out
}
