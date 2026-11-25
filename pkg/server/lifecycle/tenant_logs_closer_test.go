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

package lifecycle

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// closerWiringTenantSvc is a tenant service that holds the runtime closer it
// is given, as tenant.TenantService does.
type closerWiringTenantSvc struct {
	tenant.TenantServicer
	closer tenant.TenantRuntimeCloser
}

func (s *closerWiringTenantSvc) SetTenantRuntimeCloser(c tenant.TenantRuntimeCloser) { s.closer = c }
func (s *closerWiringTenantSvc) TenantRuntimeCloser() tenant.TenantRuntimeCloser     { return s.closer }

// recordingRuntimeCloser records the tenants it closed into calls.
type recordingRuntimeCloser struct {
	calls *[]string
	err   error
}

func (c recordingRuntimeCloser) CloseTenantRuntime(_ context.Context, tenantID string) error {
	*c.calls = append(*c.calls, "runtime:"+tenantID)
	return c.err
}

// forgettingLogService records the tenants whose logs it forgot into calls.
type forgettingLogService struct {
	service.LogService
	calls *[]string
}

func (l forgettingLogService) ForgetTenantLogs(tenantID string) {
	*l.calls = append(*l.calls, "logs:"+tenantID)
}

// TestWireTenantLogsCloser_RunsThePriorCloserThenForgetsTheLogs proves
// DELETE TENANT's runtime closer keeps the closer wired before it and then
// evicts the tenant's log state, and that a failed prior close skips the
// eviction and is returned.
func TestWireTenantLogsCloser_RunsThePriorCloserThenForgetsTheLogs(t *testing.T) {
	var calls []string
	svc := &closerWiringTenantSvc{closer: recordingRuntimeCloser{calls: &calls}}
	wireTenantLogsCloser(svc, forgettingLogService{calls: &calls})
	if err := svc.closer.CloseTenantRuntime(context.Background(), "vault"); err != nil {
		t.Fatalf("CloseTenantRuntime: %v", err)
	}
	if want := []string{"runtime:vault", "logs:vault"}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}

	calls = nil
	failed := errors.New("host close failed")
	svc = &closerWiringTenantSvc{closer: recordingRuntimeCloser{calls: &calls, err: failed}}
	wireTenantLogsCloser(svc, forgettingLogService{calls: &calls})
	if err := svc.closer.CloseTenantRuntime(context.Background(), "vault"); !errors.Is(err, failed) {
		t.Fatalf("CloseTenantRuntime = %v, want the prior closer's error", err)
	}
	if want := []string{"runtime:vault"}; !slices.Equal(calls, want) {
		t.Fatalf("calls after a failed prior close = %v, want %v", calls, want)
	}

	// No prior closer: the eviction alone runs.
	calls = nil
	svc = &closerWiringTenantSvc{}
	wireTenantLogsCloser(svc, forgettingLogService{calls: &calls})
	if err := svc.closer.CloseTenantRuntime(context.Background(), "acme"); err != nil {
		t.Fatalf("CloseTenantRuntime without a prior closer: %v", err)
	}
	if want := []string{"logs:acme"}; !slices.Equal(calls, want) {
		t.Fatalf("calls without a prior closer = %v, want %v", calls, want)
	}

	// A log service without the eviction leaves the closer as it was.
	prior := recordingRuntimeCloser{calls: &calls}
	svc = &closerWiringTenantSvc{closer: prior}
	wireTenantLogsCloser(svc, nil)
	if _, ok := svc.closer.(recordingRuntimeCloser); !ok {
		t.Fatalf("closer = %T after wiring no log service, want the prior closer", svc.closer)
	}
}
