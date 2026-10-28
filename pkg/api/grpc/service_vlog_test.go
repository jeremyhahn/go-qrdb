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
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// vlogCall is one call the recording LogService received: the method, the
// log reference and the tenant the context carried.
type vlogCall struct {
	method    string
	ref       service.LogRef
	ctxTenant string
}

// recordingLogService is a LogService that records every call and answers
// with fixed responses, or with err when it is set.
type recordingLogService struct {
	mu    sync.Mutex
	calls []vlogCall
	err   error

	create  *service.CreateLogRequest
	sigType vlog.SignatureType
}

var _ service.LogService = (*recordingLogService)(nil)

func (f *recordingLogService) record(ctx context.Context, method string, ref service.LogRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, vlogCall{method: method, ref: ref, ctxTenant: tenant.FromContext(ctx)})
	return f.err
}

func (f *recordingLogService) lastCall(t *testing.T) vlogCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("the LogService was not called")
	}
	return f.calls[len(f.calls)-1]
}

func (f *recordingLogService) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *recordingLogService) CreateLog(ctx context.Context, req *service.CreateLogRequest) (*service.CreateLogResponse, error) {
	f.create = req
	if err := f.record(ctx, "CreateLog", req.LogRef); err != nil {
		return nil, err
	}
	id := vlog.LogID{TenantID: "acme", Database: req.Database, Log: req.Log}
	if req.SigningKeyType == "" {
		return &service.CreateLogResponse{ID: id}, nil
	}
	return &service.CreateLogResponse{ID: id, Key: &vlog.HeadKey{Version: 1, Ref: "k/1", PublicKey: []byte{0x30}}}, nil
}

func (f *recordingLogService) ListLogs(ctx context.Context, req *service.ListLogsRequest) (*service.ListLogsResponse, error) {
	if err := f.record(ctx, "ListLogs", service.LogRef{TenantID: req.TenantID, Database: req.Database}); err != nil {
		return nil, err
	}
	return &service.ListLogsResponse{Logs: []vlog.LogID{{TenantID: "acme", Database: req.Database, Log: "a"}}}, nil
}

func (f *recordingLogService) Append(ctx context.Context, req *service.AppendRequest) (*service.AppendResponse, error) {
	if err := f.record(ctx, "Append", req.LogRef); err != nil {
		return nil, err
	}
	return &service.AppendResponse{FirstIndex: 7, Count: len(req.Entries)}, nil
}

func (f *recordingLogService) GetEntries(ctx context.Context, req *service.GetEntriesRequest) (*service.GetEntriesResponse, error) {
	if err := f.record(ctx, "GetEntries", req.LogRef); err != nil {
		return nil, err
	}
	return &service.GetEntriesResponse{Entries: []vlog.Entry{
		{Index: req.From, Body: []byte("b")},
		{Index: req.From + 1, Expired: true},
	}}, nil
}

func (f *recordingLogService) ProveInclusion(ctx context.Context, req *service.InclusionRequest) (*service.InclusionResponse, error) {
	if err := f.record(ctx, "ProveInclusion", req.LogRef); err != nil {
		return nil, err
	}
	return &service.InclusionResponse{Index: req.Index, Size: req.Size, Hashes: [][]byte{{1}, {2}}}, nil
}

func (f *recordingLogService) ProveConsistency(ctx context.Context, req *service.ConsistencyRequest) (*service.ConsistencyResponse, error) {
	if err := f.record(ctx, "ProveConsistency", req.LogRef); err != nil {
		return nil, err
	}
	return &service.ConsistencyResponse{Size1: req.Size1, Size2: req.Size2, Hashes: [][]byte{{3}}}, nil
}

func (f *recordingLogService) Head(ctx context.Context, req *service.HeadRequest) (*service.HeadResponse, error) {
	if err := f.record(ctx, "Head", req.LogRef); err != nil {
		return nil, err
	}
	return &service.HeadResponse{Head: vlog.Head{Size: 9, Root: []byte{9}, BodiesFrom: 2, Checkpoint: []byte("cp"), CheckpointSize: 8}}, nil
}

func (f *recordingLogService) SignHead(ctx context.Context, req *service.SignHeadRequest) (*service.SignHeadResponse, error) {
	if err := f.record(ctx, "SignHead", req.LogRef); err != nil {
		return nil, err
	}
	sigType := f.sigType
	if sigType == "" {
		sigType = vlog.SignatureEd25519
	}
	return &service.SignHeadResponse{
		Head:       vlog.SignedHead{Size: 9, Root: []byte{9}, KeyVersion: 2, SignatureType: sigType, Signature: []byte("sig")},
		Checkpoint: []byte("checkpoint"),
	}, nil
}

