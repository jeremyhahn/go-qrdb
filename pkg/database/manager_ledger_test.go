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

	dropped, err := mgr.WasDropped("never-touched")
	require.NoError(t, err)
	assert.False(t, dropped, "a name this manager never recorded a drop for")

	require.NoError(t, mgr.recordDrop("orders"))
	dropped, err = mgr.WasDropped("orders")
	require.NoError(t, err)
	assert.True(t, dropped, "a name recordDrop just tombstoned")
}

// forgetDrop is a no-op, not an error, for a name that was never dropped
// (or whose tombstone forgetDrop already cleared): the os.Remove ErrNotExist
// case it exists to swallow.
func TestManager_ForgetDrop_NoOpWhenAbsent(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	require.NoError(t, mgr.forgetDrop("never-dropped"))

	require.NoError(t, mgr.recordDrop("orders"))
	require.NoError(t, mgr.forgetDrop("orders"))
	dropped, err := mgr.droppedHere("orders")
	require.NoError(t, err)
	assert.False(t, dropped, "forgetDrop clears the tombstone")

	// Forgetting it again is still a no-op, not a second error.
	require.NoError(t, mgr.forgetDrop("orders"))
}

// recordDrop reports *MetaWriteError, not a silent no-op, when it cannot
// even create the directory its tombstone files live in.
func TestManager_RecordDrop_MkdirFailureIsAMetaWriteError(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	// A plain FILE already occupies the path recordDrop wants to MkdirAll,
	// so MkdirAll fails (not a directory) instead of creating it.
	require.NoError(t, os.WriteFile(filepath.Join(mgr.dataDir, droppedDir), []byte("x"), 0o600))

	err := mgr.recordDrop("orders")
	var writeErr *MetaWriteError
	require.ErrorAs(t, err, &writeErr)
}

// droppedHere reports *MetaReadError, not "not dropped", when os.Stat fails
// for a reason OTHER than the tombstone being absent.
func TestManager_DroppedHere_StatFailureIsAMetaReadError(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	dir := filepath.Join(mgr.dataDir, droppedDir)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	// #nosec G302 -- the test removes every permission from a temp directory to force the I/O error path
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) // #nosec G302 -- restores the temp directory so t.TempDir can remove it

	_, err := mgr.droppedHere("orders")
	var readErr *MetaReadError
	require.ErrorAs(t, err, &readErr)
}

// forgetDrop reports *MetaWriteError, not a silent success, when os.Remove
// fails for a reason OTHER than the tombstone already being gone.
func TestManager_ForgetDrop_RemoveFailureIsAMetaWriteError(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	require.NoError(t, mgr.recordDrop("orders"))
	dir := filepath.Join(mgr.dataDir, droppedDir)
	// #nosec G302 -- the test makes a temp directory read-only to force the remove error path
	require.NoError(t, os.Chmod(dir, 0o500))       // read-only: Stat/readdir fine, Remove refused
	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) // #nosec G302 -- restores the temp directory so t.TempDir can remove it

	err := mgr.forgetDrop("orders")
	var writeErr *MetaWriteError
	require.ErrorAs(t, err, &writeErr)
}
