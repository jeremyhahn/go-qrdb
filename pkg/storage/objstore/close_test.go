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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closeRecordingStore is go-objstore's local provider, unchanged, that counts
// the calls to its Close. Close is forwarded, so the provider releases its
// os.Root and lifecycle goroutine exactly as it does in production.
type closeRecordingStore struct {
	objcommon.Storage
	closes atomic.Int32
}

func (s *closeRecordingStore) Close() {
	s.closes.Add(1)
	s.Storage.(interface{ Close() }).Close()
}

// closeRecorder records every provider a registered provider name creates.
type closeRecorder struct {
	name    string
	mu      sync.Mutex
	created []*closeRecordingStore
}

var closeRecorders atomic.Int32

// newCloseRecorder registers a provider that opens go-objstore's local
// provider and walks its root as "local" does (walkedRoots), so New opens
// a separate data and journal provider for it, as it does for "local".
func newCloseRecorder(t *testing.T) *closeRecorder {
	t.Helper()
	r := &closeRecorder{name: fmt.Sprintf("close-recording-%d", closeRecorders.Add(1))}
	require.NoError(t, RegisterProvider(r.name, ProviderCredentials{}, func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
		inner, err := objfactory.NewStorage("local", settings, creds)
		if err != nil {
			return nil, err
		}
		s := &closeRecordingStore{Storage: inner}
		r.mu.Lock()
		r.created = append(r.created, s)
		r.mu.Unlock()
		return s, nil
	}))
	walkedRoots[r.name] = walkedRoots["local"]
	t.Cleanup(func() { delete(walkedRoots, r.name) })
	return r
}

// closes returns the Close count of every provider created so far, in
// creation order.
func (r *closeRecorder) closes() []int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int32, len(r.created))
	for i, s := range r.created {
		out[i] = s.closes.Load()
	}
	return out
}

func TestProviders_CloseReleasesEachProviderOnce(t *testing.T) {
	r := newCloseRecorder(t)
	e, err := New(r.name, map[string]string{"path": t.TempDir()}, WithObjectPrefix("db/"))
	require.NoError(t, err)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	assert.Equal(t, []int32{0, 0}, r.closes(), "an open engine keeps its data and journal providers")

	require.NoError(t, e.Close())
	assert.Equal(t, []int32{1, 1}, r.closes())
}

func TestProviders_DestroyReleasesEachProviderOnce(t *testing.T) {
	r := newCloseRecorder(t)
	e, err := New(r.name, map[string]string{"path": t.TempDir()}, WithObjectPrefix("db/"))
	require.NoError(t, err)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))

	require.NoError(t, e.Destroy())
	assert.Equal(t, []int32{1, 1}, r.closes())
}

// TestProviders_DestroyAfterCloseReleasesAgain is the drop path: the manager
// closes the engine, then reclaims its objects through Destroy. The provider
// reopens its root to serve the destroy, and Destroy releases it again.
func TestProviders_DestroyAfterCloseReleasesAgain(t *testing.T) {
	r := newCloseRecorder(t)
	e, err := New(r.name, map[string]string{"path": t.TempDir()}, WithObjectPrefix("db/"))
	require.NoError(t, err)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))

	require.NoError(t, e.Close())
	require.NoError(t, e.Destroy())
	assert.Equal(t, []int32{2, 2}, r.closes(), "one release per Close and per Destroy")
}

func TestProviders_FailedOpenReleasesEachProviderOnce(t *testing.T) {
	r := newCloseRecorder(t)
	settings := map[string]string{"path": t.TempDir()}
	first, err := New(r.name, settings, WithObjectPrefix("db/"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	// The namespace is owned, so the second open fails after it has
	// created its providers.
	_, err = New(r.name, settings, WithObjectPrefix("db/"))
	var owned *NamespaceOwnedError
	require.ErrorAs(t, err, &owned)
	assert.Equal(t, []int32{0, 0, 1, 1}, r.closes(), "the failed open releases its own providers, and only those")
}

// TestProviders_ReplacedProviderIsReleased: a root given through a symlink is
// reopened at its physical path, and the provider opened on the symlink is
// released at once rather than left behind.
func TestProviders_ReplacedProviderIsReleased(t *testing.T) {
	r := newCloseRecorder(t)
	physical := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(physical, link))

	e, err := New(r.name, map[string]string{"path": link}, WithObjectPrefix("db/"))
	require.NoError(t, err)
	assert.Equal(t, []int32{1, 0, 0}, r.closes(), "the provider on the symlink is released by the open")

	require.NoError(t, e.Close())
	assert.Equal(t, []int32{1, 1, 1}, r.closes())
}
