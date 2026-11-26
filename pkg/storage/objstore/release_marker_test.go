package objstore

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileTree maps every regular file under each of roots to its contents.
func fileTree(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tree[path] = string(body)
			return nil
		})
		require.NoError(t, err)
	}
	return tree
}

// withoutOwnerMarker drops the owner marker (and the local provider's
// metadata file for it) from tree.
func withoutOwnerMarker(tree map[string]string, root string) map[string]string {
	out := maps.Clone(tree)
	for path := range out {
		if filepath.Dir(path) == root && strings.HasPrefix(filepath.Base(path), ownerMarkerName) {
			delete(out, path)
		}
	}
	return out
}

// corruptJournalNamespace leaves root as a crashed process would: records
// and the format marker owned by owner (when set), and a journal no open
// can recover.
func corruptJournalNamespace(t *testing.T, root, owner string) {
	t.Helper()
	var opts []Option
	if owner != "" {
		opts = append(opts, WithOwner(owner))
	}
	e, err := New("local", map[string]string{"path": root}, opts...)
	require.NoError(t, err)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	require.NoError(t, e.writeJournal([]byte("not a journal image")))
	require.NoError(t, e.Close())
	_, err = New("local", map[string]string{"path": root}, opts...)
	var corrupt *JournalCorruptError
	require.ErrorAs(t, err, &corrupt, "the namespace's journal must fail every open")
}

func TestReleaseOwnerMarkerOf(t *testing.T) {
	t.Run("own marker over a corrupt journal", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		corruptJournalNamespace(t, root, "db-a")
		before := fileTree(t, root, root+journalTreeSuffix)
		require.NotEqual(t, before, withoutOwnerMarker(before, root), "no owner marker to release")

		require.NoError(t, ReleaseOwnerMarkerOf("local", map[string]string{"path": root}, WithOwner("db-a")))
		after := fileTree(t, root, root+journalTreeSuffix)
		assert.Equal(t, withoutOwnerMarker(before, root), after,
			"only the owner marker goes; the records and the journal stay byte for byte")

		// Released twice is still released.
		require.NoError(t, ReleaseOwnerMarkerOf("local", map[string]string{"path": root}, WithOwner("db-a")))
	})
	t.Run("marker naming another owner", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		corruptJournalNamespace(t, root, "db-b")
		before := fileTree(t, root, root+journalTreeSuffix)
		require.NoError(t, ReleaseOwnerMarkerOf("local", map[string]string{"path": root}, WithOwner("db-a")))
		assert.Equal(t, before, fileTree(t, root, root+journalTreeSuffix))
	})
	t.Run("unowned call releases nothing", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "objects")
		corruptJournalNamespace(t, root, "db-a")
		before := fileTree(t, root, root+journalTreeSuffix)
		require.NoError(t, ReleaseOwnerMarkerOf("local", map[string]string{"path": root}))
		assert.Equal(t, before, fileTree(t, root, root+journalTreeSuffix))
	})
	t.Run("no provider", func(t *testing.T) {
		assert.Error(t, ReleaseOwnerMarkerOf("", nil, WithOwner("db-a")))
	})
	t.Run("unknown provider", func(t *testing.T) {
		assert.Error(t, ReleaseOwnerMarkerOf("no-such-provider", map[string]string{}, WithOwner("db-a")))
	})
}
