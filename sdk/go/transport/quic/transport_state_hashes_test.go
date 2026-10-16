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
	"slices"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_StateHashes_SelectsAndConverts(t *testing.T) {
	srv, tr := startE2EServer(t)
	// asked is written on the server's stream goroutine and read here.
	var asked atomic.Pointer[[]uint64]
	srv.SetResponder("admin.GetStateHashes", func(payload []byte) (uint16, []byte) {
		var req pb.GetStateHashesRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		ids := req.GetShardIds()
		asked.Store(&ids)
		return wire.StatusOK, mustMarshal(&pb.GetStateHashesResponse{
			NodeId:   "node-a",
			Replicas: []*pb.ReplicaStateHash{{TenantId: "acme", ShardId: 3, ReplicaId: 2, AppliedIndex: 9, Records: 4, Hash: "ab"}},
		})
	})
	ctx := context.Background()

	got, err := tr.StateHashes(ctx, []uint64{3, 7})
	if err != nil {
		t.Fatalf("StateHashes: %v", err)
	}
	if ids := asked.Load(); ids == nil || !slices.Equal(*ids, []uint64{3, 7}) {
		t.Fatalf("the server was asked for %v, want [3 7]", ids)
	}
	want := transport.ReplicaStateHash{TenantID: "acme", ShardID: 3, ReplicaID: 2, AppliedIndex: 9, Records: 4, Hash: "ab"}
	if got.NodeID != "node-a" || len(got.Replicas) != 1 || got.Replicas[0] != want {
		t.Fatalf("StateHashes = %+v, want node-a [%+v]", got, want)
	}
	if _, err := tr.StateHashes(ctx, nil); err != nil || len(*asked.Load()) != 0 {
		t.Fatalf("StateHashes(nil) = %v (asked %v); want every shard", err, *asked.Load())
	}
}

func TestE2E_StateHashes_Failures(t *testing.T) {
	srv, tr := startE2EServer(t)
	var refuse atomic.Bool
	refuse.Store(true)
	srv.SetResponder("admin.GetStateHashes", func(_ []byte) (uint16, []byte) {
		if refuse.Load() {
			return wire.StatusPermissionDenied, []byte("permission denied")
		}
		return wire.StatusOK, []byte{0xff, 0xff}
	})
	ctx := context.Background()
	if _, err := tr.StateHashes(ctx, nil); err == nil {
		t.Fatal("a refused StateHashes succeeded")
	}
	refuse.Store(false)
	if _, err := tr.StateHashes(ctx, nil); err == nil {
		t.Fatal("an undecodable StateHashes answer was accepted")
	}
	_ = tr.Close()
	var closed *transport.ClosedError
	if _, err := tr.StateHashes(ctx, nil); !errors.As(err, &closed) {
		t.Fatalf("StateHashes on a closed transport = %v, want *transport.ClosedError", err)
	}
}
