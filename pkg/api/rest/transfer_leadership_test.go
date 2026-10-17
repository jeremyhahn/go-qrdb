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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// leaderTransferAdmin is a mockAdminService that also implements
// service.LeaderTransferrer.
type leaderTransferAdmin struct {
	*mockAdminService
	transferFn func(ctx context.Context, shardID, replicaID uint64) error
}

// TransferLeadership implements service.LeaderTransferrer.
func (m *leaderTransferAdmin) TransferLeadership(ctx context.Context, shardID, replicaID uint64) error {
	return m.transferFn(ctx, shardID, replicaID)
}

var _ service.LeaderTransferrer = (*leaderTransferAdmin)(nil)

const transferLeadershipPath = "/membership/transfer-leadership"

func postTransferLeadership(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, transferLeadershipPath, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

func TestHandler_MembershipTransferLeadership_OK(t *testing.T) {
	var gotShardID, gotReplicaID uint64
	adminSvc := &leaderTransferAdmin{
		mockAdminService: &mockAdminService{},
		transferFn: func(_ context.Context, shardID, replicaID uint64) error {
			gotShardID, gotReplicaID = shardID, replicaID
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	rec := postTransferLeadership(t, srv, `{"shard_id":1,"replica_id":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body)
	}
	if gotShardID != 1 || gotReplicaID != 3 {
		t.Fatalf("transfer(shard=%d, replica=%d), want (1, 3)", gotShardID, gotReplicaID)
	}
}

func TestHandler_MembershipTransferLeadership_TimeoutCarriesTheRefusal(t *testing.T) {
	want := &service.LeaderTransferTimeoutError{ShardID: 1, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second}
	adminSvc := &leaderTransferAdmin{
		mockAdminService: &mockAdminService{},
		transferFn:       func(context.Context, uint64, uint64) error { return want },
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	rec := postTransferLeadership(t, srv, `{"shard_id":1,"replica_id":3}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != replicaforward.ReasonLeaderTransferTimeout {
		t.Fatalf("code = %q, want %s", body.Code, replicaforward.ReasonLeaderTransferTimeout)
	}
	got, err := replicaforward.ParseLeaderTransferTimeout(body.Metadata)
	if err != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", err)
	}
	if *got != *want {
		t.Fatalf("rebuilt %+v, want %+v", *got, *want)
	}
}

// TestHandler_MembershipTransferLeadership_ServiceOwnsTheBound verifies the
// handler adds no deadline of its own: the service's wait bound must be the
// one that ends a stalled transfer, or a handler deadline as long as it
// races it and answers a bare context error instead of the typed refusal.
func TestHandler_MembershipTransferLeadership_ServiceOwnsTheBound(t *testing.T) {
	adminSvc := &leaderTransferAdmin{
		mockAdminService: &mockAdminService{},
		transferFn: func(ctx context.Context, _, _ uint64) error {
			if deadline, ok := ctx.Deadline(); ok {
				t.Errorf("service received a deadline %s away, want none", time.Until(deadline))
			}
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	rec := postTransferLeadership(t, srv, `{"shard_id":1,"replica_id":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body)
	}
}

func TestHandler_MembershipTransferLeadership_Unsupported(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")

	rec := postTransferLeadership(t, srv, `{"shard_id":1,"replica_id":3}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

func TestHandler_MembershipTransferLeadership_BadBody(t *testing.T) {
	adminSvc := &leaderTransferAdmin{
		mockAdminService: &mockAdminService{},
		transferFn: func(context.Context, uint64, uint64) error {
			t.Fatal("a malformed body reached the service")
			return nil
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	rec := postTransferLeadership(t, srv, "{bad")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_MembershipTransferLeadership_UnknownReplica(t *testing.T) {
	adminSvc := &leaderTransferAdmin{
		mockAdminService: &mockAdminService{},
		transferFn: func(_ context.Context, shardID, replicaID uint64) error {
			return &service.UnknownReplicaError{ShardID: shardID, ReplicaID: replicaID}
		},
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	rec := postTransferLeadership(t, srv, `{"shard_id":1,"replica_id":9}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandler_MembershipTransferLeadership_ForwardsOnNotLeader verifies that
// a transfer issued to a follower is forwarded to the shard's leader, so
// every node serves the operation.
func TestHandler_MembershipTransferLeadership_ForwardsOnNotLeader(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != transferLeadershipPath {
			t.Errorf("leader received path %s, want %s", r.URL.Path, transferLeadershipPath)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer leaderSrv.Close()

	adminSvc := &leaderTransferAdmin{
		mockAdminService: &mockAdminService{},
		transferFn: func(context.Context, uint64, uint64) error {
			return &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0", WithLeaderForwarding(reg))

	rec := postTransferLeadership(t, srv, `{"shard_id":1,"replica_id":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (forwarded)", rec.Code, http.StatusOK)
	}
}
