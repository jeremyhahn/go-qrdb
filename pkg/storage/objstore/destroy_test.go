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
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sortedProvider is a provider that is not walked, so one store holds both
// records and journal, and that lists in name order, as s3 does.
const sortedProvider = "destroy-test-sorted"

var (
	sortedOnce     sync.Once
	sortedStore    *sortedFaultStore
	sortedPrefixes atomic.Int64
)

// sortedFaultStore lists its objects in name order and, once armed, deletes
// only the number of objects it was told to and fails every delete after.
type sortedFaultStore struct {
	objcommon.Storage

	mu          sync.Mutex
	deletesLeft int // -1: every delete succeeds
}

func (s *sortedFaultStore) List(prefix string) ([]string, error) {
	names, err := s.Storage.List(prefix)
	sort.Strings(names)
	return names, err
}

func (s *sortedFaultStore) Delete(name string) error {
	s.mu.Lock()
	if s.deletesLeft == 0 {
		s.mu.Unlock()
		return errProvider
	}
	if s.deletesLeft > 0 {
		s.deletesLeft--
	}
	s.mu.Unlock()
	return s.Storage.Delete(name)
}

func (s *sortedFaultStore) allowDeletes(n int) {
	s.mu.Lock()
	s.deletesLeft = n
	s.mu.Unlock()
}

// newSortedEngine opens an engine on the sorted provider beneath a prefix no
// other test uses, and returns the prefix.
func newSortedEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	sortedOnce.Do(func() {
		inner, err := objfactory.NewStorage("memory", map[string]string{}, objcommon.Credentials{})
		require.NoError(t, err)
		sortedStore = &sortedFaultStore{Storage: inner, deletesLeft: -1}
		require.NoError(t, RegisterProvider(sortedProvider, ProviderCredentials{}, func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
			return sortedStore, nil
		}))
	})
	sortedStore.allowDeletes(-1)
	prefix := fmt.Sprintf("databases/%d/", sortedPrefixes.Add(1))
	e, err := New(sortedProvider, map[string]string{}, WithObjectPrefix(prefix))
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e, prefix
}

func TestDestroy_InterruptedDestroyReopensAndRetries(t *testing.T) {
	// Three objects: the format marker, the journal and a record. Every
	// interruption point short of the last delete must leave a namespace that
	// reopens, and a retried Destroy must finish it.
	for allowed := 0; allowed < 3; allowed++ {
		t.Run(fmt.Sprintf("after %d deletes", allowed), func(t *testing.T) {
			e, prefix := newSortedEngine(t)
			assert.Same(t, e.store, e.journal, "a shared store must stay one store beneath the prefix")
			require.NoError(t, e.Set([]byte("k"), []byte("v")))
			require.NoError(t, e.writeJournal(encodeJournal(nil)))
			listed, err := sortedStore.List(prefix)
			require.NoError(t, err)
			require.Len(t, listed, 3)

			sortedStore.allowDeletes(allowed)
			require.ErrorIs(t, e.Destroy(), errProvider)
			sortedStore.allowDeletes(-1)

			reopened, err := New(sortedProvider, map[string]string{}, WithObjectPrefix(prefix))
			require.NoError(t, err, "an interrupted destroy must leave a namespace that opens")
			require.NoError(t, reopened.Destroy())
			listed, err = sortedStore.List(prefix)
			require.NoError(t, err)
			assert.Empty(t, listed, "the retried destroy must reclaim every object")
		})
	}
}

func TestDestroy_RefusesAWriteThatRacedIt(t *testing.T) {
	e, prefix := newSortedEngine(t)
	reached := make(chan struct{})
	release := make(chan struct{})
	// The hook holds the Set after its closed check and before writeMu.
	e.beforeEnterWrite = func() {
		close(reached)
		<-release
	}
	done := make(chan error, 1)
	go func() { done <- e.Set([]byte("late"), []byte("v")) }()
	<-reached
	require.NoError(t, e.Destroy())
	close(release)

	var closed *storage.ClosedError
	require.ErrorAs(t, <-done, &closed)
	listed, err := sortedStore.List(prefix)
	require.NoError(t, err)
	assert.Empty(t, listed, "a write that raced the destroy must leave no object")
}
