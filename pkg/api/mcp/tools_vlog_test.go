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

// The qrdb_vlog_* tools, driven through a started MCP server over streamable
// HTTP and TLS: every call passes the tool-authorization middleware, a REAL
// rbac.RBACAuthorizer over the REAL built-in roles. The doubles are the
// authenticator and the log service, which confines the tenant exactly as the
// real one does.
//
// No build constraint: testTLSConfig (server_test.go), callCtx, wantOK and
// wantToolCode (tenant_isolation_test.go) are unconstrained.

package mcp

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
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

// fakeVlogService is a service.LogService that confines the request's tenant
// exactly as the real service does (tenant.Confine against the caller in
// ctx) before answering, records the LogRef and entries it was given, and
// fails every call with err when err is set.
type fakeVlogService struct {
	err     error
	mu      sync.Mutex
	ref     service.LogRef
	entries [][]byte
	calls   int
	keyType vlog.SignatureType
}

var _ service.LogService = (*fakeVlogService)(nil)

func (f *fakeVlogService) enter(ctx context.Context, ref service.LogRef, op string) error {
	if _, err := tenant.Confine(ctx, ref.TenantID, op); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ref = ref
	f.calls++
	return f.err
}

func (f *fakeVlogService) seen() (service.LogRef, [][]byte, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ref, f.entries, f.calls
}

func (f *fakeVlogService) CreateLog(ctx context.Context, req *service.CreateLogRequest) (*service.CreateLogResponse, error) {
	if err := f.enter(ctx, req.LogRef, "create verifiable log"); err != nil {
		return nil, err
	}
	return &service.CreateLogResponse{
		ID:  vlog.LogID{TenantID: "acme", Database: req.Database, Log: req.Log},
		Key: &vlog.HeadKey{Version: 1, Ref: "software/v1", PublicKey: []byte("pk")},
	}, nil
}

func (f *fakeVlogService) ListLogs(ctx context.Context, req *service.ListLogsRequest) (*service.ListLogsResponse, error) {
	if err := f.enter(ctx, service.LogRef{TenantID: req.TenantID, Database: req.Database}, "list verifiable logs"); err != nil {
		return nil, err
	}
	return &service.ListLogsResponse{Logs: []vlog.LogID{{Log: "a"}, {Log: "b"}}}, nil
}

