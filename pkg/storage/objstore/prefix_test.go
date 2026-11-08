// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
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
	"io"
	"sync"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedProvider is a provider whose every open returns one memory store, as
// every open of an s3 provider with one bucket reaches one bucket.
const sharedProvider = "prefix-test-shared"

var (
	sharedOnce  sync.Once
	sharedStore objcommon.Storage
)

func registerSharedProvider(t *testing.T) objcommon.Storage {
	t.Helper()
	sharedOnce.Do(func() {
		store, err := objfactory.NewStorage("memory", map[string]string{}, objcommon.Credentials{})
		require.NoError(t, err)
		sharedStore = store
		require.NoError(t, RegisterProvider(sharedProvider, ProviderCredentials{}, func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
			return sharedStore, nil
		}))
	})
	return sharedStore
}

func TestWithObjectPrefix_EnginesOnOneProviderAreIsolated(t *testing.T) {
	raw := registerSharedProvider(t)
	a, err := New(sharedProvider, map[string]string{}, WithObjectPrefix("databases/a/"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	b, err := New(sharedProvider, map[string]string{}, WithObjectPrefix("databases/b/"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	require.NoError(t, a.Set([]byte("k"), []byte("from-a")))
	require.NoError(t, b.Set([]byte("k"), []byte("from-b")))
	bt := b.Batch()
	require.NoError(t, bt.Set([]byte("only-b"), []byte("v")))
	require.NoError(t, bt.Commit())

	got, err := a.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, "from-a", string(got))
	got, err = b.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, "from-b", string(got))

	got, err = a.Get([]byte("only-b"))
	require.NoError(t, err)
	assert.Nil(t, got, "a sees b's key")
	keys, err := a.List(nil)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("k")}, keys)

	// Every object, the format marker included, is under its engine's prefix.
	names, err := raw.List("")
	require.NoError(t, err)
	require.NotEmpty(t, names)
	for _, name := range names {
		assert.Regexp(t, `^databases/[ab]/`, name)
	}

	require.NoError(t, a.Delete([]byte("k")))
	got, err = b.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, "from-b", string(got))
}

func TestWithObjectPrefix_RefusesAnInvalidPrefix(t *testing.T) {
	for _, prefix := range []string{"/abs/", "no-slash", "/"} {
		_, err := New("memory", map[string]string{}, WithObjectPrefix(prefix))
		var prefixErr *ObjectPrefixError
		require.ErrorAs(t, err, &prefixErr, prefix)
		assert.Equal(t, prefix, prefixErr.Prefix)
		assert.Contains(t, prefixErr.Error(), prefix)
	}
}

// The wrapper namespaces every name-taking method of the provider interface,
// not only the ones the engine calls today.
func TestPrefixedStore_EveryNamedMethodIsScoped(t *testing.T) {
	ctx := context.Background()
	raw, err := objfactory.NewStorage("memory", map[string]string{}, objcommon.Credentials{})
	require.NoError(t, err)
	p := withPrefix(raw, "ns/")

	require.NoError(t, p.PutWithContext(ctx, "a", bytes.NewReader([]byte("1"))))
	require.NoError(t, p.PutWithMetadata(ctx, "b", bytes.NewReader([]byte("2")), &objcommon.Metadata{}))
	require.NoError(t, raw.Put("outside", bytes.NewReader([]byte("x"))))

	rc, err := p.GetWithContext(ctx, "a")
	require.NoError(t, err)
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "1", string(body))
	ok, err := raw.Exists(ctx, "ns/b")
	require.NoError(t, err)
	assert.True(t, ok)

	_, err = p.GetMetadata(ctx, "a")
	require.NoError(t, err)
	require.NoError(t, p.UpdateMetadata(ctx, "a", &objcommon.Metadata{}))

	names, err := p.ListWithContext(ctx, "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "b"}, names)
	res, err := p.ListWithOptions(ctx, nil)
	require.NoError(t, err)
	var keys []string
	for _, o := range res.Objects {
		keys = append(keys, o.Key)
	}
	assert.ElementsMatch(t, []string{"a", "b"}, keys)
	res, err = p.ListWithOptions(ctx, &objcommon.ListOptions{Prefix: "a"})
	require.NoError(t, err)
	require.Len(t, res.Objects, 1)
	assert.Equal(t, "a", res.Objects[0].Key)

	var dest archiveSink
	require.NoError(t, p.Archive("a", &dest))
	assert.Equal(t, "ns/a", dest.key)

	require.NoError(t, p.DeleteWithContext(ctx, "a"))
	ok, err = p.Exists(ctx, "a")
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = raw.Exists(ctx, "outside")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestPrefixedStore_ListErrorsPassThrough(t *testing.T) {
	boom := errors.New("list failed")
	p := withPrefix(&failingList{err: boom}, "ns/")
	_, err := p.List("")
	assert.ErrorIs(t, err, boom)
	_, err = p.ListWithContext(context.Background(), "")
	assert.ErrorIs(t, err, boom)
	_, err = p.ListWithOptions(context.Background(), nil)
	assert.ErrorIs(t, err, boom)
}

type archiveSink struct{ key string }

func (a *archiveSink) Put(key string, _ io.Reader) error {
	a.key = key
	return nil
}

type failingList struct {
	objcommon.Storage
	err error
}

func (f *failingList) List(string) ([]string, error) { return nil, f.err }

func (f *failingList) ListWithContext(context.Context, string) ([]string, error) { return nil, f.err }

func (f *failingList) ListWithOptions(context.Context, *objcommon.ListOptions) (*objcommon.ListResult, error) {
	return nil, f.err
}