func (f *recordingLogService) RotateKey(ctx context.Context, req *service.RotateKeyRequest) (*service.RotateKeyResponse, error) {
	if err := f.record(ctx, "RotateKey", req.LogRef); err != nil {
		return nil, err
	}
	return &service.RotateKeyResponse{Key: vlog.HeadKey{Version: 2, Ref: "k/2", PublicKey: []byte("pub2")}}, nil
}

func (f *recordingLogService) ExpireBodies(ctx context.Context, req *service.ExpireBodiesRequest) (*service.ExpireBodiesResponse, error) {
	if err := f.record(ctx, "ExpireBodies", req.LogRef); err != nil {
		return nil, err
	}
	return &service.ExpireBodiesResponse{Expired: req.Cutoff}, nil
}

// vlogMethod is one Vlog* RPC: its full method name, a request, and a call
// into the handler.
type vlogMethod struct {
	name    string
	full    string
	req     any
	service string
	call    func(s *qrdbServiceServer, ctx context.Context, req any) (any, error)
}

func vlogMethods() []vlogMethod {
	return []vlogMethod{
		{"VlogCreate", pb.QRDBService_VlogCreate_FullMethodName, &pb.VlogCreateRequest{Log: "audit", Origin: "db.example.com/audit"}, "CreateLog",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogCreate(ctx, r.(*pb.VlogCreateRequest))
			}},
		{"VlogList", pb.QRDBService_VlogList_FullMethodName, &pb.VlogListRequest{}, "ListLogs",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogList(ctx, r.(*pb.VlogListRequest))
			}},
		{"VlogAppend", pb.QRDBService_VlogAppend_FullMethodName, &pb.VlogAppendRequest{Log: "audit", Entries: [][]byte{[]byte("e")}}, "Append",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogAppend(ctx, r.(*pb.VlogAppendRequest))
			}},
		{"VlogGet", pb.QRDBService_VlogGet_FullMethodName, &pb.VlogGetRequest{Log: "audit", From: 3, Limit: 2}, "GetEntries",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogGet(ctx, r.(*pb.VlogGetRequest))
			}},
		{"VlogProveInclusion", pb.QRDBService_VlogProveInclusion_FullMethodName, &pb.VlogProveInclusionRequest{Log: "audit", LeafIndex: 1, TreeSize: 4}, "ProveInclusion",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogProveInclusion(ctx, r.(*pb.VlogProveInclusionRequest))
			}},
		{"VlogProveConsistency", pb.QRDBService_VlogProveConsistency_FullMethodName, &pb.VlogProveConsistencyRequest{Log: "audit", Size1: 2, Size2: 4}, "ProveConsistency",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogProveConsistency(ctx, r.(*pb.VlogProveConsistencyRequest))
			}},
		{"VlogHead", pb.QRDBService_VlogHead_FullMethodName, &pb.VlogHeadRequest{Log: "audit"}, "Head",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogHead(ctx, r.(*pb.VlogHeadRequest))
			}},
		{"VlogSignHead", pb.QRDBService_VlogSignHead_FullMethodName, &pb.VlogSignHeadRequest{Log: "audit"}, "SignHead",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogSignHead(ctx, r.(*pb.VlogSignHeadRequest))
			}},
		{"VlogExpireBodies", pb.QRDBService_VlogExpireBodies_FullMethodName, &pb.VlogExpireBodiesRequest{Log: "audit", Cutoff: 5}, "ExpireBodies",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogExpireBodies(ctx, r.(*pb.VlogExpireBodiesRequest))
			}},
		{"VlogRotateKey", pb.QRDBService_VlogRotateKey_FullMethodName,
			&pb.VlogRotateKeyRequest{Log: "audit", SigningKeyType: pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ED25519}, "RotateKey",
			func(s *qrdbServiceServer, ctx context.Context, r any) (any, error) {
				return s.VlogRotateKey(ctx, r.(*pb.VlogRotateKeyRequest))
			}},
	}
}

