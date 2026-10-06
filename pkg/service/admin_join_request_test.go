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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func TestAdminService_ListPendingJoinRequests_NotConfigured(t *testing.T) {
	svc := NewAdminService(&mockHost{})
	reviewer := svc.(JoinRequestReviewer)
	_, err := reviewer.ListPendingJoinRequests(context.Background())
	var notConfigured *ClusterJoinTokenNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("ListPendingJoinRequests with no store configured = %v, want *ClusterJoinTokenNotConfiguredError", err)
	}
}

func TestAdminService_JoinRequestReviewer_ApproveThenList(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := NewAdminService(&mockHost{}, WithClusterJoinTokenStore(store, "test.cluster"))
	reviewer := svc.(JoinRequestReviewer)

	rec, err := clusterenroll.SubmitPendingJoinRequest(context.Background(), store, &clusterenroll.JoinRequest{CSRDER: []byte("csr-bytes")}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	pending, err := reviewer.ListPendingJoinRequests(context.Background())
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != rec.ID {
		t.Fatalf("ListPendingJoinRequests = %+v, want exactly one entry with ID %q", pending, rec.ID)
	}

	if err := reviewer.ApproveJoinRequest(context.Background(), rec.ID, "node-7"); err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}

	// Approved: no longer in the pending list (the list is "awaiting a
	// decision", not a full history).
	pending, err = reviewer.ListPendingJoinRequests(context.Background())
	if err != nil {
		t.Fatalf("ListPendingJoinRequests after approval: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("ListPendingJoinRequests after approval = %+v, want none", pending)
	}
}

func TestAdminService_JoinRequestReviewer_Reject(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := NewAdminService(&mockHost{}, WithClusterJoinTokenStore(store, "test.cluster"))
	reviewer := svc.(JoinRequestReviewer)

	rec, err := clusterenroll.SubmitPendingJoinRequest(context.Background(), store, &clusterenroll.JoinRequest{CSRDER: []byte("csr-bytes-2")}, 0)
	if err != nil {
		t.Fatalf("SubmitPendingJoinRequest: %v", err)
	}

	if err := reviewer.RejectJoinRequest(context.Background(), rec.ID, "unrecognized source"); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}

	// A second decision on the same (now-decided) request is refused.
	err = reviewer.RejectJoinRequest(context.Background(), rec.ID, "again")
	var notFound *clusterenroll.PendingJoinRequestNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("RejectJoinRequest on an already-decided entry = %v, want *clusterenroll.PendingJoinRequestNotFoundError", err)
	}
}

func TestAdminService_ApproveJoinRequest_NotConfigured(t *testing.T) {
	svc := NewAdminService(&mockHost{})
	reviewer := svc.(JoinRequestReviewer)
	err := reviewer.ApproveJoinRequest(context.Background(), "some-id", "node-1")
	var notConfigured *ClusterJoinTokenNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("ApproveJoinRequest with no store configured = %v, want *ClusterJoinTokenNotConfiguredError", err)
	}
}
