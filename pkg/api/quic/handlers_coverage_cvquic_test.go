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
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

// badProto is a payload no protobuf message unmarshals: a field tag with
// wire type 7, which does not exist.
var badProto = []byte{0xff, 0xff, 0xff, 0xff}

// ---------------------------------------------------------------------------
// Volume pool handlers
// ---------------------------------------------------------------------------

func TestHandleVolumePool_NotConfigured(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"list":         srv.handleVolumePoolList,
		"get":          srv.handleVolumePoolGet,
		"create":       srv.handleVolumePoolCreate,
		"update":       srv.handleVolumePoolUpdate,
		"delete":       srv.handleVolumePoolDelete,
		"list-tenants": srv.handleVolumePoolListTenants,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := h(context.Background(), nil)
			var nc *ServiceNotConfiguredError
			if !errors.As(err, &nc) || nc.Service != "volume" {
				t.Fatalf("err = %v, want *ServiceNotConfiguredError{volume}", err)
			}
		})
	}
}

func TestHandleVolumePool_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(&volumetest.Service{T: t}))
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"get":          srv.handleVolumePoolGet,
		"create":       srv.handleVolumePoolCreate,
		"update":       srv.handleVolumePoolUpdate,
		"delete":       srv.handleVolumePoolDelete,
		"list-tenants": srv.handleVolumePoolListTenants,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := h(context.Background(), badProto)
			var ue *UnmarshalError
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *UnmarshalError", err)
			}
		})
	}
}

func TestHandleVolumePool_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "volume refused"}
	svc := &volumetest.Service{
		T:                 t,
		ListPoolsFn:       func(context.Context) ([]volume.PoolView, error) { return nil, svcErr },
		GetPoolFn:         func(context.Context, string) (*volume.PoolDetail, error) { return nil, svcErr },
		CreatePoolFn:      func(context.Context, volume.CreatePoolRequest) (*volume.Pool, error) { return nil, svcErr },
		UpdatePoolFn:      func(context.Context, string, volume.UpdatePoolRequest) (*volume.Pool, error) { return nil, svcErr },
		DeletePoolFn:      func(context.Context, string) error { return svcErr },
		ListAssignmentsFn: func(context.Context, string, string, int) (*volume.AssignmentPage, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"list":         srv.handleVolumePoolList,
		"get":          srv.handleVolumePoolGet,
		"create":       srv.handleVolumePoolCreate,
		"update":       srv.handleVolumePoolUpdate,
		"delete":       srv.handleVolumePoolDelete,
		"list-tenants": srv.handleVolumePoolListTenants,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			if _, err := h(context.Background(), nil); !errors.Is(err, svcErr) {
				t.Fatalf("err = %v, want the service error", err)
			}
		})
	}
}

func TestHandleVolumePoolList_Success(t *testing.T) {
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
		return []volume.PoolView{{Pool: volume.Pool{Name: "fast", Provider: "dir"}}, {Pool: volume.Pool{Name: "slow"}}}, nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
	out, err := srv.handleVolumePoolList(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleVolumePoolList: %v", err)
	}
	var resp pb.VolumePoolListResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.GetPools()) != 2 || resp.GetPools()[0].GetName() != "fast" || resp.GetPools()[1].GetName() != "slow" {
		t.Fatalf("pools = %+v", resp.GetPools())
	}
}

func TestHandleVolumePoolGet_Success(t *testing.T) {
	var gotName string
	svc := &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		gotName = name
		return &volume.PoolDetail{PoolView: volume.PoolView{Pool: volume.Pool{Name: name}}}, nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
	out, err := srv.handleVolumePoolGet(context.Background(), mustMarshal(t, &pb.VolumePoolGetRequest{Name: "fast"}))
	if err != nil {
		t.Fatalf("handleVolumePoolGet: %v", err)
	}
	if gotName != "fast" {
		t.Fatalf("service got name %q, want fast", gotName)
	}
	if len(out) == 0 {
		t.Fatal("empty response")
	}
}

