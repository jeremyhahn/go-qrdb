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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/policywire"
)

// ABAC policy handlers. Frames carry the gRPC protobuf messages, converted
// by policywire so spec.enabled keeps its presence; the tenant scoping is
// service.PolicyServicer's.

// handlePolicyCreate serves "abacpolicy.Create".
func (s *Server) handlePolicyCreate(ctx context.Context, payload []byte) ([]byte, error) {
	p, err := unmarshalPolicy(payload)
	if err != nil {
		return nil, err
	}
	return marshalPolicy(s.policySvc.CreatePolicy(ctx, p))
}

// handlePolicyGet serves "abacpolicy.Get".
func (s *Server) handlePolicyGet(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.ABACPolicyRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	return marshalPolicy(s.policySvc.GetPolicy(ctx, req.GetTenantId(), req.GetName()))
}

// handlePolicyList serves "abacpolicy.List".
func (s *Server) handlePolicyList(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.ABACPolicyListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	policies, err := s.policySvc.ListPolicies(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	resp, err := policywire.ListToProto(policies)
	if err != nil {
		return nil, err
	}
	return marshalReply(resp)
}

// handlePolicyUpdate serves "abacpolicy.Update".
func (s *Server) handlePolicyUpdate(ctx context.Context, payload []byte) ([]byte, error) {
	p, err := unmarshalPolicy(payload)
	if err != nil {
		return nil, err
	}
	return marshalPolicy(s.policySvc.UpdatePolicy(ctx, p))
}

// handlePolicyDelete serves "abacpolicy.Delete".
func (s *Server) handlePolicyDelete(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.ABACPolicyRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.policySvc.DeletePolicy(ctx, req.GetTenantId(), req.GetName()); err != nil {
		return nil, err
	}
	return []byte{}, nil
}

// unmarshalPolicy decodes a policy frame, refusing one whose wire form does
// not convert as an invalid policy.
func unmarshalPolicy(payload []byte) (*abac.Policy, error) {
	var m pb.ABACPolicy
	if err := proto.Unmarshal(payload, &m); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	p, err := policywire.FromProto(&m)
	if err != nil {
		return nil, &service.PolicyInvalidError{Name: m.GetMetadata().GetName(), Reason: "decode", Err: err}
	}
	return p, nil
}

// marshalPolicy encodes a stored policy as a response frame.
func marshalPolicy(p *abac.Policy, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	m, err := policywire.ToProto(p)
	if err != nil {
		return nil, err
	}
	return marshalReply(m)
}
