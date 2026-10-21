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

package policywire

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func testPolicy(name string) *abac.Policy {
	return &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: name},
		Spec: abac.PolicySpec{
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

// fakeClient serves the five policy RPCs from a map and fails every call
// with err when it is set. The embedded interface leaves every other RPC
// nil: the Caller must never reach one.
type fakeClient struct {
	pb.QRDBServiceClient
	stored map[string]*pb.ABACPolicy
	reply  *pb.ABACPolicy
	err    error
}

func (f *fakeClient) CreateABACPolicy(_ context.Context, in *pb.ABACPolicy, _ ...grpc.CallOption) (*pb.ABACPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.stored[in.GetMetadata().GetName()] = in
	if f.reply != nil {
		return f.reply, nil
	}
	return in, nil
}

func (f *fakeClient) UpdateABACPolicy(ctx context.Context, in *pb.ABACPolicy, opts ...grpc.CallOption) (*pb.ABACPolicy, error) {
	return f.CreateABACPolicy(ctx, in, opts...)
}

func (f *fakeClient) GetABACPolicy(_ context.Context, in *pb.ABACPolicyRequest, _ ...grpc.CallOption) (*pb.ABACPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.stored[in.GetName()], nil
}

func (f *fakeClient) ListABACPolicies(_ context.Context, _ *pb.ABACPolicyListRequest, _ ...grpc.CallOption) (*pb.ABACPolicyListResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	resp := &pb.ABACPolicyListResponse{}
	for _, m := range f.stored {
		resp.Policies = append(resp.Policies, m)
	}
	return resp, nil
}

func (f *fakeClient) DeleteABACPolicy(_ context.Context, in *pb.ABACPolicyRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if f.err != nil {
		return nil, f.err
	}
	delete(f.stored, in.GetName())
	return &emptypb.Empty{}, nil
}

// mappedError marks an error that went through the Caller's MapError.
type mappedError struct {
	method string
	err    error
}

func (e *mappedError) Error() string { return e.method + ": " + e.err.Error() }

func newCaller(f *fakeClient) Caller {
	return Caller{Client: f, MapError: func(method string, err error) error { return &mappedError{method: method, err: err} }}
}

func TestCallerRoundTripsEveryOperation(t *testing.T) {
	c := newCaller(&fakeClient{stored: map[string]*pb.ABACPolicy{}})
	ctx := context.Background()
	if p, err := c.Create(ctx, testPolicy("p")); err != nil || p.Metadata.Name != "p" {
		t.Fatalf("create = %+v, %v", p, err)
	}
	if p, err := c.Update(ctx, testPolicy("p")); err != nil || p.Metadata.Name != "p" {
		t.Fatalf("update = %+v, %v", p, err)
	}
	if p, err := c.Get(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil || len(p.Spec.Rules) != 1 {
		t.Fatalf("get = %+v, %v", p, err)
	}
	if list, err := c.List(ctx, &transport.ABACPolicyListRequest{}); err != nil || len(list.Policies) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := c.Delete(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := c.List(ctx, &transport.ABACPolicyListRequest{})
	if err != nil || list.Policies == nil || len(list.Policies) != 0 {
		t.Fatalf("list after delete = %#v, %v, want an empty slice", list, err)
	}
}

func TestCallerMapsEveryRPCError(t *testing.T) {
	c := newCaller(&fakeClient{err: errors.New("unavailable")})
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["CreateABACPolicy"] = c.Create(ctx, testPolicy("p"))
	_, checks["GetABACPolicy"] = c.Get(ctx, &transport.ABACPolicyRequest{Name: "p"})
	_, checks["ListABACPolicies"] = c.List(ctx, &transport.ABACPolicyListRequest{})
	_, checks["UpdateABACPolicy"] = c.Update(ctx, testPolicy("p"))
	checks["DeleteABACPolicy"] = c.Delete(ctx, &transport.ABACPolicyRequest{Name: "p"})
	for method, err := range checks {
		var me *mappedError
		if !errors.As(err, &me) || me.method != method {
			t.Errorf("%s: err = %v, want it mapped under its own method", method, err)
		}
	}
}

func TestCallerRefusesNilRequests(t *testing.T) {
	c := newCaller(&fakeClient{stored: map[string]*pb.ABACPolicy{}})
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["create"] = c.Create(ctx, nil)
	_, checks["get"] = c.Get(ctx, nil)
	_, checks["list"] = c.List(ctx, nil)
	_, checks["update"] = c.Update(ctx, nil)
	checks["delete"] = c.Delete(ctx, nil)
	for op, err := range checks {
		var nre *transport.NilRequestError
		if !errors.As(err, &nre) {
			t.Errorf("%s(nil): %v", op, err)
		}
	}
}

// TestConversionFailuresAreRequestErrors pins that a document the wire
// cannot carry, and a reply that does not decode, surface as the
// RequestError of the method, wrapping policywire's typed error.
func TestConversionFailuresAreRequestErrors(t *testing.T) {
	unencodable := testPolicy("p")
	unencodable.Spec.Subjects.Match = map[string]interface{}{"f": func() {}}
	var me *MatchEncodingError
	if _, err := newCaller(&fakeClient{stored: map[string]*pb.ABACPolicy{}}).Create(context.Background(), unencodable); !errors.As(err, &me) {
		t.Errorf("create of an unencodable match: %v", err)
	}

	bad, err := Encode("x", testPolicy("p"))
	if err != nil {
		t.Fatal(err)
	}
	bad.Metadata.Created = "not a time"
	var te *TimestampError
	var re *transport.RequestError
	if _, err := Decode("GetABACPolicy", bad); !errors.As(err, &te) || !errors.As(err, &re) || re.Method != "GetABACPolicy" {
		t.Errorf("decode of a bad timestamp: %v", err)
	}
	if _, err := DecodeList("ListABACPolicies", &pb.ABACPolicyListResponse{Policies: []*pb.ABACPolicy{bad}}); !errors.As(err, &te) {
		t.Errorf("list decode of a bad timestamp: %v", err)
	}
	c := newCaller(&fakeClient{stored: map[string]*pb.ABACPolicy{}, reply: bad})
	if _, err := c.Update(context.Background(), testPolicy("p")); !errors.As(err, &te) {
		t.Errorf("update with an undecodable reply: %v", err)
	}
}
