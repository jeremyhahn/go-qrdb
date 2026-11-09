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

package tenantconv

import (
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A set status reaches the wire; a nil one stays absent ("unchanged").
func TestUpdateRequest_Status(t *testing.T) {
	s := transport.TenantStatusSuspended
	set := UpdateRequest(&transport.UpdateTenantRequest{TenantID: "acme", Status: &s})
	if set.Status == nil || set.GetStatus() != "suspended" {
		t.Fatalf("set request status = %v; want suspended", set.Status)
	}
	unchanged := UpdateRequest(&transport.UpdateTenantRequest{TenantID: "acme"})
	if unchanged.Status != nil {
		t.Fatalf("unchanged request status = %q; want absent", unchanged.GetStatus())
	}
}

// TestUpdateRequest_IsolationPolicy proves a set isolation policy reaches the
// wire request as its own copy, and an unset one stays absent so the update
// leaves the tenant's policy unchanged.
func TestUpdateRequest_IsolationPolicy(t *testing.T) {
	p := "strict"
	set := UpdateRequest(&transport.UpdateTenantRequest{TenantID: "acme", IsolationPolicy: &p})
	if set.IsolationPolicy == nil || set.GetIsolationPolicy() != "strict" {
		t.Fatalf("set request isolation policy = %v; want strict", set.IsolationPolicy)
	}
	if set.IsolationPolicy == &p {
		t.Fatal("set request shares the caller's isolation policy pointer; want a copy")
	}
	unchanged := UpdateRequest(&transport.UpdateTenantRequest{TenantID: "acme"})
	if unchanged.IsolationPolicy != nil {
		t.Fatalf("unchanged request isolation policy = %q; want absent", unchanged.GetIsolationPolicy())
	}
}

// The read carries the status the server answered, and none when it sent none.
func TestToTenantInfo_Status(t *testing.T) {
	if got := ToTenantInfo(&pb.TenantResponse{Id: "acme", Status: "disabled"}).Status; got != transport.TenantStatusDisabled {
		t.Fatalf("Status = %q; want disabled", got)
	}
	if got := ToTenantInfo(&pb.TenantResponse{Id: "acme"}).Status; got != "" {
		t.Fatalf("Status = %q; want empty", got)
	}
}

// TestToTenantInfo_IsolationPolicy proves the protobuf transports (gRPC,
// QUIC, unix) carry the tenant's isolation policy, and leave it empty when
// the tenant inherits the cluster floor.
func TestToTenantInfo_IsolationPolicy(t *testing.T) {
	if got := ToTenantInfo(&pb.TenantResponse{Id: "acme", IsolationPolicy: "strict"}).IsolationPolicy; got != "strict" {
		t.Fatalf("IsolationPolicy = %q; want strict", got)
	}
	if got := ToTenantInfo(&pb.TenantResponse{Id: "acme"}).IsolationPolicy; got != "" {
		t.Fatalf("IsolationPolicy = %q; want empty", got)
	}
}
