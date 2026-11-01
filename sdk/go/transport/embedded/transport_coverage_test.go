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
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Mock: CollectionServicer
// ===========================================================================

type mockCollectionService struct {
	createFn    func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropFn      func(ctx context.Context, req *service.DropCollectionRequest) error
	listFn      func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getInfoFn   func(ctx context.Context, database, name string) (*service.CollectionInfo, error)
	insertFn    func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getDocFn    func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateDocFn func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteDocFn func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocsFn  func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

func (m *mockCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &service.CollectionInfo{Name: req.Name, Database: req.Database, CreatedAt: time.Unix(1700000000, 0).UTC()}, nil
}

func (m *mockCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	if m.dropFn != nil {
		return m.dropFn(ctx, req)
	}
	return nil
}

func (m *mockCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListCollectionsResponse{
		Collections: []service.CollectionInfo{
			{Name: "users", Database: req.Database, CreatedAt: time.Unix(1700000000, 0).UTC()},
		},
	}, nil
}

func (m *mockCollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	if m.getInfoFn != nil {
		return m.getInfoFn(ctx, database, name)
	}
	return &service.CollectionInfo{Name: name, Database: database, CreatedAt: time.Unix(1700000000, 0).UTC()}, nil
}

func (m *mockCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertFn != nil {
		return m.insertFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 42}, nil
}

func (m *mockCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocFn != nil {
		return m.getDocFn(ctx, req)
	}
	return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"name": "alice"}}, nil
}

func (m *mockCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateDocFn != nil {
		return m.updateDocFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
}

// UpsertDocument reports an insert: no test that uses this double drives
// the guarded upsert, which is exercised against the real service.
func (m *mockCollectionService) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return &service.UpsertDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteDocFn != nil {
		return m.deleteDocFn(ctx, req)
	}
	return &service.DeleteDocumentResponse{Deleted: true}, nil
}

func (m *mockCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocsFn != nil {
		return m.listDocsFn(ctx, req)
	}
	return &service.ListDocumentsResponse{
		Documents:  []service.GetDocumentResponse{{ID: 1, Document: map[string]interface{}{"k": "v"}}},
		TotalCount: 1,
	}, nil
}

var _ service.CollectionServicer = (*mockCollectionService)(nil)

// ===========================================================================
// Mock: AuthFactorServicer
// ===========================================================================

type mockAuthFactorService struct {
	enrollFn    func(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

func (m *mockAuthFactorService) EnrollFactor(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, factorName, subject, params)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "enr-1",
		FactorName:   factorName,
		UserID:       subject,
		TenantID:     params.TenantID,
		Metadata:     map[string]string{"k": "v"},
		EnrolledAt:   time.Unix(1700000000, 0).UTC(),
	}, nil
}

func (m *mockAuthFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	if m.challengeFn != nil {
		return m.challengeFn(ctx, enrollmentID)
	}
	return &authfactors.Challenge{
		ChallengeID:  "ch-1",
		EnrollmentID: enrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Unix(1700001000, 0).UTC(),
		MethodData:   map[string]any{"method": "pkcs11"},
	}, nil
}

func (m *mockAuthFactorService) VerifyFactor(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, factorName, challengeID, response)
	}
	return nil
}

func (m *mockAuthFactorService) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if m.listFn != nil {
		return m.listFn(ctx, subject)
	}
	return []*authfactors.Enrollment{
		{EnrollmentID: "enr-1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Unix(1700000000, 0).UTC()},
	}, nil
}

func (m *mockAuthFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, enrollmentID)
	}
	return nil
}

var _ service.AuthFactorServicer = (*mockAuthFactorService)(nil)

// ===========================================================================
// Mock: CeremonyServicer + CeremonyCA stub
// ===========================================================================

type mockCeremonyService struct {
	initFn       func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	lastInitOpts *service.CeremonyInitializeOptions
	initOptsSeen bool
	claimShareFn func(ctx context.Context, username string) ([]byte, error)
	signCSRFn    func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
	statusFn     func(ctx context.Context) (*service.CeremonyStatusInfo, error)
}

func (m *mockCeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.lastInitOpts = opts
	m.initOptsSeen = true
	if m.initFn != nil {
		return m.initFn(ctx, ca)
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "pin",
		CACertPEM:  []byte("CA"),
		TLSCertPEM: []byte("TLS"),
		State:      "initialized",
		ShareCount: 3,
	}, nil
}

func (m *mockCeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("CERT")}, nil
}

func (m *mockCeremonyService) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin"}, nil
}

