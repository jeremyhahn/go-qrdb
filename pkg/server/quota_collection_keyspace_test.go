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
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestQuotaCollection_SupersededWriteRetainsReservation: a refused write whose
// document somebody else removed and released keeps its reservation, so the
// bytes are not released a second time; the plain refusal releases.
func TestQuotaCollection_SupersededWriteRetainsReservation(t *testing.T) {
	ctx := context.Background()
	doc := map[string]interface{}{"v": "x"}
	n := service.DocumentBodyBytes(doc)
	refusal := &service.CollectionNotFoundError{Database: "db", Collection: "c"}
	for name, tc := range map[string]struct {
		err  error
		want int64
	}{
		"superseded": {&service.CollectionWriteSupersededError{Err: refusal}, n},
		"refused":    {refusal, 0},
	} {
		acct, _ := newTestAccountant(t, nil)
		q := newQuotaCollectionService(&failingCollection{err: tc.err}, acct, "t1", 1<<20)
		if _, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "c", Document: doc}); err == nil {
			t.Fatalf("%s: the write must fail", name)
		}
		if got := acct.Usage("t1"); got != tc.want {
			t.Errorf("%s: usage = %d, want %d", name, got, tc.want)
		}
	}
}

// TestQuotaCollection_DropRequiresAMeteredSurface: a wrapped service that
// cannot report what its drop removed is refused, not dropped unmetered.
func TestQuotaCollection_DropRequiresAMeteredSurface(t *testing.T) {
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaCollectionService(&failingCollection{}, acct, "t1", 1<<20)
	var unsupported *service.CapabilityUnsupportedError
	if err := q.DropCollection(context.Background(), &service.DropCollectionRequest{Database: "db", Name: "c"}); !errors.As(err, &unsupported) {
		t.Fatalf("expected CapabilityUnsupportedError, got %v", err)
	}
}

// TestQuotaCollection_SweepReleasesWhatItFreed: the sweeper, driven through
// the wrapper, releases the body bytes its teardowns removed.
func TestQuotaCollection_SweepReleasesWhatItFreed(t *testing.T) {
	ctx := context.Background()
	kv := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaCollectionService(service.NewCollectionService(kv), acct, "t1", 1<<20)
	if _, err := q.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "c"}); err != nil {
		t.Fatal(err)
	}
	doc := map[string]interface{}{"v": "0123456789"}
	if _, err := q.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "c", Document: doc}); err != nil {
		t.Fatal(err)
	}
	// A drop that crashed after marking the collection an hour ago.
	raw, err := kv.Get(ctx, "__sys:collections/db/c")
	if err != nil {
		t.Fatal(err)
	}
	var info service.CollectionInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	info.State, info.StateSince = service.CollectionStateDropping, time.Now().Add(-time.Hour)
	data, err := json.Marshal(&info)
	if err != nil {
		t.Fatal(err)
	}
	if err := kv.Put(ctx, "__sys:collections/db/c", data); err != nil {
		t.Fatal(err)
	}

	res, err := q.SweepStuckCollections(ctx, "db", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if res.Swept != 1 || res.FreedBytes != service.DocumentBodyBytes(doc) {
		t.Fatalf("sweep = %+v", res)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after sweep = %d, want 0", got)
	}
}

// TestRaftBackendFactory_LeadsShard covers every answer: a led started shard,
// one another replica leads, one whose leader is unknown or unreadable, one
// not started here, and a factory with no leadership source.
func TestRaftBackendFactory_LeadsShard(t *testing.T) {
	f := &RaftBackendFactory{
		shards: map[raftShardKey]*raftShard{
			{tenantID: "t", database: "led"}:      {shardID: 1, replicaID: 3},
			{tenantID: "t", database: "follower"}: {shardID: 2, replicaID: 3},
			{tenantID: "t", database: "unknown"}:  {shardID: 4, replicaID: 3},
			{tenantID: "t", database: "broken"}:   {shardID: 5, replicaID: 3},
		},
		leaderOf: func(shardID uint64) (uint64, bool, error) {
			switch shardID {
			case 1:
				return 3, true, nil
			case 2:
				return 9, true, nil
			case 4:
				return 0, false, nil
			default:
				return 3, true, errors.New("unreadable")
			}
		},
	}
	want := map[string]bool{"led": true, "follower": false, "unknown": false, "broken": false, "absent": false}
	for db, leads := range want {
		if got := f.LeadsShard("t", db); got != leads {
			t.Errorf("LeadsShard(%q) = %v, want %v", db, got, leads)
		}
	}
	f.leaderOf = nil
	if f.LeadsShard("t", "led") {
		t.Fatal("a factory with no leadership source leads nothing")
	}
}

// TestServer_CollectionSweepTargets_OnlyWhatThisNodeLeads: a node that does
// not lead the system shard sweeps none of the shared store's databases, and
// still sweeps what only it serves -- its volume tenants' own Hosts.
func TestServer_CollectionSweepTargets_OnlyWhatThisNodeLeads(t *testing.T) {
	tenants := &listFnServicer{fn: func(context.Context) ([]tenant.TenantInfo, error) {
		return []tenant.TenantInfo{
			{ID: tenantid.System},
			{ID: "prefix-t"},
			{ID: "vol-up", IsolationType: tenant.IsolationVolume},
		}, nil
	}}
	hosts := NewTenantHostRegistry(&seal.BarrierRegistry{}, staticDataRoot{}, volTestLogger())
	hosts.hosts["vol-up"] = &tenantHost{tenantID: "vol-up", services: journalStore(t, time.Now(), [2]string{"vol-up", "system"})}
	shared := journalStore(t, time.Now(), [2]string{tenantid.System, "system"}, [2]string{"prefix-t", "system"})

	for leads, want := range map[bool][]string{
		false: {"vol-up/system"},
		true:  {"prefix-t/system", tenantid.System + "/system", "vol-up/system"},
	} {
		dbReg := NewDatabaseServiceRegistry("system")
		dbReg.Register("system", shared)
		srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
			WithDatabaseServiceRegistry(dbReg),
			WithTenantService(tenants), WithTenantHostRegistry(hosts),
			WithCollectionSweepLeader(func() bool { return leads }))
		targets, err := srv.collectionSweepTargets(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, len(targets))
		for i, tgt := range targets {
			got[i] = tgt.Tenant + "/" + tgt.Database
		}
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("leads=%v: targets = %v, want %v", leads, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("leads=%v: targets = %v, want %v", leads, got, want)
			}
		}
	}
}
