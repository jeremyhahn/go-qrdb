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
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
)

const encRemoteTestPassphrase = "objstore-encrypted-remote-test-passphrase"

// newUnsealedBarrier returns an initialized, unsealed barrier over an
// in-memory backend, and the credentials that unseal it again.
func newUnsealedBarrier(t *testing.T) (*seal.StorageBarrier, seal.Credentials) {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(encRemoteTestPassphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	barrier, err := seal.NewStorageBarrier(log, seal.NewMemoryBackend(), seal.BarrierConfig{
		RootKeyPath:  "objstore-encremote-test-root-key",
		DeploymentID: 1,
	}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(ctx, []byte(encRemoteTestPassphrase))
	if err := barrier.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if barrier.IsSealed() {
		if err := barrier.Unseal(ctx, creds); err != nil {
			t.Fatalf("Unseal: %v", err)
		}
	}
	return barrier, creds
}

func newTestEncryptedRemote(t *testing.T, store objcommon.Storage, barrier ObjectBarrier) *EncryptedRemote {
	t.Helper()
	e, err := NewEncryptedRemote(newTestRemote(t, store, "tenant/db1"), barrier)
	require.NoError(t, err)
	return e
}

func putRemoteObject(t *testing.T, rs remote.Storage, name string, data []byte) {
	t.Helper()
	w, err := rs.CreateObject(name)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
}

// rawObject returns the bytes the store holds under key.
func rawObject(t *testing.T, store objcommon.Storage, key string) []byte {
	t.Helper()
	rc, err := store.GetWithContext(context.Background(), key)
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	return data
}

func TestNewEncryptedRemote_Refusals(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	inner := newTestRemote(t, memory.New(), "db1")
	tests := []struct {
		name    string
		inner   remote.Storage
		barrier ObjectBarrier
		reason  string
	}{
		{"nil inner", nil, barrier, "nil inner"},
		{"nil barrier", inner, nil, "nil barrier"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewEncryptedRemote(tc.inner, tc.barrier)
			require.Nil(t, e)
			var ier *InvalidEncryptedRemoteError
			require.ErrorAs(t, err, &ier)
			require.Contains(t, ier.Error(), tc.reason)
		})
	}
}

func TestEncryptedRemote_RoundTrip(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	store := memory.New()
	e := newTestEncryptedRemote(t, store, barrier)
	data := bytes.Repeat([]byte("0123456789abcdef"), 600) // spans three blocks
	putRemoteObject(t, e, "000001.sst", data)

	raw := rawObject(t, store, "tenant/db1/000001.sst")
	require.True(t, bytes.HasPrefix(raw, []byte("QENC")))
	require.False(t, bytes.Contains(raw, []byte("0123456789abcdef")))

	rd, size, err := e.ReadObject(context.Background(), "000001.sst")
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), size)
	n := int64(len(data))
	tests := []struct {
		name string
		off  int64
		n    int
		err  error
	}{
		{"offset 0", 0, 16, nil},
		{"mid", 4090, 20, nil},
		{"up to end", n - 7, 7, nil},
		{"past end", n - 3, 7, io.EOF},
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
			require.Equal(t, data[tc.off:tc.off+int64(tc.n)], p)
		})
	}
	require.NoError(t, rd.Close())

	got, err := e.Size("000001.sst")
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), got)
}

func TestEncryptedRemote_EmptyObject(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	e := newTestEncryptedRemote(t, memory.New(), barrier)
	putRemoteObject(t, e, "empty", nil)
	rd, size, err := e.ReadObject(context.Background(), "empty")
	require.NoError(t, err)
	require.Zero(t, size)
	require.NoError(t, rd.ReadAt(context.Background(), nil, 0))
	require.ErrorIs(t, rd.ReadAt(context.Background(), make([]byte, 1), 0), io.EOF)
	require.NoError(t, rd.Close())
	got, err := e.Size("empty")
	require.NoError(t, err)
	require.Zero(t, got)
}

func TestEncryptedRemote_WriterClose(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	store := memory.New()
	e := newTestEncryptedRemote(t, store, barrier)
	w, err := e.CreateObject("obj")
	require.NoError(t, err)
	_, err = w.Write([]byte("abc"))
	require.NoError(t, err)
	ok, err := store.Exists(context.Background(), "tenant/db1/obj")
	require.NoError(t, err)
	require.False(t, ok, "nothing is stored before Close")
	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
	_, err = w.Write([]byte("d"))
	require.ErrorIs(t, err, errWriterClosed)
}