func (f *fakeVlogService) Append(ctx context.Context, req *service.AppendRequest) (*service.AppendResponse, error) {
	if err := f.enter(ctx, req.LogRef, "append to verifiable log"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.entries = req.Entries
	f.mu.Unlock()
	return &service.AppendResponse{FirstIndex: 7, Count: len(req.Entries)}, nil
}

func (f *fakeVlogService) GetEntries(ctx context.Context, req *service.GetEntriesRequest) (*service.GetEntriesResponse, error) {
	if err := f.enter(ctx, req.LogRef, "read verifiable log"); err != nil {
		return nil, err
	}
	return &service.GetEntriesResponse{Entries: []vlog.Entry{{Index: req.From, Body: []byte("e0")}, {Index: req.From + 1, Expired: true}}}, nil
}

func (f *fakeVlogService) ProveInclusion(ctx context.Context, req *service.InclusionRequest) (*service.InclusionResponse, error) {
	if err := f.enter(ctx, req.LogRef, "prove inclusion"); err != nil {
		return nil, err
	}
	return &service.InclusionResponse{Index: req.Index, Size: req.Size, Hashes: [][]byte{[]byte("h1")}}, nil
}

func (f *fakeVlogService) ProveConsistency(ctx context.Context, req *service.ConsistencyRequest) (*service.ConsistencyResponse, error) {
	if err := f.enter(ctx, req.LogRef, "prove consistency"); err != nil {
		return nil, err
	}
	return &service.ConsistencyResponse{Size1: req.Size1, Size2: req.Size2, Hashes: [][]byte{[]byte("c1")}}, nil
}

func (f *fakeVlogService) Head(ctx context.Context, req *service.HeadRequest) (*service.HeadResponse, error) {
	if err := f.enter(ctx, req.LogRef, "read verifiable log head"); err != nil {
		return nil, err
	}
	return &service.HeadResponse{Head: vlog.Head{Size: 9, Root: []byte("root"), CheckpointSize: 8}}, nil
}

func (f *fakeVlogService) SignHead(ctx context.Context, req *service.SignHeadRequest) (*service.SignHeadResponse, error) {
	if err := f.enter(ctx, req.LogRef, "sign verifiable log head"); err != nil {
		return nil, err
	}
	return &service.SignHeadResponse{
		Head:       vlog.SignedHead{Size: 9, Root: []byte("root"), KeyVersion: 1, SignatureType: vlog.SignatureEd25519, Signature: []byte("sig")},
		Checkpoint: []byte("note"),
	}, nil
}

func (f *fakeVlogService) RotateKey(ctx context.Context, req *service.RotateKeyRequest) (*service.RotateKeyResponse, error) {
	if err := f.enter(ctx, req.LogRef, "rotate verifiable log key"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.keyType = req.SigningKeyType
	f.mu.Unlock()
	return &service.RotateKeyResponse{Key: vlog.HeadKey{Version: 2, Ref: "software/v2", PublicKey: []byte("pk2")}}, nil
}

func (f *fakeVlogService) ExpireBodies(ctx context.Context, req *service.ExpireBodiesRequest) (*service.ExpireBodiesResponse, error) {
	if err := f.enter(ctx, req.LogRef, "expire verifiable log bodies"); err != nil {
		return nil, err
	}
	return &service.ExpireBodiesResponse{Expired: req.Cutoff}, nil
}

// vlogFixture is one started MCP server serving the log tools over svc, with
// an acme Admin as the caller until as() rebinds it.
type vlogFixture struct {
	client *mcpclient.Client
	caller *authtest.FixedAuthenticator
	svc    *fakeVlogService
}

func newVlogFixture(t *testing.T) *vlogFixture {
	t.Helper()
	svc := &fakeVlogService{}
	f := newVlogFixtureOver(t, svc)
	f.svc = svc
	return f
}

// newVlogFixtureOver starts the MCP server over svc; its svc field is unset.
func newVlogFixtureOver(t *testing.T, svc service.LogService) *vlogFixture {
	t.Helper()
	tenants := authtest.NewTenants(t, "acme", "globex")
	caller := authtest.NewFixedAuthenticator(authtest.AdminIdentity("acme", "acme-admin"))
	roles := authtest.BuiltInRoles(t, "acme", "globex")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(caller))

	tlsCfg := testTLSConfig(t)
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithTLS(tlsCfg), WithTenantService(tenants), WithLogService(svc), WithAuthMiddleware(mw))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}}
	client, err := mcpclient.NewStreamableHttpClient("https://"+srv.Addr()+"/mcp",
		transport.WithHTTPBasicClient(httpClient))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := callCtx(t)
	if err := client.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	init := mcpgo.InitializeRequest{}
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "vlog-tools-test", Version: "1"}
	if _, err := client.Initialize(ctx, init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return &vlogFixture{client: client, caller: caller}
}

func (f *vlogFixture) as(id *auth.Identity) { f.caller.Set(id) }

