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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// fnAdminService extensions: v0.44 admin surface (add-observer, join-request
// review, join-identity retirement). Each method mirrors the mintJoinTokenFn
// pattern in admin_test.go: absent its Fn, it reports the same "not
// configured"-shaped error the real AdminService returns with no join-token
// store bound, so a test that forgets to set one fails closed rather than
// panicking on a nil func call.
// ---------------------------------------------------------------------------

func (m *fnAdminService) AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	if m.addObserverMemberFn != nil {
		return m.addObserverMemberFn(ctx, shardID, replicaID, raftAddr, identity)
	}
	return &service.ClusterJoinRequestReviewUnsupportedError{}
}

func (m *fnAdminService) ListPendingJoinRequests(ctx context.Context) ([]*service.PendingJoinRequest, error) {
	if m.listPendingJoinRequestsFn != nil {
		return m.listPendingJoinRequestsFn(ctx)
	}
	return nil, &service.ClusterJoinTokenNotConfiguredError{}
}

func (m *fnAdminService) ApproveJoinRequest(ctx context.Context, id, nodeID string) error {
	if m.approveJoinRequestFn != nil {
		return m.approveJoinRequestFn(ctx, id, nodeID)
	}
	return &service.ClusterJoinTokenNotConfiguredError{}
}

func (m *fnAdminService) RejectJoinRequest(ctx context.Context, id, reason string) error {
	if m.rejectJoinRequestFn != nil {
		return m.rejectJoinRequestFn(ctx, id, reason)
	}
	return &service.ClusterJoinTokenNotConfiguredError{}
}

func (m *fnAdminService) RetireJoinIdentity(ctx context.Context, nodeID, reason string) error {
	if m.retireJoinIdentityFn != nil {
		return m.retireJoinIdentityFn(ctx, nodeID, reason)
	}
	return &service.ClusterJoinTokenNotConfiguredError{}
}

var (
	_ service.ObserverMemberAdder = (*fnAdminService)(nil)
	_ service.JoinRequestReviewer = (*fnAdminService)(nil)
	_ service.JoinIdentityRetirer = (*fnAdminService)(nil)
)

// ---------------------------------------------------------------------------
// member add-observer tests
// ---------------------------------------------------------------------------

func TestAdminMemberAddObserver_Success(t *testing.T) {
	var gotShard, gotReplica uint64
	var gotAddr, gotIdentity string
	identity := "spki:" + strings.Repeat("cd", 32)

	adminSvc := &fnAdminService{
		addObserverMemberFn: func(_ context.Context, shardID, replicaID uint64, raftAddr, ident string) error {
			gotShard, gotReplica, gotAddr, gotIdentity = shardID, replicaID, raftAddr, ident
			return nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc,
		"member", "add-observer", "10.0.0.5:63001",
		"--shard-id", "2",
		"--replica-id", "40",
		"--identity", identity,
	)
	if err != nil {
		t.Fatalf("admin member add-observer error: %v", err)
	}
	if !strings.Contains(output, "OK") {
		t.Errorf("output missing 'OK', got: %q", output)
	}
	if gotShard != 2 {
		t.Errorf("shardID = %d, want 2", gotShard)
	}
	if gotReplica != 40 {
		t.Errorf("replicaID = %d, want 40", gotReplica)
	}
	if gotAddr != "10.0.0.5:63001" {
		t.Errorf("address = %q, want %q", gotAddr, "10.0.0.5:63001")
	}
	if gotIdentity != identity {
		t.Errorf("identity = %q, want %q", gotIdentity, identity)
	}
}

func TestAdminMemberAddObserver_MissingShardID(t *testing.T) {
	adminSvc := &fnAdminService{
		addObserverMemberFn: func(context.Context, uint64, uint64, string, string) error { return nil },
	}

	_, err := executeAdminCmd(t, adminSvc,
		"member", "add-observer", "10.0.0.5:63001",
		"--replica-id", "40",
	)
	if err == nil {
		t.Fatal("expected error for missing --shard-id")
	}
	if !strings.Contains(err.Error(), "shard-id") {
		t.Errorf("error should mention shard-id, got: %v", err)
	}
}

func TestAdminMemberAddObserver_ServiceError(t *testing.T) {
	adminSvc := &fnAdminService{
		addObserverMemberFn: func(context.Context, uint64, uint64, string, string) error {
			return errors.New("raft: shard not found")
		},
	}

	_, err := executeAdminCmd(t, adminSvc,
		"member", "add-observer", "10.0.0.5:63001",
		"--shard-id", "1",
		"--replica-id", "2",
	)
	if err == nil {
		t.Fatal("expected service error to propagate")
	}
	if !strings.Contains(err.Error(), "shard not found") {
		t.Errorf("error = %v, want it to mention the service failure", err)
	}
}

// TestObserverMemberAddUnsupportedError pins the typed error's message, the
// branch taken when a connected SDK transport's concrete type never
// implements transport.ObserverMemberAdder at all (grpc, quic, unix, mcp --
// see client.Admin().(transport.ObserverMemberAdder) in
// newAdminMemberAddObserverCmd). The embedded transport used by
// executeAdminCmd implements the interface unconditionally, so this branch is
// unreachable through it and is pinned directly instead, matching
// TestJoinTokenMintUnsupportedError's approach for the analogous case.
func TestObserverMemberAddUnsupportedError(t *testing.T) {
	err := &ObserverMemberAddUnsupportedError{}
	if !strings.Contains(err.Error(), "does not support adding observer members") {
		t.Errorf("Error() = %q, missing expected substring", err.Error())
	}
}

// ---------------------------------------------------------------------------
// join-request list tests
// ---------------------------------------------------------------------------

func TestAdminJoinRequestList_Table(t *testing.T) {
	submitted := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expires := submitted.Add(72 * time.Hour)

	adminSvc := &fnAdminService{
		listPendingJoinRequestsFn: func(context.Context) ([]*service.PendingJoinRequest, error) {
			return []*service.PendingJoinRequest{
				{
					ID:          "req-1",
					NodeURI:     "spiffe://example.org/node/n1",
					ReplicaID:   7,
					SubmittedAt: submitted,
					ExpiresAt:   expires,
				},
			}, nil
		},
	}

	out, err := executeAdminCmd(t, adminSvc, "join-request", "list")
	if err != nil {
		t.Fatalf("admin join-request list error: %v", err)
	}
	for _, want := range []string{
		"ID:           req-1",
		"NodeURI:      spiffe://example.org/node/n1",
		"ReplicaID:    7",
		"SubmittedAt:  " + submitted.Format(time.RFC3339),
		"ExpiresAt:    " + expires.Format(time.RFC3339),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got: %q", want, out)
		}
	}
}

