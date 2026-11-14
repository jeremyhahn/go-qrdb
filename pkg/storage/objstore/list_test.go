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
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestJournal_LocalProviderKeepsItOutOfTheListedTree pins the cause of a
// listing failing while a batch commits: while a journal is published it is
// invisible to a listing of the data store, and it is gone once the commit is
// final.
func TestJournal_LocalProviderKeepsItOutOfTheListedTree(t *testing.T) {
	e, err := New("local", map[string]string{"path": t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	require.NoError(t, e.writeJournal(encodeJournal(nil)))

	listed, err := e.store.List("")
	require.NoError(t, err)
	assert.NotContains(t, listed, journalName)
	journaled, err := e.journal.List("")
	require.NoError(t, err)
	assert.Equal(t, []string{journalName}, journaled)

	require.NoError(t, e.clearJournal())
	journaled, err = e.journal.List("")
	require.NoError(t, err)
	assert.Empty(t, journaled)
}

// TestJournal_NonWalkedProviderSharesTheStore checks a provider whose List is
// not a tree walk keeps the journal beside its data.
func TestJournal_NonWalkedProviderSharesTheStore(t *testing.T) {
	e := newMemEngine(t)
	assert.Same(t, e.store, e.journal)
}

// TestJournalRoot_IsASiblingOfTheCleanedRoot checks the journal tree is derived
// from the resolved root, so no spelling of the root puts it inside the data
// tree.
func TestJournalRoot_IsASiblingOfTheCleanedRoot(t *testing.T) {
	sep := string(filepath.Separator)
	want := filepath.Clean(sep+"x") + journalTreeSuffix
	if runtime.GOOS == "windows" {
		want = filepath.Clean(filepath.VolumeName(os.TempDir())+sep+"x") + journalTreeSuffix
	}
	base := filepath.VolumeName(os.TempDir()) + sep + "x"
	for name, root := range map[string]string{
		"plain":              base,
		"trailing dot":       base + sep + ".",
		"trailing separator": base + sep,
		"redundant elements": base + sep + "y" + sep + "..",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := journalRoot(root)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	t.Run("relative", func(t *testing.T) {
		got, err := journalRoot("rel" + sep + "db" + sep + ".")
		require.NoError(t, err)
		abs, err := filepath.Abs(filepath.Join("rel", "db"))
		require.NoError(t, err)
		assert.Equal(t, abs+journalTreeSuffix, got)
	})

	if runtime.GOOS == "windows" {
		t.Run("windows separators", func(t *testing.T) {
			got, err := journalRoot(`C:\data\db\.`)
			require.NoError(t, err)
			assert.Equal(t, `C:\data\db`+journalTreeSuffix, got)
		})
	}
}

// symlinkedDir returns a fresh directory and a symlink beside it naming it.
func symlinkedDir(t *testing.T) (target, link string) {
	t.Helper()
	base := t.TempDir()
	target = filepath.Join(base, "data")
	require.NoError(t, os.Mkdir(target, 0o700))
	link = filepath.Join(base, "link")
	require.NoError(t, os.Symlink(target, link))
	return target, link
}

// TestJournal_SymlinkedRootSharesOneJournal opens one local store through a
// symlinked directory and through its target. Both spellings name the same
// data tree, so both must name the same journal: a batch interrupted under one
// is recovered under the other, and no second journal tree appears beside the
// link.
func TestJournal_SymlinkedRootSharesOneJournal(t *testing.T) {
	for name, linkFirst := range map[string]bool{
		"link then target": true,
		"target then link": false,
	} {
		t.Run(name, func(t *testing.T) {
			target, link := symlinkedDir(t)
			firstPath, secondPath := target, link
			if linkFirst {
				firstPath, secondPath = link, target
			}
			key := []byte("k")
			first, err := New("local", map[string]string{"path": firstPath})
			require.NoError(t, err)
			// The batch is interrupted after its journal publish: the apply
			// never runs.
			require.NoError(t, first.writeJournal(encodeJournal([]storage.BufferedOp{{Key: key, Value: []byte("batch")}})))
			require.NoError(t, first.Close())

			second, err := New("local", map[string]string{"path": secondPath})
			require.NoError(t, err)
			t.Cleanup(func() { _ = second.Close() })
			got, err := second.Get(key)
			require.NoError(t, err)
			assert.Equal(t, "batch", string(got), "the reopen must recover the batch journaled under the other spelling")
			keys, err := second.List(nil)
			require.NoError(t, err)
			assert.Equal(t, [][]byte{key}, keys, "both spellings must list the one data tree")

			_, err = os.Lstat(link + journalTreeSuffix)
			assert.True(t, os.IsNotExist(err), "a journal tree beside the link, got %v", err)
		})
	}
}

// TestJournal_RootUnderASymlinkedParentIsResolved opens a store whose root
// does not exist yet, under a symlinked parent: the journal is still derived
// from the physical directory.
func TestJournal_RootUnderASymlinkedParentIsResolved(t *testing.T) {
	target, link := symlinkedDir(t)

	e, err := New("local", map[string]string{"path": filepath.Join(link, "db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	require.NoError(t, e.writeJournal(encodeJournal(nil)))

	physical, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(physical, "db"+journalTreeSuffix, journalName))
	assert.NoError(t, err, "the journal must sit beside the physical root")
}

// TestJournal_RootSpelledWithADotKeepsTheJournalOutOfTheTree opens a store
// through a root spelled "<dir>/." and checks the journal tree is the sibling.
func TestJournal_RootSpelledWithADotKeepsTheJournalOutOfTheTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	e, err := New("local", map[string]string{"path": dir + string(filepath.Separator) + "."})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	require.NoError(t, e.writeJournal(encodeJournal(nil)))
	_, err = os.Stat(dir + journalTreeSuffix)
	require.NoError(t, err)
	listed, err := e.store.List("")
	require.NoError(t, err)
	assert.NotContains(t, listed, journalName)
}

// TestNew_RefusesAJournalInTheDataTree checks a local store left with an
// interrupted batch by the earlier in-tree layout is refused, not served.
func TestNew_RefusesAJournalInTheDataTree(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, journalName), encodeJournal(nil), 0o600))

	e, err := New("local", map[string]string{"path": dir})
	require.Nil(t, e)
	var inTree *InTreeJournalError
	require.ErrorAs(t, err, &inTree)
	assert.Equal(t, "local", inTree.Provider)
	assert.Contains(t, err.Error(), journalName)
}

// TestNew_RefusesAJournalInThePrefixedNamespace checks the in-tree journal
// refusal also runs inside an object prefix: a legacy journal under the
// database's namespace refuses the prefixed open.
func TestNew_RefusesAJournalInThePrefixedNamespace(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "ns"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ns", journalName), encodeJournal(nil), 0o600))

	e, err := New("local", map[string]string{"path": dir}, WithObjectPrefix("ns/"))
	require.Nil(t, e)
	var inTree *InTreeJournalError
	require.ErrorAs(t, err, &inTree)
	assert.Equal(t, "local", inTree.Provider)
	assert.Contains(t, err.Error(), journalName)
}

// TestDestroy_ReclaimsEveryObjectInTheNamespace checks Destroy deletes the
// records, format marker and journal beneath its prefix, so an engine
// reopened on the prefix finds none of them, while a sibling namespace keeps
// its objects.
func TestDestroy_ReclaimsEveryObjectInTheNamespace(t *testing.T) {
	dir := t.TempDir()
	settings := map[string]string{"path": dir}
	sibling, err := New("local", settings, WithObjectPrefix("other/"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sibling.Close() })
	require.NoError(t, sibling.Set([]byte("k"), []byte("kept")))

	e, err := New("local", settings, WithObjectPrefix("ns/"))
	require.NoError(t, err)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	require.NoError(t, e.writeJournal(encodeJournal(nil)))
	require.NoError(t, e.Destroy())
	listed, err := e.store.List("")
	require.NoError(t, err)
	assert.Empty(t, listed, "no record or format marker may survive")
	journaled, err := e.journal.List("")
	require.NoError(t, err)
	assert.Empty(t, journaled, "no journal may survive")
	_, err = e.Get([]byte("k"))
	var closed *storage.ClosedError
	assert.ErrorAs(t, err, &closed)

	reopened, err := New("local", settings, WithObjectPrefix("ns/"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.Get([]byte("k"))
	require.NoError(t, err)
	assert.Nil(t, got, "a destroyed key must not resurrect")
	got, err = sibling.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, "kept", string(got))
}

// TestDestroy_DeleteFaultFails checks a Destroy that cannot delete an object
// reports it rather than succeeding.
func TestDestroy_DeleteFaultFails(t *testing.T) {
	e, fs := newFaultEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	fs.deleteFails = true
	assert.ErrorIs(t, e.Destroy(), errProvider)
}
