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
