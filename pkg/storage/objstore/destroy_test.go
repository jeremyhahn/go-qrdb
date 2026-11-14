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
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
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

// newSharedMemory registers two providers over one memory store: root at the
// store's root and rooted at sub inside it, so one set of objects is reached
// through two spellings of the same namespace.
func newSharedMemory(t *testing.T, sub string) (root, rooted string, inner objcommon.Storage) {
	t.Helper()
	inner, err := objfactory.NewStorage("memory", map[string]string{}, objcommon.Credentials{})
	require.NoError(t, err)
	n := sortedPrefixes.Add(1)
	root = fmt.Sprintf("shared-root-%d", n)
	rooted = fmt.Sprintf("shared-rooted-%d", n)
	require.NoError(t, RegisterProvider(root, ProviderCredentials{}, func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
		return inner, nil
	}))
	require.NoError(t, RegisterProvider(rooted, ProviderCredentials{}, func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
		return withPrefix(inner, sub), nil
	}))
	return root, rooted, inner
}

// readAll returns every key and value e iterates, in order.
func readAll(t *testing.T, e *Engine) ([][]byte, []string) {
	t.Helper()
	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()
	var keys [][]byte
	var values []string
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		keys = append(keys, append([]byte(nil), it.Key()...))
		values = append(values, string(it.Value()))
	}
	require.NoError(t, it.Error())
	return keys, values
}

func TestNameLimit_ReopenUnderAnotherSpellingKeepsEveryKey(t *testing.T) {
	root, rooted, _ := newSharedMemory(t, "a/")
	written, err := New(root, map[string]string{}, WithObjectPrefix("a/"))
	require.NoError(t, err)

	// Every key length whose direct name fits the rooted spelling's limit but
	// not the prefixed one's, between a short key that sorts first and one
	// that sorts last.
	keys := [][]byte{{0x01}}
	for n := 1; directNameLen(n) <= objcommon.MaxKeyLength; n++ {
		if directNameLen(n) > objcommon.MaxKeyLength-len("a/") {
			key := make([]byte, n)
			for i := range key {
				key[i] = byte(0x10 + len(keys))
			}
			keys = append(keys, key)
		}
	}
	require.Greater(t, len(keys), 1, "some key length must sit between the two limits")
	keys = append(keys, []byte{0xff})
	var values []string
	for i, key := range keys {
		values = append(values, fmt.Sprintf("v%d", i))
		require.NoError(t, written.Set(key, []byte(values[i])))
	}
	require.NoError(t, written.Close())

	reopened, err := New(rooted, map[string]string{})
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()
	for i, key := range keys {
		got, err := reopened.Get(key)
		require.NoError(t, err, "key %d", i)
		assert.Equal(t, values[i], string(got))
	}
	gotKeys, gotValues := readAll(t, reopened)
	assert.Equal(t, keys, gotKeys, "every key must iterate back, in order")
	assert.Equal(t, values, gotValues)
}

func TestNameLimit_RefusesAPersistedLimitThatNoLongerFits(t *testing.T) {
	root, _, inner := newSharedMemory(t, "a/")
	require.NoError(t, inner.Put("a/"+formatMarkerName, strings.NewReader(
		fmt.Sprintf("3\nname-limit=%d\n", objcommon.MaxKeyLength))))
	_, err := New(root, map[string]string{}, WithObjectPrefix("a/"))
	var limitErr *NameLimitError
	require.ErrorAs(t, err, &limitErr)
	assert.Equal(t, objcommon.MaxKeyLength, limitErr.Persisted)
	assert.Equal(t, objcommon.MaxKeyLength-len("a/"), limitErr.Max)
}

func TestNameLimit_RefusesAVersion2Marker(t *testing.T) {
	root, _, inner := newSharedMemory(t, "a/")
	require.NoError(t, inner.Put(formatMarkerName, strings.NewReader("2")))
	_, err := New(root, map[string]string{})
	var mismatch *FormatVersionMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, "2", mismatch.Found)
	assert.Equal(t, "3", mismatch.Want)
}

func TestNameLimit_RefusesAMalformedMarker(t *testing.T) {
	for _, body := range []string{"3", "3\n", "3\nname-limit=\n", "3\nname-limit=0\n", "3\nname-limit=0100\n", "3\nname-limit=100", "3\nlimit=100\n"} {
		root, _, inner := newSharedMemory(t, "a/")
		require.NoError(t, inner.Put(formatMarkerName, strings.NewReader(body)))
		_, err := New(root, map[string]string{})
		var mismatch *FormatVersionMismatchError
		require.ErrorAs(t, err, &mismatch, "marker %q", body)
		assert.Equal(t, body, mismatch.Found)
	}
}

func TestDestroy_LeavesNestedNamespacesIntact(t *testing.T) {
	root, _, _ := newSharedMemory(t, "a/")
	open := func(prefix string) *Engine {
		e, err := New(root, map[string]string{}, WithObjectPrefix(prefix))
		require.NoError(t, err)
		t.Cleanup(func() { _ = e.Close() })
		return e
	}
	// A opens first: a nested namespace written before it would leave the
	// parent unmarked but not empty.
	a := open("ns/")
	b := open("ns/child/")
	c := open("ns/k/")
	long := make([]byte, objcommon.MaxKeyLength)
	for _, e := range []*Engine{a, b, c} {
		require.NoError(t, e.Set([]byte("short"), []byte("v")))
		require.NoError(t, e.Set(long, []byte("hashed")))
		require.NoError(t, e.writeJournal(encodeJournal(nil)))
	}
	require.NoError(t, a.Destroy())

	for _, e := range []*Engine{b, c} {
		got, err := e.Get([]byte("short"))
		require.NoError(t, err)
		assert.Equal(t, "v", string(got))
		got, err = e.Get(long)
		require.NoError(t, err)
		assert.Equal(t, "hashed", string(got))
		for _, name := range []string{formatMarkerName, journalName} {
			rc, err := e.store.Get(name)
			require.NoError(t, err, "a nested engine's %s must survive", name)
			_ = rc.Close()
		}
	}
	reopened := open("ns/")
	keys, _ := readAll(t, reopened)
	assert.Empty(t, keys, "the destroyed namespace must reopen empty")
	reopenedB := open("ns/child/")
	keys, _ = readAll(t, reopenedB)
	assert.Len(t, keys, 2)
}
