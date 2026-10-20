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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// fakeAllocatorForReplicaFilter is a minimal volume.Allocator double:
// Resolve answers from a fixed map (or *volume.AssignmentNotFoundError for a
// miss); every other method panics (unused by newVolumePoolHealthFilter).
type fakeAllocatorForReplicaFilter struct {
	assignments map[string]volume.Assignment // tenantID -> Assignment
}

func (f *fakeAllocatorForReplicaFilter) Resolve(_ context.Context, tenantID string) (*volume.Assignment, error) {
	a, ok := f.assignments[tenantID]
	if !ok {
		return nil, &volume.AssignmentNotFoundError{TenantID: tenantID}
	}
	return &a, nil
}
func (f *fakeAllocatorForReplicaFilter) Reserve(context.Context, string, string, string, int64) (*volume.Assignment, error) {
	panic("unused")
}
func (f *fakeAllocatorForReplicaFilter) Release(context.Context, string) error { panic("unused") }
func (f *fakeAllocatorForReplicaFilter) ListAll(context.Context) ([]volume.Assignment, error) {
	panic("unused")
}
func (f *fakeAllocatorForReplicaFilter) AdjustReservation(context.Context, string, int64) (*volume.Assignment, error) {
	panic("unused")
}

// fakeVolumeServiceForReplicaFilter is a minimal volume.Service double:
// GetPool answers from a fixed map; every other method panics (unused by
// newVolumePoolHealthFilter).
type fakeVolumeServiceForReplicaFilter struct {
	pools map[string]*volume.PoolDetail // pool name -> detail
}

func (f *fakeVolumeServiceForReplicaFilter) GetPool(_ context.Context, name string) (*volume.PoolDetail, error) {
	d, ok := f.pools[name]
	if !ok {
		return nil, &volume.PoolNotFoundError{Pool: name}
	}
	return d, nil
}
func (f *fakeVolumeServiceForReplicaFilter) ListPools(context.Context) ([]volume.PoolView, error) {
	panic("unused")
}
func (f *fakeVolumeServiceForReplicaFilter) CreatePool(context.Context, volume.CreatePoolRequest) (*volume.Pool, error) {
	panic("unused")
}
func (f *fakeVolumeServiceForReplicaFilter) UpdatePool(context.Context, string, volume.UpdatePoolRequest) (*volume.Pool, error) {
	panic("unused")
}
func (f *fakeVolumeServiceForReplicaFilter) DeletePool(context.Context, string) error {
	panic("unused")
}
func (f *fakeVolumeServiceForReplicaFilter) ListAssignments(context.Context, string, string, int) (*volume.AssignmentPage, error) {
	panic("unused")
}
func (f *fakeVolumeServiceForReplicaFilter) MoveTenant(context.Context, string, string) (*volume.Move, error) {
	panic("unused")
}
func (f *fakeVolumeServiceForReplicaFilter) GetMove(context.Context, string) (*volume.Move, error) {
	panic("unused")
}

// TestVolumePoolHealthFilter_NoAssignmentPassesThrough proves a tenant with
// no catalog Assignment (prefix/barrier isolation, or the system tenant) is
// not this filter's concern: it is admitted without consulting the pool at
// all.
func TestVolumePoolHealthFilter_NoAssignmentPassesThrough(t *testing.T) {
	allocator := &fakeAllocatorForReplicaFilter{assignments: map[string]volume.Assignment{}}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{}}
	filter := newVolumePoolHealthFilter(allocator, volumeSvc, map[string]string{}, nil)

	if err := filter("tenant-no-assignment", 1, "10.0.0.1:7000"); err != nil {
		t.Fatalf("filter() = %v, want nil", err)
	}
}

// TestVolumePoolHealthFilter_ReadyNodeAdmits proves a tenant's replica is
// admitted on a node whose latest report shows the tenant's pool
// volume.NodeStateReady there.
func TestVolumePoolHealthFilter_ReadyNodeAdmits(t *testing.T) {
	allocator := &fakeAllocatorForReplicaFilter{assignments: map[string]volume.Assignment{
		"tenant-a": {TenantID: "tenant-a", Pool: "fast"},
	}}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{
		"fast": {NodeReports: []volume.NodeReportRow{
			{NodeID: "1", State: volume.NodeStateReady},
		}},
	}}
	filter := newVolumePoolHealthFilter(allocator, volumeSvc, map[string]string{"10.0.0.1:7000": "1"}, nil)

	if err := filter("tenant-a", 1, "10.0.0.1:7000"); err != nil {
		t.Fatalf("filter() = %v, want nil", err)
	}
}

