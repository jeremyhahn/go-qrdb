//go:build !qrdb_ts

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
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TimeSeriesBinding is a stub that returns FeatureDisabledError for all
// methods when the time-series feature is not compiled in.
type TimeSeriesBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewTimeSeriesBinding creates a new TimeSeriesBinding backed by the
// given connection.
func NewTimeSeriesBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *TimeSeriesBinding {
	return &TimeSeriesBinding{logger: log, conn: conn}
}

// Write returns a FeatureDisabledError.
func (b *TimeSeriesBinding) Write(_ *transport.TSDataPoint) error {
	return &FeatureDisabledError{Feature: "time-series"}
}

// WriteBatch returns a FeatureDisabledError.
func (b *TimeSeriesBinding) WriteBatch(_ []transport.TSDataPoint) error {
	return &FeatureDisabledError{Feature: "time-series"}
}

// Query returns a FeatureDisabledError.
func (b *TimeSeriesBinding) Query(_ *transport.TSQueryRequest) (*transport.TSQueryResponse, error) {
	return nil, &FeatureDisabledError{Feature: "time-series"}
}

// Aggregate returns a FeatureDisabledError.
func (b *TimeSeriesBinding) Aggregate(_ *transport.TSAggregateRequest) (*transport.TSAggregateResponse, error) {
	return nil, &FeatureDisabledError{Feature: "time-series"}
}

// WritePoint returns a FeatureDisabledError.
func (b *TimeSeriesBinding) WritePoint(_ *transport.TSDataPoint) error {
	return &FeatureDisabledError{Feature: "time-series"}
}

// RichQuery returns a FeatureDisabledError.
func (b *TimeSeriesBinding) RichQuery(_ *transport.TSRichQueryRequest) (*transport.TSRichQueryResponse, error) {
	return nil, &FeatureDisabledError{Feature: "time-series"}
}

// DeleteSeries returns a FeatureDisabledError.
func (b *TimeSeriesBinding) DeleteSeries(_ *transport.TSDeleteRequest) (*transport.TSDeleteResponse, error) {
	return nil, &FeatureDisabledError{Feature: "time-series"}
}

// GetRetention returns a FeatureDisabledError.
func (b *TimeSeriesBinding) GetRetention(_ *transport.TSGetRetentionRequest) (*transport.TSGetRetentionResponse, error) {
	return nil, &FeatureDisabledError{Feature: "time-series"}
}

// SetRetention returns a FeatureDisabledError.
func (b *TimeSeriesBinding) SetRetention(_ *transport.TSSetRetentionRequest) error {
	return &FeatureDisabledError{Feature: "time-series"}
}

// ListMetrics returns a FeatureDisabledError.
func (b *TimeSeriesBinding) ListMetrics(_ *transport.TSListMetricsRequest) (*transport.TSListMetricsResponse, error) {
	return nil, &FeatureDisabledError{Feature: "time-series"}
}
