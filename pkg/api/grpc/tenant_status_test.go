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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The update request carries status, the read returns it, and an absent
// status leaves it unchanged.
func TestGRPC_TenantUpdate_StatusRoundTrip(t *testing.T) {
	s := descriptiveGRPCServer(t)
	ctx := context.Background()
	got, err := s.TenantGet(ctx, &pb.TenantGetRequest{Id: "acme"})
	if err != nil || got.GetStatus() != string(tenant.StatusActive) {
		t.Fatalf("new tenant status = %q, %v; want active", got.GetStatus(), err)
	}
	for _, want := range []tenant.Status{tenant.StatusSuspended, tenant.StatusDisabled, tenant.StatusActive} {
		resp, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String(string(want))})
		if err != nil || resp.GetStatus() != string(want) {
			t.Fatalf("update to %s = %q, %v", want, resp.GetStatus(), err)
		}
		read, err := s.TenantGet(ctx, &pb.TenantGetRequest{Id: "acme"})
		if err != nil || read.GetStatus() != string(want) {
			t.Fatalf("read after %s = %q, %v", want, read.GetStatus(), err)
		}
	}
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("suspended")}); err != nil {
		t.Fatal(err)
	}
	resp, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", Description: proto.String("d")})
	if err != nil || resp.GetStatus() != string(tenant.StatusSuspended) {
		t.Fatalf("absent status changed it: %q, %v", resp.GetStatus(), err)
	}
}

// An unknown status is the caller's error; a transition the table does not
// allow depends on the tenant's state. Neither is stored.
func TestGRPC_TenantUpdate_StatusRefusals(t *testing.T) {
	s := descriptiveGRPCServer(t)
	ctx := context.Background()
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("paused")}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown status: code = %s; want InvalidArgument (%v)", status.Code(err), err)
	}
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("disabled")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("suspended")}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("disabled -> suspended: code = %s; want FailedPrecondition (%v)", status.Code(err), err)
	}
	got, err := s.TenantGet(ctx, &pb.TenantGetRequest{Id: "acme"})
	if err != nil || got.GetStatus() != string(tenant.StatusDisabled) {
		t.Fatalf("refused transition was stored: %q, %v", got.GetStatus(), err)
	}
}