var _ service.CeremonyServicer = (*mockCeremonyService)(nil)

// Minimal CeremonyCA stub satisfying the interface.
type mockCeremonyCA struct{}

func (m *mockCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) CABundle() ([]byte, error) {
	return nil, errors.New("not used")
}

func (m *mockCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, errors.New("not used")
}

var _ ceremony.CeremonyCA = (*mockCeremonyCA)(nil)

// ===========================================================================
// Mock: LeaseServicer
// ===========================================================================

type mockLeaseService struct {
	grantFn     func(ctx context.Context, ttl int64) (*lease.Lease, error)
	revokeFn    func(ctx context.Context, id int64) error
	keepAliveFn func(ctx context.Context, id int64) (*lease.Lease, error)
	ttlFn       func(ctx context.Context, id int64) (*lease.Lease, error)
	leasesFn    func(ctx context.Context) ([]*lease.Lease, error)
}

func (m *mockLeaseService) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	if m.grantFn != nil {
		return m.grantFn(ctx, ttl)
	}
	return &lease.Lease{ID: 42, TTL: ttl, GrantedTTL: ttl, RemainingTTL: ttl}, nil
}

func (m *mockLeaseService) Revoke(ctx context.Context, id int64) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, id)
	}
	return nil
}

func (m *mockLeaseService) KeepAlive(ctx context.Context, id int64) (*lease.Lease, error) {
	if m.keepAliveFn != nil {
		return m.keepAliveFn(ctx, id)
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 60}, nil
}

func (m *mockLeaseService) TimeToLive(ctx context.Context, id int64) (*lease.Lease, error) {
	if m.ttlFn != nil {
		return m.ttlFn(ctx, id)
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 30}, nil
}

func (m *mockLeaseService) Leases(ctx context.Context) ([]*lease.Lease, error) {
	if m.leasesFn != nil {
		return m.leasesFn(ctx)
	}
	return []*lease.Lease{{ID: 1, TTL: 60}, {ID: 2, TTL: 120}}, nil
}

var _ lease.LeaseServicer = (*mockLeaseService)(nil)

// ===========================================================================
// Mock: WatchServicer
// ===========================================================================

// stubWatcher implements watch.Watcher for tests.
type stubWatcher struct {
	ch chan watch.WatchResponse
}

func newStubWatcher() *stubWatcher {
	w := &stubWatcher{ch: make(chan watch.WatchResponse, 4)}
	return w
}

func (s *stubWatcher) Events() <-chan watch.WatchResponse { return s.ch }
func (s *stubWatcher) WatchID() int64                     { return 1 }
func (s *stubWatcher) Close() error                       { return nil }
func (s *stubWatcher) Err() error                         { return nil }

type mockWatchService struct {
	watchFn       func(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
	watchPrefixFn func(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

func (m *mockWatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if m.watchFn != nil {
		return m.watchFn(ctx, key, opts...)
	}
	w := newStubWatcher()
	close(w.ch)
	return w, nil
}

func (m *mockWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if m.watchPrefixFn != nil {
		return m.watchPrefixFn(ctx, prefix, opts...)
	}
	w := newStubWatcher()
	close(w.ch)
	return w, nil
}

// WatchEntity / WatchEntityType are required by service.WatchServicer interface,
// but the embedded transport doesn't use them so they return errors.
func (m *mockWatchService) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, errors.New("not implemented")
}

func (m *mockWatchService) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, errors.New("not implemented")
}

var _ service.WatchServicer = (*mockWatchService)(nil)

// ===========================================================================
// Mock: TimeSeriesServicer
// ===========================================================================

type mockTSService struct {
	writeFn       func(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error
	writeBatchFn  func(ctx context.Context, points []timeseries.DataPoint) error
	queryFn       func(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	aggregateFn   func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error)
	writePointFn  func(ctx context.Context, point *timeseries.DataPoint) error
	queryRichFn   func(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error)
	deleteFn      func(ctx context.Context, req *timeseries.DeleteRequest) error
	getRetFn      func(metric string) (*timeseries.RetentionPolicy, error)
	setRetFn      func(metric string, policy *timeseries.RetentionPolicy) error
	applyRetFn    func(ctx context.Context, metric string, cutoff time.Time) error
	listMetricsFn func(prefix string, limit int) ([]string, error)
}

func (m *mockTSService) Write(ctx context.Context, metric string, value float64, ts time.Time, tags map[string]string) error {
	if m.writeFn != nil {
		return m.writeFn(ctx, metric, value, ts, tags)
	}
	return nil
}

func (m *mockTSService) WriteBatch(ctx context.Context, points []timeseries.DataPoint) error {
	if m.writeBatchFn != nil {
		return m.writeBatchFn(ctx, points)
	}
	return nil
}

func (m *mockTSService) Query(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, metric, start, end, opts...)
	}
	return []timeseries.DataPoint{{Metric: metric, Value: 1.0, Timestamp: start}}, nil
}

