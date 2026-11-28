package objstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNew_OwnerClaimRace opens one namespace from several engines in this
// process at once, each with its own owner: exactly one claims it, and every
// other open is refused with a NamespaceOwnedError naming the winner.
func TestNew_OwnerClaimRace(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	const n = 8

	start := make(chan struct{})
	engines := make([]*Engine, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			engines[i], errs[i] = New("local", settings, WithOwner(fmt.Sprintf("db-%d", i)))
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i := 0; i < n; i++ {
		if errs[i] == nil {
			require.Equal(t, -1, winner, "engines %d and %d both claimed the namespace", winner, i)
			winner = i
			continue
		}
		want, refused := ownerRefusal(errs[i])
		require.True(t, refused, "engine %d: got %v", i, errs[i])
		assert.Equal(t, fmt.Sprintf("db-%d", i), want)
	}
	require.NotEqual(t, -1, winner, "no engine claimed the namespace")
	e := engines[winner]
	defer func() { require.NoError(t, e.Close()) }()

	rc, err := e.store.Get(ownerMarkerName)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("db-%d", winner), string(got))
}

// TestNew_OwnerClaimRaceAcrossAliases opens one directory concurrently
// through its own path, a symlink to it, and an unclean spelling of each:
// they are one namespace, so exactly one owner claim wins.
func TestNew_OwnerClaimRaceAcrossAliases(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "data")
	require.NoError(t, os.Mkdir(dir, 0o700))
	link := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(dir, link))
	paths := []string{dir, link, dir + string(filepath.Separator) + ".", link + string(filepath.Separator)}
	const rounds = 4
	n := len(paths) * rounds

	start := make(chan struct{})
	engines := make([]*Engine, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			settings := map[string]string{"path": paths[i%len(paths)]}
			engines[i], errs[i] = New("local", settings, WithOwner(fmt.Sprintf("db-%d", i)))
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i := 0; i < n; i++ {
		if errs[i] == nil {
			require.Equal(t, -1, winner, "engines %d (%s) and %d (%s) both claimed the namespace",
				winner, paths[max(winner, 0)%len(paths)], i, paths[i%len(paths)])
			winner = i
			continue
		}
		_, refused := ownerRefusal(errs[i])
		require.True(t, refused, "engine %d: got %v", i, errs[i])
	}
	require.NotEqual(t, -1, winner, "no engine claimed the namespace")
	require.NoError(t, engines[winner].Close())
}
