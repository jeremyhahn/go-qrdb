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

package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WasDropped is the public accessor resolve's catalog lookup uses to tell
// "never a database here" from "dropped here" for a name the manager holds
// no record for; it is a thin wrapper over droppedHere and had no direct
// test of its own.
func TestManager_WasDropped(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	dropped, err := mgr.WasDropped("", "never-touched")
	require.NoError(t, err)
	assert.False(t, dropped, "a name this manager never recorded a drop for")

	require.NoError(t, mgr.recordDrop("", "orders"))
	dropped, err = mgr.WasDropped("", "orders")
	require.NoError(t, err)
	assert.True(t, dropped, "a name recordDrop just tombstoned")
}

// TestManager_DropLedger_KeyedByTenantAndName is the collision: two tenants
// and the system tenant hold one name, and a drop recorded or forgotten for
// one of them says nothing about the others.
func TestManager_DropLedger_KeyedByTenantAndName(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	require.NoError(t, mgr.recordDrop("tenant-a", "orders"))
	for _, tc := range []struct {
		tenant string
		want   bool
	}{{"tenant-a", true}, {"tenant-b", false}, {"", false}} {
		dropped, err := mgr.WasDropped(tc.tenant, "orders")
		require.NoError(t, err)
		assert.Equal(t, tc.want, dropped, "tenant %q after tenant-a's drop", tc.tenant)
	}

	require.NoError(t, mgr.recordDrop("tenant-b", "orders"))
	require.NoError(t, mgr.forgetDrop("tenant-a", "orders"))
	dropped, err := mgr.WasDropped("tenant-a", "orders")
	require.NoError(t, err)
	assert.False(t, dropped, "tenant-a's drop was forgotten")
	dropped, err = mgr.WasDropped("tenant-b", "orders")
	require.NoError(t, err)
	assert.True(t, dropped, "forgetting tenant-a's drop cleared tenant-b's")
}

// forgetDrop is a no-op, not an error, for a name that was never dropped
// (or whose tombstone forgetDrop already cleared): the os.Remove ErrNotExist
// case it exists to swallow.
func TestManager_ForgetDrop_NoOpWhenAbsent(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	require.NoError(t, mgr.forgetDrop("", "never-dropped"))

	require.NoError(t, mgr.recordDrop("", "orders"))
	require.NoError(t, mgr.forgetDrop("", "orders"))
	dropped, err := mgr.droppedHere("", "orders")
	require.NoError(t, err)
	assert.False(t, dropped, "forgetDrop clears the tombstone")

	// Forgetting it again is still a no-op, not a second error.
	require.NoError(t, mgr.forgetDrop("", "orders"))
}

// recordDrop reports *MetaWriteError, not a silent no-op, when it cannot
// even create the directory its tombstone files live in.
func TestManager_RecordDrop_MkdirFailureIsAMetaWriteError(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	// A plain FILE already occupies the path recordDrop wants to MkdirAll,
	// so MkdirAll fails (not a directory) instead of creating it.
	require.NoError(t, os.WriteFile(filepath.Join(mgr.dataDir, droppedDir), []byte("x"), 0o600))

	err := mgr.recordDrop("", "orders")
	var writeErr *MetaWriteError
	require.ErrorAs(t, err, &writeErr)
}

// droppedHere reports *MetaReadError, not "not dropped", when os.Stat fails
// for a reason OTHER than the tombstone being absent.
//
// A permission-denied directory would normally force this, but it does not
// under a root-running test process (CI containers commonly run as root,
// which bypasses DAC permission checks entirely): os.Stat would then still
// succeed in finding nothing, reporting a false negative rather than a read
// failure. Occupying droppedDir's own path with a plain FILE instead -- the
// same technique TestManager_RecordDrop_MkdirFailureIsAMetaWriteError uses
// -- forces ENOTDIR, a structural failure no uid, including root's, can
// bypass.
func TestManager_DroppedHere_StatFailureIsAMetaReadError(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	require.NoError(t, os.WriteFile(filepath.Join(mgr.dataDir, droppedDir), []byte("x"), 0o600))

	_, err := mgr.droppedHere("", "orders")
	var readErr *MetaReadError
	require.ErrorAs(t, err, &readErr)
}

// forgetDrop reports *MetaWriteError, not a silent success, when os.Remove
// fails for a reason OTHER than the tombstone already being gone.
//
// A read-only directory would normally force this, but -- same root-bypass
// problem as TestManager_DroppedHere_StatFailureIsAMetaReadError above --
// root's CAP_DAC_OVERRIDE ignores the directory's write permission too. A
// non-empty directory occupying the tombstone's path forces ENOTEMPTY
// instead: rmdir refusing a non-empty directory is a filesystem invariant,
// never a permission check, so no uid can bypass it.
func TestManager_ForgetDrop_RemoveFailureIsAMetaWriteError(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	path := filepath.Join(mgr.dataDir, droppedDir, "orders")
	require.NoError(t, os.MkdirAll(filepath.Join(path, "child"), 0o750))

	err := mgr.forgetDrop("", "orders")
	var writeErr *MetaWriteError
	require.ErrorAs(t, err, &writeErr)
}

// Two creates of different names share the databases root. When the first
// one creates that root, the second one then creates its own directory
// under it, and the first one fails and undoes, the undo must not take the
// second create's directory with it: that removal is what failed concurrent
// creates while the shard ID claimer was being attached.
func TestCreateUndo_DoesNotRemoveASiblingCreatesDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "databases")
	first := &createUndo{m: newCatalogManager(nil, t.TempDir()), name: "a"}
	second := &createUndo{m: first.m, name: "b"}

	require.NoError(t, first.mkdirAll(filepath.Join(root, "a")))
	require.NoError(t, second.mkdirAll(filepath.Join(root, "b")))
	require.NoError(t, first.run(context.Background(), nil))

	_, err := os.Stat(filepath.Join(root, "a"))
	assert.True(t, os.IsNotExist(err), "the failed create left its own directory behind")
	info, err := os.Stat(filepath.Join(root, "b"))
	require.NoError(t, err, "the failed create removed a concurrent create's directory")
	assert.True(t, info.IsDir())
}

// A directory that existed before the create belongs to someone else: the
// undo leaves it, and everything in it, alone.
func TestCreateUndo_LeavesAPreExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "existing")
	require.NoError(t, os.Mkdir(dir, 0o750))
	keep := filepath.Join(dir, "keep")
	require.NoError(t, os.WriteFile(keep, nil, 0o600))
	u := &createUndo{m: newCatalogManager(nil, t.TempDir()), name: "x"}

	require.NoError(t, u.mkdirAll(dir))
	require.NoError(t, u.run(context.Background(), nil))

	_, err := os.Stat(keep)
	assert.NoError(t, err, "the undo removed a directory the create did not make")
}
