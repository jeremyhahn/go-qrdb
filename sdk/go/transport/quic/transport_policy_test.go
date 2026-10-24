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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/policywire"
)

// startPolicyServer answers the abacpolicy.* methods from a real
// PolicyService, converting with policywire exactly as
// pkg/api/quic/handlers_policy.go does.
func startPolicyServer(t *testing.T) *Transport {
	t.Helper()
	svc, err := service.NewPolicyService(dekStores{})
	if err != nil {
		t.Fatal(err)
	}
	srv, tr := startE2EServer(t)
	ctx := context.Background()
	reply := func(m proto.Message, err error) (uint16, []byte) {
		if err != nil {
			return wire.StatusFailedPrecondition, []byte(err.Error())
		}
		out, merr := proto.Marshal(m)
		if merr != nil {
			return wire.StatusError, []byte(merr.Error())
		}
		return wire.StatusOK, out
	}
	document := func(p *abac.Policy, err error) (uint16, []byte) {
		if err != nil {
			return reply(nil, err)
		}
		m, err := policywire.ToProto(p)
		return reply(m, err)
	}
	documentResponder := func(op func(*abac.Policy) (*abac.Policy, error)) wireResponder {
		return func(payload []byte) (uint16, []byte) {
			var in pb.ABACPolicy
			if err := proto.Unmarshal(payload, &in); err != nil {
				return wire.StatusInvalidArgument, []byte(err.Error())
			}
			p, err := policywire.FromProto(&in)
			if err != nil {
				return wire.StatusInvalidArgument, []byte(err.Error())
			}
			return document(op(p))
		}
	}
	srv.SetResponder("abacpolicy.Create", documentResponder(func(p *abac.Policy) (*abac.Policy, error) {
		return svc.CreatePolicy(ctx, p)
	}))
	srv.SetResponder("abacpolicy.Update", documentResponder(func(p *abac.Policy) (*abac.Policy, error) {
		return svc.UpdatePolicy(ctx, p)
	}))
	srv.SetResponder("abacpolicy.Get", func(payload []byte) (uint16, []byte) {
		var in pb.ABACPolicyRequest
		if err := proto.Unmarshal(payload, &in); err != nil {
			return wire.StatusInvalidArgument, []byte(err.Error())
		}
		return document(svc.GetPolicy(ctx, in.GetTenantId(), in.GetName()))
	})
	srv.SetResponder("abacpolicy.List", func(payload []byte) (uint16, []byte) {
		var in pb.ABACPolicyListRequest
		if err := proto.Unmarshal(payload, &in); err != nil {
			return wire.StatusInvalidArgument, []byte(err.Error())
		}
		policies, err := svc.ListPolicies(ctx, in.GetTenantId())
		if err != nil {
			return reply(nil, err)
		}
		return reply(policywire.ListToProto(policies))
	})
	srv.SetResponder("abacpolicy.Delete", func(payload []byte) (uint16, []byte) {
		var in pb.ABACPolicyRequest
		if err := proto.Unmarshal(payload, &in); err != nil {
			return wire.StatusInvalidArgument, []byte(err.Error())
		}
		if err := svc.DeletePolicy(ctx, in.GetTenantId(), in.GetName()); err != nil {
			return reply(nil, err)
		}
		return wire.StatusOK, []byte{}
	})
	return tr
}

func quicTestPolicy(name string, enabled *bool) *abac.Policy {
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
				When:      []abac.Condition{{Expression: `subject.department == "finance"`}},
			}},
		},
	}
}

func TestQUICPolicy_LifecycleOverTheWire(t *testing.T) {
	tr := startPolicyServer(t)
	ctx := context.Background()

	if p, err := tr.CreateABACPolicy(ctx, quicTestPolicy("p", nil)); err != nil || p.Metadata.Name != "p" || p.Metadata.Created.IsZero() {
		t.Fatalf("create = %+v, %v", p, err)
	}
	if p, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil || len(p.Spec.Rules[0].When) != 1 {
		t.Fatalf("get = %+v, %v", p, err)
	}
	off := false
	if p, err := tr.UpdateABACPolicy(ctx, quicTestPolicy("p", &off)); err != nil || p.Spec.Enabled == nil || *p.Spec.Enabled {
		t.Fatalf("update = %+v, %v", p, err)
	}
	if list, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{}); err != nil || len(list.Policies) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	if err != nil || list.Policies == nil || len(list.Policies) != 0 {
		t.Fatalf("list after delete = %#v, %v, want an empty array", list, err)
	}
	if _, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err == nil {
		t.Fatal("get after delete must fail")
	}
}

func TestQUICPolicy_NilRequestsAndClosed(t *testing.T) {
	tr := startPolicyServer(t)
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

	unencodable := quicTestPolicy("p", nil)
	unencodable.Spec.Subjects.Match = map[string]interface{}{"f": func() {}}
	var me *policywire.MatchEncodingError
	if _, err := tr.CreateABACPolicy(ctx, unencodable); !errors.As(err, &me) {
		t.Errorf("create of an unencodable match: %v", err)
	}

	_ = tr.Close()
	closed := map[string]error{}
	_, closed["create"] = tr.CreateABACPolicy(ctx, quicTestPolicy("p", nil))
	_, closed["get"] = tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	_, closed["list"] = tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	_, closed["update"] = tr.UpdateABACPolicy(ctx, quicTestPolicy("p", nil))
	closed["delete"] = tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	for op, err := range closed {
		var ce *transport.ClosedError
		if !errors.As(err, &ce) {
			t.Errorf("%s after close: %v", op, err)
		}
	}
}