func (m *mockTSService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, _ consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return m.Query(ctx, metric, start, end, opts...)
}

func (m *mockTSService) Aggregate(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateFn != nil {
		return m.aggregateFn(ctx, metric, start, end, fn, interval)
	}
	return []timeseries.AggResult{{Timestamp: start, Value: 100.0, Count: 1}}, nil
}

func (m *mockTSService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return m.Aggregate(ctx, metric, start, end, fn, interval)
}

func (m *mockTSService) WritePoint(ctx context.Context, point *timeseries.DataPoint) error {
	if m.writePointFn != nil {
		return m.writePointFn(ctx, point)
	}
	return nil
}

func (m *mockTSService) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if m.queryRichFn != nil {
		return m.queryRichFn(ctx, req)
	}
	return &timeseries.QueryResult{Metric: req.Metric, Points: []*timeseries.DataPoint{{Metric: req.Metric, Value: 1.0, Timestamp: req.StartTime}}}, nil
}

func (m *mockTSService) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return m.QueryRich(ctx, req)
}

func (m *mockTSService) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, req)
	}
	return nil
}

func (m *mockTSService) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	if m.getRetFn != nil {
		return m.getRetFn(metric)
	}
	return &timeseries.RetentionPolicy{Duration: 24 * time.Hour}, nil
}

func (m *mockTSService) SetRetentionPolicy(_ context.Context, metric string, policy *timeseries.RetentionPolicy) error {
	if m.setRetFn != nil {
		return m.setRetFn(metric, policy)
	}
	return nil
}

func (m *mockTSService) ApplyRetention(ctx context.Context, metric string, cutoff time.Time) error {
	if m.applyRetFn != nil {
		return m.applyRetFn(ctx, metric, cutoff)
	}
	return nil
}

func (m *mockTSService) ListMetrics(prefix string, limit int) ([]string, error) {
	if m.listMetricsFn != nil {
		return m.listMetricsFn(prefix, limit)
	}
	return []string{"cpu.usage", "mem.free"}, nil
}

func (m *mockTSService) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, _ consistency.ReadOptions) ([]string, error) {
	return m.ListMetrics(prefix, limit)
}

var _ timeseries.TimeSeriesServicer = (*mockTSService)(nil)

// ===========================================================================
// TimeSeries tests
// ===========================================================================

func TestEmbeddedTS_Write(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	if err := tr.TSWrite(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.0, TimestampNs: 1700000000000000000}); err != nil {
		t.Fatalf("TSWrite: %v", err)
	}
}

func TestEmbeddedTS_Write_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_Write_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	err := tr.TSWrite(context.Background(), &transport.TSDataPoint{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_WriteBatch(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	if err := tr.TSWriteBatch(context.Background(), []transport.TSDataPoint{
		{Metric: "m", Value: 1.0, TimestampNs: 1700000000000000000},
	}); err != nil {
		t.Fatalf("TSWriteBatch: %v", err)
	}
}

func TestEmbeddedTS_WriteBatch_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TSWriteBatch(context.Background(), nil)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_WriteBatch_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	err := tr.TSWriteBatch(context.Background(), nil)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_Query(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:     "m",
		StartNs:    1700000000000000000,
		EndNs:      1700001000000000000,
		Tags:       []transport.TSTag{{Key: "host", Value: "a"}},
		Limit:      10,
		Descending: true,
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}
	if len(resp.Points) != 1 {
		t.Fatalf("got %d points", len(resp.Points))
	}
}

func TestEmbeddedTS_Query_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_Query_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_Query_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbeddedTS_Aggregate(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "m",
		StartNs:    1700000000000000000,
		EndNs:      1700001000000000000,
		Function:   transport.AggAvg,
		IntervalNs: int64(time.Minute),
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results", len(resp.Results))
	}
}

func TestEmbeddedTS_Aggregate_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_Aggregate_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_WritePoint(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	if err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{Metric: "m", Value: 1.0, TimestampNs: 1700000000000000000}); err != nil {
		t.Fatalf("TSWritePoint: %v", err)
	}
}

