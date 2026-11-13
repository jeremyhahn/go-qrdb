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
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// memRouteStore is an in-memory AuditRouteStore with the replicated store's
// first-writer-wins PutIfAbsent.
type memRouteStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemRouteStore() *memRouteStore { return &memRouteStore{data: map[string][]byte{}} }

func (s *memRouteStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key], nil
}

func (s *memRouteStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; ok {
		return false, nil
	}
	s.data[key] = append([]byte(nil), value...)
	return true, nil
}

func (s *memRouteStore) route(tenantID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.data[AuditRouteKey(tenantID)])
}

// chainShard is the system shard as the route sees it: a recording host
// whose replicated chain for "widgets" holds head events, gated per feature.
type chainShard struct {
	host  *perFeatureHost
	cmds  *[][]byte
	reads []any
}

func newChainShard(head uint64, on map[feature.Feature]bool) *chainShard {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	c := &chainShard{cmds: cmds}
	inner.syncReadFn = func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
		c.reads = append(c.reads, query)
		switch query.(type) {
		case audit.VerifyRequest:
			return &audit.ChainContinuityReport{Verified: true, Records: int64(head), Head: head, Low: min(head, 1), Genesis: head > 0}, nil
		case audit.QueryRequest:
			return &audit.Page{}, nil
		}
		return nil, nil
	}
	c.host = &perFeatureHost{mockHost: inner, on: on}
	return c
}

func auditActive() map[feature.Feature]bool {
	return map[feature.Feature]bool{
		featuregate.AuditAppend:        true,
		featuregate.VerifiableLog:      true,
		featuregate.VerifiableLogAudit: true,
	}
}

// mixedCapability is a cluster where some replica does not yet advertise
// the audit instance: VerifiableLogAudit is not activated.
func mixedCapability() map[feature.Feature]bool {
	return map[feature.Feature]bool{featuregate.AuditAppend: true, featuregate.VerifiableLog: true}
}

func homeShard() (*fakeHomeGroups, *[][]byte) {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	return &fakeHomeGroups{host: &perFeatureHost{mockHost: inner, on: auditActive()}}, cmds
}

// TestAuditRoute_ChainOnTheSystemShardKeepsItsRoute: a barrier tenant whose
// replicated chain already has records on the system shard keeps that
// route after the home groups attach, for writes, Query and Verify, so its
// history stays readable and its next event extends the same chain.
func TestAuditRoute_ChainOnTheSystemShardKeepsItsRoute(t *testing.T) {
	system := newChainShard(3, auditActive())
	homes, homeCmds := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	routes := newMemRouteStore()
	r.SetHomeGroupSource(homes, routes)
	logger := NewReplicatedLogger(r)
	ctx := context.Background()

	if err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*system.cmds) != 1 || len(*homeCmds) != 0 {
		t.Fatalf("proposals: system %d, home %d; want the event on the system shard's chain", len(*system.cmds), len(*homeCmds))
	}
	if _, err := logger.Query(ctx, audit.Filter{TenantID: "widgets"}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	report, err := logger.VerifyTenantChain(ctx, "widgets")
	if err != nil || report.Head != 3 {
		t.Fatalf("VerifyTenantChain = %+v, %v; want the system shard's chain (head 3)", report, err)
	}
	if len(homes.asked) != 0 {
		t.Fatalf("home groups asked for %v, want none", homes.asked)
	}
	if got := routes.route("widgets"); got != AuditRouteSystem {
		t.Fatalf("recorded route = %q, want %q", got, AuditRouteSystem)
	}
}

// TestAuditRoute_NoPriorChainRoutesToTheHomeGroup: a barrier tenant with
// no chain on the system shard records the home route and writes there.
func TestAuditRoute_NoPriorChainRoutesToTheHomeGroup(t *testing.T) {
	system := newChainShard(0, auditActive())
	homes, homeCmds := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	routes := newMemRouteStore()
	r.SetHomeGroupSource(homes, routes)
	logger := NewReplicatedLogger(r)

	if err := logger.Emit(context.Background(), audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*system.cmds) != 0 || len(*homeCmds) != 1 {
		t.Fatalf("proposals: system %d, home %d; want the event in the home group", len(*system.cmds), len(*homeCmds))
	}
	if got := routes.route("widgets"); got != AuditRouteHome {
		t.Fatalf("recorded route = %q, want %q", got, AuditRouteHome)
	}
}

