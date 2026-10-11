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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// These tests pin the fixture itself (go-qrdb#489): a double that stands in for
// the registry-resolved KV service has to resolve like the registry, or every
// test built on it measures a layout nothing runs.

// TestResolvedKVSvc_WritesLandInsideTheCompositeNamespace: nothing a service
// writes through the resolved view reaches the raw store bare.
func TestResolvedKVSvc_WritesLandInsideTheCompositeNamespace(t *testing.T) {
	ctx := context.Background()
	kv := newResolvedKVSvc()
	svc := NewCollectionService(kv)
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "v"}})
	require.NoError(t, err)

	ns := tenant.ComposeNamespace(resolvedTenant, resolvedDatabase)
	raw := kv.rawKeys("")
	require.NotEmpty(t, raw)
	for _, key := range raw {
		assert.True(t, strings.HasPrefix(key, ns), "key %q was written outside the composite namespace", key)
	}
	assert.Empty(t, kv.rawKeys(collectionMetadataPrefix), "the bare, pre-#441 collection layout must not appear")
	assert.NotEmpty(t, kv.rawKeys(ns+collectionMetadataPrefix))
}

// TestResolvedKVSvc_EnumerationStripsTheNamespaceAndKeepsTheGuard: the caller
// sees the keys it wrote, and an internal key stays hidden from List exactly
// as it does on every production KVServicer.
func TestResolvedKVSvc_EnumerationStripsTheNamespaceAndKeepsTheGuard(t *testing.T) {
	ctx := context.Background()
	kv := newResolvedKVSvc()
	require.NoError(t, kv.Put(ctx, "plain/a", []byte("1")))
	require.NoError(t, kv.Put(ctx, "__sys:hidden", []byte("2")))

	keys, err := kv.List(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"plain/a"}, keys)

	all, err := kv.ListAll(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"__sys:hidden", "plain/a"}, all)

	pairs, err := kv.Scan(ctx, "plain/")
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"plain/a": []byte("1")}, pairs)
}

// TestMemoryKVSvc_ViewsOfOneStoreAreIsolated: two databases of one store, which
// is the shape a teardown or a tenant-isolation test needs, cannot see each
// other's keys and share the store's atomicity.
func TestMemoryKVSvc_ViewsOfOneStoreAreIsolated(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVSvc()
	one := store.view("acme", "one")
	two := store.view("acme", "two")

	stored, err := one.PutIfAbsent(ctx, "k", []byte("a"))
	require.NoError(t, err)
	require.True(t, stored)
	stored, err = two.PutIfAbsent(ctx, "k", []byte("b"))
	require.NoError(t, err)
	assert.True(t, stored, "the same logical key in another database is a different key")

	stored, err = one.PutIfAbsent(ctx, "k", []byte("c"))
	require.NoError(t, err)
	assert.False(t, stored, "within one database the claim is still decided once")

	got, err := two.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "b", string(got))
	assert.Equal(t, []string{
		tenant.ComposeNamespace("acme", "one") + "k",
		tenant.ComposeNamespace("acme", "two") + "k",
	}, store.rawKeys(""))
}
