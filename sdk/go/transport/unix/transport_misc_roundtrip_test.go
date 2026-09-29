// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Round-trip tests for the Barrier, AuthFactor, TimeSeries, and Watch
// services over the Unix domain socket transport. These services pull down
// the long tail of partially covered code paths in the Unix transport, each
// using its own stubbed servicer and the shared startAuthServer harness.

package unix

import (
	"context"
	"errors"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockBarrierSvc — service.BarrierServicer stub
// ---------------------------------------------------------------------------

type mockBarrierSvc struct {
	service.BarrierServicer
	initFn           func(ctx context.Context, secret string) error
	unsealFn         func(ctx context.Context, secret []byte) error
	sealFn           func(ctx context.Context) error
	statusFn         func(ctx context.Context) (*service.BarrierStatusInfo, error)
	shamirInitFn     func(ctx context.Context, threshold, total int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error)
	recoveryGenFn    func(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error)
	recoveryDeleteFn func(ctx context.Context) error
	hasRecoveryFn    func(ctx context.Context) (bool, error)
}

func (m *mockBarrierSvc) BarrierInitialize(ctx context.Context, secret []byte) error {
	if m.initFn != nil {
		return m.initFn(ctx, string(secret))
	}
	return nil
}

func (m *mockBarrierSvc) BarrierUnseal(ctx context.Context, secret []byte) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierSvc) BarrierSeal(ctx context.Context) error {
	if m.sealFn != nil {
		return m.sealFn(ctx)
	}
	return nil
}

func (m *mockBarrierSvc) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.BarrierStatusInfo{
		Sealed:        false,
		Strategy:      "shamir",
		InitializedAt: time.Unix(1_700_000_000, 0),
	}, nil
}

func (m *mockBarrierSvc) BarrierInitializeShamir(ctx context.Context, threshold, total int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	if m.shamirInitFn != nil {
		return m.shamirInitFn(ctx, threshold, total, policy)
	}
	return &service.ShamirInitInfo{ShareCount: total, Threshold: threshold, TotalShares: total}, nil
}

func (m *mockBarrierSvc) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	if m.recoveryGenFn != nil {
		return m.recoveryGenFn(ctx, threshold, policy)
	}
	return &service.RecoveryKeysInfo{ShareCount: threshold + 1, Threshold: threshold, Total: threshold + 1}, nil
}

func (m *mockBarrierSvc) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.recoveryDeleteFn != nil {
		return m.recoveryDeleteFn(ctx)
	}
	return nil
}

func (m *mockBarrierSvc) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryFn != nil {
		return m.hasRecoveryFn(ctx)
	}
	return true, nil
}

var _ service.BarrierServicer = (*mockBarrierSvc)(nil)

// ---------------------------------------------------------------------------
// Barrier round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_BarrierInitialize(t *testing.T) {
	svc := &mockBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()

	if err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret: []byte("master-secret"),
	}); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
}

func TestUnixRT_BarrierUnseal(t *testing.T) {
	svc := &mockBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()

	if err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{
		Secret: []byte("master-secret"),
	}); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
}

func TestUnixRT_BarrierSeal(t *testing.T) {
	svc := &mockBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()

	if err := tr.BarrierSeal(context.Background()); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
}

func TestUnixRT_BarrierStatus(t *testing.T) {
	svc := &mockBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()

	resp, err := tr.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if resp.Strategy != "shamir" {
		t.Errorf("got Strategy=%q, want shamir", resp.Strategy)
	}
}

func TestUnixRT_BarrierInitializeShamir(t *testing.T) {
	svc := &mockBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()

	resp, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Threshold:   2,
		TotalShares: 3,
	})
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	if resp.ShareCount != 3 || resp.Threshold != 2 {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_BarrierDeleteRecoveryKeys(t *testing.T) {
	svc := &mockBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()

	if err := tr.BarrierDeleteRecoveryKeys(context.Background()); err != nil {
		t.Fatalf("BarrierDeleteRecoveryKeys: %v", err)
	}
}

func TestUnixRT_BarrierHasRecoveryKeys(t *testing.T) {
	svc := &mockBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()

	resp, err := tr.BarrierHasRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", err)
	}
	if !resp.HasKeys {
		t.Error("expected HasKeys=true")
	}
}

func TestUnixRT_BarrierInitialize_ServiceError(t *testing.T) {
	svc := &mockBarrierSvc{
		initFn: func(_ context.Context, _ string) error { return errors.New("init failed") },
	}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()

	err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{Secret: []byte("x")})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// mockTimeSeriesSvc — timeseries.TimeSeriesServicer stub
// ---------------------------------------------------------------------------

