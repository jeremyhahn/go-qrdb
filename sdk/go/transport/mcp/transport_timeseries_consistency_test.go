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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// TSQuery: consistency routing through MCP transport
// ---------------------------------------------------------------------------

func TestSDKTSQuery_NoConsistency_UsesPlainMethod(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}

	svc.mu.Lock()
	qCalled := svc.lastQueryConsistency
	svc.mu.Unlock()

	if qCalled != nil {
		t.Error("expected plain Query (lastQueryConsistency should be nil)")
	}
}

func TestSDKTSQuery_WithConsistency_RoutesToConsistencyMethod(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "strong",
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}

	svc.mu.Lock()
	captured := svc.lastQueryConsistency
	svc.mu.Unlock()

	if captured == nil {
		t.Fatal("expected QueryWithConsistency to be called (lastQueryConsistency is nil)")
	}
	if captured.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", captured.Consistency)
	}
}

func TestSDKTSQuery_EventualConsistency_SetsAllowStale(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSQuery(context.Background(), &transport.TSQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSQuery: %v", err)
	}

	svc.mu.Lock()
	captured := svc.lastQueryConsistency
	svc.mu.Unlock()

	if captured == nil {
		t.Fatal("expected QueryWithConsistency to be called")
	}
	if !captured.AllowStale {
		t.Error("AllowStale should be true for eventual consistency")
	}
}

// ---------------------------------------------------------------------------
// TSAggregate: consistency routing through MCP transport
// ---------------------------------------------------------------------------

func TestSDKTSAggregate_NoConsistency_UsesPlainMethod(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    now.Add(-time.Hour).UnixNano(),
		EndNs:      now.UnixNano(),
		Function:   transport.AggAvg,
		IntervalNs: 60_000_000_000,
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}

	svc.mu.Lock()
	captured := svc.lastAggConsistency
	svc.mu.Unlock()

	if captured != nil {
		t.Error("expected plain Aggregate (lastAggConsistency should be nil)")
	}
}

func TestSDKTSAggregate_WithConsistency_RoutesToConsistencyMethod(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Function:    transport.AggAvg,
		IntervalNs:  60_000_000_000,
		Consistency: "linearizable",
	})
	if err != nil {
		t.Fatalf("TSAggregate: %v", err)
	}

	svc.mu.Lock()
	captured := svc.lastAggConsistency
	svc.mu.Unlock()

	if captured == nil {
		t.Fatal("expected AggregateWithConsistency to be called")
	}
	if captured.Consistency != consistency.Linearizable {
		t.Errorf("consistency = %v, want Linearizable", captured.Consistency)
	}
}

// ---------------------------------------------------------------------------
// TSRichQuery: consistency routing through MCP transport
// ---------------------------------------------------------------------------

func TestSDKTSRichQuery_NoConsistency_UsesPlainMethod(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}

	svc.mu.Lock()
	captured := svc.lastRichQueryConsistency
	svc.mu.Unlock()

	if captured != nil {
		t.Error("expected plain QueryRich (lastRichQueryConsistency should be nil)")
	}
}

func TestSDKTSRichQuery_WithConsistency_RoutesToConsistencyMethod(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	now := time.Now().UTC()
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{
		Metric:      "cpu",
		StartNs:     now.Add(-time.Hour).UnixNano(),
		EndNs:       now.UnixNano(),
		Consistency: "eventual",
	})
	if err != nil {
		t.Fatalf("TSRichQuery: %v", err)
	}

	svc.mu.Lock()
	captured := svc.lastRichQueryConsistency
	svc.mu.Unlock()

	if captured == nil {
		t.Fatal("expected QueryRichWithConsistency to be called")
	}
	if captured.Consistency != consistency.Eventual {
		t.Errorf("consistency = %v, want Eventual", captured.Consistency)
	}
}

// ---------------------------------------------------------------------------
// TSListMetrics: consistency routing through MCP transport
// ---------------------------------------------------------------------------

func TestSDKTSListMetrics_NoConsistency_UsesPlainMethod(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "cpu",
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}

	svc.mu.Lock()
	captured := svc.lastListConsistency
	svc.mu.Unlock()

	if captured != nil {
		t.Error("expected plain ListMetrics (lastListConsistency should be nil)")
	}
}

func TestSDKTSListMetrics_WithConsistency_RoutesToConsistencyMethod(t *testing.T) {
	svc := newMockTSSvc()
	tr, cleanup := connectTSInProcess(t, svc)
	defer cleanup()

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix:      "cpu",
		Consistency: "strong",
	})
	if err != nil {
		t.Fatalf("TSListMetrics: %v", err)
	}

	svc.mu.Lock()
	captured := svc.lastListConsistency
	svc.mu.Unlock()

	if captured == nil {
		t.Fatal("expected ListMetricsWithConsistency to be called")
	}
	if captured.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", captured.Consistency)
	}
}