func TestHandleVolumePoolCreate_Success(t *testing.T) {
	var got volume.CreatePoolRequest
	svc := &volumetest.Service{T: t, CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		got = req
		return &volume.Pool{Name: req.Name, Provider: req.Provider}, nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
	out, err := srv.handleVolumePoolCreate(context.Background(), mustMarshal(t, &pb.VolumePoolCreateRequest{
		Name: "fast", Provider: "dir", Description: "d", CapacityBytes: 1 << 20, Overcommit: 1.5,
	}))
	if err != nil {
		t.Fatalf("handleVolumePoolCreate: %v", err)
	}
	if got.Name != "fast" || got.Provider != "dir" || got.Description != "d" || got.CapacityBytes != 1<<20 || got.Overcommit != 1.5 {
		t.Fatalf("service got %+v", got)
	}
	var pool pb.VolumePool
	if err := proto.Unmarshal(out, &pool); err != nil || pool.GetName() != "fast" {
		t.Fatalf("pool = %+v, err = %v", &pool, err)
	}
}

func TestHandleVolumePoolUpdate_FieldPresence(t *testing.T) {
	tests := map[string]struct {
		req      *pb.VolumePoolUpdateRequest
		wantDesc bool
		wantCap  bool
		wantOver bool
	}{
		"none set": {req: &pb.VolumePoolUpdateRequest{Name: "fast"}},
		"all set": {
			req: &pb.VolumePoolUpdateRequest{
				Name:          "fast",
				Description:   wrapperspb.String("new"),
				CapacityBytes: wrapperspb.Int64(42),
				Overcommit:    wrapperspb.Double(2),
			},
			wantDesc: true, wantCap: true, wantOver: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got volume.UpdatePoolRequest
			svc := &volumetest.Service{T: t, UpdatePoolFn: func(_ context.Context, n string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
				got = req
				return &volume.Pool{Name: n}, nil
			}}
			srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
			if _, err := srv.handleVolumePoolUpdate(context.Background(), mustMarshal(t, tc.req)); err != nil {
				t.Fatalf("handleVolumePoolUpdate: %v", err)
			}
			if (got.Description != nil) != tc.wantDesc || (got.CapacityBytes != nil) != tc.wantCap || (got.Overcommit != nil) != tc.wantOver {
				t.Fatalf("update = %+v", got)
			}
			if tc.wantDesc && (*got.Description != "new" || *got.CapacityBytes != 42 || *got.Overcommit != 2) {
				t.Fatalf("update values = %q %d %v", *got.Description, *got.CapacityBytes, *got.Overcommit)
			}
		})
	}
}

func TestHandleVolumePoolDelete_Success(t *testing.T) {
	var gotName string
	svc := &volumetest.Service{T: t, DeletePoolFn: func(_ context.Context, name string) error {
		gotName = name
		return nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
	out, err := srv.handleVolumePoolDelete(context.Background(), mustMarshal(t, &pb.VolumePoolDeleteRequest{Name: "fast"}))
	if err != nil || out != nil {
		t.Fatalf("handleVolumePoolDelete = %v, %v", out, err)
	}
	if gotName != "fast" {
		t.Fatalf("service got %q", gotName)
	}
}

func TestHandleVolumePoolListTenants_Success(t *testing.T) {
	svc := &volumetest.Service{T: t, ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
		if pool != "fast" || token != "t1" || size != 5 {
			t.Errorf("service got pool=%q token=%q size=%d", pool, token, size)
		}
		return &volume.AssignmentPage{
			Assignments:   []volume.Assignment{{TenantID: "acme", Pool: pool, ReservedBytes: 7}},
			NextPageToken: "t2",
		}, nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithVolumeService(svc))
	out, err := srv.handleVolumePoolListTenants(context.Background(), mustMarshal(t, &pb.VolumePoolListTenantsRequest{
		Pool: "fast", PageToken: "t1", PageSize: 5,
	}))
	if err != nil {
		t.Fatalf("handleVolumePoolListTenants: %v", err)
	}
	var resp pb.VolumePoolListTenantsResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.GetNextPageToken() != "t2" || len(resp.GetTenants()) != 1 ||
		resp.GetTenants()[0].GetTenantId() != "acme" || resp.GetTenants()[0].GetReservedBytes() != 7 {
		t.Fatalf("resp = %+v", &resp)
	}
}

// ---------------------------------------------------------------------------
// Chunked import handlers
// ---------------------------------------------------------------------------

func TestHandleImport_NotConfigured(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"begin":  srv.handleImportBegin,
		"append": srv.handleImportAppend,
		"commit": srv.handleImportCommit,
		"abort":  srv.handleImportAbort,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := h(context.Background(), nil)
			var nc *ServiceNotConfiguredError
			if !errors.As(err, &nc) || nc.Service != "batch" {
				t.Fatalf("err = %v, want *ServiceNotConfiguredError{batch}", err)
			}
		})
	}
}

