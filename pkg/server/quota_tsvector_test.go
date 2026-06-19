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

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// M-1 follow-up — storage quota enforcement on the TimeSeries and Vector
// engines, which write independent tenant payload (data points, embeddings +
// metadata) outside the wrapped KV/MVCC servicers. Before the fix applyQuota
// returned these servicers unwrapped, so a tenant at its MaxStorageBytes could
// exceed it via time-series ingests or vector upserts.
// ---------------------------------------------------------------------------

// countingTS is an inner TimeSeriesServicer that records whether each surface
// was reached, so a quota rejection (which must short-circuit before the inner
// call) is observable. The embedded nil interface panics if an un-overridden
// method is invoked, ensuring tests only exercise the wired surface.
type countingTS struct {
	service.TimeSeriesServicer
	writes  int
	queries int
	err     error
}

func (m *countingTS) Write(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
	m.writes++
	return m.err
}

func (m *countingTS) WriteBatch(_ context.Context, _ []timeseries.DataPoint) error {
	m.writes++
	return m.err
}

func (m *countingTS) WritePoint(_ context.Context, _ *timeseries.DataPoint) error {
	m.writes++
	return m.err
}

func (m *countingTS) Query(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	m.queries++
	return nil, nil
}

func TestQuotaTimeSeriesService_EnforcesAndAccounts(t *testing.T) {
	inner := &countingTS{}
	acct, _ := newTestAccountant(t, nil)
	// "cpu" (3) + 16 fixed + tag "h"(1)+"1"(1) = 21 bytes per point.
	q := newQuotaTimeSeriesService(inner, acct, "t1", 50)
	ctx := context.Background()
	ts := time.Unix(1, 0)

	if err := q.Write(ctx, "cpu", 0.5, ts, map[string]string{"h": "1"}); err != nil {
		t.Fatalf("Write within limit: %v", err)
	}
	if acct.Usage("t1") != 21 {
		t.Fatalf("usage = %d, want 21", acct.Usage("t1"))
	}
	if inner.writes != 1 {
		t.Fatalf("inner writes = %d, want 1", inner.writes)
	}

	// Two more points (42 bytes) would push usage to 63 > 50: rejected before
	// reaching the inner store.
	err := q.WriteBatch(ctx, []timeseries.DataPoint{
		{Metric: "cpu", Timestamp: ts, Tags: map[string]string{"h": "1"}},
		{Metric: "cpu", Timestamp: ts, Tags: map[string]string{"h": "1"}},
	})
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("over-limit WriteBatch err = %v, want *quota.ExceededError", err)
	}
	if inner.writes != 1 {
		t.Fatalf("rejected batch must not reach inner store (writes=%d)", inner.writes)
	}
	if acct.Usage("t1") != 21 {
		t.Fatalf("usage after rejection = %d, want 21", acct.Usage("t1"))
	}
}

func TestQuotaTimeSeriesService_WritePointEnforces(t *testing.T) {
	inner := &countingTS{}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaTimeSeriesService(inner, acct, "t1", 10)
	ctx := context.Background()

	// "metric" (6) + 16 = 22 > 10: rejected.
	err := q.WritePoint(ctx, &timeseries.DataPoint{Metric: "metric", Timestamp: time.Unix(1, 0)})
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("over-limit WritePoint err = %v, want *quota.ExceededError", err)
	}
	if inner.writes != 0 {
		t.Fatalf("rejected point must not reach inner store (writes=%d)", inner.writes)
	}

	// A nil point carries no payload and passes straight through.
	if err := q.WritePoint(ctx, nil); err != nil {
		t.Fatalf("nil WritePoint: %v", err)
	}
	if inner.writes != 1 {
		t.Fatalf("nil point should forward (writes=%d)", inner.writes)
	}
}

func TestQuotaTimeSeriesService_InnerErrorReleases(t *testing.T) {
	inner := &countingTS{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaTimeSeriesService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := q.Write(ctx, "cpu", 1, time.Unix(1, 0), nil); !errors.Is(err, errBoom) {
		t.Fatalf("Write err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after failed write = %d, want 0 (released)", acct.Usage("t1"))
	}
}

func TestQuotaTimeSeriesService_ReadPassthrough(t *testing.T) {
	inner := &countingTS{}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaTimeSeriesService(inner, acct, "t1", 100)
	ctx := context.Background()

	if _, err := q.Query(ctx, "cpu", time.Unix(0, 0), time.Unix(10, 0)); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if inner.queries != 1 {
		t.Fatalf("inner queries = %d, want 1", inner.queries)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("read path must not account (usage=%d)", acct.Usage("t1"))
	}
}

// countingVec is an inner VectorServicer that records reach-through on the
// mutating and read surfaces the quota wrapper distinguishes.
type countingVec struct {
	service.VectorServicer
	upserts int
	gets    int
	err     error
}

func (m *countingVec) UpsertVector(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	m.upserts++
	return &service.UpsertVectorResponse{}, m.err
}

func (m *countingVec) BatchUpsert(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	m.upserts++
	return &service.BatchUpsertResponse{}, m.err
}

func (m *countingVec) UpdateMetadata(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	m.upserts++
	return &service.UpdateVectorMetadataResponse{}, m.err
}

func (m *countingVec) GetVector(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	m.gets++
	return &service.GetVectorResponse{}, nil
}

func TestQuotaVectorService_EnforcesAndAccounts(t *testing.T) {
	inner := &countingVec{}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaVectorService(inner, acct, "t1", 100)
	ctx := context.Background()

	// 8 floats * 4 = 32 bytes, no metadata.
	if _, err := q.UpsertVector(ctx, &service.UpsertVectorRequest{Vector: make([]float32, 8)}); err != nil {
		t.Fatalf("UpsertVector within limit: %v", err)
	}
	if acct.Usage("t1") != 32 {
		t.Fatalf("usage = %d, want 32", acct.Usage("t1"))
	}
	if inner.upserts != 1 {
		t.Fatalf("inner upserts = %d, want 1", inner.upserts)
	}

	// 20 floats * 4 = 80 bytes would push usage to 112 > 100: rejected.
	_, err := q.UpsertVector(ctx, &service.UpsertVectorRequest{Vector: make([]float32, 20)})
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("over-limit UpsertVector err = %v, want *quota.ExceededError", err)
	}
	if inner.upserts != 1 {
		t.Fatalf("rejected upsert must not reach inner store (upserts=%d)", inner.upserts)
	}
	if acct.Usage("t1") != 32 {
		t.Fatalf("usage after rejection = %d, want 32", acct.Usage("t1"))
	}
}

