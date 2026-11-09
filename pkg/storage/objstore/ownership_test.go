// Copyright (c) 2026 Jeremy Hahn
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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestLocal_ReopenUnderAnotherRootSplitIsRefused checks that a local
// namespace, whose commit journal sits in a sibling tree named after the root
// it was opened with, is not reopened through another root and prefix that
// reach the same records: that spelling would look for the journal elsewhere,
// miss an interrupted batch and serve it half applied. The spelling it was
// created with still finds and replays the batch.
func TestLocal_ReopenUnderAnotherRootSplitIsRefused(t *testing.T) {
	pending := []storage.BufferedOp{{Key: []byte("pending"), Value: []byte("v1")}}
	for _, tc := range []struct {
		name                     string
		createRoot, createPrefix string
		reopenRoot, reopenPrefix string
	}{
		{name: "prefix moved into the root", createRoot: "", createPrefix: "ns/", reopenRoot: "ns", reopenPrefix: ""},
		{name: "root moved into the prefix", createRoot: "ns", createPrefix: "", reopenRoot: "", reopenPrefix: "ns/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			open := func(root, prefix string) (*Engine, error) {
				return New("local", map[string]string{"path": filepath.Join(base, root)}, WithObjectPrefix(prefix))
			}
			e, err := open(tc.createRoot, tc.createPrefix)
			require.NoError(t, err)
			require.NoError(t, e.Set([]byte("before"), []byte("v0")))
			// An interrupted batch: its journal is published, its ops never applied.
			require.NoError(t, e.writeJournal(encodeJournal(pending)))
			require.NoError(t, e.Close())

			_, err = open(tc.reopenRoot, tc.reopenPrefix)
			var split *NamespaceSplitError
			require.ErrorAs(t, err, &split, "a reopen through another root/prefix split must not miss the journal")
			assert.Equal(t, "local", split.Provider)

			e, err = open(tc.createRoot, tc.createPrefix)
			require.NoError(t, err)
			defer func() { _ = e.Close() }()
			for key, want := range map[string]string{"before": "v0", "pending": "v1"} {
				v, err := e.Get([]byte(key))
				require.NoError(t, err)
				assert.Equal(t, want, string(v), "key %q", key)
			}
		})
	}
}

// TestLocal_SecondOpenOfANamespaceIsRefusedUntilClose checks that two
// independently opened engines never own one local namespace at once, so the
// exclusion Exclusive promises cannot be bypassed by a second opener: the
// second open is refused while the first is open, and succeeds once it closes.
// A namespace nested inside the first is a namespace of its own.
func TestLocal_SecondOpenOfANamespaceIsRefusedUntilClose(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	first, err := New("local", settings, WithObjectPrefix("ns/"))
	require.NoError(t, err)
	require.NoError(t, first.Set([]byte("k"), []byte("v")))

	_, err = New("local", settings, WithObjectPrefix("ns/"))
	var owned *NamespaceOwnedError
	require.ErrorAs(t, err, &owned, "a second engine must not open a namespace another engine owns")
	assert.Equal(t, "local", owned.Provider)

	child, err := New("local", settings, WithObjectPrefix("ns/child/"))
	require.NoError(t, err)
	require.NoError(t, child.Close())

	require.NoError(t, first.Close())
	second, err := New("local", settings, WithObjectPrefix("ns/"))
	require.NoError(t, err)
	defer func() { _ = second.Close() }()
	v, err := second.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, "v", string(v))
}

// TestLocal_DestroyAfterCloseRefusesWhileAnotherEngineOwnsTheNamespace checks
// that a Destroy retried on a closed engine takes the namespace back before
// deleting, and is refused while a second engine owns it, rather than
// deleting that engine's records under it.
func TestLocal_DestroyAfterCloseRefusesWhileAnotherEngineOwnsTheNamespace(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	first, err := New("local", settings)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := New("local", settings)
	require.NoError(t, err)
	require.NoError(t, second.Set([]byte("k"), []byte("v")))

	var owned *NamespaceOwnedError
	require.ErrorAs(t, first.Destroy(), &owned)
	v, err := second.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, "v", string(v), "a refused Destroy must leave the owner's records")

	require.NoError(t, second.Close())
	require.NoError(t, first.Destroy(), "with no other owner the retried Destroy reclaims")
	third, err := New("local", settings)
	require.NoError(t, err)
	defer func() { _ = third.Close() }()
	v, err = third.Get([]byte("k"))
	require.NoError(t, err)
	assert.Nil(t, v)
}

// TestExclusive_RefusedWhereTheProviderCannotExcludeOtherOpeners checks that
// an engine whose provider offers no way to exclude a second opener -- here a
// registered provider over a store another engine opens too -- does not claim
// storage.Exclusive's cross-process guarantee: Exclusive is refused with
// storage.ExclusiveUnsupportedError and fn never runs, so a guarded write
// falls back to the caller's own in-process exclusion.
func TestExclusive_RefusedWhereTheProviderCannotExcludeOtherOpeners(t *testing.T) {
	root, _, _ := newSharedMemory(t, "unused/")
	a, err := New(root, map[string]string{})
	require.NoError(t, err)
	defer func() { _ = a.Close() }()
	b, err := New(root, map[string]string{})
	require.NoError(t, err)
	defer func() { _ = b.Close() }()

	for _, e := range []*Engine{a, b} {
		called := false
		err := e.Exclusive(func(storage.ReadWriter) error { called = true; return nil })
		var unsupported *storage.ExclusiveUnsupportedError
		require.ErrorAs(t, err, &unsupported)
		assert.False(t, called)
	}
}

// TestExclusive_MemoryAndLocalStillExclude checks that the providers whose
// namespace has one owner -- a private in-memory store, and a local directory
// held under an operating-system lock -- keep Exclusive.
func TestExclusive_MemoryAndLocalStillExclude(t *testing.T) {
	for _, provider := range []string{"memory", "local"} {
		t.Run(provider, func(t *testing.T) {
			settings := map[string]string{}
			if provider == "local" {
				settings["path"] = t.TempDir()
			}
			e, err := New(provider, settings)
			require.NoError(t, err)
			defer func() { _ = e.Close() }()
			called := false
			require.NoError(t, e.Exclusive(func(storage.ReadWriter) error { called = true; return nil }))
			assert.True(t, called)
		})
	}
}