func TestHandleImport_UnmarshalAndServiceErrors(t *testing.T) {
	svcErr := &testError{msg: "import refused"}
	batchSvc := &mockBatchService{
		importBeginFn:  func(context.Context, service.ImportOptions) (string, error) { return "", svcErr },
		importAppendFn: func(context.Context, string, uint64, []byte) error { return svcErr },
		importCommitFn: func(context.Context, string) (*service.ImportResult, error) { return nil, svcErr },
		importAbortFn:  func(context.Context, string) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBatch(batchSvc))
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"begin":  srv.handleImportBegin,
		"append": srv.handleImportAppend,
		"commit": srv.handleImportCommit,
		"abort":  srv.handleImportAbort,
	}
	for name, h := range handlers {
		t.Run(name+"/unmarshal", func(t *testing.T) {
			var ue *UnmarshalError
			if _, err := h(context.Background(), badProto); !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *UnmarshalError", err)
			}
		})
		t.Run(name+"/service", func(t *testing.T) {
			if _, err := h(context.Background(), nil); !errors.Is(err, svcErr) {
				t.Fatalf("err = %v, want the service error", err)
			}
		})
	}
}

func TestHandleImport_Success(t *testing.T) {
	var (
		gotOpts   service.ImportOptions
		gotSeq    uint64
		gotChunk  []byte
		gotCommit string
		gotAbort  string
	)
	batchSvc := &mockBatchService{
		importBeginFn: func(_ context.Context, opts service.ImportOptions) (string, error) {
			gotOpts = opts
			return "sess-1", nil
		},
		importAppendFn: func(_ context.Context, _ string, seq uint64, chunk []byte) error {
			gotSeq, gotChunk = seq, chunk
			return nil
		},
		importCommitFn: func(_ context.Context, id string) (*service.ImportResult, error) {
			gotCommit = id
			return &service.ImportResult{Total: 3, Succeeded: 2, Failed: 1,
				Errors: []service.ImportError{{Row: 2, Key: "k2", Message: "bad row"}}}, nil
		},
		importAbortFn: func(_ context.Context, id string) error {
			gotAbort = id
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBatch(batchSvc))
	ctx := context.Background()

	out, err := srv.handleImportBegin(ctx, mustMarshal(t, &pb.ImportBeginRequest{
		Format: "csv", Filename: "f.csv", KeyField: "id", SkipHeader: true, Transactional: true,
	}))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var begin pb.ImportBeginResponse
	if err := proto.Unmarshal(out, &begin); err != nil || begin.GetSessionId() != "sess-1" {
		t.Fatalf("begin resp = %+v, err = %v", &begin, err)
	}
	if gotOpts.Format != "csv" || gotOpts.Filename != "f.csv" || gotOpts.KeyField != "id" || !gotOpts.SkipHeader || !gotOpts.Transactional {
		t.Fatalf("opts = %+v", gotOpts)
	}

	if _, err := srv.handleImportAppend(ctx, mustMarshal(t, &pb.ImportAppendRequest{SessionId: "sess-1", Seq: 4, Chunk: []byte("a,b")})); err != nil {
		t.Fatalf("append: %v", err)
	}
	if gotSeq != 4 || string(gotChunk) != "a,b" {
		t.Fatalf("append got seq=%d chunk=%q", gotSeq, gotChunk)
	}

	out, err = srv.handleImportCommit(ctx, mustMarshal(t, &pb.ImportCommitRequest{SessionId: "sess-1"}))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	var commit pb.ImportCommitResponse
	if err := proto.Unmarshal(out, &commit); err != nil {
		t.Fatalf("unmarshal commit: %v", err)
	}
	if gotCommit != "sess-1" || commit.GetTotal() != 3 || commit.GetSucceeded() != 2 || commit.GetFailed() != 1 ||
		len(commit.GetErrors()) != 1 || commit.GetErrors()[0].GetRow() != 2 || commit.GetErrors()[0].GetKey() != "k2" {
		t.Fatalf("commit resp = %+v", &commit)
	}

	if _, err := srv.handleImportAbort(ctx, mustMarshal(t, &pb.ImportAbortRequest{SessionId: "sess-2"})); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if gotAbort != "sess-2" {
		t.Fatalf("abort got %q", gotAbort)
	}
}

// ---------------------------------------------------------------------------
// Join-request review, observer admission and identity retirement
// ---------------------------------------------------------------------------

// joinReviewAdmin extends mockAdminService with the optional join-review
// interfaces the handlers type-assert for.
type joinReviewAdmin struct {
	mockAdminService
	err      error
	pending  []*service.PendingJoinRequest
	gotID    string
	gotArg   string
	observer [3]any
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
	a.gotID, a.gotArg = id, nodeID
	return a.err
}

func (a *joinReviewAdmin) RejectJoinRequest(_ context.Context, id, reason string) error {
	a.gotID, a.gotArg = id, reason
	return a.err
}

func (a *joinReviewAdmin) AddObserverMember(_ context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	a.observer = [3]any{shardID, replicaID, raftAddr}
	a.gotArg = identity
	return a.err
}

func (a *joinReviewAdmin) RetireJoinIdentity(_ context.Context, nodeID, reason string) error {
	a.gotID, a.gotArg = nodeID, reason
	return a.err
}

func TestJoinReviewHandlers_Unsupported(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	review := map[string]func(context.Context, []byte) ([]byte, error){
		"list":     srv.handleListPendingJoinRequests,
		"approve":  srv.handleApproveJoinRequest,
		"reject":   srv.handleRejectJoinRequest,
		"observer": srv.handleAddObserverMember,
	}
	for name, h := range review {
		t.Run(name, func(t *testing.T) {
			var ue *service.ClusterJoinRequestReviewUnsupportedError
			if _, err := h(context.Background(), nil); !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *ClusterJoinRequestReviewUnsupportedError", err)
			}
		})
	}
	t.Run("retire", func(t *testing.T) {
		var nc *service.ClusterJoinTokenNotConfiguredError
		if _, err := srv.handleRetireJoinIdentity(context.Background(), nil); !errors.As(err, &nc) {
			t.Fatalf("err = %v, want *ClusterJoinTokenNotConfiguredError", err)
		}
	})
}

