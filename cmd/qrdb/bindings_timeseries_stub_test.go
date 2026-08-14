//go:build !qrdb_ts && !integration

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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newTSStubBinding creates a TimeSeriesBinding backed by a no-connection
// ConnectionBinding (the stub always returns FeatureDisabledError).
func newTSStubBinding(t *testing.T) *TimeSeriesBinding {
	t.Helper()
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	return NewTimeSeriesBinding(log, conn)
}

func assertTSFeatureDisabled(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected FeatureDisabledError, got nil")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
}

func TestTSStub_NewTimeSeriesBinding(t *testing.T) {
	b := newTSStubBinding(t)
	if b == nil {
		t.Fatal("expected non-nil TimeSeriesBinding")
	}
}

func TestTSStub_Write(t *testing.T) {
	b := newTSStubBinding(t)
	assertTSFeatureDisabled(t, b.Write(&transport.TSDataPoint{}))
}

func TestTSStub_WriteBatch(t *testing.T) {
	b := newTSStubBinding(t)
	assertTSFeatureDisabled(t, b.WriteBatch(nil))
}

func TestTSStub_Query(t *testing.T) {
	b := newTSStubBinding(t)
	_, err := b.Query(&transport.TSQueryRequest{})
	assertTSFeatureDisabled(t, err)
}

func TestTSStub_Aggregate(t *testing.T) {
	b := newTSStubBinding(t)
	_, err := b.Aggregate(&transport.TSAggregateRequest{})
	assertTSFeatureDisabled(t, err)
}

func TestTSStub_WritePoint(t *testing.T) {
	b := newTSStubBinding(t)
	assertTSFeatureDisabled(t, b.WritePoint(&transport.TSDataPoint{}))
}

func TestTSStub_RichQuery(t *testing.T) {
	b := newTSStubBinding(t)
	_, err := b.RichQuery(&transport.TSRichQueryRequest{})
	assertTSFeatureDisabled(t, err)
}

func TestTSStub_DeleteSeries(t *testing.T) {
	b := newTSStubBinding(t)
	_, err := b.DeleteSeries(&transport.TSDeleteRequest{})
	assertTSFeatureDisabled(t, err)
}

func TestTSStub_GetRetention(t *testing.T) {
	b := newTSStubBinding(t)
	_, err := b.GetRetention(&transport.TSGetRetentionRequest{})
	assertTSFeatureDisabled(t, err)
}

func TestTSStub_SetRetention(t *testing.T) {
	b := newTSStubBinding(t)
	assertTSFeatureDisabled(t, b.SetRetention(&transport.TSSetRetentionRequest{}))
}

func TestTSStub_ListMetrics(t *testing.T) {
	b := newTSStubBinding(t)
	_, err := b.ListMetrics(&transport.TSListMetricsRequest{})
	assertTSFeatureDisabled(t, err)
}
