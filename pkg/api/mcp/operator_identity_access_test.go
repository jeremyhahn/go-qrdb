package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

type operatorPolicy struct{ acme tenant.TenantIsolationPolicy }

func (p *operatorPolicy) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	if id != "acme" {
		return nil, errors.New("tenant not found")
	}
	return &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationRaft, IsolationPolicy: p.acme}, nil
}

func (p *operatorPolicy) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

type operatorAuditSink struct {
	mu     sync.Mutex
	trails map[string]bool
}

func (s *operatorAuditSink) Log(_ context.Context, e *audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Action == authmw.OperatorIdentityAccessAction {
		s.trails[e.TenantID] = true
	}
	return nil
}

func (s *operatorAuditSink) Close() error { return nil }

// operatorCall runs every identity tool's handler, as wrapped by
// withOperatorTenant, for the platform operator naming acme.
func operatorCall(t *testing.T, policy tenant.TenantIsolationPolicy) (ran map[string]string, errs map[string]error, sink *operatorAuditSink) {
	t.Helper()
	sink = &operatorAuditSink{trails: map[string]bool{}}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithAuditLogger(sink), authmw.WithIsolationPolicyReader(&operatorPolicy{acme: policy}))
	ctx := auth.WithIdentity(tenant.WithTenantID(context.Background(), tenantid.System),
		authtest.AdminIdentity(tenantid.System, "operator"))
	ran, errs = map[string]string{}, map[string]error{}
	tools := append(userTools(nil, nil, mw, nil), roleTools(nil, nil, mw)...)
	for _, tl := range tools {
		if _, ok := operatorIdentityTools[tl.Tool.Name]; !ok {
			continue
		}
		if _, ok := tl.Tool.InputSchema.Properties["tenant_id"]; !ok {
			t.Fatalf("%s has no tenant_id argument", tl.Tool.Name)
		}
		name := tl.Tool.Name
		// Stand in for the tool's own handler: record the tenant it runs under.
		next := func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ran[name] = tenant.FromContext(ctx)
			return mcp.NewToolResultText("ok"), nil
		}
		h := operatorTenantHandler(mw, operatorIdentityTools[name], next)
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = map[string]any{"tenant_id": "acme"}
		if _, err := h(ctx, req); err != nil {
			errs[name] = err
		}
	}
	if len(ran)+len(errs) != len(operatorIdentityTools) {
		t.Fatalf("ran %d tools and refused %d, want all %d identity tools", len(ran), len(errs), len(operatorIdentityTools))
	}
	return ran, errs, sink
}

func TestOperatorIdentityAccess_MCPToolsUnderOperatorAccess(t *testing.T) {
	ran, errs, sink := operatorCall(t, tenant.TenantIsolationPolicyInherit)
	for name, err := range errs {
		t.Errorf("%s: refused under operator-access: %v", name, err)
	}
	for name, tid := range ran {
		if tid != "acme" {
			t.Errorf("%s ran under tenant %q, want acme", name, tid)
		}
	}
	if !sink.trails["acme"] || !sink.trails[tenantid.Normalize(tenantid.System)] {
		t.Fatalf("operator access recorded in trails %v, want the platform trail and acme's", sink.trails)
	}
}

func TestOperatorIdentityAccess_MCPToolsRefusedUnderStrict(t *testing.T) {
	ran, errs, sink := operatorCall(t, tenant.TenantIsolationPolicyStrict)
	if len(ran) != 0 {
		t.Fatalf("tools ran after the refusal: %v", ran)
	}
	for name, err := range errs {
		var refused *authmw.OperatorAccessRefusedError
		if !errors.As(err, &refused) {
			t.Errorf("%s: error %v, want OperatorAccessRefusedError", name, err)
		}
	}
	if !sink.trails["acme"] {
		t.Fatal("the refusal is not in acme's trail")
	}
}