func TestJoinReviewHandlers_UnmarshalAndServiceErrors(t *testing.T) {
	svcErr := &testError{msg: "review refused"}
	admin := &joinReviewAdmin{err: svcErr}
	srv := newTestServer(&mockKVService{}, admin)
	handlers := map[string]func(context.Context, []byte) ([]byte, error){
		"approve":  srv.handleApproveJoinRequest,
		"reject":   srv.handleRejectJoinRequest,
		"observer": srv.handleAddObserverMember,
		"retire":   srv.handleRetireJoinIdentity,
	}
	for name, h := range handlers {
		t.Run(name+"/unmarshal", func(t *testing.T) {
			var ue *UnmarshalError
			if _, err := h(context.Background(), badProto); !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *UnmarshalError", err)
			}
		})
		t.Run(name+"/service", func(t *testing.T) {
			if _, err := h(context.Background(), nil); !errors.Is(err, svcErr) {
				t.Fatalf("err = %v, want the service error", err)
			}
		})
	}
	t.Run("list/service", func(t *testing.T) {
		if _, err := srv.handleListPendingJoinRequests(context.Background(), nil); !errors.Is(err, svcErr) {
			t.Fatalf("err = %v, want the service error", err)
		}
	})
}

func TestHandleListPendingJoinRequests_Success(t *testing.T) {
	submitted := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	admin := &joinReviewAdmin{pending: []*service.PendingJoinRequest{{
		ID: "abc", NodeURI: "spiffe://db.example.com/node/n2", ReplicaID: 2,
		Features: []string{"f1"}, SubmittedAt: submitted, ExpiresAt: submitted.Add(time.Hour),
	}}}
	srv := newTestServer(&mockKVService{}, admin)
	out, err := srv.handleListPendingJoinRequests(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleListPendingJoinRequests: %v", err)
	}
	var resp pb.ListPendingJoinRequestsResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.GetRequests()) != 1 {
		t.Fatalf("requests = %+v", resp.GetRequests())
	}
	r := resp.GetRequests()[0]
	if r.GetId() != "abc" || r.GetReplicaId() != 2 || r.GetSubmittedAt() != "2026-10-01T11:00:00Z" || r.GetExpiresAt() != "2026-10-01T12:00:00Z" {
		t.Fatalf("request = %+v", r)
	}
}

