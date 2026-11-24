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
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
)

// errPlainMissing is the not-found error plainMissingStore returns in place of the sentinel.
var errPlainMissing = errors.New("provider: the specified object does not exist")

// plainMissingStore reports a missing object on Get, GetWithContext and Delete with a plain error, as cloud providers do.
type plainMissingStore struct {
	objcommon.Storage
}

func (s plainMissingStore) Get(key string) (io.ReadCloser, error) {
	rc, err := s.Storage.Get(key)
	if errors.Is(err, objcommon.ErrKeyNotFound) {
		return nil, errPlainMissing
	}
	return rc, err
}

func (s plainMissingStore) GetWithContext(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := s.Storage.GetWithContext(ctx, key)
	if errors.Is(err, objcommon.ErrKeyNotFound) {
		return nil, errPlainMissing
	}
	return rc, err
}

func (s plainMissingStore) Delete(key string) error {
	ok, err := s.Storage.Exists(context.Background(), key)
	if err == nil && !ok {
		return errPlainMissing
	}
	return s.Storage.Delete(key)
}

const plainMissingProvider = "missing-test-plain-error"

var registerPlainMissing sync.Once

// openPlainMissing opens an engine over a fresh plainMissingStore through the provider registry.
func openPlainMissing(t *testing.T) *Engine {
	t.Helper()
	registerPlainMissing.Do(func() {
		require.NoError(t, RegisterProvider(plainMissingProvider, ProviderCredentials{},
			func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
				store := objmemory.New()
				if err := store.Configure(settings, creds); err != nil {
					return nil, err
				}
				return plainMissingStore{Storage: store}, nil
			}))
	})
	e, err := New(plainMissingProvider, map[string]string{})
	require.NoError(t, err, "an empty store without the not-found sentinel opens")
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestEngine_MissingWithoutSentinel(t *testing.T) {
	e := openPlainMissing(t)

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

func TestMissingIn(t *testing.T) {
	cases := []struct {
		label       string
		name        string
		err         error
		existsFails bool
		want        bool
	}{
		{label: "nil error", name: "absent", err: nil, want: false},
		{label: "sentinel", name: "absent", err: objcommon.ErrKeyNotFound, want: true},
		{label: "wrapped sentinel", name: "absent", err: fmt.Errorf("x: %w", objcommon.ErrKeyNotFound), want: true},
		{label: "plain error on an absent object", name: "absent", err: errProvider, want: true},
		{label: "plain error on a present object", name: "present", err: errProvider, want: false},
		{label: "exists fault never reads as missing", name: "absent", err: errProvider, existsFails: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, fs := newFaultEngine(t)
			require.NoError(t, fs.Storage.Put("present", strings.NewReader("v")))
			fs.existsFails = tc.existsFails
			require.Equal(t, tc.want, missingIn(context.Background(), fs, tc.name, tc.err))
		})
	}
}

func TestMissingIn_CallSites(t *testing.T) {
	plainEngine := func() (*Engine, plainMissingStore) {
		pm := plainMissingStore{Storage: objmemory.New()}
		return &Engine{store: pm, journal: pm, provider: "memory"}, pm
	}

	t.Run("clearJournal tolerates a missing journal", func(t *testing.T) {
		e, _ := plainEngine()
		require.NoError(t, e.clearJournal())
	})
	t.Run("clearJournal delete fault on a missing journal", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.deleteFails = true
		require.NoError(t, e.clearJournal())
	})
	t.Run("clearJournal delete fault with exists fault", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.deleteFails = true
		fs.existsFails = true
		err := e.clearJournal()
		var jwe *JournalWriteError
		require.ErrorAs(t, err, &jwe)
		require.ErrorIs(t, err, errProvider)
	})
	t.Run("clearJournal delete fault on a present journal", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, fs.Storage.Put(journalName, strings.NewReader("x")))
		fs.deleteFails = true
		err := e.clearJournal()
		var jwe *JournalWriteError
		require.ErrorAs(t, err, &jwe)
		require.ErrorIs(t, err, errProvider)
	})
	t.Run("Delete tolerates a missing object", func(t *testing.T) {
		e, _ := plainEngine()
		require.NoError(t, e.Delete([]byte("absent")))
	})
	t.Run("Delete of a present object removes it", func(t *testing.T) {
		e, _ := plainEngine()
		require.NoError(t, e.Set([]byte("present"), []byte("v")))
		require.NoError(t, e.Delete([]byte("present")))
		ok, err := e.Exists([]byte("present"))
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("recoverJournal with no journal", func(t *testing.T) {
		e, _ := plainEngine()
		require.NoError(t, e.recoverJournal())
	})
	t.Run("refuseInTreeJournal with no journal", func(t *testing.T) {
		_, pm := plainEngine()
		require.NoError(t, refuseInTreeJournal("memory", pm))
	})
	t.Run("EnsureLSMFormat marks an empty store", func(t *testing.T) {
		_, pm := plainEngine()
		require.NoError(t, EnsureLSMFormat(context.Background(), pm))
		rc, err := pm.Get(formatMarkerName)
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()
		found, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.Equal(t, lsmFormatVersion, string(found))
	})
	t.Run("hashed key absent", func(t *testing.T) {
		e := openPlainMissing(t)
		key := patterned(directThreshold()+1, 21)
		_, hashed := objectName(key)
		require.True(t, hashed)
		got, err := e.Get(key)
		require.NoError(t, err)
		require.Nil(t, got)
	})
	t.Run("scan skips a vanished hashed record", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, e.Set([]byte("kept"), []byte("v")))
		ghost, _ := objectName(patterned(directThreshold()+1, 22))
		e.store = &ghostListingStore{faultStore: fs, ghost: ghost}

		entries, err := e.scan(nil, true)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, []byte("kept"), entries[0].key)
	})
}

// ghostListingStore lists one never-stored object and reports it missing on Get with a plain error.
type ghostListingStore struct {
	*faultStore
	ghost string
}

func (g *ghostListingStore) List(prefix string) ([]string, error) {
	names, err := g.faultStore.List(prefix)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(g.ghost, prefix) {
		names = append(names, g.ghost)
	}
	return names, nil
}

func (g *ghostListingStore) Get(name string) (io.ReadCloser, error) {
	if name == g.ghost {
		return nil, errPlainMissing
	}
	return g.faultStore.Get(name)
}
