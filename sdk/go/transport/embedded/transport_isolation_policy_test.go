package embedded

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// isoPolicySvc is a tenant service whose strict-isolation gate is open only
// when active is set. It stores the floor and records every tenant patch,
// so a test can prove a refusal changed nothing.
type isoPolicySvc struct {
	tenant.TenantServicer

	mu      sync.Mutex
	active  bool
	floor   tenant.ClusterIsolationPolicy
	patches []tenant.TenantPatch
}

func (s *isoPolicySvc) StrictIsolationCheck() func() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return nil
	}
	return func() error { return nil }
}

func (s *isoPolicySvc) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.floor == "" {
		return tenant.ClusterIsolationPolicyOperatorAccess, nil
	}
	return s.floor, nil
}

func (s *isoPolicySvc) SetClusterIsolationPolicy(_ context.Context, next tenant.ClusterIsolationPolicy) error {
	if next != tenant.ClusterIsolationPolicyStrict && next != tenant.ClusterIsolationPolicyOperatorAccess {
		return &tenant.InvalidIsolationPolicyError{Value: string(next)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.floor = next
	return nil
}

func (s *isoPolicySvc) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Name: "Acme"}, nil
}

func (s *isoPolicySvc) UpdateTenant(_ context.Context, id string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patches = append(s.patches, patch)
	return &tenant.TenantInfo{ID: id, Name: "Acme", IsolationPolicy: patch.IsolationPolicy}, nil
}

func (s *isoPolicySvc) state() (tenant.ClusterIsolationPolicy, []tenant.TenantPatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.floor, append([]tenant.TenantPatch(nil), s.patches...)
}

func requireStrictIsolationNotActive(t *testing.T, err error, op string) {
	t.Helper()
	var na *feature.NotActiveError
	if !errors.As(err, &na) || na.Feature != featuregate.StrictIsolation {
		t.Fatalf("%s = %T %v, want *feature.NotActiveError{%s}", op, err, err, featuregate.StrictIsolation)
	}
}

func TestRoundTrip_IsolationPolicyVerbs_RefusedUntilActive(t *testing.T) {
	svc := &isoPolicySvc{}
	tr := isoPolicyTransport(t, svc)
	ctx := context.Background()
	strict := "strict"

	_, err := tr.GetClusterIsolationPolicy(ctx)
	requireStrictIsolationNotActive(t, err, "GetClusterIsolationPolicy")
	_, err = tr.SetClusterIsolationPolicy(ctx, strict)
	requireStrictIsolationNotActive(t, err, "SetClusterIsolationPolicy")
	_, err = tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", IsolationPolicy: &strict})
	requireStrictIsolationNotActive(t, err, "UpdateTenant")
	if floor, patches := svc.state(); floor != "" || len(patches) != 0 {
		t.Fatalf("a refusal changed state: floor %q, patches %+v", floor, patches)
	}

	// Without the field the update is not gated and carries no policy.
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Name: "Renamed"}); err != nil {
		t.Fatalf("UpdateTenant without isolation policy: %v", err)
	}
	if _, patches := svc.state(); len(patches) != 1 || patches[0].IsolationPolicy != "" {
		t.Fatalf("patches = %+v, want one with no isolation policy", patches)
	}
}

func TestRoundTrip_IsolationPolicyVerbs_ActiveRoundTrip(t *testing.T) {
	svc := &isoPolicySvc{active: true}
	tr := isoPolicyTransport(t, svc)
	ctx := context.Background()

	if got, err := tr.GetClusterIsolationPolicy(ctx); err != nil || got != "operator-access" {
		t.Fatalf("GetClusterIsolationPolicy = %q, %v", got, err)
	}
	if got, err := tr.SetClusterIsolationPolicy(ctx, "strict"); err != nil || got != "strict" {
		t.Fatalf("SetClusterIsolationPolicy = %q, %v", got, err)
	}
	_, err := tr.SetClusterIsolationPolicy(ctx, "loose")
	var bad *tenant.InvalidIsolationPolicyError
	if !errors.As(err, &bad) || bad.Value != "loose" {
		t.Fatalf("SetClusterIsolationPolicy(loose) = %T %v, want *tenant.InvalidIsolationPolicyError", err, err)
	}
	if floor, _ := svc.state(); floor != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("floor = %q after a refused set, want strict", floor)
	}

	strict := "strict"
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", IsolationPolicy: &strict}); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if _, patches := svc.state(); len(patches) != 1 || patches[0].IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("patches = %+v, want one carrying strict", patches)
	}
}

// strictTenantSvc reports every tenant as already strict.
type strictTenantSvc struct{ *isoPolicySvc }

func (s strictTenantSvc) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, IsolationPolicy: tenant.TenantIsolationPolicyStrict}, nil
}

func TestEmbedded_UpdateTenantRefusesLooseningAStrictTenant(t *testing.T) {
	svc := &isoPolicySvc{active: true}
	tr := newExtTransport(t, WithTenantService(strictTenantSvc{svc}))
	inherit := ""
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "acme", IsolationPolicy: &inherit})
	var change *tenant.IsolationPolicyChangeError
	if !errors.As(err, &change) {
		t.Fatalf("UpdateTenant(inherit) on a strict tenant = %T %v, want *tenant.IsolationPolicyChangeError", err, err)
	}
	if _, patches := svc.state(); len(patches) != 0 {
		t.Fatalf("a refused loosening reached the service: %+v", patches)
	}
}

func isoPolicyTransport(t *testing.T, svc *isoPolicySvc) *Transport {
	t.Helper()
	return newExtTransport(t, WithTenantService(svc))
}