type mockTimeSeriesSvc struct {
	writeFn       func(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error
	writeBatchFn  func(ctx context.Context, points []timeseries.DataPoint) error
	queryFn       func(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	aggregateFn   func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error)
	writePointFn  func(ctx context.Context, point *timeseries.DataPoint) error
	queryRichFn   func(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error)
	deleteFn      func(ctx context.Context, req *timeseries.DeleteRequest) error
	getRetFn      func(metric string) (*timeseries.RetentionPolicy, error)
	setRetFn      func(metric string, policy *timeseries.RetentionPolicy) error
	listMetricsFn func(prefix string, limit int) ([]string, error)
}

func (m *mockTimeSeriesSvc) Write(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error {
	if m.writeFn != nil {
		return m.writeFn(ctx, metric, value, ts, tags)
	}
	return nil
}

func (m *mockTimeSeriesSvc) WriteBatch(ctx context.Context, points []timeseries.DataPoint) error {
	if m.writeBatchFn != nil {
		return m.writeBatchFn(ctx, points)
	}
	return nil
}

func (m *mockTimeSeriesSvc) Query(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, metric, start, end, opts...)
	}
	return []timeseries.DataPoint{
		{Metric: metric, Value: 1.5, Timestamp: time.Unix(1_700_000_000, 0)},
		{Metric: metric, Value: 2.5, Timestamp: time.Unix(1_700_000_060, 0)},
	}, nil
}

func (m *mockTimeSeriesSvc) Aggregate(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateFn != nil {
		return m.aggregateFn(ctx, metric, start, end, fn, interval)
	}
	return []timeseries.AggResult{
		{Timestamp: start, Value: 10.0, Count: 5},
	}, nil
}

func (m *mockTimeSeriesSvc) WritePoint(ctx context.Context, point *timeseries.DataPoint) error {
	if m.writePointFn != nil {
		return m.writePointFn(ctx, point)
	}
	return nil
}

func (m *mockTimeSeriesSvc) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if m.queryRichFn != nil {
		return m.queryRichFn(ctx, req)
	}
	return &timeseries.QueryResult{
		Metric: req.Metric,
		Points: []*timeseries.DataPoint{
			{Metric: req.Metric, Value: 3.14, Timestamp: req.StartTime},
		},
	}, nil
}

func (m *mockTimeSeriesSvc) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return nil
}

func (m *mockTimeSeriesSvc) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	if m.getRetFn != nil {
		return m.getRetFn(metric)
	}
	return &timeseries.RetentionPolicy{
		Duration:    24 * time.Hour,
		Resolution:  time.Minute,
		Aggregation: timeseries.AggAvg,
	}, nil
}

func (m *mockTimeSeriesSvc) SetRetentionPolicy(_ context.Context, metric string, policy *timeseries.RetentionPolicy) error {
	if m.setRetFn != nil {
		return m.setRetFn(metric, policy)
	}
	return nil
}

func (m *mockTimeSeriesSvc) ListMetrics(prefix string, limit int) ([]string, error) {
	if m.listMetricsFn != nil {
		return m.listMetricsFn(prefix, limit)
	}
	return []string{"cpu.usage", "mem.usage"}, nil
}

func (m *mockTimeSeriesSvc) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, _ consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return m.Query(ctx, metric, start, end, opts...)
}

func (m *mockTimeSeriesSvc) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return m.Aggregate(ctx, metric, start, end, fn, interval)
}

func (m *mockTimeSeriesSvc) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return m.QueryRich(ctx, req)
}

func (m *mockTimeSeriesSvc) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, _ consistency.ReadOptions) ([]string, error) {
	return m.ListMetrics(prefix, limit)
}

var _ timeseries.TimeSeriesServicer = (*mockTimeSeriesSvc)(nil)

// ---------------------------------------------------------------------------
// TimeSeries round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_TSWrite(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{
		Metric:      "cpu.usage",
		Value:       0.75,
		TimestampNs: time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("TSWrite: %v", err)
	}
}

func TestUnixRT_TSWriteBatch(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	now := time.Now().UnixNano()
	err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "cpu.usage", Value: 0.5, TimestampNs: now},
		{Metric: "cpu.usage", Value: 0.6, TimestampNs: now + 1},
	})
	if err != nil {
		t.Fatalf("TSWriteBatch: %v", err)
	}
}

func TestUnixRT_TSQuery(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu.usage",
		StartNs: time.Unix(1_700_000_000, 0).UnixNano(),
		EndNs:   time.Unix(1_700_000_120, 0).UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if len(resp.Points) != 2 {
		t.Errorf("got %d points, want 2", len(resp.Points))
	}
}

