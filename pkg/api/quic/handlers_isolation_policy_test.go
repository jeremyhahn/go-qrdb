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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func quicFloorGet(t *testing.T, srv *Server) string {
	t.Helper()
	out, err := srv.handleClusterIsolationPolicyGet(context.Background(), nil)
	if err != nil {
		t.Fatalf("floor get: %v", err)
	}
	var resp pb.ClusterIsolationPolicyResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.GetPolicy()
}

func quicFloorSet(t *testing.T, srv *Server, policy string) error {
	t.Helper()
	payload, err := proto.Marshal(&pb.SetClusterIsolationPolicyRequest{Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.handleClusterIsolationPolicySet(context.Background(), payload)
	return err
}

// strictIsolationActiveQUICServer is descriptiveQUICServer with the
// strict-isolation gate reporting active, which this binary cannot reach
// in production: it exercises the verbs' success paths.
func strictIsolationActiveQUICServer(t *testing.T) *Server {
	t.Helper()
	srv := descriptiveQUICServer(t)
	srv.tenantSvc.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })
	return srv
}

// TestIsolationPolicyVerbs_RefusedBeforeStrictIsolation pins that before
// strict-isolation is active the floor's get and set and a tenant update
// carrying isolation_policy answer UNAVAILABLE and change nothing.
func TestIsolationPolicyVerbs_RefusedBeforeStrictIsolation(t *testing.T) {
	srv := descriptiveQUICServer(t)
	_, getErr := srv.handleClusterIsolationPolicyGet(context.Background(), nil)
	_, updErr := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("strict")})
	for name, err := range map[string]error{"get floor": getErr, "set floor": quicFloorSet(t, srv, "strict"), "tenant strict": updErr} {
		if code, _ := mapServiceError(err); code != wire.StatusUnavailable {
			t.Fatalf("%s before activation: code %d (%v); want UNAVAILABLE", name, code, err)
		}
	}
	if got := quicTenantGet(t, srv, "acme"); got.GetIsolationPolicy() != "" {
		t.Fatalf("isolation_policy after a refused update = %q; want inherit", got.GetIsolationPolicy())
	}
	srv.tenantSvc.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })
	if got := quicFloorGet(t, srv); got != string(tenant.ClusterIsolationPolicyOperatorAccess) {
		t.Fatalf("floor after a refused set = %q; want operator-access", got)
	}
}

// TestClusterIsolationPolicy_TightenOnly pins cluster.IsolationPolicyGet/Set:
// an unset floor reads as operator-access, it tightens to strict, and a
// loosening is FAILED_PRECONDITION with the floor unchanged.
func TestClusterIsolationPolicy_TightenOnly(t *testing.T) {
	srv := strictIsolationActiveQUICServer(t)
	if got := quicFloorGet(t, srv); got != string(tenant.ClusterIsolationPolicyOperatorAccess) {
		t.Fatalf("unset floor = %q; want operator-access", got)
	}
	if err := quicFloorSet(t, srv, "strict"); err != nil {
		t.Fatalf("set strict: %v", err)
	}
	if code, _ := mapServiceError(quicFloorSet(t, srv, "loose")); code != wire.StatusInvalidArgument {
		t.Fatalf("unknown policy: code %d; want INVALID_ARGUMENT", code)
	}
	err := quicFloorSet(t, srv, "operator-access")
	if code, _ := mapServiceError(err); code != uint16(wire.StatusFailedPrecondition) {
		t.Fatalf("loosening: code %d (%v); want FAILED_PRECONDITION", code, err)
	}
	if got := quicFloorGet(t, srv); got != string(tenant.ClusterIsolationPolicyStrict) {
		t.Fatalf("floor after refused loosening = %q; want strict", got)
	}
}

func TestClusterIsolationPolicy_BadPayloadAndNoService(t *testing.T) {
	srv := descriptiveQUICServer(t)
	if _, err := srv.handleClusterIsolationPolicySet(context.Background(), []byte{0xff}); err == nil {
		t.Fatal("malformed payload: want an error")
	}
	bare := newTestServer(&mockKVService{}, &mockAdminService{})
	if _, err := bare.handleClusterIsolationPolicyGet(context.Background(), nil); err == nil {
		t.Fatal("get without a tenant service: want an error")
	}
	if err := quicFloorSet(t, bare, "strict"); err == nil {
		t.Fatal("set without a tenant service: want an error")
	}
}

// TestHandleTenantUpdate_IsolationPolicy pins isolation_policy on
// tenant.Update: inherit to strict shows on tenant.Get; strict back to
// inherit is FAILED_PRECONDITION and leaves the tenant strict.
func TestHandleTenantUpdate_IsolationPolicy(t *testing.T) {
	srv := strictIsolationActiveQUICServer(t)
	_, loose := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("loose")})
	if code, _ := mapServiceError(loose); code != wire.StatusInvalidArgument {
		t.Fatalf("unknown isolation_policy: code %d (%v); want INVALID_ARGUMENT", code, loose)
	}
	if _, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("strict")}); err != nil {
		t.Fatalf("strict: %v", err)
	}
	if got := quicTenantGet(t, srv, "acme"); got.GetIsolationPolicy() != "strict" {
		t.Fatalf("isolation_policy = %q; want strict", got.GetIsolationPolicy())
	}
	_, err := quicTenantUpdate(t, srv, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("")})
	if code, _ := mapServiceError(err); code != uint16(wire.StatusFailedPrecondition) {
		t.Fatalf("strict to inherit: code %d (%v); want FAILED_PRECONDITION", code, err)
	}
	if got := quicTenantGet(t, srv, "acme"); got.GetIsolationPolicy() != "strict" {
		t.Fatalf("isolation_policy after refused loosening = %q; want strict", got.GetIsolationPolicy())
	}
}
