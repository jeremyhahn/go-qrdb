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
	"runtime"
	"slices"
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
	gate    *featuregate.Gate
	adverts *mutableAdverts

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

	// pauseRequestAddr's first add request blocks before the stale check
	// until resumeRequest is closed; requested is closed when it reaches
	// the pause. That is go-quicraft's window between Host.requestConfigChange's
	// check and the Raft worker, which compares no ConfigChangeID.
	pauseRequestAddr string
	requestPaused    bool
	requested        chan struct{}
	resumeRequest    chan struct{}

	// ambiguousAddr's first add request reaches raft but its wait times out
	// with the outcome unknown (ambiguousResult): it commits only when
	// commitAmbiguous runs, which then delivers its result. entered is
	// signalled each time an add enters ProposeBound.
	ambiguousAddr      string
	ambiguousRS        *quicraft.RequestState
	ambiguousReplica   uint64
	ambiguousDelivered chan quicraft.RequestResult
	entered            chan struct{}
	// ambiguousSettled is set once the ambiguous add committed or its
	// proposal was lost; until then a re-proposal is refused as a second
	// configuration change in flight (inFlightRefusals counts them).
	ambiguousSettled bool
	inFlightRefusals int
	membershipReads  int
}

// ambiguousResult is the result go-quicraft reports for an add whose wait
// (RequestState.Result's deadline, or the pending-proposal sweeper) ended
// after it was handed to raft: a *quicraft.MaybeAppliedError wrapping
// quicraft.ErrTimeout. go-quicraft completes a RequestState once, and that
// one completion is what Result consumed, so ResultC never delivers again (c
// is never sent on). go-quicraft exports no way to build such a
// RequestState, so the test substitutes it through AdminService.resultOf.
type ambiguousResult struct {
	c chan quicraft.RequestResult
}

func (r ambiguousResult) Result() (quicraft.RequestResult, error) {
	return quicraft.RequestResult{}, &quicraft.MaybeAppliedError{Err: quicraft.ErrTimeout}
}

func (r ambiguousResult) ResultC() <-chan quicraft.RequestResult { return r.c }

// resultOf substitutes ambiguousResult for the ambiguous add's request state.
func (h *bindingHost) resultOf(rs *quicraft.RequestState) membershipResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	if rs == h.ambiguousRS {
		return ambiguousResult{c: h.ambiguousDelivered}
	}
	return rs
}

// commitAmbiguous commits the ambiguous add late and delivers its result.
func (h *bindingHost) commitAmbiguous() {
	h.mu.Lock()
	h.members[h.ambiguousReplica] = h.ambiguousAddr
	h.configChangeID++
	h.ambiguousSettled = true
	h.mu.Unlock()
}

// dropAmbiguous loses the ambiguous add's proposal (a leader change dropped
// the uncommitted entry): nothing is pending any more, and a re-proposal of
// the add is handled like any other.
func (h *bindingHost) dropAmbiguous() {
	h.mu.Lock()
	h.ambiguousSettled = true
	h.mu.Unlock()
}

