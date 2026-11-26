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

//go:build qrdb_ts

package main

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newTestTSBinding creates a TimeSeriesBinding backed by an embedded
// transport with the mock time-series service.
func newTestTSBinding(t *testing.T) *TimeSeriesBinding {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tsSvc := &mockTimeSeriesService{}
	trans, err := embedded.New(
		kvSvc,
		adminSvc,
		embedded.WithTimeSeriesService(tsSvc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(trans)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	conn.SetClient(cl)
	return NewTimeSeriesBinding(log, conn)
}

// newTSBindingNoConn returns a TimeSeriesBinding with no active client.
func newTSBindingNoConn(t *testing.T) *TimeSeriesBinding {
	t.Helper()
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	return NewTimeSeriesBinding(log, conn)
}

// TestTSBinding_ScopesToTheSelectedDatabase checks that every time-series
// binding resolves its client from the database argument, as the KV bindings
// do: the default database is served by the connection's own client, and a
// named database goes through DatabaseScoped (which this test transport
// refuses), never silently to the default database.
func TestTSBinding_ScopesToTheSelectedDatabase(t *testing.T) {
	b := newTestTSBinding(t)

	if err := b.Write("", &transport.TSDataPoint{Metric: "cpu", Value: 1}); err != nil {
		t.Fatalf("Write(default): %v", err)
	}

	calls := map[string]func() error{
		"Write":      func() error { return b.Write("metrics", &transport.TSDataPoint{Metric: "cpu", Value: 1}) },
		"WriteBatch": func() error { return b.WriteBatch("metrics", []transport.TSDataPoint{{Metric: "cpu", Value: 1}}) },
		"WritePoint": func() error { return b.WritePoint("metrics", &transport.TSDataPoint{Metric: "cpu", Value: 1}) },
		"Query": func() error {
			_, err := b.Query("metrics", &transport.TSQueryRequest{Metric: "cpu"})
			return err
		},
		"Aggregate": func() error {
			_, err := b.Aggregate("metrics", &transport.TSAggregateRequest{Metric: "cpu"})
			return err
		},
		"RichQuery": func() error {
			_, err := b.RichQuery("metrics", &transport.TSRichQueryRequest{})
			return err
		},
		"DeleteSeries": func() error {
			_, err := b.DeleteSeries("metrics", &transport.TSDeleteRequest{Metric: "cpu"})
			return err
		},
		"GetRetention": func() error {
			_, err := b.GetRetention("metrics", &transport.TSGetRetentionRequest{Metric: "cpu"})
			return err
		},
		"SetRetention": func() error { return b.SetRetention("metrics", &transport.TSSetRetentionRequest{Metric: "cpu"}) },
		"ListMetrics": func() error {
			_, err := b.ListMetrics("metrics", &transport.TSListMetricsRequest{})
			return err
		},
	}
	var unsupported *transport.DatabaseScopeUnsupportedError
	for name, call := range calls {
		if err := call(); !errors.As(err, &unsupported) {
			t.Errorf("%s(metrics): want *transport.DatabaseScopeUnsupportedError, got %T: %v", name, err, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

func TestTSBinding_Write_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	err := b.Write("", &transport.TSDataPoint{})
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_Write_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	err := b.Write("", &transport.TSDataPoint{
		Metric: "cpu.usage",
		Value:  75.5,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WriteBatch
// ---------------------------------------------------------------------------

func TestTSBinding_WriteBatch_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	err := b.WriteBatch("", nil)
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_WriteBatch_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	points := []transport.TSDataPoint{
		{Metric: "cpu.usage", Value: 42.0},
		{Metric: "mem.used", Value: 1024.0},
	}
	err := b.WriteBatch("", points)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------

func TestTSBinding_Query_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	resp, err := b.Query("", &transport.TSQueryRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_Query_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	resp, err := b.Query("", &transport.TSQueryRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// Aggregate
// ---------------------------------------------------------------------------

func TestTSBinding_Aggregate_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	resp, err := b.Aggregate("", &transport.TSAggregateRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_Aggregate_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	resp, err := b.Aggregate("", &transport.TSAggregateRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// WritePoint
// ---------------------------------------------------------------------------

func TestTSBinding_WritePoint_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	err := b.WritePoint("", &transport.TSDataPoint{})
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_WritePoint_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	err := b.WritePoint("", &transport.TSDataPoint{
		Metric: "cpu.usage",
		Value:  99.0,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RichQuery
// ---------------------------------------------------------------------------

func TestTSBinding_RichQuery_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	resp, err := b.RichQuery("", &transport.TSRichQueryRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_RichQuery_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	resp, err := b.RichQuery("", &transport.TSRichQueryRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// DeleteSeries
// ---------------------------------------------------------------------------

func TestTSBinding_DeleteSeries_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	resp, err := b.DeleteSeries("", &transport.TSDeleteRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_DeleteSeries_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	resp, err := b.DeleteSeries("", &transport.TSDeleteRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// GetRetention
// ---------------------------------------------------------------------------

func TestTSBinding_GetRetention_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	resp, err := b.GetRetention("", &transport.TSGetRetentionRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_GetRetention_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	resp, err := b.GetRetention("", &transport.TSGetRetentionRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// ---------------------------------------------------------------------------
// SetRetention
// ---------------------------------------------------------------------------

func TestTSBinding_SetRetention_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	err := b.SetRetention("", &transport.TSSetRetentionRequest{})
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_SetRetention_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	err := b.SetRetention("", &transport.TSSetRetentionRequest{
		Metric: "cpu.usage",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListMetrics
// ---------------------------------------------------------------------------

func TestTSBinding_ListMetrics_NoClient(t *testing.T) {
	b := newTSBindingNoConn(t)
	resp, err := b.ListMetrics("", &transport.TSListMetricsRequest{})
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestTSBinding_ListMetrics_Connected(t *testing.T) {
	b := newTestTSBinding(t)
	resp, err := b.ListMetrics("", &transport.TSListMetricsRequest{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}