// vlogMethodNamed returns the vlogMethods row for name, carrying req.
func vlogMethodNamed(t *testing.T, name string, req any) vlogMethod {
	t.Helper()
	for _, m := range vlogMethods() {
		if m.name == name {
			m.req = req
			return m
		}
	}
	t.Fatalf("no Vlog* method %q", name)
	return vlogMethod{}
}

// vlogNeedsSignHead reports whether name is one of the methods vlog:sign_head
// gates, which only Operator holds.
func vlogNeedsSignHead(name string) bool {
	return name == "VlogSignHead" || name == "VlogRotateKey"
}

// vlogRoleGrants is the design's role table (docs/databases/architecture.md,
// "Verifiable log", Authorization): the Vlog* methods each built-in role is
// admitted to. Every method not listed is refused.
var vlogRoleGrants = map[string]map[string]bool{
	role.RoleNameAdmin: {
		"VlogCreate": true, "VlogList": true, "VlogAppend": true, "VlogGet": true,
		"VlogProveInclusion": true, "VlogProveConsistency": true, "VlogHead": true, "VlogExpireBodies": true,
	},
	role.RoleNameOperator: {
		"VlogList": true, "VlogAppend": true, "VlogGet": true,
		"VlogProveInclusion": true, "VlogProveConsistency": true, "VlogHead": true, "VlogSignHead": true,
		"VlogRotateKey": true,
	},
	role.RoleNameUser: {
		"VlogList": true, "VlogAppend": true, "VlogGet": true,
		"VlogProveInclusion": true, "VlogProveConsistency": true, "VlogHead": true,
	},
	role.RoleNameAuditor: {
		"VlogList": true, "VlogGet": true,
		"VlogProveInclusion": true, "VlogProveConsistency": true, "VlogHead": true,
	},
}

const vlogTestTenant = "acme"

// invokeVlog drives one Vlog* method through the real authorization
// interceptor and RBAC as a caller holding roleName in the test tenant.
func invokeVlog(t *testing.T, roles role.RoleServicer, roleName string, m vlogMethod, svc service.LogService) (any, error) {
	t.Helper()
	caller := &auth.Identity{
		Subject:  roleName + "-caller",
		TenantID: vlogTestTenant,
		Roles:    []string{role.SystemRoleID(vlogTestTenant, roleName)},
	}
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)))
	srv := &qrdbServiceServer{authMW: mw, logSvc: svc}
	info := &grpc.UnaryServerInfo{FullMethod: m.full}
	ctx := withDatabase(context.Background(), "ledger")
	return mw.UnaryInterceptor()(ctx, m.req, info, func(ctx context.Context, r any) (any, error) {
		return m.call(srv, ctx, r)
	})
}

// TestVlogMethods_AuthorizeEveryBuiltInRole drives each Vlog* method through
// the authorization interceptor as each built-in role: an admitted call
// reaches the LogService, a refused one is PermissionDenied and never does.
// Admin is refused VlogSignHead and VlogRotateKey, and Auditor VlogAppend.
func TestVlogMethods_AuthorizeEveryBuiltInRole(t *testing.T) {
	roles := authtest.BuiltInRoles(t, vlogTestTenant)
	for roleName, granted := range vlogRoleGrants {
		for _, m := range vlogMethods() {
			t.Run(roleName+"/"+m.name, func(t *testing.T) {
				svc := &recordingLogService{}
				_, err := invokeVlog(t, roles, roleName, m, svc)
				if !granted[m.name] {
					if status.Code(err) != codes.PermissionDenied {
						t.Fatalf("err = %v, want PermissionDenied", err)
					}
					if n := svc.callCount(); n != 0 {
						t.Fatalf("a refused call reached the LogService %d time(s)", n)
					}
					return
				}
				if err != nil {
					t.Fatalf("admitted call failed: %v", err)
				}
				if got := svc.lastCall(t).method; got != m.service {
					t.Fatalf("LogService method = %q, want %q", got, m.service)
				}
			})
		}
	}
}

