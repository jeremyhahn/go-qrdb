package objstore

import (
	"os"
	"path/filepath"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/require"
)

// valueStore is a provider value type that is not comparable: it holds a
// map, so == or a map key on it panics. Each one is the store created for
// one path setting, and Close counts its closes under that path.
type valueStore struct {
	objcommon.Storage
	path   string
	closes map[string]int
}

func (s valueStore) Close() error {
	s.closes[s.path]++
	return nil
}

// registerValueStore registers a creator under name returning a fresh
// valueStore per call, recording each created path in order.
func registerValueStore(t *testing.T, name string) (closes map[string]int, created *[]string) {
	t.Helper()
	closes = map[string]int{}
	created = new([]string)
	prev, hadPrev := registeredProviders.Load(name)
	registeredProviders.Store(name, registeredProvider{
		creator: func(settings map[string]string, _ objcommon.Credentials) (objcommon.Storage, error) {
			mem := objmemory.New()
			if err := mem.Configure(map[string]string{}, objcommon.Credentials{}); err != nil {
				return nil, err
			}
			*created = append(*created, settings["path"])
			return valueStore{Storage: mem, path: settings["path"], closes: closes}, nil
		},
	})
	t.Cleanup(func() {
		if hadPrev {
			registeredProviders.Store(name, prev)
		} else {
			registeredProviders.Delete(name)
		}
	})
	return closes, created
}

// A provider outside walkedRoots keeps one store as data and journal: New
// and Close release it exactly once, without comparing the values.
func TestNew_NonComparableProviderSharedSlot(t *testing.T) {
	closes, created := registerValueStore(t, "valuestore-test")
	e, err := New("valuestore-test", map[string]string{"path": "p"})
	require.NoError(t, err)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	require.Equal(t, []string{"p"}, *created)
	require.Empty(t, closes)
	require.NoError(t, e.Close())
	require.Equal(t, map[string]int{"p": 1}, closes)
}

// A walked provider opened through a symlink creates three stores: the
// one at the symlink is released at open, and the data and journal stores
// at Close, each exactly once.
func TestNew_NonComparableProviderWalkedSlots(t *testing.T) {
	closes, created := registerValueStore(t, "local")
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	require.NoError(t, os.Mkdir(real, 0o700))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(real, link))

	e, err := New("local", map[string]string{"path": link})
	require.NoError(t, err)
	require.Len(t, *created, 3)
	require.Equal(t, link, (*created)[0])
	require.Equal(t, map[string]int{link: 1}, closes, "the store at the symlink is released at open")

	require.NoError(t, e.Close())
	want := map[string]int{}
	for _, p := range *created {
		want[p] = 1
	}
	require.Equal(t, want, closes)
}

// OpenStore on a symlinked root keeps only the store reopened on the
// physical root: the store it opened at the symlink is released at once,
// and a failed canonical reopen releases the store it opened.
func TestOpenStore_ReleasesReplacedStore(t *testing.T) {
	closes, created := registerValueStore(t, "local")
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	require.NoError(t, os.Mkdir(real, 0o700))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(real, link))

	store, err := OpenStore("local", map[string]string{"path": link})
	require.NoError(t, err)
	require.Equal(t, []string{link, real}, *created)
	require.Equal(t, map[string]int{link: 1}, closes, "the store at the symlink is released at open")
	require.NoError(t, closeStores(store))
	require.Equal(t, map[string]int{link: 1, real: 1}, closes)

	// A root that does not resolve fails the canonical reopen; the store
	// opened on it is released, not leaked.
	missing := filepath.Join(dir, "missing")
	_, err = OpenStore("local", map[string]string{"path": missing})
	require.Error(t, err)
	require.Equal(t, 1, closes[missing], "the store opened before the failure is released")
}

// sameStorage compares only comparable values of one type.
func TestSameStorage(t *testing.T) {
	mem := objmemory.New()
	v := valueStore{Storage: mem, closes: map[string]int{}}
	require.True(t, sameStorage(mem, mem))
	require.False(t, sameStorage(mem, objmemory.New()))
	require.False(t, sameStorage(v, v))
	require.False(t, sameStorage(v, mem))
	require.False(t, sameStorage(nil, mem))
}