// TestVolumePoolHealthFilter_NotReadyRefusesWithTypedError proves a tenant's
// replica is refused, with a typed *PoolNotHealthyOnNodeError naming the
// tenant, pool, node and reported state, when the node's latest report for
// the tenant's pool is anything other than ready.
func TestVolumePoolHealthFilter_NotReadyRefusesWithTypedError(t *testing.T) {
	allocator := &fakeAllocatorForReplicaFilter{assignments: map[string]volume.Assignment{
		"tenant-a": {TenantID: "tenant-a", Pool: "fast"},
	}}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{
		"fast": {NodeReports: []volume.NodeReportRow{
			{NodeID: "1", State: volume.NodeStateUnavailable},
		}},
	}}
	filter := newVolumePoolHealthFilter(allocator, volumeSvc, map[string]string{"10.0.0.1:7000": "1"}, nil)

	err := filter("tenant-a", 1, "10.0.0.1:7000")
	var target *PoolNotHealthyOnNodeError
	if !errors.As(err, &target) {
		t.Fatalf("filter() = %v, want *PoolNotHealthyOnNodeError", err)
	}
	if target.TenantID != "tenant-a" || target.Pool != "fast" || target.NodeID != "1" || target.State != volume.NodeStateUnavailable {
		t.Fatalf("PoolNotHealthyOnNodeError = %+v, want TenantID=tenant-a Pool=fast NodeID=1 State=unavailable", target)
	}
}

// TestVolumePoolHealthFilter_NoReportForNodeRefuses proves a node that has
// never reported the tenant's pool at all (no NodeReportRow for it) is
// refused with *PoolNotHealthyOnNodeError and an empty State, distinct from
// a report that names an explicit non-ready state.
func TestVolumePoolHealthFilter_NoReportForNodeRefuses(t *testing.T) {
	allocator := &fakeAllocatorForReplicaFilter{assignments: map[string]volume.Assignment{
		"tenant-a": {TenantID: "tenant-a", Pool: "fast"},
	}}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{
		"fast": {NodeReports: []volume.NodeReportRow{
			{NodeID: "2", State: volume.NodeStateReady},
		}},
	}}
	filter := newVolumePoolHealthFilter(allocator, volumeSvc, map[string]string{"10.0.0.1:7000": "1"}, nil)

	err := filter("tenant-a", 1, "10.0.0.1:7000")
	var target *PoolNotHealthyOnNodeError
	if !errors.As(err, &target) {
		t.Fatalf("filter() = %v, want *PoolNotHealthyOnNodeError", err)
	}
	if target.State != "" {
		t.Fatalf("PoolNotHealthyOnNodeError.State = %q, want empty", target.State)
	}
}

// TestVolumePoolHealthFilter_UnknownRaftAddrFailsClosed proves a candidate
// raftAddr absent from the snapshot is refused with *UnknownRaftAddrError
// rather than treated as healthy -- this filter fails closed on an address
// it cannot verify.
func TestVolumePoolHealthFilter_UnknownRaftAddrFailsClosed(t *testing.T) {
	allocator := &fakeAllocatorForReplicaFilter{assignments: map[string]volume.Assignment{
		"tenant-a": {TenantID: "tenant-a", Pool: "fast"},
	}}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{}}
	filter := newVolumePoolHealthFilter(allocator, volumeSvc, map[string]string{}, nil)

	err := filter("tenant-a", 1, "10.0.0.1:7000")
	var target *UnknownRaftAddrError
	if !errors.As(err, &target) {
		t.Fatalf("filter() = %v, want *UnknownRaftAddrError", err)
	}
	if target.RaftAddr != "10.0.0.1:7000" {
		t.Fatalf("UnknownRaftAddrError.RaftAddr = %q, want 10.0.0.1:7000", target.RaftAddr)
	}
}

// fixedMembership returns a systemMembership that answers m (or err) and
// counts its reads, so a test can tell whether the snapshot short-circuited.
func fixedMembership(m *quicraft.Membership, err error, reads *int) systemMembership {
	return func(context.Context) (*quicraft.Membership, error) {
		*reads++
		return m, err
	}
}

