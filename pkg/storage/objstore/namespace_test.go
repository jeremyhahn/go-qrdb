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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestLocal_SymlinkAliasesOfANamespaceShareOneIdentity checks that two
// prefixes of equal length that are symlinks to one directory are one
// namespace: they share its owner, so the second open is refused while the
// first is open, and its journal, so an interrupted batch written through one
// alias is replayed when the namespace is reopened through the other.
func TestLocal_SymlinkAliasesOfANamespaceShareOneIdentity(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(base, "cc"), 0o700))
	for _, alias := range []string{"aa", "bb"} {
		if err := os.Symlink("cc", filepath.Join(base, alias)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	settings := map[string]string{"path": base}

	first, err := New("local", settings, WithObjectPrefix("aa/"))
	require.NoError(t, err)
	_, err = New("local", settings, WithObjectPrefix("bb/"))
	var owned *NamespaceOwnedError
	require.ErrorAs(t, err, &owned, "an alias of an open namespace must not open it a second time")

	require.NoError(t, first.Set([]byte("before"), []byte("v0")))
	// An interrupted batch: its journal is published, its ops never applied.
	pending := []storage.BufferedOp{{Key: []byte("pending"), Value: []byte("v1")}}
	require.NoError(t, first.writeJournal(encodeJournal(pending)))
	require.NoError(t, first.Close())

	for _, alias := range []string{"bb/", "aa/"} {
		e, err := New("local", settings, WithObjectPrefix(alias))
		require.NoError(t, err, "alias %q", alias)
		for key, want := range map[string]string{"before": "v0", "pending": "v1"} {
			v, err := e.Get([]byte(key))
			require.NoError(t, err)
			assert.Equal(t, want, string(v), "alias %q key %q", alias, key)
		}
		require.NoError(t, e.Close())
	}
}

// TestLocal_NestedPrefixOnAnAncestorsRecordOrControlNameIsRefused checks that
// a namespace whose directory would take the path of an object an enclosing
// namespace owns -- a record name, the format marker, the journal or the
// owner lock -- is refused before any directory is created, so the enclosing
// engine keeps reading and writing that key.
func TestLocal_NestedPrefixOnAnAncestorsRecordOrControlNameIsRefused(t *testing.T) {
	base := t.TempDir()
	settings := map[string]string{"path": base}
	parent, err := New("local", settings, WithObjectPrefix("ns/"))
	require.NoError(t, err)
	defer func() { _ = parent.Close() }()

	// Each is tried before the parent writes key "a", so an accepted one
	// would have made the directory where that record belongs.
	for _, prefix := range []string{
		"ns/k/61_/",
		"ns/k/61_/deeper/",
		"ns/k/" + strings.Repeat("61", segmentLen/2) + "/62_/",
		"ns/h/" + strings.Repeat("0", hashedNameLen-len(hashedPrefix)) + "/",
		"ns/" + formatMarkerName + "/",
		"ns/" + journalName + "/",
		"ns/" + ownerLockName + "/",
		"k/61_/",
	} {
		_, err := New("local", settings, WithObjectPrefix(prefix))
		var collision *NamespaceCollisionError
		require.ErrorAs(t, err, &collision, "prefix %q", prefix)
		assert.Equal(t, prefix, collision.Prefix)
	}

	// A namespace nested in the enclosing one's directories, never on one of
	// its objects, is still accepted.
	for _, prefix := range []string{"ns/child/", "ns/k/", "ns/h/", "ns/k/61/"} {
		child, err := New("local", settings, WithObjectPrefix(prefix))
		require.NoError(t, err, "prefix %q", prefix)
		require.NoError(t, child.Close())
	}

	require.NoError(t, parent.Set([]byte("a"), []byte("v1")))
	info, err := os.Lstat(filepath.Join(base, "ns", "k", "61_"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the parent's record must stay a file, not become a directory")
	v, err := parent.Get([]byte("a"))
	require.NoError(t, err)
	assert.Equal(t, "v1", string(v))
	require.NoError(t, parent.Set([]byte("a"), []byte("v2")))
	v, err = parent.Get([]byte("a"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(v))
}

// TestValidObjectPrefix_RefusesAnAncestorsObjectName checks the validator
// itself, for every provider: a prefix segment run that spells a record or
// control-file name of the namespace above it is a NamespaceCollisionError.
func TestValidObjectPrefix_RefusesAnAncestorsObjectName(t *testing.T) {
	for _, prefix := range []string{"k/61_/", "a/k/6162_/", "a/h/" + strings.Repeat("f", hashedNameLen-len(hashedPrefix)) + "/", formatMarkerName + "/", "a/" + journalName + "/"} {
		var collision *NamespaceCollisionError
		require.ErrorAs(t, validObjectPrefix(prefix), &collision, "prefix %q", prefix)
	}
	for _, prefix := range []string{"k/", "k/61/", "k/6_/", "k/zz_/", "h/", "h/abc/", "a/b_/"} {
		assert.NoError(t, validObjectPrefix(prefix), "prefix %q", prefix)
	}
}
