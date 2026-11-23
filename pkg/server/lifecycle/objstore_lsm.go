// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lifecycle

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2/objstorage/remote"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble/remotelsm"
)

// objstoreLSMPrefix is the object-name prefix the LSM store's SSTables live
// under. A dedicated object store holds one owning instance, the dedication
// rule the flat engine's New states, so the prefix is a constant
// (object-storage-lsm OS-3 A).
const objstoreLSMPrefix = "lsm"

// objstoreLSMLocator names the shared storage the Pebble engine places its
// SSTables in; one store per instance, so one constant locator.
const objstoreLSMLocator remote.Locator = "qrdb"

// objstoreLSMDir is the directory under the data dir holding the LSM store's
// local Pebble state (WAL, MANIFEST, QRDB-CREATORID). It is distinct from the
// pebble backend's "pebble" and the local provider's default "objstore".
const objstoreLSMDir = "objstore-lsm"

// objstoreLSMCacheDir is the directory under the data dir holding the local
// object cache (objstore.CachedRemote, OS-6 A interim): one file per fetched
// or created SSTable, as stored (sealed when the barrier is on). It is a
// sibling of objstoreLSMDir so Pebble never sees foreign files in its dir.
const objstoreLSMCacheDir = "objstore-lsm-cache"

// newObjstoreLSMStorage opens the Pebble engine over the configured object
// store (object-storage-lsm): the store is resolved by objstore.OpenStore
// (archive tiers refused), refused unless it is empty or already carries the
// LSM format marker, and handed to Pebble as shared storage under
// objstoreLSMPrefix, encrypted object by object with the at-rest barrier when
// the barrier is on. Every new SSTable is created on the shared store; the
// WAL and MANIFEST stay in <dataDir>/objstore-lsm, encrypted by the same
// barrier through the local VFS. A sealed barrier fails closed before any
// store is opened or file created.
//
// Reads go through the local object cache in <dataDir>/objstore-lsm-cache
// (objstore.CachedRemote, OS-6 A interim), below the encrypting wrapper so it
// holds only the stored bytes: each SSTable is fetched from the store at most
// once, and a freshly created one is written through and never fetched.
//
// It is the composition NewLocalStorage's objstore case will call once the
// flat engine is replaced (object-storage-lsm slice 4b); nothing calls it yet.
// Neither it nor the returned storage closes the go-objstore store, matching
// the flat Engine's Close. The local object cache it builds is closed on any
// failure after it is built and by the returned storage's Close: Pebble
// never closes a remote.Storage it is given.
func newObjstoreLSMStorage(dataDir string, enc *atrest.Encryptor, storageCfg qrdbconfig.StorageConfig) (*objstoreLSMStorage, error) {
	if enc.Enabled() && enc.Barrier().IsSealed() {
		return nil, &ObjstoreBarrierSealedError{}
	}
	objCfg := storageCfg.Objstore
	provider, settings := resolveObjstoreSettings(objCfg, dataDir)
	store, err := objstore.OpenStore(provider, settings, objstore.WithCredentialFiles(objstore.CredentialFiles{
		KeyFile:    objCfg.Credentials.KeyFile,
		SecretFile: objCfg.Credentials.SecretFile,
	}))
	if err != nil {
		return nil, err
	}
	if err := objstore.EnsureLSMFormat(context.Background(), store); err != nil {
		return nil, err
	}
	var shared remote.Storage
	rs, err := objstore.NewRemote(store, objstoreLSMPrefix)
	if err != nil {
		return nil, err
	}
	cached, err := objstore.NewCachedRemote(rs, filepath.Join(dataDir, objstoreLSMCacheDir))
	if err != nil {
		return nil, err
	}
	shared = cached
	if enc.Enabled() {
		encrypted, err := objstore.NewEncryptedRemote(cached, enc.Barrier())
		if err != nil {
			return nil, errors.Join(err, cached.Close())
		}
		shared = encrypted
	}
	cfg := localPebbleConfig(filepath.Join(dataDir, objstoreLSMDir), enc, storageCfg)
	stg, err := pebbleStorage.New(cfg, remotelsm.WithRemote(shared, objstoreLSMLocator, remote.CreateOnSharedAll))
	if err != nil {
		return nil, errors.Join(err, cached.Close())
	}
	return &objstoreLSMStorage{PebbleStorage: stg, cache: cached}, nil
}

// objstoreLSMStorage is the object-storage-lsm engine: the Pebble storage
// plus the local object cache under it, which its Close releases after the
// storage.
type objstoreLSMStorage struct {
	*pebbleStorage.PebbleStorage
	cache *objstore.CachedRemote
}

// Close closes the Pebble storage, then the object cache (its directory
// lock and root). Both are idempotent, so a second Close is a no-op.
func (s *objstoreLSMStorage) Close() error {
	return errors.Join(s.PebbleStorage.Close(), s.cache.Close())
}
