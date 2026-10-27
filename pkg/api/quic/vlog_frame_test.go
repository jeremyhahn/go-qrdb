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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// fakeLogService is a service.LogService that confines the request's tenant
// exactly as the real service does (tenant.Confine against the caller in
// ctx) before answering, records the LogRef it was asked about, and fails
// every call with err when err is set.
type fakeLogService struct {
	err     error
	ref     service.LogRef
	keyType vlog.SignatureType
}

var _ service.LogService = (*fakeLogService)(nil)

func (f *fakeLogService) enter(ctx context.Context, ref service.LogRef, op string) error {
	if _, err := tenant.Confine(ctx, ref.TenantID, op); err != nil {
		return err
	}
	f.ref = ref
	return f.err
}

func (f *fakeLogService) CreateLog(ctx context.Context, req *service.CreateLogRequest) (*service.CreateLogResponse, error) {
	if err := f.enter(ctx, req.LogRef, "create verifiable log"); err != nil {
		return nil, err
	}
	return &service.CreateLogResponse{
		ID:  vlog.LogID{TenantID: "acme", Database: req.Database, Log: req.Log},
		Key: &vlog.HeadKey{Version: 1, Ref: "software/v1", PublicKey: []byte("pk")},
	}, nil
}

func (f *fakeLogService) ListLogs(ctx context.Context, req *service.ListLogsRequest) (*service.ListLogsResponse, error) {
	if err := f.enter(ctx, service.LogRef{TenantID: req.TenantID, Database: req.Database}, "list verifiable logs"); err != nil {
		return nil, err
	}
	return &service.ListLogsResponse{Logs: []vlog.LogID{{TenantID: "acme", Database: req.Database, Log: "a"}, {TenantID: "acme", Database: req.Database, Log: "b"}}}, nil
}

func (f *fakeLogService) Append(ctx context.Context, req *service.AppendRequest) (*service.AppendResponse, error) {
	if err := f.enter(ctx, req.LogRef, "append to verifiable log"); err != nil {
		return nil, err
	}
	return &service.AppendResponse{FirstIndex: 7, Count: len(req.Entries)}, nil
}

func (f *fakeLogService) GetEntries(ctx context.Context, req *service.GetEntriesRequest) (*service.GetEntriesResponse, error) {
	if err := f.enter(ctx, req.LogRef, "read verifiable log"); err != nil {
		return nil, err
	}
	return &service.GetEntriesResponse{Entries: []vlog.Entry{{Index: req.From, Body: []byte("e0")}, {Index: req.From + 1, Expired: true}}}, nil
}

func (f *fakeLogService) ProveInclusion(ctx context.Context, req *service.InclusionRequest) (*service.InclusionResponse, error) {
	if err := f.enter(ctx, req.LogRef, "prove inclusion"); err != nil {
		return nil, err
	}
	return &service.InclusionResponse{Index: req.Index, Size: req.Size, Hashes: [][]byte{[]byte("h1"), []byte("h2")}}, nil
}

func (f *fakeLogService) ProveConsistency(ctx context.Context, req *service.ConsistencyRequest) (*service.ConsistencyResponse, error) {
	if err := f.enter(ctx, req.LogRef, "prove consistency"); err != nil {
		return nil, err
	}
	return &service.ConsistencyResponse{Size1: req.Size1, Size2: req.Size2, Hashes: [][]byte{[]byte("c1")}}, nil
}

func (f *fakeLogService) Head(ctx context.Context, req *service.HeadRequest) (*service.HeadResponse, error) {
	if err := f.enter(ctx, req.LogRef, "read verifiable log head"); err != nil {
		return nil, err
	}
	return &service.HeadResponse{Head: vlog.Head{Size: 9, Root: []byte("root"), BodiesFrom: 2, Checkpoint: []byte("cp"), CheckpointSize: 8}}, nil
}

func (f *fakeLogService) SignHead(ctx context.Context, req *service.SignHeadRequest) (*service.SignHeadResponse, error) {
	if err := f.enter(ctx, req.LogRef, "sign verifiable log head"); err != nil {
		return nil, err
	}
	return &service.SignHeadResponse{
		Head:       vlog.SignedHead{Size: 9, Root: []byte("root"), KeyVersion: 1, SignatureType: "ed25519", Signature: []byte("sig")},
		Checkpoint: []byte("note"),
	}, nil
}

func (f *fakeLogService) RotateKey(ctx context.Context, req *service.RotateKeyRequest) (*service.RotateKeyResponse, error) {
	if err := f.enter(ctx, req.LogRef, "rotate verifiable log key"); err != nil {
		return nil, err
	}
	f.keyType = req.SigningKeyType
	return &service.RotateKeyResponse{Key: vlog.HeadKey{Version: 2, Ref: "software/v2", PublicKey: []byte("pk2")}}, nil
}

