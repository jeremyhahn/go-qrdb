// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package objstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOwnerOnlyLocal opens a local engine with the owner-only tree and
// returns it with its owner-only store and root.
func newOwnerOnlyLocal(t *testing.T) (*Engine, *ownerOnlyStore, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")
	e, err := New("local", map[string]string{"path": root}, WithOwnerOnlyTree())
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	s, ok := e.store.(*ownerOnlyStore)
	require.True(t, ok, "the local store must be wrapped owner-only")
	return e, s, root
}

// TestOwnerOnlyStore_EveryPutRestrictsModes proves each put entry point
// leaves the object 0600 and its parent directories 0700, and that a put
// the inner store refuses returns that error.
func TestOwnerOnlyStore_EveryPutRestrictsModes(t *testing.T) {
	ctx := context.Background()
	puts := []struct {
		name string
		put  func(s *ownerOnlyStore, key string, data []byte) error
	}{
		{"Put", func(s *ownerOnlyStore, key string, data []byte) error {
			return s.Put(key, bytes.NewReader(data))
		}},
		{"PutWithContext", func(s *ownerOnlyStore, key string, data []byte) error {
			return s.PutWithContext(ctx, key, bytes.NewReader(data))
		}},
		{"PutWithMetadata", func(s *ownerOnlyStore, key string, data []byte) error {
			return s.PutWithMetadata(ctx, key, bytes.NewReader(data), nil)
		}},
	}
	for _, tc := range puts {
		t.Run(tc.name, func(t *testing.T) {
			_, s, root := newOwnerOnlyLocal(t)
			require.NoError(t, tc.put(s, "d1/d2/obj", []byte("v")))
			st, err := os.Stat(filepath.Join(root, "d1", "d2", "obj"))
			require.NoError(t, err)
			assert.Equal(t, fs.FileMode(0o600), st.Mode().Perm())
			for _, dir := range []string{filepath.Join(root, "d1"), filepath.Join(root, "d1", "d2")} {
				st, err := os.Stat(dir)
				require.NoError(t, err)
				assert.Equal(t, fs.FileMode(0o700), st.Mode().Perm(), dir)
			}

			// An object under a regular file cannot be written: the
			// inner store's error comes back unchanged.
			require.NoError(t, tc.put(s, "plain", []byte("f")))
			require.Error(t, tc.put(s, "plain/child", []byte("c")))
		})
	}
}

// TestOwnerOnlyStore_RestrictReportsChmodFailure proves restrict returns a
// chmod error other than not-exist instead of hiding it.
func TestOwnerOnlyStore_RestrictReportsChmodFailure(t *testing.T) {
	_, s, _ := newOwnerOnlyLocal(t)
	require.NoError(t, s.Put("plain", bytes.NewReader([]byte("f"))))
	require.Error(t, s.restrict("plain/child"), "a path under a regular file is ENOTDIR, not absent")
	require.NoError(t, s.restrict("absent/object"), "an absent object is not an error")
}

// TestEnsureOwnerOnlyRoot_RootUnderAFileFails proves a root that cannot be
// created is refused.
func TestEnsureOwnerOnlyRoot_RootUnderAFileFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	require.Error(t, ensureOwnerOnlyRoot(filepath.Join(file, "root")))

	_, err := New("local", map[string]string{"path": filepath.Join(file, "root")}, WithOwnerOnlyTree())
	require.Error(t, err)
}

// TestOpenStores_CreatorAndRootFailures proves openStores returns the
// error when the root does not resolve, when reopening the data store on
// the physical root fails, and when the journal store cannot be created.
func TestOpenStores_CreatorAndRootFailures(t *testing.T) {
	_, inner, _ := newOwnerOnlyLocal(t)
	errCreate := errors.New("create failed")

	physical := filepath.Join(t.TempDir(), "physical")
	require.NoError(t, os.MkdirAll(physical, 0o700))
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(physical, link))
	plain := filepath.Join(t.TempDir(), "plain")
	require.NoError(t, os.MkdirAll(plain, 0o700))

	cases := []struct {
		name   string
		root   string
		failAt int // 1-indexed create call that fails; 0 never
		calls  int // create calls expected
	}{
		{name: "root does not resolve", root: filepath.Join(t.TempDir(), "absent"), calls: 0},
		{name: "data reopen on the physical root", root: link, failAt: 1, calls: 1},
		{name: "journal store", root: plain, failAt: 1, calls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			create := func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
				calls++
				if calls == tc.failAt {
					return nil, errCreate
				}
				return inner, nil
			}
			data, journal, err := openStores("local", create, map[string]string{"path": tc.root}, inner, objcommon.Credentials{})
			require.Error(t, err)
			if tc.failAt != 0 {
				require.ErrorIs(t, err, errCreate)
			}
			require.Nil(t, data)
			require.Nil(t, journal)
			require.Equal(t, tc.calls, calls)
		})
	}
}

// TestEnsureLSMFormat_StoreFailures proves EnsureLSMFormat refuses a nil
// store and returns the store's error when the marker body cannot be read.
func TestEnsureLSMFormat_StoreFailures(t *testing.T) {
	ctx := context.Background()
	require.Error(t, EnsureLSMFormat(ctx, nil))

	t.Run("marker body unreadable", func(t *testing.T) {
		_, inner, _ := newOwnerOnlyLocal(t)
		require.NoError(t, inner.Put(formatMarkerName, bytes.NewReader([]byte(lsmFormatVersion))))
		require.ErrorIs(t, EnsureLSMFormat(ctx, &lsmFaultStore{Storage: inner, readFails: true}), errProvider)
	})
	t.Run("bucket list fails", func(t *testing.T) {
		_, inner, _ := newOwnerOnlyLocal(t)
		require.NoError(t, inner.Delete(formatMarkerName))
		require.ErrorIs(t, EnsureLSMFormat(ctx, &lsmFaultStore{Storage: inner, listFails: true}), errProvider)
	})
}

// lsmFaultStore fails the context-aware calls EnsureLSMFormat makes: the
// marker body read and the bucket listing. Every other call reaches the
// real store.
type lsmFaultStore struct {
	objcommon.Storage
	readFails, listFails bool
}

func (s *lsmFaultStore) GetWithContext(ctx context.Context, name string) (io.ReadCloser, error) {
	rc, err := s.Storage.GetWithContext(ctx, name)
	if err != nil || !s.readFails {
		return rc, err
	}
	_ = rc.Close()
	return io.NopCloser(&failingReader{}), nil
}

func (s *lsmFaultStore) ListWithContext(ctx context.Context, prefix string) ([]string, error) {
	if s.listFails {
		return nil, errProvider
	}
	return s.Storage.ListWithContext(ctx, prefix)
}