func TestAdminJoinRequestList_Empty(t *testing.T) {
	adminSvc := &fnAdminService{
		listPendingJoinRequestsFn: func(context.Context) ([]*service.PendingJoinRequest, error) {
			return nil, nil
		},
	}

	out, err := executeAdminCmd(t, adminSvc, "join-request", "list")
	if err != nil {
		t.Fatalf("admin join-request list error: %v", err)
	}
	if !strings.Contains(out, "No join requests awaiting a decision.") {
		t.Errorf("output = %q, want the empty-queue message", out)
	}
}

func TestAdminJoinRequestList_JSON(t *testing.T) {
	adminSvc := &fnAdminService{
		listPendingJoinRequestsFn: func(context.Context) ([]*service.PendingJoinRequest, error) {
			return []*service.PendingJoinRequest{{ID: "req-2", ReplicaID: 3}}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	out, err := executeAdminCmd(t, adminSvc, "join-request", "list")
	if err != nil {
		t.Fatalf("admin join-request list json error: %v", err)
	}

	var got []transport.PendingJoinRequest
	if jsonErr := json.Unmarshal([]byte(out), &got); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, out)
	}
	if len(got) != 1 || got[0].ID != "req-2" || got[0].ReplicaID != 3 {
		t.Errorf("got = %+v, want one entry req-2/3", got)
	}
}

