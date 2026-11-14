package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// gatedTenants is a real KV tenant service whose strict-isolation gate is
// open only when active is set, and which stores the cluster floor.
type gatedTenants struct {
	tenant.TenantServicer

	mu     sync.Mutex
	active bool
	floor  tenant.ClusterIsolationPolicy
}

func newGatedTenants(t *testing.T, active bool) *gatedTenants {
	t.Helper()
	return &gatedTenants{TenantServicer: descriptiveTenants(t), active: active}
}

func (s *gatedTenants) StrictIsolationCheck() func() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return nil
	}
	return func() error { return nil }
}

func (s *gatedTenants) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.floor == tenant.ClusterIsolationPolicyUnset {
		return tenant.ClusterIsolationPolicyOperatorAccess, nil
	}
	return s.floor, nil
}

func (s *gatedTenants) SetClusterIsolationPolicy(_ context.Context, next tenant.ClusterIsolationPolicy) error {
	if next != tenant.ClusterIsolationPolicyStrict && next != tenant.ClusterIsolationPolicyOperatorAccess {
		return &tenant.InvalidIsolationPolicyError{Value: string(next)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.floor = next
	return nil
}

func (s *gatedTenants) storedFloor() tenant.ClusterIsolationPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.floor
}

// executeRootCmd runs the full qrdb command tree against svc over the
// embedded transport and returns what it printed.
func executeRootCmd(t *testing.T, svc tenant.TenantServicer, format string, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithTenantService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient, oldWriter, oldFormat := client, outputWriter, outputFormat
	t.Cleanup(func() {
		client, outputWriter, outputFormat = oldClient, oldWriter, oldFormat
	})
	client = sdkClient
	outputFormat = format
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newClusterIsolationPolicyCmd()
	if args[0] == "tenant" {
		cmd = newTenantCmd()
	}
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args[1:])
	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

func requireStrictIsolationRefusal(t *testing.T, err error, what string) {
	t.Helper()
	var na *feature.NotActiveError
	if !errors.As(err, &na) || na.Feature != featuregate.StrictIsolation {
		t.Fatalf("%s = %T %v; want *feature.NotActiveError{%s}", what, err, err, featuregate.StrictIsolation)
	}
}

func TestClusterIsolationPolicyCmd_RefusedUntilActive(t *testing.T) {
	svc := newGatedTenants(t, false)

	_, err := executeRootCmd(t, svc, "table", "cluster-isolation-policy", "get")
	requireStrictIsolationRefusal(t, err, "get")
	_, err = executeRootCmd(t, svc, "table", "cluster-isolation-policy", "set", "strict")
	requireStrictIsolationRefusal(t, err, "set strict")
	_, err = executeRootCmd(t, svc, "table", "tenant", "update", "acme", "--isolation-policy", "strict")
	requireStrictIsolationRefusal(t, err, "tenant update --isolation-policy strict")

	if floor := svc.storedFloor(); floor != tenant.ClusterIsolationPolicyUnset {
		t.Fatalf("a refused set stored floor %q", floor)
	}
	if got, err := svc.GetTenant(context.Background(), "acme"); err != nil || got.IsolationPolicy != tenant.TenantIsolationPolicyInherit {
		t.Fatalf("a refused update stored isolation policy %q (%v)", got.IsolationPolicy, err)
	}
}

func TestClusterIsolationPolicyCmd_GetAndSetPrintTheStoredFloor(t *testing.T) {
	svc := newGatedTenants(t, true)

	out, err := executeRootCmd(t, svc, "table", "cluster-isolation-policy", "get")
	if err != nil || !strings.Contains(out, "Cluster isolation policy: operator-access") {
		t.Fatalf("get = %v:\n%s", err, out)
	}
	out, err = executeRootCmd(t, svc, "json", "cluster-isolation-policy", "set", "strict")
	if err != nil || !strings.Contains(out, `"policy": "strict"`) {
		t.Fatalf("set strict = %v:\n%s", err, out)
	}
	if floor := svc.storedFloor(); floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("stored floor = %q; want strict", floor)
	}

	_, err = executeRootCmd(t, svc, "table", "cluster-isolation-policy", "set", "loose")
	var bad *tenant.InvalidIsolationPolicyError
	if !errors.As(err, &bad) || bad.Value != "loose" {
		t.Fatalf("set loose = %T %v; want *tenant.InvalidIsolationPolicyError", err, err)
	}
	if floor := svc.storedFloor(); floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("a refused set changed the floor to %q", floor)
	}

	if _, err := executeRootCmd(t, svc, "table", "cluster-isolation-policy", "set"); err == nil {
		t.Fatal("set with no policy succeeded")
	}
}

