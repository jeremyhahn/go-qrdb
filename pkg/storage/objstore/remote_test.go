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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	"github.com/cockroachdb/pebble/v2/vfs"
	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/require"
)

func newTestRemote(t *testing.T, store objcommon.Storage, prefix string) *Remote {
	t.Helper()
	r, err := NewRemote(store, prefix)
	require.NoError(t, err)
	return r
}

func putObject(t *testing.T, r *Remote, name string, data []byte) {
	t.Helper()
	w, err := r.CreateObject(name)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
}

func TestNewRemote_Refusals(t *testing.T) {
	store := memory.New()
	tests := []struct {
		name   string
		store  objcommon.Storage
		prefix string
		reason string
	}{
		{"nil store", nil, "db1", "nil store"},
		{"empty prefix", store, "", "empty prefix"},
		{"leading slash", store, "/db1", "leading /"},
		{"NUL", store, "db\x001", "NUL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewRemote(tc.store, tc.prefix)
			require.Nil(t, r)
			var ire *InvalidRemoteError
			require.ErrorAs(t, err, &ire)
			require.Equal(t, tc.prefix, ire.Prefix)
			require.Contains(t, ire.Error(), tc.reason)
		})
	}
	r, err := NewRemote(store, "tenant/db1")
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestRemote_CreateReadRoundTrip(t *testing.T) {
	store := memory.New()
	r := newTestRemote(t, store, "db1")
	data := []byte("0123456789abcdef")
	putObject(t, r, "000001.sst", data)

	// The object is stored as <prefix>/<objName>.
	ok, err := store.Exists(context.Background(), "db1/000001.sst")
	require.NoError(t, err)
	require.True(t, ok)

	rd, size, err := r.ReadObject(context.Background(), "000001.sst")
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), size)

	tests := []struct {
		name string
		off  int64
		n    int
		want string
		err  error
	}{
		{"offset 0", 0, 4, "0123", nil},
		{"mid", 6, 5, "6789a", nil},
		{"up to end", 12, 4, "cdef", nil},
		{"empty at end", 16, 0, "", nil},
		{"past end", 14, 4, "", io.EOF},
		{"wholly past end", 20, 1, "", io.EOF},
		{"negative offset", -1, 1, "", errNegativeOffset},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := make([]byte, tc.n)
			err := rd.ReadAt(context.Background(), p, tc.off)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, string(p))
		})
	}

	require.NoError(t, rd.Close())
	require.ErrorIs(t, rd.ReadAt(context.Background(), make([]byte, 1), 0), errReaderClosed)
	require.NoError(t, rd.Close())

	got, err := r.Size("000001.sst")
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), got)
}

func TestRemote_EmptyObject(t *testing.T) {
	r := newTestRemote(t, memory.New(), "db1")
	putObject(t, r, "empty", nil)
	rd, size, err := r.ReadObject(context.Background(), "empty")
	require.NoError(t, err)
	require.Zero(t, size)
	require.NoError(t, rd.ReadAt(context.Background(), nil, 0))
	require.ErrorIs(t, rd.ReadAt(context.Background(), make([]byte, 1), 0), io.EOF)
	require.NoError(t, rd.Close())
}

func TestRemote_CreateObjectOverwrites(t *testing.T) {
	r := newTestRemote(t, memory.New(), "db1")
	putObject(t, r, "obj", []byte("first version"))
	putObject(t, r, "obj", []byte("second"))
	rd, size, err := r.ReadObject(context.Background(), "obj")
	require.NoError(t, err)
	require.Equal(t, int64(len("second")), size)
	p := make([]byte, size)
	require.NoError(t, rd.ReadAt(context.Background(), p, 0))
	require.Equal(t, "second", string(p))
	require.NoError(t, rd.Close())
}

func TestRemote_NotExist(t *testing.T) {
	r := newTestRemote(t, memory.New(), "db1")
	putObject(t, r, "gone", []byte("x"))
	require.NoError(t, r.Delete("gone"))

	_, _, err := r.ReadObject(context.Background(), "gone")
	require.Error(t, err)
	require.True(t, r.IsNotExistError(err), "ReadObject: %v", err)

	_, err = r.Size("gone")
	require.Error(t, err)
	require.True(t, r.IsNotExistError(err), "Size: %v", err)

	err = r.Delete("gone")
	require.Error(t, err)
	require.True(t, r.IsNotExistError(err), "Delete: %v", err)

	require.False(t, r.IsNotExistError(nil))
	require.False(t, r.IsNotExistError(errors.New("other")))
}

