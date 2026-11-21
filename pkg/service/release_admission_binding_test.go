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

func (h *bindingHost) CheckConfigBinding(shardID uint64, b featuregate.ConfigBinding) error {
	return h.gate.CheckConfigBinding(shardID, b)
}

func (h *bindingHost) AdmitReplicaAt(shardID uint64, raftAddr string) (featuregate.ConfigBinding, error) {
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

// RequestAddObserverWithIdentity applies the same stale-configuration check
// as RequestAddNodeWithIdentity: an observer is a member of the group's
// configuration, so adding one advances its change ID just as a voter does.
func (h *bindingHost) RequestAddObserverWithIdentity(ctx context.Context, shardID, replicaID uint64, addr, identity string, configChangeIndex uint64) (*quicraft.RequestState, error) {
	return h.RequestAddNodeWithIdentity(ctx, shardID, replicaID, addr, identity, configChangeIndex)
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
	return newBindingHostAt(t, 7, candidates...)
}

// newBindingHostAt is newBindingHost whose membership is at configuration
// change ID configChangeID: 0 is a bootstrapped membership before its first
// change.
func newBindingHostAt(t *testing.T, configChangeID uint64, candidates ...cluster.Member) *bindingHost {
	t.Helper()
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	h := &bindingHost{
		mockHost:       &mockHost{},
		members:        map[uint64]string{1: "n1:1", 2: "n2:1", 3: "n3:1"},
		configChangeID: configChangeID,
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
	return raceAddsVia(t, h, first, second, (*AdminService).AddMember, (*AdminService).AddMember)
}

// raceAddsVia is raceAdds with the admin call each addition goes through, so
// an observer addition can race a voter addition.
func raceAddsVia(t *testing.T, h *bindingHost, first, second cluster.Member, addFirst, addSecond func(*AdminService, context.Context, uint64, uint64, string, string) error) error {
	t.Helper()
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	h.pauseAddr = second.Address
	done := make(chan error, 1)
	go func() {
		done <- addSecond(svc, context.Background(), bindingSystemShard, second.NodeID, second.Address, "")
	}()
	select {
	case <-h.admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the second add never reached admission")
	}
	if err := addFirst(svc, context.Background(), bindingSystemShard, first.NodeID, first.Address, ""); err != nil {
		t.Fatalf("first add = %v, want admitted", err)
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

// TestAdminService_AddObserverMemberReleaseAdmissionBindsTheCheckedConfiguration
// pauses a 0.58 observer addition after admission in an all-0.59 cluster,
// commits a concurrent 0.60 voter addition, then resumes the observer: the
// observer addition is bound to the configuration admission checked, so it
// is refused stale, re-admitted against the new configuration and refused
// with the release gap (docs/SPEC.md non-adjacent releases).
func TestAdminService_AddObserverMemberReleaseAdmissionBindsTheCheckedConfiguration(t *testing.T) {
	voter := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	observer := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.58.0-alpha"}
	h := newBindingHost(t, voter, observer)

	err := raceAddsVia(t, h, voter, observer, (*AdminService).AddMember, (*AdminService).AddObserverMember)
	var gap *release.GapError
	if !errors.As(err, &gap) {
		t.Fatalf("AddObserverMember = %v, want *release.GapError", err)
	}
	if !h.member(4) || h.member(5) {
		t.Fatalf("members = %v, want voter n4 added and observer n5 refused", h.members)
	}
	if len(h.indexes) != 2 || h.indexes[0] != 7 || h.indexes[1] != 7 {
		t.Fatalf("configChangeIndex per add = %v, want both bound to the admitted configuration 7", h.indexes)
	}
}

// TestAdminService_AddObserverMemberReleaseAdmissionRetriesOnceOnStale races
// two same-release additions, the paused one an observer: its stale refusal
// is re-admitted against the new configuration and retried once, bound to it.
func TestAdminService_AddObserverMemberReleaseAdmissionRetriesOnceOnStale(t *testing.T) {
	voter := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	observer := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHost(t, voter, observer)

	if err := raceAddsVia(t, h, voter, observer, (*AdminService).AddMember, (*AdminService).AddObserverMember); err != nil {
		t.Fatalf("AddObserverMember = %v, want admitted on the retry", err)
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
	want := featuregate.ConfigBinding{ConfigChangeID: 7, Required: true}
	if b, err := h.gate.AdmitReplicaAt(bindingSystemShard, "n4:1"); err != nil || b != want {
		t.Fatalf("AdmitReplicaAt(system) = %+v, %v; want %+v, nil", b, err, want)
	}
	if b, err := h.gate.AdmitReplicaAt(900, "n4:1"); err != nil || b.Required {
		t.Fatalf("AdmitReplicaAt(900) = %+v, %v; want no binding, nil", b, err)
	}
}

// A bootstrapped system shard's configuration change ID is 0 until its
// first membership change. Admission against it is still bound -- Required,
// at ID 0 -- and the binding is compared: it holds while the configuration
// is the initial one and is stale once a change commits.
func TestGate_AdmitReplicaAtBindsTheInitialConfiguration(t *testing.T) {
	c := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHostAt(t, 0, c)
	b, err := h.gate.AdmitReplicaAt(bindingSystemShard, "n4:1")
	if want := (featuregate.ConfigBinding{ConfigChangeID: 0, Required: true}); err != nil || b != want {
		t.Fatalf("AdmitReplicaAt(system) = %+v, %v; want %+v, nil", b, err, want)
	}
	if err := h.gate.CheckConfigBinding(bindingSystemShard, b); err != nil {
		t.Fatalf("CheckConfigBinding on the initial configuration = %v, want nil", err)
	}
	h.mu.Lock()
	h.configChangeID = 1
	h.mu.Unlock()
	var stale *featuregate.StaleConfigurationError
	if err := h.gate.CheckConfigBinding(bindingSystemShard, b); !errors.As(err, &stale) || stale.Admitted != 0 || stale.Current != 1 {
		t.Fatalf("CheckConfigBinding after a change = %v, want *featuregate.StaleConfigurationError 0 -> 1", err)
	}
	if err := h.gate.CheckConfigBinding(bindingSystemShard, featuregate.ConfigBinding{}); err != nil {
		t.Fatalf("CheckConfigBinding without a binding = %v, want nil", err)
	}
}

// From the initial configuration (change ID 0) two concurrent adds of
// non-adjacent releases (0.58 and 0.60 into an all-0.59 cluster) are still
// bound to the configuration admission checked: go-quicraft skips its own
// comparison for index 0, so the binding is compared before the proposal,
// the paused add is admitted again against the new configuration and
// refused on the release gap.
func TestAdminService_AddMemberReleaseAdmissionBindsTheInitialConfiguration(t *testing.T) {
	old := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.58.0-alpha"}
	next := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHostAt(t, 0, old, next)

	err := raceAdds(t, h, old, next)
	var gap *release.GapError
	if !errors.As(err, &gap) {
		t.Fatalf("second AddMember = %v, want *release.GapError", err)
	}
	if !h.member(4) || h.member(5) {
		t.Fatalf("members = %v, want n4 added and n5 refused", h.members)
	}
	if len(h.indexes) != 1 || h.indexes[0] != 0 {
		t.Fatalf("configChangeIndex per add = %v, want only the first add, at the initial configuration 0", h.indexes)
	}
}

// The observer form of the initial-configuration race: a 0.58 observer
// paused after admission against configuration 0 is refused once a 0.60
// voter commits.
func TestAdminService_AddObserverMemberReleaseAdmissionBindsTheInitialConfiguration(t *testing.T) {
	voter := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	observer := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.58.0-alpha"}
	h := newBindingHostAt(t, 0, voter, observer)

	err := raceAddsVia(t, h, voter, observer, (*AdminService).AddMember, (*AdminService).AddObserverMember)
	var gap *release.GapError
	if !errors.As(err, &gap) {
		t.Fatalf("AddObserverMember = %v, want *release.GapError", err)
	}
	if !h.member(4) || h.member(5) {
		t.Fatalf("members = %v, want voter n4 added and observer n5 refused", h.members)
	}
	if len(h.indexes) != 1 || h.indexes[0] != 0 {
		t.Fatalf("configChangeIndex per add = %v, want only the voter add, at the initial configuration 0", h.indexes)
	}
}

// TestAdminService_InitialConfigurationSecondNonAdjacentAddRefused starts the
// system shard at the initial configuration (change ID 0) in an all-0.59
// cluster, commits a 0.60 voter, then lets a 0.58 voter that was admitted
// against the initial configuration resume: the binding to change ID 0 must
// refuse it with the release-gap error and leave only the 0.60 member added.
func TestAdminService_InitialConfigurationSecondNonAdjacentAddRefused(t *testing.T) {
	next := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	old := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.58.0-alpha"}
	h := newBindingHostAt(t, 0, next, old)

	err := raceAdds(t, h, next, old)
	var gap *release.GapError
	if !errors.As(err, &gap) {
		t.Fatalf("second AddMember = %v, want *release.GapError", err)
	}
	if !h.member(4) || h.member(5) {
		t.Fatalf("members = %v, want n4 added and n5 refused", h.members)
	}
	if len(h.indexes) != 1 || h.indexes[0] != 0 {
		t.Fatalf("configChangeIndex per add = %v, want only the first add, at the initial configuration 0", h.indexes)
	}
}
