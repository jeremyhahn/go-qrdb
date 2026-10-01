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
	"maps"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

var (
	identityOne     = "spki:" + strings.Repeat("11", 32)
	identityTwo     = "spki:" + strings.Repeat("22", 32)
	identityNewNode = "spki:" + strings.Repeat("44", 32)
)

// clusteredMockHost is a host WITH a transport whose system shard has
// committed replicas 1 and 2 with identities one and two.
func clusteredMockHost() *mockHost {
	return &mockHost{
		nodeIdentity: identityOne,
		memberships: map[uint64]*quicraft.Membership{
			shard.ShardKV: {
				Nodes:      map[uint64]string{1: "one.example:1", 2: "two.example:1"},
				Identities: map[uint64]string{1: identityOne, 2: identityTwo},
			},
		},
	}
}

func TestAdminService_AddMember_Identity(t *testing.T) {
	tests := []struct {
		name     string
		host     func() *mockHost
		replica  uint64
		addr     string
		override string
		want     string
		refusal  *MemberIdentityRefusal
	}{
		{name: "committed identity of a known replica", host: clusteredMockHost, replica: 2, addr: "two.example:1", want: identityTwo},
		{name: "override for a brand-new node", host: clusteredMockHost, replica: 4, addr: "four.example:1", override: identityNewNode, want: identityNewNode},
		{name: "override matching the committed identity", host: clusteredMockHost, replica: 2, addr: "two.example:1", override: identityTwo, want: identityTwo},
		{name: "override contradicting the committed identity", host: clusteredMockHost, replica: 2, addr: "two.example:1", override: identityNewNode, refusal: refusalPtr(IdentityConflict)},
		{name: "malformed override", host: clusteredMockHost, replica: 4, addr: "four.example:1", override: "spki:short", refusal: refusalPtr(IdentityInvalid)},
		{name: "brand-new node without override", host: clusteredMockHost, replica: 4, addr: "four.example:1", refusal: refusalPtr(IdentityUnknown)},
		{name: "membership unreadable", host: func() *mockHost {
			h := clusteredMockHost()
			h.membershipErr = errors.New("no leader")
			return h
		}, replica: 4, addr: "four.example:1", refusal: refusalPtr(IdentityMembershipUnavailable)},
		{name: "address committed with two identities", host: func() *mockHost {
			h := clusteredMockHost()
			h.memberships[shard.ShardKV].Observers = map[uint64]string{3: "two.example:1"}
			h.memberships[shard.ShardKV].Identities[3] = identityNewNode
			return h
		}, replica: 4, addr: "two.example:1", refusal: refusalPtr(IdentityConflict)},
		{name: "replica committed at another address", host: clusteredMockHost, replica: 2, addr: "one.example:1", refusal: refusalPtr(IdentityConflict)},
		{name: "transport-less host commits none", host: func() *mockHost { return &mockHost{} }, replica: 4, addr: "four.example:1", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host := tc.host()
			// The proposal itself is not under test: stop it with a sentinel
			// once the identity it was given has been recorded.
			host.requestAddNodeFn = func(context.Context, uint64, uint64, string) (*quicraft.RequestState, error) {
				return nil, errProposalStopped
			}
			svc := NewAdminService(host)
			err := svc.AddMember(context.Background(), shard.ShardKV, tc.replica, tc.addr, tc.override)
			if tc.refusal != nil {
				var mie *MemberIdentityError
				if !errors.As(err, &mie) || mie.Refusal() != *tc.refusal || mie.ReplicaID != tc.replica {
					t.Fatalf("AddMember = %v, want *MemberIdentityError with refusal %d", err, *tc.refusal)
				}
				if len(host.addedIdentities) != 0 {
					t.Fatalf("a refused add must propose nothing, proposed %v", host.addedIdentities)
				}
				return
			}
			if !errors.Is(err, errProposalStopped) {
				t.Fatalf("AddMember = %v, want the add to reach the proposal", err)
			}
			if len(host.addedIdentities) != 1 || host.addedIdentities[0] != tc.want {
				t.Fatalf("proposed identities %v, want [%q]", host.addedIdentities, tc.want)
			}
		})
	}
}

func TestMemberIdentityError_Error(t *testing.T) {
	_, err := ResolveMemberIdentity(context.Background(), clusteredMockHost(), shard.ShardKV, 4, "four.example:1", "")
	if err == nil || !strings.HasPrefix(err.Error(), "service: member identity: ") ||
		!strings.Contains(err.Error(), "qrdbd node-identity --cert") {
		t.Fatalf("err = %v, want the refusal to say how to get the identity", err)
	}
}

var errProposalStopped = errors.New("proposal stopped by the test")

func refusalPtr(r MemberIdentityRefusal) *MemberIdentityRefusal { return &r }

// TestAdminService_StartShard_MemberIdentities pins that StartShard builds
// Config.MemberIdentities the way the dedicated-shard factory does: the local
// replica's is the host's own, every other member's is its host's committed
// identity, a founder refuses a member it cannot resolve, a joiner keeps only
// the anchors it can, and a transport-less host states none.
func TestAdminService_StartShard_MemberIdentities(t *testing.T) {
	founders := map[uint64]string{1: "one.example:1", 2: "two.example:1"}
	withStranger := map[uint64]string{1: "one.example:1", 2: "two.example:1", 3: "four.example:1"}
	tests := []struct {
		name    string
		host    func() *mockHost
		members map[uint64]string
		join    bool
		want    map[uint64]string
		refusal *MemberIdentityRefusal
	}{
		{name: "founder with committed peers", host: clusteredMockHost, members: founders,
			want: map[uint64]string{1: identityOne, 2: identityTwo}},
		{name: "founder with a peer no membership names", host: clusteredMockHost, members: withStranger,
			refusal: refusalPtr(IdentityUnknown)},
		{name: "joiner keeps the anchors it can resolve", host: clusteredMockHost, members: withStranger, join: true,
			want: map[uint64]string{1: identityOne, 2: identityTwo}},
		{name: "transport-less host states none", host: func() *mockHost { return &mockHost{} }, members: founders},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host := tc.host()
			var started *config.Config
			host.startShardFn = func(_ map[uint64]string, _ bool, _ sm.CreateFunc, cfg config.Config) error {
				started = &cfg
				return nil
			}
			svc := NewAdminService(host, WithStateMachineFactory(dummyCreateFunc))
			err := svc.StartShard(context.Background(), &StartShardRequest{
				ShardID: 100, ReplicaID: 1, Members: tc.members, Join: tc.join,
			})
			if tc.refusal != nil {
				var mie *MemberIdentityError
				if !errors.As(err, &mie) || mie.Refusal() != *tc.refusal {
					t.Fatalf("StartShard = %v, want *MemberIdentityError with refusal %d", err, *tc.refusal)
				}
				if started != nil {
					t.Fatal("a refused start must not start the shard")
				}
				return
			}
			if err != nil || started == nil {
				t.Fatalf("StartShard = %v, want the shard started", err)
			}
			if !maps.Equal(started.MemberIdentities, tc.want) {
				t.Fatalf("MemberIdentities = %v, want %v", started.MemberIdentities, tc.want)
			}
		})
	}
}