// TestVlogMethods_ConfineToTheCallersTenant pins that no Vlog* request names
// a tenant: every call reaches the LogService with an empty TenantID, which
// the service confines to the caller's, the caller's tenant in the context,
// and the database from the request metadata.
func TestVlogMethods_ConfineToTheCallersTenant(t *testing.T) {
	roles := authtest.BuiltInRoles(t, vlogTestTenant)
	for _, m := range vlogMethods() {
		t.Run(m.name, func(t *testing.T) {
			roleName := role.RoleNameAdmin
			if vlogNeedsSignHead(m.name) {
				roleName = role.RoleNameOperator
			}
			svc := &recordingLogService{}
			if _, err := invokeVlog(t, roles, roleName, m, svc); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			call := svc.lastCall(t)
			if call.ref.TenantID != "" {
				t.Fatalf("TenantID = %q, want empty (the caller's)", call.ref.TenantID)
			}
			if call.ctxTenant != vlogTestTenant {
				t.Fatalf("context tenant = %q, want %q", call.ctxTenant, vlogTestTenant)
			}
			if call.ref.Database != "ledger" {
				t.Fatalf("Database = %q, want ledger", call.ref.Database)
			}
		})
	}
}

// TestVlogMethods_MapEveryClassifiedError pins the vector-style error
// mapping: each typed vlog refusal reaches the caller as its class's code.
func TestVlogMethods_MapEveryClassifiedError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"invalid log name", &vlog.InvalidLogNameError{Name: "x"}, codes.InvalidArgument},
		{"append batch", &vlog.AppendBatchError{}, codes.InvalidArgument},
		{"unsupported key type", &vlog.UnsupportedKeyTypeError{Type: "rsa"}, codes.InvalidArgument},
		{"log exists", &vlog.LogExistsError{}, codes.AlreadyExists},
		{"log not found", &vlog.LogNotFoundError{}, codes.NotFound},
		{"key not found", &vlog.KeyNotFoundError{}, codes.NotFound},
		{"size out of range", &vlog.SizeOutOfRangeError{}, codes.OutOfRange},
		{"index out of range", &vlog.IndexOutOfRangeError{}, codes.OutOfRange},
		{"expire not allowed", &vlog.ExpireNotAllowedError{}, codes.FailedPrecondition},
		{"key version conflict", &vlog.KeyVersionConflictError{}, codes.Aborted},
		{"not compiled", &vlog.NotCompiledError{}, codes.Unimplemented},
		{"feature not active", &service.LogFeatureNotActiveError{TenantID: "acme"}, codes.Unavailable},
	}
	m := vlogMethods()
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			method := m[i%len(m)]
			srv := &qrdbServiceServer{logSvc: &recordingLogService{err: c.err}}
			_, err := method.call(srv, context.Background(), method.req)
			if got := status.Code(err); got != c.want {
				t.Fatalf("%s: code = %v, want %v (err %v)", method.name, got, c.want, err)
			}
		})
	}
}

// TestVlogMethods_RefuseWithoutAService pins that a node serving no
// verifiable logs answers every Vlog* method with ServiceNotConfiguredError's
// code rather than panicking.
func TestVlogMethods_RefuseWithoutAService(t *testing.T) {
	want := ServiceErrorCode(&ServiceNotConfiguredError{Service: "vlog"})
	if want == codes.OK {
		t.Fatal("ServiceNotConfiguredError maps to OK")
	}
	for _, m := range vlogMethods() {
		t.Run(m.name, func(t *testing.T) {
			_, err := m.call(&qrdbServiceServer{}, context.Background(), m.req)
			if got := status.Code(err); got != want {
				t.Fatalf("code = %v, want %v", got, want)
			}
		})
	}
}

// TestWithLogService_ReachesTheService checks the ServerOption hands the
// LogService to the QRDBService the server registers.
func TestWithLogService_ReachesTheService(t *testing.T) {
	svc := &recordingLogService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithLogService(svc))
	if srv.logSvc != svc {
		t.Fatal("expected logSvc to be set")
	}
	if NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithLogService(nil)).logSvc != nil {
		t.Fatal("expected logSvc to be nil")
	}
}

// TestWithRegisterLogService_SetsTheService checks the RegisterOption the
// Unix socket uses sets the LogService, and that nil leaves it unset.
func TestWithRegisterLogService_SetsTheService(t *testing.T) {
	svc := &recordingLogService{}
	var s qrdbServiceServer
	WithRegisterLogService(svc)(&s)
	if s.logSvc != svc {
		t.Fatal("expected logSvc to be set")
	}
	WithRegisterLogService(nil)(&s)
	if s.logSvc != nil {
		t.Fatal("expected logSvc to be nil")
	}
}

