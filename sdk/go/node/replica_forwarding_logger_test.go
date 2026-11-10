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

package node

import (
	"context"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// placementWatcherStub reports no placement changes until its context ends.
type placementWatcherStub struct{}

func (placementWatcherStub) Watch(ctx context.Context) (<-chan placement.Event, error) {
	ch := make(chan placement.Event)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// TestReplicaForwardingOption_ClusteredNodeWithNoLogger: a node built with no
// logger still composes forwarding, its locator on the node's live background
// context. It lives apart from replica_forwarding_test.go, whose raft build
// constraint would drop it from every other tag set.
func TestReplicaForwardingOption_ClusteredNodeWithNoLogger(t *testing.T) {
	client := newBareStandaloneNode(t).featureClient.Load()
	if client == nil {
		t.Fatal("precondition: a started node holds a feature client")
	}
	n := &Node{}
	ctx := n.background()
	t.Cleanup(n.bgCancel)
	opt, err := n.replicaForwardingOption(qrdbconfig.DefaultConfig(), false, placementWatcherStub{}, client)
	if err != nil {
		t.Fatalf("replicaForwardingOption: %v", err)
	}
	if opt == nil {
		t.Fatal("a clustered node with forwarding on must get the forwarding option")
	}
	if ctx.Err() != nil {
		t.Fatal("the locator runs on the node's background context, which must still be live")
	}
}