// TestVolumePoolHealthFilter_JoinedNodeResolvedFromMembership is the #812
// three-node case: a node admitted with `admin member add` after the
// founders booted is absent from their --peers snapshot, and is admitted
// on its pool report because the system shard's membership names it.
func TestVolumePoolHealthFilter_JoinedNodeResolvedFromMembership(t *testing.T) {
	allocator := &fakeAllocatorForReplicaFilter{assignments: map[string]volume.Assignment{
		"tenant-a": {TenantID: "tenant-a", Pool: "fast"},
	}}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{
		"fast": {NodeReports: []volume.NodeReportRow{
			{NodeID: "3", State: volume.NodeStateReady},
		}},
	}}
	var reads int
	members := fixedMembership(&quicraft.Membership{Nodes: map[uint64]string{
		1: "10.0.0.1:7000", 2: "10.0.0.2:7000", 3: "10.0.0.3:7000",
	}}, nil, &reads)
	filter := newVolumePoolHealthFilter(allocator, volumeSvc,
		map[string]string{"10.0.0.1:7000": "1", "10.0.0.2:7000": "2"}, members)

	if err := filter("tenant-a", 1, "10.0.0.3:7000"); err != nil {
		t.Fatalf("filter() = %v, want nil", err)
	}
	if reads != 1 {
		t.Fatalf("membership reads = %d, want 1", reads)
	}
}

// TestVolumePoolHealthFilter_JoinedNodeNotReadyRefuses keeps the pool check
// in force for a membership-resolved node: resolving the address is not
// admitting it.
func TestVolumePoolHealthFilter_JoinedNodeNotReadyRefuses(t *testing.T) {
	allocator := &fakeAllocatorForReplicaFilter{assignments: map[string]volume.Assignment{
		"tenant-a": {TenantID: "tenant-a", Pool: "fast"},
	}}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{
		"fast": {NodeReports: []volume.NodeReportRow{
			{NodeID: "3", State: volume.NodeStateLocked},
		}},
	}}
	var reads int
	members := fixedMembership(&quicraft.Membership{Nodes: map[uint64]string{3: "10.0.0.3:7000"}}, nil, &reads)
	filter := newVolumePoolHealthFilter(allocator, volumeSvc, map[string]string{"10.0.0.1:7000": "1"}, members)

	err := filter("tenant-a", 1, "10.0.0.3:7000")
	var target *PoolNotHealthyOnNodeError
	if !errors.As(err, &target) {
		t.Fatalf("filter() = %v, want *PoolNotHealthyOnNodeError", err)
	}
	if target.NodeID != "3" {
		t.Fatalf("PoolNotHealthyOnNodeError.NodeID = %q, want 3", target.NodeID)
	}
}

func TestResolveNodeID_SnapshotHitSkipsMembership(t *testing.T) {
	var reads int
	members := fixedMembership(nil, errors.New("must not be read"), &reads)

	got, err := resolveNodeID(context.Background(), "10.0.0.1:7000", map[string]string{"10.0.0.1:7000": "1"}, members)
	if err != nil {
		t.Fatalf("resolveNodeID() error = %v", err)
	}
	if got != "1" {
		t.Fatalf("resolveNodeID() = %q, want 1", got)
	}
	if reads != 0 {
		t.Fatalf("membership reads = %d, want 0", reads)
	}
}

func TestResolveNodeID_ObserverResolvedWitnessNot(t *testing.T) {
	var reads int
	members := fixedMembership(&quicraft.Membership{
		Observers: map[uint64]string{4: "10.0.0.4:7000"},
		Witnesses: map[uint64]string{5: "10.0.0.5:7000"},
	}, nil, &reads)

	got, err := resolveNodeID(context.Background(), "10.0.0.4:7000", nil, members)
	if err != nil || got != "4" {
		t.Fatalf("resolveNodeID(observer) = %q, %v, want 4, nil", got, err)
	}
	_, err = resolveNodeID(context.Background(), "10.0.0.5:7000", nil, members)
	var target *UnknownRaftAddrError
	if !errors.As(err, &target) {
		t.Fatalf("resolveNodeID(witness) error = %v, want *UnknownRaftAddrError", err)
	}
}

func TestResolveNodeID_MissInBothFailsClosed(t *testing.T) {
	cases := map[string]systemMembership{
		"no membership source": nil,
		"absent from membership": func(context.Context) (*quicraft.Membership, error) {
			return &quicraft.Membership{Nodes: map[uint64]string{1: "10.0.0.1:7000"}}, nil
		},
		"nil membership": func(context.Context) (*quicraft.Membership, error) {
			return nil, nil
		},
	}
	for name, members := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := resolveNodeID(context.Background(), "10.0.0.9:7000", map[string]string{}, members)
			var target *UnknownRaftAddrError
			if !errors.As(err, &target) {
				t.Fatalf("resolveNodeID() error = %v, want *UnknownRaftAddrError", err)
			}
			if target.RaftAddr != "10.0.0.9:7000" {
				t.Fatalf("UnknownRaftAddrError.RaftAddr = %q, want 10.0.0.9:7000", target.RaftAddr)
			}
		})
	}
}

