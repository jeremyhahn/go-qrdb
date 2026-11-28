package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// roundTripClosed sends req through rt and closes the response body.
func roundTripClosed(t *testing.T, rt http.RoundTripper, req *http.Request) {
	t.Helper()
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestTenantRoundTripper_QualifiesIdentityRoutes(t *testing.T) {
	var got *http.Request
	var body string
	rt := TenantRoundTripper(rtFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}))
	ctx := WithTenantQualifier(context.Background(), "acme")

	req := httptest.NewRequest(http.MethodGet, "http://x/users/7", nil).WithContext(ctx)
	roundTripClosed(t, rt, req)
	if got.URL.Query().Get("tenantID") != "acme" {
		t.Fatalf("get user query = %q, want tenantID=acme", got.URL.RawQuery)
	}

	req = httptest.NewRequest(http.MethodPost, "http://x/roles/assign", strings.NewReader(`{"user_id":"7","role_id":"r1"}`)).WithContext(ctx)
	roundTripClosed(t, rt, req)
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil || m["tenant_id"] != "acme" || m["role_id"] != "r1" {
		t.Fatalf("assign body = %s, want tenant_id acme beside the request", body)
	}

	req = httptest.NewRequest(http.MethodGet, "http://x/kv/a", nil).WithContext(ctx)
	roundTripClosed(t, rt, req)
	if got.URL.RawQuery != "" {
		t.Fatalf("a non-identity route got the qualifier: %q", got.URL.RawQuery)
	}

	req = httptest.NewRequest(http.MethodGet, "http://x/users/7", nil)
	roundTripClosed(t, rt, req)
	if got.URL.RawQuery != "" {
		t.Fatalf("no qualifier on ctx, yet the query is %q", got.URL.RawQuery)
	}
}

