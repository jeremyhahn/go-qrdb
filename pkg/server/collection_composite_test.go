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

package server

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// recordingIndexService is an IndexServicer that records the index names it is
// asked to create, so a test can tell whether a collection service was built
// WITH an index or without one.
type recordingIndexService struct {
	service.IndexServicer
	created []string
	configs []index.IndexConfig
}

// CreateIndex records the name and the config -- the config carries the
// declared KeyPrefix, which is half of the scope the engine maintains the
// index under -- and succeeds.
func (r *recordingIndexService) CreateIndex(_ context.Context, name, _ string, cfg index.IndexConfig) error {
	r.created = append(r.created, name)
	r.configs = append(r.configs, cfg)
	return nil
}

// DropIndex succeeds without recording; teardown is not what these tests
// measure.
func (r *recordingIndexService) DropIndex(_ context.Context, _ string) error { return nil }

// DropOwnedIndex answers as DropIndex does: this double records no
// ownership, and no test that uses it exercises an owner-checked drop.
func (r *recordingIndexService) DropOwnedIndex(ctx context.Context, name, _ string) error {
	return r.DropIndex(ctx, name)
}

// TestBuildCompositeCollectionServiceRefusesABundleWithNoKV holds the fail
// path: a bundle with no KV servicer has no document store, so there is
// nothing to build a collection over and the builder must say so rather than
// return a service that dereferences nil on first use.
func TestBuildCompositeCollectionServiceRefusesABundleWithNoKV(t *testing.T) {
	if svc := buildCompositeCollectionService(&DatabaseServices{}, nil); svc != nil {
		t.Fatalf("expected nil for a bundle with no KV, got %T", svc)
	}
	if svc := buildCompositeCollectionService(&DatabaseServices{Index: &recordingIndexService{}}, nil); svc != nil {
		t.Fatalf("expected nil for an index-only bundle, got %T", svc)
	}
}

// TestBuildCompositeCollectionServiceBuildsOverWhatTheBundleHas covers the
// two shapes the builder serves rather than refuses. A bundle with KV alone
// yields a collection service with no secondary-index maintenance; a bundle
// that also carries an index yields one that creates the collection's declared
// indexes. The difference is observable, so it is asserted rather than assumed.
func TestBuildCompositeCollectionServiceBuildsOverWhatTheBundleHas(t *testing.T) {
	ctx := context.Background()
	req := &service.CreateCollectionRequest{
		Database: "appdb",
		Name:     "users",
		Indexes:  []service.IndexDefinition{{FieldName: "email"}},
	}

	t.Run("kv only", func(t *testing.T) {
		svc := buildCompositeCollectionService(&DatabaseServices{KV: newMockKVService()}, nil)
		if svc == nil {
			t.Fatal("expected a collection service")
		}
		// Without an index servicer the declared index cannot be built, and
		// the create refuses it rather than recording an index that would
		// never exist; a collection without indexes is served.
		var invalid *service.CollectionInvalidIndexError
		if _, err := svc.CreateCollection(ctx, req); !errors.As(err, &invalid) {
			t.Fatalf("CreateCollection with an index on a KV-only bundle: %v", err)
		}
		plain := &service.CreateCollectionRequest{Database: "appdb", Name: "users"}
		if _, err := svc.CreateCollection(ctx, plain); err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
		if _, err := svc.GetCollectionInfo(ctx, "appdb", "users"); err != nil {
			t.Fatalf("GetCollectionInfo: %v", err)
		}
	})

	t.Run("kv and index", func(t *testing.T) {
		idx := &recordingIndexService{}
		svc := buildCompositeCollectionService(&DatabaseServices{KV: newMockKVService(), Index: idx}, nil)
		if svc == nil {
			t.Fatal("expected a collection service")
		}
		if _, err := svc.CreateCollection(ctx, req); err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
		want := []string{"appdb.users.email"}
		if len(idx.created) != len(want) || idx.created[0] != want[0] {
			t.Fatalf("indexes created = %v, want %v -- the bundle's index servicer was not wired", idx.created, want)
		}
	})
}

// TestCompositeCollectionCacheReturnsTheSameServicePerScope is the property
// the cache exists for. A CollectionService owns the per-collection,
// process-local increment ID generators, and applyComposite runs on EVERY
// resolve -- so a service rebuilt per call would restart those counters per
// REQUEST. The identity check is the cheapest true statement of that.
func TestCompositeCollectionCacheReturnsTheSameServicePerScope(t *testing.T) {
	c := newCompositeCollectionCache()
	kv := newMockKVService()
	src := &DatabaseServices{KV: kv}

	first := c.getOrBuild(src, src, "tenant-a", "appdb")
	second := c.getOrBuild(src, src, "tenant-a", "appdb")
	if first == nil {
		t.Fatal("expected a collection service")
	}
	if first != second {
		t.Fatal("cache returned a different service for the same (bundle, prefix)")
	}

	other := c.getOrBuild(src, src, "tenant-b", "appdb")
	if other == first {
		t.Fatal("cache returned the same service for two different tenants")
	}
}