func TestAdminJoinRequestList_YAML(t *testing.T) {
	adminSvc := &fnAdminService{
		listPendingJoinRequestsFn: func(context.Context) ([]*service.PendingJoinRequest, error) {
			return []*service.PendingJoinRequest{{ID: "req-3"}}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "yaml"
	t.Cleanup(func() { outputFormat = oldFormat })

	out, err := executeAdminCmd(t, adminSvc, "join-request", "list")
	if err != nil {
		t.Fatalf("admin join-request list yaml error: %v", err)
	}
	if !strings.Contains(out, "req-3") {
		t.Errorf("output missing req-3, got: %q", out)
	}
}

func TestAdminJoinRequestList_ServiceError(t *testing.T) {
	adminSvc := &fnAdminService{
		listPendingJoinRequestsFn: func(context.Context) ([]*service.PendingJoinRequest, error) {
			return nil, errors.New("store unavailable")
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "join-request", "list")
	if err == nil {
		t.Fatal("expected error from join-request list")
	}
}

// TestJoinRequestReviewUnsupportedError pins the typed error's message for
// the same reason TestObserverMemberAddUnsupportedError does: the embedded
// transport always implements transport.JoinRequestReviewer, so the branch
// is pinned directly rather than driven through the CLI.
func TestJoinRequestReviewUnsupportedError(t *testing.T) {
	err := &JoinRequestReviewUnsupportedError{}
	if !strings.Contains(err.Error(), "does not support reviewing join requests") {
		t.Errorf("Error() = %q, missing expected substring", err.Error())
	}
}

// ---------------------------------------------------------------------------
// join-request approve/reject tests
// ---------------------------------------------------------------------------

func TestAdminJoinRequestApprove_Success(t *testing.T) {
	var gotID, gotNode string
	adminSvc := &fnAdminService{
		approveJoinRequestFn: func(_ context.Context, id, nodeID string) error {
			gotID, gotNode = id, nodeID
			return nil
		},
	}

	out, err := executeAdminCmd(t, adminSvc, "join-request", "approve", "req-1", "--node-id", "node-7")
	if err != nil {
		t.Fatalf("admin join-request approve error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("output missing 'OK', got: %q", out)
	}
	if gotID != "req-1" {
		t.Errorf("id = %q, want req-1", gotID)
	}
	if gotNode != "node-7" {
		t.Errorf("nodeID = %q, want node-7", gotNode)
	}
}

func TestAdminJoinRequestApprove_MissingNodeID(t *testing.T) {
	adminSvc := &fnAdminService{
		approveJoinRequestFn: func(context.Context, string, string) error { return nil },
	}

	_, err := executeAdminCmd(t, adminSvc, "join-request", "approve", "req-1")
	if err == nil {
		t.Fatal("expected error for missing --node-id")
	}
	if !strings.Contains(err.Error(), "node-id") {
		t.Errorf("error should mention node-id, got: %v", err)
	}
}

func TestAdminJoinRequestApprove_ServiceError(t *testing.T) {
	adminSvc := &fnAdminService{
		approveJoinRequestFn: func(context.Context, string, string) error {
			return errors.New("join request not found")
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "join-request", "approve", "req-1", "--node-id", "node-7")
	if err == nil {
		t.Fatal("expected service error to propagate")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v", err)
	}
}

func TestAdminJoinRequestReject_Success(t *testing.T) {
	var gotID, gotReason string
	adminSvc := &fnAdminService{
		rejectJoinRequestFn: func(_ context.Context, id, reason string) error {
			gotID, gotReason = id, reason
			return nil
		},
	}

	out, err := executeAdminCmd(t, adminSvc, "join-request", "reject", "req-2", "--reason", "unknown device")
	if err != nil {
		t.Fatalf("admin join-request reject error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("output missing 'OK', got: %q", out)
	}
	if gotID != "req-2" {
		t.Errorf("id = %q, want req-2", gotID)
	}
	if gotReason != "unknown device" {
		t.Errorf("reason = %q, want %q", gotReason, "unknown device")
	}
}

// TestAdminJoinRequestReject_NoReason pins that --reason is optional (no
// MarkFlagRequired in newAdminJoinRequestRejectCmd), unlike approve's
// --node-id.
func TestAdminJoinRequestReject_NoReason(t *testing.T) {
	var gotReason string
	called := false
	adminSvc := &fnAdminService{
		rejectJoinRequestFn: func(_ context.Context, _ string, reason string) error {
			called = true
			gotReason = reason
			return nil
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "join-request", "reject", "req-2")
	if err != nil {
		t.Fatalf("admin join-request reject (no reason) error: %v", err)
	}
	if !called {
		t.Fatal("expected RejectJoinRequest to be called")
	}
	if gotReason != "" {
		t.Errorf("reason = %q, want empty default", gotReason)
	}
}

func TestAdminJoinRequestReject_ServiceError(t *testing.T) {
	adminSvc := &fnAdminService{
		rejectJoinRequestFn: func(context.Context, string, string) error {
			return errors.New("already decided")
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "join-request", "reject", "req-2")
	if err == nil {
		t.Fatal("expected service error to propagate")
	}
}

// ---------------------------------------------------------------------------
// join-token retire tests
// ---------------------------------------------------------------------------

func TestAdminJoinTokenRetire_Success(t *testing.T) {
	var gotNode, gotReason string
	adminSvc := &fnAdminService{
		retireJoinIdentityFn: func(_ context.Context, nodeID, reason string) error {
			gotNode, gotReason = nodeID, reason
			return nil
		},
	}

	out, err := executeAdminCmd(t, adminSvc, "join-token", "retire", "node-9", "--reason", "decommissioned")
	if err != nil {
		t.Fatalf("admin join-token retire error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("output missing 'OK', got: %q", out)
	}
	if gotNode != "node-9" {
		t.Errorf("nodeID = %q, want node-9", gotNode)
	}
	if gotReason != "decommissioned" {
		t.Errorf("reason = %q, want %q", gotReason, "decommissioned")
	}
}

func TestAdminJoinTokenRetire_NoReason(t *testing.T) {
	called := false
	adminSvc := &fnAdminService{
		retireJoinIdentityFn: func(_ context.Context, _, reason string) error {
			called = true
			if reason != "" {
				t.Errorf("reason = %q, want empty default", reason)
			}
			return nil
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "join-token", "retire", "node-9")
	if err != nil {
		t.Fatalf("admin join-token retire (no reason) error: %v", err)
	}
	if !called {
		t.Fatal("expected RetireJoinIdentity to be called")
	}
}

func TestAdminJoinTokenRetire_ServiceError(t *testing.T) {
	adminSvc := &fnAdminService{
		retireJoinIdentityFn: func(context.Context, string, string) error {
			return &service.ClusterJoinTokenNotConfiguredError{}
		},
	}

	_, err := executeAdminCmd(t, adminSvc, "join-token", "retire", "node-9")
	var notConfigured *service.ClusterJoinTokenNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("admin join-token retire (service error) = %v, want *service.ClusterJoinTokenNotConfiguredError", err)
	}
}