// call invokes one tool; a protocol-level failure is fatal, because every
// refusal must arrive as a coded tool result.
func (f *vlogFixture) call(t *testing.T, tool string, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := f.client.CallTool(callCtx(t), req)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

// vlogToolArgs is a well-formed argument set for every tool.
var vlogToolArgs = map[string]map[string]any{
	"qrdb_vlog_create":            {"log": "l", "origin": "db.example.com/l", "signing_key_type": "ed25519"},
	"qrdb_vlog_list":              {},
	"qrdb_vlog_append":            {"log": "l", "entries": []any{"aGk="}},
	"qrdb_vlog_get":               {"log": "l", "from": 3},
	"qrdb_vlog_prove_inclusion":   {"log": "l", "index": 1, "size": 4},
	"qrdb_vlog_prove_consistency": {"log": "l", "size1": 2, "size2": 4},
	"qrdb_vlog_head":              {"log": "l"},
	"qrdb_vlog_sign_head":         {"log": "l"},
	"qrdb_vlog_rotate_key":        {"log": "l", "signing_key_type": "ecdsa-p256-sha256"},
	"qrdb_vlog_expire_bodies":     {"log": "l", "cutoff": 2},
}

func roleIdentity(tenantID, roleName string) *auth.Identity {
	return &auth.Identity{Subject: tenantID + "-" + roleName, TenantID: tenantID, Roles: []string{role.SystemRoleID(tenantID, roleName)}}
}

// TestVlogTools_RoleMatrix: each built-in role is served exactly the tools
// the verifiable-log matrix grants it and refused every other one with
// PermissionDenied before the service is reached. An Auditor may not append
// and an Admin may not sign a head.
func TestVlogTools_RoleMatrix(t *testing.T) {
	f := newVlogFixture(t)
	reads := []string{"qrdb_vlog_list", "qrdb_vlog_get", "qrdb_vlog_prove_inclusion", "qrdb_vlog_prove_consistency", "qrdb_vlog_head"}
	admit := map[string][]string{
		role.RoleNameAdmin:    append([]string{"qrdb_vlog_create", "qrdb_vlog_append", "qrdb_vlog_expire_bodies"}, reads...),
		role.RoleNameOperator: append([]string{"qrdb_vlog_append", "qrdb_vlog_sign_head", "qrdb_vlog_rotate_key"}, reads...),
		role.RoleNameUser:     append([]string{"qrdb_vlog_append"}, reads...),
		role.RoleNameAuditor:  reads,
	}
	for roleName, allowed := range admit {
		f.as(roleIdentity("acme", roleName))
		granted := map[string]bool{}
		for _, tool := range allowed {
			granted[tool] = true
		}
		for tool, args := range vlogToolArgs {
			_, _, before := f.svc.seen()
			res := f.call(t, tool, args)
			_, _, after := f.svc.seen()
			if granted[tool] {
				wantOK(t, roleName+" "+tool, res)
				continue
			}
			wantToolCode(t, roleName+" "+tool, res, wire.CodePermissionDenied)
			if after != before {
				t.Errorf("%s %s: the refused call reached the service", roleName, tool)
			}
		}
	}
}

// TestVlogTools_NameNoTenant: no tool takes a tenant, like every transport;
// a stray tenant_id argument is ignored, and the log is named within the
// caller's tenant and the session's database.
func TestVlogTools_NameNoTenant(t *testing.T) {
	f := newVlogFixture(t)
	for tool, args := range vlogToolArgs {
		caller := role.RoleNameAdmin
		if tool == "qrdb_vlog_sign_head" || tool == "qrdb_vlog_rotate_key" {
			caller = role.RoleNameOperator
		}
		f.as(roleIdentity("acme", caller))
		stray := map[string]any{"tenant_id": "globex"}
		for k, v := range args {
			stray[k] = v
		}
		wantOK(t, caller+" "+tool+" with a stray tenant_id", f.call(t, tool, stray))
		ref, _, _ := f.svc.seen()
		if ref.TenantID != "" || ref.Database != tenant.DefaultDatabase {
			t.Errorf("%s ref = %+v, want no tenant in the session's database", tool, ref)
		}
	}
}

// TestVlogTools_ServiceErrorsCarryTheSharedClassification: a log error is
// coded by pkg/api/errclass on MCP as on every transport.
func TestVlogTools_ServiceErrorsCarryTheSharedClassification(t *testing.T) {
	f := newVlogFixture(t)
	f.svc.err = &vlog.LogNotFoundError{}
	wantToolCode(t, "missing log", f.call(t, "qrdb_vlog_head", map[string]any{"log": "l"}), wire.CodeNotFound)
	f.svc.err = &vlog.ExpireNotAllowedError{}
	wantToolCode(t, "expire refused", f.call(t, "qrdb_vlog_expire_bodies", map[string]any{"log": "l", "cutoff": 1}), wire.CodeFailedPrecondition)
}

// vlogHandle calls a tool handler directly with args under ctx.
func vlogHandle(ctx context.Context, h func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error), args map[string]any) (*mcpgo.CallToolResult, error) {
	return h(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Arguments: args}})
}

