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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// retentionTSService returns a fixed retention policy for TSGetRetention.
type retentionTSService struct {
	mockTimeSeriesService
	policy *timeseries.RetentionPolicy
}

func (m *retentionTSService) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return m.policy, nil
}

// requireUnimplemented fails t unless err is a gRPC Unimplemented status.
func requireUnimplemented(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSWritePoint_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithTimeSeries(&mockTimeSeriesService{}))
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{
			Metric:      "cpu",
			Value:       55.0,
			TimestampNs: time.Now().UnixNano(),
		},
	})
	if err != nil {
		t.Fatalf("TSWritePoint failed: %v", err)
	}
}

func TestGRPC_TSWritePoint_NilPoint(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithTimeSeries(&mockTimeSeriesService{}))
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{})
	if err == nil {
		t.Fatal("expected error for nil point, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_TSDeleteSeries_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithTimeSeries(&mockTimeSeriesService{}))
	defer cleanup()

	_, err := client.TSDeleteSeries(context.Background(), &pb.TSDeleteSeriesRequest{
		Metric:  "cpu",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSDeleteSeries failed: %v", err)
	}
}

func TestGRPC_TSGetRetention_Success(t *testing.T) {
	tsSvc := &retentionTSService{
		policy: &timeseries.RetentionPolicy{
			Duration:    time.Hour * 24 * 7,
			Resolution:  time.Minute,
			Aggregation: timeseries.AggAvg,
		},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSGetRetention(context.Background(), &pb.TSGetRetentionRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("TSGetRetention failed: %v", err)
	}
	if got, want := resp.GetDurationNs(), int64(tsSvc.policy.Duration); got != want {
		t.Fatalf("duration_ns = %d, want %d", got, want)
	}
	if got, want := resp.GetResolutionNs(), int64(tsSvc.policy.Resolution); got != want {
		t.Fatalf("resolution_ns = %d, want %d", got, want)
	}
}

func TestGRPC_TSGetRetention_Error(t *testing.T) {
	tsSvc := &errorTSService{inner: &mockTimeSeriesService{}, retentionErr: errors.New("not found")}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSGetRetention(context.Background(), &pb.TSGetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() == codes.OK || st.Code() == codes.Unknown {
		t.Fatalf("expected a mapped status code, got %s", st.Code())
	}
}

func TestGRPC_TSSetRetention_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithTimeSeries(&mockTimeSeriesService{}))
	defer cleanup()

	_, err := client.TSSetRetention(context.Background(), &pb.TSSetRetentionRequest{
		Metric:     "cpu",
		DurationNs: int64(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("TSSetRetention failed: %v", err)
	}
}

// TestGRPC_TS_NilService checks that every timeseries RPC answers
// Unimplemented when the server has no timeseries service.
func TestGRPC_TS_NilService(t *testing.T) {
	calls := map[string]func(pb.QRDBServiceClient) error{
		"TSWritePoint": func(c pb.QRDBServiceClient) error {
			_, err := c.TSWritePoint(context.Background(), &pb.TSWriteRequest{
				Point: &pb.TSDataPoint{Metric: "cpu", Value: 1.0, TimestampNs: 1000},
			})
			return err
		},
		"TSRichQuery": func(c pb.QRDBServiceClient) error {
			_, err := c.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{Metric: "cpu"})
			return err
		},
		"TSDeleteSeries": func(c pb.QRDBServiceClient) error {
			_, err := c.TSDeleteSeries(context.Background(), &pb.TSDeleteSeriesRequest{Metric: "cpu"})
			return err
		},
		"TSGetRetention": func(c pb.QRDBServiceClient) error {
			_, err := c.TSGetRetention(context.Background(), &pb.TSGetRetentionRequest{Metric: "cpu"})
			return err
		},
		"TSSetRetention": func(c pb.QRDBServiceClient) error {
			_, err := c.TSSetRetention(context.Background(), &pb.TSSetRetentionRequest{Metric: "cpu"})
			return err
		},
		"TSListMetrics": func(c pb.QRDBServiceClient) error {
			_, err := c.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{})
			return err
		},
	}
	client, cleanup := startTestServerFull(t)
	defer cleanup()
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			requireUnimplemented(t, call(client))
		})
	}
}
