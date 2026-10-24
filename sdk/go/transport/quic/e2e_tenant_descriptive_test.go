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
	"maps"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The server side of these fields (patch semantics, limits) is tested
// against the real tenant service in pkg/api/quic; this covers what the SDK
// puts on the wire and reads back.

func TestE2E_UpdateTenant_DescriptiveFieldsOnTheWire(t *testing.T) {
	srv, tr := startE2EServer(t)
	// The responder runs on the server's goroutine; the channel hands each
	// decoded request to the test.
	requests := make(chan *pb.TenantUpdateRequest, 1)
	srv.SetResponder("tenant.Update", func(payload []byte) (uint16, []byte) {
		req := &pb.TenantUpdateRequest{}
		if err := proto.Unmarshal(payload, req); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		requests <- req
		return wire.StatusOK, mustMarshal(&pb.TenantResponse{
			Id:          req.GetId(),
			Description: req.GetDescription(),
			Metadata:    req.GetMetadata().GetEntries(),
		})
	})
	desc := "Acme production"
	want := map[string]string{"env": "prod"}

	resp, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "acme", Description: &desc, Metadata: want})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	sent := <-requests
	if sent.Description == nil || !maps.Equal(sent.GetMetadata().GetEntries(), want) {
		t.Fatalf("sent = %v; want description and metadata present", sent)
	}
	if resp.Tenant.Description != desc || !maps.Equal(resp.Tenant.Metadata, want) {
		t.Fatalf("resp = %q, %v; want %q, %v", resp.Tenant.Description, resp.Tenant.Metadata, desc, want)
	}

	// Nil fields stay absent: "unchanged".
	if _, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "acme"}); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if sent = <-requests; sent.Description != nil || sent.Metadata != nil {
		t.Fatalf("sent = %v; want description and metadata absent", sent)
	}

	// An empty map is a present, empty message: "clear".
	if _, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "acme", Metadata: map[string]string{}}); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if sent = <-requests; sent.Metadata == nil || len(sent.GetMetadata().GetEntries()) != 0 {
		t.Fatalf("sent = %v; want a present, empty metadata message", sent)
	}
}