// vlogDecode returns a successful tool result's JSON body decoded into out.
func vlogDecode(t *testing.T, res *mcpgo.CallToolResult, err error, out any) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error result: %+v", res.Content)
	}
	if err := json.Unmarshal([]byte(res.Content[0].(mcpgo.TextContent).Text), out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// TestVlogTools_CarryTheServiceResults: every tool hands the service the
// arguments it was given, within the session's database, and answers the
// vlogwire body.
func TestVlogTools_CarryTheServiceResults(t *testing.T) {
	svc := &fakeVlogService{}
	ctx := tenant.WithDatabase(tenant.WithTenantID(context.Background(), "acme"), "ledger")
	want := service.LogRef{Database: "ledger", Log: "l"}

	var created vlogwire.CreateResult
	res, err := vlogHandle(ctx, handleVlogCreate(svc), map[string]any{"log": "l", "origin": "o", "verifier_keys": []any{"vk"}})
	vlogDecode(t, res, err, &created)
	if created.Log != "l" || created.Database != "ledger" || created.Key == nil || string(created.Key.PublicKey) != "pk" {
		t.Fatalf("create = %+v", created)
	}

	var listed vlogwire.ListResult
	res, err = vlogHandle(ctx, handleVlogList(svc), map[string]any{})
	vlogDecode(t, res, err, &listed)
	if strings.Join(listed.Logs, ",") != "a,b" {
		t.Fatalf("list = %+v", listed)
	}

	var appended vlogwire.AppendResult
	res, err = vlogHandle(ctx, handleVlogAppend(svc), map[string]any{"log": "l", "entries": []any{"aGk=", ""}})
	vlogDecode(t, res, err, &appended)
	ref, entries, _ := svc.seen()
	if appended.FirstIndex != 7 || appended.Count != 2 || ref != want || string(entries[0]) != "hi" || len(entries[1]) != 0 {
		t.Fatalf("append = %+v, ref %+v, entries %q", appended, ref, entries)
	}

	var got vlogwire.GetResult
	res, err = vlogHandle(ctx, handleVlogGet(svc), map[string]any{"log": "l", "from": 3, "limit": 2})
	vlogDecode(t, res, err, &got)
	if len(got.Entries) != 2 || got.Entries[0].Index != 3 || string(got.Entries[0].Body) != "e0" || !got.Entries[1].Expired {
		t.Fatalf("get = %+v", got)
	}

	var inc vlogwire.InclusionProof
	res, err = vlogHandle(ctx, handleVlogProveInclusion(svc), map[string]any{"log": "l", "index": 1, "size": 4})
	vlogDecode(t, res, err, &inc)
	if inc.Index != 1 || inc.Size != 4 || len(inc.Hashes) != 1 {
		t.Fatalf("inclusion = %+v", inc)
	}

	var cons vlogwire.ConsistencyProof
	res, err = vlogHandle(ctx, handleVlogProveConsistency(svc), map[string]any{"log": "l", "size1": 2, "size2": 4})
	vlogDecode(t, res, err, &cons)
	if cons.Size1 != 2 || cons.Size2 != 4 || len(cons.Hashes) != 1 {
		t.Fatalf("consistency = %+v", cons)
	}

	var head vlogwire.Head
	res, err = vlogHandle(ctx, handleVlogHead(svc), map[string]any{"log": "l"})
	vlogDecode(t, res, err, &head)
	if head.Size != 9 || string(head.Root) != "root" || head.CheckpointSize != 8 {
		t.Fatalf("head = %+v", head)
	}

	var signed vlogwire.SignHeadResult
	res, err = vlogHandle(ctx, handleVlogSignHead(svc), map[string]any{"log": "l"})
	vlogDecode(t, res, err, &signed)
	if signed.Head.SignatureType != vlog.SignatureEd25519 || string(signed.Checkpoint) != "note" {
		t.Fatalf("sign head = %+v", signed)
	}

	var rotated vlogwire.RotateKeyResult
	res, err = vlogHandle(ctx, handleVlogRotateKey(svc), map[string]any{"log": "l", "signing_key_type": "ecdsa-p256-sha256"})
	vlogDecode(t, res, err, &rotated)
	if rotated.Key.Version != 2 || string(rotated.Key.PublicKey) != "pk2" || strings.Contains(res.Content[0].(mcpgo.TextContent).Text, "software/v2") || svc.keyType != vlog.SignatureECDSAP256SHA256 {
		t.Fatalf("rotate key = %+v, key type %q", rotated, svc.keyType)
	}

	var expired vlogwire.ExpireResult
	res, err = vlogHandle(ctx, handleVlogExpireBodies(svc), map[string]any{"log": "l", "cutoff": 5})
	vlogDecode(t, res, err, &expired)
	if expired.Expired != 5 {
		t.Fatalf("expire = %+v", expired)
	}
	if ref, _, _ := svc.seen(); ref != want {
		t.Fatalf("last ref = %+v, want %+v", ref, want)
	}
}

// TestVlogTools_RefuseBadArguments: a missing log or entries argument, an
// entry that is not base64 and an argument of the wrong shape are refused
// with the typed argument error, before the service is reached.
func TestVlogTools_RefuseBadArguments(t *testing.T) {
	svc := &fakeVlogService{}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	for name, c := range map[string]struct {
		h    func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
		args map[string]any
		want error
	}{
		"missing log":        {handleVlogHead(svc), map[string]any{}, &RequiredParameterError{Parameter: "log"}},
		"log not a string":   {handleVlogSignHead(svc), map[string]any{"log": 7}, &RequiredParameterError{Parameter: "log"}},
		"missing entries":    {handleVlogAppend(svc), map[string]any{"log": "l"}, &RequiredParameterError{Parameter: "entries"}},
		"entries not a list": {handleVlogAppend(svc), map[string]any{"log": "l", "entries": "aGk="}, &ToolArgumentError{Argument: "entries", Reason: "must be an array of strings"}},
		"entry not a string": {handleVlogAppend(svc), map[string]any{"log": "l", "entries": []any{1}}, &ToolArgumentError{Argument: "entries", Reason: "must be an array of strings"}},
		"verifier keys":      {handleVlogCreate(svc), map[string]any{"log": "l", "verifier_keys": "vk"}, &ToolArgumentError{Argument: "verifier_keys", Reason: "must be an array of strings"}},
	} {
		res, err := vlogHandle(ctx, c.h, c.args)
		if err != nil || res == nil || !res.IsError {
			t.Errorf("%s: res=%+v err=%v, want a tool error result", name, res, err)
			continue
		}
		if text := res.Content[0].(mcpgo.TextContent).Text; text != c.want.Error() {
			t.Errorf("%s: %q, want %q", name, text, c.want.Error())
		}
	}

	_, decodeErr := base64.StdEncoding.DecodeString("!!")
	res, err := vlogHandle(ctx, handleVlogAppend(svc), map[string]any{"log": "l", "entries": []any{"aGk=", "!!"}})
	want := (&ValueDecodeError{Field: "entries", Err: decodeErr}).Error()
	if err != nil || res == nil || !res.IsError || res.Content[0].(mcpgo.TextContent).Text != want {
		t.Fatalf("bad base64: res=%+v err=%v, want %q", res, err, want)
	}
	if _, _, calls := svc.seen(); calls != 0 {
		t.Fatalf("a refused argument reached the service %d times", calls)
	}
}

// TestVlogTools_RegisteredOnlyWithALogService: the tools are listed when a
// log service is configured, and absent without one.
func TestVlogTools_RegisteredOnlyWithALogService(t *testing.T) {
	for _, tc := range []struct {
		opts []ServerOption
		want bool
	}{
		{opts: []ServerOption{WithLogService(&fakeVlogService{})}, want: true},
		{opts: nil, want: false},
	} {
		srv := NewServer(nil, nil, "127.0.0.1:0", tc.opts...)
		for tool := range vlogToolArgs {
			if got := srv.mcpSrv.GetTool(tool) != nil; got != tc.want {
				t.Errorf("with log service %v: %s registered = %v", tc.want, tool, got)
			}
		}
	}
}