func TestJoinReviewHandlers_Success(t *testing.T) {
	tests := map[string]struct {
		call    func(*Server) ([]byte, error)
		wantID  string
		wantArg string
	}{
		"approve": {
			call: func(s *Server) ([]byte, error) {
				return s.handleApproveJoinRequest(context.Background(), mustMarshal(t, &pb.ApproveJoinRequestRequest{Id: "abc", NodeId: "n2"}))
			},
			wantID: "abc", wantArg: "n2",
		},
		"reject": {
			call: func(s *Server) ([]byte, error) {
				return s.handleRejectJoinRequest(context.Background(), mustMarshal(t, &pb.RejectJoinRequestRequest{Id: "abc", Reason: "unknown"}))
			},
			wantID: "abc", wantArg: "unknown",
		},
		"retire": {
			call: func(s *Server) ([]byte, error) {
				return s.handleRetireJoinIdentity(context.Background(), mustMarshal(t, &pb.RetireJoinIdentityRequest{NodeId: "n3", Reason: "decommissioned"}))
			},
			wantID: "n3", wantArg: "decommissioned",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			admin := &joinReviewAdmin{}
			if _, err := tc.call(newTestServer(&mockKVService{}, admin)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if admin.gotID != tc.wantID || admin.gotArg != tc.wantArg {
				t.Fatalf("service got (%q, %q), want (%q, %q)", admin.gotID, admin.gotArg, tc.wantID, tc.wantArg)
			}
		})
	}
}

func TestHandleAddObserverMember_Success(t *testing.T) {
	admin := &joinReviewAdmin{}
	srv := newTestServer(&mockKVService{}, admin)
	_, err := srv.handleAddObserverMember(context.Background(), mustMarshal(t, &pb.AddObserverMemberRequest{
		ShardId: 1, ReplicaId: 4, RaftAddress: "n4.db.example.com:7000", MemberIdentity: "spiffe://db.example.com/node/n4",
	}))
	if err != nil {
		t.Fatalf("handleAddObserverMember: %v", err)
	}
	if admin.observer != [3]any{uint64(1), uint64(4), "n4.db.example.com:7000"} || admin.gotArg != "spiffe://db.example.com/node/n4" {
		t.Fatalf("service got %v identity=%q", admin.observer, admin.gotArg)
	}
}

// ---------------------------------------------------------------------------
// Payload and stream limits
// ---------------------------------------------------------------------------

func TestMaxLimitOptions_Resolve(t *testing.T) {
	tests := map[string]struct {
		n           int
		wantPayload int
		wantStreams int
	}{
		"zero takes the defaults":     {n: 0, wantPayload: DefaultMaxPayloadBytes, wantStreams: DefaultMaxStreamsPerConn},
		"negative takes the defaults": {n: -1, wantPayload: DefaultMaxPayloadBytes, wantStreams: DefaultMaxStreamsPerConn},
		"positive is kept":            {n: 7, wantPayload: 7, wantStreams: 7},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(&mockKVService{}, &mockAdminService{},
				WithMaxPayloadBytes(tc.n), WithMaxStreamsPerConn(tc.n))
			if srv.maxPayloadBytes != tc.n || srv.maxStreamsPerConn != tc.n {
				t.Fatalf("options stored (%d, %d), want %d", srv.maxPayloadBytes, srv.maxStreamsPerConn, tc.n)
			}
			if got := resolveMaxPayloadBytes(srv.maxPayloadBytes); got != tc.wantPayload {
				t.Fatalf("resolveMaxPayloadBytes = %d, want %d", got, tc.wantPayload)
			}
			if got := resolveMaxStreamsPerConn(srv.maxStreamsPerConn); got != tc.wantStreams {
				t.Fatalf("resolveMaxStreamsPerConn = %d, want %d", got, tc.wantStreams)
			}
		})
	}
}
