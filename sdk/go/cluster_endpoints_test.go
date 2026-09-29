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

package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// fakeClusterTransport
//
// mockAdminService/newMockKVService in client_test.go wrap a single
// hardcoded shard/replica through the embedded service-layer adapter and
// offer no way to inject errors or vary the Raft-peer/registry view per
// test. ClusterClient.Members enrichment, LeaderEndpoint's protocol
// resolution and the fail-soft registry contract all need exactly that
// control, so fakeClusterTransport implements the transport-layer
// interfaces ClusterClient actually consumes (transport.AdminClient via
// Client.Admin(), and transport.KVClient via Client.KV(), which backs the
// cluster registry's KV().Scan(MembershipPrefix) read) directly and
// configurably. This is the "Client whose transport is a fake" approach the
// existing tests use, just with a purpose-built transport instead of the
// fixed embedded one.
// ---------------------------------------------------------------------------

type addMemberCall struct {
	shardID, replicaID uint64
	raftAddr           string
}

type removeMemberCall struct {
	shardID, replicaID uint64
}

type fakeClusterTransport struct {
	mu sync.Mutex

	peersResp  *transport.PeersResponse
	peersErr   error
	peersCalls int

	shardState    *transport.ShardStateResponse
	shardStateErr error

	addMemberErr   error
	addMemberCalls []addMemberCall

	removeMemberErr   error
	removeMemberCalls []removeMemberCall

	// scanResult/scanErr back the cluster registry: ClusterClient.Members
	// enriches Raft peers via the sdkMembershipStore, which reads the
	// registry through Client.KV().Scan(ctx, cluster.MembershipPrefix).
	scanResult map[string][]byte
	scanErr    error
}

var (
	_ transport.Transport   = (*fakeClusterTransport)(nil)
	_ transport.AdminClient = (*fakeClusterTransport)(nil)
	_ transport.KVClient    = (*fakeClusterTransport)(nil)
)

// --- transport.Transport ---

func (f *fakeClusterTransport) Connect(context.Context) error { return nil }
func (f *fakeClusterTransport) Close() error                  { return nil }
func (f *fakeClusterTransport) Healthy(context.Context) bool  { return true }
func (f *fakeClusterTransport) Request(context.Context, string, any, any) error {
	return nil
}

// --- transport.AdminClient ---

func (f *fakeClusterTransport) Health(context.Context) (*transport.HealthResponse, error) {
	return &transport.HealthResponse{Status: "ok"}, nil
}

func (f *fakeClusterTransport) Status(context.Context) (*transport.StatusResponse, error) {
	return &transport.StatusResponse{}, nil
}

func (f *fakeClusterTransport) Peers(context.Context) (*transport.PeersResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peersCalls++
	return f.peersResp, f.peersErr
}

func (f *fakeClusterTransport) AddMember(_ context.Context, shardID, replicaID uint64, raftAddr, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addMemberCalls = append(f.addMemberCalls, addMemberCall{shardID: shardID, replicaID: replicaID, raftAddr: raftAddr})
	return f.addMemberErr
}

func (f *fakeClusterTransport) RemoveMember(_ context.Context, shardID, replicaID uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeMemberCalls = append(f.removeMemberCalls, removeMemberCall{shardID: shardID, replicaID: replicaID})
	return f.removeMemberErr
}

func (f *fakeClusterTransport) RequestSnapshot(context.Context, uint64) error { return nil }

func (f *fakeClusterTransport) Drain(context.Context) error { return nil }

func (f *fakeClusterTransport) Capabilities(context.Context) (*transport.CapabilitiesResponse, error) {
	return &transport.CapabilitiesResponse{}, nil
}

func (f *fakeClusterTransport) StartShard(context.Context, *transport.StartShardRequest) error {
	return nil
}

func (f *fakeClusterTransport) StopShard(context.Context, uint64) error { return nil }

func (f *fakeClusterTransport) GetShardState(context.Context, uint64) (*transport.ShardStateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shardState, f.shardStateErr
}

