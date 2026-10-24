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
	"log/slog"
	"maps"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// descriptiveQUICServer returns a server over the REAL seal-disabled tenant
// service holding one tenant, "acme".
func descriptiveQUICServer(t *testing.T) *Server {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	return newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
}

// quicTenantUpdate sends req through handleTenantUpdate and decodes the reply.
func quicTenantUpdate(t *testing.T, srv *Server, req *pb.TenantUpdateRequest) (*pb.TenantResponse, error) {
	t.Helper()
	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := srv.handleTenantUpdate(context.Background(), payload)
	if err != nil {
		return nil, err
	}
	var resp pb.TenantResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return &resp, nil
}

// quicTenantGet reads tenant id through handleTenantGet.
func quicTenantGet(t *testing.T, srv *Server, id string) *pb.TenantResponse {
	t.Helper()
	payload, err := proto.Marshal(&pb.TenantGetRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	out, err := srv.handleTenantGet(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	var resp pb.TenantResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return &resp
}

func TestHandleTenantUpdate_DescriptionAndMetadataRoundTrip(t *testing.T) {
	srv := descriptiveQUICServer(t)
	want := map[string]string{"env": "prod", "team": "storage"}

	if _, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{
		Id:          "acme",
		Description: proto.String("Acme production"),
		Metadata:    &pb.TenantMetadata{Entries: want},
	}); err != nil {
		t.Fatal(err)
	}
	got := quicTenantGet(t, srv, "acme")
	if got.GetDescription() != "Acme production" || !maps.Equal(got.GetMetadata(), want) {
		t.Fatalf("get = description %q, metadata %v; want %q, %v", got.GetDescription(), got.GetMetadata(), "Acme production", want)
	}

	// Absent fields leave the stored values unchanged.
	if _, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", Name: "Acme Inc"}); err != nil {
		t.Fatal(err)
	}
	got = quicTenantGet(t, srv, "acme")
	if got.GetDescription() != "Acme production" || !maps.Equal(got.GetMetadata(), want) {
		t.Fatalf("rename changed descriptive fields: %q, %v", got.GetDescription(), got.GetMetadata())
	}
}

func TestHandleTenantUpdate_EmptyMetadataMessageClears(t *testing.T) {
	srv := descriptiveQUICServer(t)
	if _, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{
		Id:          "acme",
		Description: proto.String("to clear"),
		Metadata:    &pb.TenantMetadata{Entries: map[string]string{"env": "prod"}},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{
		Id:          "acme",
		Description: proto.String(""),
		Metadata:    &pb.TenantMetadata{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetDescription() != "" || len(resp.GetMetadata()) != 0 {
		t.Fatalf("cleared tenant = description %q, metadata %v; want both empty", resp.GetDescription(), resp.GetMetadata())
	}
}

func TestHandleTenantUpdate_OverLimitIsInvalidArgument(t *testing.T) {
	srv := descriptiveQUICServer(t)
	cases := map[string]*pb.TenantUpdateRequest{
		"description": {Id: "acme", Description: proto.String(strings.Repeat("d", tenant.MaxDescriptionBytes+1))},
		"metadata key": {Id: "acme", Metadata: &pb.TenantMetadata{
			Entries: map[string]string{strings.Repeat("k", tenant.MaxMetadataKeyBytes+1): "v"},
		}},
	}
	for name, req := range cases {
		_, err := quicTenantUpdate(t, srv, req)
		if code, _ := mapServiceError(err); code != uint16(wire.StatusInvalidArgument) {
			t.Errorf("%s over limit maps to %d; want StatusInvalidArgument (%v)", name, code, err)
		}
	}
	got := quicTenantGet(t, srv, "acme")
	if got.GetDescription() != "" || len(got.GetMetadata()) != 0 {
		t.Fatalf("refused update was stored: %q, %v", got.GetDescription(), got.GetMetadata())
	}
}
