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

package quic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_ReplicaStates_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	var gotShard uint64
	srv.SetResponder("admin.GetReplicaStates", func(payload []byte) (uint16, []byte) {
		var req pb.GetReplicaStatesRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		gotShard = req.GetShardId()
		return wire.StatusOK, mustMarshal(&pb.ReplicaStatesResponse{NodeId: "n2", Replicas: []*pb.ReplicaStateProto{
			{ShardId: 1, ReplicaId: 2, AppliedIndex: 48211, Hash: 1<<63 + 5},
		}})
	})
	resp, err := tr.ReplicaStates(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if gotShard != 1 {
		t.Fatalf("shard on the wire = %d, want 1", gotShard)
	}
	want := &transport.ReplicaStatesResponse{NodeID: "n2", Replicas: []transport.ReplicaState{
		{ShardID: 1, ReplicaID: 2, AppliedIndex: 48211, Hash: 1<<63 + 5},
	}}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("ReplicaStates = %+v, want %+v", resp, want)
	}
}

func TestE2E_ReplicaStates_UnsupportedIsCapabilityUnsupported(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.GetReplicaStates", func(_ []byte) (uint16, []byte) {
		return wire.StatusUnimplemented, []byte("replica state read is not supported on a standalone node")
	})
	_, err := tr.ReplicaStates(context.Background(), 0)
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *transport.CapabilityUnsupportedError", err, err)
	}
}

func TestE2E_ReplicaStates_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.GetReplicaStates", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.ReplicaStates(context.Background(), 0)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.Method != "ReplicaStates" {
		t.Fatalf("err = %v (%T), want *transport.RequestError for ReplicaStates", err, err)
	}
}

func TestTransport_ReplicaStates_Closed(t *testing.T) {
	_, tr := startE2EServer(t)
	_ = tr.Close()
	_, err := tr.ReplicaStates(context.Background(), 0)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}
