package objstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openLocal opens the local provider rooted at dir, creating dir first.
func openLocal(t *testing.T, dir string) (*Engine, error) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return New("local", map[string]string{"path": dir})
}

// TestNew_OverlappingRoots covers the refusal of a local root nested in, or
// holding, a root that is already open: each engine's walk would otherwise
// list, and Reclaim delete, the other's objects.
func TestNew_OverlappingRoots(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	child := filepath.Join(a, "child")

	tests := []struct {
		name   string
		first  string
		second string
		refuse bool
	}{
		{name: "parent then child refused", first: a, second: child, refuse: true},
		{name: "child then parent refused", first: child, second: a, refuse: true},
		{name: "siblings both open", first: a, second: filepath.Join(base, "b")},
		{name: "root inside the open journal refused", first: a, second: filepath.Join(a+journalTreeSuffix, "x"), refuse: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first, err := openLocal(t, tc.first)
			require.NoError(t, err)
			defer func() { require.NoError(t, first.Close()) }()

			second, err := openLocal(t, tc.second)
			if !tc.refuse {
				require.NoError(t, err)
				require.NoError(t, second.Close())
				return
			}
			var overlap *OverlappingRootError
			require.True(t, errors.As(err, &overlap), "got %v", err)
			assert.Contains(t, overlap.Error(), "overlaps the open root")
			assert.NotEmpty(t, overlap.Root)
			assert.NotEmpty(t, overlap.Other)
		})
	}
}

// TestNew_AliasedJournalRefused: a database whose journal directory is a
// symlink to another open database's journal shares that journal, so the
// second open is refused as an overlapping root.
func TestNew_AliasedJournalRefused(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	require.NoError(t, os.MkdirAll(b+journalTreeSuffix, 0o700))
	require.NoError(t, os.Symlink(b+journalTreeSuffix, a+journalTreeSuffix))

	first, err := openLocal(t, b)
	require.NoError(t, err)
	defer func() { require.NoError(t, first.Close()) }()

	second, err := openLocal(t, a)
	if err == nil {
		require.NoError(t, second.Close())
	}
	var overlap *OverlappingRootError
	require.True(t, errors.As(err, &overlap), "got %v", err)
}

// TestNew_OverlapReleasedOnClose covers the registry release: a nested root
// opens once the root holding it is closed.
func TestNew_OverlapReleasedOnClose(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a")
	parent, err := openLocal(t, a)
	require.NoError(t, err)
	require.NoError(t, parent.Close())

	child, err := openLocal(t, filepath.Join(a, "child"))
	require.NoError(t, err)
	require.NoError(t, child.Close())
}

// TestReclaim_LeavesForeignObjects covers Reclaim deleting only the names
// this engine writes: an object under the root that the engine did not
// write, such as a nested namespace's, stays in place.
func TestReclaim_LeavesForeignObjects(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a")
	e, err := openLocal(t, a)
	require.NoError(t, err)
	defer func() { require.NoError(t, e.Close()) }()
	require.NoError(t, e.Set([]byte("key"), []byte("value")))

	foreign := filepath.Join(a, "child", "x")
	require.NoError(t, os.MkdirAll(filepath.Dir(foreign), 0o700))
	require.NoError(t, os.WriteFile(foreign, []byte("foreign"), 0o600))

	require.NoError(t, e.Reclaim())
	_, err = os.Stat(foreign)
	assert.NoError(t, err, "Reclaim deleted an object it did not write")
	names, err := e.store.List("")
	require.NoError(t, err)
	assert.Equal(t, []string{"child/x"}, names)
}

// TestReclaim_LeavesNamespaceNestedUnderKeyPrefix proves Reclaim deletes
// only canonical record names: a namespace created at root/k/child (after
// the root's engine closed, so no overlap refusal applies) keeps its format
// marker and its records when the root's database is dropped.
func TestReclaim_LeavesNamespaceNestedUnderKeyPrefix(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a")
	e, err := openLocal(t, root)
	require.NoError(t, err)
	require.NoError(t, e.Set([]byte("key"), []byte("value")))
	require.NoError(t, e.Close())

	childRoot := filepath.Join(root, "k", "child")
	child, err := openLocal(t, childRoot)
	require.NoError(t, err)
	require.NoError(t, child.Set([]byte("childkey"), []byte("childvalue")))
	require.NoError(t, child.Close())

	e, err = openLocal(t, root)
	require.NoError(t, err)
	require.NoError(t, e.Reclaim())
	require.NoError(t, e.Close())

	_, err = os.Stat(filepath.Join(childRoot, formatMarkerName))
	require.NoError(t, err, "Reclaim deleted the nested namespace's format marker")
	child, err = openLocal(t, childRoot)
	require.NoError(t, err)
	defer func() { require.NoError(t, child.Close()) }()
	got, err := child.Get([]byte("childkey"))
	require.NoError(t, err)
	assert.Equal(t, []byte("childvalue"), got)
}
