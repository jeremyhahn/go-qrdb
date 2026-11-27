package main

import (
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// checkBackupRepository refuses a boot that names two backup repositories:
// the --backup-dir file repository and a backup.destination provider.
func checkBackupRepository(f *startFlags) error {
	if f.backupDir != "" && f.backupDestination != nil {
		return &BackupDestinationConflictError{}
	}
	return nil
}

// openBackupStore opens the store the backup repository lives on: the
// backup.destination go-objstore provider, or the --backup-dir file
// repository. Neither set returns (nil, nil): the node keeps backups in
// memory only, and an offline restore has no source. It never falls back to
// memory when the named repository fails to open. The caller closes the
// returned store.
func openBackupStore(f *startFlags) (storage.Storage, error) {
	if err := checkBackupRepository(f); err != nil {
		return nil, err
	}
	switch {
	case f.backupDestination != nil:
		engine, err := lifecycle.OpenBackupDestination(*f.backupDestination)
		if err != nil {
			return nil, err
		}
		return engine, nil
	case f.backupDir != "":
		return file.New(f.backupDir)
	default:
		return nil, nil
	}
}
