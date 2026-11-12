package placement

import (
	"context"
	"errors"
	"testing"
)

// servingNodes is a five-node cluster in which the isolated tenant "iso"
// is served by n1 and n2 only (its home group's replicas), and the shared
// tenant "shared" is not confined at all.
func servingNodes() []NodeLoad {
	return []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1", Bytes: 900, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2", Bytes: 800, Healthy: true},
		{NodeID: "n3", RaftAddr: "n3", Bytes: 1, Healthy: true},
		{NodeID: "n4", RaftAddr: "n4", Bytes: 2, Healthy: true},
		{NodeID: "n5", RaftAddr: "n5", Bytes: 3, Healthy: true},
	}
}

func servingSetOf(_ context.Context, tenantID, _ string) ([]string, bool, error) {
	if tenantID == "iso" {
		return []string{"n1", "n2"}, true, nil
	}
	return nil, false, nil
}

func newServingScheduler(t *testing.T) *Scheduler {
	t.Helper()
	s, err := NewScheduler(&mockTopology{nodes: servingNodes()}, WithServingSet(servingSetOf))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	return s
}

// TestPlace_ServingSetConfinesIsolatedTenant: the lightly loaded n3-n5
// would win on load, but an isolated tenant's group lands only on n1, n2.
func TestPlace_ServingSetConfinesIsolatedTenant(t *testing.T) {
	s := newServingScheduler(t)
	p, err := s.Place(context.Background(), PlaceRequest{TenantID: "iso", DatabaseID: "db-id", Database: "db", ShardID: 7, RF: 2})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	for _, r := range p.Replicas {
		if r.NodeID != "n1" && r.NodeID != "n2" {
			t.Fatalf("replica on %s, outside the serving set {n1,n2}", r.NodeID)
		}
	}
}

func TestPlace_ServingSetRefusesRFBeyondServingSet(t *testing.T) {
	s := newServingScheduler(t)
	_, err := s.Place(context.Background(), PlaceRequest{TenantID: "iso", DatabaseID: "db-id", Database: "db", ShardID: 7, RF: 3})
	var sse *ServingSetError
	if !errors.As(err, &sse) {
		t.Fatalf("Place RF 3 over a 2-node serving set: err = %v, want *ServingSetError", err)
	}
	if sse.TenantID != "iso" || sse.Database != "db" || sse.Have != 2 || sse.Need != 3 {
		t.Fatalf("ServingSetError = %+v", sse)
	}
}

func TestPlaceAdditional_ServingSetRefusesScaleOutBeyondServingSet(t *testing.T) {
	s := newServingScheduler(t)
	existing := &Placement{
		TenantID: "iso", DatabaseID: "db-id", Database: "db", ShardID: 7, RF: 2,
		Replicas: []Replica{{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1"}, {ReplicaID: 2, NodeID: "n2", RaftAddr: "n2"}},
	}
	_, err := s.PlaceAdditional(context.Background(), existing, 1)
	var sse *ServingSetError
	if !errors.As(err, &sse) {
		t.Fatalf("PlaceAdditional beyond the serving set: err = %v, want *ServingSetError", err)
	}
	if sse.Have != 0 || sse.Need != 1 {
		t.Fatalf("ServingSetError = %+v", sse)
	}
}

func TestPlace_ServingSetLeavesSharedTenantUnconfined(t *testing.T) {
	s := newServingScheduler(t)
	p, err := s.Place(context.Background(), PlaceRequest{TenantID: "shared", DatabaseID: "db-id", Database: "db", ShardID: 7, RF: 3})
	if err != nil {
		t.Fatalf("Place shared tenant: %v", err)
	}
	for _, r := range p.Replicas {
		if r.NodeID == "n1" || r.NodeID == "n2" {
			t.Fatalf("shared tenant's RF 3 placed on loaded %s; want the three lightly loaded nodes", r.NodeID)
		}
	}
}

func TestPlace_ServingSetLookupErrorFailsClosed(t *testing.T) {
	boom := errors.New("home placement unreadable")
	s, err := NewScheduler(&mockTopology{nodes: servingNodes()}, WithServingSet(
		func(context.Context, string, string) ([]string, bool, error) { return nil, false, boom }))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if _, err := s.Place(context.Background(), PlaceRequest{TenantID: "iso", DatabaseID: "db-id", Database: "db", ShardID: 7, RF: 1}); !errors.Is(err, boom) {
		t.Fatalf("Place with a failing serving-set lookup: err = %v, want %v", err, boom)
	}
}

func TestServingSetError_CheckNode(t *testing.T) {
	err := CheckServingSet([]string{"n1", "n2"}, "iso", "db", "n3")
	var sse *ServingSetError
	if !errors.As(err, &sse) || sse.Node != "n3" {
		t.Fatalf("CheckServingSet(n3) = %v, want *ServingSetError naming n3", err)
	}
	if err := CheckServingSet([]string{"n1", "n2"}, "iso", "db", "n2"); err != nil {
		t.Fatalf("CheckServingSet(n2) = %v, want nil", err)
	}
}