func (f *fakeLogService) ExpireBodies(ctx context.Context, req *service.ExpireBodiesRequest) (*service.ExpireBodiesResponse, error) {
	if err := f.enter(ctx, req.LogRef, "expire verifiable log bodies"); err != nil {
		return nil, err
	}
	return &service.ExpireBodiesResponse{Expired: req.Cutoff}, nil
}

// vlogCtx is a call from subject in tenant acme, database ledger.
func vlogCtx() context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	return tenant.WithDatabase(ctx, "ledger")
}

func vlogJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// vlogCall runs method's handler as dispatch would, after the method gate.
func vlogCall(t *testing.T, srv *Server, ctx context.Context, method string, payload []byte) ([]byte, error) {
	t.Helper()
	h, ok := srv.handlers[method]
	if !ok {
		t.Fatalf("%s is not registered", method)
	}
	return h(ctx, payload)
}

// vlogMethods is the design's matrix (docs/databases/architecture.md,
// "Transports"): each QUIC method and the permission it is gated on.
var vlogMethods = []struct {
	method string
	perm   string
}{
	{"vlog.Create", "vlog:create"},
	{"vlog.List", "vlog:list"},
	{"vlog.Append", "vlog:append"},
	{"vlog.Get", "vlog:get"},
	{"vlog.ProveInclusion", "vlog:proof"},
	{"vlog.ProveConsistency", "vlog:proof"},
	{"vlog.Head", "vlog:head"},
	{"vlog.SignHead", "vlog:sign_head"},
	{"vlog.RotateKey", "vlog:sign_head"},
	{"vlog.ExpireBodies", "vlog:expire"},
}

func TestVlogMethods_GatedOnTheMatrixPermission(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLogService(&fakeLogService{}))
	for _, m := range vlogMethods {
		got, ok := srv.methodPerms.Lookup(m.method)
		if !ok || string(got) != m.perm {
			t.Errorf("%s gated on %q (registered %v), want %q", m.method, got, ok, m.perm)
		}
		if _, ok := srv.handlers[m.method]; !ok {
			t.Errorf("%s has no handler with a log service configured", m.method)
		}
	}
}

func TestVlogMethods_UnservedWithoutALogService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	for _, m := range vlogMethods {
		if _, ok := srv.handlers[m.method]; ok {
			t.Errorf("%s is served with no log service", m.method)
		}
		if !srv.methodPerms.Covered(m.method) {
			t.Errorf("%s lost its permission row with no log service", m.method)
		}
	}
	if _, err := srv.handleVlogHead(vlogCtx(), []byte(`{"log":"l"}`)); !errors.As(err, new(*ServiceNotConfiguredError)) {
		t.Fatalf("handler with no log service = %v, want *ServiceNotConfiguredError", err)
	}
}

// TestVlogMethods_RoleMatrix runs every method's gate under real RBAC for
// each built-in role. The verdicts are the design's grants: Admin is never
// granted sign_head, Operator never create or expire, User only the data
// path, Auditor only reads.
func TestVlogMethods_RoleMatrix(t *testing.T) {
	admit := map[string]map[string]bool{
		role.RoleNameAdmin:    {"vlog.Create": true, "vlog.List": true, "vlog.Append": true, "vlog.Get": true, "vlog.ProveInclusion": true, "vlog.ProveConsistency": true, "vlog.Head": true, "vlog.ExpireBodies": true},
		role.RoleNameOperator: {"vlog.List": true, "vlog.Append": true, "vlog.Get": true, "vlog.ProveInclusion": true, "vlog.ProveConsistency": true, "vlog.Head": true, "vlog.SignHead": true, "vlog.RotateKey": true},
		role.RoleNameUser:     {"vlog.List": true, "vlog.Append": true, "vlog.Get": true, "vlog.ProveInclusion": true, "vlog.ProveConsistency": true, "vlog.Head": true},
		role.RoleNameAuditor:  {"vlog.List": true, "vlog.Get": true, "vlog.ProveInclusion": true, "vlog.ProveConsistency": true, "vlog.Head": true},
	}
	roles := authtest.BuiltInRoles(t, "acme")
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLogService(&fakeLogService{}),
		WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))))
	for name, allowed := range admit {
		id := &auth.Identity{Subject: "s", TenantID: "acme", Roles: []string{role.SystemRoleID("acme", name)}}
		ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), "acme")
		for _, m := range vlogMethods {
			err := srv.authorize(ctx, m.method)
			switch {
			case allowed[m.method] && err != nil:
				t.Errorf("%s refused %s: %v", name, m.method, err)
			case !allowed[m.method] && authmw.ClassifyAuthError(err) != authmw.AuthFailureUnauthorized:
				t.Errorf("%s calling %s = %v, want an authorization refusal", name, m.method, err)
			}
		}
	}
}

