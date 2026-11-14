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
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got.URL.Query().Get("tenantID") != "acme" {
		t.Fatalf("get user query = %q, want tenantID=acme", got.URL.RawQuery)
	}

	req = httptest.NewRequest(http.MethodPost, "http://x/roles/assign", strings.NewReader(`{"user_id":"7","role_id":"r1"}`)).WithContext(ctx)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil || m["tenant_id"] != "acme" || m["role_id"] != "r1" {
		t.Fatalf("assign body = %s, want tenant_id acme beside the request", body)
	}

	req = httptest.NewRequest(http.MethodGet, "http://x/kv/a", nil).WithContext(ctx)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got.URL.RawQuery != "" {
		t.Fatalf("a non-identity route got the qualifier: %q", got.URL.RawQuery)
	}

	req = httptest.NewRequest(http.MethodGet, "http://x/users/7", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got.URL.RawQuery != "" {
		t.Fatalf("no qualifier on ctx, yet the query is %q", got.URL.RawQuery)
	}
}

func TestTenantUnaryClientInterceptor_SetsEmptyTenantField(t *testing.T) {
	icpt := TenantUnaryClientInterceptor()
	ctx := WithTenantQualifier(context.Background(), "acme")
	invoke := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }

	req := &pb.GetUserRequest{Id: 7}
	if err := icpt(ctx, "/m", req, nil, nil, invoke); err != nil {
		t.Fatal(err)
	}
	if req.GetTenantId() != "acme" {
		t.Fatalf("tenant_id = %q, want acme", req.GetTenantId())
	}

	named := &pb.GetUserRequest{Id: 7, TenantId: "globex"}
	if err := icpt(ctx, "/m", named, nil, nil, invoke); err != nil {
		t.Fatal(err)
	}
	if named.GetTenantId() != "globex" {
		t.Fatalf("an explicit tenant_id was overwritten: %q", named.GetTenantId())
	}

	// A message without a tenant_id field passes untouched.
	if err := icpt(ctx, "/m", structpb.NewNullValue(), nil, nil, invoke); err != nil {
		t.Fatal(err)
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
