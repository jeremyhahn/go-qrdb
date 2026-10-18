//go:build !integration

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

package statemachine

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Helpers: apply a raw command with a specific op byte + truncated payload.
// The Update method strips byte 0 (the opcode) and passes the rest as
// payload to the handler. A truncated payload triggers the decode error
// branch inside the handler.
// ---------------------------------------------------------------------------

func applyTruncated(t *testing.T, update func(context.Context, []sm.Entry, []sm.Result) error, opcode byte) {
	t.Helper()
	cmd := []byte{opcode, 0x01} // 1-byte payload: always too short for any decoder
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := update(context.Background(), entries, results)
	if err == nil {
		t.Fatalf("expected IncompleteDataError for opcode 0x%02x, got nil", opcode)
	}
	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError for opcode 0x%02x, got %T: %v", opcode, err, err)
	}
}

// ---------------------------------------------------------------------------
// RegularStateMachine: decode error tests
// ---------------------------------------------------------------------------

func TestRegularStateMachine_MVCCPut_DecodeError(t *testing.T) {
	rsm := newRegularWithMVCC(&mockMVCCStore{})
	applyTruncated(t, rsm.Update, protocol.OpMVCCPut)
}

func TestRegularStateMachine_MVCCDelete_DecodeError(t *testing.T) {
	rsm := newRegularWithMVCC(&mockMVCCStore{})
	applyTruncated(t, rsm.Update, protocol.OpMVCCDelete)
}

func TestRegularStateMachine_MVCCRange_DecodeError(t *testing.T) {
	rsm := newRegularWithMVCC(&mockMVCCStore{})
	applyTruncated(t, rsm.Update, protocol.OpMVCCRange)
}

func TestRegularStateMachine_Transaction_DecodeError(t *testing.T) {
	rsm := newRegularWithMVCC(&mockMVCCStore{})
	applyTruncated(t, rsm.Update, protocol.OpTransaction)
}

func TestRegularStateMachine_LeaseGrant_DecodeError(t *testing.T) {
	rsm := newRegularWithLease(&mockLeaseManager{})
	applyTruncated(t, rsm.Update, protocol.OpLeaseGrant)
}

func TestRegularStateMachine_LeaseRevoke_DecodeError(t *testing.T) {
	rsm := newRegularWithLease(&mockLeaseManager{})
	applyTruncated(t, rsm.Update, protocol.OpLeaseRevoke)
}

func TestRegularStateMachine_LeaseKeepAlive_DecodeError(t *testing.T) {
	rsm := newRegularWithLease(&mockLeaseManager{})
	applyTruncated(t, rsm.Update, protocol.OpLeaseKeepAlive)
}

func TestRegularStateMachine_IndexCreate_DecodeError(t *testing.T) {
	rsm := newRegularWithIndex(&mockIndexManager{})
	applyTruncated(t, rsm.Update, protocol.OpIndexCreate)
}

func TestRegularStateMachine_IndexDrop_DecodeError(t *testing.T) {
	rsm := newRegularWithIndex(&mockIndexManager{})
	applyTruncated(t, rsm.Update, protocol.OpIndexDrop)
}

func TestRegularStateMachine_TSWrite_DecodeError(t *testing.T) {
	rsm := newRegularWithTS(&mockTimeSeriesStore{})
	applyTruncated(t, rsm.Update, protocol.OpTSWrite)
}

func TestRegularStateMachine_TSWriteBatch_DecodeError(t *testing.T) {
	rsm := newRegularWithTS(&mockTimeSeriesStore{})
	applyTruncated(t, rsm.Update, protocol.OpTSWriteBatch)
}

func TestRegularStateMachine_TSWritePoint_DecodeError(t *testing.T) {
	rsm := newRegularWithTS(&mockTimeSeriesStore{})
	applyTruncated(t, rsm.Update, protocol.OpTSWritePoint)
}

func TestRegularStateMachine_TSDeleteSeries_DecodeError(t *testing.T) {
	rsm := newRegularWithTS(&mockTimeSeriesStore{})
	applyTruncated(t, rsm.Update, protocol.OpTSDeleteSeries)
}

func TestRegularStateMachine_TSSetRetention_DecodeError(t *testing.T) {
	rsm := newRegularWithTS(&mockTimeSeriesStore{})
	applyTruncated(t, rsm.Update, protocol.OpTSSetRetention)
}

// ---------------------------------------------------------------------------
// FastStateMachine: decode error tests for MVCC/Transaction/TSWritePoint
// (Lease, Index, TS other ops already covered in fast_*_test.go)
// ---------------------------------------------------------------------------

