// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package replicaforward

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// bindingRefusal is node C's refusal of a database node A holds (OD-2): a
// local binding, with no shard and one replica, the owner.
func bindingRefusal() (*members, *placement.NotAReplicaError) {
	ms, _ := threeNodes()
	return ms, &placement.NotAReplicaError{
		TenantID: "acme", Database: "orders", NodeID: "c:1",
		Replicas: []placement.Replica{{NodeID: "a:1", RaftAddr: "a:1"}},
	}
}

// requireOwnerUnavailable fails unless err is the typed owner error naming A.
func requireOwnerUnavailable(t *testing.T, err error) {
	t.Helper()
	var owner *database.BindingOwnerUnavailableError
	if !errors.As(err, &owner) {
		t.Fatalf("Forward = %v; want *database.BindingOwnerUnavailableError", err)
	}
	if owner.Owner != "a:1" || owner.Name != "orders" || owner.TenantID != "acme" {
		t.Fatalf("owner error = %+v; want owner a:1 for acme/orders", owner)
	}
}

func TestForward_BindingReachesTheOwner(t *testing.T) {
	ms, refusal := bindingRefusal()
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	s := &sender{}
	served, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	if err != nil || served.RaftAddr != "a:1" {
		t.Fatalf("Forward = %+v, %v; want served by the owner", served, err)
	}
	if len(s.sent) != 1 || s.sent[0].RaftAddr != "a:1" || s.sent[0].Endpoint != "a:grpc" {
		t.Fatalf("sends = %+v; want one to the owner's gRPC endpoint", s.sent)
	}
}

func TestForward_BindingIsNeverCached(t *testing.T) {
	ms, refusal := bindingRefusal()
	cache := NewLeaderCache()
	l := NewLocator(ms, cache, "c:1", nil)
	s := &sender{}
	for range 2 {
		if _, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send); err != nil {
			t.Fatalf("Forward: %v", err)
		}
	}
	if _, ok := cache.Get(0); ok {
		t.Fatal("a local binding was cached under shard 0")
	}
	if ms.count() != 2 {
		t.Fatalf("member reads = %d; want one per forward, no cache hit", ms.count())
	}
}

func TestForward_BindingOwnerUnreachableIsTyped(t *testing.T) {
	ms, refusal := bindingRefusal()
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	s := &sender{answers: map[string]Result{"a:1": {Outcome: Unreachable}}}
	_, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	requireOwnerUnavailable(t, err)
	var none *NoTargetServedError
	if !errors.As(err, &none) {
		t.Fatalf("Forward = %v; want the no-target failure wrapped", err)
	}
}

func TestForward_BindingOwnerSendErrorIsTyped(t *testing.T) {
	ms, refusal := bindingRefusal()
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	cause := errors.New("connection refused")
	s := &sender{errs: map[string]error{"a:1": cause}}
	_, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	requireOwnerUnavailable(t, err)
	if !errors.Is(err, cause) {
		t.Fatalf("Forward = %v; want the send failure wrapped", err)
	}
}

func TestForward_BindingOwnerNotAMemberIsTyped(t *testing.T) {
	ms, refusal := bindingRefusal()
	refusal.Replicas = []placement.Replica{{NodeID: "gone:1", RaftAddr: "gone:1"}}
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	s := &sender{}
	_, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, s.send)
	var owner *database.BindingOwnerUnavailableError
	if !errors.As(err, &owner) || owner.Owner != "gone:1" || len(s.sent) != 0 {
		t.Fatalf("Forward = %v after sends %+v; want the owner error naming gone:1, nothing sent", err, s.sent)
	}
}

func TestForward_BindingMemberReadFailureIsTyped(t *testing.T) {
	ms, refusal := bindingRefusal()
	ms.err = errors.New("members unavailable")
	l := NewLocator(ms, NewLeaderCache(), "c:1", nil)
	_, err := l.Forward(context.Background(), refusal, cluster.APIProtocolGRPC, (&sender{}).send)
	requireOwnerUnavailable(t, err)
}