func (f *fakeClusterTransport) GetTopology(context.Context) (*transport.TopologyResponse, error) {
	return &transport.TopologyResponse{}, nil
}

// --- transport.KVClient ---
//
// Only Scan is exercised (via the cluster registry read); the rest are
// unused by ClusterClient and exist solely to satisfy the interface.

func (f *fakeClusterTransport) Put(context.Context, string, []byte, ...transport.WriteOption) error {
	return nil
}

func (f *fakeClusterTransport) Get(context.Context, string, ...transport.ReadOption) ([]byte, error) {
	return nil, nil
}

func (f *fakeClusterTransport) Delete(context.Context, string, ...transport.WriteOption) error {
	return nil
}

func (f *fakeClusterTransport) List(context.Context, string, ...transport.ReadOption) ([]string, error) {
	return nil, nil
}

func (f *fakeClusterTransport) Scan(context.Context, string, ...transport.ReadOption) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scanResult, f.scanErr
}

func (f *fakeClusterTransport) Exists(context.Context, string, ...transport.ReadOption) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newFakeClusterClient wires ft into an sdk.Client and returns its
// ClusterClient, mirroring how Client.Cluster() is obtained in cluster_test.go.
func newFakeClusterClient(t *testing.T, ft *fakeClusterTransport) *sdk.ClusterClient {
	t.Helper()
	client, err := sdk.NewClient(ft)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return client.Cluster()
}

// peersResponse builds a single-shard transport.PeersResponse.
func peersResponse(shardID, leaderID uint64, peers ...transport.PeerInfo) *transport.PeersResponse {
	return &transport.PeersResponse{
		Shards: []transport.ShardPeers{
			{ShardID: shardID, LeaderID: leaderID, Peers: peers},
		},
	}
}

// marshalMember JSON-encodes a cluster.Member the way the registry stores it
// (sdkMembershipStore.ListMembers unmarshals each Scan value into one).
func marshalMember(t *testing.T, m cluster.Member) []byte {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal member: %v", err)
	}
	return data
}

// pollIntervalOf reads ClusterClient's unexported pollInterval field.
// Reflect's Int() accessor, unlike Interface(), does not enforce the
// exported-field restriction, so this is a legitimate way to assert on
// WithPollInterval's effect without exporting a test-only accessor from a
// non-test file.
func pollIntervalOf(t *testing.T, cc *sdk.ClusterClient) time.Duration {
	t.Helper()
	v := reflect.ValueOf(cc).Elem().FieldByName("pollInterval")
	if !v.IsValid() {
		t.Fatal("ClusterClient has no pollInterval field")
	}
	return time.Duration(v.Int())
}

// ---------------------------------------------------------------------------
// Member.APIEndpoint (sdk.Member)
// ---------------------------------------------------------------------------

