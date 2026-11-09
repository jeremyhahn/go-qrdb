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
	"io"
	"strings"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errProvider is the fault faultStore injects.
var errProvider = errors.New("provider fault")

// faultStore is a real in-memory go-objstore provider that fails the named
// operations: every other call, and every call on a name or prefix not
// listed, reaches the real provider unchanged.
type faultStore struct {
	objcommon.Storage
	getFails    map[string]bool // object name -> Get fails
	readFails   map[string]bool // object name -> reading the body fails
	listFails   map[string]bool // provider prefix -> List fails
	existsFails bool
	deleteFails bool
}

var _ objcommon.Storage = (*faultStore)(nil)

func (f *faultStore) Get(name string) (io.ReadCloser, error) {
	if f.getFails[name] {
		return nil, errProvider
	}
	if f.readFails[name] {
		return io.NopCloser(&failingReader{}), nil
	}
	return f.Storage.Get(name)
}

func (f *faultStore) List(prefix string) ([]string, error) {
	if f.listFails[prefix] {
		return nil, errProvider
	}
	return f.Storage.List(prefix)
}

func (f *faultStore) Exists(ctx context.Context, name string) (bool, error) {
	if f.existsFails {
		return false, errProvider
	}
	return f.Storage.Exists(ctx, name)
}

func (f *faultStore) Delete(name string) error {
	if f.deleteFails {
		return errProvider
	}
	return f.Storage.Delete(name)
}

// failingReader fails every read.
type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) { return 0, errProvider }

// newFaultEngine returns an engine over a fresh faultStore with no faults
// armed, already carrying this layout's format marker.
func newFaultEngine(t *testing.T) (*Engine, *faultStore) {
	t.Helper()
	inner, err := objfactory.NewStorage("memory", map[string]string{}, objcommon.Credentials{})
	require.NoError(t, err)
	fs := &faultStore{
		Storage:   inner,
		getFails:  map[string]bool{},
		readFails: map[string]bool{},
		listFails: map[string]bool{},
	}
	e := &Engine{store: fs, journal: fs, provider: "memory", nameLimit: objcommon.MaxKeyLength}
	require.NoError(t, e.ensureFormatMarker(objcommon.MaxKeyLength))
	return e, fs
}

func TestEnsureFormatMarker_ProviderFaults(t *testing.T) {
	t.Run("marker read fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.getFails[formatMarkerName] = true
		assert.ErrorIs(t, e.ensureFormatMarker(objcommon.MaxKeyLength), errProvider)
	})
	t.Run("marker body unreadable", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.readFails[formatMarkerName] = true
		assert.ErrorIs(t, e.ensureFormatMarker(objcommon.MaxKeyLength), errProvider)
	})
	t.Run("emptiness check fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, fs.Storage.Delete(formatMarkerName))
		fs.listFails[""] = true
		assert.ErrorIs(t, e.ensureFormatMarker(objcommon.MaxKeyLength), errProvider)
	})
}

func TestGet_ProviderFaults(t *testing.T) {
	short := []byte("k")
	long := patterned(directThreshold()+1, 10)
	for label, key := range map[string][]byte{"direct": short, "hashed": long} {
		t.Run(label, func(t *testing.T) {
			e, fs := newFaultEngine(t)
			require.NoError(t, e.Set(key, []byte("v")))
			name, _ := objectName(key, objcommon.MaxKeyLength)

			fs.getFails[name] = true
			_, err := e.Get(key)
			assert.ErrorIs(t, err, errProvider)

			fs.getFails[name] = false
			fs.readFails[name] = true
			_, err = e.Get(key)
			assert.ErrorIs(t, err, errProvider)
		})
	}
}

func TestScan_ProviderFaults(t *testing.T) {
	t.Run("direct listing fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.listFails[directPrefix] = true
		_, err := e.List(nil)
		assert.ErrorIs(t, err, errProvider)
	})
	t.Run("hashed listing fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.listFails[hashedPrefix] = true
		_, err := e.List(nil)
		assert.ErrorIs(t, err, errProvider)
	})
	t.Run("direct value read fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, e.Set([]byte("k"), []byte("v")))
		name, _ := objectName([]byte("k"), objcommon.MaxKeyLength)
		fs.getFails[name] = true
		_, err := e.scan(nil, true)
		assert.ErrorIs(t, err, errProvider)
	})
	t.Run("hashed record read fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		key := patterned(directThreshold()+1, 11)
		require.NoError(t, e.Set(key, []byte("v")))
		name, _ := objectName(key, objcommon.MaxKeyLength)
		fs.readFails[name] = true
		_, err := e.List(nil)
		assert.ErrorIs(t, err, errProvider)

		fs.readFails[name] = false
		fs.getFails[name] = true
		_, err = e.List(nil)
		assert.ErrorIs(t, err, errProvider)
	})
	t.Run("record deleted after the listing is skipped", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, e.Set([]byte("kept"), []byte("v")))
		// Names that list but no longer resolve: the race between a listing
		// and the read that follows it.
		gone := patterned(directThreshold()+1, 12)
		goneName, _ := objectName(gone, objcommon.MaxKeyLength)
		require.NoError(t, fs.Storage.Put(goneName, strings.NewReader("x")))
		goneDirect, _ := objectName([]byte("gone"), objcommon.MaxKeyLength)
		require.NoError(t, fs.Storage.Put(goneDirect, strings.NewReader("x")))
		e.store = &vanishingStore{faultStore: fs, vanished: map[string]bool{goneName: true, goneDirect: true}}

		entries, err := e.scan(nil, true)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, []byte("kept"), entries[0].key)
	})
}

// vanishingStore lists the named objects but reports them missing on read,
// as a provider does when another writer deletes them in between.
type vanishingStore struct {
	*faultStore
	vanished map[string]bool
}

func (v *vanishingStore) Get(name string) (io.ReadCloser, error) {
	if v.vanished[name] {
		return nil, objcommon.ErrKeyNotFound
	}
	return v.faultStore.Get(name)
}

func TestExistsAndDelete_ProviderFaults(t *testing.T) {
	e, fs := newFaultEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))

	fs.existsFails = true
	_, err := e.Exists([]byte("k"))
	assert.ErrorIs(t, err, errProvider)

	fs.deleteFails = true
	assert.ErrorIs(t, e.Delete([]byte("k")), errProvider)
}

func TestIsHashedName_RefusesUppercaseDigest(t *testing.T) {
	name, _ := objectName(patterned(directThreshold()+1, 13), objcommon.MaxKeyLength)
	upper := name[:10] + strings.ToUpper(name[10:])
	require.NotEqual(t, name, upper)
	assert.False(t, isHashedName(upper))
}
