package objstore

import (
	"fmt"
	"io"
	"sync"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerGateStore holds every read of the owner marker until the claims can
// overlap no further: either every claimant has read the marker (nothing
// serialises them), or every claimant holds or waits for the namespace's
// claim lock (open), so the claims run one at a time from here.
type ownerGateStore struct {
	objcommon.Storage
	n       int
	mu      sync.Mutex
	reads   int
	release chan struct{}
	once    sync.Once
}

func (s *ownerGateStore) open() { s.once.Do(func() { close(s.release) }) }

func (s *ownerGateStore) Get(name string) (io.ReadCloser, error) {
	if name == ownerMarkerName {
		s.mu.Lock()
		s.reads++
		all := s.reads == s.n
		s.mu.Unlock()
		if all {
			s.open()
		}
		<-s.release
	}
	return s.Storage.Get(name)
}

// setOwnerClaimQueued installs fn as ownerClaims' queue hook until the test
// ends.
func setOwnerClaimQueued(t *testing.T, fn func(namespace string, queued int)) {
	t.Helper()
	ownerClaims.mu.Lock()
	ownerClaims.queued = fn
	ownerClaims.mu.Unlock()
	t.Cleanup(func() {
		ownerClaims.mu.Lock()
		ownerClaims.queued = nil
		ownerClaims.mu.Unlock()
	})
}

// TestClaimOwner_ConcurrentClaimsOnOneNamespace: Engines of different
// databases opening one namespace at once (Manager's creation claim is per
// name, each Engine has its own locks) must not all read an absent owner
// marker and all write it: exactly one claim wins.
func TestClaimOwner_ConcurrentClaimsOnOneNamespace(t *testing.T) {
	const n = 8
	inner, err := objfactory.NewStorage("memory", map[string]string{}, objcommon.Credentials{})
	require.NoError(t, err)
	store := &ownerGateStore{Storage: inner, n: n, release: make(chan struct{})}
	ns := namespaceOf("memory", map[string]string{"bucket": t.Name()})
	setOwnerClaimQueued(t, func(namespace string, queued int) {
		if namespace == ns && queued == n {
			store.open()
		}
	})

	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		e := &Engine{store: store, journal: store, provider: "memory", namespace: ns}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = e.claimOwner(fmt.Sprintf("db-%d", i))
		}()
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		if err == nil {
			require.Equal(t, -1, winner, "db-%d and db-%d both claimed the namespace", winner, i)
			winner = i
			continue
		}
		var owned *NamespaceOwnedError
		require.ErrorAs(t, err, &owned)
		assert.Equal(t, fmt.Sprintf("db-%d", i), owned.Want)
	}
	require.NotEqual(t, -1, winner, "no claim won")
	rc, err := inner.Get(ownerMarkerName)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("db-%d", winner), string(got))
}

func TestNamespaceOf_IdentifiesTheNamespace(t *testing.T) {
	a := namespaceOf("s3", map[string]string{"bucket": "b", "prefix": "p"})
	assert.Equal(t, a, namespaceOf("s3", map[string]string{"prefix": "p", "bucket": "b"}), "setting order is not identity")
	assert.NotEqual(t, a, namespaceOf("gcs", map[string]string{"bucket": "b", "prefix": "p"}))
	assert.NotEqual(t, a, namespaceOf("s3", map[string]string{"bucket": "b", "prefix": "q"}))
	assert.NotEqual(t, namespaceOf("s3", map[string]string{"a": "b=c"}), namespaceOf("s3", map[string]string{"a=b": "c"}))
	assert.Equal(t, namespaceOf("local", map[string]string{"path": "/x/y"}), namespaceOf("local", map[string]string{"path": "/x/y/"}))
}
