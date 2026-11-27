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

package objstore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
)

// Owner-only modes for a filesystem store's tree.
const (
	ownerOnlyDirMode  fs.FileMode = 0o700
	ownerOnlyFileMode fs.FileMode = 0o600
)

// ownerOnlyCreator wraps create so a store of a provider with a filesystem
// root is created on an owner-only root and keeps every object it writes
// owner-only. A provider with no filesystem root opens through create as is.
func ownerOnlyCreator(provider string, create objfactory.StorageCreator) objfactory.StorageCreator {
	rootSetting, walked := walkedRoots[provider]
	if !walked {
		return create
	}
	return func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
		root := settings[rootSetting]
		if root == "" {
			return create(settings, creds)
		}
		if err := ensureOwnerOnlyRoot(root); err != nil {
			return nil, err
		}
		store, err := create(settings, creds)
		if err != nil {
			return nil, err
		}
		return &ownerOnlyStore{Storage: store, root: filepath.Clean(root)}, nil
	}
}

// ensureOwnerOnlyRoot creates root 0700 if it is absent and refuses it when
// its group or other users can access it.
func ensureOwnerOnlyRoot(root string) error {
	if err := os.MkdirAll(root, ownerOnlyDirMode); err != nil {
		return err
	}
	st, err := os.Stat(root)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return &GroupAccessibleRootError{Path: root, Mode: st.Mode().Perm()}
	}
	return nil
}

// ownerOnlyStore is a filesystem store whose writes leave the object 0600
// and every directory between it and the root 0700.
type ownerOnlyStore struct {
	objcommon.Storage
	root string
}

func (s *ownerOnlyStore) Put(key string, data io.Reader) error {
	if err := s.Storage.Put(key, data); err != nil {
		return err
	}
	return s.restrict(key)
}

func (s *ownerOnlyStore) PutWithContext(ctx context.Context, key string, data io.Reader) error {
	if err := s.Storage.PutWithContext(ctx, key, data); err != nil {
		return err
	}
	return s.restrict(key)
}

func (s *ownerOnlyStore) PutWithMetadata(ctx context.Context, key string, data io.Reader, metadata *objcommon.Metadata) error {
	if err := s.Storage.PutWithMetadata(ctx, key, data, metadata); err != nil {
		return err
	}
	return s.restrict(key)
}

// restrict makes the object under key 0600 and each directory from its
// parent up to, not including, the root 0700. A path a concurrent delete
// removed is already done.
func (s *ownerOnlyStore) restrict(key string) error {
	object := filepath.Join(s.root, filepath.FromSlash(key))
	if err := chmodIfPresent(object, ownerOnlyFileMode); err != nil {
		return err
	}
	for dir := filepath.Dir(object); dir != s.root && len(dir) > len(s.root); dir = filepath.Dir(dir) {
		if err := chmodIfPresent(dir, ownerOnlyDirMode); err != nil {
			return err
		}
	}
	return nil
}

// chmodIfPresent sets path's mode, treating a missing path as done.
func chmodIfPresent(path string, mode fs.FileMode) error {
	if err := os.Chmod(path, mode); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
