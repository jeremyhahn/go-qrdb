package objstore

import (
	"fmt"
	"sync"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/require"
)

// TestNamespaceKey_GCSIgnoresProject: the GCS backend places objects by
// bucket alone, so the project does not select the namespace.
func TestNamespaceKey_GCSIgnoresProject(t *testing.T) {
	a := namespaceKey("gcs", map[string]string{"bucket": "b", "project": "p1"})
	b := namespaceKey("gcs", map[string]string{"bucket": "b", "project": "p2"})
	require.Equal(t, a, b)
	require.NotEqual(t, a, namespaceKey("gcs", map[string]string{"bucket": "other", "project": "p1"}))
}

// TestNew_GCSOwnerClaimRaceAcrossProjects opens one unowned GCS bucket at
// once under two projects and two owners: exactly one open claims the
// bucket and the other gets the ownership refusal. The "gcs" provider is
// stood in by an in-memory store for the test's lifetime.
func TestNew_GCSOwnerClaimRaceAcrossProjects(t *testing.T) {
	prev, hadPrev := registeredProviders.Load("gcs")
	registeredProviders.Store("gcs", registeredProvider{
		creator: func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
			store := objmemory.New()
			if err := store.Configure(map[string]string{}, creds); err != nil {
				return nil, err
			}
			return store, nil
		},
	})
	t.Cleanup(func() {
		if hadPrev {
			registeredProviders.Store("gcs", prev)
		} else {
			registeredProviders.Delete("gcs")
		}
	})

	projects := []string{"project-a", "project-b"}
	const rounds = 8
	for r := 0; r < rounds; r++ {
		start := make(chan struct{})
		engines := make([]*Engine, len(projects))
		errs := make([]error, len(projects))
		var wg sync.WaitGroup
		for i := range projects {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				settings := map[string]string{"bucket": "shared-bucket", "project": projects[i]}
				engines[i], errs[i] = New("gcs", settings, WithOwner(fmt.Sprintf("db-%d", i)))
			}(i)
		}
		close(start)
		wg.Wait()

		winner := -1
		for i := range projects {
			if errs[i] == nil {
				require.Equal(t, -1, winner, "round %d: both projects claimed the bucket", r)
				winner = i
				continue
			}
			_, refused := ownerRefusal(errs[i])
			require.True(t, refused, "round %d, %s: got %v", r, projects[i], errs[i])
		}
		require.NotEqual(t, -1, winner, "round %d: no open claimed the bucket", r)
		require.NoError(t, engines[winner].Close())
	}
}

// TestNew_S3EmptyEndpointIsOmittedEndpoint: an S3 endpoint set to "" and
// one left out name the same bucket, so a second owner's claim through the
// other spelling is refused.
func TestNew_S3EmptyEndpointIsOmittedEndpoint(t *testing.T) {
	require.Equal(t,
		namespaceKey("s3", map[string]string{"bucket": "b", "region": "r"}),
		namespaceKey("s3", map[string]string{"bucket": "b", "region": "r", "endpoint": ""}))

	// One in-memory bucket stands for the one S3 bucket both spellings reach.
	shared := objmemory.New()
	require.NoError(t, shared.Configure(map[string]string{}, objcommon.Credentials{}))
	prev, hadPrev := registeredProviders.Load("s3")
	registeredProviders.Store("s3", registeredProvider{
		creator: func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
			return shared, nil
		},
	})
	t.Cleanup(func() {
		if hadPrev {
			registeredProviders.Store("s3", prev)
		} else {
			registeredProviders.Delete("s3")
		}
	})

	first, err := New("s3", map[string]string{"bucket": "b", "region": "r"}, WithOwner("db-a"))
	require.NoError(t, err)
	defer func() { require.NoError(t, first.Close()) }()

	second, err := New("s3", map[string]string{"bucket": "b", "region": "r", "endpoint": ""}, WithOwner("db-b"))
	if err == nil {
		require.NoError(t, second.Close())
	}
	_, refused := ownerRefusal(err)
	require.True(t, refused, "got %v", err)
}