func TestMember_APIEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		member   sdk.Member
		proto    sdk.APIProtocol
		wantAddr string
		wantOK   bool
	}{
		{
			name:     "protocol present",
			member:   sdk.Member{APIEndpoints: map[sdk.APIProtocol]string{sdk.APIProtocolREST: "10.0.0.1:8080"}},
			proto:    sdk.APIProtocolREST,
			wantAddr: "10.0.0.1:8080",
			wantOK:   true,
		},
		{
			name:   "protocol absent",
			member: sdk.Member{APIEndpoints: map[sdk.APIProtocol]string{sdk.APIProtocolREST: "10.0.0.1:8080"}},
			proto:  sdk.APIProtocolGRPC,
			wantOK: false,
		},
		{
			name:   "empty-string address advertised",
			member: sdk.Member{APIEndpoints: map[sdk.APIProtocol]string{sdk.APIProtocolREST: ""}},
			proto:  sdk.APIProtocolREST,
			wantOK: false,
		},
		{
			name:   "nil endpoint map",
			member: sdk.Member{},
			proto:  sdk.APIProtocolREST,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, ok := tt.member.APIEndpoint(tt.proto)
			if ok != tt.wantOK || addr != tt.wantAddr {
				t.Fatalf("APIEndpoint(%q) = (%q, %v), want (%q, %v)", tt.proto, addr, ok, tt.wantAddr, tt.wantOK)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ClusterClient.Members endpoint enrichment
// ---------------------------------------------------------------------------

func TestClusterClient_Members_EnrichesAdvertisedEndpoints(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1,
			transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true},
			transport.PeerInfo{ReplicaID: 2, Address: "10.0.0.2:9001", IsLeader: false},
		),
		scanResult: map[string][]byte{
			cluster.MembershipPrefix + "1": marshalMember(t, cluster.Member{
				NodeID:  1,
				Address: "10.0.0.1:9001",
				APIEndpoints: map[cluster.APIProtocol]string{
					cluster.APIProtocolREST: "10.0.0.1:8080",
					cluster.APIProtocolGRPC: "10.0.0.1:9090",
				},
			}),
			// Replica 2 registers nothing: predates advertisement, or its
			// operator configured no advertise address.
		},
	}
	cc := newFakeClusterClient(t, ft)

	members, err := cc.Members(context.Background(), 0)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("len(members) = %d, want 2", len(members))
	}

	byReplica := make(map[uint64]sdk.Member, len(members))
	for _, m := range members {
		byReplica[m.ReplicaID] = m
	}

	m1, ok := byReplica[1]
	if !ok {
		t.Fatal("replica 1 missing from Members()")
	}
	addr, ok := m1.APIEndpoint(sdk.APIProtocolREST)
	if !ok || addr != "10.0.0.1:8080" {
		t.Fatalf("replica 1 REST endpoint = (%q, %v), want (10.0.0.1:8080, true)", addr, ok)
	}
	if !m1.IsLeader {
		t.Fatal("replica 1 IsLeader = false, want true (leader flag not carried through)")
	}

	m2, ok := byReplica[2]
	if !ok {
		t.Fatal("replica 2 missing from Members()")
	}
	if m2.APIEndpoints != nil {
		t.Fatalf("replica 2 (advertised nothing) APIEndpoints = %v, want nil", m2.APIEndpoints)
	}
	if m2.IsLeader {
		t.Fatal("replica 2 IsLeader = true, want false (leader flag not carried through)")
	}
}

func TestClusterClient_Members_RegistryErrorFailsSoft(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1,
			transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true},
		),
		scanErr: errors.New("registry temporarily unavailable"),
	}
	cc := newFakeClusterClient(t, ft)

	members, err := cc.Members(context.Background(), 0)
	if err != nil {
		t.Fatalf("Members returned error on registry failure, want fail-soft nil error: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("len(members) = %d, want 1 (Raft-only view)", len(members))
	}
	if members[0].APIEndpoints != nil {
		t.Fatalf("APIEndpoints = %v, want nil when the registry read fails", members[0].APIEndpoints)
	}
	if !members[0].IsLeader {
		t.Fatal("IsLeader = false, want true (Raft-only view must still be correct)")
	}
}

func TestClusterClient_Members_UnmatchedRegistryEntryIgnored(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1,
			transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true},
		),
		scanResult: map[string][]byte{
			// Node 99 is registered but is not a Raft peer of shard 0 (e.g. a
			// stale entry from a removed replica, or a member of another
			// shard). It must not fabricate an extra member.
			cluster.MembershipPrefix + "99": marshalMember(t, cluster.Member{
				NodeID:       99,
				Address:      "10.0.0.99:9001",
				APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolREST: "10.0.0.99:8080"},
			}),
		},
	}
	cc := newFakeClusterClient(t, ft)

	members, err := cc.Members(context.Background(), 0)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("len(members) = %d, want 1 (unmatched registry entry must be ignored)", len(members))
	}
	if members[0].ReplicaID != 1 {
		t.Fatalf("members[0].ReplicaID = %d, want 1", members[0].ReplicaID)
	}
	if members[0].APIEndpoints != nil {
		t.Fatalf("APIEndpoints = %v, want nil (replica 1 itself advertised nothing)", members[0].APIEndpoints)
	}
}