func TestEmbeddedTS_WritePoint_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_WritePoint_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_RichQuery(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "m",
		StartNs:     1700000000000000000,
		EndNs:       1700001000000000000,
		Aggregation: transport.AggAvg,
		IntervalNs:  int64(time.Minute),
		GroupBy:     []string{"host"},
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}
	if resp.Metric != "m" {
		t.Fatalf("Metric = %q", resp.Metric)
	}
}

func TestEmbeddedTS_RichQuery_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_RichQuery_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_DeleteSeries(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{Metric: "m"})
	if err != nil {
		t.Fatalf("TSDeleteSeries: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
}

func TestEmbeddedTS_DeleteSeries_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_DeleteSeries_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedTS_GetRetention(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "m"})
	if err != nil {
		t.Fatalf("TSGetRetention: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
}

func TestEmbeddedTS_GetRetention_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_SetRetention(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	if err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{
		Metric: "m",
		Policy: &transport.TSRetentionPolicy{DurationNs: int64(24 * time.Hour)},
	}); err != nil {
		t.Fatalf("TSSetRetention: %v", err)
	}
}

func TestEmbeddedTS_SetRetention_NilService(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.TSSetRetention(context.Background(), &transport.TSSetRetentionRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTS_ListMetrics(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	resp, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{Prefix: "cpu", Limit: 10})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}
	if len(resp.Metrics) != 2 {
		t.Fatalf("got %d metrics", len(resp.Metrics))
	}
}

func TestEmbeddedTS_ListMetrics_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

// ===========================================================================
// Mock: BatchServicer (for ImportFile only)
// ===========================================================================

// ===========================================================================
// ImportFile tests via existing BatchServicer interface (use a fully-typed mock).
// ===========================================================================

// fullBatchService satisfies service.BatchServicer with a configurable Import.
type fullBatchService struct {
	importFn func(ctx context.Context, r io.Reader, opts service.ImportOptions) (*service.ImportResult, error)

	// sessions backs ImportBegin/ImportAppend/ImportCommit/ImportAbort: a
	// minimal real session (not just stubs) so TestEmbeddedImportFile_*
	// exercises the SAME importFn hook ImportFile's chunked session now
	// drives through ImportCommit, exactly as it exercised Import
	// directly before. seq is deliberately not validated here -- that
	// behavior belongs to *service.BatchService's own tests
	// (pkg/service/batch_import_session_test.go), not this double.
	sessions map[string]*fullBatchSession
}

// fullBatchSession is one in-memory chunked-import session for
// fullBatchService.
type fullBatchSession struct {
	opts service.ImportOptions
	buf  bytes.Buffer
}

func (b *fullBatchService) Execute(_ context.Context, _ []service.Op) (*service.BatchResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) (*service.BatchResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, errors.New("not implemented")
}

func (b *fullBatchService) Import(ctx context.Context, r io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
	if b.importFn != nil {
		return b.importFn(ctx, r, opts)
	}
	return &service.ImportResult{Total: 1, Succeeded: 1}, nil
}

func (b *fullBatchService) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	if b.sessions == nil {
		b.sessions = make(map[string]*fullBatchSession)
	}
	id := strconv.Itoa(len(b.sessions))
	b.sessions[id] = &fullBatchSession{opts: opts}
	return id, nil
}

func (b *fullBatchService) ImportAppend(_ context.Context, sessionID string, _ uint64, chunk []byte) error {
	sess, ok := b.sessions[sessionID]
	if !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	sess.buf.Write(chunk)
	return nil
}

func (b *fullBatchService) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	sess, ok := b.sessions[sessionID]
	if !ok {
		return nil, &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	delete(b.sessions, sessionID)
	return b.Import(ctx, bytes.NewReader(sess.buf.Bytes()), sess.opts)
}

func (b *fullBatchService) ImportAbort(_ context.Context, sessionID string) error {
	if _, ok := b.sessions[sessionID]; !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	delete(b.sessions, sessionID)
	return nil
}

// SetImportKeyGuard satisfies service.ImportKeyGuardSetter as a no-op:
// this double tests ImportFile's wire-level plumbing (the transport's
// mapping between transport.ImportResult and service.ImportResult), never
// the hard-confinement feature, and its inputs below ("k,v\n1,2", "")
// never name the confined key -- so declaring it here (rather than
// leaving NewHardConfinedBatch to refuse Import outright for a
// BatchServicer with no confinement of its own) is a deliberate,
// visible opt-out for this one concern, not an accidental one.
func (b *fullBatchService) SetImportKeyGuard(func(string) error) {}

// ===========================================================================
// ImportFile tests
// ===========================================================================

