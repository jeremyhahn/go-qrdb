package objstore

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
)

// plainMissingStore reports a missing object on Get with a plain error, as cloud providers do.
type plainMissingStore struct {
	objcommon.Storage
}

func (s plainMissingStore) Get(key string) (io.ReadCloser, error) {
	rc, err := s.Storage.Get(key)
	if errors.Is(err, objcommon.ErrKeyNotFound) {
		return nil, errors.New("provider: the specified object does not exist")
	}
	return rc, err
}

func TestEngine_MissingWithoutSentinel(t *testing.T) {
	require.NoError(t, RegisterProvider("missing-test-plain-error", ProviderCredentials{},
		func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
			store := objmemory.New()
			if err := store.Configure(settings, creds); err != nil {
				return nil, err
			}
			return plainMissingStore{Storage: store}, nil
		}))

	e, err := New("missing-test-plain-error", map[string]string{})
	require.NoError(t, err, "an empty store without the not-found sentinel opens")
	defer func() { _ = e.Close() }()

	got, err := e.Get([]byte("absent"))
	require.NoError(t, err)
	require.Nil(t, got)

	require.NoError(t, e.Set([]byte("present"), []byte("value")))
	got, err = e.Get([]byte("present"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), got)

	_, err = e.store.Get("absent")
	require.False(t, notFound(err), "the wrapper must hide the sentinel")
}
