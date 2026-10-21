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

package unix

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// connectPolicyTransport serves the policy RPCs of a real PolicyService on
// a Unix socket and returns a connected client.
func connectPolicyTransport(t *testing.T) *Transport {
	t.Helper()
	svc, err := service.NewPolicyService(dekStores{})
	if err != nil {
		t.Fatal(err)
	}
	socketPath := tempSocketPath(t)
	srv := unixserver.NewServer(newMockKVService(), newMockAdminService(), socketPath,
		unixserver.WithPolicyService(svc),
		unixserver.WithTLS(testUnixServerTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("start Unix server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	tr, err := New(transport.WithAddress(socketPath), testClientTLSOption())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func unixTestPolicy(name string, enabled *bool) *abac.Policy {
	return &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: name},
		Spec: abac.PolicySpec{
			Enabled:  enabled,
			Subjects: abac.SubjectSelector{Roles: []string{"auditor"}},
			Rules: []abac.Rule{{
				Name:      "deny-secrets",
				Effect:    abac.EffectDeny,
				Actions:   []string{"read"},
				Resources: []string{"/secrets/**"},
			}},
		},
	}
}

func TestTransportPolicy_LifecycleOverTheSocket(t *testing.T) {
	tr := connectPolicyTransport(t)
	ctx := context.Background()

	if p, err := tr.CreateABACPolicy(ctx, unixTestPolicy("p", nil)); err != nil || p.Metadata.Name != "p" {
		t.Fatalf("create = %+v, %v", p, err)
	}
	if p, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil || p.Spec.Enabled != nil {
		t.Fatalf("get = %+v, %v", p, err)
	}
	off := false
	if p, err := tr.UpdateABACPolicy(ctx, unixTestPolicy("p", &off)); err != nil || p.Spec.Enabled == nil || *p.Spec.Enabled {
		t.Fatalf("update = %+v, %v", p, err)
	}
	if list, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{}); err != nil || len(list.Policies) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var nf *transport.KeyNotFoundError
	if _, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); !errors.As(err, &nf) {
		t.Fatalf("get after delete = %v, want *transport.KeyNotFoundError", err)
	}
}

func TestTransportPolicy_CrossTenantNilAndClosed(t *testing.T) {
	tr := connectPolicyTransport(t)
	ctx := context.Background()
	_, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{TenantID: "globex"})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != int(codes.PermissionDenied) {
		t.Fatalf("cross tenant: err = %v, want PermissionDenied", err)
	}

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
	_, closed["create"] = tr.CreateABACPolicy(ctx, unixTestPolicy("p", nil))
	_, closed["get"] = tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	_, closed["list"] = tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	_, closed["update"] = tr.UpdateABACPolicy(ctx, unixTestPolicy("p", nil))
	closed["delete"] = tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	for op, err := range closed {
		var ce *transport.ClosedError
		if !errors.As(err, &ce) {
			t.Errorf("%s after close: %v", op, err)
		}
	}
}