func TestTenantUpdateCmd_IsolationPolicyFlag(t *testing.T) {
	svc := newGatedTenants(t, true)
	ctx := context.Background()

	// Without the flag the update carries no policy and is not gated.
	if _, err := executeRootCmd(t, svc, "table", "tenant", "update", "acme", "--description", "d"); err != nil {
		t.Fatal(err)
	}
	// inherit on an inherit tenant is a no-op, not a refusal.
	if _, err := executeRootCmd(t, svc, "table", "tenant", "update", "acme", "--isolation-policy", "inherit"); err != nil {
		t.Fatalf("--isolation-policy inherit: %v", err)
	}
	_, err := executeRootCmd(t, svc, "table", "tenant", "update", "acme", "--isolation-policy", "loose")
	var bad *tenant.InvalidIsolationPolicyError
	if !errors.As(err, &bad) {
		t.Fatalf("--isolation-policy loose = %T %v; want *tenant.InvalidIsolationPolicyError", err, err)
	}
	if got, _ := svc.GetTenant(ctx, "acme"); got.IsolationPolicy != tenant.TenantIsolationPolicyInherit {
		t.Fatalf("a refused update stored %q", got.IsolationPolicy)
	}

	if _, err := executeRootCmd(t, svc, "table", "tenant", "update", "acme", "--isolation-policy", "strict"); err != nil {
		t.Fatalf("--isolation-policy strict: %v", err)
	}
	if got, _ := svc.GetTenant(ctx, "acme"); got.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("stored isolation policy = %q; want strict", got.IsolationPolicy)
	}

	// A strict tenant never goes back to inherit.
	_, err = executeRootCmd(t, svc, "table", "tenant", "update", "acme", "--isolation-policy", "inherit")
	var change *tenant.IsolationPolicyChangeError
	if !errors.As(err, &change) {
		t.Fatalf("strict -> inherit = %T %v; want *tenant.IsolationPolicyChangeError", err, err)
	}
	if got, _ := svc.GetTenant(ctx, "acme"); got.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("a refused loosening stored %q", got.IsolationPolicy)
	}
}

// TestTenantGetCmd_PrintsIsolationPolicy proves `qrdb tenant get` reports
// the tenant's isolation policy in both the table and the JSON output, and
// prints no policy line for a tenant that inherits the cluster floor.
func TestTenantGetCmd_PrintsIsolationPolicy(t *testing.T) {
	svc := newGatedTenants(t, true)

	out, err := executeRootCmd(t, svc, "table", "tenant", "get", "acme")
	if err != nil || strings.Contains(out, "Isolation policy:") {
		t.Fatalf("get of an inherit tenant = %v:\n%s", err, out)
	}
	if _, err := executeRootCmd(t, svc, "table", "tenant", "update", "acme", "--isolation-policy", "strict"); err != nil {
		t.Fatalf("--isolation-policy strict: %v", err)
	}
	out, err = executeRootCmd(t, svc, "table", "tenant", "get", "acme")
	if err != nil || !strings.Contains(out, "Isolation policy: strict") {
		t.Fatalf("get = %v:\n%s", err, out)
	}
	out, err = executeRootCmd(t, svc, "json", "tenant", "get", "acme")
	if err != nil || !strings.Contains(out, `"isolation_policy": "strict"`) {
		t.Fatalf("get -o json = %v:\n%s", err, out)
	}
}