func TestQuotaVectorService_BatchAndMetadataEnforce(t *testing.T) {
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaVectorService(&countingVec{}, acct, "t1", 50)
	ctx := context.Background()

	// Two vectors of 8 floats (32 bytes each) = 64 > 50: rejected.
	_, err := q.BatchUpsert(ctx, &service.BatchUpsertRequest{Vectors: []*service.VectorData{
		{Vector: make([]float32, 8)},
		{Vector: make([]float32, 8)},
	}})
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("over-limit BatchUpsert err = %v, want *quota.ExceededError", err)
	}

	// Metadata-only update: key "k"(1) + string value "value"(5) = 6 bytes,
	// under the limit, so it is admitted and accounted.
	if _, err := q.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{
		Metadata: map[string]interface{}{"k": "value"},
	}); err != nil {
		t.Fatalf("UpdateMetadata within limit: %v", err)
	}
	if acct.Usage("t1") != 6 {
		t.Fatalf("usage after metadata update = %d, want 6", acct.Usage("t1"))
	}
}

func TestQuotaVectorService_InnerErrorReleases(t *testing.T) {
	inner := &countingVec{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaVectorService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if _, err := q.UpsertVector(ctx, &service.UpsertVectorRequest{Vector: make([]float32, 10)}); !errors.Is(err, errBoom) {
		t.Fatalf("UpsertVector err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after failed upsert = %d, want 0 (released)", acct.Usage("t1"))
	}
}

func TestQuotaVectorService_ReadPassthrough(t *testing.T) {
	inner := &countingVec{}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaVectorService(inner, acct, "t1", 100)
	ctx := context.Background()

	if _, err := q.GetVector(ctx, &service.GetVectorRequest{ID: 1}); err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if inner.gets != 1 {
		t.Fatalf("inner gets = %d, want 1", inner.gets)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("read path must not account (usage=%d)", acct.Usage("t1"))
	}
}

// TestApplyQuota_TimeSeriesVectorGating proves applyQuota installs the
// TimeSeries/Vector quota wrappers only for a non-system tenant with a positive
// MaxStorageBytes limit, and leaves them as plain passthroughs (the original
// servicer instance, never touching the accountant) for the system tenant and
// for unlimited tenants.
func TestApplyQuota_TimeSeriesVectorGating(t *testing.T) {
	acct, _ := newTestAccountant(t, nil)
	dbReg := NewDatabaseServiceRegistry("default")
	reg := NewRESTServiceRegistry(dbReg, nil, tenant.SystemTenantID,
		withQuotaAccountant(acct),
	).(*restServiceRegistry)

	tsInner := &countingTS{}
	vecInner := &countingVec{}
	base := &DatabaseServices{TimeSeries: tsInner, Vector: vecInner}

	// Limited, non-system tenant: both engines are wrapped.
	limited := &tenant.TenantInfo{ID: "acme", Quotas: &quota.Quotas{MaxStorageBytes: 100}}
	out := reg.applyQuota(base, "acme", limited)
	if _, ok := out.TimeSeries.(*quotaTimeSeriesService); !ok {
		t.Fatalf("limited tenant: TimeSeries = %T, want *quotaTimeSeriesService", out.TimeSeries)
	}
	if _, ok := out.Vector.(*quotaVectorService); !ok {
		t.Fatalf("limited tenant: Vector = %T, want *quotaVectorService", out.Vector)
	}

	// System tenant: passthrough, original instances returned.
	sys := reg.applyQuota(base, tenant.SystemTenantID, limited)
	if sys.TimeSeries != service.TimeSeriesServicer(tsInner) || sys.Vector != service.VectorServicer(vecInner) {
		t.Fatal("system tenant must not wrap TimeSeries/Vector")
	}

	// Unlimited tenant (MaxStorageBytes == 0): passthrough.
	unlimited := &tenant.TenantInfo{ID: "acme", Quotas: &quota.Quotas{MaxStorageBytes: 0}}
	un := reg.applyQuota(base, "acme", unlimited)
	if un.TimeSeries != service.TimeSeriesServicer(tsInner) || un.Vector != service.VectorServicer(vecInner) {
		t.Fatal("unlimited tenant must not wrap TimeSeries/Vector")
	}
}
