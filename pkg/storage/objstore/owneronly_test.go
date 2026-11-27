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
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireOwnerOnlyTree fails unless every entry under tree, the root
// included, grants nothing to group or other users.
func requireOwnerOnlyTree(t *testing.T, tree string) (files int) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		assert.Zero(t, st.Mode().Perm()&0o077, "%s has mode %v", p, st.Mode().Perm())
		if !d.IsDir() {
			files++
		}
		return nil
	}))
	return files
}

func TestNew_OwnerOnlyTreeKeepsDataAndJournalOwnerOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	e, err := New("local", map[string]string{"path": root}, WithOwnerOnlyTree())
	require.NoError(t, err)

	require.NoError(t, e.Set([]byte("a/b/c"), []byte("v")))
	// A journal left in place shows the journal tree's modes too.
	require.NoError(t, e.writeJournal([]byte("j")))

	nested := filepath.Join(root, "nested", "dir")
	require.NoError(t, e.store.Put("nested/dir/obj", bytes.NewReader([]byte("n"))))
	st, err := os.Stat(nested)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), st.Mode().Perm())

	st, err = os.Stat(root)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), st.Mode().Perm())
	assert.NotZero(t, requireOwnerOnlyTree(t, root))
	assert.NotZero(t, requireOwnerOnlyTree(t, root+journalTreeSuffix))
	require.NoError(t, e.journal.Delete(journalName))
	require.NoError(t, e.Close())
}

func TestNew_OwnerOnlyTreeRefusesAGroupAccessibleRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	require.NoError(t, os.Mkdir(root, 0o700))
	require.NoError(t, os.Chmod(root, 0o750)) // #nosec G302 -- a group-accessible root is the fault this test injects
	e, err := New("local", map[string]string{"path": root}, WithOwnerOnlyTree())
	assert.Nil(t, e)
	var modeErr *GroupAccessibleRootError
	require.True(t, errors.As(err, &modeErr), "want *GroupAccessibleRootError, got %v", err)
	assert.Equal(t, root, modeErr.Path)
	assert.Equal(t, fs.FileMode(0o750), modeErr.Mode)
	assert.Contains(t, err.Error(), root)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestNew_OwnerOnlyTreeLeavesMemoryUnaffected(t *testing.T) {
	e, err := New("memory", map[string]string{}, WithOwnerOnlyTree())
	require.NoError(t, err)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	require.NoError(t, e.Close())
}
