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
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
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
	tooLong := prefixOfLen(maxObjectPrefixLen + 1)
	for _, prefix := range []string{"/abs/", "no-slash", "/", "ns//", "./", "ns/./", "a/../", `ns\x/`, tooLong} {
		_, err := New("memory", map[string]string{}, WithObjectPrefix(prefix))
		var prefixErr *ObjectPrefixError
		require.ErrorAs(t, err, &prefixErr, prefix)
		assert.Equal(t, prefix, prefixErr.Prefix)
		assert.Contains(t, prefixErr.Error(), prefix)
	}
}

// prefixOfLen returns a valid-shaped prefix of exactly n bytes, n >= 2, made
// of short segments so a local provider can create it as directories.
func prefixOfLen(n int) string {
	p := strings.Repeat("a/", n/2)
	if n%2 == 1 {
		p = "b" + p
	}
	return p
}

func TestValidObjectPrefix(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		ok     bool
	}{
		{"", true},
		{"ns/", true},
		{"databases/a/", true},
		{"a.b/..c/", true},
		{prefixOfLen(maxObjectPrefixLen), true},
		{"/", false},
		{"/abs/", false},
		{"no-slash", false},
		{"ns//", false},
		{"//", false},
		{"./", false},
		{"../", false},
		{"ns/./", false},
		{"ns/../", false},
		{"a/../", false},
		{`ns\x/`, false},
		{`ns\`, false},
		{prefixOfLen(maxObjectPrefixLen + 1), false},
	} {
		err := validObjectPrefix(tc.prefix)
		if tc.ok {
			assert.NoError(t, err, tc.prefix)
			continue
		}
		var prefixErr *ObjectPrefixError
		assert.ErrorAs(t, err, &prefixErr, tc.prefix)
	}
	// The longest prefix leaves room for every name the engine writes.
	assert.LessOrEqual(t, maxObjectPrefixLen+hashedNameLen, objcommon.MaxKeyLength)
	assert.LessOrEqual(t, maxObjectPrefixLen+len(journalName), objcommon.MaxKeyLength)
	assert.LessOrEqual(t, maxObjectPrefixLen+len(formatMarkerName), objcommon.MaxKeyLength)
}

// TestWithObjectPrefix_BoundaryKeyFitsTheProvider writes, under a prefix, a
// key whose unprefixed direct name is the longest the provider takes. Its
// name is chosen within the limit the prefix leaves, so the batch, the
// reads, the listing and the reopen all succeed on a provider that enforces
// the limit, and no object name on disk exceeds it.
func TestWithObjectPrefix_BoundaryKeyFitsTheProvider(t *testing.T) {
	key := patterned(directThreshold(), 7)
	require.Greater(t, directNameLen(len(key)), objcommon.MaxKeyLength-len("ns/"),
		"the key's direct name must not fit beneath the prefix")
	for _, prefix := range []string{"ns/", prefixOfLen(maxObjectPrefixLen)} {
		t.Run(strconv.Itoa(len(prefix)), func(t *testing.T) {
			dir := t.TempDir()
			settings := map[string]string{"path": dir}
			e, err := New("local", settings, WithObjectPrefix(prefix))
			require.NoError(t, err)
			b := e.Batch()
			require.NoError(t, b.Set(key, []byte("v")))
			require.NoError(t, b.Set([]byte("short"), []byte("s")))
			require.NoError(t, b.Commit())

			check := func(e *Engine) {
				got, err := e.Get(key)
				require.NoError(t, err)
				assert.Equal(t, "v", string(got))
				keys, err := e.List(nil)
				require.NoError(t, err)
				assert.ElementsMatch(t, [][]byte{key, []byte("short")}, keys)
				keys, err = e.List(key[:len(key)-1])
				require.NoError(t, err)
				assert.Equal(t, [][]byte{key}, keys)
			}
			check(e)
			require.NoError(t, e.Close())

			e, err = New("local", settings, WithObjectPrefix(prefix))
			require.NoError(t, err)
			t.Cleanup(func() { _ = e.Close() })
			check(e)

			// Every object name the provider holds fits its limit; the
			// provider's own metadata sidecars are not object names.
			require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || strings.HasSuffix(path, ".metadata.json") {
					return err
				}
				rel, err := filepath.Rel(dir, path)
				if err != nil {
					return err
				}
				assert.LessOrEqual(t, len(filepath.ToSlash(rel)), objcommon.MaxKeyLength, rel)
				return nil
			}))
		})
	}
}

// TestWithObjectPrefix_SiblingPrefixesOnOneLocalRoot opens two engines on one
// local provider root, under "ns/" and "ns2/": each lists only its own keys.
func TestWithObjectPrefix_SiblingPrefixesOnOneLocalRoot(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	a, err := New("local", settings, WithObjectPrefix("ns/"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	b, err := New("local", settings, WithObjectPrefix("ns2/"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	require.NoError(t, a.Set([]byte("a-key"), []byte("a")))
	bt := b.Batch()
	require.NoError(t, bt.Set([]byte("b-key"), []byte("b")))
	require.NoError(t, bt.Commit())

	keys, err := a.List(nil)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("a-key")}, keys)
	keys, err = b.List(nil)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("b-key")}, keys)
	got, err := a.Get([]byte("b-key"))
	require.NoError(t, err)
	assert.Nil(t, got, "ns/ sees ns2/'s key")
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