// SyncGetShardMembership is the linearizable read of the committed
// membership: the members and the configuration change ID.
func (h *bindingHost) SyncGetShardMembership(_ context.Context, shardID uint64) (*quicraft.Membership, error) {
	if shardID != bindingSystemShard {
		return nil, errors.New("shard not hosted")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.membershipReads++
	m := &quicraft.Membership{ConfigChangeID: h.configChangeID, Nodes: map[uint64]string{}}
	for id, addr := range h.members {
		m.Nodes[id] = addr
	}
	return m, nil
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

func (h *bindingHost) ProposeBound(shardID uint64, b featuregate.ConfigBinding, propose func(<-chan struct{}) (<-chan struct{}, error)) error {
	if h.entered != nil {
		h.entered <- struct{}{}
	}
	return h.gate.ProposeBound(shardID, b, propose)
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
	pause := addr == h.pauseRequestAddr && !h.requestPaused
	if pause {
		h.requestPaused = true
	}
	h.mu.Unlock()
	if pause {
		close(h.requested)
		<-h.resumeRequest
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ambiguousRS != nil && !h.ambiguousSettled {
		// The ambiguous add is still in raft: the host's stale check runs,
		// then the worker refuses a second configuration change in flight.
		if configChangeIndex != 0 && configChangeIndex != h.configChangeID {
			return nil, quicraft.ErrStaleConfigChange
		}
		h.inFlightRefusals++
		return nil, quicraft.ErrRejected
	}
	h.indexes = append(h.indexes, configChangeIndex)
	if configChangeIndex != 0 && configChangeIndex != h.configChangeID {
		return nil, quicraft.ErrStaleConfigChange
	}
	if addr == h.ambiguousAddr && h.ambiguousRS == nil {
		// Handed to raft; it commits later (commitAmbiguous).
		h.ambiguousRS = newCompletedRequestState(quicraft.RequestResult{})
		h.ambiguousReplica = replicaID
		return h.ambiguousRS, nil
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
	adverts := []cluster.Member{
		{NodeID: 1, Address: "n1:1", ReleaseVersion: "0.59.0-alpha"},
		{NodeID: 2, Address: "n2:1", ReleaseVersion: "0.59.0-alpha"},
		{NodeID: 3, Address: "n3:1", ReleaseVersion: "0.59.0-alpha"},
	}
	h.adverts = &mutableAdverts{members: append(adverts, candidates...)}
	if err := gate.Start(context.Background(), noActivations{}, h.adverts); err != nil {
		t.Fatalf("gate.Start: %v", err)
	}
	h.gate = gate
	return h
}

// mutableAdverts is a fixed advertisement list a test can change, as a
// member restart on another release does, without a configuration change.
type mutableAdverts struct {
	mu      sync.Mutex
	members []cluster.Member
}

func (*mutableAdverts) PutMember(context.Context, uint64, []byte, time.Duration) error { return nil }
func (*mutableAdverts) DeleteMember(context.Context, uint64) error                     { return nil }
func (*mutableAdverts) WatchMembers(context.Context) (<-chan cluster.MemberEvent, error) {
	return nil, errors.New("no watch")
}

func (a *mutableAdverts) ListMembers(context.Context) ([]cluster.Member, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.members), nil
}

// restart makes member id advertise rel.
func (a *mutableAdverts) restart(id uint64, rel string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.members {
		if a.members[i].NodeID == id {
			a.members[i].ReleaseVersion = rel
		}
	}
}

// rollbackDuringAdmission adds candidate through add, rolling member 2 back
// to 0.58 after the gate admitted the candidate and before the add is
// requested, and returns the add's result.
func rollbackDuringAdmission(t *testing.T, h *bindingHost, candidate cluster.Member, add func(*AdminService, context.Context, uint64, uint64, string, string) error) error {
	t.Helper()
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	h.pauseAddr = candidate.Address
	done := make(chan error, 1)
	go func() {
		done <- add(svc, context.Background(), bindingSystemShard, candidate.NodeID, candidate.Address, "")
	}()
	select {
	case <-h.admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the add never reached admission")
	}
	h.adverts.restart(2, "0.58.0-alpha")
	close(h.resume)
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the add never returned")
		return nil
	}
}

func assertRollbackRefused(t *testing.T, h *bindingHost, err error) {
	t.Helper()
	var gap *release.GapError
	if !errors.As(err, &gap) || gap.Local != "0.58.0-alpha" || gap.Peer != "0.60.0-alpha" {
		t.Fatalf("add = %v, want the 0.58/0.60 release-gap refusal", err)
	}
	if h.member(4) {
		t.Fatal("n4 was added across the release gap")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.indexes) != 0 {
		t.Fatalf("adds requested = %v, want none", h.indexes)
	}
}

func TestAdminService_AddMemberRefusedAfterMemberRollbackDuringAdmission(t *testing.T) {
	candidate := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHost(t, candidate)
	assertRollbackRefused(t, h, rollbackDuringAdmission(t, h, candidate, (*AdminService).AddMember))
}

func TestAdminService_AddObserverMemberRefusedAfterMemberRollbackDuringAdmission(t *testing.T) {
	candidate := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHost(t, candidate)
	assertRollbackRefused(t, h, rollbackDuringAdmission(t, h, candidate, (*AdminService).AddObserverMember))
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
	// The second add's binding to 7 is refused by the gate's
	// CheckConfigBinding before any request; only the first add, bound to
	// 7, and none of the refused one reach go-quicraft.
	if len(h.indexes) != 1 || h.indexes[0] != 7 {
		t.Fatalf("configChangeIndex per add = %v, want only the first add, bound to the admitted configuration 7", h.indexes)
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
	// The stale binding to 7 is refused by the gate before a request; the
	// retry, admitted again, is bound to 8.
	want := []uint64{7, 8}
	if !slices.Equal(h.indexes, want) {
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
	// The second add's binding to 7 is refused by the gate's
	// CheckConfigBinding before any request; only the first add, bound to
	// 7, and none of the refused one reach go-quicraft.
	if len(h.indexes) != 1 || h.indexes[0] != 7 {
		t.Fatalf("configChangeIndex per add = %v, want only the first add, bound to the admitted configuration 7", h.indexes)
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
	// The stale binding to 7 is refused by the gate before a request; the
	// retry, admitted again, is bound to 8.
	want := []uint64{7, 8}
	if !slices.Equal(h.indexes, want) {
		t.Fatalf("configChangeIndex per add = %v, want %v", h.indexes, want)
	}
}

// A non-system shard's add is not bound: its configuration change index
// is a different counter, so admission returns 0.
func TestGate_AdmitReplicaAtBindsOnlyTheSystemShard(t *testing.T) {
	c := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHost(t, c)
	want := featuregate.ConfigBinding{ConfigChangeID: 7, Required: true}
	if b, err := h.gate.AdmitReplicaAt(bindingSystemShard, "n4:1"); err != nil || b.ConfigChangeID != want.ConfigChangeID || !b.Required {
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
	if want := (featuregate.ConfigBinding{ConfigChangeID: 0, Required: true}); err != nil || b.ConfigChangeID != want.ConfigChangeID || !b.Required {
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

// serializedBoundAdds runs two adds of non-adjacent releases into an
// all-0.59 cluster at the initial configuration (change ID 0, which
// go-quicraft does not compare). The first add, of old, is paused inside its
// request, after its recheck passed and before it commits: go-quicraft's
// window between the host check and the Raft worker. The second add, of
// next, is admitted on that same configuration while the first is paused.
// Only then is the first add released. It returns both results.
func serializedBoundAdds(t *testing.T, h *bindingHost, old, next cluster.Member,
	add func(*AdminService, context.Context, uint64, uint64, string, string) error) (first, second error) {
	t.Helper()
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	h.pauseRequestAddr = old.Address
	h.requested = make(chan struct{})
	h.resumeRequest = make(chan struct{})
	h.pauseAddr = next.Address
	close(h.resume)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- add(svc, context.Background(), bindingSystemShard, old.NodeID, old.Address, "")
	}()
	select {
	case <-h.requested:
	case <-time.After(10 * time.Second):
		t.Fatal("the first add never reached its request")
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- add(svc, context.Background(), bindingSystemShard, next.NodeID, next.Address, "")
	}()
	select {
	case <-h.admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the second add never reached admission")
	}
	close(h.resumeRequest)
	for i := 0; i < 2; i++ {
		select {
		case first = <-firstDone:
		case second = <-secondDone:
		case <-time.After(10 * time.Second):
			t.Fatal("an add never returned")
		}
	}
	return first, second
}

// assertSerializedBoundAdds requires the first add committed and the second
// refused with the release gap, with only the first add's request proposed.
func assertSerializedBoundAdds(t *testing.T, h *bindingHost, first, second error) {
	t.Helper()
	if first != nil {
		t.Fatalf("first add = %v, want committed", first)
	}
	var gap *release.GapError
	if !errors.As(second, &gap) {
		t.Fatalf("second add = %v, want *release.GapError", second)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, n4 := h.members[4]
	_, n5 := h.members[5]
	if n4 || !n5 {
		t.Fatalf("members = %v, want n5 added and n4 refused", h.members)
	}
	if len(h.indexes) != 1 || h.indexes[0] != 0 {
		t.Fatalf("configChangeIndex per request = %v, want only the first add's, at the initial configuration 0", h.indexes)
	}
}

// TestAdminService_AddMemberBoundAddsAreSerializedThroughCommit: a bound
// system-shard add holds the gate's lock from its recheck until its request
// returns, so a concurrent add admitted on the same configuration rechecks
// only after the first commits, is refused as stale, re-admits and gets the
// release gap. Without the lock, the second add's recheck passes while the
// first is in flight and both commit at change ID 0.
func TestAdminService_AddMemberBoundAddsAreSerializedThroughCommit(t *testing.T) {
	old := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.58.0-alpha"}
	next := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHostAt(t, 0, old, next)

	first, second := serializedBoundAdds(t, h, old, next, (*AdminService).AddMember)
	assertSerializedBoundAdds(t, h, first, second)
}

// TestAdminService_AddObserverMemberBoundAddsAreSerializedThroughCommit: an
// observer add shares the voter add's lock, so a voter add admitted while an
// observer add is in flight is refused with the release gap the same way.
func TestAdminService_AddObserverMemberBoundAddsAreSerializedThroughCommit(t *testing.T) {
	old := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.58.0-alpha"}
	next := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHostAt(t, 0, old, next)

	svcAdd := func(svc *AdminService, ctx context.Context, shardID, replicaID uint64, addr, identity string) error {
		if addr == old.Address {
			return svc.AddObserverMember(ctx, shardID, replicaID, addr, identity)
		}
		return svc.AddMember(ctx, shardID, replicaID, addr, identity)
	}
	first, second := serializedBoundAdds(t, h, old, next, svcAdd)
	assertSerializedBoundAdds(t, h, first, second)
}

// ambiguousBoundAdds adds old into h with a wait that times out ambiguously,
// then starts adding next. It asserts that next is not admitted (requested)
// while old's outcome is unknown, commits old late, and returns both errors.
func ambiguousBoundAdds(t *testing.T, h *bindingHost, old, next cluster.Member,
	add func(*AdminService, context.Context, uint64, uint64, string, string) error) (first, second error) {
	t.Helper()
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	svc.resultOf = h.resultOf
	h.ambiguousAddr = old.Address
	h.ambiguousDelivered = make(chan quicraft.RequestResult, 1)
	h.entered = make(chan struct{}, 2)
	close(h.resume)

	first = add(svc, context.Background(), bindingSystemShard, old.NodeID, old.Address, "")
	<-h.entered
	if h.member(old.NodeID) {
		t.Fatal("the ambiguous add committed before commitAmbiguous")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- add(svc, context.Background(), bindingSystemShard, next.NodeID, next.Address, "")
	}()
	select {
	case <-h.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the second add never reached ProposeBound")
	}
	select {
	case err := <-secondDone:
		t.Fatalf("the second add returned %v while the first add's outcome was unknown", err)
	default:
	}
	h.mu.Lock()
	requested := len(h.indexes)
	h.mu.Unlock()
	if requested != 1 {
		t.Fatalf("add requests = %d while the first add's outcome was unknown, want 1", requested)
	}

	h.commitAmbiguous()
	select {
	case second = <-secondDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the second add never returned after the first committed")
	}
	return first, second
}

// assertAmbiguousBoundAdds checks that the ambiguous add surfaced its timeout,
// the second add was refused with the release gap once the first committed,
// and only the first candidate became a member.
func assertAmbiguousBoundAdds(t *testing.T, h *bindingHost, first, second error) {
	t.Helper()
	if first == nil {
		t.Fatal("ambiguous add = nil, want its timeout error")
	}
	var gap *release.GapError
	if !errors.As(second, &gap) {
		t.Fatalf("second add = %v, want *release.GapError", second)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, n4 := h.members[4]
	_, n5 := h.members[5]
	if n4 || !n5 {
		t.Fatalf("members = %v, want n5 added and n4 refused", h.members)
	}
	if len(h.indexes) != 1 {
		t.Fatalf("configChangeIndex per request = %v, want only the ambiguous add's", h.indexes)
	}
}

// A 0.58 add whose wait times out with its outcome unknown (go-quicraft does
// not cancel it) keeps this node's proposal lock until it commits, so a 0.60
// add into the all-0.59 cluster is not admitted against the membership
// before it and is refused with the release gap once it commits.
func TestAdminService_AddMemberAmbiguousTimeoutStaysFenced(t *testing.T) {
	old := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.58.0-alpha"}
	next := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHostAt(t, 0, old, next)

	first, second := ambiguousBoundAdds(t, h, old, next, (*AdminService).AddMember)
	assertAmbiguousBoundAdds(t, h, first, second)
}

// AddObserverMember keeps an ambiguous add fenced exactly as AddMember does.
func TestAdminService_AddObserverMemberAmbiguousTimeoutStaysFenced(t *testing.T) {
	old := cluster.Member{NodeID: 5, Address: "n5:1", ReleaseVersion: "0.58.0-alpha"}
	next := cluster.Member{NodeID: 4, Address: "n4:1", ReleaseVersion: "0.60.0-alpha"}
	h := newBindingHostAt(t, 0, old, next)

	first, second := ambiguousBoundAdds(t, h, old, next, (*AdminService).AddObserverMember)
	assertAmbiguousBoundAdds(t, h, first, second)
}

// resultStub is a membershipResult with a fixed Result error and ResultC.
type resultStub struct {
	err error
	c   chan quicraft.RequestResult
}

func (r resultStub) Result() (quicraft.RequestResult, error) { return quicraft.RequestResult{}, r.err }

func (r resultStub) ResultC() <-chan quicraft.RequestResult {
	if r.c == nil {
		return nil
	}
	return r.c
}

// awaitAdd reports a pending outcome only for a timeout after the add reached
// raft; that pending closes once the result is delivered, or at once when the
// request state was released.
func TestAwaitAdd_OnlyAnAmbiguousTimeoutIsPending(t *testing.T) {
	h := newBindingHostAt(t, 0)
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	a := pendingAdd{shardID: bindingSystemShard, replicaID: 9}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"committed", nil},
		{"refused before raft", quicraft.ErrStaleConfigChange},
		{"timeout before raft", quicraft.ErrTimeout},
		{"delivered maybe-applied", &quicraft.MaybeAppliedError{Err: quicraft.ErrShardNotReady}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending, err := svc.awaitAdd(resultStub{err: tc.err, c: make(chan quicraft.RequestResult)}, a, make(chan struct{}))
			if !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if pending != nil {
				t.Fatal("a final outcome returned a pending channel")
			}
		})
	}
}

// TestAwaitAdd_SweeperConsumedCompletionStillResolves: the one completion
// was the sweeper's ambiguous one, consumed by Result, so ResultC never
// delivers again; the fence is released once the membership read shows the
// add committed, with no second value.
func TestAwaitAdd_SweeperConsumedCompletionStillResolves(t *testing.T) {
	h := newBindingHostAt(t, 0)
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	h.mu.Lock()
	h.members[9] = "n9:1"
	h.mu.Unlock()
	ambiguous := &quicraft.MaybeAppliedError{Err: quicraft.ErrTimeout}
	pending, err := svc.awaitAdd(resultStub{err: ambiguous, c: make(chan quicraft.RequestResult)},
		pendingAdd{shardID: bindingSystemShard, replicaID: 9, repropose: func(context.Context) (*quicraft.RequestState, error) {
			t.Error("a committed add was proposed again")
			return nil, quicraft.ErrClosed
		}}, make(chan struct{}))
	if !errors.Is(err, quicraft.ErrTimeout) || pending == nil {
		t.Fatalf("awaitAdd = (%v, %v), want a pending timeout", pending, err)
	}
	select {
	case <-pending:
	case <-time.After(10 * time.Second):
		t.Fatal("the fence was never released for a committed add")
	}
}

// TestAwaitAdd_TimerWonThenSweeperCompletionKeepsFence: Result's own timer
// won and the sweeper's ambiguous completion then lands on ResultC; that is
// no outcome, so the fence holds while the add is still in raft and is
// released only when the membership shows it settled.
func TestAwaitAdd_TimerWonThenSweeperCompletionKeepsFence(t *testing.T) {
	h := newBindingHostAt(t, 0)
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	h.mu.Lock()
	h.ambiguousRS = newCompletedRequestState(quicraft.RequestResult{})
	h.ambiguousAddr, h.ambiguousReplica = "n9:1", 9
	h.mu.Unlock()
	sweeper := make(chan quicraft.RequestResult, 1)
	sweeper <- quicraft.RequestResult{}
	ambiguous := &quicraft.MaybeAppliedError{Err: quicraft.ErrTimeout}
	propose := func(ctx context.Context) (*quicraft.RequestState, error) {
		return h.RequestAddNodeWithIdentity(ctx, bindingSystemShard, 9, "n9:1", "", 0)
	}
	pending, err := svc.awaitAdd(resultStub{err: ambiguous, c: sweeper},
		pendingAdd{shardID: bindingSystemShard, replicaID: 9, repropose: propose}, make(chan struct{}))
	if !errors.Is(err, quicraft.ErrTimeout) || pending == nil {
		t.Fatalf("awaitAdd = (%v, %v), want a pending timeout", pending, err)
	}
	// The resolver has read the membership and been refused once: the add
	// is still in flight, and the fence must still hold.
	for {
		h.mu.Lock()
		refused := h.inFlightRefusals
		h.mu.Unlock()
		if refused > 0 {
			break
		}
		runtime.Gosched()
	}
	select {
	case <-pending:
		t.Fatal("the fence was released while the add was still in raft")
	default:
	}
	h.commitAmbiguous()
	select {
	case <-pending:
	case <-time.After(10 * time.Second):
		t.Fatal("the fence was never released after the add committed")
	}
}

// An ambiguous add whose membership never settles is resolved until the
// gate fencing it closes: closing stop cancels the re-proposal in flight,
// closes pending, and nothing proposes the add or reads the membership
// after that.
func TestAwaitAdd_GateCloseStopsTheResolution(t *testing.T) {
	h := newBindingHostAt(t, 0)
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	var (
		mu        sync.Mutex
		proposals int
		attempt   error
	)
	inFlight := make(chan struct{})
	propose := func(ctx context.Context) (*quicraft.RequestState, error) {
		mu.Lock()
		proposals++
		first := proposals == 1
		mu.Unlock()
		if first {
			close(inFlight)
		}
		<-ctx.Done()
		mu.Lock()
		attempt = ctx.Err()
		mu.Unlock()
		return nil, ctx.Err()
	}
	stop := make(chan struct{})
	ambiguous := &quicraft.MaybeAppliedError{Err: quicraft.ErrTimeout}
	pending, err := svc.awaitAdd(resultStub{err: ambiguous, c: make(chan quicraft.RequestResult)},
		pendingAdd{shardID: bindingSystemShard, replicaID: 9, repropose: propose}, stop)
	if !errors.Is(err, quicraft.ErrTimeout) || pending == nil {
		t.Fatalf("awaitAdd = (%v, %v), want a pending timeout", pending, err)
	}
	<-inFlight
	h.mu.Lock()
	reads := h.membershipReads
	h.mu.Unlock()
	select {
	case <-pending:
		t.Fatal("the fence was released before the gate closed")
	default:
	}
	close(stop)
	<-pending
	mu.Lock()
	defer mu.Unlock()
	if proposals != 1 {
		t.Errorf("the add was proposed %d times, want 1 (none after the gate closed)", proposals)
	}
	if !errors.Is(attempt, context.Canceled) {
		t.Errorf("the re-proposal in flight ended with %v, want it cancelled by the gate close", attempt)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.membershipReads != reads {
		t.Errorf("membership read %d times after the gate closed", h.membershipReads-reads)
	}
}

// An ambiguous add nothing fences (a closed stop) returns its error with no
// pending, and nothing resolves it: no re-proposal, no membership read.
func TestAwaitAdd_UnfencedAmbiguousAddIsNotResolved(t *testing.T) {
	h := newBindingHostAt(t, 0)
	svc, ok := NewAdminService(h).(*AdminService)
	if !ok {
		t.Fatal("NewAdminService did not return *AdminService")
	}
	ambiguous := &quicraft.MaybeAppliedError{Err: quicraft.ErrTimeout}
	pending, err := svc.awaitAdd(resultStub{err: ambiguous, c: make(chan quicraft.RequestResult)},
		pendingAdd{shardID: bindingSystemShard, replicaID: 9, repropose: func(context.Context) (*quicraft.RequestState, error) {
			t.Error("an unfenced ambiguous add was proposed again")
			return nil, quicraft.ErrClosed
		}}, featuregate.Unfenced)
	if !errors.Is(err, quicraft.ErrTimeout) || pending != nil {
		t.Fatalf("awaitAdd = (%v, %v), want the timeout and no pending", pending, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.membershipReads != 0 {
		t.Errorf("an unfenced ambiguous add read the membership %d times", h.membershipReads)
	}
}
