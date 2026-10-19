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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// joinReviewAdmin is a mockAdminService that also implements the optional
// join-review, observer-admission and identity-retirement interfaces, and
// records what each was called with.
type joinReviewAdmin struct {
	mockAdminService
	pending []*service.PendingJoinRequest
	err     error

	gotShard, gotReplica  uint64
	gotAddr, gotIdentity  string
	gotID, gotNode, gotRe string
}

var (
	_ service.JoinRequestReviewer = (*joinReviewAdmin)(nil)
	_ service.ObserverMemberAdder = (*joinReviewAdmin)(nil)
	_ service.JoinIdentityRetirer = (*joinReviewAdmin)(nil)
)

func (a *joinReviewAdmin) ListPendingJoinRequests(context.Context) ([]*service.PendingJoinRequest, error) {
	return a.pending, a.err
}

func (a *joinReviewAdmin) ApproveJoinRequest(_ context.Context, id, nodeID string) error {
	a.gotID, a.gotNode = id, nodeID
	return a.err
}

func (a *joinReviewAdmin) RejectJoinRequest(_ context.Context, id, reason string) error {
	a.gotID, a.gotRe = id, reason
	return a.err
}

func (a *joinReviewAdmin) AddObserverMember(_ context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	a.gotShard, a.gotReplica, a.gotAddr, a.gotIdentity = shardID, replicaID, raftAddr, identity
	return a.err
}

func (a *joinReviewAdmin) RetireJoinIdentity(_ context.Context, nodeID, reason string) error {
	a.gotNode, a.gotRe = nodeID, reason
	return a.err
}

var errJoinReview = errors.New("join review backend failed")

// assertToolErr fails unless err is a *ToolError naming tool.
func assertToolErr(t *testing.T, err error, tool string) {
	t.Helper()
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != tool {
		t.Fatalf("err = %v, want *ToolError for %s", err, tool)
	}
}

func TestJoinReviewTools_UnsupportedAdminRefused(t *testing.T) {
	admin := &mockAdminService{}
	ctx := context.Background()
	cases := []struct {
		tool string
		call func() error
	}{
		{"qrdb_add_observer_member", func() error {
			_, err := handleAddObserverMember(admin)(ctx, buildCallToolRequest(t, "qrdb_add_observer_member", nil))
			return err
		}},
		{"qrdb_list_pending_join_requests", func() error {
			_, err := handleListPendingJoinRequests(admin)(ctx, buildCallToolRequest(t, "qrdb_list_pending_join_requests", nil))
			return err
		}},
		{"qrdb_approve_join_request", func() error {
			_, err := handleApproveJoinRequest(admin)(ctx, buildCallToolRequest(t, "qrdb_approve_join_request", nil))
			return err
		}},
		{"qrdb_reject_join_request", func() error {
			_, err := handleRejectJoinRequest(admin)(ctx, buildCallToolRequest(t, "qrdb_reject_join_request", nil))
			return err
		}},
		{"qrdb_retire_join_identity", func() error {
			_, err := handleRetireJoinIdentity(admin)(ctx, buildCallToolRequest(t, "qrdb_retire_join_identity", nil))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			assertToolErr(t, tc.call(), tc.tool)
		})
	}
}

