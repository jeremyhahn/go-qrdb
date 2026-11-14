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

// TestGRPC_ClusterIsolationPolicy_TightenOnly pins the floor RPCs: an unset
// floor reads as operator-access, it tightens to strict, and a loosening is
// FAILED_PRECONDITION with the floor unchanged.
// strictIsolationActiveGRPCServer is descriptiveGRPCServer with the
// strict-isolation gate reporting active, which this binary cannot reach
// in production: it exercises the verbs' success paths.
func strictIsolationActiveGRPCServer(t *testing.T) *qrdbServiceServer {
	t.Helper()
	s := descriptiveGRPCServer(t)
	s.tenantSvc.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })
	return s
}

// TestGRPC_IsolationPolicyVerbs_RefusedBeforeStrictIsolation pins that
// before strict-isolation is active the floor's get and set and a tenant
// update carrying isolation_policy answer Unavailable and change nothing.
func TestGRPC_IsolationPolicyVerbs_RefusedBeforeStrictIsolation(t *testing.T) {
	s := descriptiveGRPCServer(t)
	ctx := context.Background()
	_, getErr := s.GetClusterIsolationPolicy(ctx, &pb.GetClusterIsolationPolicyRequest{})
	_, setErr := s.SetClusterIsolationPolicy(ctx, &pb.SetClusterIsolationPolicyRequest{Policy: "strict"})
	_, updErr := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("strict")})
	for name, err := range map[string]error{"get floor": getErr, "set floor": setErr, "tenant strict": updErr} {
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("%s before activation: code = %s; want Unavailable (%v)", name, status.Code(err), err)
		}
	}
	got, err := s.TenantGet(ctx, &pb.TenantGetRequest{Id: "acme"})
	if err != nil || got.GetIsolationPolicy() != "" {
		t.Fatalf("isolation_policy after a refused update = %q, %v; want inherit", got.GetIsolationPolicy(), err)
	}
	s.tenantSvc.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })
	floor, err := s.GetClusterIsolationPolicy(ctx, &pb.GetClusterIsolationPolicyRequest{})
	if err != nil || floor.GetPolicy() != string(tenant.ClusterIsolationPolicyOperatorAccess) {
		t.Fatalf("floor after a refused set = %q, %v; want operator-access", floor.GetPolicy(), err)
	}
}

func TestGRPC_ClusterIsolationPolicy_TightenOnly(t *testing.T) {
	s := strictIsolationActiveGRPCServer(t)
	ctx := context.Background()
	got, err := s.GetClusterIsolationPolicy(ctx, &pb.GetClusterIsolationPolicyRequest{})
	if err != nil || got.GetPolicy() != string(tenant.ClusterIsolationPolicyOperatorAccess) {
		t.Fatalf("unset floor = %q, %v; want operator-access", got.GetPolicy(), err)
	}
	got, err = s.SetClusterIsolationPolicy(ctx, &pb.SetClusterIsolationPolicyRequest{Policy: "strict"})
	if err != nil || got.GetPolicy() != string(tenant.ClusterIsolationPolicyStrict) {
		t.Fatalf("set strict = %q, %v", got.GetPolicy(), err)
	}
	_, err = s.SetClusterIsolationPolicy(ctx, &pb.SetClusterIsolationPolicyRequest{Policy: "loose"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown policy: code = %s; want InvalidArgument (%v)", status.Code(err), err)
	}
	_, err = s.SetClusterIsolationPolicy(ctx, &pb.SetClusterIsolationPolicyRequest{Policy: "operator-access"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("loosening: code = %s; want FailedPrecondition (%v)", status.Code(err), err)
	}
	got, err = s.GetClusterIsolationPolicy(ctx, &pb.GetClusterIsolationPolicyRequest{})
	if err != nil || got.GetPolicy() != string(tenant.ClusterIsolationPolicyStrict) {
		t.Fatalf("floor after refused loosening = %q, %v; want strict", got.GetPolicy(), err)
	}
}

func TestGRPC_ClusterIsolationPolicy_NoService(t *testing.T) {
	s := &qrdbServiceServer{}
	ctx := context.Background()
	if _, err := s.GetClusterIsolationPolicy(ctx, &pb.GetClusterIsolationPolicyRequest{}); err == nil {
		t.Fatal("get without a tenant service: want an error")
	}
	if _, err := s.SetClusterIsolationPolicy(ctx, &pb.SetClusterIsolationPolicyRequest{Policy: "strict"}); err == nil {
		t.Fatal("set without a tenant service: want an error")
	}
}

// TestGRPC_TenantUpdate_IsolationPolicy pins the tenant's own policy:
// inherit to strict is allowed and shows on TenantGet; strict back to
// inherit ("") is FAILED_PRECONDITION and leaves the tenant strict.
func TestGRPC_TenantUpdate_IsolationPolicy(t *testing.T) {
	s := strictIsolationActiveGRPCServer(t)
	ctx := context.Background()
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("")}); err != nil {
		t.Fatalf("inherit on an inherit tenant: %v", err)
	}
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("loose")}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown isolation_policy: code = %s; want InvalidArgument (%v)", status.Code(err), err)
	}
	resp, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("strict")})
	if err != nil || resp.GetIsolationPolicy() != "strict" {
		t.Fatalf("strict = %q, %v", resp.GetIsolationPolicy(), err)
	}
	_, err = s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("")})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("strict to inherit: code = %s; want FailedPrecondition (%v)", status.Code(err), err)
	}
	got, err := s.TenantGet(ctx, &pb.TenantGetRequest{Id: "acme"})
	if err != nil || got.GetIsolationPolicy() != "strict" {
		t.Fatalf("isolation_policy after refused loosening = %q, %v; want strict", got.GetIsolationPolicy(), err)
	}
	if _, err := s.TenantUpdate(ctx, &pb.TenantUpdateRequest{Id: "missing", IsolationPolicy: proto.String("")}); err == nil {
		t.Fatal("inherit on a missing tenant: want an error")
	}
}
