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
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeCompactionAdmin is an admin double that holds the node-keyspace policy
// the way the service does: validated on set, off stored as none.
type nodeCompactionAdmin struct {
	mockAdminService
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

func decodeNodeCompaction(t *testing.T, b []byte) *pb.DatabaseCompactionConfig {
	t.Helper()
	var resp pb.NodeCompactionResponse
	if err := proto.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.GetCompaction()
}

func TestHandleNodeCompaction_SetGetAndClear(t *testing.T) {
	admin := &nodeCompactionAdmin{}
	srv := newTestServer(&mockKVService{}, admin)
	ctx := context.Background()

	set, _ := proto.Marshal(&pb.SetNodeCompactionRequest{Compaction: &pb.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 5}})
	out, err := srv.handleSetNodeCompaction(ctx, set)
	if err != nil {
		t.Fatalf("handleSetNodeCompaction: %v", err)
	}
	if c := decodeNodeCompaction(t, out); c.GetMode() != "revision" || c.GetRetainRevisions() != 5 {
		t.Fatalf("set response = %v; want the revision policy", c)
	}
	out, err = srv.handleGetNodeCompaction(ctx, nil)
	if err != nil || decodeNodeCompaction(t, out).GetRetainRevisions() != 5 {
		t.Fatalf("handleGetNodeCompaction = %v; want the policy set", err)
	}
	clear, _ := proto.Marshal(&pb.SetNodeCompactionRequest{})
	out, err = srv.handleSetNodeCompaction(ctx, clear)
	if err != nil || decodeNodeCompaction(t, out) != nil || admin.policy != nil {
		t.Fatalf("clear = %v (held %+v); want no policy", err, admin.policy)
	}
}

func TestHandleNodeCompaction_Refusals(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(&mockKVService{}, &nodeCompactionAdmin{})

	var unmarshal *UnmarshalError
	if _, err := srv.handleSetNodeCompaction(ctx, []byte{0xff, 0xff}); !errors.As(err, &unmarshal) {
		t.Fatalf("malformed payload = %v; want *UnmarshalError", err)
	}
	invalid, _ := proto.Marshal(&pb.SetNodeCompactionRequest{Compaction: &pb.DatabaseCompactionConfig{Mode: "revision"}})
	var bad *compaction.InvalidPolicyError
	if _, err := srv.handleSetNodeCompaction(ctx, invalid); !errors.As(err, &bad) {
		t.Fatalf("revision with no retention = %v; want *compaction.InvalidPolicyError", err)
	}

	down := newTestServer(&mockKVService{}, &nodeCompactionAdmin{err: &service.NodeCompactionUnavailableError{}})
	var unavailable *service.NodeCompactionUnavailableError
	if _, err := down.handleGetNodeCompaction(ctx, nil); !errors.As(err, &unavailable) {
		t.Fatalf("get (unavailable) = %v", err)
	}
	if _, err := down.handleSetNodeCompaction(ctx, clearPayload(t)); !errors.As(err, &unavailable) {
		t.Fatalf("set (unavailable) = %v", err)
	}
}

func clearPayload(t *testing.T) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.SetNodeCompactionRequest{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
