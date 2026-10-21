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
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const grpcPolicyTenant = "acme"

// grpcPolicyStores resolves each tenant to its own memory store; an
// unknown tenant is sealed.
type grpcPolicyStores map[string]kvstore.KVStore

func (p grpcPolicyStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if s, ok := p[tenantID]; ok {
		return s, nil
	}
	return nil, tenant.ErrTenantSealed
}

func newPolicyServiceServer(t *testing.T) (*qrdbServiceServer, context.Context) {
	t.Helper()
	svc, err := service.NewPolicyService(grpcPolicyStores{grpcPolicyTenant: kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatal(err)
	}
	return &qrdbServiceServer{policySvc: svc}, tenant.WithTenantID(context.Background(), grpcPolicyTenant)
}

func grpcTestPolicy(name string, enabled *bool) *pb.ABACPolicy {
	return &pb.ABACPolicy{
		ApiVersion: "abac.qrdb.io/v1",
		Kind:       "Policy",
		Metadata:   &pb.ABACPolicyMetadata{Name: name, Description: "test policy"},
		Spec: &pb.ABACPolicySpec{
			Enabled:  enabled,
			Subjects: &pb.ABACSubjectSelector{Roles: []string{"auditor"}},
			Rules: []*pb.ABACRule{{
				Name:      "deny-secrets",
				Effect:    "deny",
				Actions:   []string{"read"},
				Resources: []string{"/secrets/**"},
			}},
		},
	}
}

// TestABACPolicyRPCsRoundTrip drives create, get, list, update and delete
// through the gRPC handlers, pinning that spec.enabled keeps its presence on
// every read: unset stays unset and false stays false.
func TestABACPolicyRPCsRoundTrip(t *testing.T) {
	s, ctx := newPolicyServiceServer(t)

	created, err := s.CreateABACPolicy(ctx, grpcTestPolicy("unset", nil))
	if err != nil {
		t.Fatalf("CreateABACPolicy: %v", err)
	}
	if created.GetSpec().Enabled != nil {
		t.Fatalf("created enabled = %v, want unset", *created.GetSpec().Enabled)
	}
	if created.GetMetadata().GetTenant() != grpcPolicyTenant || created.GetMetadata().GetCreated() == "" {
		t.Fatalf("created metadata = %+v, want tenant and created stamped", created.GetMetadata())
	}
	if _, err := s.CreateABACPolicy(ctx, grpcTestPolicy("off", proto.Bool(false))); err != nil {
		t.Fatalf("CreateABACPolicy(off): %v", err)
	}

	got, err := s.GetABACPolicy(ctx, &pb.ABACPolicyRequest{Name: "unset"})
	if err != nil {
		t.Fatalf("GetABACPolicy: %v", err)
	}
	if got.GetSpec().Enabled != nil {
		t.Fatal("get returned enabled for a policy written without it")
	}
	off, err := s.GetABACPolicy(ctx, &pb.ABACPolicyRequest{TenantId: grpcPolicyTenant, Name: "off"})
	if err != nil {
		t.Fatalf("GetABACPolicy(off): %v", err)
	}
	if off.GetSpec().Enabled == nil || off.GetSpec().GetEnabled() {
		t.Fatal("get lost enabled=false")
	}

	list, err := s.ListABACPolicies(ctx, &pb.ABACPolicyListRequest{})
	if err != nil {
		t.Fatalf("ListABACPolicies: %v", err)
	}
	if n := len(list.GetPolicies()); n != 2 || list.GetPolicies()[0].GetMetadata().GetName() != "off" {
		t.Fatalf("list = %d policies, want [off unset]", n)
	}
	if list.GetPolicies()[1].GetSpec().Enabled != nil {
		t.Fatal("list returned enabled for a policy written without it")
	}

	updated, err := s.UpdateABACPolicy(ctx, grpcTestPolicy("unset", proto.Bool(true)))
	if err != nil {
		t.Fatalf("UpdateABACPolicy: %v", err)
	}
	if !updated.GetSpec().GetEnabled() || updated.GetMetadata().GetCreated() != created.GetMetadata().GetCreated() {
		t.Fatalf("update = enabled %v created %s, want true and the original creation time", updated.GetSpec().GetEnabled(), updated.GetMetadata().GetCreated())
	}

	if _, err := s.DeleteABACPolicy(ctx, &pb.ABACPolicyRequest{Name: "unset"}); err != nil {
		t.Fatalf("DeleteABACPolicy: %v", err)
	}
	if _, err := s.GetABACPolicy(ctx, &pb.ABACPolicyRequest{Name: "unset"}); status.Code(err) != codes.NotFound {
		t.Fatalf("get after delete: %v, want NotFound", err)
	}
}

// TestABACPolicyRPCsMapRefusals pins the status code of every service
// refusal the handlers render.
func TestABACPolicyRPCsMapRefusals(t *testing.T) {
	s, ctx := newPolicyServiceServer(t)
	if _, err := s.CreateABACPolicy(ctx, grpcTestPolicy("p", nil)); err != nil {
		t.Fatalf("CreateABACPolicy: %v", err)
	}
	badMatch := grpcTestPolicy("q", nil)
	badMatch.Spec.Subjects.MatchJson = "not json"
	badTime := grpcTestPolicy("q", nil)
	badTime.Metadata.Created = "yesterday"
	badEffect := grpcTestPolicy("q", nil)
	badEffect.Spec.Rules[0].Effect = "maybe"

	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"create exists", second(s.CreateABACPolicy(ctx, grpcTestPolicy("p", nil))), codes.AlreadyExists},
		{"create nil", second(s.CreateABACPolicy(ctx, nil)), codes.InvalidArgument},
		{"create bad match", second(s.CreateABACPolicy(ctx, badMatch)), codes.InvalidArgument},
		{"create bad time", second(s.CreateABACPolicy(ctx, badTime)), codes.InvalidArgument},
		{"create uncompilable", second(s.CreateABACPolicy(ctx, badEffect)), codes.InvalidArgument},
		{"get missing", second(s.GetABACPolicy(ctx, &pb.ABACPolicyRequest{Name: "missing"})), codes.NotFound},
		{"get bad name", second(s.GetABACPolicy(ctx, &pb.ABACPolicyRequest{Name: "a/b"})), codes.InvalidArgument},
		{"get cross tenant", second(s.GetABACPolicy(ctx, &pb.ABACPolicyRequest{TenantId: "globex", Name: "p"})), codes.PermissionDenied},
		{"list cross tenant", second(s.ListABACPolicies(ctx, &pb.ABACPolicyListRequest{TenantId: "globex"})), codes.PermissionDenied},
		{"update missing", second(s.UpdateABACPolicy(ctx, grpcTestPolicy("missing", nil))), codes.NotFound},
		{"update bad match", second(s.UpdateABACPolicy(ctx, badMatch)), codes.InvalidArgument},
		{"delete missing", second(s.DeleteABACPolicy(ctx, &pb.ABACPolicyRequest{Name: "missing"})), codes.NotFound},
	}
	for _, tc := range cases {
		if got := status.Code(tc.err); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, tc.err, tc.want)
		}
	}
}

