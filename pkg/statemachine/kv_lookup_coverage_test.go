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

//go:build codec_json && raft

package statemachine

import (
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// lookupIndex: nil-guard branches for pointer and value variants
// ---------------------------------------------------------------------------

func TestKVLookup_IndexQuery_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(index.IndexQueryRequest{IndexName: "idx", Value: []byte("v")})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if sncErr.Service != "index" {
		t.Fatalf("expected service 'index', got %q", sncErr.Service)
	}
}

func TestKVLookup_IndexScan_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(index.IndexScanRequest{IndexName: "idx", Start: []byte("a"), End: []byte("z")})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_IndexList_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(index.IndexListRequest{})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_IndexQuery_WithManager(t *testing.T) {
	idx := &kvMockIndexManager{}
	k := newTestKVSM(WithKVIndexManager(idx))
	_, err := k.Lookup(index.IndexQueryRequest{IndexName: "idx", Value: []byte("v")})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	// The lookup succeeded without error; mock returns nil slice, which is valid.
}

func TestKVLookup_IndexScan_WithManager(t *testing.T) {
	idx := &kvMockIndexManager{}
	k := newTestKVSM(WithKVIndexManager(idx))
	_, err := k.Lookup(index.IndexScanRequest{IndexName: "idx", Start: []byte("a"), End: []byte("z")})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_IndexList_WithManager(t *testing.T) {
	idx := &kvMockIndexManager{}
	k := newTestKVSM(WithKVIndexManager(idx))
	_, err := k.Lookup(index.IndexListRequest{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// lookupLease: nil-guard branches for pointer and value variants
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// lookupMVCC: nil-guard branches for pointer and value variants
// ---------------------------------------------------------------------------

func TestKVLookup_MVCCGet_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&MVCCGetQuery{Key: []byte("k")})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if sncErr.Service != "mvcc" {
		t.Fatalf("expected service 'mvcc', got %q", sncErr.Service)
	}
}

func TestKVLookup_MVCCGetAtRevision_Value_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 1})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_MVCCGetAtRevision_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 1})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_MVCCRange_Value_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(MVCCRangeQuery{Start: []byte("a"), End: []byte("z")})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_MVCCRange_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&MVCCRangeQuery{Start: []byte("a"), End: []byte("z")})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_MVCCRevision_Value_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(MVCCRevisionQuery{})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_MVCCRevision_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&MVCCRevisionQuery{})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_MVCCGet_Pointer_WithStore(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))
	_, err := k.Lookup(&MVCCGetQuery{Key: []byte("k")})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !mvcc.rangeCalled {
		t.Fatal("expected MVCCStore.Range to be called")
	}
}

func TestKVLookup_MVCCGetAtRevision_Pointer_WithStore(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))
	_, err := k.Lookup(&MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 5})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_MVCCRange_Pointer_WithStore(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))
	_, err := k.Lookup(&MVCCRangeQuery{Start: []byte("a"), End: []byte("z"), Limit: 10})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_MVCCRevision_Value_WithStore(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))
	_, err := k.Lookup(MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_MVCCRevision_Pointer_WithStore(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))
	_, err := k.Lookup(&MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// lookupTimeSeries: nil-guard branches for all query type variants
// ---------------------------------------------------------------------------

var (
	tsStart = time.Now().Add(-time.Hour)
	tsEnd   = time.Now()
)

func TestKVLookup_TSQueryRequest_Value_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(timeseries.TSQueryRequest{Metric: "cpu", Start: tsStart, End: tsEnd})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if sncErr.Service != "timeseries" {
		t.Fatalf("expected service 'timeseries', got %q", sncErr.Service)
	}
}

func TestKVLookup_TSQueryRequest_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&timeseries.TSQueryRequest{Metric: "cpu", Start: tsStart, End: tsEnd})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSAggregateRequest_Value_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(timeseries.TSAggregateRequest{Metric: "cpu", Start: tsStart, End: tsEnd})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSAggregateRequest_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&timeseries.TSAggregateRequest{Metric: "cpu", Start: tsStart, End: tsEnd})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSRichQueryRequest_Value_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(timeseries.TSRichQueryRequest{})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSRichQueryRequest_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&timeseries.TSRichQueryRequest{})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSGetRetentionRequest_Value_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(timeseries.TSGetRetentionRequest{Metric: "cpu"})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSGetRetentionRequest_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&timeseries.TSGetRetentionRequest{Metric: "cpu"})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSListMetricsRequest_Value_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(timeseries.TSListMetricsRequest{Prefix: "cpu", Limit: 10})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSListMetricsRequest_Pointer_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(&timeseries.TSListMetricsRequest{Prefix: "cpu", Limit: 10})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVLookup_TSQueryRequest_Value_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(timeseries.TSQueryRequest{Metric: "cpu", Start: tsStart, End: tsEnd})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSQueryRequest_Pointer_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(&timeseries.TSQueryRequest{Metric: "cpu", Start: tsStart, End: tsEnd})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSAggregateRequest_Value_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(timeseries.TSAggregateRequest{Metric: "cpu", Start: tsStart, End: tsEnd})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSAggregateRequest_Pointer_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(&timeseries.TSAggregateRequest{Metric: "cpu", Start: tsStart, End: tsEnd})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSRichQueryRequest_Value_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(timeseries.TSRichQueryRequest{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSRichQueryRequest_Pointer_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(&timeseries.TSRichQueryRequest{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSGetRetentionRequest_Value_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(timeseries.TSGetRetentionRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSGetRetentionRequest_Pointer_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(&timeseries.TSGetRetentionRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSListMetricsRequest_Value_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(timeseries.TSListMetricsRequest{Prefix: "cpu", Limit: 5})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestKVLookup_TSListMetricsRequest_Pointer_WithStore(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))
	_, err := k.Lookup(&timeseries.TSListMetricsRequest{Prefix: "cpu", Limit: 5})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}