func TestClusterClient_Members_ShardNotFound(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1, transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true}),
	}
	cc := newFakeClusterClient(t, ft)

	_, err := cc.Members(context.Background(), 42)
	var nfe *sdk.ShardNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("Members error = %T (%v), want *ShardNotFoundError", err, err)
	}
	if nfe.ShardID != 42 {
		t.Fatalf("ShardNotFoundError.ShardID = %d, want 42", nfe.ShardID)
	}
}

func TestClusterClient_Members_AdminErrorPropagates(t *testing.T) {
	sentinel := errors.New("peers rpc failed")
	ft := &fakeClusterTransport{peersErr: sentinel}
	cc := newFakeClusterClient(t, ft)

	_, err := cc.Members(context.Background(), 0)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Members error = %v, want sentinel %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// ClusterClient.LeaderEndpoint
// ---------------------------------------------------------------------------

func TestClusterClient_LeaderEndpoint_Success(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1, transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true}),
		scanResult: map[string][]byte{
			cluster.MembershipPrefix + "1": marshalMember(t, cluster.Member{
				NodeID:  1,
				Address: "10.0.0.1:9001",
				APIEndpoints: map[cluster.APIProtocol]string{
					cluster.APIProtocolREST: "10.0.0.1:8080",
				},
			}),
		},
	}
	cc := newFakeClusterClient(t, ft)

	addr, err := cc.LeaderEndpoint(context.Background(), 0, sdk.APIProtocolREST)
	if err != nil {
		t.Fatalf("LeaderEndpoint: %v", err)
	}
	if addr != "10.0.0.1:8080" {
		t.Fatalf("LeaderEndpoint = %q, want 10.0.0.1:8080", addr)
	}
}

func TestClusterClient_LeaderEndpoint_LeaderAdvertisesNothing(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1, transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true}),
		// No registry entry for replica 1 at all.
	}
	cc := newFakeClusterClient(t, ft)

	_, err := cc.LeaderEndpoint(context.Background(), 0, sdk.APIProtocolREST)
	var lue *sdk.LeaderEndpointUnknownError
	if !errors.As(err, &lue) {
		t.Fatalf("LeaderEndpoint error = %T (%v), want *LeaderEndpointUnknownError", err, err)
	}
	if lue.ShardID != 0 || lue.ReplicaID != 1 || lue.Protocol != sdk.APIProtocolREST {
		t.Fatalf("LeaderEndpointUnknownError = %+v, want {ShardID:0 ReplicaID:1 Protocol:rest}", lue)
	}
}

func TestClusterClient_LeaderEndpoint_LeaderAdvertisesDifferentProtocol(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1, transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true}),
		scanResult: map[string][]byte{
			cluster.MembershipPrefix + "1": marshalMember(t, cluster.Member{
				NodeID:  1,
				Address: "10.0.0.1:9001",
				APIEndpoints: map[cluster.APIProtocol]string{
					cluster.APIProtocolGRPC: "10.0.0.1:9090",
				},
			}),
		},
	}
	cc := newFakeClusterClient(t, ft)

	_, err := cc.LeaderEndpoint(context.Background(), 0, sdk.APIProtocolREST)
	var lue *sdk.LeaderEndpointUnknownError
	if !errors.As(err, &lue) {
		t.Fatalf("LeaderEndpoint error = %T (%v), want *LeaderEndpointUnknownError", err, err)
	}
	if lue.Protocol != sdk.APIProtocolREST {
		t.Fatalf("LeaderEndpointUnknownError.Protocol = %q, want rest", lue.Protocol)
	}
}