// TestVlogCreate_CarriesTheSigningKeyType checks the wire signature type
// reaches the service as the vlog one and the created key comes back.
func TestVlogCreate_CarriesTheSigningKeyType(t *testing.T) {
	svc := &recordingLogService{}
	srv := &qrdbServiceServer{logSvc: svc}
	resp, err := srv.VlogCreate(context.Background(), &pb.VlogCreateRequest{
		Log:               "audit",
		Origin:            "db.example.com/audit",
		VerifierKeys:      []string{"vk"},
		AllowExpireBodies: true,
		SigningKeyType:    pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ECDSA_P256_SHA256,
	})
	if err != nil {
		t.Fatalf("VlogCreate: %v", err)
	}
	if svc.create.SigningKeyType != vlog.SignatureECDSAP256SHA256 || !svc.create.AllowExpireBodies ||
		svc.create.Origin != "db.example.com/audit" || len(svc.create.VerifierKeys) != 1 {
		t.Fatalf("request = %+v", svc.create)
	}
	if resp.GetKey().GetVersion() != 1 || resp.GetKey().GetRef() != "k/1" || resp.GetLog().GetLog() != "audit" {
		t.Fatalf("response = %+v", resp)
	}
}

// TestVlogCreate_UnspecifiedKeyTypeCreatesNoKey checks UNSPECIFIED is the
// empty type, and the response then carries no key.
func TestVlogCreate_UnspecifiedKeyTypeCreatesNoKey(t *testing.T) {
	svc := &recordingLogService{}
	resp, err := (&qrdbServiceServer{logSvc: svc}).VlogCreate(context.Background(), &pb.VlogCreateRequest{Log: "audit"})
	if err != nil {
		t.Fatalf("VlogCreate: %v", err)
	}
	if svc.create.SigningKeyType != "" || resp.GetKey() != nil {
		t.Fatalf("key type %q, key %+v: want none", svc.create.SigningKeyType, resp.GetKey())
	}
}

