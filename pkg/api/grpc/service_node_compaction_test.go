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
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeCompactionAdmin is an admin double that holds the node-keyspace policy
// the way the service does: validated on set, off stored as none.
type nodeCompactionAdmin struct {
	*mockAdminService
	policy *compaction.Policy
	err    error
}

func (a *nodeCompactionAdmin) NodeCompaction(context.Context) (*compaction.Policy, error) {
	return a.policy, a.err
}

func (a *nodeCompactionAdmin) SetNodeCompaction(_ context.Context, p *compaction.Policy) error {
	if a.err != nil {
		return a.err
	}
	if p != nil {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	a.policy = p.Normalized()
	return nil
}

func TestGRPC_NodeCompaction_SetGetAndClear(t *testing.T) {
	admin := &nodeCompactionAdmin{mockAdminService: newMockAdminService()}
	s := &qrdbServiceServer{adminSvc: admin}
	ctx := context.Background()

	set, err := s.SetNodeCompaction(ctx, &pb.SetNodeCompactionRequest{
		Compaction: &pb.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 7},
	})
	if err != nil || set.GetCompaction().GetMode() != "revision" || set.GetCompaction().GetRetainRevisions() != 7 {
		t.Fatalf("SetNodeCompaction = %v, %v; want the revision policy", set.GetCompaction(), err)
	}
	got, err := s.GetNodeCompaction(ctx, &emptypb.Empty{})
	if err != nil || got.GetCompaction().GetRetainRevisions() != 7 {
		t.Fatalf("GetNodeCompaction = %v, %v; want the policy set", got.GetCompaction(), err)
	}
	cleared, err := s.SetNodeCompaction(ctx, &pb.SetNodeCompactionRequest{})
	if err != nil || cleared.GetCompaction() != nil || admin.policy != nil {
		t.Fatalf("SetNodeCompaction(unset) = %v, %v (held %v); want none", cleared.GetCompaction(), err, admin.policy)
	}
}

func TestGRPC_NodeCompaction_Refusals(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func(s *qrdbServiceServer) error
		want codes.Code
	}{
		{"invalid policy", func(s *qrdbServiceServer) error {
			_, err := s.SetNodeCompaction(ctx, &pb.SetNodeCompactionRequest{Compaction: &pb.DatabaseCompactionConfig{Mode: "weekly"}})
			return err
		}, codes.InvalidArgument},
		{"get without an admin service", func(*qrdbServiceServer) error {
			_, err := (&qrdbServiceServer{}).GetNodeCompaction(ctx, &emptypb.Empty{})
			return err
		}, codes.Unimplemented},
		{"set without an admin service", func(*qrdbServiceServer) error {
			_, err := (&qrdbServiceServer{}).SetNodeCompaction(ctx, &pb.SetNodeCompactionRequest{})
			return err
		}, codes.Unimplemented},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &qrdbServiceServer{adminSvc: &nodeCompactionAdmin{mockAdminService: newMockAdminService()}}
			if st, _ := status.FromError(tc.call(s)); st.Code() != tc.want {
				t.Fatalf("code = %s, want %s", st.Code(), tc.want)
			}
		})
	}
	unavailable := &nodeCompactionAdmin{mockAdminService: newMockAdminService(), err: &service.NodeCompactionUnavailableError{}}
	s := &qrdbServiceServer{adminSvc: unavailable}
	if _, err := s.GetNodeCompaction(ctx, &emptypb.Empty{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("GetNodeCompaction (unavailable) code = %s, want %s", status.Code(err), codes.FailedPrecondition)
	}
	if _, err := s.SetNodeCompaction(ctx, &pb.SetNodeCompactionRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("SetNodeCompaction (unavailable) code = %s, want %s", status.Code(err), codes.FailedPrecondition)
	}
}