func TestVlogHandlers_CarryTheServiceResults(t *testing.T) {
	svc := &fakeLogService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLogService(svc))
	ctx := vlogCtx()

	t.Run("create", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.Create", []byte(`{"log":"l","signing_key_type":"ed25519"}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.CreateResult
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if res.Log != "l" || res.Database != "ledger" || res.Key == nil || res.Key.Version != 1 || !bytes.Equal(res.Key.PublicKey, []byte("pk")) {
			t.Fatalf("create result = %+v", res)
		}
		if bytes.Contains(out, []byte("software/v1")) {
			t.Fatalf("create result carries the provider key reference: %s", out)
		}
	})
	t.Run("list", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.List", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.ListResult
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Logs) != 2 || res.Logs[0] != "a" || res.Logs[1] != "b" || svc.ref.Database != "ledger" {
			t.Fatalf("list result = %+v, database %q", res, svc.ref.Database)
		}
	})
	t.Run("append", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.Append", vlogJSON(t, vlogwire.AppendRequest{Log: "l", Entries: [][]byte{{0, 1}, {2}}}))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.AppendResult
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if res.FirstIndex != 7 || res.Count != 2 {
			t.Fatalf("append result = %+v", res)
		}
	})
	t.Run("get", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.Get", []byte(`{"log":"l","from":3,"limit":2}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.GetResult
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 2 || res.Entries[0].Index != 3 || !bytes.Equal(res.Entries[0].Body, []byte("e0")) || !res.Entries[1].Expired {
			t.Fatalf("get result = %+v", res)
		}
	})
	t.Run("inclusion proof carries index, size and hashes", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.ProveInclusion", []byte(`{"log":"l","index":4,"size":9}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.InclusionProof
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if res.Index != 4 || res.Size != 9 || len(res.Hashes) != 2 || !bytes.Equal(res.Hashes[1], []byte("h2")) {
			t.Fatalf("inclusion result = %+v", res)
		}
	})
	t.Run("consistency proof carries both sizes and hashes", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.ProveConsistency", []byte(`{"log":"l","size1":3,"size2":9}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.ConsistencyProof
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if res.Size1 != 3 || res.Size2 != 9 || len(res.Hashes) != 1 || !bytes.Equal(res.Hashes[0], []byte("c1")) {
			t.Fatalf("consistency result = %+v", res)
		}
	})
	t.Run("head", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.Head", []byte(`{"log":"l"}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.Head
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if res.Size != 9 || !bytes.Equal(res.Root, []byte("root")) || res.BodiesFrom != 2 || res.CheckpointSize != 8 || !bytes.Equal(res.Checkpoint, []byte("cp")) {
			t.Fatalf("head result = %+v", res)
		}
	})
	t.Run("signed head carries its signature type", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.SignHead", []byte(`{"log":"l"}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.SignHeadResult
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if res.Head.SignatureType != "ed25519" || res.Head.KeyVersion != 1 || !bytes.Equal(res.Head.Signature, []byte("sig")) || !bytes.Equal(res.Checkpoint, []byte("note")) {
			t.Fatalf("sign head result = %+v", res)
		}
	})
	t.Run("rotate key carries the key type and the new key", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.RotateKey", []byte(`{"log":"l","signing_key_type":"ecdsa-p256-sha256"}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.RotateKeyResult
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if res.Key.Version != 2 || !bytes.Equal(res.Key.PublicKey, []byte("pk2")) || bytes.Contains(out, []byte("software/v2")) {
			t.Fatalf("rotate key result = %s", out)
		}
		if svc.keyType != vlog.SignatureECDSAP256SHA256 || svc.ref.Log != "l" || svc.ref.Database != "ledger" {
			t.Fatalf("rotate key request: type %q, ref %+v", svc.keyType, svc.ref)
		}
	})
	t.Run("expire", func(t *testing.T) {
		out, err := vlogCall(t, srv, ctx, "vlog.ExpireBodies", []byte(`{"log":"l","cutoff":5}`))
		if err != nil {
			t.Fatal(err)
		}
		var res vlogwire.ExpireResult
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatal(err)
		}
		if res.Expired != 5 || svc.ref.Log != "l" || svc.ref.Database != "ledger" {
			t.Fatalf("expire result = %+v, ref %+v", res, svc.ref)
		}
	})
}

