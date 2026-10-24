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
	"errors"
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// connectPolicyTransport serves the policy RPCs of a real PolicyService
// over loopback gRPC and returns a connected client.
func connectPolicyTransport(t *testing.T) *Transport {
	t.Helper()
	svc, err := service.NewPolicyService(dekStores{})
	if err != nil {
		t.Fatal(err)
	}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterPolicyService(svc))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	tr, cleanup := connectCredentialTransport(t, lis.Addr().String())
	t.Cleanup(cleanup)
	return tr
}

func grpcTestPolicy(name string, enabled *bool) *abac.Policy {
	return &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: name},
		Spec: abac.PolicySpec{
			Enabled: enabled,
			Subjects: abac.SubjectSelector{
				Roles: []string{"auditor"},
				Match: map[string]interface{}{"department": "finance"},
			},
			Rules: []abac.Rule{{
				Name:      "deny-secrets",
				Effect:    abac.EffectDeny,
				Actions:   []string{"read"},
				Resources: []string{"/secrets/**"},
				When:      []abac.Condition{{Expression: `subject.department == "finance"`}},
			}},
		},
	}
}

func TestTransportPolicy_LifecycleOverTheWire(t *testing.T) {
	tr := connectPolicyTransport(t)
	ctx := context.Background()

	created, err := tr.CreateABACPolicy(ctx, grpcTestPolicy("p", nil))
	if err != nil || created.Metadata.Name != "p" || created.Spec.Enabled != nil || created.Metadata.Created.IsZero() {
		t.Fatalf("create = %+v, %v", created, err)
	}
	got, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	if err != nil || len(got.Spec.Rules) != 1 || len(got.Spec.Rules[0].When) != 1 || got.Spec.Subjects.Match["department"] != "finance" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	off := false
	updated, err := tr.UpdateABACPolicy(ctx, grpcTestPolicy("p", &off))
	if err != nil || updated.Spec.Enabled == nil || *updated.Spec.Enabled || !updated.Metadata.Created.Equal(created.Metadata.Created) {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	list, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	if err != nil || len(list.Policies) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err = tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	if err != nil || list.Policies == nil || len(list.Policies) != 0 {
		t.Fatalf("list after delete = %#v, %v, want an empty array", list, err)
	}
}

// TestTransportPolicy_RefusalsCarryTheirCodes pins each service refusal to
// its gRPC code on the client's RequestError.
func TestTransportPolicy_RefusalsCarryTheirCodes(t *testing.T) {
	tr := connectPolicyTransport(t)
	ctx := context.Background()
	if _, err := tr.CreateABACPolicy(ctx, grpcTestPolicy("p", nil)); err != nil {
		t.Fatal(err)
	}
	invalid := grpcTestPolicy("q", nil)
	invalid.Spec.Rules = nil
	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"duplicate":    {second(tr.CreateABACPolicy(ctx, grpcTestPolicy("p", nil))), codes.AlreadyExists},
		"invalid":      {second(tr.CreateABACPolicy(ctx, invalid)), codes.InvalidArgument},
		"cross tenant": {tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{TenantID: "globex", Name: "p"}), codes.PermissionDenied},
	}
	for name, c := range cases {
		var re *transport.RequestError
		if !errors.As(c.err, &re) || re.StatusCode != int(c.want) {
			t.Errorf("%s: err = %v, want a RequestError with code %v", name, c.err, c.want)
		}
	}
	var nf *transport.KeyNotFoundError
	if _, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "absent"}); !errors.As(err, &nf) {
		t.Errorf("missing: err = %v, want *transport.KeyNotFoundError", err)
	}
}

func TestTransportPolicy_NilRequestsAndClosed(t *testing.T) {
	tr := connectPolicyTransport(t)
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["create"] = tr.CreateABACPolicy(ctx, nil)
	_, checks["get"] = tr.GetABACPolicy(ctx, nil)
	_, checks["list"] = tr.ListABACPolicies(ctx, nil)
	_, checks["update"] = tr.UpdateABACPolicy(ctx, nil)
	checks["delete"] = tr.DeleteABACPolicy(ctx, nil)
	for op, err := range checks {
		var nre *transport.NilRequestError
		if !errors.As(err, &nre) {
			t.Errorf("%s(nil): %v", op, err)
		}
	}

	_ = tr.Close()
	closed := map[string]error{}
	_, closed["create"] = tr.CreateABACPolicy(ctx, grpcTestPolicy("p", nil))
	_, closed["get"] = tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	_, closed["list"] = tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	_, closed["update"] = tr.UpdateABACPolicy(ctx, grpcTestPolicy("p", nil))
	closed["delete"] = tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	for op, err := range closed {
		var ce *transport.ClosedError
		if !errors.As(err, &ce) {
			t.Errorf("%s after close: %v", op, err)
		}
	}
}