func TestAddObserverMemberTool_MissingArgumentsRefused(t *testing.T) {
	cases := map[string]map[string]any{
		"shard_id":   {"replica_id": 2, "raft_addr": "10.0.0.2:9100"},
		"replica_id": {"shard_id": 1, "raft_addr": "10.0.0.2:9100"},
		"raft_addr":  {"shard_id": 1, "replica_id": 2},
	}
	for missing, args := range cases {
		t.Run(missing, func(t *testing.T) {
			admin := &joinReviewAdmin{}
			res, err := handleAddObserverMember(admin)(context.Background(), buildCallToolRequest(t, "qrdb_add_observer_member", args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.IsError || resultText(t, res) != missing+" is required" {
				t.Fatalf("result = %+v, want %q", res, missing+" is required")
			}
			if admin.gotShard != 0 {
				t.Fatal("adder called despite a missing argument")
			}
		})
	}
}

func TestAddObserverMemberTool_DelegatesAndReportsErrors(t *testing.T) {
	args := map[string]any{"shard_id": 1, "replica_id": 2, "raft_addr": "10.0.0.2:9100", "identity": "spiffe://db.example.com/node/n2"}
	admin := &joinReviewAdmin{}
	res, err := handleAddObserverMember(admin)(context.Background(), buildCallToolRequest(t, "qrdb_add_observer_member", args))
	if err != nil {
		t.Fatalf("qrdb_add_observer_member: %v", err)
	}
	if got := resultText(t, res); got != "ok" {
		t.Fatalf("result = %q, want ok", got)
	}
	if admin.gotShard != 1 || admin.gotReplica != 2 || admin.gotAddr != "10.0.0.2:9100" || admin.gotIdentity != "spiffe://db.example.com/node/n2" {
		t.Fatalf("adder got (%d, %d, %q, %q)", admin.gotShard, admin.gotReplica, admin.gotAddr, admin.gotIdentity)
	}

	admin = &joinReviewAdmin{err: errJoinReview}
	_, err = handleAddObserverMember(admin)(context.Background(), buildCallToolRequest(t, "qrdb_add_observer_member", args))
	assertToolErr(t, err, "qrdb_add_observer_member")
	if !errors.Is(err, errJoinReview) {
		t.Fatalf("err = %v, want it to wrap the backend error", err)
	}
}

func TestListPendingJoinRequestsTool_ReturnsJSON(t *testing.T) {
	submitted := time.Unix(1700000000, 0).UTC()
	admin := &joinReviewAdmin{pending: []*service.PendingJoinRequest{{
		ID:          "req-1",
		NodeURI:     "spiffe://db.example.com/node/n2",
		ReplicaID:   2,
		Features:    []string{"f1"},
		SubmittedAt: submitted,
		ExpiresAt:   submitted.Add(time.Hour),
	}}}
	res, err := handleListPendingJoinRequests(admin)(context.Background(), buildCallToolRequest(t, "qrdb_list_pending_join_requests", nil))
	if err != nil {
		t.Fatalf("qrdb_list_pending_join_requests: %v", err)
	}
	var out []pendingJoinRequestJSON
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(out) != 1 || out[0].ID != "req-1" || out[0].ReplicaID != 2 || !out[0].SubmittedAt.Equal(submitted) {
		t.Fatalf("result = %+v", out)
	}
}

func TestListPendingJoinRequestsTool_EmptyAndError(t *testing.T) {
	res, err := handleListPendingJoinRequests(&joinReviewAdmin{})(context.Background(), buildCallToolRequest(t, "qrdb_list_pending_join_requests", nil))
	if err != nil {
		t.Fatalf("qrdb_list_pending_join_requests: %v", err)
	}
	if got := resultText(t, res); got != "[]" {
		t.Fatalf("empty result = %q, want []", got)
	}

	_, err = handleListPendingJoinRequests(&joinReviewAdmin{err: errJoinReview})(context.Background(), buildCallToolRequest(t, "qrdb_list_pending_join_requests", nil))
	assertToolErr(t, err, "qrdb_list_pending_join_requests")
}

func TestApproveJoinRequestTool(t *testing.T) {
	for missing, args := range map[string]map[string]any{
		"id":      {"node_id": "n2"},
		"node_id": {"id": "req-1"},
	} {
		t.Run("missing "+missing, func(t *testing.T) {
			res, err := handleApproveJoinRequest(&joinReviewAdmin{})(context.Background(), buildCallToolRequest(t, "qrdb_approve_join_request", args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.IsError || resultText(t, res) != missing+" is required" {
				t.Fatalf("result = %+v, want %q", res, missing+" is required")
			}
		})
	}

	args := map[string]any{"id": "req-1", "node_id": "n2"}
	admin := &joinReviewAdmin{}
	res, err := handleApproveJoinRequest(admin)(context.Background(), buildCallToolRequest(t, "qrdb_approve_join_request", args))
	if err != nil || resultText(t, res) != "ok" {
		t.Fatalf("approve = (%v, %v), want ok", res, err)
	}
	if admin.gotID != "req-1" || admin.gotNode != "n2" {
		t.Fatalf("reviewer got (%q, %q)", admin.gotID, admin.gotNode)
	}

	_, err = handleApproveJoinRequest(&joinReviewAdmin{err: errJoinReview})(context.Background(), buildCallToolRequest(t, "qrdb_approve_join_request", args))
	assertToolErr(t, err, "qrdb_approve_join_request")
}

func TestRejectJoinRequestTool(t *testing.T) {
	res, err := handleRejectJoinRequest(&joinReviewAdmin{})(context.Background(), buildCallToolRequest(t, "qrdb_reject_join_request", map[string]any{"reason": "unknown host"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || resultText(t, res) != "id is required" {
		t.Fatalf("result = %+v, want id is required", res)
	}

	args := map[string]any{"id": "req-1", "reason": "unknown host"}
	admin := &joinReviewAdmin{}
	res, err = handleRejectJoinRequest(admin)(context.Background(), buildCallToolRequest(t, "qrdb_reject_join_request", args))
	if err != nil || resultText(t, res) != "ok" {
		t.Fatalf("reject = (%v, %v), want ok", res, err)
	}
	if admin.gotID != "req-1" || admin.gotRe != "unknown host" {
		t.Fatalf("reviewer got (%q, %q)", admin.gotID, admin.gotRe)
	}

	_, err = handleRejectJoinRequest(&joinReviewAdmin{err: errJoinReview})(context.Background(), buildCallToolRequest(t, "qrdb_reject_join_request", args))
	assertToolErr(t, err, "qrdb_reject_join_request")
}

func TestRetireJoinIdentityTool(t *testing.T) {
	args := map[string]any{"node_id": "n2", "reason": "decommissioned"}
	admin := &joinReviewAdmin{}
	res, err := handleRetireJoinIdentity(admin)(context.Background(), buildCallToolRequest(t, "qrdb_retire_join_identity", args))
	if err != nil || resultText(t, res) != "ok" {
		t.Fatalf("retire = (%v, %v), want ok", res, err)
	}
	if admin.gotNode != "n2" || admin.gotRe != "decommissioned" {
		t.Fatalf("retirer got (%q, %q)", admin.gotNode, admin.gotRe)
	}

	_, err = handleRetireJoinIdentity(&joinReviewAdmin{err: errJoinReview})(context.Background(), buildCallToolRequest(t, "qrdb_retire_join_identity", args))
	assertToolErr(t, err, "qrdb_retire_join_identity")
}