// TestVlogCreate_RefusesAnUnknownKeyType checks an enum value outside the
// RFC 9162 types is InvalidArgument before the service is reached.
func TestVlogCreate_RefusesAnUnknownKeyType(t *testing.T) {
	svc := &recordingLogService{}
	_, err := (&qrdbServiceServer{logSvc: svc}).VlogCreate(context.Background(), &pb.VlogCreateRequest{
		Log: "audit", SigningKeyType: pb.VlogSignatureType(99),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
	if svc.callCount() != 0 {
		t.Fatal("an unknown key type reached the LogService")
	}
}

// TestVlogSignHead_CarriesTheSignatureType checks a signed head carries its
// signature type, key version and checkpoint for both RFC 9162 types.
func TestVlogSignHead_CarriesTheSignatureType(t *testing.T) {
	for typ, want := range vlogSignatureTypeToProto {
		t.Run(string(typ), func(t *testing.T) {
			srv := &qrdbServiceServer{logSvc: &recordingLogService{sigType: typ}}
			resp, err := srv.VlogSignHead(context.Background(), &pb.VlogSignHeadRequest{Log: "audit"})
			if err != nil {
				t.Fatalf("VlogSignHead: %v", err)
			}
			h := resp.GetHead()
			if h.GetSignatureType() != want || h.GetKeyVersion() != 2 || h.GetSize() != 9 ||
				!bytes.Equal(h.GetSignature(), []byte("sig")) || string(resp.GetCheckpoint()) != "checkpoint" {
				t.Fatalf("response = %+v", resp)
			}
		})
	}
}

// TestVlogSignHead_RefusesAnUnknownSignatureType checks a head the service
// signed with a type the wire cannot name is refused, never sent unnamed.
func TestVlogSignHead_RefusesAnUnknownSignatureType(t *testing.T) {
	srv := &qrdbServiceServer{logSvc: &recordingLogService{sigType: "rsa"}}
	if _, err := srv.VlogSignHead(context.Background(), &pb.VlogSignHeadRequest{Log: "audit"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// TestVlogProofs_CarryTheHashesIndexAndSizes checks a proof response carries
// what a client needs to verify it without trusting the server.
func TestVlogProofs_CarryTheHashesIndexAndSizes(t *testing.T) {
	srv := &qrdbServiceServer{logSvc: &recordingLogService{}}
	inc, err := srv.VlogProveInclusion(context.Background(), &pb.VlogProveInclusionRequest{Log: "audit", LeafIndex: 1, TreeSize: 4})
	if err != nil {
		t.Fatalf("VlogProveInclusion: %v", err)
	}
	if inc.GetLeafIndex() != 1 || inc.GetTreeSize() != 4 || len(inc.GetHashes()) != 2 {
		t.Fatalf("inclusion = %+v", inc)
	}
	con, err := srv.VlogProveConsistency(context.Background(), &pb.VlogProveConsistencyRequest{Log: "audit", Size1: 2, Size2: 4})
	if err != nil {
		t.Fatalf("VlogProveConsistency: %v", err)
	}
	if con.GetSize1() != 2 || con.GetSize2() != 4 || len(con.GetHashes()) != 1 {
		t.Fatalf("consistency = %+v", con)
	}
}

// TestVlogReads_ConvertEveryField checks the head, entries, list, append and
// expire responses carry every field the service returned.
func TestVlogReads_ConvertEveryField(t *testing.T) {
	srv := &qrdbServiceServer{logSvc: &recordingLogService{}}
	ctx := withDatabase(context.Background(), "ledger")

	head, err := srv.VlogHead(ctx, &pb.VlogHeadRequest{Log: "audit"})
	if err != nil || head.GetSize() != 9 || head.GetBodiesFrom() != 2 || head.GetCheckpointSize() != 8 || string(head.GetCheckpoint()) != "cp" {
		t.Fatalf("head = %+v, err %v", head, err)
	}
	got, err := srv.VlogGet(ctx, &pb.VlogGetRequest{Log: "audit", From: 3, Limit: 2})
	if err != nil || len(got.GetEntries()) != 2 || got.GetEntries()[0].GetIndex() != 3 || !got.GetEntries()[1].GetExpired() {
		t.Fatalf("entries = %+v, err %v", got, err)
	}
	list, err := srv.VlogList(ctx, &pb.VlogListRequest{})
	if err != nil || len(list.GetLogs()) != 1 || list.GetLogs()[0].GetDatabase() != "ledger" || list.GetLogs()[0].GetTenantId() != "acme" {
		t.Fatalf("list = %+v, err %v", list, err)
	}
	app, err := srv.VlogAppend(ctx, &pb.VlogAppendRequest{Log: "audit", Entries: [][]byte{{1}, {2}}})
	if err != nil || app.GetFirstIndex() != 7 || app.GetCount() != 2 {
		t.Fatalf("append = %+v, err %v", app, err)
	}
	exp, err := srv.VlogExpireBodies(ctx, &pb.VlogExpireBodiesRequest{Log: "audit", Cutoff: 5})
	if err != nil || exp.GetExpired() != 5 {
		t.Fatalf("expire = %+v, err %v", exp, err)
	}
}

// TestVlogRotateKey_CarriesTheKeyTypeAndNewKey checks the wire key type
// reaches the service as the vlog one and the appended key comes back.
func TestVlogRotateKey_CarriesTheKeyTypeAndNewKey(t *testing.T) {
	svc := &recordingLogService{}
	resp, err := (&qrdbServiceServer{logSvc: svc}).VlogRotateKey(context.Background(), &pb.VlogRotateKeyRequest{
		Log: "audit", SigningKeyType: pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ECDSA_P256_SHA256,
	})
	if err != nil {
		t.Fatalf("VlogRotateKey: %v", err)
	}
	if got := svc.lastCall(t); got.method != "RotateKey" || got.ref.Log != "audit" {
		t.Fatalf("call = %+v", got)
	}
	if k := resp.GetKey(); k.GetVersion() != 2 || k.GetRef() != "k/2" || string(k.GetPublicKey()) != "pub2" {
		t.Fatalf("key = %+v", k)
	}
}

// TestVlogRotateKey_RefusesAnUnnamedKeyType checks UNSPECIFIED and a value
// outside the RFC 9162 types are InvalidArgument before the service is reached:
// a rotation always names the new key's type.
func TestVlogRotateKey_RefusesAnUnnamedKeyType(t *testing.T) {
	for _, typ := range []pb.VlogSignatureType{pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_UNSPECIFIED, pb.VlogSignatureType(99)} {
		t.Run(typ.String(), func(t *testing.T) {
			svc := &recordingLogService{}
			_, err := (&qrdbServiceServer{logSvc: svc}).VlogRotateKey(context.Background(), &pb.VlogRotateKeyRequest{Log: "audit", SigningKeyType: typ})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
			if svc.callCount() != 0 {
				t.Fatal("an unnamed key type reached the LogService")
			}
		})
	}
}