func TestUnixRT_TSAggregate(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu.usage",
		StartNs:    time.Unix(1_700_000_000, 0).UnixNano(),
		EndNs:      time.Unix(1_700_000_120, 0).UnixNano(),
		Function:   transport.AggAvg,
		IntervalNs: int64(time.Minute),
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Errorf("got %d results, want 1", len(resp.Results))
	}
}

func TestUnixRT_TSWritePoint(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	if err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric: "ram.used",
		Value:  42,
	}); err != nil {
		t.Fatalf("TSWritePoint: %v", err)
	}
}

func TestUnixRT_TSRichQuery(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu.usage",
		StartNs: time.Unix(1_700_000_000, 0).UnixNano(),
		EndNs:   time.Unix(1_700_000_120, 0).UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp.Metric != "cpu.usage" {
		t.Errorf("got Metric=%q, want cpu.usage", resp.Metric)
	}
}

func TestUnixRT_TSDeleteSeries(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	if _, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric: "cpu.usage",
	}); err != nil {
		t.Fatalf("TSDeleteSeries: %v", err)
	}
}

func TestUnixRT_TSGetRetention(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("TSGetRetention: %v", err)
	}
	if resp.Policy == nil || resp.Policy.Aggregation != transport.AggAvg {
		t.Errorf("got %+v", resp.Policy)
	}
}

func TestUnixRT_TSSetRetention(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
		Policy: &transport.TSRetentionPolicy{
			DurationNs:  int64(24 * time.Hour),
			Aggregation: transport.AggAvg,
		},
	})
	if err != nil {
		t.Fatalf("TSSetRetention: %v", err)
	}
}

func TestUnixRT_TSListMetrics(t *testing.T) {
	svc := &mockTimeSeriesSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTimeSeries(svc))
	defer cleanup()

	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "cpu",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if len(resp.Metrics) != 2 {
		t.Errorf("got %d, want 2", len(resp.Metrics))
	}
}

// ---------------------------------------------------------------------------
// mockWatchSvc — service.WatchServicer stub
// ---------------------------------------------------------------------------
//
// We provide a Watcher backed by a buffered channel that the test pre-fills
// then closes, allowing the streaming client to drain a deterministic set of
// events.

type mockWatcher struct {
	events chan watch.WatchResponse
	id     int64
}

func newMockWatcher(id int64, buf int) *mockWatcher {
	return &mockWatcher{events: make(chan watch.WatchResponse, buf), id: id}
}

func (m *mockWatcher) Events() <-chan watch.WatchResponse { return m.events }
func (m *mockWatcher) WatchID() int64                     { return m.id }
func (m *mockWatcher) Close() error                       { return nil }
func (m *mockWatcher) Err() error                         { return nil }

type mockWatchSvc struct {
	watcher *mockWatcher
	err     error
}

func (m *mockWatchSvc) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.watcher, nil
}

func (m *mockWatchSvc) WatchPrefix(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.watcher, nil
}

func (m *mockWatchSvc) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

func (m *mockWatchSvc) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

var _ service.WatchServicer = (*mockWatchSvc)(nil)

