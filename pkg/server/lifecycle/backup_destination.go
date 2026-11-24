package lifecycle

import (
	"errors"
	"maps"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// errBackupDestinationProvider reports a backup.destination without a
// provider. A destination has no default: a backup kept under the node data
// directory would die with it.
var errBackupDestinationProvider = errors.New("provider is required")

// OpenBackupDestination opens the go-objstore provider a backup.destination
// block names, as the store a backupsink.Sink writes backup artifacts to and
// reads them back from. It is the one opener both composers (qrdbd and the
// embedded SDK node) use, so neither imports go-objstore itself.
//
// Unlike a storage.objstore engine, the local provider gets no default path:
// the settings are passed to the provider as configured. The caller owns the
// returned engine and closes it.
func OpenBackupDestination(cfg qrdbconfig.ObjstoreConfig) (*objstore.Engine, error) {
	if cfg.Provider == "" {
		return nil, &BackupDestinationError{Err: errBackupDestinationProvider}
	}
	settings := maps.Clone(cfg.Settings)
	if settings == nil {
		settings = map[string]string{}
	}
	engine, err := objstore.New(cfg.Provider, settings, objstore.WithCredentialFiles(objstore.CredentialFiles{
		KeyFile:    cfg.Credentials.KeyFile,
		SecretFile: cfg.Credentials.SecretFile,
	}))
	if err != nil {
		return nil, &BackupDestinationError{Provider: cfg.Provider, Err: err}
	}
	return engine, nil
}
