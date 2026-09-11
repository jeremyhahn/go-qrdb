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

package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// TestNamespacedKeyPrefixes_CoversEveryNamespacedShape pins the list itself.
// It is asserted exactly rather than by membership: a prefix silently dropped
// from it is a keyspace silently left behind by every drop.
func TestNamespacedKeyPrefixes_CoversEveryNamespacedShape(t *testing.T) {
	ns := tenant.ComposeNamespace("acme", "reports")

	assert.Equal(t, []string{
		ns,
		timeseries.KeyPrefixPoints + ns,
		timeseries.KeyPrefixMetricIndex + ns,
		timeseries.KeyPrefixRetention + ns,
	}, NamespacedKeyPrefixes(ns))
}

// TestNamespacedKeyPrefixes_OmitsTheTagIndex is the deliberate exclusion,
// stated as a test so a later "completeness" change has to argue with it. A
// tag index key is "idx:tag:" + tagKey + ":" + tagValue and carries no metric,
// so it belongs to every database at once and to none in particular.
func TestNamespacedKeyPrefixes_OmitsTheTagIndex(t *testing.T) {
	for _, p := range NamespacedKeyPrefixes(tenant.ComposeNamespace("acme", "reports")) {
		assert.False(t, strings.HasPrefix(p, timeseries.KeyPrefixTagIndex),
			"the tag index is not database-scoped and must not be reclaimed by a database drop")
	}
}

// TestNamespacedKeyPrefixes_EmptyNamespaceYieldsNothing holds the refusal that
// keeps a lost scope from becoming a store wipe: no namespace means no
// prefixes, never the empty prefix.
func TestNamespacedKeyPrefixes_EmptyNamespaceYieldsNothing(t *testing.T) {
	assert.Nil(t, NamespacedKeyPrefixes(""))
}

// TestCollectionKeyPrefixes_CoversCatalogAndDocuments names both halves. A
// list with only one of them leaves either orphaned documents or a collection
// that lists but cannot be read.
func TestCollectionKeyPrefixes_CoversCatalogAndDocuments(t *testing.T) {
	assert.Equal(t, []string{
		collectionMetadataPrefix + "reports/",
		collectionDataPrefix + "reports:",
	}, CollectionKeyPrefixes("reports"))
}

// TestCollectionKeyPrefixes_DoNotMatchASiblingByNamePrefix is the separator
// test: "reports" must not reclaim "reportsarchive".
func TestCollectionKeyPrefixes_DoNotMatchASiblingByNamePrefix(t *testing.T) {
	for _, p := range CollectionKeyPrefixes("reports") {
		assert.False(t, strings.HasPrefix(collectionMetadataPrefix+"reportsarchive/users", p))
		assert.False(t, strings.HasPrefix(collectionDataPrefix+"reportsarchive:users/1", p))
	}
}

// TestCollectionKeyPrefixes_EmptyDatabaseYieldsNothing is the peer of the
// empty-namespace refusal.
func TestCollectionKeyPrefixes_EmptyDatabaseYieldsNothing(t *testing.T) {
	assert.Nil(t, CollectionKeyPrefixes(""))
}

// TestDatabaseIndexNamePrefixes_CoversBothNameShapes pins that the index
// catalog is addressed in the same two shapes the keyspace is.
func TestDatabaseIndexNamePrefixes_CoversBothNameShapes(t *testing.T) {
	ns := tenant.ComposeNamespace("acme", "reports")

	assert.Equal(t, []string{ns, "reports."}, DatabaseIndexNamePrefixes(ns, "reports"))
	assert.Equal(t, []string{ns}, DatabaseIndexNamePrefixes(ns, ""))
	assert.Equal(t, []string{"reports."}, DatabaseIndexNamePrefixes("", "reports"))
}

// TestDatabaseIndexNamePrefixes_EmptyScopeYieldsNothing holds the refusal on
// the third constructor too.
func TestDatabaseIndexNamePrefixes_EmptyScopeYieldsNothing(t *testing.T) {
	assert.Nil(t, DatabaseIndexNamePrefixes("", ""))
}

// TestReclaimErrors_CarryTheirSubjectAndCause covers the error vocabulary. An
// operator reading a failed drop needs to know which prefix, index or
// namespace refused and why, and errors.As/Is has to reach the cause.
func TestReclaimErrors_CarryTheirSubjectAndCause(t *testing.T) {
	cause := errors.New("store unreachable")

	tests := []struct {
		name     string
		err      error
		contains []string
		wrapped  bool
	}{
		{
			name:     "scope",
			err:      &ReclaimScopeError{TenantID: "acme", Database: ""},
			contains: []string{"unscoped", `"acme"`},
		},
		{
			name:     "store unavailable",
			err:      &ReclaimStoreUnavailableError{TenantID: "acme", Err: cause},
			contains: []string{`"acme"`, "store unreachable"},
			wrapped:  true,
		},
		{
			name:     "store unavailable without a cause",
			err:      &ReclaimStoreUnavailableError{TenantID: "acme"},
			contains: []string{`"acme"`},
		},
		{
			name:     "prefix",
			err:      &ReclaimPrefixError{Prefix: "acme/reports/", Op: "list", Err: cause},
			contains: []string{"list", `"acme/reports/"`, "store unreachable"},
			wrapped:  true,
		},
		{
			name:     "index",
			err:      &ReclaimIndexError{Name: "reports.users.email", Op: "drop", Err: cause},
			contains: []string{"drop", `"reports.users.email"`},
			wrapped:  true,
		},
		{
			name:     "vector",
			err:      &ReclaimVectorError{Namespace: "acme/reports/docs", Op: "delete", Err: cause},
			contains: []string{"delete", `"acme/reports/docs"`},
			wrapped:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			require.NotEmpty(t, msg)
			assert.True(t, strings.HasPrefix(msg, "service: "), "error strings identify their origin: %q", msg)
			for _, want := range tc.contains {
				assert.Contains(t, msg, want)
			}
			assert.Equal(t, tc.wrapped, errors.Is(tc.err, cause), "Unwrap must reach the cause exactly when there is one")
		})
	}
}
