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
	"bytes"
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// errSentinelRestore is a stand-in underlying cause for restore guard errors
// that wrap another error (RestoreChangeIndexUnavailableError,
// MVCCBackendRequiredError, RestoreApplyError).
var errSentinelRestore = errors.New("sentinel")

func TestGRPC_RestoreToRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		restoreResult: &service.RestoreRevisionResult{
			Revision: 5,
			Changes: []service.RestoreKeyChange{
				{Key: "a", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5, ValueBytes: 3},
				{Key: "b", Action: service.RestoreActionRecreate, ToRevision: 5, ValueBytes: 1},
				{Key: "c", Action: service.RestoreActionDelete, FromRevision: 9},
			},
			Applied: 3,
			Skipped: 1,
		},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.RestoreToRevision(context.Background(), &pb.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
	})
	if err != nil {
		t.Fatalf("RestoreToRevision failed: %v", err)
	}
	if resp.GetRevision() != 5 {
		t.Fatalf("expected revision 5, got %d", resp.GetRevision())
	}
	if resp.GetApplied() != 3 || resp.GetSkipped() != 1 {
		t.Fatalf("expected applied=3 skipped=1, got applied=%d skipped=%d", resp.GetApplied(), resp.GetSkipped())
	}
	if len(resp.GetChanges()) != 3 {
		t.Fatalf("expected 3 changes, got %d", len(resp.GetChanges()))
	}
	if !bytes.Equal(resp.GetChanges()[0].GetKey(), []byte("a")) {
		t.Fatalf("expected first change key %q, got %q", "a", resp.GetChanges()[0].GetKey())
	}
	if resp.GetChanges()[0].GetAction() != pb.RestoreAction_RESTORE_ACTION_REVERT {
		t.Fatalf("expected REVERT action, got %v", resp.GetChanges()[0].GetAction())
	}
	if resp.GetChanges()[1].GetAction() != pb.RestoreAction_RESTORE_ACTION_RECREATE {
		t.Fatalf("expected RECREATE action, got %v", resp.GetChanges()[1].GetAction())
	}
	if resp.GetChanges()[2].GetAction() != pb.RestoreAction_RESTORE_ACTION_DELETE {
		t.Fatalf("expected DELETE action, got %v", resp.GetChanges()[2].GetAction())
	}

	// The request the service layer actually received should be the
	// unprefixed, decoded scope -- proving the proto bytes round-trip to a
	// Go string without corruption.
	if mvccSvc.lastRestoreReq == nil || mvccSvc.lastRestoreReq.Prefix != "prefix" {
		t.Fatalf("expected service to receive prefix %q, got %+v", "prefix", mvccSvc.lastRestoreReq)
	}
}

func TestGRPC_RestoreToRevision_DryRun(t *testing.T) {
	mvccSvc := &mockMVCCService{
		restoreResult: &service.RestoreRevisionResult{
			Revision: 5,
			Changes:  []service.RestoreKeyChange{{Key: "a", Action: service.RestoreActionRevert, ToRevision: 5}},
			DryRun:   true,
		},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.RestoreToRevision(context.Background(), &pb.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision failed: %v", err)
	}
	if !resp.GetDryRun() {
		t.Fatal("expected dry_run=true in response")
	}
	if resp.GetApplied() != 0 {
		t.Fatalf("expected 0 applied on a dry run, got %d", resp.GetApplied())
	}
	if mvccSvc.lastRestoreReq == nil || !mvccSvc.lastRestoreReq.DryRun {
		t.Fatal("expected the service to receive DryRun=true")
	}
}

func TestGRPC_RestoreToRevision_ForceWithConflicts(t *testing.T) {
	mvccSvc := &mockMVCCService{
		restoreResult: &service.RestoreRevisionResult{
			Revision: 5,
			Changes: []service.RestoreKeyChange{
				{Key: "a", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5},
				{Key: "b", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5},
			},
			Conflicts: []service.RestoreKeyChange{
				{Key: "b", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5},
			},
			Applied:    1,
			Conflicted: 1,
		},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.RestoreToRevision(context.Background(), &pb.RestoreRevisionRequest{
		Revision: 5,
		Keys:     [][]byte{[]byte("a"), []byte("b")},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision failed: %v", err)
	}
	if resp.GetApplied() != 1 || resp.GetConflicted() != 1 {
		t.Fatalf("expected applied=1 conflicted=1, got applied=%d conflicted=%d", resp.GetApplied(), resp.GetConflicted())
	}
	if len(resp.GetConflicts()) != 1 || !bytes.Equal(resp.GetConflicts()[0].GetKey(), []byte("b")) {
		t.Fatalf("expected conflict on key %q, got %+v", "b", resp.GetConflicts())
	}
	if len(mvccSvc.lastRestoreReq.Keys) != 2 || mvccSvc.lastRestoreReq.Keys[0] != "a" || mvccSvc.lastRestoreReq.Keys[1] != "b" {
		t.Fatalf("expected service to receive keys [a b], got %v", mvccSvc.lastRestoreReq.Keys)
	}
}

func TestGRPC_RestoreToRevision_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RestoreToRevision(context.Background(), &pb.RestoreRevisionRequest{
		Revision: 1, Prefix: []byte("k"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// TestGRPC_RestoreToRevision_GuardErrors pins the status code each restore
// guard error maps to, matching the REST/QUIC mappings for the same errors.
func TestGRPC_RestoreToRevision_GuardErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"ScopeEmpty", &service.RestoreScopeEmptyError{}, codes.InvalidArgument},
		{"ScopeAmbiguous", &service.RestoreScopeAmbiguousError{}, codes.InvalidArgument},
		{"RevisionInvalid", &service.RestoreRevisionInvalidError{Revision: -1}, codes.InvalidArgument},
		{"RevisionFuture", &service.RestoreRevisionFutureError{Requested: 99, Current: 5}, codes.InvalidArgument},
		{"ScopeTooLarge", &service.RestoreScopeTooLargeError{Limit: 10000, Scope: "prefix"}, codes.InvalidArgument},
		{"UnboundedPrefix", &service.RestoreUnboundedPrefixError{Prefix: "\xff"}, codes.InvalidArgument},
		{"InternalKey", &service.RestoreInternalKeyError{Prefix: "__sys:"}, codes.PermissionDenied},
		{"ChangeIndexUnavailable", &service.RestoreChangeIndexUnavailableError{Err: errSentinelRestore}, codes.Unimplemented},
		{"BackendRequired", &service.MVCCBackendRequiredError{Err: errSentinelRestore}, codes.Unimplemented},
		{"RevisionCompacted", &service.RevisionCompactedError{Requested: 1, Compacted: 5}, codes.OutOfRange},
		{"ApplyError", &service.RestoreApplyError{Key: "a", Action: service.RestoreActionRevert, Applied: 1, Err: errSentinelRestore}, codes.Internal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mvccSvc := &mockMVCCService{restoreErr: tc.err}
			client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
			defer cleanup()

			_, err := client.RestoreToRevision(context.Background(), &pb.RestoreRevisionRequest{
				Revision: 1, Prefix: []byte("k"),
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			st, _ := status.FromError(err)
			if st.Code() != tc.want {
				t.Fatalf("expected %s, got %s (%v)", tc.want, st.Code(), err)
			}
		})
	}
}