func TestEmbeddedImportFile_Success(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader("k,v\n1,2"), transport.ImportOptions{Format: "csv"})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("Total = %d", result.Total)
	}
}

func TestEmbeddedImportFile_NilResult(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, nil
		},
	}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader(""), transport.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
}

func TestEmbeddedImportFile_WithErrors(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{
				Total:     3,
				Succeeded: 1,
				Failed:    2,
				Errors: []service.ImportError{
					{Row: 2, Key: "k1", Message: "duplicate"},
					{Row: 3, Key: "k2", Message: "invalid"},
				},
			}, nil
		},
	}))
	result, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader(""), transport.ImportOptions{})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Failed != 2 || len(result.Errors) != 2 {
		t.Fatalf("unexpected: %+v", result)
	}
	if result.Errors[0].Row != 2 || result.Errors[0].Message != "duplicate" {
		t.Fatalf("error[0] = %+v", result.Errors[0])
	}
}

func TestEmbeddedImportFile_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedImportFile_Closed(t *testing.T) {
	tr := closedExtTransport(t, WithBatchService(&fullBatchService{}))
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedImportFile_ServiceError(t *testing.T) {
	tr := newExtTransport(t, WithBatchService(&fullBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, errSentinel
		},
	}))
	_, err := tr.ImportFile(context.Background(), "x", strings.NewReader(""), transport.ImportOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// Error type coverage: BarrierSealedError
// ===========================================================================

func TestEmbeddedServerErrors_BarrierSealedError(t *testing.T) {
	// nil err returns sentinel
	e := &BarrierSealedError{}
	if e.Error() == "" {
		t.Fatal("empty error")
	}
	if e.Unwrap() == nil {
		t.Fatal("expected ErrSealed unwrap fallback")
	}
	// with err
	wrapped := errors.New("inner")
	e2 := &BarrierSealedError{Err: wrapped}
	if !strings.Contains(e2.Error(), "inner") {
		t.Fatalf("Error() = %q", e2.Error())
	}
	if !errors.Is(e2, wrapped) {
		t.Fatal("Unwrap")
	}
}

// ===========================================================================
// Coverage of errors.go alternative paths
// ===========================================================================

func TestEmbeddedNilServiceError_Error(t *testing.T) {
	e := &NilServiceError{Service: "FooServicer"}
	if !strings.Contains(e.Error(), "FooServicer") {
		t.Fatalf("Error() = %q", e.Error())
	}
}

// ===========================================================================
// translateBackupError coverage - exercise each branch
// ===========================================================================

func TestEmbeddedTranslateBackupError(t *testing.T) {
	t.Run("Nil", func(t *testing.T) {
		if translateBackupError(nil) != nil {
			t.Fatal("expected nil")
		}
	})
	t.Run("NotFound", func(t *testing.T) {
		got := translateBackupError(&service.BackupNotFoundError{ID: "b1"})
		var want *transport.BackupNotFoundError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EmptyDestination", func(t *testing.T) {
		got := translateBackupError(&service.BackupEmptyDestinationError{})
		var want *transport.BackupEmptyDestinationError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EmptyID", func(t *testing.T) {
		got := translateBackupError(&service.BackupEmptyIDError{})
		var want *transport.BackupEmptyIDError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("CreateError", func(t *testing.T) {
		inner := errors.New("inner")
		got := translateBackupError(&service.BackupCreateError{Err: inner})
		var want *transport.BackupCreateError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("RestoreError", func(t *testing.T) {
		inner := errors.New("inner")
		got := translateBackupError(&service.BackupRestoreError{BackupID: "b1", Err: inner})
		var want *transport.BackupRestoreError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("ChecksumMismatch", func(t *testing.T) {
		got := translateBackupError(&service.BackupChecksumMismatchError{Expected: "a", Actual: "b"})
		var want *transport.BackupChecksumMismatchError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EncryptionKeyRequired", func(t *testing.T) {
		got := translateBackupError(&service.BackupEncryptionKeyRequiredError{})
		var want *transport.BackupCreateError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("DecryptionFailed", func(t *testing.T) {
		got := translateBackupError(&service.BackupDecryptionFailedError{BackupID: "b1", Err: errors.New("wrong key")})
		var want *transport.BackupDecryptionFailedError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
		if want.BackupID != "b1" {
			t.Fatalf("BackupID = %q, want %q", want.BackupID, "b1")
		}
	})
	t.Run("Passthrough", func(t *testing.T) {
		want := errors.New("other")
		got := translateBackupError(want)
		if !errors.Is(got, want) {
			t.Fatalf("expected passthrough, got %v", got)
		}
	})
}
