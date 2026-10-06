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
	"errors"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newClusterClient creates an SDK client with embedded transport wired to the
// mock KV and admin services from client_test.go.
func newClusterClient(t *testing.T) *sdk.Client {
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

func TestClient_Cluster_ReturnsClient(t *testing.T) {
	client := newClusterClient(t)
	defer client.Close()

	if client.Cluster() == nil {
		t.Fatal("expected non-nil ClusterClient")
	}
}

func TestClient_Cluster_Members_Success(t *testing.T) {
	client := newClusterClient(t)
	defer client.Close()

	members, err := client.Cluster().Members(context.Background(), 0)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("len(members) = %d, want 1", len(members))
	}
	if members[0].ReplicaID != 1 || members[0].Address != "127.0.0.1:63001" || !members[0].IsLeader {
		t.Fatalf("member = %+v, want {1, 127.0.0.1:63001, leader}", members[0])
	}
}

func TestClient_Cluster_Members_ShardNotFound(t *testing.T) {
	client := newClusterClient(t)
	defer client.Close()

	_, err := client.Cluster().Members(context.Background(), 99)
	var nfe *sdk.ShardNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("Members error = %T (%v), want *ShardNotFoundError", err, err)
	}
	if nfe.ShardID != 99 {
		t.Fatalf("ShardNotFoundError.ShardID = %d, want 99", nfe.ShardID)
	}
}

func TestClient_Cluster_Leader_Success(t *testing.T) {
	client := newClusterClient(t)
	defer client.Close()

	replicaID, hasLeader, err := client.Cluster().Leader(context.Background(), 0)
	if err != nil {
		t.Fatalf("Leader: %v", err)
	}
	if !hasLeader || replicaID != 1 {
		t.Fatalf("Leader() = (%d, %v), want (1, true)", replicaID, hasLeader)
	}
}

func TestClient_Cluster_AddRemoveReplica(t *testing.T) {
	client := newClusterClient(t)
	defer client.Close()

	cc := client.Cluster()
	if err := cc.AddReplica(context.Background(), 0, 4, "127.0.0.1:63004", ""); err != nil {
		t.Fatalf("AddReplica: %v", err)
	}
	if err := cc.RemoveReplica(context.Background(), 0, 4); err != nil {
		t.Fatalf("RemoveReplica: %v", err)
	}
}

func TestClient_Cluster_PromoteReplica(t *testing.T) {
	client := newClusterClient(t)
	defer client.Close()

	if err := client.Cluster().PromoteReplica(context.Background(), 0, 4); err != nil {
		t.Fatalf("PromoteReplica: %v", err)
	}
}

func TestClient_Cluster_WatchMembership_InitialSnapshot(t *testing.T) {
	client := newClusterClient(t)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := client.Cluster().WatchMembership(ctx, 0)
	if err != nil {
		t.Fatalf("WatchMembership: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.ShardID != 0 {
			t.Fatalf("event ShardID = %d, want 0", ev.ShardID)
		}
		if len(ev.Members) != 1 || len(ev.Added) != 1 {
			t.Fatalf("initial event = %+v, want 1 member / 1 added", ev)
		}
		if ev.LeaderID != 1 {
			t.Fatalf("event LeaderID = %d, want 1", ev.LeaderID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial membership snapshot")
	}
}

func TestClient_Cluster_WatchMembership_ShardNotFound(t *testing.T) {
	client := newClusterClient(t)
	defer client.Close()

	_, err := client.Cluster().WatchMembership(context.Background(), 99)
	var nfe *sdk.ShardNotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("WatchMembership error = %T (%v), want *ShardNotFoundError", err, err)
	}
}
