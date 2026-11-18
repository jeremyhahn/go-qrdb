package objstore

import (
	"sync"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/require"
)

// An S3 namespace is the bucket at its effective endpoint: every spelling
// of one bucket takes one key, and a different bucket or host does not.
func TestNamespaceKey_S3EffectiveIdentity(t *testing.T) {
	aws := namespaceKey("s3", map[string]string{"bucket": "b"})
	for name, settings := range map[string]map[string]string{
		"region":                {"bucket": "b", "region": "us-east-1"},
		"other region":          {"bucket": "b", "region": "eu-west-1"},
		"empty endpoint":        {"bucket": "b", "endpoint": ""},
		"global endpoint":       {"bucket": "b", "endpoint": "https://s3.amazonaws.com"},
		"no scheme":             {"bucket": "b", "endpoint": "s3.amazonaws.com"},
		"http":                  {"bucket": "b", "endpoint": "http://s3.amazonaws.com/"},
		"regional dot":          {"bucket": "b", "endpoint": "https://s3.eu-west-1.amazonaws.com", "region": "eu-west-1"},
		"regional dash":         {"bucket": "b", "endpoint": "https://s3-us-west-2.amazonaws.com"},
		"virtual host":          {"bucket": "b", "endpoint": "https://b.s3.amazonaws.com"},
		"regional virtual":      {"bucket": "b", "endpoint": "https://b.s3.us-east-2.amazonaws.com:443/"},
		"upper case":            {"bucket": "b", "endpoint": "HTTPS://S3.AMAZONAWS.COM"},
		"explicit default port": {"bucket": "b", "endpoint": "https://s3.amazonaws.com:443"},
	} {
		require.Equal(t, aws, namespaceKey("s3", settings), name)
	}

	minio := namespaceKey("s3", map[string]string{"bucket": "b", "endpoint": "https://minio.invalid"})
	for name, endpoint := range map[string]string{
		"trailing slash": "https://minio.invalid/",
		"default port":   "https://minio.invalid:443",
		"no scheme":      "minio.invalid",
		"upper case":     "HTTPS://MINIO.INVALID",
		"path":           "https://minio.invalid:443/some/path/",
	} {
		require.Equal(t, minio, namespaceKey("s3", map[string]string{"bucket": "b", "endpoint": endpoint, "region": "r"}), name)
	}
	require.Equal(t,
		namespaceKey("s3", map[string]string{"bucket": "b", "endpoint": "http://minio.invalid"}),
		namespaceKey("s3", map[string]string{"bucket": "b", "endpoint": "http://minio.invalid:80/"}))

	for name, settings := range map[string]map[string]string{
		"other bucket":      {"bucket": "c"},
		"other aws bucket":  {"bucket": "c", "endpoint": "https://s3.amazonaws.com"},
		"non-aws host":      {"bucket": "b", "endpoint": "https://minio.invalid"},
		"amazonaws, not s3": {"bucket": "b", "endpoint": "https://sqs.us-east-1.amazonaws.com"},
	} {
		require.NotEqual(t, aws, namespaceKey("s3", settings), name)
	}
	for name, endpoint := range map[string]string{
		"other host":   "https://other.invalid",
		"other port":   "https://minio.invalid:9000",
		"other scheme": "http://minio.invalid",
	} {
		require.NotEqual(t, minio, namespaceKey("s3", map[string]string{"bucket": "b", "endpoint": endpoint}), name)
	}
	require.NotEqual(t, minio, namespaceKey("s3", map[string]string{"bucket": "c", "endpoint": "https://minio.invalid"}))
}

// Two concurrent opens of one AWS bucket, spelled differently and by
// different owners: exactly one succeeds, the other gets the owner refusal.
func TestNew_S3EquivalentEndpointsConcurrentOwners(t *testing.T) {
	requireOneOwnerAcrossSpellings(t, "s3", []map[string]string{
		{"bucket": "b", "region": "us-east-1"},
		{"bucket": "b", "region": "eu-west-1", "endpoint": "https://s3.eu-west-1.amazonaws.com/"},
	})
}

// requireOneOwnerAcrossSpellings opens provider, backed by one shared
// in-memory store, concurrently under each settings spelling of one namespace,
// each as its own owner, and requires that exactly one opens and the rest are
// refused by the owner lock.
func requireOneOwnerAcrossSpellings(t *testing.T, provider string, spellings []map[string]string) {
	t.Helper()
	shared := objmemory.New()
	require.NoError(t, shared.Configure(map[string]string{}, objcommon.Credentials{}))
	prev, hadPrev := registeredProviders.Load(provider)
	registeredProviders.Store(provider, registeredProvider{
		creator: func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
			return shared, nil
		},
	})
	t.Cleanup(func() {
		if hadPrev {
			registeredProviders.Store(provider, prev)
		} else {
			registeredProviders.Delete(provider)
		}
	})

	engines := make([]*Engine, len(spellings))
	errs := make([]error, len(spellings))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, settings := range spellings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			engines[i], errs[i] = New(provider, settings, WithOwner("db-"+string(rune('a'+i))))
		}()
	}
	close(start)
	wg.Wait()

	opened, refused := 0, 0
	for i := range spellings {
		if errs[i] == nil {
			opened++
			require.NoError(t, engines[i].Close())
			continue
		}
		_, ok := ownerRefusal(errs[i])
		require.True(t, ok, "got %v", errs[i])
		refused++
	}
	require.Equal(t, 1, opened)
	require.Equal(t, len(spellings)-1, refused)
}