// TestAuditRoute_RecordedRouteWins: the recorded route is the fact every
// node follows; a node never re-derives it once recorded.
func TestAuditRoute_RecordedRouteWins(t *testing.T) {
	system := newChainShard(0, auditActive())
	homes, homeCmds := homeShard()
	routes := newMemRouteStore()
	if _, err := routes.PutIfAbsent(context.Background(), AuditRouteKey("widgets"), []byte(AuditRouteSystem)); err != nil {
		t.Fatal(err)
	}
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(homes, routes)
	if err := NewReplicatedLogger(r).Emit(context.Background(), audit.AuditEvent{TenantID: "widgets"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*system.cmds) != 1 || len(*homeCmds) != 0 {
		t.Fatalf("proposals: system %d, home %d; want the recorded system route", len(*system.cmds), len(*homeCmds))
	}
}

// TestAuditRoute_MixedCapabilityNeverWritesToTheHomeGroup: until every
// replica advertises the audit instance, no event reaches a home group: a
// barrier tenant stays on (and records) the system shard, a volume or raft
// tenant is refused, and a tenant already recorded home is refused rather
// than split onto the system shard.
func TestAuditRoute_MixedCapabilityNeverWritesToTheHomeGroup(t *testing.T) {
	ctx := context.Background()
	system := newChainShard(0, mixedCapability())
	homes, homeCmds := homeShard()
	routes := newMemRouteStore()
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(homes, routes)
	logger := NewReplicatedLogger(r)

	if err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets"}); err != nil {
		t.Fatalf("Emit(widgets): %v", err)
	}
	if got := routes.route("widgets"); got != AuditRouteSystem {
		t.Fatalf("recorded route = %q, want %q", got, AuditRouteSystem)
	}
	for _, id := range []string{"vault", "shard1"} {
		var notActive *AuditHomeNotActiveError
		if err := logger.Emit(ctx, audit.AuditEvent{TenantID: id}); !errors.As(err, &notActive) {
			t.Fatalf("Emit(%s) = %v, want *AuditHomeNotActiveError", id, err)
		}
	}

	if _, err := routes.PutIfAbsent(ctx, AuditRouteKey("gadgets"), []byte(AuditRouteHome)); err != nil {
		t.Fatal(err)
	}
	lookup := isolatedTenantsLookup()
	lookup.tenants["gadgets"] = &tenant.TenantInfo{ID: "gadgets", IsolationType: tenant.IsolationBarrier}
	r2 := NewSystemShardAuditResolver(system.host, 1, lookup)
	r2.SetHomeGroupSource(homes, routes)
	var notActive *AuditHomeNotActiveError
	if err := NewReplicatedLogger(r2).Emit(ctx, audit.AuditEvent{TenantID: "gadgets"}); !errors.As(err, &notActive) {
		t.Fatalf("Emit(gadgets) = %v, want *AuditHomeNotActiveError", err)
	}

	if len(*homeCmds) != 0 || len(homes.asked) != 0 {
		t.Fatalf("home group: %d proposals, asked for %v; want none", len(*homeCmds), homes.asked)
	}
	if len(*system.cmds) != 1 {
		t.Fatalf("system shard proposals = %d, want widgets' one event", len(*system.cmds))
	}
}

// TestAuditRoute_ReadsNeverRecordARoute: Query and Verify follow the route
// without recording one, so reading never pins a tenant.
func TestAuditRoute_ReadsNeverRecordARoute(t *testing.T) {
	system := newChainShard(0, mixedCapability())
	homes, _ := homeShard()
	routes := newMemRouteStore()
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(homes, routes)
	if _, err := NewReplicatedLogger(r).VerifyTenantChain(context.Background(), "widgets"); err != nil {
		t.Fatalf("VerifyTenantChain: %v", err)
	}
	if got := routes.route("widgets"); got != "" {
		t.Fatalf("a read recorded route %q", got)
	}
}

// TestAuditRoute_AuditInstanceOnTheSystemShardIsNotReadFromTheHome: on
// the log surface, a barrier tenant whose audit chain is recorded on the
// system shard is refused, as before, rather than answered by the home
// group's empty instance; its other logs apply in the home group.
func TestAuditRoute_AuditInstanceOnTheSystemShardIsNotReadFromTheHome(t *testing.T) {
	system := newChainShard(3, auditActive())
	homes, _ := homeShard()
	routes := newMemRouteStore()
	audits := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	logs := NewSystemShardLogResolver(system.host, 1, isolatedTenantsLookup())
	audits.SetHomeGroupSource(homes, routes)
	logs.SetHomeGroupSource(homes, routes)
	if err := NewReplicatedLogger(audits).Emit(context.Background(), audit.AuditEvent{TenantID: "widgets"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	var refused *BarrierTenantLogNotSupportedError
	if _, _, err := logs.ResolveAuditInstance(context.Background(), "widgets"); !errors.As(err, &refused) {
		t.Fatalf("ResolveAuditInstance(widgets) = %v, want *BarrierTenantLogNotSupportedError", err)
	}
	if host, _, err := logs.ResolveAuditShard(context.Background(), "widgets"); err != nil || host != homes.host {
		t.Fatalf("ResolveAuditShard(widgets) = %v, %v; want the home group", host, err)
	}
}