func TestVlogHandlers_MalformedPayloadIsInvalidArgument(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLogService(&fakeLogService{}))
	for _, m := range vlogMethods {
		_, err := vlogCall(t, srv, vlogCtx(), m.method, []byte(`{"log":`))
		if !errors.As(err, new(*UnmarshalError)) {
			t.Errorf("%s with a malformed payload = %v, want *UnmarshalError", m.method, err)
		}
		if code, _ := mapServiceError(err); code != wire.StatusInvalidArgument {
			t.Errorf("%s malformed payload status = %d, want %d", m.method, code, wire.StatusInvalidArgument)
		}
	}
}

// TestVlogHandlers_TenantConfined names another tenant on every method: the
// request's tenant reaches the service unchanged and is refused there.
func TestVlogHandlers_NameNoTenant(t *testing.T) {
	for _, m := range vlogMethods {
		svc := &fakeLogService{}
		srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLogService(svc))
		if _, err := vlogCall(t, srv, vlogCtx(), m.method, []byte(`{"tenant_id":"globex","log":"l"}`)); err != nil {
			t.Fatalf("%s with a stray tenant_id = %v, want it ignored", m.method, err)
		}
		if svc.ref.TenantID != "" || svc.ref.Database != "ledger" {
			t.Errorf("%s service saw tenant %q database %q, want the caller's tenant and database ledger", m.method, svc.ref.TenantID, svc.ref.Database)
		}
	}
}

// TestVlogHandlers_ServiceErrorStatus maps each verifiable-log condition to
// its wire status, bare and inside the service's read wrapper.
func TestVlogHandlers_ServiceErrorStatus(t *testing.T) {
	id := vlog.LogID{TenantID: "acme", Database: "ledger", Log: "l"}
	cases := []struct {
		err  error
		want uint16
	}{
		{&service.LogFeatureNotActiveError{TenantID: "acme"}, wire.StatusUnavailable},
		{&vlog.InvalidLogNameError{Field: "log", Name: "a/b"}, wire.StatusInvalidArgument},
		{&vlog.InvalidTenantIDError{TenantID: "a/b"}, wire.StatusInvalidArgument},
		{&vlog.InvalidDescriptorError{Reason: "origin"}, wire.StatusInvalidArgument},
		{&vlog.AppendBatchError{Count: 0}, wire.StatusInvalidArgument},
		{&vlog.UnsupportedKeyTypeError{Type: "rsa"}, wire.StatusInvalidArgument},
		{&vlog.InvalidSigningKeyError{Reason: "short"}, wire.StatusFailedPrecondition},
		{&vlog.LogExistsError{ID: id}, wire.StatusAlreadyExists},
		{&vlog.CheckpointExistsError{Size: 3}, wire.StatusAlreadyExists},
		{&vlog.KeyVersionConflictError{ID: id, Version: 2}, wire.StatusAborted},
		{&vlog.LogNotFoundError{ID: id}, wire.StatusNotFound},
		{&vlog.KeyNotFoundError{ID: id, Version: 2}, wire.StatusNotFound},
		{&vlog.SizeOutOfRangeError{Size: 10, Limit: 9}, wire.StatusOutOfRange},
		{&vlog.IndexOutOfRangeError{Index: 9, Size: 9}, wire.StatusOutOfRange},
		{&vlog.CheckpointRootMismatchError{Size: 3}, wire.StatusFailedPrecondition},
		{&vlog.EmptyCheckpointError{Size: 3}, wire.StatusInvalidArgument},
		{&vlog.ExpireNotAllowedError{ID: id}, wire.StatusFailedPrecondition},
		{&vlog.CorruptTableError{Key: "k", Reason: "short"}, wire.StatusDataLoss},
		{&vlog.HeadSignatureError{Reason: "bad"}, wire.StatusError},
		{&vlog.NotCompiledError{}, wire.StatusUnimplemented},
		{&service.ReadError{Err: &vlog.IndexOutOfRangeError{Index: 9, Size: 9}}, wire.StatusOutOfRange},
		{&service.ReadError{Err: &vlog.LogNotFoundError{ID: id}}, wire.StatusNotFound},
	}
	for _, c := range cases {
		srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithLogService(&fakeLogService{err: c.err}))
		_, err := vlogCall(t, srv, vlogCtx(), "vlog.Get", []byte(`{"log":"l"}`))
		if !errors.Is(err, c.err) {
			t.Errorf("handler returned %v, want the service's %v unchanged", err, c.err)
		}
		if code, _ := mapServiceError(err); code != c.want {
			t.Errorf("%T (%v) status = %d, want %d", c.err, c.err, code, c.want)
		}
	}
}
