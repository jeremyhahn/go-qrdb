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

package embedded

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The time-series retention and metric-listing calls carry no context at the
// service interface, so the scoped adapter used to resolve them for the system
// tenant whatever the caller's: on a database-scoped transport, a tenant's
// TSGetRetention, TSSetRetention and TSListMetrics reached the system
// tenant's database of that name. The transport has the caller's context and
// resolves for its tenant.
func TestScopedTransport_TimeSeriesResolvesCallerTenant(t *testing.T) {
	reg := &recordingRegistry{}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(reg), WithTimeSeriesService(&mockTSService{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scopedTr, err := tr.WithDatabase("ledger")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	scoped := scopedTr.(*Transport)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	calls := map[string]func() error{
		"TSGetRetention": func() error {
			_, err := scoped.TSGetRetention(ctx, &transport.TSGetRetentionRequest{Metric: "cpu"})
			return err
		},
		"TSSetRetention": func() error {
			return scoped.TSSetRetention(ctx, &transport.TSSetRetentionRequest{
				Metric: "cpu", Policy: &transport.TSRetentionPolicy{DurationNs: int64(time.Hour)},
			})
		},
		"TSListMetrics": func() error {
			_, err := scoped.TSListMetrics(ctx, &transport.TSListMetricsRequest{Prefix: "c"})
			return err
		},
	}
	for name, call := range calls {
		before := len(reg.asked)
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := reg.asked[before:]; len(got) != 1 || got[0] != "acme/ledger" {
			t.Errorf("%s resolved %v, want [acme/ledger]", name, got)
		}
	}
}

// On an unscoped transport the calls reach the transport's own service, as
// before, and nothing is resolved.
func TestUnscopedTransport_TimeSeriesUsesOwnService(t *testing.T) {
	reg := &recordingRegistry{}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(reg), WithTimeSeriesService(&mockTSService{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	if _, err := tr.TSListMetrics(ctx, &transport.TSListMetricsRequest{Prefix: "c"}); err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if _, err := tr.TSGetRetention(ctx, &transport.TSGetRetentionRequest{Metric: "cpu"}); err != nil {
		t.Fatalf("TSGetRetention: %v", err)
	}
	if len(reg.asked) != 0 {
		t.Fatalf("an unscoped transport resolved %v", reg.asked)
	}
}
