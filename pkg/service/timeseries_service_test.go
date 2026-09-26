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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// NewTimeSeriesService
// ---------------------------------------------------------------------------

func TestTimeSeriesService_NewTimeSeriesService_ReturnsNonNil(t *testing.T) {
	svc := NewTimeSeriesService(&mockHost{}, 1)
	if svc == nil {
		t.Fatal("expected non-nil TimeSeriesService")
	}
}

func TestTimeSeriesService_NewTimeSeriesService_PreservesShardID(t *testing.T) {
	var capturedShardID uint64
	host := &mockHost{
		syncProposeFn: func(_ context.Context, shardID uint64, _ []byte) (sm.Result, error) {
			capturedShardID = shardID
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewTimeSeriesService(host, 99)
	err := svc.Write(context.Background(), "cpu", 42.0, time.Now(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedShardID != 99 {
		t.Fatalf("expected shardID 99, got %d", capturedShardID)
	}
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

func TestTimeSeriesService_Write(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	err := svc.Write(context.Background(), "cpu.usage", 42.5, time.Now(), map[string]string{"host": "node1"})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestTimeSeriesService_Write_EncodesCommand(t *testing.T) {
	var capturedCmd []byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	err := svc.Write(context.Background(), "mem.usage", 75.0, time.Now(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedCmd) == 0 {
		t.Fatal("expected non-empty command")
	}
	// First byte should be OpTSWrite (60).
	if capturedCmd[0] != 60 {
		t.Fatalf("expected op code 60, got %d", capturedCmd[0])
	}
}

// ---------------------------------------------------------------------------
// Write: NotLeader
// ---------------------------------------------------------------------------

func TestTimeSeriesService_Write_NotLeader(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       5,
				LeaderID:      3,
				LeaderAddress: "10.0.0.3:9100",
			}
		},
	}

	svc := NewTimeSeriesService(host, 5)
	err := svc.Write(context.Background(), "cpu", 42.0, time.Now(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 5 {
		t.Fatalf("expected ShardID 5, got %d", nle.ShardID)
	}
	if nle.LeaderID != 3 {
		t.Fatalf("expected LeaderID 3, got %d", nle.LeaderID)
	}
	if nle.LeaderAddress != "10.0.0.3:9100" {
		t.Fatalf("expected LeaderAddress 10.0.0.3:9100, got %s", nle.LeaderAddress)
	}
}

func TestTimeSeriesService_Write_ProposeError(t *testing.T) {
	sentinel := errors.New("raft timeout")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewTimeSeriesService(host, 1)
	err := svc.Write(context.Background(), "cpu", 42.0, time.Now(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// WriteBatch
// ---------------------------------------------------------------------------

func TestTimeSeriesService_WriteBatch(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 3}, nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	now := time.Now()
	points := []DataPoint{
		{Metric: "disk.io", Value: 100.0, Timestamp: now, Tags: map[string]string{"host": "node1"}},
		{Metric: "disk.io", Value: 200.0, Timestamp: now.Add(time.Second), Tags: map[string]string{"host": "node1"}},
		{Metric: "disk.io", Value: 300.0, Timestamp: now.Add(2 * time.Second), Tags: map[string]string{"host": "node1"}},
	}

	err := svc.WriteBatch(context.Background(), points)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestTimeSeriesService_WriteBatch_EncodesCommand(t *testing.T) {
	var capturedCmd []byte
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			capturedCmd = make([]byte, len(cmd))
			copy(capturedCmd, cmd)
			return sm.Result{Value: 1}, nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	points := []DataPoint{
		{Metric: "net.rx", Value: 50.0, Timestamp: time.Now(), Tags: nil},
	}

	err := svc.WriteBatch(context.Background(), points)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedCmd) == 0 {
		t.Fatal("expected non-empty command")
	}
	// First byte should be OpTSWriteBatch (61).
	if capturedCmd[0] != 61 {
		t.Fatalf("expected op code 61, got %d", capturedCmd[0])
	}
}

// ---------------------------------------------------------------------------
// WriteBatch: ProposeError
// ---------------------------------------------------------------------------

func TestTimeSeriesService_WriteBatch_ProposeError(t *testing.T) {
	sentinel := errors.New("proposal dropped")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	svc := NewTimeSeriesService(host, 1)
	points := []DataPoint{
		{Metric: "cpu", Value: 42.0, Timestamp: time.Now(), Tags: nil},
	}

	err := svc.WriteBatch(context.Background(), points)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

func TestTimeSeriesService_WriteBatch_NotLeader(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{
				ShardID:       8,
				LeaderID:      1,
				LeaderAddress: "10.0.0.1:9100",
			}
		},
	}

	svc := NewTimeSeriesService(host, 8)
	points := []DataPoint{
		{Metric: "cpu", Value: 42.0, Timestamp: time.Now(), Tags: nil},
	}

	err := svc.WriteBatch(context.Background(), points)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 8 {
		t.Fatalf("expected ShardID 8, got %d", nle.ShardID)
	}
}

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------

func TestTimeSeriesService_Query(t *testing.T) {
	now := time.Now()
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(TSQueryRequest)
			if !ok {
				t.Fatalf("expected TSQueryRequest, got %T", query)
			}
			if q.Metric != "cpu.usage" {
				t.Fatalf("expected metric cpu.usage, got %s", q.Metric)
			}
			return []DataPoint{
				{Metric: "cpu.usage", Value: 42.5, Timestamp: now, Tags: map[string]string{"host": "node1"}},
				{Metric: "cpu.usage", Value: 55.0, Timestamp: now.Add(time.Second), Tags: map[string]string{"host": "node1"}},
			}, nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	points, err := svc.Query(context.Background(), "cpu.usage", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 data points, got %d", len(points))
	}
	if points[0].Value != 42.5 {
		t.Fatalf("expected first point value 42.5, got %f", points[0].Value)
	}
	if points[1].Value != 55.0 {
		t.Fatalf("expected second point value 55.0, got %f", points[1].Value)
	}
}

func TestTimeSeriesService_Query_EmptyResult(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []DataPoint{}, nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	points, err := svc.Query(context.Background(), "cpu", time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected 0 data points, got %d", len(points))
	}
}

// ---------------------------------------------------------------------------
// Query: ReadError
// ---------------------------------------------------------------------------

func TestTimeSeriesService_Query_ReadError(t *testing.T) {
	sentinel := errors.New("state machine error")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewTimeSeriesService(host, 1)
	_, err := svc.Query(context.Background(), "cpu", time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

func TestTimeSeriesService_Query_NotLeader(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       3,
				LeaderID:      5,
				LeaderAddress: "10.0.0.5:9100",
			}
		},
	}

	svc := NewTimeSeriesService(host, 3)
	_, err := svc.Query(context.Background(), "cpu", time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 3 {
		t.Fatalf("expected ShardID 3, got %d", nle.ShardID)
	}
}

// ---------------------------------------------------------------------------
// Query: UnexpectedResultType
// ---------------------------------------------------------------------------

func TestTimeSeriesService_Query_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not-a-slice", nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	_, err := svc.Query(context.Background(), "cpu", time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var urte *unexpectedResultTypeError
	if !errors.As(err, &urte) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Aggregate
// ---------------------------------------------------------------------------

func TestTimeSeriesService_Aggregate(t *testing.T) {
	now := time.Now()
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			q, ok := query.(TSAggregateRequest)
			if !ok {
				t.Fatalf("expected TSAggregateRequest, got %T", query)
			}
			if q.Metric != "latency" {
				t.Fatalf("expected metric latency, got %s", q.Metric)
			}
			if q.Func != AggAvg {
				t.Fatalf("expected AggAvg, got %d", q.Func)
			}
			if q.Interval != time.Minute {
				t.Fatalf("expected interval 1m, got %v", q.Interval)
			}
			return []AggResult{
				{Timestamp: now, Value: 15.0, Count: 2},
				{Timestamp: now.Add(time.Minute), Value: 35.0, Count: 2},
			}, nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	results, err := svc.Aggregate(context.Background(), "latency", now.Add(-time.Minute), now.Add(2*time.Minute), AggAvg, time.Minute)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 agg results, got %d", len(results))
	}
	if results[0].Value != 15.0 {
		t.Fatalf("expected first bucket value 15.0, got %f", results[0].Value)
	}
	if results[1].Value != 35.0 {
		t.Fatalf("expected second bucket value 35.0, got %f", results[1].Value)
	}
}

func TestTimeSeriesService_Aggregate_SingleBucket(t *testing.T) {
	now := time.Now()
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []AggResult{
				{Timestamp: now, Value: 50.0, Count: 3},
			}, nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	results, err := svc.Aggregate(context.Background(), "requests", now.Add(-time.Hour), now, AggSum, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Value != 50.0 {
		t.Fatalf("expected value 50.0, got %f", results[0].Value)
	}
	if results[0].Count != 3 {
		t.Fatalf("expected count 3, got %d", results[0].Count)
	}
}

// ---------------------------------------------------------------------------
// Aggregate: NotLeader
// ---------------------------------------------------------------------------

func TestTimeSeriesService_Aggregate_NotLeader(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{
				ShardID:       6,
				LeaderID:      2,
				LeaderAddress: "10.0.0.2:9100",
			}
		},
	}

	svc := NewTimeSeriesService(host, 6)
	_, err := svc.Aggregate(context.Background(), "cpu", time.Now().Add(-time.Hour), time.Now(), AggAvg, time.Minute)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 6 {
		t.Fatalf("expected ShardID 6, got %d", nle.ShardID)
	}
	if nle.LeaderID != 2 {
		t.Fatalf("expected LeaderID 2, got %d", nle.LeaderID)
	}
}

func TestTimeSeriesService_Aggregate_ReadError(t *testing.T) {
	sentinel := errors.New("aggregate failed")
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, sentinel
		},
	}

	svc := NewTimeSeriesService(host, 1)
	_, err := svc.Aggregate(context.Background(), "cpu", time.Now().Add(-time.Hour), time.Now(), AggAvg, time.Minute)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// Aggregate: UnexpectedResultType
// ---------------------------------------------------------------------------

func TestTimeSeriesService_Aggregate_UnexpectedResultType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "not-a-slice", nil
		},
	}

	svc := NewTimeSeriesService(host, 1)
	_, err := svc.Aggregate(context.Background(), "cpu", time.Now().Add(-time.Hour), time.Now(), AggAvg, time.Minute)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var urte *unexpectedResultTypeError
	if !errors.As(err, &urte) {
		t.Fatalf("expected *unexpectedResultTypeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// translateProposeError
// ---------------------------------------------------------------------------

func TestTimeSeriesService_translateProposeError_NotLeader(t *testing.T) {

	err := translateProposeError(&quicraft.NotLeaderError{
		ShardID:       10,
		LeaderID:      2,
		LeaderAddress: "192.168.0.2:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 10 {
		t.Fatalf("expected ShardID 10, got %d", nle.ShardID)
	}
}

func TestTimeSeriesService_translateProposeError_GenericError(t *testing.T) {
	sentinel := errors.New("generic failure")

	err := translateProposeError(sentinel)

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", pe.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// translateReadError
// ---------------------------------------------------------------------------

func TestTimeSeriesService_translateReadError_NotLeader(t *testing.T) {
	svc := &TimeSeriesService{host: &mockHost{}, shardID: 1}

	err := svc.translateReadError(&quicraft.NotLeaderError{
		ShardID:       12,
		LeaderID:      6,
		LeaderAddress: "192.168.0.6:9100",
	})

	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 12 {
		t.Fatalf("expected ShardID 12, got %d", nle.ShardID)
	}
}

func TestTimeSeriesService_translateReadError_GenericError(t *testing.T) {
	svc := &TimeSeriesService{host: &mockHost{}, shardID: 1}
	sentinel := errors.New("read failure")

	err := svc.translateReadError(sentinel)

	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
	if !errors.Is(re.Unwrap(), sentinel) {
		t.Fatalf("expected underlying sentinel, got %v", re.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// WritePoint
// ---------------------------------------------------------------------------

func TestTimeSeriesService_WritePoint(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			if cmd[0] != 62 { // OpTSWritePoint
				t.Errorf("opcode = 0x%02x, want 0x3e", cmd[0])
			}
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	err := svc.WritePoint(context.Background(), &DataPoint{
		Metric: "cpu.usage", Value: 42.5, Timestamp: time.Now(),
		Tags: map[string]string{"host": "node1"},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestTimeSeriesService_WritePoint_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("raft down")
		},
	}
	svc := NewTimeSeriesService(host, 1)
	err := svc.WritePoint(context.Background(), &DataPoint{
		Metric: "cpu", Value: 1.0, Timestamp: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTimeSeriesService_WritePoint_NotLeader(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{}
		},
	}
	svc := NewTimeSeriesService(host, 1)
	err := svc.WritePoint(context.Background(), &DataPoint{
		Metric: "cpu", Value: 1.0, Timestamp: time.Now(),
	})
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// QueryRich
// ---------------------------------------------------------------------------

func TestTimeSeriesService_QueryRich(t *testing.T) {
	expected := &QueryResult{Metric: "cpu.usage"}
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return expected, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	result, err := svc.QueryRich(context.Background(), &QueryRequest{
		Metric: "cpu.usage", StartTime: time.Now().Add(-time.Hour), EndTime: time.Now(),
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result.Metric != "cpu.usage" {
		t.Errorf("Metric = %q, want %q", result.Metric, "cpu.usage")
	}
}

func TestTimeSeriesService_QueryRich_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("read failed")
		},
	}
	svc := NewTimeSeriesService(host, 1)
	_, err := svc.QueryRich(context.Background(), &QueryRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTimeSeriesService_QueryRich_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "wrong type", nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	_, err := svc.QueryRich(context.Background(), &QueryRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected unexpectedResultTypeError")
	}
}

// ---------------------------------------------------------------------------
// DeleteSeries
// ---------------------------------------------------------------------------

func TestTimeSeriesService_DeleteSeries(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			if cmd[0] != 63 { // OpTSDeleteSeries
				t.Errorf("opcode = 0x%02x, want 0x3f", cmd[0])
			}
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	now := time.Now()
	err := svc.DeleteSeries(context.Background(), &DeleteRequest{
		Metric: "cpu", StartTime: now.Add(-time.Hour), EndTime: now,
		Tags: map[string]string{"host": "node1"},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestTimeSeriesService_DeleteSeries_ZeroTimes(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	err := svc.DeleteSeries(context.Background(), &DeleteRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestTimeSeriesService_DeleteSeries_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("propose failed")
		},
	}
	svc := NewTimeSeriesService(host, 1)
	err := svc.DeleteSeries(context.Background(), &DeleteRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// GetRetentionPolicy
// ---------------------------------------------------------------------------

func TestTimeSeriesService_GetRetentionPolicy(t *testing.T) {
	expected := &RetentionPolicy{Duration: 24 * time.Hour}
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return expected, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	policy, err := svc.GetRetentionPolicy("cpu.usage")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if policy.Duration != 24*time.Hour {
		t.Errorf("Duration = %v, want 24h", policy.Duration)
	}
}

func TestTimeSeriesService_GetRetentionPolicy_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("read failed")
		},
	}
	svc := NewTimeSeriesService(host, 1)
	_, err := svc.GetRetentionPolicy("cpu")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTimeSeriesService_GetRetentionPolicy_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return 42, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	_, err := svc.GetRetentionPolicy("cpu")
	if err == nil {
		t.Fatal("expected unexpectedResultTypeError")
	}
}

// ---------------------------------------------------------------------------
// SetRetentionPolicy
// ---------------------------------------------------------------------------

func TestTimeSeriesService_SetRetentionPolicy(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			if cmd[0] != 64 { // OpTSSetRetention
				t.Errorf("opcode = 0x%02x, want 0x40", cmd[0])
			}
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	err := svc.SetRetentionPolicy("cpu.usage", &RetentionPolicy{
		Duration: 7 * 24 * time.Hour, Resolution: time.Minute, Aggregation: 1,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestTimeSeriesService_SetRetentionPolicy_ProposeError(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, errors.New("propose failed")
		},
	}
	svc := NewTimeSeriesService(host, 1)
	err := svc.SetRetentionPolicy("cpu", &RetentionPolicy{Duration: time.Hour})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ListMetrics
// ---------------------------------------------------------------------------

func TestTimeSeriesService_ListMetrics(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []string{"cpu.usage", "mem.free", "disk.io"}, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	metrics, err := svc.ListMetrics("cpu", 10)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(metrics) != 3 {
		t.Fatalf("expected 3 metrics, got %d", len(metrics))
	}
}

func TestTimeSeriesService_ListMetrics_ReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("read failed")
		},
	}
	svc := NewTimeSeriesService(host, 1)
	_, err := svc.ListMetrics("", 0)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTimeSeriesService_ListMetrics_UnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return 123, nil
		},
	}
	svc := NewTimeSeriesService(host, 1)
	_, err := svc.ListMetrics("", 0)
	if err == nil {
		t.Fatal("expected unexpectedResultTypeError")
	}
}
