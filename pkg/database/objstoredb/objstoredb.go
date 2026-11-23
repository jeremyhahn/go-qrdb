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

// Package objstoredb wires the object-storage engine (pkg/storage/objstore)
// into pkg/database's EmbeddedBackendFactory. pkg/database does not import the
// engine, so a lean embedder stays free of go-objstore; the node composers
// (qrdbd, the server lifecycle and the Go SDK node) pass
// database.WithObjectStorage(objstoredb.Open),
// database.WithObjectStorageValidator(objstoredb.ValidateSettings) and
// database.WithObjectStorageNamespace(objstoredb.Namespace), as
// NewBackendFactory does.
package objstoredb

import (
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// Open opens a `backend: objstore` database's store from its own provider and
// settings. The credential handle opens the key and secret files for the open
// only; no plaintext copy is held. A nil cfg.Objstore opens with no provider,
// which objstore.New refuses. The factory wraps an error in
// *database.StorageCreateError.
func Open(cfg database.DatabaseConfig) (storage.Storage, error) {
	var oc database.DatabaseObjstoreConfig
	if cfg.Objstore != nil {
		oc = *cfg.Objstore
	}
	engine, err := objstore.New(oc.Provider, oc.Settings,
		objstore.WithCredentialFiles(objstore.CredentialFiles{
			KeyFile:    oc.KeyFile,
			SecretFile: oc.SecretFile,
		}),
		// The database owns its provider namespace: a second database
		// configured onto the same bucket or path is refused rather than
		// reading, overwriting or reclaiming this one's objects.
		objstore.WithOwner(namespaceOwner(cfg)))
	if err != nil {
		return nil, err
	}
	return engine, nil
}

// namespaceOwner is the owner a database records in its provider namespace:
// its tenant, its name and its incarnation (DatabaseConfig.Incarnation). The
// incarnation makes it one database's and no successor's: a database of the
// same tenant and name created after a drop is refused the namespace while
// the dropped one still holds it, and the dropped one's release, retried
// later, is refused a namespace the successor owns and reclaims none of it.
func namespaceOwner(cfg database.DatabaseConfig) string {
	return "database:" + strconv.Quote(cfg.TenantID) + "/" + strconv.Quote(cfg.Name) + "#" + strconv.Quote(cfg.Incarnation)
}

// NewBackendFactory returns the embedded backend factory with object storage
// wired in.
func NewBackendFactory() *database.EmbeddedBackendFactory {
	return database.NewEmbeddedBackendFactory(database.WithObjectStorage(Open),
		database.WithObjectStorageValidator(ValidateSettings),
		database.WithObjectStorageNamespace(Namespace))
}

// errNoObjstoreConfig is Namespace's refusal of a configuration with no
// object storage.
var errNoObjstoreConfig = errors.New("objstoredb: the database has no object-storage configuration")

// Namespace is the database.ObjectStorageNamespacer of an objstore database:
// the namespace key (objstore.NamespaceKey) of its provider and settings.
func Namespace(cfg database.DatabaseConfig) (string, error) {
	if cfg.Objstore == nil {
		return "", errNoObjstoreConfig
	}
	return objstore.NamespaceKey(cfg.Objstore.Provider, cfg.Objstore.Settings)
}

// ValidateSettings is the database.ObjectStorageSettingsValidator the node
// composers wire with WithObjectStorageValidator: it refuses an inline
// credential among a database's Objstore settings (go-objstore owns the set of
// credential setting names) with a *database.ObjstoreCredentialSettingError
// naming the setting, never its value; the cause stays reachable through
// Unwrap.
func ValidateSettings(settings map[string]string) error {
	err := objstore.RejectCredentialSettings(settings)
	if err == nil {
		return nil
	}
	settingErr := &database.ObjstoreCredentialSettingError{Err: err}
	var cause *objstore.CredentialSettingError
	if errors.As(err, &cause) {
		settingErr.Setting = cause.Setting
	}
	return settingErr
}
