package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type tenantQualifierKey struct{}

// WithTenantQualifier names the tenant whose identity records the calls made with ctx
// act on. Every transport sends it as the request's tenant qualifier on the
// user and role operations; the server treats another tenant than the
// caller's own as an operator access, allowed only under that tenant's
// operator-access isolation policy and recorded in both audit trails. An
// explicit tenant on a request (a create's or a list's TenantID) wins.
func WithTenantQualifier(ctx context.Context, tenantID string) context.Context {
	if tenantID == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantQualifierKey{}, tenantID)
}

// TenantQualifierFromContext is the tenant qualifier WithTenantQualifier
// set, or "".
func TenantQualifierFromContext(ctx context.Context) string {
	s, _ := ctx.Value(tenantQualifierKey{}).(string)
	return s
}

// TenantUnaryClientInterceptor sets the tenant qualifier on every request
// message that has an empty string field tenant_id.
func TenantUnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if tid := TenantQualifierFromContext(ctx); tid != "" {
			if m, ok := req.(proto.Message); ok {
				setTenantField(m.ProtoReflect(), tid)
			}
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func setTenantField(m protoreflect.Message, tenantID string) {
	fd := m.Descriptor().Fields().ByName("tenant_id")
	if fd == nil || fd.Kind() != protoreflect.StringKind || fd.Cardinality() == protoreflect.Repeated {
		return
	}
	if m.Get(fd).String() == "" {
		m.Set(fd, protoreflect.ValueOfString(tenantID))
	}
}

// TenantRoundTripper sends the tenant qualifier on the REST user and role
// routes: the tenantID query parameter, and tenant_id in the body of a role
// assignment.
func TenantRoundTripper(base http.RoundTripper) http.RoundTripper {
	return &tenantRoundTripper{base: base}
}

type tenantRoundTripper struct{ base http.RoundTripper }

func (rt *tenantRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tid := TenantQualifierFromContext(req.Context())
	if tid == "" || !isIdentityPath(req.URL.Path) {
		return rt.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	q := req.URL.Query()
	if q.Get("tenantID") == "" {
		q.Set("tenantID", tid)
		req.URL.RawQuery = q.Encode()
	}
	if strings.HasSuffix(req.URL.Path, "/roles/assign") || strings.HasSuffix(req.URL.Path, "/roles/unassign") {
		if err := setBodyTenant(req, "tenant_id", tid); err != nil {
			return nil, err
		}
	}
	return rt.base.RoundTrip(req)
}

func isIdentityPath(p string) bool {
	return strings.Contains(p, "/users") || strings.Contains(p, "/roles")
}

func setBodyTenant(req *http.Request, key, tenantID string) error {
	if req.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return err
	}
	out := SetJSONTenant(raw, tenantID, key)
	req.Body = io.NopCloser(bytes.NewReader(out))
	req.ContentLength = int64(len(out))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(out)), nil }
	return nil
}

// SetJSONTenant returns payload, a JSON object, with key set to tenantID
// unless it already names a tenant under key or any of also. A payload that
// is not a JSON object is returned unchanged.
func SetJSONTenant(payload []byte, tenantID, key string, also ...string) []byte {
	var obj map[string]json.RawMessage
	if json.Unmarshal(payload, &obj) != nil || obj == nil {
		return payload
	}
	for _, k := range append([]string{key}, also...) {
		if v, ok := obj[k]; ok && string(v) != `""` && string(v) != "null" {
			return payload
		}
	}
	v, _ := json.Marshal(tenantID)
	obj[key] = v
	out, err := json.Marshal(obj)
	if err != nil {
		return payload
	}
	return out
}

// QualifyJSON sets the tenant qualifier ctx carries on payload, under key,
// when name (a QUIC method or an MCP tool) starts with one of prefixes.
func QualifyJSON(ctx context.Context, name string, payload []byte, key string, prefixes ...string) []byte {
	tid := TenantQualifierFromContext(ctx)
	if tid == "" || !hasAnyPrefix(name, prefixes) {
		return payload
	}
	return SetJSONTenant(payload, tid, key, "tenant_id", "tenantID")
}

// QualifyArgs sets tenant_id in an MCP tool's arguments to the tenant
// qualifier ctx carries, when tool starts with one of prefixes and the
// arguments name no tenant.
func QualifyArgs(ctx context.Context, tool string, args map[string]any, prefixes ...string) map[string]any {
	tid := TenantQualifierFromContext(ctx)
	if tid == "" || !hasAnyPrefix(tool, prefixes) {
		return args
	}
	if v, ok := args["tenant_id"].(string); ok && v != "" {
		return args
	}
	if args == nil {
		args = map[string]any{}
	}
	args["tenant_id"] = tid
	return args
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
