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
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The update request carries status, the read returns it, and an absent
// status leaves it unchanged.
func TestHandleTenantUpdate_StatusRoundTrip(t *testing.T) {
	srv := descriptiveQUICServer(t)
	if got := quicTenantGet(t, srv, "acme").GetStatus(); got != string(tenant.StatusActive) {
		t.Fatalf("new tenant status = %q; want active", got)
	}
	for _, want := range []tenant.Status{tenant.StatusSuspended, tenant.StatusDisabled, tenant.StatusActive} {
		resp, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String(string(want))})
		if err != nil || resp.GetStatus() != string(want) {
			t.Fatalf("update to %s = %v, %v", want, resp, err)
		}
		if got := quicTenantGet(t, srv, "acme").GetStatus(); got != string(want) {
			t.Fatalf("read after %s = %q", want, got)
		}
	}
	if _, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("suspended")}); err != nil {
		t.Fatal(err)
	}
	resp, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", Description: proto.String("d")})
	if err != nil || resp.GetStatus() != string(tenant.StatusSuspended) {
		t.Fatalf("absent status changed it: %v, %v", resp, err)
	}
}

// An unknown status is the caller's error; a transition the table does not
// allow depends on the tenant's state. Neither is stored.
func TestHandleTenantUpdate_StatusRefusals(t *testing.T) {
	srv := descriptiveQUICServer(t)
	_, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("paused")})
	if code, _ := mapServiceError(err); code != uint16(wire.StatusInvalidArgument) {
		t.Fatalf("unknown status maps to %d; want StatusInvalidArgument (%v)", code, err)
	}
	if _, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("disabled")}); err != nil {
		t.Fatal(err)
	}
	_, err = quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("suspended")})
	if code, _ := mapServiceError(err); code != uint16(wire.StatusFailedPrecondition) {
		t.Fatalf("disabled -> suspended maps to %d; want StatusFailedPrecondition (%v)", code, err)
	}
	if got := quicTenantGet(t, srv, "acme").GetStatus(); got != string(tenant.StatusDisabled) {
		t.Fatalf("refused transition was stored: %q", got)
	}
}