func TestClusterClient_LeaderEndpoint_NoLeader(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 0, transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: false}),
	}
	cc := newFakeClusterClient(t, ft)

	_, err := cc.LeaderEndpoint(context.Background(), 0, sdk.APIProtocolREST)
	var nle *sdk.NoLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("LeaderEndpoint error = %T (%v), want *NoLeaderError", err, err)
	}
	if nle.ShardID != 0 {
		t.Fatalf("NoLeaderError.ShardID = %d, want 0", nle.ShardID)
	}
}

func TestClusterClient_LeaderEndpoint_InvalidProtocol(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1, transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true}),
	}
	cc := newFakeClusterClient(t, ft)

	_, err := cc.LeaderEndpoint(context.Background(), 0, sdk.APIProtocol("carrier-pigeon"))
	var upe *sdk.UnknownAPIProtocolError
	if !errors.As(err, &upe) {
		t.Fatalf("LeaderEndpoint error = %T (%v), want *UnknownAPIProtocolError", err, err)
	}
	if upe.Name != "carrier-pigeon" {
		t.Fatalf("UnknownAPIProtocolError.Name = %q, want carrier-pigeon", upe.Name)
	}
	if ft.peersCalls != 0 {
		t.Fatalf("Peers was called %d times, want 0 (protocol must be validated before consulting membership)", ft.peersCalls)
	}
}

func TestClusterClient_LeaderEndpoint_ShardNotFound(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(0, 1, transport.PeerInfo{ReplicaID: 1, Address: "10.0.0.1:9001", IsLeader: true}),
	}
	cc := newFakeClusterClient(t, ft)

	_, err := cc.LeaderEndpoint(context.Background(), 7, sdk.APIProtocolREST)
	var nfe *sdk.ShardNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("LeaderEndpoint error = %T (%v), want *ShardNotFoundError", err, err)
	}
	if nfe.ShardID != 7 {
		t.Fatalf("ShardNotFoundError.ShardID = %d, want 7", nfe.ShardID)
	}
}