// ---------------------------------------------------------------------------
// Watch round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_Watch(t *testing.T) {
	w := newMockWatcher(1, 4)
	w.events <- watch.WatchResponse{
		WatchID: 1,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("k1"), Value: []byte("v1"), ModRevision: 5}},
		},
		Revision: 5,
	}
	close(w.events)

	svc := &mockWatchSvc{watcher: w}
	tr, cleanup := startAuthServer(t, unixserver.WithWatch(svc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "k1", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	resp, ok := <-ch
	if !ok {
		t.Fatal("expected event on channel")
	}
	if resp.WatchID != 1 || len(resp.Events) != 1 {
		t.Errorf("got %+v", resp)
	}
	if resp.Events[0].Type != transport.EventPut {
		t.Errorf("got Type=%v, want EventPut", resp.Events[0].Type)
	}
}

func TestUnixRT_WatchPrefix(t *testing.T) {
	w := newMockWatcher(2, 4)
	w.events <- watch.WatchResponse{
		WatchID: 2,
		Events: []*watch.Event{
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte("prefix/k1")}},
		},
		Revision: 7,
	}
	close(w.events)

	svc := &mockWatchSvc{watcher: w}
	tr, cleanup := startAuthServer(t, unixserver.WithWatch(svc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := tr.WatchPrefix(ctx, "prefix/", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	resp, ok := <-ch
	if !ok {
		t.Fatal("expected event on channel")
	}
	if resp.WatchID != 2 || len(resp.Events) != 1 {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_Watch_Canceled(t *testing.T) {
	w := newMockWatcher(3, 4)
	w.events <- watch.WatchResponse{
		WatchID:      3,
		Canceled:     true,
		CancelReason: "compacted",
	}
	// No close — server returns when Canceled=true.

	svc := &mockWatchSvc{watcher: w}
	tr, cleanup := startAuthServer(t, unixserver.WithWatch(svc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	resp, ok := <-ch
	if !ok {
		t.Fatal("expected cancellation event")
	}
	if !resp.Canceled || resp.CancelReason != "compacted" {
		t.Errorf("got %+v, want canceled=true", resp)
	}
}

// ---------------------------------------------------------------------------
// mockAuthFactorSvc — service.AuthFactorServicer stub
// ---------------------------------------------------------------------------

type mockAuthFactorSvc struct {
	enrollFn    func(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

func (m *mockAuthFactorSvc) EnrollFactor(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, factorName, subject, params)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "enroll-1",
		FactorName:   factorName,
		UserID:       subject,
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

func (m *mockAuthFactorSvc) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	if m.challengeFn != nil {
		return m.challengeFn(ctx, enrollmentID)
	}
	return &authfactors.Challenge{
		ChallengeID:  "ch-1",
		EnrollmentID: enrollmentID,
		Nonce:        []byte("nonce-bytes"),
		ExpiresAt:    time.Now().Add(60 * time.Second),
	}, nil
}

func (m *mockAuthFactorSvc) VerifyFactor(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, factorName, challengeID, response)
	}
	return nil
}

func (m *mockAuthFactorSvc) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if m.listFn != nil {
		return m.listFn(ctx, subject)
	}
	return []*authfactors.Enrollment{
		{EnrollmentID: "enroll-1", FactorName: "piv", UserID: subject, EnrolledAt: time.Now().UTC()},
	}, nil
}

func (m *mockAuthFactorSvc) RevokeFactor(ctx context.Context, enrollmentID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, enrollmentID)
	}
	return nil
}

var _ service.AuthFactorServicer = (*mockAuthFactorSvc)(nil)

// ---------------------------------------------------------------------------
// AuthFactor round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_EnrollFactor(t *testing.T) {
	svc := &mockAuthFactorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithAuthFactorService(svc))
	defer cleanup()

	resp, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{
		FactorName: "piv",
		Subject:    "user-1",
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if resp.EnrollmentID != "enroll-1" || resp.FactorName != "piv" {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_ChallengeFactor(t *testing.T) {
	svc := &mockAuthFactorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithAuthFactorService(svc))
	defer cleanup()

	resp, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{
		EnrollmentID: "enroll-1",
	})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if resp.ChallengeID != "ch-1" {
		t.Errorf("got ChallengeID=%q, want ch-1", resp.ChallengeID)
	}
}

func TestUnixRT_VerifyFactor(t *testing.T) {
	called := false
	svc := &mockAuthFactorSvc{
		verifyFn: func(_ context.Context, factorName, challengeID string, response *authfactors.Response) error {
			called = true
			if factorName != "piv" || challengeID != "ch-1" {
				t.Errorf("server got factor=%q challenge=%q", factorName, challengeID)
			}
			if string(response.Value) != "signature" {
				t.Errorf("server got value=%q, want signature", response.Value)
			}
			return nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithAuthFactorService(svc))
	defer cleanup()

	// A successful verification is nil on the client too. This test used to
	// discard the result because mapGRPCError turned a nil gRPC error into a
	// RequestError, so every successful Unix VerifyFactor reported failure.
	if err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{
		FactorName:  "piv",
		ChallengeID: "ch-1",
		Value:       []byte("signature"),
	}); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
	if !called {
		t.Fatal("expected service VerifyFactor to be invoked")
	}
}

func TestUnixRT_ListFactors(t *testing.T) {
	svc := &mockAuthFactorSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithAuthFactorService(svc))
	defer cleanup()

	resp, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{
		Subject: "user-1",
	})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if len(resp.Enrollments) != 1 {
		t.Errorf("got %d enrollments, want 1", len(resp.Enrollments))
	}
}

func TestUnixRT_RevokeFactor(t *testing.T) {
	called := false
	svc := &mockAuthFactorSvc{
		revokeFn: func(_ context.Context, enrollmentID string) error {
			called = true
			if enrollmentID != "enroll-1" {
				t.Errorf("server got enrollmentID=%q, want enroll-1", enrollmentID)
			}
			return nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithAuthFactorService(svc))
	defer cleanup()

	if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{
		EnrollmentID: "enroll-1",
	}); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
	if !called {
		t.Fatal("expected service RevokeFactor to be invoked")
	}
}

func TestUnixRT_EnrollFactor_ServiceError(t *testing.T) {
	svc := &mockAuthFactorSvc{
		enrollFn: func(_ context.Context, _, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
			return nil, errors.New("enroll failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithAuthFactorService(svc))
	defer cleanup()

	_, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{FactorName: "piv"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}
