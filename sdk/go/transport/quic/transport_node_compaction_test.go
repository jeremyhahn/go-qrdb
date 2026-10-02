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
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_NodeCompaction_SetAndRead(t *testing.T) {
	srv, tr := startE2EServer(t)
	var sent *pb.DatabaseCompactionConfig
	srv.SetResponder("admin.SetNodeCompaction", func(payload []byte) (uint16, []byte) {
		var req pb.SetNodeCompactionRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		sent = req.GetCompaction()
		return wire.StatusOK, mustMarshal(&pb.NodeCompactionResponse{Compaction: sent})
	})
	srv.SetResponder("admin.GetNodeCompaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.NodeCompactionResponse{Compaction: sent})
	})
	ctx := context.Background()

	set, err := tr.SetNodeCompaction(ctx, &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 4})
	if err != nil || set == nil || set.RetainRevisions != 4 {
		t.Fatalf("SetNodeCompaction = %+v, %v; want the revision policy", set, err)
	}
	if sent.GetMode() != "revision" || sent.GetRetainRevisions() != 4 {
		t.Fatalf("the server received %v; want the revision policy", sent)
	}
	if got, err := tr.NodeCompaction(ctx); err != nil || got == nil || got.Mode != "revision" {
		t.Fatalf("NodeCompaction = %+v, %v; want the policy set", got, err)
	}
	if off, err := tr.SetNodeCompaction(ctx, nil); err != nil || off != nil || sent != nil {
		t.Fatalf("SetNodeCompaction(nil) = %+v, %v (sent %v); want off", off, err, sent)
	}
}

func TestE2E_NodeCompaction_Failures(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.GetNodeCompaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusPermissionDenied, []byte("permission denied")
	})
	srv.SetResponder("admin.SetNodeCompaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte{0xff, 0xff}
	})
	ctx := context.Background()
	if _, err := tr.NodeCompaction(ctx); err == nil {
		t.Fatal("a refused NodeCompaction succeeded")
	}
	if _, err := tr.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("an undecodable SetNodeCompaction answer was accepted")
	}
	_ = tr.Close()
	if _, err := tr.NodeCompaction(ctx); err == nil {
		t.Fatal("NodeCompaction on a closed transport succeeded")
	}
	if _, err := tr.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("SetNodeCompaction on a closed transport succeeded")
	}
}