func TestClusterClient_LeaderEndpoint_AdminErrorPropagates(t *testing.T) {
	sentinel := errors.New("peers rpc failed")
	ft := &fakeClusterTransport{peersErr: sentinel}
	cc := newFakeClusterClient(t, ft)

	_, err := cc.LeaderEndpoint(context.Background(), 0, sdk.APIProtocolREST)
	if !errors.Is(err, sentinel) {
		t.Fatalf("LeaderEndpoint error = %v, want sentinel %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// ClusterClient.AddReplica / RemoveReplica
// ---------------------------------------------------------------------------

func TestClusterClient_AddReplica_Success(t *testing.T) {
	ft := &fakeClusterTransport{}
	cc := newFakeClusterClient(t, ft)

	if err := cc.AddReplica(context.Background(), 3, 7, "10.0.0.7:9001", ""); err != nil {
		t.Fatalf("AddReplica: %v", err)
	}
	if len(ft.addMemberCalls) != 1 {
		t.Fatalf("addMemberCalls = %d, want 1", len(ft.addMemberCalls))
	}
	want := addMemberCall{shardID: 3, replicaID: 7, raftAddr: "10.0.0.7:9001"}
	if got := ft.addMemberCalls[0]; got != want {
		t.Fatalf("AddMember call = %+v, want %+v", got, want)
	}
}

func TestClusterClient_AddReplica_ErrorPropagates(t *testing.T) {
	sentinel := errors.New("add member failed")
	ft := &fakeClusterTransport{addMemberErr: sentinel}
	cc := newFakeClusterClient(t, ft)

	err := cc.AddReplica(context.Background(), 3, 7, "10.0.0.7:9001", "")
	if !errors.Is(err, sentinel) {
		t.Fatalf("AddReplica error = %v, want sentinel %v", err, sentinel)
	}
}

func TestClusterClient_AddReplica_UnknownShardPropagates(t *testing.T) {
	sentinel := &sdk.ShardNotFoundError{ShardID: 99}
	ft := &fakeClusterTransport{addMemberErr: sentinel}
	cc := newFakeClusterClient(t, ft)

	err := cc.AddReplica(context.Background(), 99, 1, "10.0.0.1:9001", "")
	var nfe *sdk.ShardNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("AddReplica error = %T (%v), want *ShardNotFoundError", err, err)
	}
	if nfe.ShardID != 99 {
		t.Fatalf("ShardNotFoundError.ShardID = %d, want 99", nfe.ShardID)
	}
}

func TestClusterClient_RemoveReplica_Success(t *testing.T) {
	ft := &fakeClusterTransport{}
	cc := newFakeClusterClient(t, ft)

	if err := cc.RemoveReplica(context.Background(), 3, 7); err != nil {
		t.Fatalf("RemoveReplica: %v", err)
	}
	if len(ft.removeMemberCalls) != 1 {
		t.Fatalf("removeMemberCalls = %d, want 1", len(ft.removeMemberCalls))
	}
	want := removeMemberCall{shardID: 3, replicaID: 7}
	if got := ft.removeMemberCalls[0]; got != want {
		t.Fatalf("RemoveMember call = %+v, want %+v", got, want)
	}
}

func TestClusterClient_RemoveReplica_ErrorPropagates(t *testing.T) {
	sentinel := errors.New("remove member failed")
	ft := &fakeClusterTransport{removeMemberErr: sentinel}
	cc := newFakeClusterClient(t, ft)

	err := cc.RemoveReplica(context.Background(), 3, 7)
	if !errors.Is(err, sentinel) {
		t.Fatalf("RemoveReplica error = %v, want sentinel %v", err, sentinel)
	}
}

func TestClusterClient_RemoveReplica_UnknownReplicaPropagates(t *testing.T) {
	sentinel := errors.New("replica 42 is not a member of shard 3")
	ft := &fakeClusterTransport{removeMemberErr: sentinel}
	cc := newFakeClusterClient(t, ft)

	err := cc.RemoveReplica(context.Background(), 3, 42)
	if !errors.Is(err, sentinel) {
		t.Fatalf("RemoveReplica error = %v, want sentinel %v", err, sentinel)
	}
}

// TestClusterClient_RemoveReplica_IdempotentRetrySucceeds models an admin
// backend where RemoveMember is idempotent (removing an already-absent
// replica is not an error, matching go-qrdb's own AdminService semantics).
// A caller retrying RemoveReplica after an ambiguous failure (e.g. a timeout
// where the first call actually succeeded) must not see an error on the
// second attempt.
func TestClusterClient_RemoveReplica_IdempotentRetrySucceeds(t *testing.T) {
	ft := &fakeClusterTransport{}
	cc := newFakeClusterClient(t, ft)

	if err := cc.RemoveReplica(context.Background(), 3, 7); err != nil {
		t.Fatalf("first RemoveReplica: %v", err)
	}
	if err := cc.RemoveReplica(context.Background(), 3, 7); err != nil {
		t.Fatalf("second RemoveReplica (retry): %v", err)
	}
	if len(ft.removeMemberCalls) != 2 {
		t.Fatalf("removeMemberCalls = %d, want 2", len(ft.removeMemberCalls))
	}
}

// ---------------------------------------------------------------------------
// ClusterClient.WithPollInterval
// ---------------------------------------------------------------------------

func TestClusterClient_WithPollInterval_PositiveApplied(t *testing.T) {
	cc := newFakeClusterClient(t, &fakeClusterTransport{})

	if got := pollIntervalOf(t, cc); got != sdk.DefaultMembershipPollInterval {
		t.Fatalf("initial pollInterval = %v, want default %v", got, sdk.DefaultMembershipPollInterval)
	}

	ret := cc.WithPollInterval(5 * time.Second)
	if ret != cc {
		t.Fatal("WithPollInterval did not return the receiver for chaining")
	}
	if got := pollIntervalOf(t, cc); got != 5*time.Second {
		t.Fatalf("pollInterval = %v, want 5s", got)
	}
}

func TestClusterClient_WithPollInterval_NonPositiveIgnored(t *testing.T) {
	cc := newFakeClusterClient(t, &fakeClusterTransport{})

	cc.WithPollInterval(9 * time.Second)
	before := pollIntervalOf(t, cc)

	if ret := cc.WithPollInterval(0); ret != cc {
		t.Fatal("WithPollInterval(0) did not return the receiver for chaining")
	}
	if got := pollIntervalOf(t, cc); got != before {
		t.Fatalf("pollInterval after WithPollInterval(0) = %v, want unchanged %v", got, before)
	}

	if ret := cc.WithPollInterval(-3 * time.Second); ret != cc {
		t.Fatal("WithPollInterval(negative) did not return the receiver for chaining")
	}
	if got := pollIntervalOf(t, cc); got != before {
		t.Fatalf("pollInterval after WithPollInterval(negative) = %v, want unchanged %v", got, before)
	}
}

// ---------------------------------------------------------------------------
// Client.NewNodeAdvertiser
// ---------------------------------------------------------------------------

func TestClient_NewNodeAdvertiser_Success(t *testing.T) {
	client, err := sdk.NewClient(&fakeClusterTransport{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	self := cluster.Member{
		NodeID:  5,
		Address: "10.0.0.5:9001",
		APIEndpoints: map[cluster.APIProtocol]string{
			cluster.APIProtocolREST: "10.0.0.5:8080",
		},
	}
	adv, err := client.NewNodeAdvertiser(self, cluster.AdvertiseConfig{})
	if err != nil {
		t.Fatalf("NewNodeAdvertiser: %v", err)
	}
	if adv == nil {
		t.Fatal("NewNodeAdvertiser returned a nil advertiser with a nil error")
	}
	if got := adv.Self(); got.NodeID != self.NodeID || got.Address != self.Address {
		t.Fatalf("adv.Self() = %+v, want %+v", got, self)
	}
}

func TestClient_NewNodeAdvertiser_UnknownProtocol(t *testing.T) {
	client, err := sdk.NewClient(&fakeClusterTransport{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	self := cluster.Member{
		NodeID:       6,
		Address:      "10.0.0.6:9001",
		APIEndpoints: map[cluster.APIProtocol]string{"carrier-pigeon": "10.0.0.6:1"},
	}
	adv, err := client.NewNodeAdvertiser(self, cluster.AdvertiseConfig{})
	if adv != nil {
		t.Fatal("NewNodeAdvertiser returned a non-nil advertiser alongside a validation error")
	}
	var upe *cluster.UnknownAPIProtocolError
	if !errors.As(err, &upe) {
		t.Fatalf("NewNodeAdvertiser error = %T (%v), want *cluster.UnknownAPIProtocolError", err, err)
	}
	if upe.Name != "carrier-pigeon" {
		t.Fatalf("UnknownAPIProtocolError.Name = %q, want carrier-pigeon", upe.Name)
	}
}

func TestClient_NewNodeAdvertiser_EmptyAddress(t *testing.T) {
	client, err := sdk.NewClient(&fakeClusterTransport{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	self := cluster.Member{
		NodeID:       7,
		Address:      "10.0.0.7:9001",
		APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolREST: ""},
	}
	adv, err := client.NewNodeAdvertiser(self, cluster.AdvertiseConfig{})
	if adv != nil {
		t.Fatal("NewNodeAdvertiser returned a non-nil advertiser alongside a validation error")
	}
	var eae *cluster.EmptyAPIEndpointError
	if !errors.As(err, &eae) {
		t.Fatalf("NewNodeAdvertiser error = %T (%v), want *cluster.EmptyAPIEndpointError", err, err)
	}
	if eae.Protocol != cluster.APIProtocolREST {
		t.Fatalf("EmptyAPIEndpointError.Protocol = %q, want rest", eae.Protocol)
	}
}