// TestABACPolicyRPCsUnconfigured pins that a server built without a policy
// service answers every policy RPC Unimplemented.
func TestABACPolicyRPCsUnconfigured(t *testing.T) {
	s := &qrdbServiceServer{}
	ctx := context.Background()
	for name, err := range map[string]error{
		"create": second(s.CreateABACPolicy(ctx, &pb.ABACPolicy{})),
		"get":    second(s.GetABACPolicy(ctx, &pb.ABACPolicyRequest{})),
		"list":   second(s.ListABACPolicies(ctx, &pb.ABACPolicyListRequest{})),
		"update": second(s.UpdateABACPolicy(ctx, &pb.ABACPolicy{})),
		"delete": second(s.DeleteABACPolicy(ctx, &pb.ABACPolicyRequest{})),
	} {
		if status.Code(err) != codes.Unimplemented {
			t.Errorf("%s: %v, want Unimplemented", name, err)
		}
	}
}

// TestPolicyServiceOptionsAttach pins that both the server option and the
// register option attach the policy service.
func TestPolicyServiceOptionsAttach(t *testing.T) {
	svc, err := service.NewPolicyService(grpcPolicyStores{})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{}
	WithPolicyService(svc)(srv)
	if srv.policySvc != svc {
		t.Fatal("WithPolicyService did not attach the service")
	}
	reg := &qrdbServiceServer{}
	WithRegisterPolicyService(svc)(reg)
	if reg.policySvc != svc {
		t.Fatal("WithRegisterPolicyService did not attach the service")
	}
}

// unencodablePolicyService returns stored documents whose subjects.match
// cannot encode, standing in for a record no transport can render.
type unencodablePolicyService struct{ service.PolicyServicer }

func (unencodablePolicyService) unencodable() *abac.Policy {
	return &abac.Policy{Spec: abac.PolicySpec{Subjects: abac.SubjectSelector{Match: map[string]interface{}{"bad": make(chan int)}}}}
}

func (u unencodablePolicyService) GetPolicy(context.Context, string, string) (*abac.Policy, error) {
	return u.unencodable(), nil
}

func (u unencodablePolicyService) ListPolicies(context.Context, string) ([]*abac.Policy, error) {
	return []*abac.Policy{u.unencodable()}, nil
}

// TestABACPolicyRPCsRefuseUnencodableResponse pins that a stored document
// the wire form cannot carry fails the RPC rather than returning a partial
// message.
func TestABACPolicyRPCsRefuseUnencodableResponse(t *testing.T) {
	s := &qrdbServiceServer{policySvc: unencodablePolicyService{}}
	ctx := context.Background()
	if _, err := s.GetABACPolicy(ctx, &pb.ABACPolicyRequest{Name: "p"}); err == nil {
		t.Fatal("GetABACPolicy returned an unencodable document")
	}
	if _, err := s.ListABACPolicies(ctx, &pb.ABACPolicyListRequest{}); err == nil {
		t.Fatal("ListABACPolicies returned an unencodable document")
	}
}