// TestEncryptedRemote_FailsClosed proves a stored object not in the sealed
// form, or moved to another name, is refused rather than passed through.
func TestEncryptedRemote_FailsClosed(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	store := memory.New()
	e := newTestEncryptedRemote(t, store, barrier)
	plain := newTestRemote(t, store, "tenant/db1")
	putRemoteObject(t, plain, "plain.sst", []byte(strings.Repeat("plain sst bytes ", 8)))
	_, _, err := e.ReadObject(context.Background(), "plain.sst")
	require.ErrorIs(t, err, encfs.ErrInvalidHeader)
	_, err = e.Size("plain.sst")
	require.ErrorIs(t, err, encfs.ErrInvalidHeader)

	putRemoteObject(t, e, "a.sst", []byte("object a"))
	putRemoteObject(t, plain, "b.sst", rawObject(t, store, "tenant/db1/a.sst"))
	_, _, err = e.ReadObject(context.Background(), "b.sst")
	require.ErrorIs(t, err, encfs.ErrAuthFailed)
}

func TestEncryptedRemote_PassThrough(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	e := newTestEncryptedRemote(t, memory.New(), barrier)
	for _, n := range []string{"a", "b/1", "b/2"} {
		putRemoteObject(t, e, n, []byte(n))
	}
	got, err := e.List("", "/")
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b/"}, got)
	got, err = e.List("b/", "")
	require.NoError(t, err)
	require.Equal(t, []string{"b/1", "b/2"}, got)

	require.NoError(t, e.Delete("a"))
	_, _, err = e.ReadObject(context.Background(), "a")
	require.True(t, e.IsNotExistError(err), "ReadObject: %v", err)
	_, err = e.Size("a")
	require.True(t, e.IsNotExistError(err), "Size: %v", err)
	require.True(t, e.IsNotExistError(e.Delete("a")))
	require.False(t, e.IsNotExistError(nil))
	require.NoError(t, e.Close())
}

func TestEncryptedRemote_SealedBarrier(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	e := newTestEncryptedRemote(t, memory.New(), barrier)
	putRemoteObject(t, e, "obj", []byte("sealed later"))
	require.NoError(t, barrier.Seal())

	w, err := e.CreateObject("other")
	require.NoError(t, err)
	_, err = w.Write([]byte("x"))
	require.NoError(t, err)
	require.ErrorIs(t, w.Close(), seal.ErrSealed)

	_, _, err = e.ReadObject(context.Background(), "obj")
	require.ErrorIs(t, err, seal.ErrSealed)
}

// storeContains reports whether any object in store holds needle.
func storeContains(t *testing.T, store objcommon.Storage, needle string) bool {
	t.Helper()
	all, err := store.ListWithContext(context.Background(), "")
	require.NoError(t, err)
	for _, k := range all {
		if bytes.Contains(rawObject(t, store, k), []byte(needle)) {
			return true
		}
	}
	return false
}

// countSSTs returns how many .sst objects store holds.
func countSSTs(t *testing.T, store objcommon.Storage) int {
	t.Helper()
	all, err := store.ListWithContext(context.Background(), "")
	require.NoError(t, err)
	n := 0
	for _, k := range all {
		if strings.HasSuffix(k, ".sst") {
			n++
		}
	}
	return n
}

// TestPebbleRoundTrip_PlainRemoteLeaksKeys proves the byte check the
// encrypted round trip relies on: on a plain Remote, the SSTables hold the
// keys in the clear.
func TestPebbleRoundTrip_PlainRemoteLeaksKeys(t *testing.T) {
	store := memory.New()
	pebbleRoundTrip(t, newTestRemote(t, store, "tenant/db1"), func([]string) {
		require.Positive(t, countSSTs(t, store))
		require.True(t, storeContains(t, store, "remote-key-0"))
	})
}

// TestPebbleRoundTrip_EncryptedRemote runs Pebble with every SSTable sealed
// by EncryptedRemote over Remote over the go-objstore memory provider.
func TestPebbleRoundTrip_EncryptedRemote(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	store := memory.New()
	pebbleRoundTrip(t, newTestEncryptedRemote(t, store, barrier), func([]string) {
		require.Positive(t, countSSTs(t, store))
		require.False(t, storeContains(t, store, "remote-key-0"))
	})
}