func TestFSM_MVCCPut_DecodeError(t *testing.T) {
	ms := &mockMVCCStore{}
	fsm, _ := newTestFSM(WithMVCCStore(ms))
	applyTruncated(t, fsm.Update, protocol.OpMVCCPut)
}

func TestFSM_MVCCDelete_DecodeError(t *testing.T) {
	ms := &mockMVCCStore{}
	fsm, _ := newTestFSM(WithMVCCStore(ms))
	applyTruncated(t, fsm.Update, protocol.OpMVCCDelete)
}

func TestFSM_MVCCRange_DecodeError(t *testing.T) {
	ms := &mockMVCCStore{}
	fsm, _ := newTestFSM(WithMVCCStore(ms))
	applyTruncated(t, fsm.Update, protocol.OpMVCCRange)
}

func TestFSM_Transaction_DecodeError(t *testing.T) {
	ms := &mockMVCCStore{}
	fsm, _ := newTestFSM(WithMVCCStore(ms))
	applyTruncated(t, fsm.Update, protocol.OpTransaction)
}

func TestFSM_TSWritePoint_DecodeError(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	fsm, _ := newTestFSMWithTS(ts)
	applyTruncated(t, fsm.Update, protocol.OpTSWritePoint)
}

// ---------------------------------------------------------------------------
// RegularStateMachine: lookup not-configured tests for Index, Lease, TS
// ---------------------------------------------------------------------------

func TestRegularStateMachine_LookupIndex_NotConfigured(t *testing.T) {
	rsm := NewRegularStateMachine() // no index manager

	queries := []interface{}{
		index.IndexQueryRequest{IndexName: "x", Value: []byte("v")},
		index.IndexScanRequest{IndexName: "x", Start: []byte("a"), End: []byte("z")},
		index.IndexListRequest{},
	}

	for _, q := range queries {
		_, err := rsm.Lookup(context.Background(), q)
		if err == nil {
			t.Fatalf("expected ServiceNotConfiguredError for %T, got nil", q)
		}
		var svcErr *ServiceNotConfiguredError
		if !errors.As(err, &svcErr) {
			t.Fatalf("expected ServiceNotConfiguredError for %T, got %T: %v", q, err, err)
		}
		if svcErr.Service != "index" {
			t.Fatalf("expected service 'index', got %q", svcErr.Service)
		}
	}
}

func TestRegularStateMachine_LookupLease_NotConfigured(t *testing.T) {
	rsm := NewRegularStateMachine() // no lease manager

	queries := []interface{}{
		&lease.LeaseTimeToLiveQuery{ID: 1},
		lease.LeaseTimeToLiveQuery{ID: 1},
		&lease.LeasesQuery{},
		lease.LeasesQuery{},
	}

	for _, q := range queries {
		_, err := rsm.Lookup(context.Background(), q)
		if err == nil {
			t.Fatalf("expected ServiceNotConfiguredError for %T, got nil", q)
		}
		var svcErr *ServiceNotConfiguredError
		if !errors.As(err, &svcErr) {
			t.Fatalf("expected ServiceNotConfiguredError for %T, got %T: %v", q, err, err)
		}
		if svcErr.Service != "lease" {
			t.Fatalf("expected service 'lease', got %q", svcErr.Service)
		}
	}
}

func TestRegularStateMachine_LookupTimeSeries_NotConfigured(t *testing.T) {
	rsm := NewRegularStateMachine() // no ts store

	queries := []interface{}{
		timeseries.TSQueryRequest{Metric: "cpu"},
		&timeseries.TSQueryRequest{Metric: "cpu"},
		timeseries.TSAggregateRequest{Metric: "cpu"},
		&timeseries.TSAggregateRequest{Metric: "cpu"},
		timeseries.TSRichQueryRequest{},
		&timeseries.TSRichQueryRequest{},
		timeseries.TSGetRetentionRequest{Metric: "cpu"},
		&timeseries.TSGetRetentionRequest{Metric: "cpu"},
		timeseries.TSListMetricsRequest{Prefix: "c"},
		&timeseries.TSListMetricsRequest{Prefix: "c"},
	}

	for _, q := range queries {
		_, err := rsm.Lookup(context.Background(), q)
		if err == nil {
			t.Fatalf("expected ServiceNotConfiguredError for %T, got nil", q)
		}
		var svcErr *ServiceNotConfiguredError
		if !errors.As(err, &svcErr) {
			t.Fatalf("expected ServiceNotConfiguredError for %T, got %T: %v", q, err, err)
		}
		if svcErr.Service != "timeseries" {
			t.Fatalf("expected service 'timeseries', got %q", svcErr.Service)
		}
	}
}