// TestCompositeCollectionCacheRebuildsWhenTheBundleIsReplaced covers the
// invalidation guard. A database dropped and recreated yields a NEW
// *DatabaseServices under the same name; continuing to serve a collection
// service built over the previous, closed engines would be a use-after-close.
func TestCompositeCollectionCacheRebuildsWhenTheBundleIsReplaced(t *testing.T) {
	c := newCompositeCollectionCache()

	first := c.getOrBuild(&DatabaseServices{KV: newMockKVService()}, &DatabaseServices{KV: newMockKVService()}, "tenant-a", "appdb")
	replacement := &DatabaseServices{KV: newMockKVService()}
	second := c.getOrBuild(replacement, replacement, "tenant-a", "appdb")

	if first == second {
		t.Fatal("cache kept serving the collection service of a replaced bundle")
	}
	if third := c.getOrBuild(replacement, replacement, "tenant-a", "appdb"); third != second {
		t.Fatal("cache did not settle on the replacement")
	}
}

// TestCompositeCollectionCacheCachesTheAbsentCase pins that a bundle with no
// collection surface does not re-enter the builder on every request. Without
// it the nil result would look like a cache miss forever.
func TestCompositeCollectionCacheCachesTheAbsentCase(t *testing.T) {
	c := newCompositeCollectionCache()
	src := &DatabaseServices{}
	prefix := tenant.ComposeNamespace("tenant-a", "appdb")

	if svc := c.getOrBuild(src, src, "tenant-a", "appdb"); svc != nil {
		t.Fatalf("expected nil for a bundle with no KV, got %T", svc)
	}
	c.mu.RLock()
	entry, ok := c.entries[prefix]
	c.mu.RUnlock()
	if !ok {
		t.Fatal("the absent case was not cached")
	}
	if entry.src != src || entry.svc != nil {
		t.Fatalf("unexpected cache entry %#v", entry)
	}
}

// TestApplyCompositeReusesOneCollectionServicePerScope is the same statement
// through the caller that matters: two resolutions of the same (tenant,
// database) pair must hand back one collection service, not two.
func TestApplyCompositeReusesOneCollectionServicePerScope(t *testing.T) {
	_, svcs := sharedCollectionStore()
	dbReg := NewDatabaseServiceRegistry("appdb")
	dbReg.Register("appdb", svcs)
	reg := NewRESTServiceRegistry(dbReg, nil, tenant.SystemTenantID).(*restServiceRegistry)

	first, err := reg.CollectionService("tenant-a", "appdb")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reg.CollectionService("tenant-a", "appdb")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("two resolutions of the same scope returned different collection services")
	}
}

// TestCompositeCollectionCacheNilReceiverStillNamespaces covers the
// constructed-by-literal shape: this package's tests build a
// restServiceRegistry directly, so a nil cache must still hand back a
// collection service over the NAMESPACED engines. Returning nil there would
// silently disable the collection surface; panicking would take the whole
// resolve down.
func TestCompositeCollectionCacheNilReceiverStillNamespaces(t *testing.T) {
	var c *compositeCollectionCache
	kv := newMockKVService()
	src := &DatabaseServices{KV: kv}

	svc := c.getOrBuild(src, src, "tenant-a", "appdb")
	if svc == nil {
		t.Fatal("a nil cache must still build a collection service")
	}
	if _, err := svc.CreateCollection(context.Background(), &service.CreateCollectionRequest{
		Database: "appdb", Name: "users",
	}); err != nil {
		t.Fatalf("CreateCollection through the uncached service: %v", err)
	}
	if _, ok := kv.store["__sys:collections/appdb/users"]; !ok {
		t.Fatalf("expected the bare metadata key from an unwrapped KV, got %v", storeKeys(kv))
	}
}

// TestApplyCompositeNamespacesWithoutTheConstructor is the same statement at
// the caller, over a registry built as a struct literal rather than through
// NewRESTServiceRegistry: the composite collection service must still write
// inside the tenant's namespace.
func TestApplyCompositeNamespacesWithoutTheConstructor(t *testing.T) {
	kv, svcs := sharedCollectionStore()
	reg := &restServiceRegistry{dbReg: NewDatabaseServiceRegistry("appdb")}

	out := reg.applyComposite(svcs, "tenant-a", "appdb")
	if _, err := out.Collection.CreateCollection(context.Background(), &service.CreateCollectionRequest{
		Database: "appdb", Name: "users",
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if _, ok := kv.store["tenant-a/appdb/__sys:collections/appdb/users"]; !ok {
		t.Fatalf("metadata key was not namespaced; keys = %v", storeKeys(kv))
	}
}

// TestCompositeCollectionCacheIsSafeForConcurrentUse drives the double-checked
// lock from many goroutines at once. Two things are asserted and neither is
// incidental: every caller gets the SAME service (a racing builder that won
// twice would hand two callers two independent sets of increment ID
// generators), and the run is clean under -race, which is the only way the
// second, write-locked identity check is reached at all.
func TestCompositeCollectionCacheIsSafeForConcurrentUse(t *testing.T) {
	c := newCompositeCollectionCache()
	src := &DatabaseServices{KV: newMockKVService()}

	const goroutines = 32
	results := make(chan service.CollectionServicer, goroutines)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- c.getOrBuild(src, src, "tenant-a", "appdb")
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var first service.CollectionServicer
	for got := range results {
		if got == nil {
			t.Fatal("a concurrent caller got no collection service")
		}
		if first == nil {
			first = got
			continue
		}
		if got != first {
			t.Fatal("concurrent callers got different collection services for one scope")
		}
	}
}
