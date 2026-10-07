//go:build qrdb_ts

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

package main

import (
	"context"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TimeSeriesBinding exposes time-series operations to the Wails frontend.
type TimeSeriesBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewTimeSeriesBinding creates a new TimeSeriesBinding backed by the
// given connection.
func NewTimeSeriesBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *TimeSeriesBinding {
	return &TimeSeriesBinding{logger: log, conn: conn}
}

// Write stores a single time-series data point.
func (b *TimeSeriesBinding) Write(point *transport.TSDataPoint) error {
	b.logger.Debug("Write called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Write failed", "error", err)
		return err
	}
	if err := cl.TimeSeries().TSWrite(context.Background(), point); err != nil {
		b.logger.Error("Write failed", "error", err)
		return err
	}
	b.logger.Debug("Write completed")
	return nil
}

// WriteBatch stores multiple time-series data points atomically.
func (b *TimeSeriesBinding) WriteBatch(points []transport.TSDataPoint) error {
	b.logger.Debug("WriteBatch called", "count", len(points))
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("WriteBatch failed", "error", err)
		return err
	}
	if err := cl.TimeSeries().TSWriteBatch(context.Background(), points); err != nil {
		b.logger.Error("WriteBatch failed", "error", err)
		return err
	}
	b.logger.Debug("WriteBatch completed", "count", len(points))
	return nil
}

// Query retrieves data points for a metric within a time range.
func (b *TimeSeriesBinding) Query(req *transport.TSQueryRequest) (*transport.TSQueryResponse, error) {
	b.logger.Debug("Query called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Query failed", "error", err)
		return nil, err
	}
	resp, err := cl.TimeSeries().TSQuery(context.Background(), req)
	if err != nil {
		b.logger.Error("Query failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Query completed")
	return resp, nil
}

// Aggregate computes an aggregation over data points in a time range.
func (b *TimeSeriesBinding) Aggregate(req *transport.TSAggregateRequest) (*transport.TSAggregateResponse, error) {
	b.logger.Debug("Aggregate called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Aggregate failed", "error", err)
		return nil, err
	}
	resp, err := cl.TimeSeries().TSAggregate(context.Background(), req)
	if err != nil {
		b.logger.Error("Aggregate failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Aggregate completed")
	return resp, nil
}

// WritePoint stores a single validated data point.
func (b *TimeSeriesBinding) WritePoint(point *transport.TSDataPoint) error {
	b.logger.Debug("WritePoint called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("WritePoint failed", "error", err)
		return err
	}
	if err := cl.TimeSeries().TSWritePoint(context.Background(), point); err != nil {
		b.logger.Error("WritePoint failed", "error", err)
		return err
	}
	b.logger.Debug("WritePoint completed")
	return nil
}

// RichQuery performs a rich query with aggregation, downsampling, and grouping.
func (b *TimeSeriesBinding) RichQuery(req *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	b.logger.Debug("RichQuery called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RichQuery failed", "error", err)
		return nil, err
	}
	resp, err := cl.TimeSeries().TSRichQuery(context.Background(), req)
	if err != nil {
		b.logger.Error("RichQuery failed", "error", err)
		return nil, err
	}
	b.logger.Debug("RichQuery completed")
	return resp, nil
}

// DeleteSeries deletes time-series data matching the criteria.
func (b *TimeSeriesBinding) DeleteSeries(req *transport.TSDeleteRequest) (*transport.TSDeleteResponse, error) {
	b.logger.Debug("DeleteSeries called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("DeleteSeries failed", "error", err)
		return nil, err
	}
	resp, err := cl.TimeSeries().TSDeleteSeries(context.Background(), req)
	if err != nil {
		b.logger.Error("DeleteSeries failed", "error", err)
		return nil, err
	}
	b.logger.Debug("DeleteSeries completed")
	return resp, nil
}

// GetRetention retrieves the retention policy for a metric.
func (b *TimeSeriesBinding) GetRetention(req *transport.TSGetRetentionRequest) (*transport.TSGetRetentionResponse, error) {
	b.logger.Debug("GetRetention called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetRetention failed", "error", err)
		return nil, err
	}
	resp, err := cl.TimeSeries().TSGetRetention(context.Background(), req)
	if err != nil {
		b.logger.Error("GetRetention failed", "error", err)
		return nil, err
	}
	b.logger.Debug("GetRetention completed")
	return resp, nil
}

// SetRetention sets the retention policy for a metric.
func (b *TimeSeriesBinding) SetRetention(req *transport.TSSetRetentionRequest) error {
	b.logger.Debug("SetRetention called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("SetRetention failed", "error", err)
		return err
	}
	if err := cl.TimeSeries().TSSetRetention(context.Background(), req); err != nil {
		b.logger.Error("SetRetention failed", "error", err)
		return err
	}
	b.logger.Debug("SetRetention completed")
	return nil
}

// ListMetrics lists metric names matching an optional prefix.
func (b *TimeSeriesBinding) ListMetrics(req *transport.TSListMetricsRequest) (*transport.TSListMetricsResponse, error) {
	b.logger.Debug("ListMetrics called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ListMetrics failed", "error", err)
		return nil, err
	}
	resp, err := cl.TimeSeries().TSListMetrics(context.Background(), req)
	if err != nil {
		b.logger.Error("ListMetrics failed", "error", err)
		return nil, err
	}
	b.logger.Debug("ListMetrics completed")
	return resp, nil
}
