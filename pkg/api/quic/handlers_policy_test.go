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
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// quicPolicyStores resolves each tenant to its own memory store; an unknown
// tenant is sealed.
type quicPolicyStores map[string]kvstore.KVStore

func (p quicPolicyStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if s, ok := p[tenantID]; ok {
		return s, nil
	}
	return nil, tenant.ErrTenantSealed
}

func newPolicyQUICServer(t *testing.T) *Server {
	t.Helper()
	svc, err := service.NewPolicyService(quicPolicyStores{"acme": kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithPolicyService(svc))
}

func quicTestPolicy(name string, enabled *bool) *pb.ABACPolicy {
	return &pb.ABACPolicy{
		ApiVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   &pb.ABACPolicyMetadata{Name: name},
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

// TestHandlePolicy_FramesRoundTrip drives every abacpolicy.* frame and pins
// that spec.enabled keeps its presence: unset stays unset, false stays false.
func TestHandlePolicy_FramesRoundTrip(t *testing.T) {
	s := newPolicyQUICServer(t)

	var created pb.ABACPolicy
	if err := dekFrame(t, s, "abacpolicy.Create", quicTestPolicy("unset", nil), &created); err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.GetSpec().Enabled != nil || created.GetMetadata().GetTenant() != "acme" {
		t.Fatalf("created = %v, want enabled unset and tenant acme", &created)
	}
	if err := dekFrame(t, s, "abacpolicy.Create", quicTestPolicy("off", proto.Bool(false)), &created); err != nil {
		t.Fatalf("create off: %v", err)
	}

	var got pb.ABACPolicy
	if err := dekFrame(t, s, "abacpolicy.Get", &pb.ABACPolicyRequest{Name: "off"}, &got); err != nil || got.GetSpec().Enabled == nil || got.GetSpec().GetEnabled() {
		t.Fatalf("get off = %v, %v; want enabled=false present", &got, err)
	}

	var list pb.ABACPolicyListResponse
	if err := dekFrame(t, s, "abacpolicy.List", &pb.ABACPolicyListRequest{}, &list); err != nil || len(list.GetPolicies()) != 2 {
		t.Fatalf("list = %v, %v; want two policies", &list, err)
	}
	if list.GetPolicies()[1].GetSpec().Enabled != nil {
		t.Fatal("list returned enabled for a policy written without it")
	}

	var updated pb.ABACPolicy
	if err := dekFrame(t, s, "abacpolicy.Update", quicTestPolicy("unset", proto.Bool(true)), &updated); err != nil || !updated.GetSpec().GetEnabled() {
		t.Fatalf("update = %v, %v; want enabled=true", &updated, err)
	}

	var empty pb.ABACPolicy
	if err := dekFrame(t, s, "abacpolicy.Delete", &pb.ABACPolicyRequest{Name: "unset"}, &empty); err != nil {
		t.Fatalf("delete: %v", err)
	}
	err := dekFrame(t, s, "abacpolicy.Get", &pb.ABACPolicyRequest{Name: "unset"}, &got)
	if code, _ := mapServiceError(err); code != wire.StatusNotFound {
		t.Fatalf("get after delete: status %d, want NotFound", code)
	}
}

// TestHandlePolicy_ErrorsMapToTheSharedStatuses pins the wire status of
// every refusal the handlers return.
func TestHandlePolicy_ErrorsMapToTheSharedStatuses(t *testing.T) {
	s := newPolicyQUICServer(t)
	var out pb.ABACPolicy
	if err := dekFrame(t, s, "abacpolicy.Create", quicTestPolicy("p", nil), &out); err != nil {
		t.Fatal(err)
	}
	badMatch := quicTestPolicy("q", nil)
	badMatch.Spec.Subjects.MatchJson = "not json"
	badEffect := quicTestPolicy("q", nil)
	badEffect.Spec.Rules[0].Effect = "maybe"
	var list pb.ABACPolicyListResponse

	cases := []struct {
		name   string
		method string
		in     proto.Message
		out    proto.Message
		want   uint16
	}{
		{"create exists", "abacpolicy.Create", quicTestPolicy("p", nil), &out, wire.StatusAlreadyExists},
		{"create bad match", "abacpolicy.Create", badMatch, &out, wire.StatusInvalidArgument},
		{"create uncompilable", "abacpolicy.Create", badEffect, &out, wire.StatusInvalidArgument},
		{"get missing", "abacpolicy.Get", &pb.ABACPolicyRequest{Name: "missing"}, &out, wire.StatusNotFound},
		{"get cross tenant", "abacpolicy.Get", &pb.ABACPolicyRequest{TenantId: "globex", Name: "p"}, &out, wire.StatusPermissionDenied},
		{"list cross tenant", "abacpolicy.List", &pb.ABACPolicyListRequest{TenantId: "globex"}, &list, wire.StatusPermissionDenied},
		{"update missing", "abacpolicy.Update", quicTestPolicy("missing", nil), &out, wire.StatusNotFound},
		{"delete missing", "abacpolicy.Delete", &pb.ABACPolicyRequest{Name: "missing"}, &out, wire.StatusNotFound},
	}
	for _, tc := range cases {
		err := dekFrame(t, s, tc.method, tc.in, tc.out)
		if code, _ := mapServiceError(err); code != tc.want {
			t.Errorf("%s: status %d (%v), want %d", tc.name, code, err, tc.want)
		}
	}
}

// TestHandlePolicy_UndecodableFramesAreInvalidArgument pins that a frame
// that is not the method's message is refused before the service is called.
func TestHandlePolicy_UndecodableFramesAreInvalidArgument(t *testing.T) {
	s := newPolicyQUICServer(t)
	for _, method := range []string{"abacpolicy.Create", "abacpolicy.Get", "abacpolicy.List", "abacpolicy.Update", "abacpolicy.Delete"} {
		_, err := s.handlers[method](context.Background(), []byte{0xff, 0xff, 0xff})
		if code, _ := mapServiceError(err); code != wire.StatusInvalidArgument {
			t.Errorf("%s: status %d, want InvalidArgument", method, code)
		}
	}
}

// TestHandlePolicy_AbsentWithoutTheService pins that a server built without
// a policy service registers no abacpolicy.* handler.
func TestHandlePolicy_AbsentWithoutTheService(t *testing.T) {
	s := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	for _, method := range []string{"abacpolicy.Create", "abacpolicy.Get", "abacpolicy.List", "abacpolicy.Update", "abacpolicy.Delete"} {
		if _, ok := s.handlers[method]; ok {
			t.Errorf("%s registered without a policy service", method)
		}
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

// TestHandlePolicy_RefusesUnencodableResponse pins that a stored document
// the wire form cannot carry fails the frame rather than returning a
// partial message.
func TestHandlePolicy_RefusesUnencodableResponse(t *testing.T) {
	s := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithPolicyService(unencodablePolicyService{}))
	var out pb.ABACPolicy
	if err := dekFrame(t, s, "abacpolicy.Get", &pb.ABACPolicyRequest{Name: "p"}, &out); err == nil {
		t.Fatal("abacpolicy.Get returned an unencodable document")
	}
	var list pb.ABACPolicyListResponse
	if err := dekFrame(t, s, "abacpolicy.List", &pb.ABACPolicyListRequest{}, &list); err == nil {
		t.Fatal("abacpolicy.List returned an unencodable document")
	}
}