func TestResolveNodeID_MembershipErrorPropagates(t *testing.T) {
	sentinel := errors.New("membership read failed")
	var reads int
	members := fixedMembership(nil, sentinel, &reads)

	_, err := resolveNodeID(context.Background(), "10.0.0.3:7000", nil, members)
	if !errors.Is(err, sentinel) {
		t.Fatalf("resolveNodeID() error = %v, want %v", err, sentinel)
	}
}

// TestVolumePoolHealthFilter_AllocatorErrorPropagates proves an allocator
// error other than *volume.AssignmentNotFoundError is returned verbatim,
// not swallowed as a pass-through.
func TestVolumePoolHealthFilter_AllocatorErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	allocator := &erroringAllocator{err: wantErr}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{}}
	filter := newVolumePoolHealthFilter(allocator, volumeSvc, map[string]string{}, nil)

	if err := filter("tenant-a", 1, "10.0.0.1:7000"); !errors.Is(err, wantErr) {
		t.Fatalf("filter() = %v, want %v", err, wantErr)
	}
}

// TestVolumePoolHealthFilter_PoolLookupErrorPropagates: a tenant assigned to
// a pool the catalog can no longer describe is refused with the catalog's own
// error. A placement the filter cannot judge is never admitted by default.
func TestVolumePoolHealthFilter_PoolLookupErrorPropagates(t *testing.T) {
	allocator := &fakeAllocatorForReplicaFilter{assignments: map[string]volume.Assignment{
		"tenant-a": {TenantID: "tenant-a", Pool: "retired"},
	}}
	volumeSvc := &fakeVolumeServiceForReplicaFilter{pools: map[string]*volume.PoolDetail{}}
	filter := newVolumePoolHealthFilter(allocator, volumeSvc, map[string]string{"10.0.0.1:7000": "1"}, nil)

	err := filter("tenant-a", 1, "10.0.0.1:7000")
	var notFound *volume.PoolNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("filter() = %v, want *volume.PoolNotFoundError", err)
	}
	if notFound.Pool != "retired" {
		t.Fatalf("PoolNotFoundError.Pool = %q, want retired", notFound.Pool)
	}
}

// TestPoolNotHealthyOnNodeError_Message: the refusal names the tenant, pool
// and node, and adds the node's reported state only when there is one -- a
// node with no report at all has no state to show.
func TestPoolNotHealthyOnNodeError_Message(t *testing.T) {
	tests := []struct {
		name string
		err  *PoolNotHealthyOnNodeError
		want string
	}{
		{
			name: "reported state",
			err:  &PoolNotHealthyOnNodeError{TenantID: "acme", Pool: "fast", NodeID: "2", State: volume.NodeStateUnavailable},
			want: "lifecycle: tenant acme's pool fast is not ready on node 2 (unavailable)",
		},
		{
			name: "no report",
			err:  &PoolNotHealthyOnNodeError{TenantID: "acme", Pool: "fast", NodeID: "2"},
			want: "lifecycle: tenant acme's pool fast is not ready on node 2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUnknownRaftAddrError_Message: the refusal names the raft address that
// did not resolve, which is what an operator needs to find the stale entry.
func TestUnknownRaftAddrError_Message(t *testing.T) {
	err := &UnknownRaftAddrError{RaftAddr: "10.0.0.9:7000"}
	want := "lifecycle: raft address 10.0.0.9:7000 does not resolve to a known node id"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// erroringAllocator is a volume.Allocator double whose Resolve always
// returns a fixed, non-AssignmentNotFoundError error.
type erroringAllocator struct {
	err error
}

func (f *erroringAllocator) Resolve(context.Context, string) (*volume.Assignment, error) {
	return nil, f.err
}
func (f *erroringAllocator) Reserve(context.Context, string, string, string, int64) (*volume.Assignment, error) {
	panic("unused")
}
func (f *erroringAllocator) Release(context.Context, string) error { panic("unused") }
func (f *erroringAllocator) ListAll(context.Context) ([]volume.Assignment, error) {
	panic("unused")
}
func (f *erroringAllocator) AdjustReservation(context.Context, string, int64) (*volume.Assignment, error) {
	panic("unused")
}
