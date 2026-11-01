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

package replicaforward

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// listedFeeds is a member store: feeds for its watch, and a fixed list.
type listedFeeds struct {
	*feeds
	list []cluster.Member
}

func (l *listedFeeds) ListMembers(context.Context) ([]cluster.Member, error) { return l.list, nil }

var _ Members = (*listedFeeds)(nil)

// TestStart_TheLocatorsCacheFollowsTheWatches: the Locator Start returns
// reads the member records it was given, and its cache is the one the
// placement watch evicts from.
func TestStart_TheLocatorsCacheFollowsTheWatches(t *testing.T) {
	f := &listedFeeds{
		feeds: &feeds{placements: make(chan placement.Event), members: make(chan cluster.MemberEvent)},
		list:  []cluster.Member{{NodeID: 2, Address: "b:1", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: "b:443"}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, done := Start(ctx, f, f, "a:1", nil)

	refusal := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 9, LeaderReplicaID: 2,
		Replicas: []placement.Replica{{ReplicaID: 2, RaftAddr: "b:1"}}}
	target, err := l.Forward(ctx, refusal, cluster.APIProtocolGRPC, func(context.Context, Target) (Result, error) {
		return Result{Outcome: Served}, nil
	})
	if err != nil || target.Endpoint != "b:443" {
		t.Fatalf("Forward = %+v, %v; want the member's endpoint", target, err)
	}
	if _, ok := l.Cache().Get(9); !ok {
		t.Fatal("the served target was not cached")
	}
	f.placements <- placement.Event{Type: placement.EventPut, TenantID: "acme", Database: "orders"}
	eventually(t, func() bool { _, ok := l.Cache().Get(9); return !ok }, "the placement watch did not reach the Locator's cache")
	cancel()
	<-done
}

// TestStart_DoneClosesOnceTheWatchesReturn: done stays open while ctx is live
// and closes once ctx's end has returned both watches.
func TestStart_DoneClosesOnceTheWatchesReturn(t *testing.T) {
	f := &listedFeeds{feeds: &feeds{placements: make(chan placement.Event), members: make(chan cluster.MemberEvent)}}
	ctx, cancel := context.WithCancel(context.Background())
	_, done := Start(ctx, f, f, "a:1", nil)
	select {
	case <-done:
		t.Fatal("done closed while ctx was live")
	default:
	}
	cancel()
	<-done
}