// TestTenantRoundTripper_FillsTheFieldEachRouteReads round-trips each REST
// identity route that does not read the tenantID query parameter through a
// server reading the tenant where the go-qrdb REST handler does: the body's
// tenant_id for POST /users, POST /roles and POST /users/lookup, the
// tenant_id query parameter for GET /roles/by-name. The qualifier fills the
// field; a tenant set explicitly there is kept.
func TestTenantRoundTripper_FillsTheFieldEachRouteReads(t *testing.T) {
	bodyTenant := func(r *http.Request) string {
		var b struct {
			TenantID string `json:"tenant_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		return b.TenantID
	}
	mux := http.NewServeMux()
	for _, p := range []string{"POST /v1/users", "POST /v1/roles", "POST /v1/users/lookup"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, bodyTenant(r)) })
	}
	mux.HandleFunc("GET /v1/roles/by-name", func(w http.ResponseWriter, r *http.Request) {
		// #nosec G705 -- the httptest server echoes tenant_id back so the test can assert which tenant the round tripper attached; the reader is the test itself, never a browser.
		_, _ = io.WriteString(w, r.URL.Query().Get("tenant_id"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := &http.Client{Transport: TenantRoundTripper(http.DefaultTransport)}
	ctx := WithTenantQualifier(context.Background(), "acme")

	call := func(method, path, body string) string {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		got, _ := io.ReadAll(resp.Body)
		return string(got)
	}

	for _, c := range []struct{ method, path, body, want string }{
		{http.MethodPost, "/v1/users", `{"username":"ann"}`, "acme"},
		{http.MethodPost, "/v1/roles", `{"name":"readers"}`, "acme"},
		{http.MethodPost, "/v1/users/lookup", `{"email":"ann@example.com"}`, "acme"},
		{http.MethodGet, "/v1/roles/by-name?name=readers", "", "acme"},
		{http.MethodPost, "/v1/users", `{"username":"ann","tenant_id":"globex"}`, "globex"},
		{http.MethodPost, "/v1/roles", `{"name":"readers","tenant_id":"globex"}`, "globex"},
		{http.MethodPost, "/v1/users/lookup", `{"email":"ann@example.com","tenant_id":"globex"}`, "globex"},
		{http.MethodGet, "/v1/roles/by-name?name=readers&tenant_id=globex", "", "globex"},
	} {
		if got := call(c.method, c.path, c.body); got != c.want {
			t.Errorf("%s %s %s: the server read tenant %q, want %q", c.method, c.path, c.body, got, c.want)
		}
	}
}

func TestTenantUnaryClientInterceptor_SetsEmptyTenantField(t *testing.T) {
	icpt := TenantUnaryClientInterceptor()
	ctx := WithTenantQualifier(context.Background(), "acme")
	invoke := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }

	req := &pb.GetUserRequest{Id: 7}
	if err := icpt(ctx, pb.QRDBService_GetUser_FullMethodName, req, nil, nil, invoke); err != nil {
		t.Fatal(err)
	}
	if req.GetTenantId() != "acme" {
		t.Fatalf("tenant_id = %q, want acme", req.GetTenantId())
	}

	named := &pb.GetUserRequest{Id: 7, TenantId: "globex"}
	if err := icpt(ctx, pb.QRDBService_GetUser_FullMethodName, named, nil, nil, invoke); err != nil {
		t.Fatal(err)
	}
	if named.GetTenantId() != "globex" {
		t.Fatalf("an explicit tenant_id was overwritten: %q", named.GetTenantId())
	}

	// A message without a tenant_id field passes untouched.
	if err := icpt(ctx, pb.QRDBService_GetUser_FullMethodName, structpb.NewNullValue(), nil, nil, invoke); err != nil {
		t.Fatal(err)
	}
}

// TestTenantUnaryClientInterceptor_LeavesSystemBarrierCallsUnqualified
// proves a barrier call, whose empty tenant_id names the system barrier,
// is sent with the tenant its caller set, while a role call on the same
// ctx is qualified.
func TestTenantUnaryClientInterceptor_LeavesSystemBarrierCallsUnqualified(t *testing.T) {
	icpt := TenantUnaryClientInterceptor()
	ctx := WithTenantQualifier(context.Background(), "acme")
	invoke := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }

	for _, tc := range []struct {
		method string
		req    interface{ GetTenantId() string }
	}{
		{pb.QRDBService_BarrierEnableEncryption_FullMethodName, &pb.BarrierEnableEncryptionRequest{}},
		{pb.TenantBarrierService_Status_FullMethodName, &pb.TenantBarrierStatusRequest{}},
		{pb.TenantBarrierService_Seal_FullMethodName, &pb.TenantBarrierSealRequest{}},
	} {
		if err := icpt(ctx, tc.method, tc.req, nil, nil, invoke); err != nil {
			t.Fatal(err)
		}
		if got := tc.req.GetTenantId(); got != "" {
			t.Fatalf("%s: tenant_id = %q, want the system barrier (empty)", tc.method, got)
		}
	}

	role := &pb.ListRolesRequest{}
	if err := icpt(ctx, pb.QRDBService_ListRoles_FullMethodName, role, nil, nil, invoke); err != nil {
		t.Fatal(err)
	}
	if role.GetTenantId() != "acme" {
		t.Fatalf("ListRoles tenant_id = %q, want acme", role.GetTenantId())
	}
}

func TestQualifyJSONAndArgs(t *testing.T) {
	ctx := WithTenantQualifier(context.Background(), "acme")
	out := QualifyJSON(ctx, "user.Get", []byte(`{"id":7,"tenantID":""}`), "tenantID", "user.", "role.")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil || m["tenantID"] != "acme" {
		t.Fatalf("user.Get payload = %s, want tenantID acme", out)
	}
	if out := QualifyJSON(ctx, "kv.Get", []byte(`{"k":"a"}`), "tenantID", "user.", "role."); string(out) != `{"k":"a"}` {
		t.Fatalf("kv.Get payload changed: %s", out)
	}
	if out := QualifyJSON(ctx, "role.Create", []byte(`{"tenantID":"globex"}`), "tenantID", "role."); string(out) != `{"tenantID":"globex"}` {
		t.Fatalf("an explicit tenant was overwritten: %s", out)
	}
	args := QualifyArgs(ctx, "qrdb_user_get", map[string]any{"id": "7"}, "qrdb_user_")
	if args["tenant_id"] != "acme" {
		t.Fatalf("MCP args = %v, want tenant_id acme", args)
	}
}
