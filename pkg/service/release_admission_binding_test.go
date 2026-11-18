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
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

const bindingSystemShard = 1

// bindingHost is an AdminHost over one system shard whose membership is
// both the feature gate's MembershipSource and the shard go-quicraft
// changes. Its RequestAddNodeWithIdentity models go-quicraft's ordering
// check: a non-zero configChangeIndex that is not the shard's current
// configuration change ID is refused with ErrStaleConfigChange; 0 skips the
// check; an accepted add commits and advances the ID.
type bindingHost struct {
	*mockHost
	gate *featuregate.Gate

	mu             sync.Mutex
	members        map[uint64]string
	configChangeID uint64
	indexes        []uint64

	// pauseAddr's first admission blocks after the gate admits it until
	// resume is closed; admitted is closed when it reaches the pause.
	pauseAddr string
	paused    bool
	admitted  chan struct{}
	resume    chan struct{}
}

var (
	_ AdminHost                    = (*bindingHost)(nil)
	_ configBoundAdmitter          = (*bindingHost)(nil)
	_ featuregate.MembershipSource = (*bindingHost)(nil)
)

func (h *bindingHost) GroupMembership(shardID uint64) (featuregate.GroupMembership, error) {
	if shardID != bindingSystemShard {
		return featuregate.GroupMembership{}, errors.New("shard not hosted")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	replicas := make(map[uint64]string, len(h.members))
	for id, addr := range h.members {
		replicas[id] = addr
	}
	return featuregate.GroupMembership{ConfigChangeID: h.configChangeID, Replicas: replicas}, nil
}

func (h *bindingHost) AdmitReplica(shardID uint64, raftAddr string) error {
	_, err := h.AdmitReplicaAt(shardID, raftAddr)
	return err
}

func (h *bindingHost) AdmitReplicaAt(shardID uint64, raftAddr string) (uint64, error) {
	id, err := h.gate.AdmitReplicaAt(shardID, raftAddr)
	h.mu.Lock()
	pause := raftAddr == h.pauseAddr && !h.paused
	if pause {
		h.paused = true
	}
	h.mu.Unlock()
	if pause {
		close(h.admitted)
		<-h.resume
	}
	return id, err
}

func (h *bindingHost) RequestAddNodeWithIdentity(_ context.Context, shardID, replicaID uint64, addr, _ string, configChangeIndex uint64) (*quicraft.RequestState, error) {
	if shardID != bindingSystemShard {
		return nil, errors.New("shard not hosted")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.indexes = append(h.indexes, configChangeIndex)
	if configChangeIndex != 0 && configChangeIndex != h.configChangeID {
		return nil, quicraft.ErrStaleConfigChange
	}
	h.members[replicaID] = addr
	h.configChangeID++
	return newCompletedRequestState(quicraft.RequestResult{}), nil
}

func (h *bindingHost) member(replicaID uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.members[replicaID]
	return ok
}

// newBindingHost is an all-0.59 three-member system shard with candidates
// advertising before membership.
func newBindingHost(t *testing.T, candidates ...cluster.Member) *bindingHost {
	t.Helper()
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	h := &bindingHost{
		mockHost:       &mockHost{},
		members:        map[uint64]string{1: "n1:1", 2: "n2:1", 3: "n3:1"},
		configChangeID: 7,
		admitted:       make(chan struct{}),
		resume:         make(chan struct{}),
	}
	gate, err := featuregate.New(featuregate.Config{
		Self:          featuregate.Self{ReplicaID: 1},
		SystemShardID: bindingSystemShard,
		Members:       h,
	})
	if err != nil {
		t.Fatalf("featuregate.New: %v", err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	adverts := fixedAdverts{
		{NodeID: 1, Address: "n1:1", ReleaseVersion: "0.59.0-alpha"},
		{NodeID: 2, Address: "n2:1", ReleaseVersion: "0.59.0-alpha"},
		{NodeID: 3, Address: "n3:1", ReleaseVersion: "0.59.0-alpha"},
	}
	adverts = append(adverts, candidates...)
	if err := gate.Start(context.Background(), noActivations{}, adverts); err != nil {
		t.Fatalf("gate.Start: %v", err)
	}
	h.gate = gate
	return h
}

// raceAdds adds first and second concurrently, second pausing after its
// admission until first has committed, and returns second's result.
func raceAdds(t *testing.T, h *bindingHost, first, second cluster.Member) error {
	t.Helper()
	svc := NewAdminService(h)
	h.pauseAddr = second.Address
	done := make(chan error, 1)
	go func() {
		done <- svc.AddMember(context.Background(), bindingSystemShard, second.NodeID, second.Address, "")
	}()
	select {
	case <-h.admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the second add never reached admission")
	}
	if err := svc.AddMember(context.Background(), bindingSystemShard, first.NodeID, first.Address, ""); err != nil {
		t.Fatalf("first AddMember = %v, want admitted", err)
	}
	close(h.resume)
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the second add never returned")
		return nil
	}
}

// Two concurrent adds of non-adjacent releases (0.58 and 0.60 into an
// all-0.59 cluster) both pass admission while neither is a member; the
// second, resumed after the first commits, is refused by go-quicraft as
// stale, admitted again against the new membership, and refused on the
// release gap.
func TestAdminService_AddMemberReleaseAdmissionBindsTheCheckedConfiguration(t *testing.T) {
	old := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.58.0-alpha"}
	next := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHost(t, old, next)

	err := raceAdds(t, h, old, next)
	var gap *release.GapError
	if !errors.As(err, &gap) {
		t.Fatalf("second AddMember = %v, want *release.GapError", err)
	}
	if !h.member(4) || h.member(5) {
		t.Fatalf("members = %v, want n4 added and n5 refused", h.members)
	}
	if len(h.indexes) != 2 || h.indexes[0] != 7 || h.indexes[1] != 7 {
		t.Fatalf("configChangeIndex per add = %v, want both bound to the admitted configuration 7", h.indexes)
	}
}

// A second add whose release is still adjacent to everything after the
// first commits is admitted again and retried once, bound to the new
// configuration.
func TestAdminService_AddMemberReleaseAdmissionRetriesOnceOnStale(t *testing.T) {
	a := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	b := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHost(t, a, b)

	if err := raceAdds(t, h, a, b); err != nil {
		t.Fatalf("second AddMember = %v, want admitted on the retry", err)
	}
	if !h.member(4) || !h.member(5) {
		t.Fatalf("members = %v, want n4 and n5", h.members)
	}
	want := []uint64{7, 7, 8}
	if len(h.indexes) != len(want) || h.indexes[0] != want[0] || h.indexes[1] != want[1] || h.indexes[2] != want[2] {
		t.Fatalf("configChangeIndex per add = %v, want %v", h.indexes, want)
	}
}

// A non-system shard's add is not bound: its configuration change index
// is a different counter, so admission returns 0.
func TestGate_AdmitReplicaAtBindsOnlyTheSystemShard(t *testing.T) {
	c := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHost(t, c)
	if id, err := h.gate.AdmitReplicaAt(bindingSystemShard, "n4:1"); err != nil || id != 7 {
		t.Fatalf("AdmitReplicaAt(system) = %d, %v; want 7, nil", id, err)
	}
	if id, err := h.gate.AdmitReplicaAt(900, "n4:1"); err != nil || id != 0 {
		t.Fatalf("AdmitReplicaAt(900) = %d, %v; want 0, nil", id, err)
	}
}
