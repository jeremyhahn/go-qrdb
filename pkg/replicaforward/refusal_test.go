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
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestNotAReplicaMetadata_RoundTrips(t *testing.T) {
	for name, in := range map[string]*placement.NotAReplicaError{
		"replicas": {
			TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-3", LeaderReplicaID: 2,
			Replicas: []placement.Replica{
				{ReplicaID: 1, NodeID: "node-1", RaftAddr: "192.0.2.1:7000"},
				{ReplicaID: 2, NodeID: "node-2", RaftAddr: "192.0.2.2:7000"},
			},
		},
		"no replicas": {TenantID: "acme", Database: "orders", ShardID: 7, NodeID: "node-3", Replicas: []placement.Replica{}},
	} {
		got, err := ParseNotAReplica(NotAReplicaMetadata(in))
		if err != nil {
			t.Fatalf("%s: ParseNotAReplica = %v", name, err)
		}
		if !reflect.DeepEqual(got, in) {
			t.Errorf("%s: round trip = %+v; want %+v", name, got, in)
		}
	}
}

func TestParseNotAReplica_RefusesMissingOrMalformedKeys(t *testing.T) {
	base := NotAReplicaMetadata(&placement.NotAReplicaError{
		TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-3",
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "node-1", RaftAddr: "192.0.2.1:7000"}},
	})
	for _, key := range []string{keyShard, keyLeaderReplicaID, keyReplicas, "replica_0_id"} {
		for _, broken := range []map[string]string{without(base, key), with(base, key, "x")} {
			var de *RefusalDecodeError
			if _, err := ParseNotAReplica(broken); !errors.As(err, &de) || de.Key != key {
				t.Errorf("key %s broken: ParseNotAReplica = %v; want a decode error naming it", key, err)
			}
		}
	}
	// A replica count larger than the metadata could hold is refused before
	// anything is allocated for it.
	if _, err := ParseNotAReplica(with(base, keyReplicas, strconv.FormatUint(1<<40, 10))); err == nil {
		t.Error("an implausible replica count was accepted")
	}
}

func TestNotLeaderMetadata_RoundTrips(t *testing.T) {
	if got := LeaderRaftAddr(NotLeaderMetadata("192.0.2.2:7000")); got != "192.0.2.2:7000" {
		t.Fatalf("LeaderRaftAddr = %q", got)
	}
	if got := LeaderRaftAddr(NotLeaderMetadata("")); got != "" {
		t.Fatalf("an unknown leader decoded as %q", got)
	}
}

// TestRefusal_ClassifiesTheTwoReplicaRefusals: a non-replica's refusal and a
// follower's, bare or wrapped, and nothing else.
func TestRefusal_ClassifiesTheTwoReplicaRefusals(t *testing.T) {
	nar := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 3, Replicas: []placement.Replica{}}
	reason, md, ok := Refusal(&service.ReadError{Err: nar})
	if got, err := ParseNotAReplica(md); !ok || reason != ReasonNotAReplica || err != nil || !reflect.DeepEqual(got, nar) {
		t.Fatalf("Refusal(wrapped NotAReplica) = %q, %v, %v", reason, md, ok)
	}
	reason, md, ok = Refusal(&service.NotLeaderError{ShardID: 3, LeaderAddress: "192.0.2.2:7000"})
	if !ok || reason != ReasonNotLeader || LeaderRaftAddr(md) != "192.0.2.2:7000" {
		t.Fatalf("Refusal(NotLeader) = %q, %v, %v", reason, md, ok)
	}
	if _, _, ok := Refusal(errors.New("boom")); ok {
		t.Fatal("an unrelated error classified as a replica refusal")
	}
}

// TestRefusal_SealStateUnconfirmed: a node that cannot confirm the cluster
// seal state carries its own reason, wrapped or not, and a sealed barrier
// does not.
func TestRefusal_SealStateUnconfirmed(t *testing.T) {
	for _, err := range []error{
		&clusterseal.SealStateUnconfirmedError{},
		&service.ReadError{Err: &clusterseal.SealStateUnconfirmedError{}},
	} {
		reason, md, ok := Refusal(err)
		if !ok || reason != ReasonSealStateUnconfirmed || md != nil {
			t.Fatalf("Refusal(%v) = %q, %v, %v; want %q, nil, true", err, reason, md, ok, ReasonSealStateUnconfirmed)
		}
	}
	if reason, _, ok := Refusal(&clusterseal.SealedError{Barrier: clusterseal.NodeBarrier()}); ok {
		t.Fatalf("a sealed barrier classified as refusal %q", reason)
	}
}

func TestRefusalDecodeError_Message(t *testing.T) {
	inner := errors.New("bad digit")
	e := &RefusalDecodeError{Key: "shard_id", Err: inner}
	if !errors.Is(e, inner) || e.Error() == (&RefusalDecodeError{Key: "shard_id"}).Error() {
		t.Fatalf("decode error %q does not carry its cause", e.Error())
	}
}

func without(md map[string]string, key string) map[string]string {
	out := with(md, key, "")
	delete(out, key)
	return out
}

func with(md map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(md))
	for k, v := range md {
		out[k] = v
	}
	out[key] = value
	return out
}
