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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// mcpFloorGet calls qrdb_cluster_isolation_policy_get and returns the floor,
// or the handler's error.
func mcpFloorGet(t *testing.T, svc tenant.TenantServicer) (string, error) {
	t.Helper()
	result, err := handleClusterIsolationPolicyGet(svc)(context.Background(), buildCallToolRequest(t, "qrdb_cluster_isolation_policy_get", map[string]any{}))
	if err != nil {
		return "", err
	}
	return mcpFloorPolicy(t, result.IsError, resultText(t, result)), nil
}

// mcpFloorSet calls qrdb_cluster_isolation_policy_set and returns the floor
// it reports, or the handler's error.
func mcpFloorSet(t *testing.T, svc tenant.TenantServicer, policy string) (string, error) {
	t.Helper()
	result, err := handleClusterIsolationPolicySet(svc)(context.Background(), buildCallToolRequest(t, "qrdb_cluster_isolation_policy_set", map[string]any{"policy": policy}))
	if err != nil {
		return "", err
	}
	return mcpFloorPolicy(t, result.IsError, resultText(t, result)), nil
}

func mcpFloorPolicy(t *testing.T, isError bool, text string) string {
	t.Helper()
	if isError {
		t.Fatalf("floor tool error: %s", text)
	}
	var out struct {
		Policy string `json:"policy"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out.Policy
}

// strictIsolationActiveMCPTenants is descriptiveMCPTenants with the
// strict-isolation gate reporting active, which this binary cannot reach in
// production: it exercises the tools' success paths.
func strictIsolationActiveMCPTenants(t *testing.T) tenant.TenantServicer {
	t.Helper()
	svc := descriptiveMCPTenants(t)
	svc.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })
	return svc
}

// TestMCPIsolationPolicy_RoundTrip drives the floor and a tenant's
// isolation_policy through the MCP handlers over the real tenant service:
// the default floor, a tighten, a refused loosening, a refused invalid
// value, a tenant tightened to strict, and strict refused back to inherit.
// Every refusal leaves the stored state as it was.
func TestMCPIsolationPolicy_RoundTrip(t *testing.T) {
	svc := strictIsolationActiveMCPTenants(t)
	if got, err := mcpFloorGet(t, svc); err != nil || got != string(tenant.ClusterIsolationPolicyOperatorAccess) {
		t.Fatalf("default floor = %q, %v; want operator-access", got, err)
	}
	if got, err := mcpFloorSet(t, svc, "strict"); err != nil || got != string(tenant.ClusterIsolationPolicyStrict) {
		t.Fatalf("set strict = %q, %v", got, err)
	}
	_, err := mcpFloorSet(t, svc, "operator-access")
	var changeErr *tenant.IsolationPolicyChangeError
	if !errors.As(err, &changeErr) || errclass.Classify(err) != errclass.FailedPrecondition {
		t.Fatalf("loosening = %v; want a FailedPrecondition *tenant.IsolationPolicyChangeError", err)
	}
	_, err = mcpFloorSet(t, svc, "loose")
	var invalid *tenant.InvalidIsolationPolicyError
	if !errors.As(err, &invalid) || errclass.Classify(err) != errclass.InvalidArgument {
		t.Fatalf("unknown policy = %v; want an InvalidArgument *tenant.InvalidIsolationPolicyError", err)
	}
	if got, err := mcpFloorGet(t, svc); err != nil || got != string(tenant.ClusterIsolationPolicyStrict) {
		t.Fatalf("floor after refusals = %q, %v; want strict", got, err)
	}

	if err := mcpTenantUpdate(t, svc, map[string]any{"isolation_policy": "loose"}); errclass.Classify(err) != errclass.InvalidArgument {
		t.Fatalf("unknown isolation_policy = %v; want InvalidArgument", err)
	}
	if got := mcpTenantGet(t, svc); got.IsolationPolicy != tenant.TenantIsolationPolicyInherit {
		t.Fatalf("isolation_policy after an invalid value = %q; want inherit", got.IsolationPolicy)
	}
	if err := mcpTenantUpdate(t, svc, map[string]any{"isolation_policy": "strict"}); err != nil {
		t.Fatalf("tenant strict: %v", err)
	}
	if got := mcpTenantGet(t, svc); got.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("isolation_policy = %q; want strict", got.IsolationPolicy)
	}
	err = mcpTenantUpdate(t, svc, map[string]any{"isolation_policy": ""})
	if !errors.As(err, &changeErr) || errclass.Classify(err) != errclass.FailedPrecondition {
		t.Fatalf("strict to inherit = %v; want a FailedPrecondition *tenant.IsolationPolicyChangeError", err)
	}
	if got := mcpTenantGet(t, svc); got.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("isolation_policy after a refused loosening = %q; want strict", got.IsolationPolicy)
	}
}

// TestMCPIsolationPolicy_EmptyPolicyIsRefusedAsInvalid pins that the set-floor
// tool passes an empty policy to the service, so the caller gets the same
// typed refusal every other transport returns, and the floor stays as it was.
func TestMCPIsolationPolicy_EmptyPolicyIsRefusedAsInvalid(t *testing.T) {
	svc := strictIsolationActiveMCPTenants(t)
	_, err := mcpFloorSet(t, svc, "")
	var invalid *tenant.InvalidIsolationPolicyError
	if !errors.As(err, &invalid) || invalid.Value != "" || errclass.Classify(err) != errclass.InvalidArgument {
		t.Fatalf("empty policy = %v; want an InvalidArgument *tenant.InvalidIsolationPolicyError with an empty Value", err)
	}
	if got, err := mcpFloorGet(t, svc); err != nil || got != string(tenant.ClusterIsolationPolicyOperatorAccess) {
		t.Fatalf("floor after the refusal = %q, %v; want operator-access", got, err)
	}
}

// TestMCPIsolationPolicy_RefusedBeforeStrictIsolation pins that before
// strict-isolation is active the floor tools and a tenant update carrying
// isolation_policy return *feature.NotActiveError and change nothing.
func TestMCPIsolationPolicy_RefusedBeforeStrictIsolation(t *testing.T) {
	svc := descriptiveMCPTenants(t)
	_, getErr := mcpFloorGet(t, svc)
	_, setErr := mcpFloorSet(t, svc, "strict")
	updErr := mcpTenantUpdate(t, svc, map[string]any{"isolation_policy": "strict"})
	for name, err := range map[string]error{"get floor": getErr, "set floor": setErr, "tenant strict": updErr} {
		var notActive *feature.NotActiveError
		if !errors.As(err, &notActive) || notActive.Feature != tenant.StrictIsolation || errclass.Classify(err) != errclass.Unavailable {
			t.Fatalf("%s before activation = %v; want an Unavailable *feature.NotActiveError(%s)", name, err, tenant.StrictIsolation)
		}
	}
	if got := mcpTenantGet(t, svc); got.IsolationPolicy != tenant.TenantIsolationPolicyInherit {
		t.Fatalf("isolation_policy after a refused update = %q; want inherit", got.IsolationPolicy)
	}
	svc.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })
	if got, err := mcpFloorGet(t, svc); err != nil || got != string(tenant.ClusterIsolationPolicyOperatorAccess) {
		t.Fatalf("floor after a refused set = %q, %v; want operator-access", got, err)
	}
}
