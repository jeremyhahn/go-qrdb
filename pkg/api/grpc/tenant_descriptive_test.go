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
	"context"
	"log/slog"
	"maps"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// descriptiveGRPCServer returns a server over the REAL seal-disabled tenant
// service holding one tenant, "acme".
func descriptiveGRPCServer(t *testing.T) *qrdbServiceServer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	return &qrdbServiceServer{tenantSvc: svc}
}

func TestGRPC_TenantUpdate_DescriptionAndMetadataRoundTrip(t *testing.T) {
	s := descriptiveGRPCServer(t)
	ctx := context.Background()
	want := map[string]string{"env": "prod", "team": "storage"}

	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{
		Id:          "acme",
		Description: proto.String("Acme production"),
		Metadata:    &pb.TenantMetadata{Entries: want},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.TenantGet(ctx, &pb.TenantGetRequest{Id: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetDescription() != "Acme production" || !maps.Equal(got.GetMetadata(), want) {
		t.Fatalf("get = description %q, metadata %v; want %q, %v", got.GetDescription(), got.GetMetadata(), "Acme production", want)
	}

	// Absent fields leave the stored values unchanged.
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", Name: "Acme Inc"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.TenantGet(ctx, &pb.TenantGetRequest{Id: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetDescription() != "Acme production" || !maps.Equal(got.GetMetadata(), want) {
		t.Fatalf("rename changed descriptive fields: %q, %v", got.GetDescription(), got.GetMetadata())
	}
}

func TestGRPC_TenantUpdate_EmptyMetadataMessageClears(t *testing.T) {
	s := descriptiveGRPCServer(t)
	ctx := context.Background()
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{
		Id:          "acme",
		Description: proto.String("to clear"),
		Metadata:    &pb.TenantMetadata{Entries: map[string]string{"env": "prod"}},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{
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

func TestGRPC_TenantUpdate_OverLimitIsInvalidArgument(t *testing.T) {
	s := descriptiveGRPCServer(t)
	ctx := context.Background()
	cases := map[string]*pb.TenantUpdateRequest{
		"description": {Id: "acme", Description: proto.String(strings.Repeat("d", tenant.MaxDescriptionBytes+1))},
		"metadata value": {Id: "acme", Metadata: &pb.TenantMetadata{
			Entries: map[string]string{"k": strings.Repeat("v", tenant.MaxMetadataValueBytes+1)},
		}},
	}
	for name, req := range cases {
		if _, err := s.TenantUpdate(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s over limit: code = %s; want InvalidArgument (%v)", name, status.Code(err), err)
		}
	}
	got, err := s.TenantGet(ctx, &pb.TenantGetRequest{Id: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetDescription() != "" || len(got.GetMetadata()) != 0 {
		t.Fatalf("refused update was stored: %q, %v", got.GetDescription(), got.GetMetadata())
	}
}
