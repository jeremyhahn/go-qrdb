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
// database.WithObjectStorage(objstoredb.Open).
package objstoredb

import (
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
	engine, err := objstore.New(oc.Provider, oc.Settings, objstore.WithCredentialFiles(objstore.CredentialFiles{
		KeyFile:    oc.KeyFile,
		SecretFile: oc.SecretFile,
	}))
	if err != nil {
		return nil, err
	}
	return engine, nil
}

// NewBackendFactory returns the embedded backend factory with object storage
// wired in.
func NewBackendFactory() *database.EmbeddedBackendFactory {
	return database.NewEmbeddedBackendFactory(database.WithObjectStorage(Open))
}