func TestRemote_List(t *testing.T) {
	store := memory.New()
	r := newTestRemote(t, store, "db1")
	for _, n := range []string{"a", "b/4", "b/5", "b/6", "c/x/1", "c/y"} {
		putObject(t, r, n, []byte(n))
	}
	// An object outside the prefix that shares its leading characters.
	require.NoError(t, store.PutWithContext(context.Background(), "db10/a", bytes.NewReader([]byte("other"))))

	tests := []struct {
		prefix, delim string
		want          []string
	}{
		{"", "", []string{"a", "b/4", "b/5", "b/6", "c/x/1", "c/y"}},
		{"", "/", []string{"a", "b/", "c/"}},
		{"b", "", []string{"b/4", "b/5", "b/6"}},
		{"b/", "/", []string{"b/4", "b/5", "b/6"}},
		{"c/", "/", []string{"c/x/", "c/y"}},
		{"z", "", []string{}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%q/%q", tc.prefix, tc.delim), func(t *testing.T) {
			got, err := r.List(tc.prefix, tc.delim)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestRemote_PrefixIsolation proves OS-3: two Remotes on one store under
// different prefixes see only their own objects.
func TestRemote_PrefixIsolation(t *testing.T) {
	store := memory.New()
	r1 := newTestRemote(t, store, "db1")
	r2 := newTestRemote(t, store, "db2")
	putObject(t, r1, "000001.sst", []byte("one"))
	putObject(t, r2, "000002.sst", []byte("two"))

	got1, err := r1.List("", "")
	require.NoError(t, err)
	require.Equal(t, []string{"000001.sst"}, got1)
	got2, err := r2.List("", "")
	require.NoError(t, err)
	require.Equal(t, []string{"000002.sst"}, got2)

	_, _, err = r1.ReadObject(context.Background(), "000002.sst")
	require.True(t, r1.IsNotExistError(err), "%v", err)
	require.True(t, r2.IsNotExistError(r2.Delete("000001.sst")))
}

func TestRemote_CloseSemantics(t *testing.T) {
	store := memory.New()
	r := newTestRemote(t, store, "db1")

	w, err := r.CreateObject("obj")
	require.NoError(t, err)
	_, err = w.Write([]byte("abc"))
	require.NoError(t, err)

	// Nothing is stored before Close.
	ok, err := store.Exists(context.Background(), "db1/obj")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
	_, err = w.Write([]byte("d"))
	require.ErrorIs(t, err, errWriterClosed)

	got, err := r.Size("obj")
	require.NoError(t, err)
	require.Equal(t, int64(3), got)

	// Closing the Remote leaves the shared store usable.
	require.NoError(t, r.Close())
	ok, err = store.Exists(context.Background(), "db1/obj")
	require.NoError(t, err)
	require.True(t, ok)
	r2 := newTestRemote(t, store, "db1")
	got, err = r2.Size("obj")
	require.NoError(t, err)
	require.Equal(t, int64(3), got)
}

// pebbleOnRemote opens a Pebble DB on fs whose SSTables are all created on
// the shared storage rs.
func pebbleOnRemote(t *testing.T, fs vfs.FS, rs remote.Storage) *pebble.DB {
	t.Helper()
	opts := &pebble.Options{
		FS:                 fs,
		FormatMajorVersion: pebble.FormatMinForSharedObjects,
	}
	opts.Experimental.RemoteStorage = remote.MakeSimpleFactory(map[remote.Locator]remote.Storage{"bucket": rs})
	opts.Experimental.CreateOnShared = remote.CreateOnSharedAll
	opts.Experimental.CreateOnSharedLocator = "bucket"
	db, err := pebble.Open("db", opts)
	require.NoError(t, err)
	return db
}

// pebbleRoundTrip writes keys through Pebble on rs, flushes, reopens on the
// same vfs and storage and reads every key back. afterFlush runs between the
// flush and the close.
func pebbleRoundTrip(t *testing.T, rs remote.Storage, afterFlush func(keys []string)) {
	t.Helper()
	fs := vfs.NewMem()
	db := pebbleOnRemote(t, fs, rs)
	require.NoError(t, db.SetCreatorID(1))
	keys := make([]string, 0, 50)
	for i := range 50 {
		k := fmt.Sprintf("remote-key-%03d", i)
		keys = append(keys, k)
		require.NoError(t, db.Set([]byte(k), []byte("value-"+k), pebble.Sync))
	}
	require.NoError(t, db.Flush())
	afterFlush(keys)
	require.NoError(t, db.Close())

	db = pebbleOnRemote(t, fs, rs)
	defer func() { require.NoError(t, db.Close()) }()
	for _, k := range keys {
		v, closer, err := db.Get([]byte(k))
		require.NoError(t, err, k)
		require.Equal(t, "value-"+k, string(v))
		require.NoError(t, closer.Close())
	}
}

// TestPebbleRoundTrip_InMemHarness proves the harness on Pebble's own
// in-memory remote storage.
func TestPebbleRoundTrip_InMemHarness(t *testing.T) {
	rs := remote.NewInMem()
	pebbleRoundTrip(t, rs, func([]string) {
		names, err := rs.List("", "")
		require.NoError(t, err)
		require.NotEmpty(t, names)
	})
}

// TestPebbleRoundTrip_Remote runs Pebble with every SSTable on the adapter
// over the go-objstore memory provider.
func TestPebbleRoundTrip_Remote(t *testing.T) {
	store := memory.New()
	r := newTestRemote(t, store, "tenant/db1")
	pebbleRoundTrip(t, r, func(keys []string) {
		names, err := store.ListWithContext(context.Background(), "tenant/db1/")
		require.NoError(t, err)
		ssts := 0
		for _, n := range names {
			if strings.HasSuffix(n, ".sst") {
				ssts++
			}
			for _, k := range keys {
				require.NotContains(t, n, k)
			}
		}
		require.Positive(t, ssts, "objects: %v", names)
		all, err := store.ListWithContext(context.Background(), "")
		require.NoError(t, err)
		require.Equal(t, len(names), len(all), "every object is under the prefix")
	})
}
