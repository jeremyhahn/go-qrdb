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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

func newMembershipClient(t *testing.T) *sdk.Client {
	t.Helper()
	tr, err := embedded.New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestClient_MembershipConstructors(t *testing.T) {
	client := newMembershipClient(t)
	defer client.Close()

	if client.NewMembershipStore() == nil {
		t.Fatal("NewMembershipStore returned nil")
	}
	if client.NewMembershipReconfigurer() == nil {
		t.Fatal("NewMembershipReconfigurer returned nil")
	}
	mgr, err := client.NewMembershipManager(cluster.Member{NodeID: 1, Address: "10.0.0.1:5000"}, 0, cluster.MembershipConfig{})
	if err != nil {
		t.Fatalf("NewMembershipManager: %v", err)
	}
	if mgr == nil {
		t.Fatal("NewMembershipManager returned nil")
	}
}

func TestReconfigurer_AddRemoveMember(t *testing.T) {
	client := newMembershipClient(t)
	defer client.Close()
	r := client.NewMembershipReconfigurer()

	if err := r.AddMember(context.Background(), 0, 4, "10.0.0.4:5000"); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if err := r.RemoveMember(context.Background(), 0, 4); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
}

func TestReconfigurer_Leader(t *testing.T) {
	client := newMembershipClient(t)
	defer client.Close()
	r := client.NewMembershipReconfigurer()

	leaderID, hasLeader, err := r.Leader(0)
	if err != nil {
		t.Fatalf("Leader: %v", err)
	}
	if !hasLeader || leaderID != 1 {
		t.Fatalf("Leader() = (%d, %v), want (1, true)", leaderID, hasLeader)
	}
}

func TestReconfigurer_IsMember(t *testing.T) {
	client := newMembershipClient(t)
	defer client.Close()
	r := client.NewMembershipReconfigurer()

	// mockAdminService.Peers reports shard 0 with replica 1.
	present, err := r.IsMember(0, 1)
	if err != nil {
		t.Fatalf("IsMember: %v", err)
	}
	if !present {
		t.Fatal("IsMember(0,1) = false, want true")
	}

	absent, err := r.IsMember(0, 99)
	if err != nil {
		t.Fatalf("IsMember: %v", err)
	}
	if absent {
		t.Fatal("IsMember(0,99) = true, want false")
	}

	wrongShard, err := r.IsMember(7, 1)
	if err != nil {
		t.Fatalf("IsMember: %v", err)
	}
	if wrongShard {
		t.Fatal("IsMember(7,1) = true, want false (unknown shard)")
	}
}

func TestTranslateMemberEvent(t *testing.T) {
	m := cluster.Member{NodeID: 2, Address: "10.0.0.2:5000"}
	data, _ := json.Marshal(m)
	key := []byte(cluster.MembershipPrefix + "2")

	// PUT -> joined.
	ev, ok := sdk.TranslateMemberEventForTest(&transport.WatchEvent{
		Type: transport.EventPut,
		Kv:   &transport.MVCCKeyValue{Key: key, Value: data},
	})
	if !ok || ev.Type != cluster.MemberJoined || ev.Member.NodeID != 2 {
		t.Fatalf("PUT translate = %+v ok=%v, want joined node 2", ev, ok)
	}

	// DELETE with PrevKv -> left with full member.
	ev, ok = sdk.TranslateMemberEventForTest(&transport.WatchEvent{
		Type:   transport.EventDelete,
		PrevKv: &transport.MVCCKeyValue{Key: key, Value: data},
	})
	if !ok || ev.Type != cluster.MemberLeft || ev.Member.NodeID != 2 {
		t.Fatalf("DELETE(prevkv) translate = %+v ok=%v, want left node 2", ev, ok)
	}

	// DELETE without PrevKv -> left, node derived from key.
	ev, ok = sdk.TranslateMemberEventForTest(&transport.WatchEvent{
		Type: transport.EventDelete,
		Kv:   &transport.MVCCKeyValue{Key: key},
	})
	if !ok || ev.Type != cluster.MemberLeft || ev.Member.NodeID != 2 {
		t.Fatalf("DELETE(no prevkv) translate = %+v ok=%v, want left node 2", ev, ok)
	}

	// PUT with nil Kv -> skipped.
	if _, ok := sdk.TranslateMemberEventForTest(&transport.WatchEvent{Type: transport.EventPut}); ok {
		t.Fatal("PUT with nil Kv should be skipped")
	}
	// Unknown type -> skipped.
	if _, ok := sdk.TranslateMemberEventForTest(&transport.WatchEvent{Type: transport.EventType(42)}); ok {
		t.Fatal("unknown event type should be skipped")
	}
}
