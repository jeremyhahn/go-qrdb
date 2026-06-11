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

package grpc

import (
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestProtoToWatchEvent_Nil verifies the nil-event guard branch.
func TestProtoToWatchEvent_Nil(t *testing.T) {
	if got := protoToWatchEvent(nil); got != nil {
		t.Errorf("protoToWatchEvent(nil) = %v, want nil", got)
	}
}

// TestProtoToWatchEvent_Put verifies field mapping for a PUT event with
// both current and previous key-values populated.
func TestProtoToWatchEvent_Put(t *testing.T) {
	ev := protoToWatchEvent(&pb.Event{
		Type: pb.EventType_EVENT_PUT,
		Kv: &pb.KeyValue{
			Key:            []byte("k"),
			Value:          []byte("v"),
			CreateRevision: 3,
			ModRevision:    7,
			Version:        2,
			LeaseId:        11,
		},
		PrevKv: &pb.KeyValue{Key: []byte("k"), Value: []byte("old")},
	})
	if ev == nil {
		t.Fatal("protoToWatchEvent returned nil for non-nil event")
	}
	if ev.Type != transport.EventPut {
		t.Errorf("Type = %v, want EventPut", ev.Type)
	}
	if ev.Kv == nil || string(ev.Kv.Key) != "k" || string(ev.Kv.Value) != "v" {
		t.Errorf("Kv = %+v, want key=k value=v", ev.Kv)
	}
	if ev.Kv.CreateRevision != 3 || ev.Kv.ModRevision != 7 || ev.Kv.Version != 2 || ev.Kv.LeaseID != 11 {
		t.Errorf("Kv revisions = %+v, want create=3 mod=7 version=2 lease=11", ev.Kv)
	}
	if ev.PrevKv == nil || string(ev.PrevKv.Value) != "old" {
		t.Errorf("PrevKv = %+v, want value=old", ev.PrevKv)
	}
}

// TestProtoToWatchEvent_DeleteNilKVs verifies a DELETE event with nil
// key-values maps to nil Kv/PrevKv.
func TestProtoToWatchEvent_DeleteNilKVs(t *testing.T) {
	ev := protoToWatchEvent(&pb.Event{Type: pb.EventType_EVENT_DELETE})
	if ev == nil {
		t.Fatal("protoToWatchEvent returned nil for non-nil event")
	}
	if ev.Type != transport.EventDelete {
		t.Errorf("Type = %v, want EventDelete", ev.Type)
	}
	if ev.Kv != nil || ev.PrevKv != nil {
		t.Errorf("Kv = %v, PrevKv = %v, want both nil", ev.Kv, ev.PrevKv)
	}
}

// TestAnyToString_String verifies that string values pass through verbatim.
func TestAnyToString_String(t *testing.T) {
	if got := anyToString("plain"); got != "plain" {
		t.Errorf("anyToString(string) = %q, want %q", got, "plain")
	}
}

// TestAnyToString_NonString verifies that non-string values are JSON-encoded.
func TestAnyToString_NonString(t *testing.T) {
	if got := anyToString(42); got != "42" {
		t.Errorf("anyToString(int) = %q, want %q", got, "42")
	}
	if got := anyToString(map[string]int{"a": 1}); got != `{"a":1}` {
		t.Errorf("anyToString(map) = %q, want %q", got, `{"a":1}`)
	}
}

// TestProtoToTransportRaftConfig verifies field mapping for a populated
// DatabaseRaftConfig proto.
func TestProtoToTransportRaftConfig(t *testing.T) {
	rc := protoToTransportRaftConfig(&pb.DatabaseRaftConfig{
		ClusterMode:    "multi",
		ShardId:        5,
		ReplicaId:      2,
		RaftAddress:    "127.0.0.1:9000",
		Peers:          []string{"127.0.0.1:9001"},
		LocalNodeCount: 3,
	})
	if rc.ClusterMode != "multi" {
		t.Errorf("ClusterMode = %q, want multi", rc.ClusterMode)
	}
	if rc.ShardID != 5 || rc.ReplicaID != 2 {
		t.Errorf("ShardID/ReplicaID = %d/%d, want 5/2", rc.ShardID, rc.ReplicaID)
	}
	if rc.RaftAddress != "127.0.0.1:9000" {
		t.Errorf("RaftAddress = %q, want 127.0.0.1:9000", rc.RaftAddress)
	}
	if len(rc.Peers) != 1 || rc.Peers[0] != "127.0.0.1:9001" {
		t.Errorf("Peers = %v, want [127.0.0.1:9001]", rc.Peers)
	}
	if rc.LocalNodeCount != 3 {
		t.Errorf("LocalNodeCount = %d, want 3", rc.LocalNodeCount)
	}
}

// TestProtoToTransportRaftConfig_Zero verifies a zero-value proto maps to a
// zero-value transport config (GetX accessors tolerate the zero message).
func TestProtoToTransportRaftConfig_Zero(t *testing.T) {
	rc := protoToTransportRaftConfig(&pb.DatabaseRaftConfig{})
	if rc.ClusterMode != "" || rc.ShardID != 0 || rc.ReplicaID != 0 || rc.RaftAddress != "" || rc.LocalNodeCount != 0 {
		t.Errorf("zero proto mapped to non-zero config: %+v", rc)
	}
}

// TestPrefixRangeEnd_AllFF verifies the all-0xff prefix branch returns the
// no-upper-bound sentinel.
func TestPrefixRangeEnd_AllFF(t *testing.T) {
	end := prefixRangeEnd([]byte{0xff, 0xff})
	if len(end) != 1 || end[0] != 0 {
		t.Errorf("prefixRangeEnd(all-0xff) = %v, want [0]", end)
	}
}

// TestPrefixRangeEnd_Empty verifies the empty-prefix branch returns nil.
func TestPrefixRangeEnd_Empty(t *testing.T) {
	if end := prefixRangeEnd(nil); end != nil {
		t.Errorf("prefixRangeEnd(nil) = %v, want nil", end)
	}
}
