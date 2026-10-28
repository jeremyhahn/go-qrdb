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

//go:build !integration

package node

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

const pebbleFactorySentinel = "PEBBLE-FACTORY-SENTINEL-PLAINTEXT"

// openPebbleOver is a composer's Pebble-backed storage factory written the
// way the storage-engines guidance tells it to be: its I/O goes through
// pebblestore.VFS over the data-plane file system the node hands it.
func openPebbleOver(dir string, dataPlaneFS filesys.FS) (storage.Storage, error) {
	cfg := pebbleStorage.TestConfig(dir)
	if dataPlaneFS != nil {
		cfg.FS = pebbleStorage.VFS(dataPlaneFS)
	}
	return pebbleStorage.New(cfg)
}

// requirePebbleUnderBarrier writes the sentinel through s, closes it, and
// requires that no file under dir holds the plaintext.
func requirePebbleUnderBarrier(t *testing.T, s storage.Storage, dir string) {
	t.Helper()
	require.NoError(t, s.Set([]byte("k"), []byte(pebbleFactorySentinel)))
	got, err := s.Get([]byte("k"))
	require.NoError(t, err)
	require.Equal(t, pebbleFactorySentinel, string(got))
	require.NoError(t, s.Close())
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		// #nosec G122 -- the tree being walked is a t.TempDir() this test created and no other process can reach.
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		require.False(t, bytes.Contains(body, []byte(pebbleFactorySentinel)), "plaintext leaked in %s", p)
		return nil
	}))
}

func TestCreateNodeStorage_PebbleFactoryUnderBarrier(t *testing.T) {
	enc := atrest.New(sealtest.UnsealedBarrier(t))
	dir := t.TempDir()
	s, pdb, err := createNodeStorage(openPebbleOver, database.BackendPebble, dir, enc, qrdbconfig.ObjstoreConfig{})
	require.NoError(t, err, "a Pebble-backed factory must take its database lock through the barrier's file system")
	require.Nil(t, pdb)
	requirePebbleUnderBarrier(t, s, dir)
}

func TestCreateNodeStorage_PebbleFactoryHoldsTheLock(t *testing.T) {
	enc := atrest.New(sealtest.UnsealedBarrier(t))
	dir := t.TempDir()
	s, _, err := createNodeStorage(openPebbleOver, database.BackendPebble, dir, enc, qrdbconfig.ObjstoreConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	_, _, err = createNodeStorage(openPebbleOver, database.BackendPebble, dir, enc, qrdbconfig.ObjstoreConfig{})
	require.Error(t, err, "a second open of a held data directory must fail on its lock")
}

// TestPebbleFactoryLock_ExcludesEveryOpenerInProcess opens one data
// directory through the encrypted factory filesystem (VFS(enc.FS())) and
// through each other way a Pebble store in this process reaches the same
// directory -- WrapVFS(nil, enc.Wrap), and Pebble's own default file system
// -- in both orders. The second open must fail on the lock while the first
// store is open, the failure must leave the first store's lock in place,
// and the directory opens again once the first store closes. A POSIX record
// lock belongs to the process, so this holds only when every opener shares
// one record of the locks the process holds.
func TestPebbleFactoryLock_ExcludesEveryOpenerInProcess(t *testing.T) {
	enc := atrest.New(sealtest.UnsealedBarrier(t))
	type opener func(dir string) (storage.Storage, error)
	factory := func(dir string) (storage.Storage, error) { return openPebbleOver(dir, enc.FS()) }
	openers := map[string]opener{
		"wrapvfs": func(dir string) (storage.Storage, error) {
			cfg := pebbleStorage.TestConfig(dir)
			cfg.FS = pebbleStorage.WrapVFS(nil, enc.Wrap)
			return pebbleStorage.New(cfg)
		},
		// A Config with no FS opens on Pebble's own vfs.Default.
		"pebble default": func(dir string) (storage.Storage, error) {
			return pebbleStorage.New(pebbleStorage.TestConfig(dir))
		},
	}
	for name, other := range openers {
		orders := map[string][2]opener{
			"factory then " + name: {factory, other},
			name + " then factory": {other, factory},
		}
		for order, o := range orders {
			t.Run(order, func(t *testing.T) {
				dir := t.TempDir()
				first, err := o[0](dir)
				require.NoError(t, err)
				if s, err := o[1](dir); err == nil {
					_ = s.Close()
					_ = first.Close()
					t.Fatal("a second store opened a data directory another store in this process holds")
				}
				if s, err := o[0](dir); err == nil {
					_ = s.Close()
					_ = first.Close()
					t.Fatal("the failed open released the first store's lock")
				}
				require.NoError(t, first.Close())
				// Reopened as it was written: a plaintext opener cannot read
				// the encrypted store's manifest, whatever the lock says.
				again, err := o[0](dir)
				require.NoError(t, err, "the directory must open once its holder closes")
				require.NoError(t, again.Close())
			})
		}
	}
}

func TestBindReplicaStorage_PebbleFactoryUnderBarrier(t *testing.T) {
	enc := atrest.New(sealtest.UnsealedBarrier(t))
	open := bindReplicaStorage(func(_, _ uint64, dir string, dataPlaneFS filesys.FS) (storage.Storage, error) {
		return openPebbleOver(dir, dataPlaneFS)
	}, enc)
	dir := t.TempDir()
	s, err := open(3, 1, dir)
	require.NoError(t, err, "a Pebble-backed replica factory must take its database lock through the barrier's file system")
	requirePebbleUnderBarrier(t, s, dir)
}
